package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// installProbe points the copy's readiness check at a caller-controlled answer,
// because the real one reaches the agent on two fixed firmware ports that
// httptest cannot hand out.
func installProbe(t *testing.T, ready func() bool) {
	t.Helper()
	copyTargetProbe = func(*App, string, int) bool { return ready() }
	t.Cleanup(func() { copyTargetProbe = nil })
}

// fakeBox serves the preset API for both ends of a copy and records the writes
// it accepted.
type fakeBox struct {
	mu      sync.Mutex
	srv     *httptest.Server
	host    string
	written map[int]string
	fail    func(slot int) bool
}

// newFakeBox binds on a caller-chosen loopback address, because a copy refuses
// to run when source and target share a host and httptest hands out 127.0.0.1
// for everything.
func newFakeBox(t *testing.T, host string, source []Preset) *fakeBox {
	t.Helper()
	b := &fakeBox{written: map[int]string{}, host: host}
	b.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == presetAPIPath:
			_ = json.NewEncoder(w).Encode(source)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, presetAPIPath+"/"):
			var p Preset
			_ = json.NewDecoder(r.Body).Decode(&p)
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.fail != nil && b.fail(p.Slot) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			b.written[p.Slot] = p.Name
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	l, err := net.Listen("tcp", host+":0")
	if err != nil {
		t.Fatalf("listen on %s: %v", host, err)
	}
	b.srv.Listener.Close()
	b.srv.Listener = l
	b.srv.Start()
	t.Cleanup(b.srv.Close)
	return b
}

func (b *fakeBox) port(t *testing.T) int { return listenPort(t, b.srv) }

func (b *fakeBox) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.written)
}

// copyApp is an App wired for a copy: an HTTP client, a logger, and a live
// context the settle loop can select on.
func copyApp(t *testing.T) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := newTestApp()
	a.logger = slog.Default()
	a.ctx = ctx
	return a
}

// TestCopyWaitsForATargetThatIsStillBooting is the 2026-08-08 field bundle: the
// user updated a speaker, the update rebooted it, and the copy started 72
// seconds later. Every slot failed against an agent that was not listening yet
// and the speaker was left with an empty store.
func TestCopyWaitsForATargetThatIsStillBooting(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", []Preset{
		{Slot: 1, Name: "1LIVE", Type: "radio"},
		{Slot: 2, Name: "WDR 5", Type: "radio"},
		{Slot: 3, Name: "Sunshine Live", Type: "radio"},
	})
	dst := newFakeBox(t, "127.0.0.3", nil)

	// A booting speaker does not politely refuse a write, it is not there at
	// all. So the target rejects everything until it is ready, exactly as the
	// field bundle shows, and the presets are genuinely lost if the copy does
	// not wait.
	var ready bool
	probes := 0
	dst.fail = func(int) bool { return !ready }
	installProbe(t, func() bool {
		probes++
		ready = probes > 2 // still starting for the first two probes
		return ready
	})

	a := copyApp(t)
	n, err := a.CopyPresetsAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t))
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if n != 3 || dst.count() != 3 {
		t.Errorf("copied %d presets, target holds %d, want 3 and 3: the store was left empty because the copy did not wait", n, dst.count())
	}
	if probes < 3 {
		t.Errorf("readiness was probed %d times, want it to have waited and retried", probes)
	}
}

// TestCopyRefusesRatherThanEmptyingTheStore: when the target never comes back,
// the user must get one clear refusal instead of six separate slot failures and
// a speaker that looks wiped.
func TestCopyRefusesRatherThanEmptyingTheStore(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", []Preset{{Slot: 1, Name: "1LIVE", Type: "radio"}})
	dst := newFakeBox(t, "127.0.0.3", nil)
	installProbe(t, func() bool { return false })

	a := copyApp(t)
	// Cancel the app context so the settle loop gives up at once instead of
	// spending its full window; the code path taken is the same.
	ctx, cancel := context.WithCancel(context.Background())
	a.ctx = ctx
	cancel()

	n, err := a.CopyPresetsAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t))
	if err == nil {
		t.Fatal("copy reported success against a target that never answered")
	}
	if n != 0 {
		t.Errorf("reported %d copied presets, want 0", n)
	}
	if dst.count() != 0 {
		t.Errorf("wrote %d presets to a target that was not ready", dst.count())
	}
	if strings.Contains(strings.ToLower(err.Error()), "firewall") {
		t.Errorf("blames the firewall for a speaker the app was just talking to: %v", err)
	}
}

// TestCopyIsUnchangedWhenTheTargetIsReady guards the happy path: no waiting, no
// extra probing, all slots copied.
func TestCopyIsUnchangedWhenTheTargetIsReady(t *testing.T) {
	var source []Preset
	for i := 1; i <= 6; i++ {
		source = append(source, Preset{Slot: i, Name: fmt.Sprintf("Station %d", i), Type: "radio"})
	}
	src := newFakeBox(t, "127.0.0.2", source)
	dst := newFakeBox(t, "127.0.0.3", nil)
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	start := time.Now()
	n, err := a.CopyPresetsAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t))
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if n != 6 || dst.count() != 6 {
		t.Errorf("copied %d, target holds %d, want 6 and 6", n, dst.count())
	}
	if el := time.Since(start); el > copyTargetSettleStep {
		t.Errorf("a ready target cost %v, want no settle wait at all", el)
	}
}

// TestCopyStillReportsARealRejection: a slot the target genuinely refuses (a
// 500, not a transport failure) must still be reported. The settle-wait must
// not swallow real errors.
func TestCopyStillReportsARealRejection(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", []Preset{
		{Slot: 1, Name: "1LIVE", Type: "radio"},
		{Slot: 2, Name: "WDR 5", Type: "radio"},
	})
	dst := newFakeBox(t, "127.0.0.3", nil)
	dst.fail = func(slot int) bool { return slot == 2 }
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	n, err := a.CopyPresetsAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t))
	if err == nil {
		t.Fatal("a refused slot was not reported")
	}
	if n != 1 {
		t.Errorf("copied %d, want the one slot that was accepted", n)
	}
	if !strings.Contains(err.Error(), "preset 2") {
		t.Errorf("error does not name the refused slot: %v", err)
	}
}
