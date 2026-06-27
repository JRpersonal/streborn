package spotify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// makeOggPage builds a minimal valid Ogg page (single segment, body < 255).
func makeOggPage(headerType byte, granule int64, body []byte) []byte {
	p := make([]byte, 0, 27+1+len(body))
	p = append(p, 'O', 'g', 'g', 'S')
	p = append(p, 0) // version
	p = append(p, headerType)
	var g [8]byte
	binary.LittleEndian.PutUint64(g[:], uint64(granule))
	p = append(p, g[:]...)
	p = append(p, 1, 2, 3, 4)             // serial
	p = append(p, 0, 0, 0, 0)             // page seq
	p = append(p, 0xDE, 0xAD, 0xBE, 0xEF) // crc (unchecked here)
	p = append(p, 1)                      // page_segments = 1
	p = append(p, byte(len(body)))
	p = append(p, body...)
	return p
}

func TestReadOggPageRoundtrip(t *testing.T) {
	pages := [][]byte{
		makeOggPage(0x02, 0, []byte("idheader")),
		makeOggPage(0x00, 0, []byte("setup")),
		makeOggPage(0x00, 1234, []byte("audio1")),
	}
	var stream []byte
	// prepend junk to exercise the OggS sync
	stream = append(stream, 0x11, 0x22, 0x33)
	for _, p := range pages {
		stream = append(stream, p...)
	}
	r := bufio.NewReader(bytes.NewReader(stream))
	for i, want := range pages {
		got, err := readOggPage(r)
		if err != nil {
			t.Fatalf("page %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("page %d mismatch: got %d bytes want %d", i, len(got), len(want))
		}
	}
}

// TestHeaderCapture replicates the drain's header-capture switch and checks
// it keeps exactly the BOS + granule<=0 pages and stops at the first audio
// page (granule>0).
func TestHeaderCapture(t *testing.T) {
	bos := makeOggPage(0x02, 0, []byte("id"))
	setup := makeOggPage(0x00, 0, []byte("comment+setup"))
	audio1 := makeOggPage(0x00, 100, []byte("audioA"))
	audio2 := makeOggPage(0x00, 200, []byte("audioB"))
	bos2 := makeOggPage(0x02, 0, []byte("id2")) // next track resets capture

	stream := bytes.Join([][]byte{bos, setup, audio1, audio2, bos2}, nil)
	r := bufio.NewReader(bytes.NewReader(stream))

	var hdr []byte
	capturing := false
	var committed []byte
	for {
		page, err := readOggPage(r)
		if err != nil {
			break
		}
		htype := page[5]
		gran := int64(binary.LittleEndian.Uint64(page[6:14]))
		switch {
		case htype&0x02 != 0:
			hdr = append([]byte(nil), page...)
			capturing = true
		case capturing && gran > 0:
			committed = hdr
			capturing = false
		case capturing:
			hdr = append(hdr, page...)
		}
	}
	want := append(append([]byte(nil), bos...), setup...)
	if !bytes.Equal(committed, want) {
		t.Errorf("captured headers = %d bytes, want %d (BOS+setup only)", len(committed), len(want))
	}
}

// TestLoggedIn verifies the #45 recall guard: a speaker with no persisted Spotify
// credential reports not-logged-in (so the recall returns a clear error), and
// either the active credentials.json or a per-account stored copy counts as
// logged in.
func TestLoggedIn(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "cfg")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	m := New("", cfg, "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if m.LoggedIn() {
		t.Fatal("LoggedIn should be false with no credential")
	}
	credFile := filepath.Join(cfg, "credentials.json")
	if err := os.WriteFile(credFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !m.LoggedIn() {
		t.Fatal("LoggedIn should be true once credentials.json exists")
	}
	if err := os.Remove(credFile); err != nil {
		t.Fatal(err)
	}
	if m.LoggedIn() {
		t.Fatal("LoggedIn should be false again after the credential is gone")
	}
	if err := os.MkdirAll(m.credStore, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.credStore, "user.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !m.LoggedIn() {
		t.Fatal("LoggedIn should be true with a per-account stored credential")
	}

	// state.json is the current go-librespot credential layout (credentials.json
	// is only a legacy read fallback). It counts as logged in when it carries a
	// persisted username, but a bare state.json written before any login (only
	// device_id/last_volume) must NOT count (a user diagnostic, 2026-06-23).
	dir2 := t.TempDir()
	cfg2 := filepath.Join(dir2, "cfg")
	if err := os.MkdirAll(cfg2, 0o755); err != nil {
		t.Fatal(err)
	}
	m2 := New("", cfg2, "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	stateFile := filepath.Join(cfg2, "state.json")
	if err := os.WriteFile(stateFile, []byte(`{"device_id":"d","last_volume":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if m2.LoggedIn() {
		t.Fatal("LoggedIn should be false for a state.json with no credential username")
	}
	if err := os.WriteFile(stateFile, []byte(`{"device_id":"d","credentials":{"username":"alice","data":"AA=="},"last_volume":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !m2.LoggedIn() {
		t.Fatal("LoggedIn should be true once state.json carries a credential username")
	}
}

// TestCanRecall verifies the recall-eligibility gate (#45; Patrick, ST10 rhino,
// 2026-06-24). Recall must be allowed when go-librespot holds a LIVE session even
// if no credential is persisted on disk (the box streams Spotify fine yet
// LoggedIn() is false, which previously refused the recall), and when a
// credential is persisted even if the session is momentarily down (ensureSession
// restarts go-librespot and re-auths from it). It is refused only when BOTH are
// false, which is the genuine "tap this speaker in Spotify once" case.
func TestRestartEngine(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := New("", t.TempDir(), "", nil, logger)

	// No engine running (runCancel nil): a delivered binary is picked up by the
	// supervise loop's waitForBinary, so RestartEngine must be a safe no-op and
	// never panic.
	m.RestartEngine()

	// Engine running: RestartEngine cancels the current process so the loop
	// re-execs the new binary. Stub runCancel and assert it fires.
	fired := false
	m.mu.Lock()
	m.runCancel = func() { fired = true }
	m.mu.Unlock()
	m.RestartEngine()
	if !fired {
		t.Fatal("RestartEngine should invoke runCancel when an engine is running")
	}
}

func TestStopEnginePausesAndResumes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := New("", t.TempDir(), "", nil, logger)

	// Not running: StopEngine still pauses the supervise loop so it cannot
	// relaunch the binary the caller is about to remove, and must not panic.
	m.StopEngine()
	m.mu.Lock()
	paused := m.enginePaused
	m.mu.Unlock()
	if !paused {
		t.Fatal("StopEngine should pause the supervise loop")
	}

	// RestartEngine clears the pause so the loop relaunches the re-delivered engine.
	m.RestartEngine()
	m.mu.Lock()
	paused = m.enginePaused
	m.mu.Unlock()
	if paused {
		t.Fatal("RestartEngine should clear the pause after re-delivery")
	}
}

func TestCanRecall(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// No persisted credential and no live session (/status unreachable): refuse.
	cfg := filepath.Join(t.TempDir(), "cfg")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	m := New("", cfg, "", nil, logger)
	m.apiAddr = "127.0.0.1:0" // nothing listening -> currentUsername == ""
	if m.CanRecall(ctx) {
		t.Fatal("CanRecall should be false with no credential and no live session")
	}

	// A live go-librespot session (mock /status reports a username) makes recall
	// possible even though nothing is persisted on disk: exactly the Patrick case
	// (streamed from the phone, go-librespot authenticated, nothing on NAND yet).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_, _ = w.Write([]byte(`{"username":"live-listener"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()
	m.apiAddr = strings.TrimPrefix(ts.URL, "http://")
	if m.LoggedIn() {
		t.Fatal("precondition: LoggedIn should be false (nothing persisted)")
	}
	if !m.CanRecall(ctx) {
		t.Fatal("CanRecall should be true with a live session even with no persisted credential")
	}

	// A persisted credential alone (session down) also allows recall.
	cfg2 := filepath.Join(t.TempDir(), "cfg")
	if err := os.MkdirAll(cfg2, 0o755); err != nil {
		t.Fatal(err)
	}
	m2 := New("", cfg2, "", nil, logger)
	m2.apiAddr = "127.0.0.1:0" // session down
	if err := os.WriteFile(filepath.Join(cfg2, "state.json"),
		[]byte(`{"credentials":{"username":"alice","data":"AA=="}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !m2.CanRecall(ctx) {
		t.Fatal("CanRecall should be true with a persisted credential even with no live session")
	}
}

// recordedCall is one go-librespot API request the mock server saw.
type recordedCall struct {
	path string
	body string
}

// mockLibrespot stands in for go-librespot's local HTTP API: it records every
// POST and answers /status with a loaded track so waitContextLoaded returns
// promptly. The returned cleanup closes the server.
func mockLibrespot(t *testing.T) (m *Manager, calls *[]recordedCall, cleanup func()) {
	t.Helper()
	var mu sync.Mutex
	var got []recordedCall
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_, _ = w.Write([]byte(`{"username":"u","track":{"uri":"spotify:track:cur","name":"Cur"}}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, recordedCall{path: r.URL.Path, body: string(b)})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	mgr := New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mgr.apiAddr = strings.TrimPrefix(ts.URL, "http://")
	return mgr, &got, ts.Close
}

func pathsOf(calls []recordedCall) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.path
	}
	return out
}

func bodyForPath(calls []recordedCall, path string) (string, bool) {
	for _, c := range calls {
		if c.path == path {
			return c.body, true
		}
	}
	return "", false
}

// TestPlayDefaultResumesWithoutShuffle is the core fix (Patrick + Jens,
// 2026-06-25): a default (non-shuffle) recall must (1) set shuffle OFF so the
// remote next/prev walk the playlist in order, (2) NOT skip to a random track,
// and (3) resume on the last-played track of that context via skip_to_uri.
func TestPlayDefaultResumesWithoutShuffle(t *testing.T) {
	m, calls, cleanup := mockLibrespot(t)
	defer cleanup()
	const ctxURI = "spotify:playlist:abc"
	m.resume.note(ctxURI, "spotify:track:resume-here")

	if err := m.Play(context.Background(), ctxURI, PlayOptions{Shuffle: false}); err != nil {
		t.Fatalf("Play: %v", err)
	}

	playBody, ok := bodyForPath(*calls, "/player/play")
	if !ok {
		t.Fatal("no /player/play call recorded")
	}
	if !strings.Contains(playBody, `"skip_to_uri":"spotify:track:resume-here"`) {
		t.Errorf("default recall must resume via skip_to_uri, play body = %s", playBody)
	}
	shufBody, ok := bodyForPath(*calls, "/player/shuffle_context")
	if !ok || !strings.Contains(shufBody, `"shuffle_context":false`) {
		t.Errorf("default recall must set shuffle OFF, got %q ok=%v", shufBody, ok)
	}
	for _, p := range pathsOf(*calls) {
		if p == "/player/next" {
			t.Error("default recall must NOT skip to a random track (/player/next)")
		}
	}
}

// TestPlayShuffleStartsRandom verifies a shuffle preset still gets the random
// start: shuffle ON + one /player/next, and it ignores any resume point.
func TestPlayShuffleStartsRandom(t *testing.T) {
	m, calls, cleanup := mockLibrespot(t)
	defer cleanup()
	const ctxURI = "spotify:playlist:abc"
	m.resume.note(ctxURI, "spotify:track:ignored-when-shuffling")

	if err := m.Play(context.Background(), ctxURI, PlayOptions{Shuffle: true}); err != nil {
		t.Fatalf("Play: %v", err)
	}

	playBody, _ := bodyForPath(*calls, "/player/play")
	if strings.Contains(playBody, "skip_to_uri") {
		t.Errorf("shuffle recall must NOT resume a track, play body = %s", playBody)
	}
	shufBody, ok := bodyForPath(*calls, "/player/shuffle_context")
	if !ok || !strings.Contains(shufBody, `"shuffle_context":true`) {
		t.Errorf("shuffle recall must set shuffle ON, got %q ok=%v", shufBody, ok)
	}
	sawNext := false
	for _, p := range pathsOf(*calls) {
		if p == "/player/next" {
			sawNext = true
		}
	}
	if !sawNext {
		t.Error("shuffle recall must skip once (/player/next) to land on a random track")
	}
}

// TestNoteResumeContextTracking locks in the review fix (2026-06-25): Play must
// point lastContext at the context it is loading (so noteResume never records a
// track under a stale previous context), noteResume must suppress recording
// while a recall is in flight, and record correctly once it settles.
func TestNoteResumeContextTracking(t *testing.T) {
	m, _, cleanup := mockLibrespot(t)
	defer cleanup()
	const ctxURI = "spotify:playlist:abc"

	// Pre-set a different context as if a previous playlist had been playing.
	m.mu.Lock()
	m.lastContext = "spotify:playlist:OLD"
	m.curTrackURI = "spotify:track:from-old-playlist"
	m.mu.Unlock()

	if err := m.Play(context.Background(), ctxURI, PlayOptions{Shuffle: false}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	// Play must retarget the resume context and drop the stale track.
	m.mu.Lock()
	gotCtx, gotTrack := m.lastContext, m.curTrackURI
	m.mu.Unlock()
	if gotCtx != ctxURI {
		t.Errorf("Play must set lastContext to the loaded context, got %q", gotCtx)
	}
	if gotTrack != "" {
		t.Errorf("Play must clear the stale curTrackURI, got %q", gotTrack)
	}

	// Still inside the recall window: noteResume must not record anything.
	m.mu.Lock()
	m.curTrackURI = "spotify:track:settling"
	m.mu.Unlock()
	m.noteResume()
	if got := m.resume.trackFor(ctxURI); got != "" {
		t.Errorf("noteResume must not record during a recall, got %q", got)
	}

	// Recall window over: the now-stable track records under the right context.
	m.mu.Lock()
	m.recallUntil = time.Time{}
	m.curTrackURI = "spotify:track:playing-now"
	m.mu.Unlock()
	m.noteResume()
	if got := m.resume.trackFor(ctxURI); got != "spotify:track:playing-now" {
		t.Errorf("noteResume after the recall window = %q, want spotify:track:playing-now", got)
	}
}

// TestResumeStore covers the per-context resume memory: it records the last
// track per context, ignores non-spotify and unchanged values, evicts the
// least-recently-used beyond the cap, and round-trips through NAND.
func TestResumeStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sp-resume.json")
	s := newResumeStore(path, slog.New(slog.NewTextHandler(io.Discard, nil)))

	s.note("spotify:playlist:a", "spotify:track:1")
	if got := s.trackFor("spotify:playlist:a"); got != "spotify:track:1" {
		t.Fatalf("trackFor = %q, want spotify:track:1", got)
	}
	// Non-spotify URIs are ignored.
	s.note("http://x", "spotify:track:9")
	s.note("spotify:playlist:b", "not-a-uri")
	if s.trackFor("http://x") != "" || s.trackFor("spotify:playlist:b") != "" {
		t.Error("resume store must ignore non-spotify context/track URIs")
	}
	// Update to a new track on the same context.
	s.note("spotify:playlist:a", "spotify:track:2")
	if got := s.trackFor("spotify:playlist:a"); got != "spotify:track:2" {
		t.Fatalf("trackFor after update = %q, want spotify:track:2", got)
	}

	// Eviction: fill past the cap; the oldest, untouched context drops out.
	s2 := newResumeStore(filepath.Join(t.TempDir(), "r.json"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	first := "spotify:playlist:keep0"
	s2.note(first, "spotify:track:x")
	for i := 0; i < resumeMaxContexts+5; i++ {
		s2.note("spotify:playlist:c"+string(rune('a'+i%26))+string(rune('a'+i/26)), "spotify:track:y")
	}
	if s2.trackFor(first) != "" {
		t.Error("oldest context should have been evicted past the cap")
	}

	// Persistence round-trips through NAND.
	s.flush()
	reloaded := newResumeStore(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if got := reloaded.trackFor("spotify:playlist:a"); got != "spotify:track:2" {
		t.Fatalf("reloaded trackFor = %q, want spotify:track:2", got)
	}
}

// TestStateCredentialExportCapture covers the multi-account credential plumbing
// on a current go-librespot: the credential lives in state.json (.credentials),
// export/capture must read it there (not the absent credentials.json), and
// writing a new active account must preserve the rest of state.json. This is the
// path that copy-login-to-all and per-preset account switching depend on.
func TestStateCredentialExportCapture(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "cfg")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	m := New("", cfg, "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	stateFile := filepath.Join(cfg, "state.json")
	// data "YWxpY2UtdG9rZW4=" is base64("alice-token").
	state := `{"device_id":"dev123","credentials":{"username":"alice","data":"YWxpY2UtdG9rZW4="},"last_volume":55}`
	if err := os.WriteFile(stateFile, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}

	// Export pulls the credential out of state.json (credentials.json is absent).
	blob, err := m.ExportCredential()
	if err != nil {
		t.Fatalf("ExportCredential: %v", err)
	}
	var got storedCredential
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("export blob: %v", err)
	}
	if got.Username != "alice" || string(got.Data) != "alice-token" {
		t.Fatalf("export = %q/%q, want alice/alice-token", got.Username, string(got.Data))
	}

	// Capture stores it under the account key for a later SwitchAccount.
	if err := m.captureCredential("alice"); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sp-accounts", "alice.json")); err != nil {
		t.Fatalf("captured credential missing: %v", err)
	}

	// Switching the active account must preserve device_id and last_volume.
	if err := m.writeActiveCredential(storedCredential{Username: "bob", Data: []byte("bob-token")}); err != nil {
		t.Fatalf("writeActiveCredential: %v", err)
	}
	b, _ := os.ReadFile(stateFile)
	var after map[string]json.RawMessage
	if err := json.Unmarshal(b, &after); err != nil {
		t.Fatalf("reparse state: %v", err)
	}
	if string(after["device_id"]) != `"dev123"` {
		t.Errorf("device_id not preserved: %s", after["device_id"])
	}
	if string(after["last_volume"]) != "55" {
		t.Errorf("last_volume not preserved: %s", after["last_volume"])
	}
	cred, ok := m.readStateCredential()
	if !ok || cred.Username != "bob" || string(cred.Data) != "bob-token" {
		t.Fatalf("active credential after write = %+v ok=%v, want bob/bob-token", cred, ok)
	}
}
