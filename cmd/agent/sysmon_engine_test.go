// Tests for the engine-RSS instrument: the /proc status parser, the
// resource-health reason it adds, and the heartbeat fields it fills.

package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/spotify"
)

func TestReadProcStatus(t *testing.T) {
	p := filepath.Join(t.TempDir(), "status")
	if err := os.WriteFile(p, []byte("Name:\tgo-librespot\nVmPeak:\t   40000 kB\nVmRSS:\t   31240 kB\nThreads:\t9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rss, thr := readProcStatus(p)
	if rss != 31240 || thr != 9 {
		t.Fatalf("readProcStatus = %d/%d, want 31240/9", rss, thr)
	}
	if err := os.WriteFile(p, []byte("Name:\tx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rss, thr := readProcStatus(p); rss != -1 || thr != -1 {
		t.Fatalf("fields absent: %d/%d, want -1/-1", rss, thr)
	}
	if rss, thr := readProcStatus(filepath.Join(t.TempDir(), "missing")); rss != -1 || thr != -1 {
		t.Fatalf("file missing: %d/%d, want -1/-1", rss, thr)
	}
	if got := engineRSSKB(nil); got != -1 {
		t.Fatalf("engineRSSKB(nil) = %d, want -1", got)
	}
	// A manager that never started an engine has no pid.
	m := spotify.New("", t.TempDir(), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if got := engineRSSKB(m); got != -1 {
		t.Fatalf("engineRSSKB with no engine = %d, want -1", got)
	}
}

// The engine's footprint moving is its own reason to log: that is the
// reading that tells go-librespot holding a long track apart from the
// firmware's per-stream retention.
func TestResourceHealthEngineRSSMoved(t *testing.T) {
	const total = 120000
	t0 := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	resetResHealth()
	if why, ok := resourceHealthWorthLogging(60000, total, 11000, 9, 30000, t0); !ok || why != "first" {
		t.Fatalf("first: %q/%v", why, ok)
	}
	if _, ok := resourceHealthWorthLogging(60000, total, 11000, 9, 31000, t0.Add(time.Minute)); ok {
		t.Error("a 3 % engine drift was logged")
	}
	if why, ok := resourceHealthWorthLogging(60000, total, 11000, 9, 36000, t0.Add(2*time.Minute)); !ok || why != "engine-rss-moved" {
		t.Errorf("engine RSS +20 %%: why=%q ok=%v, want engine-rss-moved/true", why, ok)
	}
	// The engine going away (-1) and coming back both count as movement.
	if why, ok := resourceHealthWorthLogging(60000, total, 11000, 9, -1, t0.Add(3*time.Minute)); !ok || why != "engine-rss-moved" {
		t.Errorf("engine gone: why=%q ok=%v, want engine-rss-moved/true", why, ok)
	}
	if why, ok := resourceHealthWorthLogging(60000, total, 11000, 9, 30000, t0.Add(4*time.Minute)); !ok || why != "engine-rss-moved" {
		t.Errorf("engine back: why=%q ok=%v, want engine-rss-moved/true", why, ok)
	}
	if _, ok := resourceHealthWorthLogging(60000, total, 11000, 9, 30000, t0.Add(5*time.Minute)); ok {
		t.Error("a flat engine was logged")
	}
}

// The heartbeat carries the engine RSS and the seam count, and survives a
// nil manager (Spotify not configured).
func TestHeartbeatCarriesEngineRSS(t *testing.T) {
	hb := collectHeartbeat(nil)
	if hb.EngineRSSKB != -1 || hb.SpotifySeams != 0 || hb.SpotifyStreaming {
		t.Fatalf("nil manager heartbeat: %+v", hb)
	}
	b, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"engineRSSKB":-1`, `"spotifySeams":0`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("heartbeat JSON lacks %s: %s", key, b)
		}
	}
	m := spotify.New("", t.TempDir(), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	hb = collectHeartbeat(m)
	if hb.EngineRSSKB != -1 || hb.SpotifySeams != 0 || hb.SpotifyStreaming {
		t.Fatalf("idle manager heartbeat: %+v", hb)
	}
	// The debug section merges the same reading.
	snap := m.ChainSnapshot()
	snap["engineRSSKB"] = engineRSSKB(m)
	if snap["enginePID"] != 0 || snap["engineRSSKB"] != int64(-1) {
		t.Fatalf("spotify_chain section: %v", snap)
	}
}
