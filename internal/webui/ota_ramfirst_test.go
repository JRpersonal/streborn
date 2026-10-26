package webui

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// pinDeferredRoute makes the route decision refuse RAM first (MemAvailable
// unknown), so a test written for the deferred NAND path stays on it even on
// a Linux host where /dev/shm and /proc/meminfo report real room.
func pinDeferredRoute(t *testing.T) {
	t.Helper()
	orig := memAvailableKBFn
	t.Cleanup(func() { memAvailableKBFn = orig })
	memAvailableKBFn = func() int64 { return -1 }
}

// ramFirstRig is a tight NAND (deferredReclaimRig) on a speaker with a
// scripted RAM stage and MemAvailable. The engine stop hook records the state
// of the stage and the NAND temp at the moment of the drop.
type ramFirstRig struct {
	*deferredReclaimRig
	stage          string
	stageAtDrop    int64
	tmpExistedAtDr bool
}

func newRAMFirstRig(t *testing.T, stageAvail, memKB int64) *ramFirstRig {
	t.Helper()
	base := newDeferredReclaimRig(t, nandWriteMargin+1<<20, true)
	rig := &ramFirstRig{deferredReclaimRig: base, stageAtDrop: -1}
	origStage, origAvail, origMem, origCopy := ramStageTarget, ramStageAvailFn, memAvailableKBFn, stageToNAND
	t.Cleanup(func() {
		ramStageTarget, ramStageAvailFn, memAvailableKBFn, stageToNAND = origStage, origAvail, origMem, origCopy
	})
	rig.stage = filepath.Join(t.TempDir(), "streborn-ota.stage")
	ramStageTarget = rig.stage
	ramStageAvailFn = func(string) (int64, bool) { return stageAvail, true }
	memAvailableKBFn = func() int64 { return memKB }
	baseHook := engineStopHook
	engineStopHook = func() bool {
		if fi, err := os.Stat(rig.stage); err == nil {
			rig.stageAtDrop = fi.Size()
		}
		_, err := os.Stat(rig.dst + ".new")
		rig.tmpExistedAtDr = err == nil
		return baseHook()
	}
	return rig
}

func assertGone(t *testing.T, what, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s must be gone, stat err = %v", what, err)
	}
}

// The 2026-10-26 BCO case: tight NAND, plenty of RAM, the transfer dies after
// 3 MB. On the RAM-first route the engine is never touched.
func TestRAMFirstAbortMidBodyKeepsEngineAndRemovesStage(t *testing.T) {
	const size = 18 << 20
	rig := newRAMFirstRig(t, 60<<20, 60<<10)
	src := &failingReader{r: bytes.NewReader(patternBytes(size)), left: 3 << 20}
	_, err := receiveAgentBinary(rig.dst, src, size, 30<<20)
	var rerr uploadReadError
	if !errors.As(err, &rerr) {
		t.Fatalf("an aborted body must surface as a read error, got %v", err)
	}
	if rig.drops != 0 {
		t.Fatalf("the engine (and with it the dropped-for-update marker) was touched for an aborted upload: %d drops", rig.drops)
	}
	assertGone(t, "the RAM stage", rig.stage)
	assertGone(t, "the NAND temp", rig.dst+".new")
	assertGone(t, "dst", rig.dst)
}

// A complete body drops the engine only after its last byte is staged, then
// installs through the normal NAND path and removes the stage.
func TestRAMFirstCompleteBodyDropsEngineAfterLastByteAndInstalls(t *testing.T) {
	body := patternBytes(5 << 20)
	rig := newRAMFirstRig(t, 60<<20, 60<<10)
	got, err := receiveAgentBinary(rig.dst, bytes.NewReader(body), int64(len(body)), 30<<20)
	if err != nil {
		t.Fatalf("the update must complete, got %v", err)
	}
	if got.ramStaged || got.size != int64(len(body)) || got.sum != sumHex(body) {
		t.Fatalf("unexpected result %+v", got)
	}
	if rig.drops != 1 {
		t.Fatalf("the engine must be dropped exactly once, got %d", rig.drops)
	}
	if rig.stageAtDrop != int64(len(body)) {
		t.Errorf("engine dropped with %d of %d bytes staged; it must wait for the last byte", rig.stageAtDrop, len(body))
	}
	if rig.tmpExistedAtDr {
		t.Error("the NAND temp was written before the engine drop")
	}
	if on, _ := os.ReadFile(rig.dst); !bytes.Equal(on, body) {
		t.Error("dst content mismatch")
	}
	assertGone(t, "the RAM stage", rig.stage)
	assertGone(t, "the NAND temp", rig.dst+".new")
}

// Too little RAM stage or MemAvailable falls back to the deferred drop at the
// NAND margin, and the stage is never created.
func TestRAMFirstFallsBackToDeferredWhenRAMIsShort(t *testing.T) {
	const size = 3 << 20
	cases := []struct {
		name       string
		stageAvail int64
		memKB      int64
	}{
		{"stage too small", size + ramFirstStageSlack - 1, 60 << 10},
		{"memory below floor", 60 << 20, (size+ramFirstMemFloor)/1024 - 1},
		{"memory unknown", 60 << 20, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newRAMFirstRig(t, c.stageAvail, c.memKB)
			body := patternBytes(size)
			got, err := receiveAgentBinary(rig.dst, bytes.NewReader(body), size, 30<<20)
			if err != nil || got.ramStaged {
				t.Fatalf("the deferred route must complete on the NAND, got %+v, %v", got, err)
			}
			if rig.drops != 1 || rig.stageAtDrop != -1 {
				t.Fatalf("want one drop with no stage, got drops=%d stageAtDrop=%d", rig.drops, rig.stageAtDrop)
			}
			// Deferred: dropped when the NAND temp reached the margin, not up front.
			if rig.writtenAtDrop <= 0 || rig.writtenAtDrop > 1<<20 {
				t.Errorf("engine dropped after %d bytes on NAND, want the deferred drop near 1 MB", rig.writtenAtDrop)
			}
			if on, _ := os.ReadFile(rig.dst); !bytes.Equal(on, body) {
				t.Error("dst content mismatch")
			}
			assertGone(t, "the RAM stage", rig.stage)
		})
	}
}

// The NAND refuses the copy even after the drop: the complete stage goes to
// the tier-3 swap instead of failing the update.
func TestRAMFirstNANDCopyFailureTakesTheSwapPath(t *testing.T) {
	body := patternBytes(2 << 20)
	rig := newRAMFirstRig(t, 60<<20, 60<<10)
	stageToNAND = func(_, tmp string, _ []byte) error {
		_ = os.WriteFile(tmp, body[:1<<20], 0o600) // a partial copy, as ENOSPC leaves it
		return &os.PathError{Op: "write", Path: tmp, Err: syscall.ENOSPC}
	}
	got, err := receiveAgentBinary(rig.dst, bytes.NewReader(body), int64(len(body)), 30<<20)
	if err != nil {
		t.Fatalf("a full NAND after the drop must take the swap path, got %v", err)
	}
	if !got.ramStaged || got.size != int64(len(body)) || got.sum != sumHex(body) {
		t.Fatalf("want a ramStaged result for the swap, got %+v", got)
	}
	if rig.drops != 1 {
		t.Fatalf("want the engine dropped once before the copy, got %d", rig.drops)
	}
	if staged, _ := os.ReadFile(rig.stage); !bytes.Equal(staged, body) {
		t.Error("the swap needs the complete binary in the RAM stage")
	}
	assertGone(t, "the partial NAND temp", rig.dst+".new")
	assertGone(t, "dst", rig.dst)
}
