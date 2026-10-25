package webui

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func withEngineRestoreTiming(t *testing.T, every, waitFor time.Duration, reachable func(context.Context) bool) {
	t.Helper()
	origEvery, origWait, origReach := engineRestoreProbeEvery, engineRestoreWaitFor, fleetReleaseReachable
	t.Cleanup(func() {
		engineRestoreProbeEvery, engineRestoreWaitFor, fleetReleaseReachable = origEvery, origWait, origReach
	})
	engineRestoreProbeEvery, engineRestoreWaitFor, fleetReleaseReachable = every, waitFor, reachable
}

// #1339: the engine an update dropped comes back once the network does, not
// only when the owner starts another update. Nothing is tried while the
// release host is out of reach.
func TestEngineRestoreWaitsForTheNetwork(t *testing.T) {
	probes := 0
	withEngineRestoreTiming(t, time.Millisecond, time.Second, func(context.Context) bool {
		probes++
		return probes > 5
	})
	tries := 0
	err := retryWhenOnline(context.Background(), func() error { tries++; return nil })
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if tries != 1 || probes != 6 {
		t.Fatalf("tries %d probes %d, want one try once the network was back", tries, probes)
	}
}

// A network error right after the network looked back (DNS still coming up)
// is asked again; a real failure is not.
func TestEngineRestoreRetriesOnlyNetworkErrors(t *testing.T) {
	withEngineRestoreTiming(t, time.Millisecond, time.Second, func(context.Context) bool { return true })
	tries := 0
	err := retryWhenOnline(context.Background(), func() error {
		tries++
		switch tries {
		case 1:
			return &net.DNSError{Err: "server misbehaving", Name: "github.com"}
		case 2:
			return errEngineRestoreBusy
		}
		return nil
	})
	if err != nil || tries != 3 {
		t.Fatalf("err %v after %d tries, want success on the third", err, tries)
	}

	tries = 0
	bad := errors.New("the engine does not match the signed release")
	err = retryWhenOnline(context.Background(), func() error { tries++; return bad })
	if !errors.Is(err, bad) || tries != 1 {
		t.Fatalf("err %v after %d tries, want the hash failure once", err, tries)
	}
}

// A network that never comes back ends the wait without a single try.
func TestEngineRestoreGivesUp(t *testing.T) {
	withEngineRestoreTiming(t, time.Millisecond, 20*time.Millisecond, func(context.Context) bool { return false })
	tries := 0
	if err := retryWhenOnline(context.Background(), func() error { tries++; return nil }); err == nil || tries != 0 {
		t.Fatalf("err %v tries %d", err, tries)
	}
}

// runSelfUpdate starts the wait only for a restore that failed on the network.
func TestFailedUpdateStartsTheEngineRestoreOnlyForANetworkError(t *testing.T) {
	b, err := os.ReadFile("selfupdate.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "if isNetworkError(eerr) {") || !strings.Contains(src, "go s.restoreEngineWhenOnline(base, ix.Version, *p.Engine, agentErr)") {
		t.Fatal("runSelfUpdate no longer hands a network-failed engine restore to restoreEngineWhenOnline")
	}
}
