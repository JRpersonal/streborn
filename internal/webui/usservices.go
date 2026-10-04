package webui

// The firmware's own US music services, Pandora and iHeartRadio
// (internal/marge/usservices.go, docs/streaming/us-services.md).
//
// GET  /api/us-services        status: region, what the stand-in serves, the
//                              registered sources (masked), the recent
//                              registrations, and the box's own PANDORA and
//                              IHEART entries from /sources
// POST /api/us-services        {"enabled":true|false} switches the opt-in that
//                              enables both services outside the US
// POST /api/pandora/account    {"user":"...","password":"..."} hands a Pandora
//                              account to the firmware's own local API
// POST /api/iheart/account     the same for iHeartRadio
// DELETE /api/{pandora,iheart}/account removes it from the firmware and the
//                              stand-in
//
// /api/pandora is the older name of /api/us-services and stays an alias.
//
// The account calls need the service enabled (a US speaker, or the opt-in),
// every call except GET needs a JSON body (a plain HTML form from a foreign
// page cannot send one), and all of them the LAN. The password is passed
// straight to the firmware and never stored or logged here.

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

// NativeServicesBackend is the stand-in's side (implemented by *marge.Server).
type NativeServicesBackend interface {
	PandoraEnabled() bool
	SetPandoraEnabled(on bool) error
	NativeServiceEnabled(typ string) bool
	NativeStatusSnapshot() marge.NativeServicesStatus
	NativeUsername(typ string) string
	ClearNative(typ string) error
}

// nativeAccountDisplayName is the display name passed with
// setMusicServiceAccount. PANDORA's is the one the firmware's own account flow
// uses; IHEART's is unverified on hardware (docs/streaming/us-services.md).
var nativeAccountDisplayName = map[string]string{
	"PANDORA": "Pandora Music Service",
	"IHEART":  "iHeartRadio",
}

// nativeBoxAPI is the subset of boxapi the handlers need, so tests can fake it.
type nativeBoxAPI interface {
	SetMusicServiceAccount(ctx context.Context, source, displayName, user, pass string) error
	RemoveMusicServiceAccount(ctx context.Context, source, displayName, user string) error
	GetSources(ctx context.Context) ([]boxapi.Source, error)
}

func (s *Server) nativeBox() nativeBoxAPI {
	if s.nativeBoxOverride != nil {
		return s.nativeBoxOverride
	}
	return boxapi.New(s.boxHost)
}

// nativeBoxSources reads the firmware's own entries for the US services from
// /sources, one request for all of them.
func (s *Server) nativeBoxSources(ctx context.Context) map[string]map[string]string {
	out := map[string]map[string]string{}
	srcs, err := s.nativeBox().GetSources(ctx)
	if err != nil {
		out["error"] = map[string]string{"error": err.Error()}
		return out
	}
	for _, typ := range marge.NativeServiceTypes() {
		out[typ] = map[string]string{"status": "absent"}
		for _, it := range srcs {
			if strings.EqualFold(it.Source, typ) {
				out[typ] = map[string]string{"status": it.Status, "displayName": it.DisplayName, "hasAccount": boolString(it.SourceAccount != "")}
				break
			}
		}
	}
	return out
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// USServicesDebugSnapshot is the diagnostic bundle's "usServices" section.
// Read only when a diagnostic is taken or GET /api/us-services is called.
func (s *Server) USServicesDebugSnapshot() any {
	out := map[string]any{"region": s.regionInfo()}
	if s.native == nil {
		out["wired"] = false
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out["standIn"] = s.native.NativeStatusSnapshot()
	out["boxSources"] = s.nativeBoxSources(ctx)
	return out
}

func (s *Server) handleUSServices(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "music service settings only allowed from LAN", http.StatusForbidden)
		return
	}
	if s.native == nil {
		http.Error(w, "not available on this build", http.StatusNotImplemented)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.USServicesDebugSnapshot())
	case http.MethodPost:
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if !decodeNativeJSON(w, r, &req) {
			return
		}
		if req.Enabled == nil {
			http.Error(w, `body needs {"enabled":true|false}`, http.StatusBadRequest)
			return
		}
		if err := s.native.SetPandoraEnabled(*req.Enabled); err != nil {
			http.Error(w, "could not write the setting to the speaker", http.StatusInternalServerError)
			return
		}
		s.logger.Info("us-services: opt-in switched", "enabled", *req.Enabled)
		writeJSON(w, http.StatusOK, map[string]any{"optIn": s.native.PandoraEnabled()})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleNativeAccount returns the account handler for one service.
func (s *Server) handleNativeAccount(typ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLocalLAN(r.RemoteAddr) {
			http.Error(w, "music service settings only allowed from LAN", http.StatusForbidden)
			return
		}
		if s.native == nil {
			http.Error(w, "not available on this build", http.StatusNotImplemented)
			return
		}
		if !s.native.NativeServiceEnabled(typ) {
			http.Error(w, "this service is offered on US speakers; elsewhere switch the test on first (POST /api/us-services {\"enabled\":true})", http.StatusConflict)
			return
		}
		display := nativeAccountDisplayName[typ]
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		switch r.Method {
		case http.MethodPost:
			var req struct {
				User     string `json:"user"`
				Password string `json:"password"`
			}
			if !decodeNativeJSON(w, r, &req) {
				return
			}
			req.User = strings.TrimSpace(req.User)
			if req.User == "" || req.Password == "" {
				http.Error(w, "user and password are required", http.StatusBadRequest)
				return
			}
			if err := s.nativeBox().SetMusicServiceAccount(ctx, typ, display, req.User, req.Password); err != nil {
				s.logger.Warn("us-services: the speaker refused the account", "service", typ, "err", err)
				http.Error(w, "the speaker refused the account: "+err.Error(), http.StatusBadGateway)
				return
			}
			s.logger.Info("us-services: account handed to the speaker's own client", "service", typ, "user", maskForLog(req.User))
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": "the speaker registers the source with STR next; check GET /api/us-services in a minute"})
		case http.MethodDelete:
			if user := s.native.NativeUsername(typ); user != "" {
				if err := s.nativeBox().RemoveMusicServiceAccount(ctx, typ, display, user); err != nil {
					s.logger.Warn("us-services: the speaker did not confirm the removal", "service", typ, "err", err)
				}
			}
			if err := s.native.ClearNative(typ); err != nil {
				http.Error(w, "could not clear the stored source", http.StatusInternalServerError)
				return
			}
			s.logger.Info("us-services: account removed", "service", typ)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			w.Header().Set("Allow", "POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// decodeNativeJSON enforces a JSON content type and decodes a small body.
func decodeNativeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "music service settings need a JSON body", http.StatusUnsupportedMediaType)
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
