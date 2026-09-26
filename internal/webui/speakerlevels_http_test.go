package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// levelRewrite sends every request to the test server, keeping the path. The
// Bose port that boxapi puts in the URL is discarded along with the host.
type levelRewrite struct{ to *url.URL }

func (rt *levelRewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme = rt.to.Scheme
	r.URL.Host = rt.to.Host
	return http.DefaultTransport.RoundTrip(r)
}

// levelBox serves the two Bose routes the handler needs, on the port boxapi
// talks to, and records the POST bodies.
func levelBox(t *testing.T, caps, levels string) (*Server, *[]string) {
	t.Helper()
	posts := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			posts = append(posts, string(b))
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		switch r.URL.Path {
		case "/capabilities":
			_, _ = w.Write([]byte(caps))
		case "/audioproductlevelcontrols":
			_, _ = w.Write([]byte(levels))
		default:
			http.NotFound(w, r)
		}
	}))
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	// A NAME, never host:port: boxapi builds its URL as http://<Host>:8090/...,
	// so a host that already carries a port makes an unparseable URL and every
	// read fails. That failure is indistinguishable from a speaker without the
	// capability, so the tests below would all have passed for the wrong reason.
	// The transport then sends it to the test server regardless.
	host := "level-box-" + t.Name()
	// The client is aimed at the test server through the seam, because there is
	// no other way to express "this host, that port" to boxapi.
	prev := levelsClient
	levelsClient = func(string) *boxapi.Client {
		return &boxapi.Client{Host: host, HTTP: &http.Client{
			Timeout: 2 * time.Second, Transport: &levelRewrite{to: u}}}
	}
	// The capability verdict is cached per host for the life of the process,
	// and httptest hands out ephemeral ports that a later server can be given
	// again once this one closes. Without the reset a test inherits the verdict
	// of whichever earlier test happened to bind the same port.
	boxapi.ForgetLevelControls(host)
	t.Cleanup(func() {
		levelsClient = prev
		boxapi.ForgetLevelControls(host)
		srv.Close()
	})
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: host}
	return s, &posts
}

// The case every one of Jens' five speakers hits: no such capability. It must
// be a 200 with supported=false, NOT an error, or the settings pane shows a
// failure on a speaker that is working perfectly. Measured on the real fleet
// 2026-09-26: they advertise only systemtimeout and rebroadcastlatencymode.
func TestSpeakerLevelsUnsupportedAnswersQuietly(t *testing.T) {
	s, _ := levelBox(t, `<capabilities deviceID="x">
  <capability name="systemtimeout" url="/systemtimeout"/>
  <capability name="rebroadcastlatencymode" url="/rebroadcastlatencymode"/>
</capabilities>`, `<audioproductlevelcontrols/>`)
	w := httptest.NewRecorder()
	s.handleBoxSpeakerLevels(w, httptest.NewRequest(http.MethodGet, "/api/box/levels", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("an ordinary speaker must answer 200, got %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not JSON: %s", w.Body.String())
	}
	if out["supported"] != false {
		t.Errorf("supported should be false, got %v", out["supported"])
	}
}

// A write to a level the speaker does not have is refused here rather than
// forwarded, so the app gets a reason instead of a bare failure from the box.
func TestSpeakerLevelWriteRefusedWhenUnsupported(t *testing.T) {
	s, posts := levelBox(t, `<capabilities deviceID="x"/>`, `<audioproductlevelcontrols/>`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/box/levels",
		strings.NewReader(`{"level":"rearSurrounds","value":3}`))
	s.handleBoxSpeakerLevels(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body.String())
	}
	if len(*posts) != 0 {
		t.Error("nothing may be written to a speaker that has no such level")
	}
}

// A value outside the speaker's own range comes back with the range, so the
// app can say what would be allowed.
func TestSpeakerLevelWriteOutOfRangeNamesTheRange(t *testing.T) {
	s, posts := levelBox(t, `<capabilities deviceID="x">
  <capability name="audioproductlevelcontrols" url="/audioproductlevelcontrols"/>
</capabilities>`, `<audioproductlevelcontrols>
  <rearSurroundSpeakersLevel value="0" minValue="-10" maxValue="10" step="1"/>
</audioproductlevelcontrols>`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/box/levels",
		strings.NewReader(`{"level":"rearSurrounds","value":99}`))
	s.handleBoxSpeakerLevels(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["min"] == nil || out["max"] == nil {
		t.Errorf("the refusal must carry the range, got %s", w.Body.String())
	}
	if len(*posts) != 0 {
		t.Error("an out-of-range value must not reach the speaker")
	}
}

// The path a soundbar owner actually takes: a value inside the range reaches
// the speaker, and only the level he moved is written.
func TestSpeakerLevelWriteReachesTheSpeaker(t *testing.T) {
	s, posts := levelBox(t, `<capabilities deviceID="x">
  <capability name="audioproductlevelcontrols" url="/audioproductlevelcontrols"/>
</capabilities>`, `<audioproductlevelcontrols>
  <frontCenterSpeakerLevel value="0" minValue="-10" maxValue="10" step="1"/>
  <rearSurroundSpeakersLevel value="0" minValue="-10" maxValue="10" step="1"/>
</audioproductlevelcontrols>`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/box/levels",
		strings.NewReader(`{"level":"rearSurrounds","value":4}`))
	s.handleBoxSpeakerLevels(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(*posts) != 1 {
		t.Fatalf("expected one write, got %d", len(*posts))
	}
	body := (*posts)[0]
	if !strings.Contains(body, `rearSurroundSpeakersLevel value="4"`) {
		t.Errorf("wrong body: %s", body)
	}
	if strings.Contains(body, "frontCenterSpeakerLevel") {
		t.Errorf("the untouched level must not be written: %s", body)
	}
}

// An unknown level name is a caller bug, not something to guess at.
func TestSpeakerLevelWriteRejectsUnknownName(t *testing.T) {
	s, _ := levelBox(t, `<capabilities deviceID="x"/>`, `<audioproductlevelcontrols/>`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/box/levels",
		strings.NewReader(`{"level":"subwoofer","value":1}`))
	s.handleBoxSpeakerLevels(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
}
