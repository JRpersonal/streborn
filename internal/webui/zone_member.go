package webui

import (
	"context"
	"net/http"
	"strings"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// Taking ONE speaker out of a saved permanent group.
//
// Until now the only control a saved group had was the x on its own frame,
// which deletes the whole thing. That gap became visible when the stereo
// section started telling people why it could not pair: "remove two of them
// from their group first, then pair them" - advice nothing in the app could
// carry out. An owner of six SoundTouch 10s, all of them in two saved groups,
// was told to do something that did not exist (#938).
//
// A saved group is only a document. Its master is idle and its members report
// nothing, so there is no firmware zone to take apart and no speaker to wake:
// this edits the stored membership and nothing else. That is why it is a
// separate endpoint rather than a flag on the dissolve, which drives the
// firmware and would wake the group to change it.
//
// DELETE /api/box/zone/member?ip=<addr>        (or ?deviceID=<id>)
//
// The last member is the end of the group: a group of one is not a group, so
// the document is cleared rather than left as a master with nobody in it.
func (s *Server) handleZoneMember(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	if s.zones == nil {
		http.Error(w, "no zone store", http.StatusServiceUnavailable)
		return
	}
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	devID := strings.TrimSpace(r.URL.Query().Get("deviceID"))
	if ip == "" && devID == "" {
		http.Error(w, "name the member to remove: ?ip= or ?deviceID=", http.StatusBadRequest)
		return
	}
	doc, ok := s.zones.Get()
	if !ok {
		http.Error(w, "this speaker has no saved group", http.StatusNotFound)
		return
	}
	kept, removed := withoutMember(doc.Slaves, ip, devID)
	if removed == nil {
		http.Error(w, "that speaker is not in this group", http.StatusNotFound)
		return
	}

	// Same serial lock every membership change takes, so this cannot interleave
	// with a form or a dissolve driving the firmware.
	s.zoneFormSeq.Add(1)
	s.zoneFormSerial.Lock()
	defer s.zoneFormSerial.Unlock()

	emptied := len(kept) == 0
	if emptied {
		if err := s.zones.Clear(); err != nil {
			s.logger.Warn("zone: clearing the group after its last member left failed", "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.logger.Info("zone: the saved group's last member was removed, so the group is gone",
			"master", doc.Master, "removed", removed.IP)
	} else {
		doc.Slaves = kept
		if err := s.zones.Set(doc); err != nil {
			s.logger.Warn("zone: saving the group without that member failed", "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.logger.Info("zone: a speaker was taken out of the saved group",
			"master", doc.Master, "removed", removed.IP, "remaining", len(kept))
	}

	// The speaker that left keeps its own copy of the document, and a member
	// that still names this master drags the group back (#342). Best-effort and
	// in the background, exactly as a dissolve does it.
	if removed.IP != "" {
		s.purgePeerZones(
			boxapi.ZoneMember{DeviceID: doc.Master, IP: s.boxHost},
			[]boxapi.ZoneMember{{DeviceID: removed.DeviceID, IP: removed.IP}},
		)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "removed": removed.IP, "remaining": len(kept), "groupGone": emptied,
	})
}

// withoutMember splits a member list on the one the caller named, matching on
// address or deviceID because a saved member is reliably known by only one of
// the two: a two-chip speaker announces a different deviceID over discovery
// than the one the firmware lists for it, so the address is what a saved group
// can always be trusted to carry.
func withoutMember(in []zones.Member, ip, devID string) (kept []zones.Member, removed *zones.Member) {
	ip = strings.TrimSpace(ip)
	devID = strings.TrimSpace(devID)
	for i := range in {
		m := in[i]
		hit := (ip != "" && strings.EqualFold(strings.TrimSpace(m.IP), ip)) ||
			(devID != "" && strings.EqualFold(strings.TrimSpace(m.DeviceID), devID))
		if hit && removed == nil {
			cp := m
			removed = &cp
			continue
		}
		kept = append(kept, m)
	}
	return kept, removed
}

// zoneMemberCount answers how many members the saved group has, for the tests
// and for callers that want to know before they ask.
func (s *Server) zoneMemberCount(_ context.Context) int {
	if s.zones == nil {
		return 0
	}
	doc, ok := s.zones.Get()
	if !ok {
		return 0
	}
	return len(doc.Slaves)
}
