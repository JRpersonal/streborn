package webui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/updatesig"
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

// #1798: an update that came back on the old version had already deleted the
// Spotify engine to make room. The resume asks for it back; a resume that came
// back on the NEW version, or a pending update that carried no engine, does not
// go through this path.
func TestFailedSwapPutsTheDroppedEngineBack(t *testing.T) {
	dir := t.TempDir()
	origPending, origResult, origVer := selfUpdatePendingPath, selfUpdateResultPath, agentVersion
	t.Cleanup(func() {
		selfUpdatePendingPath, selfUpdateResultPath, agentVersion = origPending, origResult, origVer
	})
	selfUpdatePendingPath = filepath.Join(dir, "pending.json")
	selfUpdateResultPath = filepath.Join(dir, "last.json")
	var asked []string
	fx := productionOTAEffects
	fx.engineRestoreFn = func(_, version string, want updatesig.File, agentErr error) {
		asked = append(asked, version+" "+want.SHA256+" "+agentErr.Error())
	}
	engine := updatesig.File{Size: 2048, SHA256: "abc"}

	// Back on the old version, engine gone (the test host has no
	// /mnt/nv/streborn/bin/go-librespot): the restore is asked for.
	agentVersion = func() string { return "v1.5.4" }
	if err := writeJSONFile(selfUpdatePendingPath, selfUpdatePending{Version: "v1.5.5", From: "v1.5.4", Engine: &engine}); err != nil {
		t.Fatal(err)
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ota: &otaState{}, otaEffects: &fx}
	s.resumeSelfUpdate()
	if len(asked) != 1 || !strings.Contains(asked[0], "v1.5.5 abc") || !strings.Contains(asked[0], "instead of v1.5.5") {
		t.Fatalf("restore requests %q, want one for the v1.5.5 engine", asked)
	}

	// No engine in the pending update: nothing to put back.
	asked = nil
	if err := writeJSONFile(selfUpdatePendingPath, selfUpdatePending{Version: "v1.5.5", From: "v1.5.4"}); err != nil {
		t.Fatal(err)
	}
	s = &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ota: &otaState{}, otaEffects: &fx}
	s.resumeSelfUpdate()
	if len(asked) != 0 {
		t.Fatalf("restore asked for without an engine in the update: %q", asked)
	}
}
