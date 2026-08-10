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
		// Store and publish FIRST. This is the durable half: once the server is
		// in the account document, the speaker picks it up on its own poll and
		// keeps it through every reboot. It cannot fail on the box, so doing it
		// first means a refused push below still leaves the user with a setting
		// that works, just not until the speaker next reads its account.
		if s.mediaServers != nil {
			if err := s.mediaServers.Add(mediaservers.Server{ID: srv.ID, Name: srv.FriendlyName}); err != nil {
				s.logger.Warn("media server: could not remember the server", "err", err, "id", srv.ID)
			}
		}
		s.publishMediaServers()

		// Then make it usable NOW rather than at the next boot. Skipped when the
		// speaker already has the account, which is the normal state after a
		// restart: pushing it again answers 500 / 1024 and would report a
		// perfectly healthy source as a failure.
		pending := false
		if have, herr := c.RegisteredMediaServerAccounts(ctx); herr == nil && have[srv.SourceAccount()] {
			s.logger.Info("media server: already known to the speaker, nothing to push", "id", srv.ID)
		} else if err := c.RegisterMediaServer(ctx, srv); err != nil {
			// Not an error the user needs to see as failure: the setting is
			// stored, so the library turns up after the speaker's next restart.
			s.logger.Warn("media server: the speaker refused the immediate registration, it will appear after a restart",
				"err", err, "id", srv.ID)
			pending = true
		} else {
			// The speaker accepted it but the source is not usable yet: it
			// confirms the account with marge first, which took minutes when
			// measured. Never report this as ready.
			pending = true
		}
		s.logger.Info("media server: enabled as a native music source", "id", srv.ID, "name", srv.FriendlyName)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pending": pending})

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
			// Stop advertising it before telling the box to drop it, or its next
			// account poll would put it straight back.
			s.publishMediaServers()
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

// publishMediaServers hands the current set to the marge account responses, so
// the box PICKS THE SERVERS UP ITSELF on its next account poll.
//
// This is the whole persistence mechanism, and it is a pull, not a push. The box
// reads GET /streaming/account/<id>/full at boot and keeps whatever sources that
// document advertises; radio has always arrived that way. So a media server that
// sits in the account document is simply there after every reboot, with no write
// to the speaker at all, which also means nothing here can touch its standby
// countdown.
//
// The push to /setMusicServiceAccount is kept for the moment the user enables a
// server, and only for that: it makes the new source usable within the current
// session instead of at the next boot.
func (s *Server) publishMediaServers() {
	if s.mediaServers == nil || s.publishStoredMusic == nil {
		return
	}
	list := s.mediaServers.List()
	out := make([]StoredMusicSource, 0, len(list))
	for _, srv := range list {
		m := boxapi.MediaServer{ID: srv.ID, FriendlyName: srv.Name}
		out = append(out, StoredMusicSource{Account: m.SourceAccount(), Name: srv.Name})
	}
	s.publishStoredMusic(out)
}

// PublishMediaServers is publishMediaServers for cmd/agent to call once at
// startup, after the marge bridge is wired.
func (s *Server) PublishMediaServers() { s.publishMediaServers() }
