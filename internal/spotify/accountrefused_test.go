package spotify

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	refusedUser = "qm4xvp7z2bnkd6rt1ys8hgwj3"
	otherUser   = "zz9plural7z0alpha8beta4gam"
)

// refusedEngine is a fake engine logged in as *user, counting /status reads.
type refusedEngine struct {
	mu       sync.Mutex
	user     string
	statuses atomic.Int32
}

func (e *refusedEngine) setUser(u string) {
	e.mu.Lock()
	e.user = u
	e.mu.Unlock()
}

// refusedManager is a manager with a real per-account store on disk and a
// fake engine next door.
func refusedManager(t *testing.T, user string) (*Manager, *refusedEngine) {
	t.Helper()
	eng := &refusedEngine{user: user}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			eng.statuses.Add(1)
			eng.mu.Lock()
			u := eng.user
			eng.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"username": u})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	m := &Manager{
		credStore: t.TempDir(),
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		client:    ts.Client(),
		apiAddr:   strings.TrimPrefix(ts.URL, "http://"),
	}
	return m, eng
}

// waitFor polls cond for up to two seconds: the episode is recorded in the
// background, off the engine's log path.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func isRefused(m *Manager, user string) bool {
	ok, _ := m.accountRefusedFor(user)
	return ok
}

// The older engine skips through the playlist; the agent's own run counter
// decides the episode, and that is when the account is remembered.
func TestARefusalRunRemembersTheAccount(t *testing.T) {
	m, _ := refusedManager(t, refusedUser)
	for i := 0; i < 51; i++ {
		m.noteLibrespotLine(refusalLine)
	}
	waitUntil(t, "the refused account to be recorded", func() bool { return isRefused(m, refusedUser) })
	// The memory entry is set before the file is written (markAccountRefused),
	// so the file gets the same wait; a bare stat here raced the write on CI.
	waitUntil(t, "the refusal to reach the NAND, so a reboot keeps it", func() bool {
		_, err := os.Stat(filepath.Join(m.credStore, accountRefusedFile))
		return err == nil
	})
}

// The newer engine stops after three refusals and says so itself; that report
// is the episode there.
func TestTheEngineStopLineRemembersTheAccount(t *testing.T) {
	m, _ := refusedManager(t, refusedUser)
	m.noteLibrespotLine(refusalLine)
	m.noteLibrespotLine(refusalLine)
	m.noteLibrespotLine(engineStopLine)
	waitUntil(t, "the refused account to be recorded", func() bool { return isRefused(m, refusedUser) })
}

// A single refused track is one unavailable track. Nothing is remembered.
func TestASingleRefusalRemembersNothing(t *testing.T) {
	m, eng := refusedManager(t, refusedUser)
	m.noteLibrespotLine(refusalLine)
	time.Sleep(100 * time.Millisecond)
	if isRefused(m, refusedUser) {
		t.Fatal("one refused track marked the whole account as refused")
	}
	if n := eng.statuses.Load(); n != 0 {
		t.Fatalf("a single refusal asked the engine who is logged in %d times", n)
	}
}

// One episode is one lookup, however many refusals and reports it contains:
// the event, the log line and every refused track after the trip.
func TestOneEpisodeIsOneLookup(t *testing.T) {
	m, eng := refusedManager(t, refusedUser)
	data, _ := json.Marshal(map[string]any{"kind": "key_denied", "message": "x"})
	for i := 0; i < 20; i++ {
		m.noteLibrespotLine(refusalLine)
		m.noteLibrespotLine(engineStopLine)
		m.handleEnginePlaybackError(data)
	}
	waitUntil(t, "the refused account to be recorded", func() bool { return isRefused(m, refusedUser) })
	time.Sleep(150 * time.Millisecond)
	if n := eng.statuses.Load(); n != 1 {
		t.Fatalf("one refusal episode asked the engine %d times, want once", n)
	}
}

// The flash wears out: an account already recorded is never written again,
// whatever the next episodes do.
func TestAKnownRefusalIsNotWrittenAgain(t *testing.T) {
	m, _ := refusedManager(t, refusedUser)
	path := filepath.Join(m.credStore, accountRefusedFile)
	m.markAccountRefused(refusedUser)
	marker := []byte(`{"` + refusedUser + `":"2026-11-01T00:00:00Z"}` + "\n")
	if err := os.WriteFile(path, marker, 0o644); err != nil {
		t.Fatal(err)
	}
	m.markAccountRefused(refusedUser)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(marker) {
		t.Fatalf("a refusal already recorded was written again: %s", got)
	}
}

// Sticky means across a reboot too.
func TestARefusalSurvivesARestart(t *testing.T) {
	m, _ := refusedManager(t, refusedUser)
	m.markAccountRefused(refusedUser)
	m2 := &Manager{credStore: m.credStore, logger: m.logger}
	if !isRefused(m2, refusedUser) {
		t.Fatal("the refusal did not survive a restart")
	}
}

// The account playing is what clears it, and only for that account.
func TestAudioFromTheAccountClearsIt(t *testing.T) {
	m, _ := refusedManager(t, refusedUser)
	m.markAccountRefused(refusedUser)
	m.noteAccountStreamed()
	waitUntil(t, "the refusal to be cleared", func() bool { return !isRefused(m, refusedUser) })
	if _, err := os.Stat(filepath.Join(m.credStore, accountRefusedFile)); !os.IsNotExist(err) {
		t.Fatalf("an empty store was left on the NAND: %v", err)
	}
}

// Another account of the household playing says nothing about the refused
// one, and the refused one's notice must not appear on the other.
func TestAnotherAccountPlayingLeavesTheRefusal(t *testing.T) {
	m, eng := refusedManager(t, refusedUser)
	m.markAccountRefused(refusedUser)
	eng.setUser(otherUser)
	m.noteAccountStreamed()
	waitUntil(t, "the clearing lookup to finish", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return eng.statuses.Load() > 0 && !m.refusedClearBusy
	})
	if !isRefused(m, refusedUser) {
		t.Fatal("audio from another account cleared the refused one")
	}
	if isRefused(m, otherUser) {
		t.Fatal("an account that plays is reported as refused")
	}
}

// With nothing recorded, a track start costs nothing: no lookup at all.
func TestAudioWithNothingRecordedAsksNobody(t *testing.T) {
	m, eng := refusedManager(t, refusedUser)
	for i := 0; i < 10; i++ {
		m.noteAccountStreamed()
	}
	time.Sleep(100 * time.Millisecond)
	if n := eng.statuses.Load(); n != 0 {
		t.Fatalf("track starts with no refusal recorded asked the engine %d times", n)
	}
}

// The drain's first audio page is the signal, through the real drain step.
func TestTheDrainsFirstAudioPageClearsIt(t *testing.T) {
	chain := newTestChain(t, 1<<40, nil)
	m, d, _ := newDrainHarness(t, chain, 1)
	eng := &refusedEngine{user: refusedUser}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eng.statuses.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"username": refusedUser})
	}))
	t.Cleanup(ts.Close)
	m.credStore = t.TempDir()
	m.client = ts.Client()
	m.apiAddr = strings.TrimPrefix(ts.URL, "http://")
	m.markAccountRefused(refusedUser)

	feed(d, synthVorbisTrack(0x61, 4, false))
	waitUntil(t, "the drain's audio to clear the refusal", func() bool { return !isRefused(m, refusedUser) })
}

// /spotify/info carries the sticky flag and the account's name, for the
// account logged in now only, beside the unchanged audioKeyRefused.
func TestSpotifyInfoCarriesTheStickyRefusal(t *testing.T) {
	m, _, cleanup := mockLibrespot(t) // the mock engine is logged in as "u"
	defer cleanup()
	m.rememberAccountName("u", "Alex")
	m.markAccountRefused("u")

	read := func() map[string]any {
		w := httptest.NewRecorder()
		m.ServeInfo(w, httptest.NewRequest("GET", "/spotify/info", nil))
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("not JSON: %v (%s)", err, w.Body.String())
		}
		return got
	}
	got := read()
	if got["accountRefused"] != true {
		t.Fatalf("accountRefused = %v for the refused account logged in now", got["accountRefused"])
	}
	if got["accountRefusedName"] != "Alex" {
		t.Errorf("accountRefusedName = %v, want the remembered name", got["accountRefusedName"])
	}
	if _, ok := got["audioKeyRefused"]; !ok {
		t.Error("audioKeyRefused is gone; older apps read it")
	}

	m.clearAccountRefused("u")
	m.markAccountRefused(otherUser)
	got = read()
	if got["accountRefused"] != false {
		t.Errorf("accountRefused = %v while the refused account is not the one logged in", got["accountRefused"])
	}
	if _, ok := got["accountRefusedName"]; ok {
		t.Error("a name was sent for an account that is not refused")
	}
}
