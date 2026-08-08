package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// listenPort is the port half of an httptest server's address.
func listenPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse %q: %v", srv.URL, err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("port of %q: %v", srv.URL, err)
	}
	return p
}

// newTestApp is an App with just enough wired up to make an agent HTTP call.
func newTestApp() *App {
	return &App{httpClient: &http.Client{}}
}

// deadPort returns a port nothing is listening on: a server is started and
// immediately closed, so the number is real and certainly refused.
func deadPort(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	p := listenPort(t, srv)
	srv.Close()
	return p
}

func TestNotTheAgent(t *testing.T) {
	body := func(status int, b string) *http.Response {
		r := &http.Response{
			StatusCode:    status,
			Body:          io.NopCloser(strings.NewReader(b)),
			ContentLength: int64(len(b)),
		}
		if b == "" {
			r.ContentLength = 0
		}
		return r
	}
	cases := []struct {
		name   string
		resp   *http.Response
		path   string
		want   bool
		reason string
	}{
		{"bare 400", body(400, ""), "/api/agent/version", true,
			"the field report's shape: a firmware listener answering with a status and nothing else"},
		{"400 with a reason is ours", body(400, "not an ELF binary\n"), "/api/agent/update", false,
			"every refusal the agent writes goes through http.Error and carries text"},
		{"404 on /api/ is the stock firmware", body(404, ""), "/api/agent/version", true,
			"Bose on :8090 answers unknown /api/ paths with 404"},
		{"404 with a body on /api/ is still not ours", body(404, "404 page not found\n"), "/api/agent/version", true,
			"404 on our own namespace means the port does not serve our routes"},
		{"200 is the agent", body(200, `{"version":"v0.9.35"}`), "/api/agent/version", false, "a real answer"},
		{"bare 500 is the agent struggling", body(500, ""), "/api/agent/version", false,
			"the agent does return bodiless 5xx; sending the app elsewhere then would be wrong"},
		{"non-api paths are not ours to judge", body(400, ""), "/index.html", false,
			"other software on the box legitimately serves those"},
		{"nil response", nil, "/api/agent/version", false, "no evidence"},
	}
	for _, c := range cases {
		if got := notTheAgent(c.resp, c.path); got != c.want {
			t.Errorf("%s: notTheAgent = %v, want %v (%s)", c.name, got, c.want, c.reason)
		}
	}
}

// TestBodyIsEmptyLeavesTheBodyReadable guards the thing that would be silently
// destructive: the fallback response is handed to the caller, so peeking at it
// must not eat the first byte.
func TestBodyIsEmptyLeavesTheBodyReadable(t *testing.T) {
	resp := &http.Response{
		StatusCode:    400,
		Body:          io.NopCloser(strings.NewReader("not an ELF binary")),
		ContentLength: -1, // unknown, the case that has to peek
	}
	if bodyIsEmpty(resp) {
		t.Fatal("a body with content reported as empty")
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "not an ELF binary" {
		t.Errorf("body after peek = %q, want it intact", got)
	}
}

// TestBareStatusDoesNotCaptureTheHost is the field report of 2026-08-07: a
// listener that is not the agent answers a bodiless 400, and from then on the
// app talked to nothing else for two days across three app versions. The agent
// was on the other port the whole time.
func TestBareStatusDoesNotCaptureTheHost(t *testing.T) {
	var strangerHits, agentHits int

	stranger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		strangerHits++
		w.WriteHeader(http.StatusBadRequest) // no body, exactly as reported
	}))
	defer stranger.Close()

	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentHits++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"version":"v0.9.35","nandFreeBytes":"22921216"}`)
	}))
	defer agent.Close()

	agentPort := listenPort(t, agent)
	old := altAgentPortFor
	altAgentPortFor = func(int) int { return agentPort }
	defer func() { altAgentPortFor = old }()

	a := newTestApp()
	resp, err := a.boxDo("127.0.0.1", listenPort(t, stranger), http.MethodGet, "/api/agent/version", "", "")
	if err != nil {
		t.Fatalf("boxDo: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: the bare 400 ended the search instead of moving to the next port", resp.StatusCode)
	}
	if agentHits != 1 {
		t.Errorf("agent was asked %d times, want 1", agentHits)
	}
	if strangerHits != 1 {
		t.Errorf("stranger was asked %d times, want 1", strangerHits)
	}
	if p, ok := a.cachedPort("127.0.0.1"); !ok || p != agentPort {
		t.Errorf("cached port = %d (present=%v), want the agent's %d: caching the stranger is what made this last two days",
			p, ok, agentPort)
	}
}

// TestAgentRefusalIsNotMistakenForAStranger is the other half. The agent's own
// 400 (the OTA upload checks produce one) must reach the caller unchanged, and
// the agent's port must stay cached.
func TestAgentRefusalIsNotMistakenForAStranger(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not an ELF binary", http.StatusBadRequest)
	}))
	defer agent.Close()

	old := altAgentPortFor
	altAgentPortFor = func(int) int { return deadPort(t) }
	defer func() { altAgentPortFor = old }()

	a := newTestApp()
	port := listenPort(t, agent)
	resp, err := a.boxDo("127.0.0.1", port, http.MethodGet, "/api/agent/update", "", "")
	if err != nil {
		t.Fatalf("boxDo: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want the agent's own 400 passed through", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "not an ELF binary") {
		t.Errorf("body = %q, want the agent's reason intact", b)
	}
	if p, ok := a.cachedPort("127.0.0.1"); !ok || p != port {
		t.Errorf("cached port = %d (present=%v), want %d: a real agent refusal must not lose the port", p, ok, port)
	}
}

// TestStrangerIsStillReturnedWhenNothingElseAnswers keeps the fallback honest:
// skipping a port must not turn "the box said 400" into "the box said nothing",
// which would put the firewall advice back in front of the user.
func TestStrangerIsStillReturnedWhenNothingElseAnswers(t *testing.T) {
	stranger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer stranger.Close()

	old := altAgentPortFor
	altAgentPortFor = func(int) int { return deadPort(t) }
	defer func() { altAgentPortFor = old }()

	a := newTestApp()
	resp, err := a.boxDo("127.0.0.1", listenPort(t, stranger), http.MethodGet, "/api/agent/version", "", "")
	if err != nil {
		t.Fatalf("boxDo returned an error instead of the only answer there was: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want the 400 surfaced", resp.StatusCode)
	}
	if _, ok := a.cachedPort("127.0.0.1"); ok {
		t.Error("the stranger's port was cached even as a last resort; the next call would go straight back to it")
	}
}

// TestAltAgentPortIsTheOtherRealPort runs without the seam installed, so the
// shipped mapping itself is checked rather than the test's stand-in.
func TestAltAgentPortIsTheOtherRealPort(t *testing.T) {
	if got := altAgentPort(8888); got != 17008 {
		t.Errorf("altAgentPort(8888) = %d, want 17008", got)
	}
	if got := altAgentPort(17008); got != 8888 {
		t.Errorf("altAgentPort(17008) = %d, want 8888", got)
	}
	if altAgentPortFor(8888) != altAgentPort(8888) {
		t.Error("the seam does not default to the real mapping")
	}
}
