package webui

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/standbyclock"
)

// The speaker's own standby clock (the Bose firmware's /clockDisplay), kept by
// internal/standbyclock: the owner's choice is stored on the speaker and put
// back after every agent start and when the firmware drops it (#979). The
// brightness rides along (#1226).
//
// GET /api/box/standby-clock answers
//
//	{supported, hasClock, stored?, firmware?, brightnessSupported}
//
// supported: the firmware's clock setting could be read. hasClock: the
// speaker's /capabilities says it can show a clock (false on a SoundTouch 10).
// brightnessSupported: the firmware reports a brightnessLevel at all; whether
// it also dims is a separate matter (brightnessHonoured on the POST answer).
//
// POST takes {enabled, timezone, offsetMinutes, format24, brightness?} and
// answers the write's Result plus ok.

// SetStandbyClockKeeper hands the server the agent's keeper, so the app's
// writes and the agent's own re-applies share one lock and one memory of
// whether the speaker took a brightness.
func (s *Server) SetStandbyClockKeeper(k *standbyclock.Keeper) {
	s.standbyClockMu.Lock()
	s.standbyClockKeeper = k
	s.standbyClockMu.Unlock()
}

// standbyClock returns the keeper, creating one against the speaker when the
// agent did not hand one over (tests, older wiring). nil without a box host.
func (s *Server) standbyClock() *standbyclock.Keeper {
	s.standbyClockMu.Lock()
	defer s.standbyClockMu.Unlock()
	if s.standbyClockKeeper == nil && s.boxHost != "" {
		s.standbyClockKeeper = standbyclock.New(boxapi.New(s.boxHost), s.logger.With("comp", "standbyclock"), standbyclock.Options{})
	}
	return s.standbyClockKeeper
}

func (s *Server) handleStandbyClock(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	k := s.standbyClock()
	if k == nil {
		http.Error(w, "no speaker", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		out := map[string]any{"supported": false, "brightnessSupported": false}
		if st, ok := k.Stored(); ok {
			out["stored"] = st
		}
		fw, err := k.Firmware(ctx)
		if err == nil {
			out["supported"] = true
			out["firmware"] = fw
			out["brightnessSupported"] = fw.Brightness != nil
		} else {
			out["error"] = err.Error()
		}
		hasClock := true
		if s.boxHost != "" {
			if has, ok := boxapi.New(s.boxHost).HasDisplay(ctx); ok {
				hasClock = has
			}
		}
		out["hasClock"] = hasClock
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var body struct {
			Enabled       *bool  `json:"enabled"`
			Timezone      string `json:"timezone"`
			OffsetMinutes int    `json:"offsetMinutes"`
			Format24      *bool  `json:"format24"`
			Brightness    *int   `json:"brightness"`
		}
		if !decodeJSONRequest(w, r, 4096, &body) {
			return
		}
		if len(body.Timezone) > 64 || strings.ContainsAny(body.Timezone, "<>\"&") {
			http.Error(w, "bad timezone", http.StatusBadRequest)
			return
		}
		if body.OffsetMinutes < -14*60 || body.OffsetMinutes > 14*60 {
			http.Error(w, "bad offset", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		// A brightness-only change (the phone's slider) keeps everything else
		// as stored, or as the speaker has it.
		base, ok := k.Stored()
		if !ok {
			fw, err := k.Firmware(ctx)
			if err != nil && (body.Enabled == nil || body.Format24 == nil) {
				http.Error(w, "the speaker's clock setting cannot be read: "+err.Error(), http.StatusBadGateway)
				return
			}
			base = fw
		}
		c := base
		if body.Enabled != nil {
			c.Enabled = *body.Enabled
			c.Timezone, c.OffsetMinutes = body.Timezone, body.OffsetMinutes
		}
		if body.Format24 != nil {
			c.Format24 = *body.Format24
		}
		if body.Brightness != nil {
			b := standbyclock.ClampBrightness(*body.Brightness)
			c.Brightness = &b
		}
		res, err := k.Apply(ctx, c)
		if err != nil {
			// The speaker refusing or not answering is a gateway failure; a
			// write that reached the speaker but could not be stored is ours.
			status := http.StatusBadGateway
			if errors.Is(err, standbyclock.ErrStore) {
				status = http.StatusInternalServerError
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
