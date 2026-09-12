package webui

import (
	"net/http"
	"time"

	"github.com/JRpersonal/streborn/internal/alarm"
)

// alarmView is what GET and PUT answer with: the stored document plus a
// read-only status block. The block is not part of the document and is ignored
// on the way in.
type alarmView struct {
	Zone   string        `json:"zone"`
	Alarms []alarm.Alarm `json:"alarms"`
	Status alarmStatus   `json:"status"`
}

// alarmStatus is what the editor needs to tell the user the truth about what
// the SPEAKER thinks, rather than what the phone assumes.
type alarmStatus struct {
	// ClockTrusted is false while the speaker's clock is implausible, when
	// nothing will fire. The single most valuable line in the editor.
	ClockTrusted bool `json:"clockTrusted"`
	// ZoneResolved is false when the stored zone no longer resolves, when
	// nothing is scheduled at all.
	ZoneResolved bool `json:"zoneResolved"`
	// NextFire proves the speaker agrees with the phone about when the next
	// alarm goes off, including its zone and its clock. Empty when nothing is
	// scheduled.
	NextFire    string             `json:"nextFire,omitempty"`
	NextAlarmID string             `json:"nextAlarmId,omitempty"`
	LastFire    *alarm.FireOutcome `json:"lastFire,omitempty"`
}

// handleAlarms serves the per-speaker alarm clock document.
//
// GET /api/alarms -> the whole document plus the status block.
// PUT /api/alarms <- the whole document; validated, then persisted on NAND.
//
// LAN only, like every other write to the speaker's configuration. The PUT
// replaces the document wholesale (an editor always sends all of it) and is the
// only NAND write this feature makes from a request; the scheduler writes its
// own separate file.
func (s *Server) handleAlarms(w http.ResponseWriter, r *http.Request) {
	if s.alarms == nil {
		http.Error(w, "alarms not configured", http.StatusServiceUnavailable)
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.alarmView())
	case http.MethodPut:
		var d alarm.Document
		if !decodeJSONRequest(w, r, 1<<16, &d) {
			return
		}
		if err := d.Validate(); err != nil {
			// The message reaches the user verbatim.
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.alarms.Set(d); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// An alarm moved earlier has to be picked up now, not at the next
		// evaluation.
		s.KickAlarms()
		s.logger.Info("alarms: saved from an editor", "count", len(d.Alarms), "zone", d.Zone)
		writeJSON(w, http.StatusOK, s.alarmView())
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) alarmView() alarmView {
	doc := s.alarms.Get()
	v := alarmView{Zone: doc.Zone, Alarms: doc.Alarms}
	if v.Alarms == nil {
		v.Alarms = []alarm.Alarm{}
	}
	_, zoneErr := doc.Location()
	v.Status = alarmStatus{
		ClockTrusted: s.AlarmClockTrusted(),
		ZoneResolved: zoneErr == nil,
	}
	if next, which, ok := doc.NextFire(s.alarmNowTime()); ok {
		v.Status.NextFire = next.Format(time.RFC3339)
		v.Status.NextAlarmID = which.ID
	}
	if s.alarmState != nil {
		if last, ok := s.alarmState.LastOutcome(); ok {
			v.Status.LastFire = &last
		}
	}
	return v
}

// AlarmsSnapshot is the debug-section view of the alarm clock.
func (s *Server) AlarmsSnapshot() any {
	if s == nil || s.alarms == nil {
		return nil
	}
	v := s.alarmView()
	return map[string]any{
		"zone":          v.Zone,
		"count":         len(v.Alarms),
		"clock_trusted": v.Status.ClockTrusted,
		"zone_resolved": v.Status.ZoneResolved,
		"next_fire":     v.Status.NextFire,
		"next_alarm_id": v.Status.NextAlarmID,
		"last_fire":     v.Status.LastFire,
		"alarms":        v.Alarms,
	}
}
