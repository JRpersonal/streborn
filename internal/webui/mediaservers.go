package webui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/mediaservers"
)

// Native music sources from a DLNA/UPnP media server (NAS, FRITZ!Box, Plex).
//
// The speaker finds media servers on the LAN by itself but will not play from
// one until it is registered as a STORED_MUSIC account. Once registered it
// browses and plays the server natively, and the server also shows up in the
// original Bose app. That is the whole feature: STR turns the registration on
// and keeps it on, and then gets out of the way.
//
// Everything here is a thin shell around boxapi. What STR adds is memory: the
// registration does NOT survive a reboot on its own (see the mediaservers
// package), so the user's choice is persisted and reapplied at startup.

// mediaServerView is one server as the UI sees it: what the box discovered,
// plus whether it is on right now and whether STR will put it back after a
// reboot.
type mediaServerView struct {
	boxapi.MediaServer
	// Enabled is the user's stored intent. It can differ from Registered for a
	// short while after a reboot or a fresh enable, because the box takes its
	// time confirming the account with marge.
	Enabled bool `json:"enabled"`
	// Status is the raw /sources status when the source exists, purely
	// informational. It is a connection indicator, not a capability.
	Status string `json:"status,omitempty"`
}

// handleMediaServers is GET (list), POST (enable) and DELETE (disable).
func (s *Server) handleMediaServers(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet, http.MethodPost, http.MethodDelete) {
		return
	}
	if s.boxHost == "" {
		http.Error(w, "box host not configured", http.StatusServiceUnavailable)
		return
	}
	c := boxapi.New(s.boxHost)

	switch r.Method {
	case http.MethodGet:
		// 12 s: the firmware answers /listMediaServers from its own discovery
		// cache, but a box that just woke can take a while to produce it.
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		out, err := s.mediaServerViews(ctx, c)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"servers": out})

	case http.MethodPost:
		var req struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if !decodeJSONRequest(w, r, 4<<10, &req) {
			return
		}
		if strings.TrimSpace(req.ID) == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		srv := boxapi.MediaServer{ID: strings.TrimSpace(req.ID), FriendlyName: req.Name}
		if err := c.RegisterMediaServer(ctx, srv); err != nil {
			s.logger.Warn("media server: the speaker refused the registration", "err", err, "id", srv.ID)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if s.mediaServers != nil {
			if err := s.mediaServers.Add(mediaservers.Server{ID: srv.ID, Name: srv.FriendlyName}); err != nil {
				// The box HAS the source; failing the whole call would tell the
				// user it did not work. Say it will not survive a reboot instead.
				s.logger.Warn("media server: registered on the speaker but could not be remembered", "err", err, "id", srv.ID)
			}
		}
		s.logger.Info("media server: registered as a native music source", "id", srv.ID, "name", srv.FriendlyName)
		// The source does not appear at once: the speaker confirms the account
		// with marge first, which took minutes when measured. Say so rather than
		// letting the UI report a source that is not there yet.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pending": true})

	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		name := r.URL.Query().Get("name")
		if name == "" && s.mediaServers != nil {
			for _, srv := range s.mediaServers.List() {
				if srv.ID == id {
					name = srv.Name
					break
				}
			}
		}
		// Forget it FIRST. If the box call fails we must not be left with a
		// stored intent that puts the source back at the next boot.
		if s.mediaServers != nil {
			if err := s.mediaServers.Remove(id); err != nil {
				s.logger.Warn("media server: could not forget the server", "err", err, "id", id)
			}
		}
		if err := c.UnregisterMediaServer(ctx, boxapi.MediaServer{ID: id, FriendlyName: name}); err != nil {
			s.logger.Warn("media server: the speaker refused the removal", "err", err, "id", id)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		s.logger.Info("media server: removed as a music source", "id", id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// mediaServerViews merges what the box discovered, what is registered right
// now, and what STR was told to keep.
//
// A server the user enabled but that is not answering right now still has to
// appear, or the only control for turning it off would vanish with it.
func (s *Server) mediaServerViews(ctx context.Context, c *boxapi.Client) ([]mediaServerView, error) {
	found, err := c.ListMediaServers(ctx)
	if err != nil {
		return nil, err
	}
	// Best-effort: a box that will not answer /sources still gets a usable list,
	// it just cannot mark which entries are live yet.
	status := map[string]string{}
	if srcs, serr := c.GetSources(ctx); serr == nil {
		for _, src := range srcs {
			if strings.EqualFold(src.Source, "STORED_MUSIC") && src.SourceAccount != "" {
				status[src.SourceAccount] = src.Status
			}
		}
	}

	out := make([]mediaServerView, 0, len(found))
	seen := map[string]bool{}
	for _, m := range found {
		seen[m.ID] = true
		st, registered := status[m.SourceAccount()]
		m.Registered = registered
		out = append(out, mediaServerView{
			MediaServer: m,
			Enabled:     s.mediaServers != nil && s.mediaServers.Has(m.ID),
			Status:      st,
		})
	}
	if s.mediaServers != nil {
		for _, srv := range s.mediaServers.List() {
			if seen[srv.ID] {
				continue
			}
			m := boxapi.MediaServer{ID: srv.ID, FriendlyName: srv.Name}
			st, registered := status[m.SourceAccount()]
			m.Registered = registered
			out = append(out, mediaServerView{MediaServer: m, Enabled: true, Status: st})
		}
	}
	return out, nil
}

// ReapplyMediaServers puts the user's enabled media servers back after a
// restart, once.
//
// It READS first and only writes for a server that is actually missing. That
// ordering is the point: a write to the box resets its standby countdown, and a
// speaker that is never allowed to reach deep standby is a bug we have shipped
// before (#472). On the normal path, where nothing is missing, this costs one
// read and no writes at all.
//
// Deliberately not on a timer. Once per agent start is enough, because the only
// thing that drops the registration is a restart.
func (s *Server) ReapplyMediaServers(ctx context.Context) {
	if s.mediaServers == nil || s.boxHost == "" {
		return
	}
	want := s.mediaServers.List()
	if len(want) == 0 {
		return
	}
	c := boxapi.New(s.boxHost)
	have, err := c.RegisteredMediaServerAccounts(ctx)
	if err != nil {
		s.logger.Warn("media server: could not read the speaker's sources, leaving the registrations alone", "err", err)
		return
	}
	for _, srv := range want {
		m := boxapi.MediaServer{ID: srv.ID, FriendlyName: srv.Name}
		if have[m.SourceAccount()] {
			continue
		}
		if err := c.RegisterMediaServer(ctx, m); err != nil {
			s.logger.Warn("media server: could not restore the music source after the restart",
				"err", err, "id", srv.ID, "name", srv.Name)
			continue
		}
		s.logger.Info("media server: music source restored after the restart", "id", srv.ID, "name", srv.Name)
	}
}
