// Group: the stereo-pair (L/R) group record store, its persistence, and the
// marge-side group CRUD the firmware runs during /addGroup and /removeGroup.

package marge

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

// groupRole is one <groupRole> entry inside a stereo-pair group descriptor.
type groupRole struct {
	DeviceID string `xml:"deviceId"`
	Role     string `xml:"role"`
	IP       string `xml:"ipAddress"`
}

// groupRecord mirrors the <group> descriptor the ST10 firmware POSTs to marge
// to create the L/R stereo pair, and the shape the box's own /getGroup returns:
// id as an attribute, name/masterDeviceId as child elements, and the members as
// <roles><groupRole>. Live captured 2026-07-10 from EC24B8B790CC.
type groupRecord struct {
	XMLName        xml.Name    `xml:"group"`
	ID             string      `xml:"id,attr"`
	Name           string      `xml:"name"`
	MasterDeviceID string      `xml:"masterDeviceId"`
	Roles          []groupRole `xml:"roles>groupRole"`
}

// groupCreateFormat selects the shape of the group-create acknowledgement, so
// the response the firmware accepts can be swept on hardware the same way
// addDeviceFormat sweeps the AddDevice reply. Values: "bare201" (default: HTTP
// 201 Created + a bare <group id=...>), "bare200", "wrap201"/"wrap200" (the
// <response status="OK"> envelope the AddDevice path uses). Empty falls back to
// the default.
func groupCreateFormat() string {
	if v := strings.TrimSpace(os.Getenv("STICK_GROUP_CREATE_FORMAT")); v != "" {
		return v
	}
	return "bare201"
}

// margeGroupID derives a stable, non-empty group id from the master device id
// so a create and the follow-up poll echo the same id. The box treats the
// marge group id as opaque (its own /getGroup returns a firmware-assigned id).
func margeGroupID(master string) string {
	m := strings.TrimSpace(master)
	if m == "" {
		m = "stereo"
	}
	return "str-grp-" + m
}

// renderGroupXML renders a group record in the <group id=...> shape the box's
// /getGroup parses, echoing the posted roles back (with ipAddress only when the
// firmware supplied one).
func renderGroupXML(g *groupRecord) string {
	var b strings.Builder
	b.WriteString(`<group id="`)
	b.WriteString(xmlEscapeText(g.ID))
	b.WriteString(`"><name>`)
	b.WriteString(xmlEscapeText(g.Name))
	b.WriteString(`</name><masterDeviceId>`)
	b.WriteString(xmlEscapeText(g.MasterDeviceID))
	b.WriteString(`</masterDeviceId><roles>`)
	for _, role := range g.Roles {
		b.WriteString(`<groupRole><deviceId>`)
		b.WriteString(xmlEscapeText(role.DeviceID))
		b.WriteString(`</deviceId><role>`)
		b.WriteString(xmlEscapeText(role.Role))
		b.WriteString(`</role>`)
		if strings.TrimSpace(role.IP) != "" {
			b.WriteString(`<ipAddress>`)
			b.WriteString(xmlEscapeText(role.IP))
			b.WriteString(`</ipAddress>`)
		}
		b.WriteString(`</groupRole>`)
	}
	b.WriteString(`</roles></group>`)
	return b.String()
}

// persistedGroup is the on-NAND shape of the stored stereo-pair record: the
// group document itself plus whether it is canonical (STR-installed) rather
// than a firmware self-report.
type persistedGroup struct {
	Canonical bool   `json:"canonical"`
	XML       string `json:"xml"`
}

// loadGroup restores the persisted group record at startup. Best-effort: a
// missing or unreadable file simply means no pair.
func (s *Server) loadGroup() {
	if s.groupPath == "" {
		return
	}
	data, err := os.ReadFile(s.groupPath)
	if err != nil {
		return
	}
	var pg persistedGroup
	if err := json.Unmarshal(data, &pg); err != nil {
		s.logger.Warn("marge group: persisted record unreadable, ignoring",
			slog.String("comp", "marge"), slog.String("err", err.Error()))
		return
	}
	var g groupRecord
	if err := xml.Unmarshal([]byte(pg.XML), &g); err != nil {
		s.logger.Warn("marge group: persisted record XML unreadable, ignoring",
			slog.String("comp", "marge"), slog.String("err", err.Error()))
		return
	}
	s.mu.Lock()
	s.group = &g
	s.groupCanonical = pg.Canonical
	s.groupRestored = true
	s.mu.Unlock()
	s.logger.Info("marge group: restored persisted record",
		slog.String("comp", "marge"), slog.String("groupId", g.ID),
		slog.String("master", g.MasterDeviceID), slog.Bool("canonical", pg.Canonical))
}

// persistGroupLocked writes (or removes) the on-NAND copy of the current
// record. Callers hold s.mu. Best-effort: a write failure only costs the
// record an agent restart, so it is logged and swallowed.
func (s *Server) persistGroupLocked() {
	if s.groupPath == "" {
		return
	}
	if s.group == nil {
		if err := os.Remove(s.groupPath); err != nil && !os.IsNotExist(err) {
			s.logger.Warn("marge group: could not remove persisted record",
				slog.String("comp", "marge"), slog.String("err", err.Error()))
		}
		return
	}
	pg := persistedGroup{Canonical: s.groupCanonical, XML: renderGroupXML(s.group)}
	data, err := json.Marshal(pg)
	if err != nil {
		return
	}
	if err := os.WriteFile(s.groupPath, data, 0o644); err != nil {
		s.logger.Warn("marge group: persist failed",
			slog.String("comp", "marge"), slog.String("err", err.Error()))
	}
}

// GroupSnapshot returns the current group document and whether it is the
// canonical (STR-installed) record. ok is false when no pair is stored.
func (s *Server) GroupSnapshot() (xmlDoc string, canonical bool, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.group == nil {
		return "", false, false
	}
	return renderGroupXML(s.group), s.groupCanonical, true
}

// SetCanonicalGroup installs the canonical pair document (from the pairing
// flow on this box, or relayed from the master's agent via the desktop app for
// the partner). From now on firmware posts that disagree on the master are
// answered with this document instead of stored (see createMargeGroup).
func (s *Server) SetCanonicalGroup(xmlDoc string) error {
	var g groupRecord
	if err := xml.Unmarshal([]byte(xmlDoc), &g); err != nil {
		return fmt.Errorf("parse group document: %w", err)
	}
	if strings.TrimSpace(g.MasterDeviceID) == "" || len(g.Roles) != 2 {
		return fmt.Errorf("group document needs a masterDeviceId and exactly two roles (got master=%q roles=%d)", g.MasterDeviceID, len(g.Roles))
	}
	if strings.TrimSpace(g.ID) == "" {
		g.ID = margeGroupID(g.MasterDeviceID)
	}
	s.mu.Lock()
	s.group = &g
	s.groupCanonical = true
	s.groupRestored = false
	s.persistGroupLocked()
	s.mu.Unlock()
	s.logger.Info("marge group: canonical pair document installed",
		slog.String("comp", "marge"), slog.String("groupId", g.ID),
		slog.String("master", g.MasterDeviceID))
	return nil
}

// ClearGroup drops the stored pair record (dissolve, from this box's own flow
// or relayed for the partner). No-op when nothing is stored.
func (s *Server) ClearGroup(reason string) {
	s.mu.Lock()
	existed := s.group != nil
	s.group = nil
	s.groupCanonical = false
	s.groupRestored = false
	s.persistGroupLocked()
	s.mu.Unlock()
	if existed {
		s.logger.Info("marge group: cleared", slog.String("comp", "marge"), slog.String("reason", reason))
	}
}

// GroupRestoredUnconfirmed reports whether the stored record came from NAND
// and no live signal (firmware post, canonical install) has confirmed it this
// run. The agent's post-startup check clears such a record when the firmware
// reports no group: a Bose factory reset wipes the box's own pairing but not
// /mnt/nv/streborn, and a phantom record must not keep answering the group
// poll with a pair that no longer exists.
func (s *Server) GroupRestoredUnconfirmed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.group != nil && s.groupRestored
}

// CanonicalGroupXML renders the canonical stereo-pair document the pairing
// flow installs on BOTH members' marges: master = LEFT, partner = RIGHT, the
// group id derived from the master so every copy agrees.
func CanonicalGroupXML(name, masterID, masterIP, partnerID, partnerIP string) string {
	g := &groupRecord{
		ID:             margeGroupID(masterID),
		Name:           name,
		MasterDeviceID: masterID,
		Roles: []groupRole{
			{DeviceID: masterID, Role: "LEFT", IP: masterIP},
			{DeviceID: partnerID, Role: "RIGHT", IP: partnerIP},
		},
	}
	return renderGroupXML(g)
}

// handleMargeGroup dispatches the stereo-pair group CRUD the firmware runs
// against marge as the cloud half of /addGroup and /removeGroup.
func (s *Server) handleMargeGroup(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost, http.MethodPut:
		s.createMargeGroup(w, r)
	case http.MethodDelete:
		s.deleteMargeGroup(w, r)
	default: // GET/HEAD: the box's "is this device in a group?" poll.
		s.readMargeGroup(w, r)
	}
}

// createMargeGroup answers the firmware's "create this group on marge" POST.
// It stores the record and echoes it back with a server-assigned id, which is
// what unblocks the box's /addGroup (previously HTTP 500 / error 5580).
func (s *Server) createMargeGroup(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var g groupRecord
	if err := xml.Unmarshal(body, &g); err != nil {
		s.logger.Warn("marge group create: could not parse body",
			slog.String("comp", "marge"), slog.String("err", err.Error()))
	}
	if strings.TrimSpace(g.ID) == "" {
		g.ID = margeGroupID(g.MasterDeviceID)
	}
	stored := &g
	s.mu.Lock()
	// While a canonical pair document is installed, NO firmware post replaces
	// it — the record only changes via SetCanonicalGroup/ClearGroup. A post
	// naming a DIFFERENT master is the known self-centered re-create of the
	// RIGHT box (each firmware reports the pair from its own point of view;
	// the real Bose cloud had one shared document); an agreeing post must not
	// replace the record either, or the firmware's own shape would silently
	// become "canonical". Echo the canonical document back in both cases so
	// the firmware adopts the shared view.
	// Any firmware post is live proof a pair still exists on the box side, so
	// a restored record is confirmed either way.
	s.groupRestored = false
	if s.groupCanonical && s.group != nil {
		stored = s.group
		selfCentered := !strings.EqualFold(strings.TrimSpace(g.MasterDeviceID), strings.TrimSpace(stored.MasterDeviceID))
		s.mu.Unlock()
		if selfCentered {
			s.logger.Warn("marge group create: firmware posted a self-centered pair document, answering with the canonical one",
				slog.String("comp", "marge"),
				slog.String("postedMaster", g.MasterDeviceID),
				slog.String("canonicalMaster", stored.MasterDeviceID))
		} else {
			s.logger.Info("marge group create: firmware re-created the pair, keeping the canonical document",
				slog.String("comp", "marge"), slog.String("master", stored.MasterDeviceID))
		}
	} else {
		s.group = stored
		s.persistGroupLocked()
		s.mu.Unlock()
	}

	roles := make([]string, 0, len(stored.Roles))
	for _, role := range stored.Roles {
		roles = append(roles, role.Role+"="+role.DeviceID)
	}
	s.logger.Info("marge group created",
		slog.String("comp", "marge"),
		slog.String("groupId", stored.ID),
		slog.String("master", stored.MasterDeviceID),
		slog.String("roles", strings.Join(roles, ",")),
	)

	status := http.StatusCreated
	if strings.HasSuffix(groupCreateFormat(), "200") {
		status = http.StatusOK
	}
	body = []byte(`<?xml version="1.0" encoding="UTF-8" ?>` + renderGroupXML(stored))
	if strings.HasPrefix(groupCreateFormat(), "wrap") {
		body = []byte(`<?xml version="1.0" encoding="UTF-8" ?><response status="OK">` + renderGroupXML(stored) + `</response>`)
	}
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// readMargeGroup answers the periodic group poll. When a pair exists we return
// it so the box keeps the pair; otherwise we preserve the historical standalone
// behaviour (the box tolerates the account response as "not grouped").
func (s *Server) readMargeGroup(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	g := s.group
	s.mu.RUnlock()
	if g == nil {
		s.respondMargeAccountFull(w, r)
		return
	}
	s.logger.Debug("marge group poll answered from store",
		slog.String("comp", "marge"), slog.String("groupId", g.ID))
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>` + renderGroupXML(g)))
}

// deleteMargeGroup drops the stored pair when the box dissolves it (/removeGroup
// -> the firmware's group DELETE on marge).
func (s *Server) deleteMargeGroup(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	existed := s.group != nil
	s.group = nil
	s.groupCanonical = false
	s.persistGroupLocked()
	s.mu.Unlock()
	s.logger.Info("marge group deleted",
		slog.String("comp", "marge"), slog.Bool("existed", existed))
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?><response status="OK"/>`))
}
