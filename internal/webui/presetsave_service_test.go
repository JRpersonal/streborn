package webui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// The savePresetSlot service behind PUT /api/presets/<n>, saveThroughPut and
// handleSavePlaying.
//
// saveThroughPut used to build a PUT request, drive handlePresetSlot with a
// hand-written recorder and parse code, slot and name back out of the body.
// Now it calls the service; these tests pin that the refusal it maps onto
// HoldSaveError is what the wire body said.

func TestHoldSaveErrorFromRefusal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ref  *presetRefusal
		want HoldSaveError
	}{
		{"already on another key",
			jsonPresetRefusal(http.StatusConflict, map[string]any{"code": "already-on-slot", "slot": 4, "name": "Jens Chill"}),
			HoldSaveError{Status: 409, Code: "already-on-slot", OtherSlot: 4, OtherName: "Jens Chill"}},
		{"a gate with a message",
			jsonPresetRefusal(http.StatusUnprocessableEntity, map[string]any{"code": "stream-url-missing", "error": "no stream"}),
			HoldSaveError{Status: 422, Code: "stream-url-missing", Message: "no stream"}},
		{"a plain-text 500 becomes save-failed with the text",
			textPresetRefusal(http.StatusInternalServerError, "disk full"),
			HoldSaveError{Status: 500, Code: "save-failed", Message: "disk full"}},
	}
	for _, c := range cases {
		got := holdSaveErrorFrom(c.ref)
		if *got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, *got, c.want)
		}
	}
}

func TestSavePresetSlotRefusesLikeThePut(t *testing.T) {
	t.Parallel()
	s := newSaveGateServer(t)
	if err := s.presets.SetSlot(presets.Preset{Slot: 1, Name: "Relax", Type: "radio", StreamURL: "http://stream.example/relax"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	_, ref := s.savePresetSlot(ctx, presetSaveRequest{Slot: 2, Preset: presets.Preset{Name: "Dead", Type: "radio"}})
	if ref == nil || ref.Status != 422 || ref.Code != "stream-url-missing" || ref.Error == "" {
		t.Errorf("empty stream URL: %+v, want 422 stream-url-missing with a message", ref)
	}

	_, ref = s.savePresetSlot(ctx, presetSaveRequest{Slot: 2, Preset: presets.Preset{Name: "Relax again", Type: "radio", StreamURL: "http://stream.example/relax"}})
	if ref == nil || ref.Status != 409 || ref.Code != "already-on-slot" || ref.Slot != 1 || ref.Name != "Relax" {
		t.Errorf("duplicate: %+v, want 409 already-on-slot naming key 1 Relax", ref)
	}
	if _, ok := s.presets.Get(2); ok {
		t.Error("the refused duplicate was stored anyway")
	}

	_, ref = s.savePresetSlot(ctx, presetSaveRequest{Slot: 13, Preset: presets.Preset{Name: "X"}})
	if ref == nil || ref.Status != 400 || ref.JSON != nil {
		t.Errorf("slot 13: %+v, want a plain-text 400", ref)
	}

	saved, ref := s.savePresetSlot(ctx, presetSaveRequest{Slot: 3, Preset: presets.Preset{Name: "Jazz", StreamURL: "http://stream.example/jazz"}})
	if ref != nil {
		t.Fatalf("a well-formed save was refused: %+v", ref)
	}
	if saved.Slot != 3 || saved.Type != "radio" || saved.Name != "Jazz" {
		t.Errorf("stored = %+v, want slot 3 radio Jazz", saved)
	}
}

// The hold path's mapping end to end: the held key gets the refusal the app
// would have seen, with the other key named.
func TestSaveThroughPutMapsTheDuplicateOntoHoldSaveError(t *testing.T) {
	t.Parallel()
	s := newSaveGateServer(t)
	if err := s.presets.SetSlot(presets.Preset{Slot: 5, Name: "Mix", Type: "spotify", URI: "spotify:playlist:37i9dQZF1DWX7rdRjOECPW"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.saveThroughPut(context.Background(),
		presets.Preset{Slot: 2, Name: "Spotify", Type: "spotify", URI: "spotify:playlist:37i9dQZF1DWX7rdRjOECPW"},
		"spotify", true, holdSaveUA, "speaker-key")
	var refusal *HoldSaveError
	if !errors.As(err, &refusal) || refusal.Code != "already-on-slot" || refusal.OtherSlot != 5 || refusal.OtherName != "Mix" {
		t.Fatalf("err = %v, want already-on-slot naming key 5 Mix", err)
	}
	if refusal.Status != http.StatusConflict {
		t.Errorf("status = %d, want 409", refusal.Status)
	}
}

// The wire contract of PUT /api/presets/<n>, one golden per exit reachable
// without a speaker. The desktop app's regexes (groups.js, main.js,
// presetmove.js) read "already-on-slot", "slot": N and "name" out of the raw
// body, and the phone remote's presetConflict reads the same three keys.
func TestHandlePresetSlotPutWire(t *testing.T) {
	t.Parallel()
	const relax = `{"name":"Absolut Relax","type":"radio","stream_url":"http://stream.example/relax","codec":"AAC+"}`
	withRelaxOn1 := func(t *testing.T) *Server {
		s := newSaveGateServer(t)
		if w := putPreset(t, s, 1, relax); w.Code != 200 {
			t.Fatalf("seed: %d %s", w.Code, w.Body.String())
		}
		return s
	}
	cases := []struct {
		name   string
		slot   int
		body   string
		setup  func(t *testing.T) *Server
		status int
		want   string
	}{
		{"no store", 1, relax, func(t *testing.T) *Server { s := newSaveGateServer(t); s.presets = nil; return s },
			http.StatusServiceUnavailable, "presets store not initialized\n"},
		{"slot out of range", 13, relax, newSaveGateServer,
			http.StatusBadRequest, "invalid slot, must be 1-12\n"},
		{"bad json", 1, `{"name":`, newSaveGateServer,
			http.StatusBadRequest, "invalid json: unexpected EOF\n"},
		{"empty folder", 1, `{"name":"Folder","type":"queue"}`, newSaveGateServer,
			http.StatusUnprocessableEntity,
			`{"code":"queue-empty","error":"This folder can't be saved as a preset: it has no playable tracks. Open a folder with audio files and try again."}` + "\n"},
		{"spotify without a context", 1, `{"name":"Mix","type":"spotify","uri":"spotify:playlist:"}`, newSaveGateServer,
			http.StatusUnprocessableEntity,
			`{"code":"spotify-uri-unplayable","error":"This Spotify selection can't be saved to a preset: it has no replayable playlist, album or track. Open a playlist or album and try again."}` + "\n"},
		{"no stream url", 1, `{"name":"Dead","type":"radio","stream_url":""}`, newSaveGateServer,
			http.StatusUnprocessableEntity,
			`{"code":"stream-url-missing","error":"This selection can't be saved as a preset (no playable stream). Pick a radio station or a Spotify playlist and try again."}` + "\n"},
		{"stream url not http", 1, `{"name":"Dead","type":"radio","stream_url":"ftp://x/y"}`, newSaveGateServer,
			http.StatusUnprocessableEntity,
			`{"code":"stream-url-invalid","error":"This selection can't be saved as a preset (no playable stream). Pick a radio station or a Spotify playlist and try again."}` + "\n"},
		{"own proxy with nothing to heal from", 2, `{"name":"Poisoned","type":"radio","stream_url":"http://127.0.0.1:8888/stream/4"}`, newSaveGateServer,
			http.StatusUnprocessableEntity,
			`{"code":"stream-url-self-proxy","error":"This save would store the speaker's own proxy address instead of the station. Play the station again (or re-search it) and save then."}` + "\n"},
		{"already on another key", 2, relax, withRelaxOn1,
			http.StatusConflict, `{"code":"already-on-slot","name":"Absolut Relax","slot":1}` + "\n"},
		{"native hold with no speaker", 3, `{"name":"Pandora","type":"native"}`, newSaveGateServer,
			http.StatusBadGateway,
			`{"code":"native-nowplaying-unreadable","error":"The speaker could not be asked what it is playing. Try again in a moment."}` + "\n"},
		{"native item that cannot be kept", 3, `{"name":"Pandora","type":"native","native":{"source":"PANDORA","location":"has space"}}`, newSaveGateServer,
			http.StatusUnprocessableEntity,
			`{"code":"native-item-invalid","error":"This station can't be kept on a key."}` + "\n"},
		{"native item already on another key", 4, `{"name":"Pandora","type":"native","native":{"source":"PANDORA","location":"x"}}`, func(t *testing.T) *Server {
			s := newSaveGateServer(t)
			if w := putPreset(t, s, 3, `{"name":"Pandora","type":"native","native":{"source":"PANDORA","location":"x"}}`); w.Code != 200 {
				t.Fatalf("seed: %d %s", w.Code, w.Body.String())
			}
			return s
		}, http.StatusConflict, `{"code":"already-on-slot","name":"Pandora","slot":3}` + "\n"},
		{"native stored", 3, `{"name":"Pandora","type":"native","native":{"source":"PANDORA","location":"x"}}`, newSaveGateServer,
			http.StatusOK,
			`{"slot":3,"name":"Pandora","stream_url":"","type":"native","source":"Pandora","native":{"source":"PANDORA","location":"x"}}` + "\n"},
		{"radio stored", 2, relax, newSaveGateServer,
			http.StatusOK, `{"slot":2,"name":"Absolut Relax","stream_url":"http://stream.example/relax","type":"radio","codec":"AAC+"}` + "\n"},
		{"same key re-saved", 1, relax, withRelaxOn1,
			http.StatusOK, `{"slot":1,"name":"Absolut Relax","stream_url":"http://stream.example/relax","type":"radio","codec":"AAC+"}` + "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := c.setup(t)
			r := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/presets/%d", c.slot), strings.NewReader(c.body))
			w := httptest.NewRecorder()
			s.handlePresetSlot(w, r)
			if w.Code != c.status {
				t.Errorf("status = %d, want %d", w.Code, c.status)
			}
			if got := w.Body.String(); got != c.want {
				t.Errorf("body =\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}
