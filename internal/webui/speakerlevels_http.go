package webui

import (
	"context"
	"net/http"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// levelsClient builds the Bose API client this handler talks to.
//
// A var purely so the handler can be tested against a fake box. boxapi.Client
// builds its URLs with the Bose port hardcoded, so a test cannot express "this
// host, that port" through boxapi.New at all, and without a seam every test
// here would pass for the wrong reason: an unreachable box also answers
// "supported: false".
var levelsClient = func(host string) *boxapi.Client { return boxapi.New(host) }

// handleBoxSpeakerLevels serves the front-center and rear-surround levels of a
// home theater system (SoundTouch 300 and the Lifestyle/CineMate consoles with
// the SoundTouch adapter).
//
// GET answers {"supported":false} on every ordinary speaker. That is not an
// error and the caller must not show one: a speaker without surrounds simply
// has no separate levels, and the app hides the section.
//
// PUT takes {"level":"frontCenter"|"rearSurrounds","value":N}. Only the named
// level is written, per the route's partial-update contract, so a value
// changed meanwhile on the Bose remote is not clobbered by a stale one the app
// still had on screen.
func (s *Server) handleBoxSpeakerLevels(w http.ResponseWriter, r *http.Request) {
	c := levelsClient(s.boxHost)
	switch r.Method {
	case http.MethodGet:
		// The capability probe is one extra GET on a box that has never been
		// asked; after that the verdict is cached per host in boxapi.
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		lv, err := c.GetSpeakerLevels(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, lv)
	case http.MethodPut:
		var req struct {
			Level string `json:"level"`
			Value int    `json:"value"`
		}
		if !decodeJSONRequest(w, r, 256, &req) {
			return
		}
		if req.Level != boxapi.LevelFrontCenter && req.Level != boxapi.LevelRearSurrounds {
			http.Error(w, "level must be frontCenter or rearSurrounds", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		// Refuse a value the firmware would refuse anyway, and say which
		// range applies: the alternative is a 4xx from the box that the app
		// can only report as "did not work".
		lv, err := c.GetSpeakerLevels(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		want := lv.RearSurrounds
		if req.Level == boxapi.LevelFrontCenter {
			want = lv.FrontCenter
		}
		if !lv.Supported || !want.Avail {
			http.Error(w, "this speaker has no separate "+req.Level+" level", http.StatusUnprocessableEntity)
			return
		}
		if req.Value < want.Min || req.Value > want.Max {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": "value out of range", "min": want.Min, "max": want.Max,
			})
			return
		}
		if err := c.SetSpeakerLevel(ctx, req.Level, req.Value); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		s.logger.Info("speaker level set", "level", req.Level, "value", req.Value)
		writeJSON(w, http.StatusOK, map[string]any{"level": req.Level, "value": req.Value})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
