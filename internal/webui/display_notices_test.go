package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/presets"
)

func TestDisplayNoticesSetting(t *testing.T) {
	oled.NoticesFlagPath = filepath.Join(t.TempDir(), "display-notices")
	defer func() { oled.NoticesFlagPath = "/mnt/nv/streborn/display-notices" }()
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	do := func(method, body string, want int) map[string]any {
		r := httptest.NewRequest(method, "/api/box/display-notices", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		s.handleDisplayNotices(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, body, w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	if got := do(http.MethodGet, "", 200); got["enabled"] != true || got["supported"] != false {
		t.Fatalf("default must be on (and no panel on a dev host): %v", got)
	}
	do(http.MethodPut, `{"enabled":false}`, 200)
	if got := do(http.MethodGet, "", 200); got["enabled"] != false {
		t.Fatalf("switch did not stick: %v", got)
	}
	do(http.MethodPost, `{"enabled":true}`, 200)
	if got := do(http.MethodGet, "", 200); got["enabled"] != true {
		t.Fatalf("switch did not come back on: %v", got)
	}
	do(http.MethodPost, `{}`, 400)
	do(http.MethodDelete, "", 405)
}

func TestIdentifyWithoutDisplay(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	r := httptest.NewRequest(http.MethodPost, "/api/box/identify", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	s.handleIdentify(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out["shown"] != false || out["reason"] != "no display" {
		t.Fatalf("got %d %v", w.Code, out)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/box/identify", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	w = httptest.NewRecorder()
	s.handleIdentify(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET answered %d", w.Code)
	}
}

func TestSameKeyItem(t *testing.T) {
	a := presets.Preset{Slot: 1, Name: "Radio Bob", Type: "radio", StreamURL: "http://192.0.2.1/bob", Art: "a.png"}
	b := a
	b.Name, b.Art = "BOB!", "b.png"
	if !sameKeyItem(a, b) {
		t.Fatal("a rename or new artwork is not a new item")
	}
	b.StreamURL = "http://192.0.2.1/other"
	if sameKeyItem(a, b) {
		t.Fatal("another station is a new item")
	}
	q1 := presets.Preset{Type: "queue", Items: []presets.PresetItem{{URL: "u1"}, {URL: "u2"}}}
	q2 := q1
	q2.Shuffle = true
	if !sameKeyItem(q1, q2) {
		t.Fatal("shuffle alone does not make a new folder")
	}
	q2.Items = []presets.PresetItem{{URL: "u1"}}
	if sameKeyItem(q1, q2) {
		t.Fatal("other tracks are a new folder")
	}
}

func TestKeyEmpty(t *testing.T) {
	s := &Server{presets: presets.New()}
	if _, known := s.KeyEmpty(2); known {
		t.Fatal("known before the box reported its list")
	}
	s.NoteBoxPresets([]BoxPreset{{Slot: 1, Source: "DEEZER"}})
	if empty, known := s.KeyEmpty(1); empty || !known {
		t.Fatal("a foreign box key is not empty")
	}
	if empty, known := s.KeyEmpty(2); !empty || !known {
		t.Fatal("key 2 holds nothing")
	}
	_ = s.presets.SetSlot(presets.Preset{Slot: 2, Name: "X", Type: "radio", StreamURL: "http://192.0.2.1/x"})
	if empty, _ := s.KeyEmpty(2); empty {
		t.Fatal("a key in STR's store is not empty")
	}
}

// The phone page's "find this speaker" card is localised in every bundle and
// wired to the identify endpoint.
func TestPhoneRemoteIdentifyCard(t *testing.T) {
	bundles := strings.Count(indexHTML, "now:\"")
	for _, key := range []string{"identHead", "identSum", "identBtn", "identOk", "identNoDisp", "identBusy", "identFail"} {
		if got := strings.Count(indexHTML, key+":\""); got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
	for _, want := range []string{`id="btnIdent"`, "set('lblIdent', T.identHead)", "api('/api/box/identify', 'POST')"} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("index.html misses %s", want)
		}
	}
}
