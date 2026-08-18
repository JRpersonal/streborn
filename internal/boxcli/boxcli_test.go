package boxcli

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// fakeBox stands in for the speaker on its two fixed local ports: the BoseApp
// REST API on :8090 (/now_playing) and the TAP CLI on :17000 (sys power). The
// ports are hardcoded in boxcli, so the test binds them on the loopback host;
// if they are unavailable it skips rather than failing CI.
//
// /now_playing reports STANDBY while the box is asleep, and reports a normal
// source once it has either woken on its own (selfWoke) or received a `sys
// power` toggle (powerCmd > 0) — mirroring the toggle semantics of the real CLI.
type fakeBox struct {
	httpLn   net.Listener
	cliLn    net.Listener
	selfWoke atomic.Bool  // the box left standby on its own (user button press)
	powerCmd atomic.Int32 // number of `sys power` commands received
	frozen   atomic.Bool  // BoseApp wedge: :8090 accepts TCP but never answers
}

func (b *fakeBox) awake() bool { return b.selfWoke.Load() || b.powerCmd.Load() > 0 }

func startFakeBox(t *testing.T) *fakeBox {
	t.Helper()
	httpLn, err := net.Listen("tcp", "127.0.0.1:8090")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.1:8090 (in use?): %v", err)
	}
	cliLn, err := net.Listen("tcp", "127.0.0.1:17000")
	if err != nil {
		_ = httpLn.Close()
		t.Skipf("cannot bind 127.0.0.1:17000 (in use?): %v", err)
	}
	b := &fakeBox{httpLn: httpLn, cliLn: cliLn}

	mux := http.NewServeMux()
	mux.HandleFunc("/now_playing", func(w http.ResponseWriter, r *http.Request) {
		if b.frozen.Load() {
			<-r.Context().Done() // hold the reply until the client gives up
			return
		}
		src := "STANDBY"
		if b.awake() {
			src = "UPNP"
		}
		_, _ = w.Write([]byte(`<nowPlaying source="` + src + `"></nowPlaying>`))
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(httpLn) }()

	go func() {
		for {
			conn, err := cliLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				if line, _ := bufio.NewReader(c).ReadString('\n'); line != "" {
					b.powerCmd.Add(1)
				}
			}(conn)
		}
	}()

	t.Cleanup(func() {
		_ = srv.Close()
		_ = cliLn.Close()
	})
	return b
}

// TestWakeAndWaitDoesNotToggleSelfWake is the regression guard for the
// overnight-standby "power button does nothing / box switches off again" reports
// (ST30 Klaus, ST20 #197, #183): when the box leaves standby on its own (the
// user pressed the physical button), WakeAndWait must NOT send a `sys power`
// toggle, because that would cancel the user's wake.
func TestWakeAndWaitDoesNotToggleSelfWake(t *testing.T) {
	b := startFakeBox(t)

	// Simulate the box waking itself ~600ms in, well within the self-wake grace.
	go func() {
		time.Sleep(600 * time.Millisecond)
		b.selfWoke.Store(true)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := WakeAndWait(ctx, "127.0.0.1", 6*time.Second, nil); err != nil {
		t.Fatalf("WakeAndWait returned error on a self-waking box: %v", err)
	}
	if n := b.powerCmd.Load(); n != 0 {
		t.Fatalf("WakeAndWait sent %d `sys power` toggle(s) to a self-waking box; want 0 (toggling cancels the user's wake)", n)
	}
}

// TestWakeAndWaitWithholdsToggleOnUnknownState pins the 2026-08-18 field
// failure: an ST20 led a PLAYING 2-box zone, its BoseApp :8090 froze (accepts
// TCP, never answers), and the wake helper — unable to read the state — sent
// the `sys power` toggle anyway, switching the playing master OFF and
// dissolving the group. A state that cannot be read must never be toggled:
// WakeAndWait has to return an error without a single `sys power` going out.
func TestWakeAndWaitWithholdsToggleOnUnknownState(t *testing.T) {
	b := startFakeBox(t)
	b.frozen.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := WakeAndWait(ctx, "127.0.0.1", 4*time.Second, nil); err == nil {
		t.Fatalf("WakeAndWait reported success against a frozen :8090; want an unreadable-state error")
	}
	if n := b.powerCmd.Load(); n != 0 {
		t.Fatalf("WakeAndWait sent %d `sys power` toggle(s) while the box state was unreadable; want 0 (a blind toggle switches a playing box off)", n)
	}
}

// TestWakeAndWaitTogglesWhenAsleep confirms a genuinely asleep box (no user
// wake) still gets a `sys power` toggle after the grace, so STR-initiated wakes
// (e.g. an app play on an idle box) keep working. The fake box leaves standby
// only once it receives that toggle.
func TestWakeAndWaitTogglesWhenAsleep(t *testing.T) {
	b := startFakeBox(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := WakeAndWait(ctx, "127.0.0.1", 8*time.Second, nil); err != nil {
		t.Fatalf("WakeAndWait returned error on an asleep box: %v", err)
	}
	if n := b.powerCmd.Load(); n < 1 {
		t.Fatalf("WakeAndWait sent %d `sys power` toggle(s) to an asleep box; want >=1", n)
	}
}
