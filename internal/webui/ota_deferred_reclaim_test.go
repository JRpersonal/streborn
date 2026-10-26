package webui

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// deferredReclaimRig scripts a tight volume for the deferred engine drop:
// statfs predicts no room until the engine is dropped, and the free figure
// shrinks as the temp file grows from `free`. engineStopHook stands in for the
// drop and records how much of the body had reached the temp file by then.
type deferredReclaimRig struct {
	dst           string
	drops         int
	writtenAtDrop int64
}

func newDeferredReclaimRig(t *testing.T, free int64, availOK bool) *deferredReclaimRig {
	t.Helper()
	rig := &deferredReclaimRig{dst: filepath.Join(t.TempDir(), "streborn-armv7l"), writtenAtDrop: -1}
	origRoom, origAvail, origHook := nandHasRoom, nandAvailFn, engineStopHook
	t.Cleanup(func() { nandHasRoom, nandAvailFn, engineStopHook = origRoom, origAvail, origHook })
	tmpSize := func() int64 {
		if fi, err := os.Stat(rig.dst + ".new"); err == nil {
			return fi.Size()
		}
		return 0
	}
	nandHasRoom = func(string, int64) bool { return rig.drops > 0 }
	nandAvailFn = func(string) (int64, int64, bool) { return 0, free - tmpSize(), availOK }
	engineStopHook = func() bool {
		rig.drops++
		rig.writtenAtDrop = tmpSize()
		return true
	}
	return rig
}

// failingReader hands out `good` bytes, then fails like a dropped connection.
type failingReader struct {
	r    io.Reader
	left int
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.left <= 0 {
		return 0, errors.New("connection reset by peer")
	}
	if len(p) > f.left {
		p = p[:f.left]
	}
	n, err := f.r.Read(p)
	f.left -= n
	return n, err
}

// The field case (2026-10-25): a tight speaker, an upload that dies after a
// couple of MB. The engine must still be there afterwards.
func TestReceiveAgentKeepsEngineWhenTransferDiesBeforeTheMargin(t *testing.T) {
	rig := newDeferredReclaimRig(t, nandWriteMargin+8<<20, true)
	const size = 18 << 20
	src := &failingReader{r: bytes.NewReader(patternBytes(size)), left: 2 << 20}
	_, err := receiveAgentBinary(rig.dst, src, size, 30<<20)
	var rerr uploadReadError
	if !errors.As(err, &rerr) {
		t.Fatalf("an aborted body must surface as a read error, got %v", err)
	}
	if rig.drops != 0 {
		t.Fatalf("the engine was dropped for an upload that never needed its room (%d drops)", rig.drops)
	}
	if _, serr := os.Stat(rig.dst + ".new"); !os.IsNotExist(serr) {
		t.Errorf("the aborted upload's temp must be removed, stat err = %v", serr)
	}
}

// A body that does need the room gets it once the volume reaches the margin,
// and the update completes.
func TestReceiveAgentDropsEngineWhenTheBodyReachesTheMargin(t *testing.T) {
	const headroom = 1 << 20
	rig := newDeferredReclaimRig(t, nandWriteMargin+headroom, true)
	body := patternBytes(3 << 20)
	got, err := receiveAgentBinary(rig.dst, bytes.NewReader(body), int64(len(body)), 30<<20)
	if err != nil {
		t.Fatalf("the update must complete, got %v", err)
	}
	if got.size != int64(len(body)) || got.ramStaged {
		t.Fatalf("unexpected result %+v", got)
	}
	if rig.drops != 1 {
		t.Fatalf("the engine must be dropped exactly once, got %d", rig.drops)
	}
	// Dropped once the free figure reached the margin: never before the
	// headroom was used up, and at most one check window plus a chunk past it.
	if rig.writtenAtDrop < headroom-engineReclaimCheckEvery-32*1024 || rig.writtenAtDrop > headroom {
		t.Errorf("engine dropped after %d bytes, want close to the %d bytes of headroom", rig.writtenAtDrop, headroom)
	}
	if on, _ := os.ReadFile(rig.dst); !bytes.Equal(on, body) {
		t.Error("dst content mismatch")
	}
}

// A volume already at the margin drops the engine before the first byte, as
// the up-front reclaim always did.
func TestReceiveAgentDropsEngineFirstWhenAlreadyAtTheMargin(t *testing.T) {
	rig := newDeferredReclaimRig(t, nandWriteMargin-1, true)
	body := patternBytes(1 << 20)
	if _, err := receiveAgentBinary(rig.dst, bytes.NewReader(body), int64(len(body)), 30<<20); err != nil {
		t.Fatal(err)
	}
	if rig.drops != 1 || rig.writtenAtDrop != 0 {
		t.Fatalf("want one drop before any byte was written, got drops=%d writtenAtDrop=%d", rig.drops, rig.writtenAtDrop)
	}
}

// A roomy speaker never loses its engine.
func TestReceiveAgentRoomyBoxKeepsEngine(t *testing.T) {
	rig := newDeferredReclaimRig(t, 0, true)
	nandHasRoom = func(string, int64) bool { return true }
	body := patternBytes(1 << 20)
	if _, err := receiveAgentBinary(rig.dst, bytes.NewReader(body), int64(len(body)), 30<<20); err != nil {
		t.Fatal(err)
	}
	if rig.drops != 0 {
		t.Fatalf("a roomy box dropped its engine (%d)", rig.drops)
	}
}

// Without a free figure there is nothing to steer by, so the engine stays
// unless the filesystem itself says no.
func TestEngineReclaimGateWithoutStatfsKeepsEngine(t *testing.T) {
	rig := newDeferredReclaimRig(t, 0, false)
	g := &engineReclaimGate{dir: filepath.Dir(rig.dst), need: 1 << 20}
	st := nandWriteState{enginePending: true}
	for i := 0; i < 100; i++ {
		if g.due(&st, 32*1024) {
			t.Fatal("no statfs figure must never make the drop due")
		}
	}
	g.run(&st, 0, true)
	if rig.drops != 1 || st.enginePending {
		t.Fatalf("a forced run must drop the engine and clear the pending mark, drops=%d st=%+v", rig.drops, st)
	}
	if g.due(&st, 32*1024) {
		t.Error("nothing is due once the drop has run")
	}
}
