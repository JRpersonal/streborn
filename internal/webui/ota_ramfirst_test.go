package webui

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
// of the RAM-first stage and the NAND temp at the moment of the drop.
type ramFirstRig struct {
	*deferredReclaimRig
	stageDir       string
	stage          string // the shared swap stage (ramStageTarget)
	stageAtDrop    int64
	tmpExistedAtDr bool
}

func newRAMFirstRig(t *testing.T, stageAvail, memKB int64) *ramFirstRig {
	t.Helper()
	base := newDeferredReclaimRig(t, nandWriteMargin+1<<20, true)
	rig := &ramFirstRig{deferredReclaimRig: base, stageAtDrop: -1}
	origStage, origAvail, origMem, origCopy := ramStageTarget, ramStageAvailFn, memAvailableKBFn, stageToNAND
	origArmed, origFailed := swapArmed.Load(), ramFirstStageFailed.Load()
	t.Cleanup(func() {
		ramStageTarget, ramStageAvailFn, memAvailableKBFn, stageToNAND = origStage, origAvail, origMem, origCopy
		swapArmed.Store(origArmed)
		ramFirstStageFailed.Store(origFailed)
	})
	swapArmed.Store(false)
	ramFirstStageFailed.Store(false)
	rig.stageDir = t.TempDir()
	rig.stage = filepath.Join(rig.stageDir, "streborn-ota.stage")
	ramStageTarget = rig.stage
	ramStageAvailFn = func(string) (int64, bool) { return stageAvail, true }
	memAvailableKBFn = func() int64 { return memKB }
	baseHook := engineStopHook
	engineStopHook = func() bool {
		for _, p := range ramFirstStages(t, rig.stageDir) {
			if fi, err := os.Stat(p); err == nil && fi.Size() > rig.stageAtDrop {
				rig.stageAtDrop = fi.Size()
			}
		}
		_, err := os.Stat(rig.dst + ".new")
		rig.tmpExistedAtDr = err == nil
		return baseHook()
	}
	return rig
}

// ramFirstStages lists the RAM-first stage files in dir.
func ramFirstStages(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read stage dir: %v", err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ramFirstStagePrefix) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func assertGone(t *testing.T, what, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s must be gone, stat err = %v", what, err)
	}
}

func assertNoRAMFirstStages(t *testing.T, dir string) {
	t.Helper()
	if left := ramFirstStages(t, dir); len(left) > 0 {
		t.Errorf("RAM-first stages left behind: %v", left)
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
	assertNoRAMFirstStages(t, rig.stageDir)
	assertGone(t, "the swap stage", rig.stage)
	assertGone(t, "the NAND temp", rig.dst+".new")
	assertGone(t, "dst", rig.dst)
}

// A complete body drops the engine only after its last byte is staged, then
// installs through the normal NAND path. The stage is kept for the flash
// verify and goes once it is released.
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
	if got.ramFirstStage == "" {
		t.Fatal("the stage must be handed back for the flash verify")
	}
	if staged, _ := os.ReadFile(got.ramFirstStage); !bytes.Equal(staged, body) {
		t.Error("the kept stage must hold the complete binary")
	}
	assertGone(t, "the NAND temp", rig.dst+".new")
	releaseRAMFirstStage(got.ramFirstStage)
	assertNoRAMFirstStages(t, rig.stageDir)
}

// A flash verify mismatch after a RAM-first receive is repaired from the kept
// stage, without the app sending the binary again.
func TestRAMFirstVerifyMismatchIsRecopiedFromTheStage(t *testing.T) {
	body := patternBytes(2 << 20)
	rig := newRAMFirstRig(t, 60<<20, 60<<10)
	got, err := receiveAgentBinary(rig.dst, bytes.NewReader(body), int64(len(body)), 30<<20)
	if err != nil || got.ramFirstStage == "" {
		t.Fatalf("want a RAM-first install with its stage kept, got %+v, %v", got, err)
	}
	defer releaseRAMFirstStage(got.ramFirstStage)
	if err := os.WriteFile(rig.dst, body[:1<<20], 0o600); err != nil {
		t.Fatal(err)
	}
	if verifyBinaryOnFlash(rig.dst, got.sum) == nil {
		t.Fatal("the damaged dst must fail the verify")
	}
	if err := reinstallFromRAMFirstStage(got.ramFirstStage, rig.dst); err != nil {
		t.Fatalf("re-copy from the stage: %v", err)
	}
	if err := verifyBinaryOnFlash(rig.dst, got.sum); err != nil {
		t.Fatalf("the re-copied dst must pass the verify: %v", err)
	}
	assertGone(t, "the NAND temp", rig.dst+".new")
}

// stallingReader hands out one chunk, signals, then blocks until released and
// fails like a link that finally timed out.
type stallingReader struct {
	chunk   []byte
	started chan struct{}
	release chan struct{}
}

func (s *stallingReader) Read(p []byte) (int, error) {
	if len(s.chunk) > 0 {
		n := copy(p, s.chunk)
		s.chunk = s.chunk[n:]
		if len(s.chunk) == 0 {
			close(s.started)
		}
		return n, nil
	}
	<-s.release
	return 0, errors.New("connection timed out")
}

// Review finding 1: a request stalled mid-body and the app's retry overlap.
// The retry must not delete the stalled request's stage, and the stalled
// request failing must not delete the retry's stage.
func TestRAMFirstOverlappingUploadsKeepEachOthersStage(t *testing.T) {
	const size = 6 << 20
	rig := newRAMFirstRig(t, 60<<20, 60<<10)

	a := &stallingReader{chunk: patternBytes(1 << 20), started: make(chan struct{}), release: make(chan struct{})}
	aDone := make(chan error, 1)
	go func() {
		_, err := receiveAgentBinary(rig.dst, a, size, 30<<20)
		aDone <- err
	}()
	<-a.started

	body := patternBytes(size)
	pr, pw := io.Pipe()
	type res struct {
		got receivedAgent
		err error
	}
	bDone := make(chan res, 1)
	go func() {
		got, err := receiveAgentBinary(rig.dst, pr, size, 30<<20)
		bDone <- res{got, err}
	}()
	if _, err := pw.Write(body[:2<<20]); err != nil {
		t.Fatal(err)
	}
	if n := len(ramFirstStages(t, rig.stageDir)); n != 2 {
		t.Fatalf("want both receives staging side by side, found %d stages", n)
	}
	// The stalled request finally gives up while the retry is mid-body.
	close(a.release)
	var rerr uploadReadError
	if err := <-aDone; !errors.As(err, &rerr) {
		t.Fatalf("the stalled request must end in a read error, got %v", err)
	}
	if _, err := pw.Write(body[2<<20:]); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close()
	b := <-bDone
	if b.err != nil {
		t.Fatalf("the retry must install, got %v", b.err)
	}
	defer releaseRAMFirstStage(b.got.ramFirstStage)
	if b.got.sum != sumHex(body) {
		t.Fatal("the retry's sum does not match its body")
	}
	if on, _ := os.ReadFile(rig.dst); !bytes.Equal(on, body) {
		t.Error("dst must hold the retry's binary")
	}
	if rig.drops != 1 {
		t.Errorf("only the completed retry may drop the engine, got %d drops", rig.drops)
	}
	if left := ramFirstStages(t, rig.stageDir); len(left) != 1 || left[0] != b.got.ramFirstStage {
		t.Errorf("only the retry's stage may remain, found %v", left)
	}
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
			if err != nil || got.ramStaged || got.ramFirstStage != "" {
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
			assertGone(t, "the swap stage", rig.stage)
			assertNoRAMFirstStages(t, rig.stageDir)
		})
	}
}

// Review finding 5: a stage that cannot be written leaves the engine alone,
// and the retry takes the deferred route instead of hitting the same wall.
func TestRAMFirstStageWriteFailureFallsBackToDeferredOnRetry(t *testing.T) {
	const size = 3 << 20
	rig := newRAMFirstRig(t, 60<<20, 60<<10)
	// A stage directory that cannot be written into.
	ramStageTarget = filepath.Join(rig.stageDir, "missing", "streborn-ota.stage")
	body := patternBytes(size)
	_, err := receiveAgentBinary(rig.dst, bytes.NewReader(body), size, 30<<20)
	if !errors.Is(err, errRAMFirstStageWrite) {
		t.Fatalf("want the stage write failure, got %v", err)
	}
	if rig.drops != 0 {
		t.Fatalf("a failed stage write must not touch the engine, got %d drops", rig.drops)
	}
	got, err := receiveAgentBinary(rig.dst, bytes.NewReader(body), size, 30<<20)
	if err != nil || got.ramFirstStage != "" || got.ramStaged {
		t.Fatalf("the retry must complete on the deferred route, got %+v, %v", got, err)
	}
	if rig.writtenAtDrop <= 0 || rig.writtenAtDrop > 1<<20 {
		t.Errorf("engine dropped after %d bytes on NAND, want the deferred drop near 1 MB", rig.writtenAtDrop)
	}
}

// The NAND refuses the copy even after the drop: the complete stage is handed
// to the tier-3 swap path instead of failing the update.
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
		t.Error("the swap needs the complete binary at the swap stage path")
	}
	assertNoRAMFirstStages(t, rig.stageDir)
	assertGone(t, "the partial NAND temp", rig.dst+".new")
	assertGone(t, "dst", rig.dst)
}

// With a swap already armed, a RAM-first receive must not replace the stage
// the running helper reads.
func TestRAMFirstNeverReplacesAnArmedSwapStage(t *testing.T) {
	body := patternBytes(2 << 20)
	rig := newRAMFirstRig(t, 60<<20, 60<<10)
	armed := []byte("the armed swap's binary")
	if err := os.WriteFile(rig.stage, armed, 0o600); err != nil {
		t.Fatal(err)
	}
	swapArmed.Store(true)
	stageToNAND = func(_, tmp string, _ []byte) error {
		return &os.PathError{Op: "write", Path: tmp, Err: syscall.ENOSPC}
	}
	got, err := receiveAgentBinary(rig.dst, bytes.NewReader(body), int64(len(body)), 30<<20)
	if !errors.Is(err, errInsufficientNAND) || got.ramStaged {
		t.Fatalf("want the space failure without a second swap, got %+v, %v", got, err)
	}
	if on, _ := os.ReadFile(rig.stage); !bytes.Equal(on, armed) {
		t.Error("the armed swap's stage was touched")
	}
	assertNoRAMFirstStages(t, rig.stageDir)
}

// Review finding 2: the startup cleanup removes a dead agent's RAM-first
// stage, and the swap stage only when no helper is reading it.
func TestCleanStaleRAMStages(t *testing.T) {
	_ = newRAMFirstRig(t, 60<<20, 60<<10)
	stageDir := filepath.Dir(ramStageTarget)
	origProc := procRoot
	t.Cleanup(func() { procRoot = origProc })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	write := func(p string) {
		t.Helper()
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dead := filepath.Join(stageDir, ramFirstStagePrefix+"1-1")

	// A helper is running: its command line names the swap stage.
	proc := t.TempDir()
	procRoot = proc
	if err := os.MkdirAll(filepath.Join(proc, "4242"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "4242", "cmdline"), []byte("sh\x00-c\x00cp "+ramStageTarget+" /x"), 0o600); err != nil {
		t.Fatal(err)
	}
	write(dead)
	write(ramStageTarget)
	CleanStaleRAMStages(logger)
	assertGone(t, "the dead agent's RAM-first stage", dead)
	if _, err := os.Stat(ramStageTarget); err != nil {
		t.Errorf("the swap stage must stay while a helper reads it: %v", err)
	}

	// No helper: the swap stage goes too.
	procRoot = t.TempDir()
	CleanStaleRAMStages(logger)
	assertGone(t, "the orphaned swap stage", ramStageTarget)

	// An unreadable process table keeps the swap stage.
	write(ramStageTarget)
	procRoot = filepath.Join(t.TempDir(), "missing")
	CleanStaleRAMStages(logger)
	if _, err := os.Stat(ramStageTarget); err != nil {
		t.Errorf("the swap stage must stay when /proc cannot be read: %v", err)
	}
}
