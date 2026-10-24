package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/webhooks"
)

func boxSettingsServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		resumeOnPowerOnPath: filepath.Join(t.TempDir(), "resume-on-power-on"),
	}
}

func postLAN(fn func(http.ResponseWriter, *http.Request), path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.RemoteAddr = "192.168.1.20:5555"
	fn(w, r)
	return w
}

func TestDecodeOptionalJSONRequest(t *testing.T) {
	type in struct {
		Target string `json:"target"`
	}
	cases := []struct {
		body string
		ok   bool
		want string
	}{
		{``, true, ""},
		{"  \n", true, ""},
		{`{"target":"192.0.2.1:9080"}`, true, "192.0.2.1:9080"},
		{`{"target":`, false, ""},
		{`target=x`, false, ""},
	}
	for _, c := range cases {
		var v in
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(c.body))
		ok := decodeOptionalJSONRequest(w, r, 4096, &v)
		if ok != c.ok || v.Target != c.want {
			t.Errorf("%q: ok=%v target=%q, want %v %q", c.body, ok, v.Target, c.ok, c.want)
		}
		if !ok && w.Code != http.StatusBadRequest {
			t.Errorf("%q: refused with %d, want 400", c.body, w.Code)
		}
	}
	// A request without a body at all (not even http.NoBody) is the defaults.
	var v in
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	r.Body = nil
	if !decodeOptionalJSONRequest(httptest.NewRecorder(), r, 4096, &v) {
		t.Error("nil body must count as empty")
	}
}

// The power-on resume is on by default. A broken or missing body used to decode
// to enabled=false and switch it off; now it is refused and the file untouched.
func TestResumeOnPowerOnRejectsABrokenBody(t *testing.T) {
	s := boxSettingsServer(t)
	for _, body := range []string{`{"enabled":`, `enabled=false`, ``} {
		if w := postLAN(s.handleResumeOnPowerOn, "/api/box/resume-on-power-on", body); w.Code != http.StatusBadRequest {
			t.Errorf("%q: %d, want 400", body, w.Code)
		}
	}
	if !s.resumeOnPowerOnEnabled() {
		t.Fatal("a refused post switched the power-on resume off")
	}
	if w := postLAN(s.handleResumeOnPowerOn, "/api/box/resume-on-power-on", `{"enabled":false}`); w.Code != http.StatusOK {
		t.Fatalf("valid post: %d %s", w.Code, w.Body.String())
	}
	if s.resumeOnPowerOnEnabled() {
		t.Fatal("a valid post did not switch the power-on resume off")
	}
}

// The AirPlay toggle reboots the speaker on every POST, so a body that cannot be
// read is refused before the handler looks at the speaker at all. (On the test
// machine no AirplayConfiguration.xml exists, so the only way to tell the two
// 400s apart is the message.)
func TestAirplayOptRejectsABrokenBody(t *testing.T) {
	s := boxSettingsServer(t)
	for _, body := range []string{`{"enabled":`, ``} {
		w := postLAN(s.handleBoxAirplayOpt, "/api/box/airplay-opt", body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid json") {
			t.Errorf("%q: %d %q, want 400 invalid json", body, w.Code, w.Body.String())
		}
	}
}

// The webhook test button may be pressed without a body (fire the saved thumb
// action), so an empty body keeps working; a broken one is refused instead of
// firing whatever half-decoded.
func TestWebhooksTestTakesAnEmptyBodyButNotABrokenOne(t *testing.T) {
	store, err := webhooks.Load(filepath.Join(t.TempDir(), "webhooks.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := boxSettingsServer(t)
	s.webhooks = store
	w := postLAN(s.handleWebhooksTest, "/api/webhooks/test", "")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "nothing to test") {
		t.Fatalf("empty body: %d %q, want the 'nothing to test' answer (no thumb action saved)", w.Code, w.Body.String())
	}
	w = postLAN(s.handleWebhooksTest, "/api/webhooks/test", `{"url":`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid json") {
		t.Fatalf("broken body: %d %q, want 400 invalid json", w.Code, w.Body.String())
	}
}
