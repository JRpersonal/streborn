package boxapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// recordingBox is a fake firmware that serves per-path fixtures and records
// every request (path + body) so a test can assert which routes were used.
type recordingBox struct {
	mu     sync.Mutex
	paths  []string
	bodies map[string]string // last request body per path
}

func (r *recordingBox) requested(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.paths {
		if p == path {
			n++
		}
	}
	return n
}

func (r *recordingBox) lastBody(path string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bodies[path]
}

// newRecordingBox mounts the routes and returns a Client with the given Host
// (distinct per test: the tone-controls route cache is keyed by it) plus the
// recorder. Unrouted paths answer 404, like newFakeBox.
func newRecordingBox(t *testing.T, host string, routes map[string]string) (*Client, *recordingBox) {
	t.Helper()
	rec := &recordingBox{bodies: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		rec.mu.Lock()
		rec.paths = append(rec.paths, r.URL.Path)
		rec.bodies[r.URL.Path] = string(b)
		rec.mu.Unlock()
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	t.Cleanup(func() { toneControlsHosts.Delete(host) })
	return &Client{
		Host: host,
		HTTP: &http.Client{Timeout: 2 * time.Second, Transport: &rewriteTransport{to: u}},
	}, rec
}

const (
	bassClassicOK = `<bass deviceID="AABBCCDDEEFF"><targetbass>-4</targetbass><actualbass>-4</actualbass></bass>`
	bassCapsNone  = `<bassCapabilities deviceID="AABBCCDDEEFF"><bassAvailable>false</bassAvailable><bassMin>0</bassMin><bassMax>0</bassMax><bassDefault>0</bassDefault></bassCapabilities>`
	bassCapsOK    = `<bassCapabilities deviceID="AABBCCDDEEFF"><bassAvailable>true</bassAvailable><bassMin>-9</bassMin><bassMax>0</bassMax><bassDefault>0</bassDefault></bassCapabilities>`
	capsWithTone  = `<capabilities deviceID="AABBCCDDEEFF"><capability name="audioproducttonecontrols" url="/audioproducttonecontrols" /><capability name="audiodspcontrols" url="/audiodspcontrols" /></capabilities>`
	capsNoTone    = `<capabilities deviceID="AABBCCDDEEFF"><capability name="audiodspcontrols" url="/audiodspcontrols" /></capabilities>`
	toneControls  = `<audioproducttonecontrols><bass value="50" minValue="-100" maxValue="100" step="50" /><treble value="0" minValue="-100" maxValue="100" step="50" /></audioproducttonecontrols>`
)

// A box that reports bassAvailable=false but advertises the tone-controls
// capability (the Lifestyle 650 case, support mail 2026-08-22) must surface
// the gated route's bass: value/min/max/step through, default mapped to 0 for
// the relative slider, available=true. Before the gate was probed the Bass
// stayed 0..0 unavailable and the slider sat greyed.
func TestLoadSettingsToneControlsFallback(t *testing.T) {
	c, rec := newRecordingBox(t, "tone-fallback-box", map[string]string{
		"/bass":                     `<bass deviceID="AABBCCDDEEFF"><targetbass>0</targetbass><actualbass>0</actualbass></bass>`,
		"/bassCapabilities":         bassCapsNone,
		"/capabilities":             capsWithTone,
		"/audioproducttonecontrols": toneControls,
	})
	s, err := c.LoadSettings(context.Background())
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	want := Bass{Target: 50, Actual: 50, Min: -100, Max: 100, Default: 0, Step: 50, Avail: true}
	if s.Bass != want {
		t.Errorf("Bass = %+v, want %+v", s.Bass, want)
	}

	// SetBass must now go through the gated POST, with the doc's
	// partial-update body (no <treble>, so it stays untouched). A FRESH
	// Client with the same host must route the same way: webui builds a new
	// Client per request, so the verdict may not live on the instance.
	c2 := &Client{Host: c.Host, HTTP: c.HTTP}
	if err := c2.SetBass(context.Background(), 100); err != nil {
		t.Fatalf("SetBass: %v", err)
	}
	if got := rec.lastBody("/audioproducttonecontrols"); got != `<audioproducttonecontrols><bass value="100" /></audioproducttonecontrols>` {
		t.Errorf("tone-controls POST body wrong: %q", got)
	}
	if rec.requested("/bass") != 1 { // the LoadSettings read; SetBass must not add one
		t.Errorf("classic /bass hit %d times, want 1 (read only)", rec.requested("/bass"))
	}
	// The gate verdict is cached: a second LoadSettings re-reads the values
	// but must not re-probe /capabilities.
	if _, err := c.LoadSettings(context.Background()); err != nil {
		t.Fatalf("second LoadSettings: %v", err)
	}
	if rec.requested("/capabilities") != 1 {
		t.Errorf("/capabilities probed %d times, want 1 (verdict is per-host cached)", rec.requested("/capabilities"))
	}
}

// The six one-piece fleet boxes report bassAvailable=true and must never pay
// the capability probe; SetBass keeps the classic POST byte for byte.
func TestLoadSettingsNoProbeWhenBassAvailable(t *testing.T) {
	c, rec := newRecordingBox(t, "classic-bass-box", map[string]string{
		"/bass":             bassClassicOK,
		"/bassCapabilities": bassCapsOK,
	})
	s, err := c.LoadSettings(context.Background())
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	want := Bass{Target: -4, Actual: -4, Min: -9, Max: 0, Default: 0, Step: 0, Avail: true}
	if s.Bass != want {
		t.Errorf("Bass = %+v, want %+v", s.Bass, want)
	}
	if n := rec.requested("/capabilities"); n != 0 {
		t.Errorf("/capabilities probed %d times on a classic box, want 0", n)
	}
	if err := c.SetBass(context.Background(), -5); err != nil {
		t.Fatalf("SetBass: %v", err)
	}
	if got := rec.lastBody("/bass"); got != `<bass>-5</bass>` {
		t.Errorf("classic POST body wrong: %q", got)
	}
	if n := rec.requested("/audioproducttonecontrols"); n != 0 {
		t.Errorf("tone-controls route hit %d times on a classic box, want 0", n)
	}
}

// Capability gate present but without audioproducttonecontrols: today's
// behavior byte for byte (greyed slider state), classic SetBass route, and
// the negative verdict is cached so the 30 s pollBoxInfo ticker does not
// re-probe the box every cycle.
func TestLoadSettingsCapabilityAbsentKeepsClassicState(t *testing.T) {
	c, rec := newRecordingBox(t, "no-tone-box", map[string]string{
		"/bass":             `<bass deviceID="AABBCCDDEEFF"><targetbass>0</targetbass><actualbass>0</actualbass></bass>`,
		"/bassCapabilities": bassCapsNone,
		"/capabilities":     capsNoTone,
	})
	s, err := c.LoadSettings(context.Background())
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	want := Bass{Target: 0, Actual: 0, Min: 0, Max: 0, Default: 0, Step: 0, Avail: false}
	if s.Bass != want {
		t.Errorf("Bass = %+v, want %+v", s.Bass, want)
	}
	if err := c.SetBass(context.Background(), 0); err != nil {
		t.Fatalf("SetBass: %v", err)
	}
	if got := rec.lastBody("/bass"); got != `<bass>0</bass>` {
		t.Errorf("classic POST body wrong: %q", got)
	}
	if _, err := c.LoadSettings(context.Background()); err != nil {
		t.Fatalf("second LoadSettings: %v", err)
	}
	if n := rec.requested("/capabilities"); n != 1 {
		t.Errorf("/capabilities probed %d times, want 1 (negative verdict cached)", n)
	}
}

// A tone-controls reply without a usable bass block (only treble, or a
// collapsed range) must not un-grey the slider.
func TestToneControlsBassRejectsUnusableBlocks(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"treble only", `<audioproducttonecontrols><treble value="0" minValue="-100" maxValue="100" step="50" /></audioproducttonecontrols>`},
		{"collapsed range", `<audioproducttonecontrols><bass value="0" minValue="0" maxValue="0" step="1" /></audioproducttonecontrols>`},
	}
	for _, tc := range cases {
		c, _ := newRecordingBox(t, "unusable-tone-box-"+tc.name, map[string]string{
			"/capabilities":             capsWithTone,
			"/audioproducttonecontrols": tc.body,
		})
		if got, ok := c.toneControlsBass(context.Background()); ok {
			t.Errorf("%s: toneControlsBass ok with %+v, want rejection", tc.name, got)
		}
	}
}

// A transport failure on the /capabilities probe must not store a verdict:
// the next LoadSettings may probe again (a booting box's :8090 answers
// nothing for a while, and that must not permanently misclassify it).
func TestToneControlsProbeFailureStoresNoVerdict(t *testing.T) {
	c, rec := newRecordingBox(t, "probe-fail-box", map[string]string{
		"/bass":             `<bass deviceID="AABBCCDDEEFF"><targetbass>0</targetbass><actualbass>0</actualbass></bass>`,
		"/bassCapabilities": bassCapsNone,
		// no /capabilities route: the probe gets a 404
	})
	if _, err := c.LoadSettings(context.Background()); err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if _, cached := toneControlsHosts.Load(c.Host); cached {
		t.Error("a failed probe must not cache a verdict")
	}
	if _, err := c.LoadSettings(context.Background()); err != nil {
		t.Fatalf("second LoadSettings: %v", err)
	}
	if n := rec.requested("/capabilities"); n != 2 {
		t.Errorf("/capabilities probed %d times, want 2 (no verdict cached on failure)", n)
	}
}

// The relative-slider contract the frontend depends on: LoadSettings keeps
// the classic default in Bass.Default, and the tone-controls path reports
// Default 0 so relative equals absolute there. Pinned here because
// settings.js computes slider positions from exactly these fields.
func TestBassDefaultMapping(t *testing.T) {
	c, _ := newRecordingBox(t, "default-mapping-box", map[string]string{
		"/bass":             bassClassicOK,
		"/bassCapabilities": `<bassCapabilities deviceID="AABBCCDDEEFF"><bassAvailable>true</bassAvailable><bassMin>-9</bassMin><bassMax>3</bassMax><bassDefault>-3</bassDefault></bassCapabilities>`,
	})
	s, err := c.LoadSettings(context.Background())
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if s.Bass.Default != -3 {
		t.Errorf("classic default = %d, want -3", s.Bass.Default)
	}
}
