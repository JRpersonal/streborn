package spotify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Discussion #1930 (ST10): a few stream attempts were refused, then six preset
// presses in a row all played, and the apps kept saying Spotify refuses the
// account over a track that was playing, for the rest of the ten-minute
// window. Real audio is the proof that the account plays, so it ends the
// short-lived notice too, not only the sticky per-account one.

// letTimePass moves every refusal timestamp one second into the past, order
// kept, so whatever the test notes next is strictly later. Windows' coarse
// clock can otherwise hand a refusal noted right after a clear the very same
// instant, which on a speaker never happens (seconds pass between a stream and
// the next refusal) but would make these tests flaky.
func letTimePass(m *Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range []*time.Time{&m.engineKeyDeniedAt, &m.lastKeyRefusalAt, &m.keyRefusalGaveUpAt, &m.refusalEpisodeAt, &m.keyRefusalClearedAt} {
		if !t.IsZero() {
			*t = t.Add(-time.Second)
		}
	}
}

// The older engine: a run of refused tracks, then a track that plays.
func TestAudioEndsTheRefusalNoticeOldEngine(t *testing.T) {
	m := refusalManager()
	for i := 0; i < keyRefusalRunTrips+2; i++ {
		m.noteLibrespotLine(refusalLine)
	}
	m.noteLibrespotLine(`level=warning msg="stopping after 7 consecutive unplayable tracks"`)
	if !m.AudioKeyRefused() {
		t.Fatal("a fresh run was not reported")
	}
	m.noteAccountStreamed()
	if m.AudioKeyRefused() {
		t.Fatal("the refusal notice was still reported while the engine delivered audio")
	}
}

// The newer engine: its own key_denied report, then a track that plays.
func TestAudioEndsTheRefusalNoticeNewEngine(t *testing.T) {
	m, _ := recordingEngine(t)
	m.noteLibrespotLine(engineStopLine)
	if !m.AudioKeyRefused() {
		t.Fatal("the engine's stop was not reported")
	}
	m.noteAccountStreamed()
	if m.AudioKeyRefused() {
		t.Fatal("the refusal notice was still reported while the engine delivered audio")
	}
}

// What the back-off exists for still works after a clear: a newer engine that
// is refused again right after a track played is reported at once, inside the
// run gap of its previous report.
func TestARefusalRightAfterAudioIsReportedNewEngine(t *testing.T) {
	m, _ := recordingEngine(t)
	m.noteLibrespotLine(engineStopLine)
	m.noteAccountStreamed()
	letTimePass(m)
	m.noteLibrespotLine(engineStopLine)
	if !m.AudioKeyRefused() {
		t.Fatal("a refusal after the audio was swallowed by the clear")
	}
}

// The same for an older engine: a fresh run after the audio trips the notice
// and the agent's pause again.
func TestARefusalRunRightAfterAudioIsReportedOldEngine(t *testing.T) {
	m, paths := recordingEngine(t)
	for i := 0; i < keyRefusalRunTrips; i++ {
		m.noteLibrespotLine(refusalLine)
	}
	m.noteAccountStreamed()
	letTimePass(m)
	for i := 0; i < keyRefusalRunTrips; i++ {
		m.noteLibrespotLine(refusalLine)
	}
	if !m.AudioKeyRefused() {
		t.Fatal("a new run of refusals after the audio was not reported")
	}
	waitUntil(t, "the agent to pause the engine for both runs", func() bool {
		n := 0
		for _, p := range paths() {
			if p == "/player/pause" {
				n++
			}
		}
		return n == 2
	})
}

// A track that plays breaks the row: refusals on either side of it do not add
// up to a run, however close together they are.
func TestAudioBreaksARefusalRun(t *testing.T) {
	m := refusalManager()
	for i := 0; i < keyRefusalRunTrips-1; i++ {
		m.noteLibrespotLine(refusalLine)
	}
	m.noteAccountStreamed()
	m.noteLibrespotLine(refusalLine)
	if m.AudioKeyRefused() {
		t.Fatal("refusals before and after a track that played were counted as one run")
	}
}

// Only the display ends. The recall path still asks whether the engine refused
// a play that just failed, and that answer is about what happened.
func TestAudioKeepsTheRefusalHistory(t *testing.T) {
	m, _ := recordingEngine(t)
	before := time.Now().Add(-time.Second)
	m.noteLibrespotLine(engineStopLine)
	m.noteAccountStreamed()
	if !m.engineKeyDeniedSince(before) {
		t.Fatal("the clear erased the engine's refusal from the recall path")
	}
}

// After audio, a refusal run is a new episode even inside the run gap, so an
// account that played and is refused again is recorded again.
func TestARefusalAfterAudioIsANewEpisode(t *testing.T) {
	m, eng := refusedManager(t, refusedUser)
	m.noteLibrespotLine(engineStopLine)
	waitUntil(t, "the refused account to be recorded", func() bool { return isRefused(m, refusedUser) })
	waitForRefusedFile(t, m)
	m.noteAccountStreamed()
	waitUntil(t, "the refusal to be cleared", func() bool {
		_, err := os.Stat(filepath.Join(m.credStore, accountRefusedFile))
		return !isRefused(m, refusedUser) && os.IsNotExist(err)
	})
	letTimePass(m)
	m.noteLibrespotLine(engineStopLine)
	waitUntil(t, "the second refusal to be recorded", func() bool { return isRefused(m, refusedUser) })
	waitForRefusedFile(t, m)
	if n := eng.statuses.Load(); n < 3 {
		t.Fatalf("engine asked %d times, want a lookup per episode plus the clearing one", n)
	}
}

// With nothing refused, a track start leaves the state alone: no clear stamp,
// no log line per track.
func TestAudioWithoutARefusalChangesNothing(t *testing.T) {
	m := refusalManager()
	m.noteAccountStreamed()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.keyRefusalClearedAt.IsZero() {
		t.Fatal("a track start without any refusal stamped a clear")
	}
}

// The spotify_auth debug section carries the refusal state, as plain values
// that survive the debug state's JSON encoding.
func TestCredentialSnapshotCarriesTheRefusalState(t *testing.T) {
	m := refusalManager()
	for i := 0; i < keyRefusalRunTrips; i++ {
		m.noteLibrespotLine(refusalLine)
	}
	snap := m.CredentialSnapshot()
	if snap["audioKeyRefused"] != true {
		t.Errorf("audioKeyRefused = %v, want true", snap["audioKeyRefused"])
	}
	if snap["keyRefusalRun"] != keyRefusalRunTrips {
		t.Errorf("keyRefusalRun = %v, want %d", snap["keyRefusalRun"], keyRefusalRunTrips)
	}
	if s, _ := snap["lastKeyRefusalAt"].(string); s == "" {
		t.Error("lastKeyRefusalAt is empty after a run of refusals")
	}
	for _, k := range []string{"engineKeyDeniedAt", "keyRefusalGaveUpAt", "keyRefusalClearedAt", "engineStopsOnKeyDenial"} {
		if _, ok := snap[k]; !ok {
			t.Errorf("%s is missing from spotify_auth", k)
		}
	}

	m.noteAccountStreamed()
	snap = m.CredentialSnapshot()
	if snap["audioKeyRefused"] != false || snap["keyRefusalRun"] != 0 {
		t.Errorf("after audio: audioKeyRefused = %v, keyRefusalRun = %v", snap["audioKeyRefused"], snap["keyRefusalRun"])
	}
	if s, _ := snap["keyRefusalClearedAt"].(string); s == "" {
		t.Error("keyRefusalClearedAt is empty after audio cleared a refusal")
	}

	// Encoded the way /api/debug/state encodes it.
	blob, err := json.Marshal(map[string]any{"spotify_auth": snap})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]map[string]any
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatal(err)
	}
	if back["spotify_auth"]["keyRefusalRun"] != float64(0) {
		t.Errorf("keyRefusalRun did not survive encoding: %v", back["spotify_auth"]["keyRefusalRun"])
	}
}

// /spotify/info, what the apps read: audioKeyRefused goes false with the audio.
func TestSpotifyInfoDropsAudioKeyRefusedOnAudio(t *testing.T) {
	m, _, cleanup := mockLibrespot(t)
	defer cleanup()
	// The refusal and the audio each record the account in the background
	// (noteKeyRefusalEpisode, noteAccountStreamed). A write still running when
	// the test returns races TempDir's cleanup ("sp-accounts: directory not
	// empty", flaky in CI twice on 2026-11-21), so wait for both to land.
	settle := func(done func() bool) {
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && !done(); {
			time.Sleep(10 * time.Millisecond)
		}
	}
	refusedFile := func() bool { _, err := os.Stat(m.accountRefusedPath()); return err == nil }
	defer settle(func() bool {
		m.mu.Lock()
		busy := m.refusedClearBusy
		m.mu.Unlock()
		if busy || refusedFile() {
			return false
		}
		m.storeWriteMu.Lock()
		m.storeWriteMu.Unlock() //nolint:staticcheck // waits out a write in flight
		return true
	})
	m.noteLibrespotLine(engineStopLine)
	settle(refusedFile)
	read := func() any {
		w := httptest.NewRecorder()
		m.ServeInfo(w, httptest.NewRequest(http.MethodGet, "/spotify/info", nil))
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("not JSON: %v (%s)", err, strings.TrimSpace(w.Body.String()))
		}
		return got["audioKeyRefused"]
	}
	if got := read(); got != true {
		t.Fatalf("audioKeyRefused = %v right after the refusal", got)
	}
	m.noteAccountStreamed()
	if got := read(); got != false {
		t.Fatalf("audioKeyRefused = %v while the engine delivers audio", got)
	}
}
