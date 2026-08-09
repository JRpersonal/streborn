package webui

// Group volume from the phone.
//
// The desktop app can form a group, and until now that was also the only place
// it could be used: to switch the group on or to change how loud it plays, a
// user had to walk to the computer and start it. Three people asked for this on
// the same day, one of them putting it plainly: he has to boot the laptop just
// to turn the volume down while listening.
//
// Two things make this work from a speaker's own web page. The membership comes
// from the PERSISTED zone rather than the firmware's live one, because the
// firmware's zone endpoint answers on some chassis and not on others (measured
// across a nine-speaker fleet: only the three rhino ST10s ever answered, healthy
// or not), while the persisted zone is STR's own record and is always there. And
// the volume calls go to each member's BOSE port, which stays reachable between
// speakers even on the series-I boxes whose agent ports block each other.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// zoneMemberVolume is one speaker of the group as the phone page sees it.
type zoneMemberVolume struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	DeviceID string `json:"deviceID,omitempty"`
	Role     string `json:"role,omitempty"`
	IsSelf   bool   `json:"isSelf"`
	IsMaster bool   `json:"isMaster"`
	// Volume is -1 when the speaker did not answer. The page shows the row
	// greyed rather than hiding it: a member that has gone quiet is exactly
	// what the user wants to see.
	Volume int  `json:"volume"`
	Muted  bool `json:"muted"`
}

// handleZoneVolume serves the group's volume state and changes it.
//
//	GET  -> {"grouped":bool,"stereo":bool,"members":[...],"average":N}
//	POST {"value":N}            -> set every member
//	POST {"ip":"...","value":N} -> set one member
func (s *Server) handleZoneVolume(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.zoneVolumeGet(w, r)
	case http.MethodPost:
		s.zoneVolumeSet(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// groupMembers returns this box plus its persisted group, master first. The
// second return is false when the box is standalone.
func (s *Server) groupMembers() ([]zoneMemberVolume, bool, bool) {
	if s.zones == nil {
		return nil, false, false
	}
	z, ok := s.zones.Get()
	if !ok || len(z.Slaves) == 0 {
		return nil, false, false
	}
	// Ask the speaker whether the group is actually there.
	//
	// The stored document exists so a group survives a reboot or a standby and
	// re-forms itself, which is right. But nothing used to check it against the
	// firmware, so when a group went away without STR writing the file (a failed
	// re-form, a firmware that dropped the zone, a factory reset) the file kept
	// insisting and the phone kept drawing a group card for it.
	//
	// Seen on the maintainer's own speakers 2026-08-09: the living room reported
	// itself master of a group with the bathroom, the portable reported itself
	// master of a group with the living room, the bathroom reported nothing, and
	// the living room's own firmware answered <zone /> to all of it. The card was
	// real; the group was not. Pressing play sent audio to a zone that did not
	// exist, which from the sofa looks exactly like "the speaker is broken".
	//
	// A read, never a write: this must not be the reason a speaker stays awake,
	// so it only reports the truth and leaves repairing to the paths that already
	// do it.
	if s.boxHost != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		live, err := boxapi.New(s.boxHost).GetZone(ctx)
		cancel()
		if err == nil && live.Master == "" && len(live.Members) == 0 {
			s.logger.Info("zone: the stored group is not on the speaker any more, reporting standalone",
				"storedMaster", z.Master, "storedSlaves", len(z.Slaves))
			return nil, false, false
		}
	}
	out := []zoneMemberVolume{{
		Name: s.groupSelfName(z), IP: s.boxHost, DeviceID: z.Master,
		IsSelf: true, IsMaster: true, Volume: -1,
	}}
	for _, m := range z.Slaves {
		if m.IP == "" {
			continue // nothing to call without an address
		}
		out = append(out, zoneMemberVolume{
			Name: s.peerName(m), IP: m.IP, DeviceID: m.DeviceID, Role: m.Role, Volume: -1,
		})
	}
	return out, true, z.Stereo
}

// groupSelfName is what this speaker calls itself in the member list. The
// stored zone name is the group's name, not the speaker's, so it is only used
// when nothing better is known.
func (s *Server) groupSelfName(z zones.Zone) string {
	if n := s.remoteDisplayName(); n != "" {
		return n
	}
	if z.Name != "" {
		return z.Name
	}
	return "This speaker"
}

// peerName resolves a member's display name from the peer list when the app has
// seeded one, and falls back to the address. A row labelled 192.168.1.42 is
// still useful; a row labelled nothing is not.
func (s *Server) peerName(m zones.Member) string {
	if s.peersFn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		for _, p := range s.peersFn(ctx) {
			// A peer is identified by its web URL, so compare on the host part
			// of it rather than on a field PeerLink does not carry.
			if u, err := url.Parse(p.URL); err == nil && u.Hostname() == m.IP && p.Name != "" {
				return p.Name
			}
		}
	}
	return m.IP
}

func (s *Server) zoneVolumeGet(w http.ResponseWriter, r *http.Request) {
	members, grouped, stereo := s.groupMembers()
	if !grouped {
		writeJSON(w, http.StatusOK, map[string]any{"grouped": false})
		return
	}

	// Read every member at once. Serially this is one timeout per silent
	// speaker, and the page polls: a single unplugged member would make the
	// whole card feel broken.
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range members {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := boxapi.New(members[i].IP).GetVolume(ctx)
			if err != nil {
				return // stays -1, the page shows it as not answering
			}
			members[i].Volume = v.Actual
			members[i].Muted = v.Muted
		}(i)
	}
	wg.Wait()

	sum, n := 0, 0
	for _, m := range members {
		if m.Volume >= 0 {
			sum += m.Volume
			n++
		}
	}
	avg := -1
	if n > 0 {
		avg = sum / n
	}
	sort.SliceStable(members, func(a, b int) bool { return members[a].IsMaster && !members[b].IsMaster })
	writeJSON(w, http.StatusOK, map[string]any{
		"grouped": true, "stereo": stereo, "members": members, "average": avg,
	})
}

func (s *Server) zoneVolumeSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP    string `json:"ip"`
		Value int    `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024)).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Value < 0 || req.Value > 100 {
		http.Error(w, "value must be 0..100", http.StatusBadRequest)
		return
	}
	members, grouped, _ := s.groupMembers()
	if !grouped {
		http.Error(w, "this speaker is not in a group", http.StatusConflict)
		return
	}
	targets := members
	if req.IP != "" {
		targets = nil
		for _, m := range members {
			if m.IP == req.IP {
				targets = append(targets, m)
			}
		}
		if len(targets) == 0 {
			http.Error(w, "that speaker is not in this group", http.StatusBadRequest)
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 8*time.Second)
	defer cancel()
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed []string
	)
	for _, m := range targets {
		wg.Add(1)
		go func(m zoneMemberVolume) {
			defer wg.Done()
			if err := boxapi.New(m.IP).SetVolume(ctx, req.Value); err != nil {
				mu.Lock()
				failed = append(failed, m.Name)
				mu.Unlock()
				s.logger.Info("group volume: member did not take the change", "member", m.Name, "ip", m.IP, "err", err)
			}
		}(m)
	}
	wg.Wait()

	// Partial success is reported as partial. Silently reporting ok while one
	// speaker stayed loud is the kind of answer that makes people stop
	// believing the control.
	s.logger.Info("group volume set", "value", req.Value, "targets", len(targets), "failed", len(failed))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": len(failed) == 0, "value": req.Value,
		"changed": len(targets) - len(failed), "failed": failed,
	})
}
