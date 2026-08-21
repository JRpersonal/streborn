// zonetemplates_http.go: the group-template endpoints (beta). Templates are
// named speaker constellations stored on the MASTER's NAND, so the desktop
// app and the phone remote see the same list; activation drives the full
// member list through the shared zone drive (one coalesced drive, incremental
// when the zone already exists).

package webui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zonetemplates"
)

// handleZoneTemplates serves the collection: GET lists, POST saves (upsert).
func (s *Server) handleZoneTemplates(w http.ResponseWriter, r *http.Request) {
	if s.tpls == nil {
		http.Error(w, "templates store not initialized", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleZoneTemplatesList(w, r)
	case http.MethodPost:
		s.handleZoneTemplateSave(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleZoneTemplateItem serves one template: DELETE /{id}, POST
// /{id}/activate, POST /{id}/permanent.
func (s *Server) handleZoneTemplateItem(w http.ResponseWriter, r *http.Request) {
	if s.tpls == nil {
		http.Error(w, "templates store not initialized", http.StatusServiceUnavailable)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/box/zone/templates/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" {
		http.Error(w, "template id required", http.StatusBadRequest)
		return
	}
	switch {
	case r.Method == http.MethodDelete && action == "":
		s.handleZoneTemplateDelete(w, id)
	case r.Method == http.MethodPost && action == "activate":
		s.handleZoneTemplateActivate(w, r, id)
	case r.Method == http.MethodPost && action == "permanent":
		s.handleZoneTemplatePermanent(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleZoneTemplatesList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	permanentID := ""
	if p, ok := s.tpls.Permanent(); ok {
		permanentID = p.ID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"master": map[string]string{
			"deviceID": s.localDeviceID(ctx, boxapi.New(s.boxHost), ""),
		},
		"templates":   s.tpls.List(),
		"permanentId": permanentID,
		"out":         s.tpls.OutList(),
	})
}

type zoneTemplateSaveReq struct {
	ID      string                 `json:"id"`
	Name    string                 `json:"name"`
	Members []zonetemplates.Member `json:"members"`
}

func (s *Server) handleZoneTemplateSave(w http.ResponseWriter, r *http.Request) {
	var req zoneTemplateSaveReq
	if !decodeJSONRequest(w, r, 16*1024, &req) {
		return
	}
	// The agent later dials the member IPs, so refuse anything that is not a
	// LAN peer before it lands on NAND (same guard the peer seed uses).
	for _, m := range req.Members {
		if m.IP != "" && !isLANPeer(m.IP) {
			http.Error(w, "member ip is not a LAN address: "+m.IP, http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	// The template's master is always THIS box (templates live on the master
	// they belong to), resolved from the local firmware, never trusted from
	// the caller. Members get the same wlan0-MAC correction the zone form
	// applies, so a template saved from discovery data holds the deviceIDs
	// the firmware actually keys on. Master.IP is a hint only; every client
	// already talks to this agent at an address it knows.
	masterID := s.localDeviceID(ctx, boxapi.New(s.boxHost), "")
	masterIP := ""
	if s.zones != nil {
		if z, ok := s.zones.Get(); ok && strings.EqualFold(strings.TrimSpace(z.Master), strings.TrimSpace(masterID)) {
			masterIP = z.MasterIP
		}
	}
	corrected := make([]boxapi.ZoneMember, 0, len(req.Members))
	for _, m := range req.Members {
		corrected = append(corrected, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
	}
	corrected = s.correctZoneMemberIDs(ctx, corrected)
	members := make([]zonetemplates.Member, 0, len(corrected))
	for _, m := range corrected {
		members = append(members, zonetemplates.Member{DeviceID: m.DeviceID, IP: m.IP})
	}
	tpl, err := s.tpls.Upsert(zonetemplates.Template{
		ID:      req.ID,
		Name:    req.Name,
		Master:  zonetemplates.Member{DeviceID: masterID, IP: masterIP},
		Members: members,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.logger.Info("zone templates: template saved (beta)", "name", tpl.Name, "members", len(tpl.Members))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "template": tpl})
}

func (s *Server) handleZoneTemplateDelete(w http.ResponseWriter, id string) {
	removedPermanent, err := s.tpls.Delete(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if removedPermanent {
		s.logger.Info("zone templates: the permanent group was deleted with its template (beta)", "id", id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removedPermanent": removedPermanent})
}

type zoneTemplatePermanentReq struct {
	Permanent bool `json:"permanent"`
}

func (s *Server) handleZoneTemplatePermanent(w http.ResponseWriter, r *http.Request, id string) {
	var req zoneTemplatePermanentReq
	if !decodeJSONRequest(w, r, 1024, &req) {
		return
	}
	if err := s.tpls.SetPermanent(id, req.Permanent); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if req.Permanent {
		s.logger.Info("zone templates: permanent group enabled (beta)", "id", id)
	} else {
		s.logger.Info("zone templates: permanent group disabled (beta)", "id", id)
	}
	permanentID := ""
	if p, ok := s.tpls.Permanent(); ok {
		permanentID = p.ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "permanentId": permanentID})
}

// handleZoneTemplateActivate forms the template's group in one drive. A user
// action: it coalesces, persists, wakes the master, and captures the resume,
// exactly like a manual form, so both UIs can interpret the response with the
// code they already have. Explicit activation also clears the
// deliberately-out list: the user just asked for the whole constellation.
func (s *Server) handleZoneTemplateActivate(w http.ResponseWriter, r *http.Request, id string) {
	tpl, ok := s.tpls.Get(id)
	if !ok {
		http.Error(w, "no such template", http.StatusNotFound)
		return
	}
	if err := s.tpls.ClearOut(); err != nil {
		s.logger.Warn("zone templates: clearing the out list failed", "err", err)
	}
	ctx, cancel := context.WithTimeout(r.Context(), zoneFormBudget(len(tpl.Members)))
	defer cancel()
	c := boxapi.New(s.boxHost)
	master := boxapi.ZoneMember{DeviceID: s.localDeviceID(ctx, c, tpl.Master.DeviceID), IP: tpl.Master.IP}
	slaves := make([]boxapi.ZoneMember, 0, len(tpl.Members))
	for _, m := range tpl.Members {
		slaves = append(slaves, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
	}
	slaves = s.correctZoneMemberIDs(ctx, slaves)
	s.logger.Info("zone templates: activating template (beta)", "name", tpl.Name, "members", len(slaves))
	res := s.driveZone(ctx, master, slaves, tpl.Name, "native", zoneDriveOpts{coalesce: true, persist: true, resume: true, wake: true, reason: "activate"})
	res.write(w)
}
