package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// #1763 (v1.5.2 bundle): the speaker's own Pandora was UNAVAILABLE in
// /sources and every select of a Pandora key came back 500 1008
// GENERAL_PANDORA_ERROR. Meanwhile the library track played before kept being
// re-pushed by its own verify, three retries and a false wedge strike.
func TestNativeKeyRecallAgainstTheSpeakersSourceList(t *testing.T) {
	pandora := presets.Preset{Slot: 4, Type: presets.TypeNative, Name: "Jazz Radio", Native: &presets.NativeItem{
		Source: "PANDORA", SourceAccount: "listener@example.com", Location: "111", ItemType: "stationurl",
	}}
	sources := func(status string) []byte {
		return []byte(`<sources deviceID="device-id-here"><sourceItem source="PANDORA" sourceAccount="listener@example.com" status="` +
			status + `">listener@example.com</sourceItem></sources>`)
	}
	cases := []struct {
		name        string
		status      string
		selectErr   error
		wantStatus  int
		wantCode    string
		wantSelects int
	}{
		{"unavailable: refused before the select", "UNAVAILABLE", nil, http.StatusServiceUnavailable, nativeServiceUnavailableCode, 0},
		{"ready: selected and started", "READY", nil, http.StatusOK, "", 1},
		{"ready but the speaker refuses", "READY", errors.New("select: status 500: 1008 GENERAL_PANDORA_ERROR"), http.StatusBadGateway, "native-select-refused", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newPlayTestServer(t)
			selects := 0
			s.selectFn = func(context.Context, string) error { selects++; return tc.selectErr }
			s.speakerSourcesFn = func(context.Context) ([]byte, error) { return sources(tc.status), nil }
			if err := s.presets.SetSlot(pandora); err != nil {
				t.Fatalf("SetSlot: %v", err)
			}
			// The previous play: a library track whose verify is still running.
			prevGen := s.setLastPlay("http://192.0.2.7:8200/track.flac", "Song", "", "audio/flac")
			prevStart := time.Now()

			w := httptest.NewRecorder()
			s.handlePlaySlot(w, httptest.NewRequest(http.MethodPost, "/api/play/4", nil))
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tc.wantStatus, w.Body.String())
			}
			if selects != tc.wantSelects {
				t.Fatalf("/select called %d times, want %d", selects, tc.wantSelects)
			}
			if tc.wantCode != "" {
				var body map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("body %q: %v", w.Body.String(), err)
				}
				if body["code"] != tc.wantCode {
					t.Fatalf("code = %v, want %s (%s)", body["code"], tc.wantCode, w.Body.String())
				}
			}
			// Started or refused, the key press is the newest play: the
			// library track's verify must stand down instead of re-pushing.
			if reason := s.recallStandDownReason(prevGen, prevStart); reason != "superseded by a newer play" {
				t.Fatalf("previous verify stand-down reason = %q, want superseded", reason)
			}
		})
	}
}

// An unreadable source list does not refuse anything: the speaker's own answer
// to the select decides, as before.
func TestNativeKeyRecallWithAnUnreadableSourceListStillSelects(t *testing.T) {
	s, _ := newPlayTestServer(t)
	selects := 0
	s.selectFn = func(context.Context, string) error { selects++; return nil }
	s.speakerSourcesFn = func(context.Context) ([]byte, error) { return nil, errors.New("timeout") }
	if err := s.presets.SetSlot(presets.Preset{Slot: 2, Type: presets.TypeNative, Name: "Jazz Radio", Native: &presets.NativeItem{
		Source: "PANDORA", SourceAccount: "listener@example.com", Location: "111", ItemType: "stationurl",
	}}); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}
	w := httptest.NewRecorder()
	s.handlePlaySlot(w, httptest.NewRequest(http.MethodPost, "/api/play/2", nil))
	if w.Code != http.StatusOK || selects != 1 {
		t.Fatalf("status = %d, selects = %d (%s)", w.Code, selects, w.Body.String())
	}
}
