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
// GET  /api/pandora/stations, POST /api/pandora/play: the linked account's
//                              stations and starting one (pandorastations.go)
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
	CarriedOverAccount(typ string) string
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
	ListServiceStations(ctx context.Context, source, account string) ([]boxapi.ServiceStation, error)
	SelectContentItem(ctx context.Context, item string) error
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
				if code := nativeLoginErrorCode(err); code != "" {
					// A known firmware limit, not a wrong password: answer with a
					// code the apps turn into a plain sentence. The raw firmware
					// XML stays in the log line above.
					writeJSON(w, http.StatusBadGateway, map[string]any{
						"error": nativeLoginErrorText[code],
						"code":  code,
					})
					return
				}
				http.Error(w, "the speaker refused the account: "+err.Error(), http.StatusBadGateway)
				return
			}
			s.logger.Info("us-services: account handed to the speaker's own client", "service", typ, "user", maskForLog(req.User))
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": "the speaker registers the source with STR next; check GET /api/us-services in a minute"})
		case http.MethodDelete:
			s.removeNativeAccount(ctx, w, typ, display)
		default:
			w.Header().Set("Allow", "POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// codeIHeartProfileIDTooLarge names the firmware's iHeartRadio login failure
// on a newer iHeart account: the speaker's client reads the account's
// profileId with a 32-bit stoi, and newer iHeart profile ids no longer fit,
// so it answers MUSIC_SERVICE_PARSE_ERROR with "Out-of-range error parsing
// number from json string ... Field= [profileId]". The account itself is
// fine; the speaker cannot hold it, and retrying never helps.
const codeIHeartProfileIDTooLarge = "iheart-profileid-too-large"

// nativeLoginErrorText is the English sentence sent along with each code, for
// a client that does not know the code yet.
var nativeLoginErrorText = map[string]string{
	codeIHeartProfileIDTooLarge: "The speaker's built-in iHeartRadio client cannot handle newer iHeart accounts, so this account cannot be added. Nothing is wrong with the account itself.",
}

// nativeLoginErrorCode recognises a known firmware login failure in the
// speaker's answer to setMusicServiceAccount and returns its code, "" for
// anything else.
func nativeLoginErrorCode(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "out-of-range") && strings.Contains(msg, "profileid") {
		return codeIHeartProfileIDTooLarge
	}
	return ""
}

// nativeRemoveTiming is the account removal's clock. Production reads
// productionNativeRemoveTiming; a test installs its own copy on the server
// (Server.nativeRemoveTiming), so no test ever writes a package-level knob
// that a removal of another test still reads.
type nativeRemoveTiming struct {
	// settle is the pause between the re-reads of /sources that
	// confirm a removal, in case the firmware updates the list a moment late.
	settle time.Duration
}

// productionNativeRemoveTiming is the account removal's clock on a real
// speaker.
var productionNativeRemoveTiming = nativeRemoveTiming{
	settle: time.Second,
}

// nativeRemoveTimes is the account removal's clock: the production values
// unless a test installed a faster one.
func (s *Server) nativeRemoveTimes() *nativeRemoveTiming {
	if s.nativeRemoveTiming != nil {
		return s.nativeRemoveTiming
	}
	return &productionNativeRemoveTiming
}

// nativeRemoveRereads is how many times /sources is read to confirm a removal.
const nativeRemoveRereads = 3

// listedNativeAccount returns the account the firmware lists for typ in
// /sources (unmasked, agent side only), "" when it lists none.
func listedNativeAccount(srcs []boxapi.Source, typ string) string {
	for _, it := range srcs {
		if strings.EqualFold(it.Source, typ) && strings.TrimSpace(it.SourceAccount) != "" {
			return strings.TrimSpace(it.SourceAccount)
		}
	}
	return ""
}

// stillLinked reports whether /sources lists typ READY with an account.
func stillLinked(srcs []boxapi.Source, typ string) bool {
	for _, it := range srcs {
		if strings.EqualFold(it.Source, typ) && strings.EqualFold(it.Status, "READY") && strings.TrimSpace(it.SourceAccount) != "" {
			return true
		}
	}
	return false
}

// removeNativeAccount is DELETE /api/{pandora,iheart}/account.
//
// The account to remove is the one STR registered, else the one the firmware
// lists, else the one carried over in the reflect file. A carried-over account
// (linked with the Bose app before STR) has no STR registration, and an
// earlier version sent no removal at all for it while still answering ok
// (#1101). Success is only reported once /sources no longer lists the service
// READY with an account.
func (s *Server) removeNativeAccount(ctx context.Context, w http.ResponseWriter, typ, display string) {
	box := s.nativeBox()
	user := s.native.NativeUsername(typ)
	origin := "registration"
	if user == "" {
		if srcs, err := box.GetSources(ctx); err == nil {
			user, origin = listedNativeAccount(srcs, typ), "sources"
		} else {
			s.logger.Warn("us-services: could not read the speaker's sources before the removal", "service", typ, "err", err)
		}
	}
	if user == "" {
		user, origin = s.native.CarriedOverAccount(typ), "carried-over"
	}
	if user != "" {
		if err := box.RemoveMusicServiceAccount(ctx, typ, display, user); err != nil {
			s.logger.Warn("us-services: the speaker refused the removal", "service", typ, "account", origin, "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": "the speaker refused to remove the account: " + err.Error(),
				"code":  "remove-refused",
			})
			return
		}
	}
	if err := s.native.ClearNative(typ); err != nil {
		http.Error(w, "could not clear the stored source", http.StatusInternalServerError)
		return
	}
	if user != "" {
		for i := 0; i < nativeRemoveRereads; i++ {
			if i > 0 {
				select {
				case <-ctx.Done():
				case <-time.After(s.nativeRemoveTimes().settle):
				}
			}
			srcs, err := box.GetSources(ctx)
			if err != nil {
				// Unconfirmed, not refused: the removal call itself succeeded.
				s.logger.Warn("us-services: could not re-read the speaker's sources after the removal", "service", typ, "err", err)
				break
			}
			if !stillLinked(srcs, typ) {
				break
			}
			if i == nativeRemoveRereads-1 {
				s.logger.Warn("us-services: the speaker still lists the account after the removal", "service", typ, "account", origin)
				writeJSON(w, http.StatusBadGateway, map[string]any{
					"error": "the speaker still lists the account after the removal",
					"code":  "still-linked",
				})
				return
			}
		}
	}
	s.logger.Info("us-services: account removed", "service", typ, "account", origin, "sent", user != "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
