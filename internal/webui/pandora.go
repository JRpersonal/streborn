package webui

// Pandora route A (#243): the speaker's OWN Pandora client, opt-in.
//
// GET  /api/pandora            status: opt-in, the registered source (masked),
//                              and the box's own PANDORA entry from /sources
// POST /api/pandora            {"enabled":true|false} switches the opt-in
// POST /api/pandora/account    {"user":"...","password":"..."} hands the account
//                              to the firmware's own local API
// DELETE /api/pandora/account  removes it from the firmware and the stand-in
//
// Everything except GET requires the opt-in for the account calls, a JSON body
// (a plain HTML form from a foreign page cannot send one), and the LAN. The
// password is passed straight to the firmware and never stored or logged here.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/marge"
)

// PandoraBackend is the stand-in's side of the opt-in (implemented by
// *marge.Server).
type PandoraBackend interface {
	PandoraEnabled() bool
	SetPandoraEnabled(on bool) error
	PandoraStatusSnapshot() marge.PandoraStatus
	PandoraUsername() string
	ClearPandora() error
}

// pandoraDisplayName is the display name the firmware's own account flow uses.
const pandoraDisplayName = "Pandora Music Service"

// pandoraBoxAPI is the subset of boxapi the handler needs, so tests can fake it.
type pandoraBoxAPI interface {
	SetMusicServiceAccount(ctx context.Context, source, displayName, user, pass string) error
	RemoveMusicServiceAccount(ctx context.Context, source, displayName, user string) error
	GetSources(ctx context.Context) ([]boxapi.Source, error)
}

func (s *Server) pandoraBox() pandoraBoxAPI {
	if s.pandoraBoxOverride != nil {
		return s.pandoraBoxOverride
	}
	return boxapi.New(s.boxHost)
}

// pandoraBoxSource reads the firmware's own PANDORA entry from /sources.
func (s *Server) pandoraBoxSource(ctx context.Context) map[string]string {
	srcs, err := s.pandoraBox().GetSources(ctx)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	for _, it := range srcs {
		if strings.EqualFold(it.Source, "PANDORA") {
			return map[string]string{"status": it.Status, "displayName": it.DisplayName, "hasAccount": boolString(it.SourceAccount != "")}
		}
	}
	return map[string]string{"status": "absent"}
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// PandoraDebugSnapshot is the diagnostic bundle's "pandora" section.
func (s *Server) PandoraDebugSnapshot() any {
	if s.pandora == nil {
		return map[string]any{"wired": false}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return map[string]any{"standIn": s.pandora.PandoraStatusSnapshot(), "boxSource": s.pandoraBoxSource(ctx)}
}

func (s *Server) handlePandora(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "pandora settings only allowed from LAN", http.StatusForbidden)
		return
	}
	if s.pandora == nil {
		http.Error(w, "not available on this build", http.StatusNotImplemented)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.PandoraDebugSnapshot())
	case http.MethodPost:
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if !decodePandoraJSON(w, r, &req) {
			return
		}
		if req.Enabled == nil {
			http.Error(w, `body needs {"enabled":true|false}`, http.StatusBadRequest)
			return
		}
		if err := s.pandora.SetPandoraEnabled(*req.Enabled); err != nil {
			http.Error(w, "could not write the setting to the speaker", http.StatusInternalServerError)
			return
		}
		s.logger.Info("pandora: route-A opt-in switched", "enabled", *req.Enabled)
		writeJSON(w, http.StatusOK, map[string]any{"optIn": s.pandora.PandoraEnabled()})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePandoraAccount(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "pandora settings only allowed from LAN", http.StatusForbidden)
		return
	}
	if s.pandora == nil {
		http.Error(w, "not available on this build", http.StatusNotImplemented)
		return
	}
	if !s.pandora.PandoraEnabled() {
		http.Error(w, "switch the Pandora test on first (POST /api/pandora {\"enabled\":true})", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	switch r.Method {
	case http.MethodPost:
		var req struct {
			User     string `json:"user"`
			Password string `json:"password"`
		}
		if !decodePandoraJSON(w, r, &req) {
			return
		}
		req.User = strings.TrimSpace(req.User)
		if req.User == "" || req.Password == "" {
			http.Error(w, "user and password are required", http.StatusBadRequest)
			return
		}
		if err := s.pandoraBox().SetMusicServiceAccount(ctx, "PANDORA", pandoraDisplayName, req.User, req.Password); err != nil {
			s.logger.Warn("pandora: the speaker refused the account", "err", err)
			http.Error(w, "the speaker refused the account: "+err.Error(), http.StatusBadGateway)
			return
		}
		s.logger.Info("pandora: account handed to the speaker's own client", "user", maskForLog(req.User))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": "the speaker registers the source with STR next; check GET /api/pandora in a minute"})
	case http.MethodDelete:
		user := s.pandora.PandoraUsername()
		if user != "" {
			if err := s.pandoraBox().RemoveMusicServiceAccount(ctx, "PANDORA", pandoraDisplayName, user); err != nil {
				s.logger.Warn("pandora: the speaker did not confirm the removal", "err", err)
			}
		}
		if err := s.pandora.ClearPandora(); err != nil {
			http.Error(w, "could not clear the stored source", http.StatusInternalServerError)
			return
		}
		s.logger.Info("pandora: account removed")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// decodePandoraJSON enforces a JSON content type and decodes a small body.
func decodePandoraJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "pandora settings need a JSON body", http.StatusUnsupportedMediaType)
		return false
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(dst); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	return true
}

// maskForLog keeps two characters and the domain of an account name.
func maskForLog(u string) string {
	local, domain, hasAt := strings.Cut(u, "@")
	if len(local) > 2 {
		local = local[:2]
	}
	out := local + "***"
	if hasAt {
		out += "@" + domain
	}
	return out
}
