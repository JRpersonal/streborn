package boxws

// The 1036 storm counter is meant to catch a box that rejects essentially
// every recall on its own. It counted STR's OWN footprints too: the firmware
// answers its power-on self-resume with 1036, and a zone teardown kills every
// member's in-flight UPnP session, which the members answer the same way. A
// user who formed and dissolved a group a few times collected six of them
// inside ten minutes and was shown a red banner about a speaker that was fine
// and healed by itself (field, 2026-09-07).
//
// Suppress1036Until takes the COUNT out for a window. Nothing else: the WARN
// and the boxErrors entry a diagnostic bundle is read from stay exactly as
// they were, which the last test here holds to.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// The wrong-state flavor is the one a group teardown produces: name is a plain
// UNABLE_TO_PROCESS, so no re-login self-heal fires and the frame is handled
// entirely inline.
const wrongState1036 = `<errorUpdate><error value="1036" name="UNABLE_TO_PROCESS" severity="Unknown">` +
	`UpnpRcvdContentItemInWrongState</error></errorUpdate>`

func TestNote1036DoesNotCountInsideTheSuppressionWindow(t *testing.T) {
	c := newTestClient(&recHandler{})
	c.Suppress1036Until(time.Now().Add(2 * time.Second))

	for i := 0; i < storm1036Threshold+2; i++ {
		c.note1036()
	}

	active, count, _ := c.Storm1036()
	if count != 0 || active {
		t.Fatalf("rejections STR provoked itself must not raise a storm, got count=%d active=%v", count, active)
	}
}

func TestNote1036CountsOnceTheWindowIsOver(t *testing.T) {
	c := newTestClient(&recHandler{})
	// A window that has already closed: the box is speaking for itself again.
	c.Suppress1036Until(time.Now().Add(-time.Second))

	for i := 0; i < storm1036Threshold; i++ {
		c.note1036()
	}

	active, count, since := c.Storm1036()
	if count != storm1036Threshold || !active {
		t.Fatalf("a real storm must still be reported, got count=%d active=%v", count, active)
	}
	if since.IsZero() {
		t.Error("a reported storm must say since when")
	}
}

func TestSuppressionNeverShortensAWindowAlreadyArmed(t *testing.T) {
	c := newTestClient(&recHandler{})
	c.Suppress1036Until(time.Now().Add(2 * time.Second))
	// A second arming with a nearer deadline (a power-on inside a teardown)
	// must not open the count back up for the rest of the longer window.
	c.Suppress1036Until(time.Now().Add(10 * time.Millisecond))

	c.note1036()

	if _, count, _ := c.Storm1036(); count != 0 {
		t.Fatalf("the longer window must win, got count=%d", count)
	}
}

// lockedBuf is a writer a background goroutine can share with the assertion.
type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// A bundle must lose nothing. Only the storm COUNT is suppressed; every single
// rejection still reaches the log and the boxErrors ring, because those are
// written before the counter is ever consulted.
func TestSuppressed1036KeepsTheWarnAndTheBundleEntry(t *testing.T) {
	out := &lockedBuf{}
	c := New(slog.New(slog.NewTextHandler(out, nil)), "ws://127.0.0.1:8080/", &recHandler{})
	c.Suppress1036Until(time.Now().Add(2 * time.Second))

	c.handleMessage(context.Background(), []byte(wrongState1036))

	if s := out.String(); !strings.Contains(s, "box reported error") || !strings.Contains(s, "1036") {
		t.Errorf("the per-frame WARN must survive the suppression, log was: %s", s)
	}
	errs := c.BoxErrors()
	if len(errs) != 1 || errs[0].Value != "1036" {
		t.Fatalf("the rejection must still be in boxErrors for the bundle, got %+v", errs)
	}
	if _, count, _ := c.Storm1036(); count != 0 {
		t.Errorf("only the storm count is suppressed, got count=%d", count)
	}
}

// The "a later t wins" rule has to be pinned from the other side too: the test
// above passes even without the guard, because both windows are still open when
// note1036 runs. This one arms a window that has ALREADY closed on top of a live
// one - the exact shape of a power toggle landing inside a zone teardown - and
// the count must stay shut.
func TestAnAlreadyClosedWindowNeverReopensTheCount(t *testing.T) {
	c := newTestClient(&recHandler{})
	c.Suppress1036Until(time.Now().Add(time.Minute))
	c.Suppress1036Until(time.Now().Add(-time.Hour))

	for i := 0; i < storm1036Threshold; i++ {
		c.note1036()
	}

	if active, count, _ := c.Storm1036(); count != 0 || active {
		t.Fatalf("the earlier deadline reopened the count: count=%d active=%v", count, active)
	}
}

// A suppressed frame must not age a genuine rejection out of the sliding
// window either: the early return happens before err1036Times is touched.
func TestSuppressionDoesNotDiscardRejectionsAlreadyCounted(t *testing.T) {
	c := newTestClient(&recHandler{})
	for i := 0; i < storm1036Threshold-1; i++ {
		c.note1036()
	}
	c.Suppress1036Until(time.Now().Add(time.Minute))
	c.note1036()
	if _, count, _ := c.Storm1036(); count != storm1036Threshold-1 {
		t.Fatalf("the rejections counted before the window must survive it, got %d", count)
	}
}
