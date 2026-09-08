package boxws

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The DO_NOT_RESUME restore that follows a failed remote skip is the box
// tearing its own UPnP source down, not a power-on. Reading it as a wake made
// STR resume its last station over whatever was playing, which is what a user
// streaming from Music Assistant heard as the speaker "wanting to switch
// presets" when he pressed Next (discussion #827).
func TestSkipFailureSuppressesTheWakeThatItsTeardownFakes(t *testing.T) {
	var buf bytes.Buffer
	h := &recHandler{}
	c := New(slog.New(slog.NewTextHandler(&buf, nil)), "ws://127.0.0.1:8080/", h)

	c.handleMessage(context.Background(), []byte(
		`<updates><errorUpdate>QPLAY_SKIP_NEXT_FAILED</errorUpdate></updates>`))
	c.handleMessage(context.Background(), []byte(
		`<updates><nowSelectionUpdated><preset id="0">`+
			`<ContentItem source="INVALID_SOURCE" type="DO_NOT_RESUME"/>`+
			`</preset></nowSelectionUpdated></updates>`))

	if h.powerWakes != 0 {
		t.Fatalf("the teardown after a failed skip must not fire OnPowerWake, got %d", h.powerWakes)
	}
	// The skip itself must still reach the handler: the suppression may not
	// swallow the key press it is reacting to.
	if len(h.skips) != 1 || !h.skips[0] {
		t.Fatalf("the skip key must still be dispatched, got %v", h.skips)
	}
	// A bundle has to show WHY nothing resumed, otherwise the next report of
	// this shape is undiagnosable.
	if got := buf.String(); !strings.Contains(got, "failed remote skip") {
		t.Fatalf("the stand-down must be logged, got:\n%s", got)
	}
}

// Outside the window the same frame is what it says it is: the only power-on
// signal this firmware sends. The suppression must not disable the power-on
// resume for the rest of the session.
func TestWakeStillFiresWhenTheSkipFailureIsOld(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(
		`<updates><errorUpdate>QPLAY_SKIP_PREV_FAILED</errorUpdate></updates>`))
	// Age the stamp past the window instead of sleeping: the value is what the
	// guard reads, and a real box takes seconds to get here.
	c.mu.Lock()
	c.lastSkipFailAt = time.Now().Add(-skipWakeWindow - time.Second)
	c.mu.Unlock()

	c.handleMessage(context.Background(), []byte(
		`<updates><nowSelectionUpdated><preset id="0">`+
			`<ContentItem source="INVALID_SOURCE" type="DO_NOT_RESUME"/>`+
			`</preset></nowSelectionUpdated></updates>`))

	if h.powerWakes != 1 {
		t.Fatalf("a wake outside the skip window must still resume: wakes=%d", h.powerWakes)
	}
}

// A box that has never failed a skip must not read the zero stamp as "a skip
// just happened".
func TestWakeFiresWhenNoSkipEverFailed(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	if age, recent := c.sinceSkipFailure(); recent {
		t.Fatalf("an unset stamp must not count as recent (age=%v)", age)
	}
	c.handleMessage(context.Background(), []byte(
		`<updates><nowSelectionUpdated><preset id="0">`+
			`<ContentItem source="INVALID_SOURCE" type="DO_NOT_RESUME"/>`+
			`</preset></nowSelectionUpdated></updates>`))
	if h.powerWakes != 1 {
		t.Fatalf("a plain wake must resume: wakes=%d", h.powerWakes)
	}
}

// doNotResumeRestore is the frame both a standby wake and a source teardown
// produce; telling the two apart is the whole subject of this file.
const doNotResumeRestore = `<updates><nowSelectionUpdated><preset id="0">` +
	`<ContentItem source="INVALID_SOURCE" type="DO_NOT_RESUME"/>` +
	`</preset></nowSelectionUpdated></updates>`

const skipNextFailed = `<updates><errorUpdate>QPLAY_SKIP_NEXT_FAILED</errorUpdate></updates>`

// The guard only ever looks BACKWARDS, and that has to stay true: a wake
// decided before the box reported its failed skip was decided on the evidence
// available then. A stamp applied to a frame that is already past would make
// the suppression depend on frame ORDER inside one flap, which the firmware
// does not guarantee.
func TestAWakeIsNotSuppressedByASkipFailureThatArrivesAfterIt(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(doNotResumeRestore))
	c.handleMessage(context.Background(), []byte(skipNextFailed))
	if h.powerWakes != 1 {
		t.Fatalf("a wake that preceded the skip failure must still resume: wakes=%d", h.powerWakes)
	}
}

// What "one-shot vs sticky" actually means here. The stamp is NOT consumed by
// the first frame it suppresses, on purpose: the firmware repeats the restore
// while the source flaps, so a one-shot guard would let the second copy through
// and resume anyway. The only thing bounding it is the window, and once that has
// passed the next restore resumes with no new event needed to re-arm anything.
func TestTheSuppressionCoversEveryRestoreInTheWindowAndEndsWithIt(t *testing.T) {
	// The window has to stay far below the time it takes a person to press skip
	// and then power, or the guard starts swallowing real power-ons.
	if skipWakeWindow > 5*time.Second {
		t.Fatalf("skipWakeWindow grew to %v; at that length it covers a deliberate power press", skipWakeWindow)
	}
	h := &recHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(skipNextFailed))
	c.handleMessage(context.Background(), []byte(doNotResumeRestore))
	c.handleMessage(context.Background(), []byte(doNotResumeRestore))
	if h.powerWakes != 0 {
		t.Fatalf("every restore inside the window is the same teardown: wakes=%d", h.powerWakes)
	}
	// Age the stamp past the window. Nothing else changes: no new frame, no
	// reset call, so this pins that the guard expires by itself.
	c.mu.Lock()
	c.lastSkipFailAt = time.Now().Add(-skipWakeWindow - time.Millisecond)
	c.mu.Unlock()
	c.handleMessage(context.Background(), []byte(doNotResumeRestore))
	if h.powerWakes != 1 {
		t.Fatalf("the suppression must expire with the window, not latch: wakes=%d", h.powerWakes)
	}
}

// A genuine power press reports itself as a powerState event, which no source
// teardown produces. That path stays unguarded so pressing power right after a
// skip still brings the music back.
//
// Note what this does NOT cover: the firmware in the field (27.0.6, verified on
// a Portable/taigan) sends no powerStateUpdated at all, so on those boxes the
// DO_NOT_RESUME restore is the only power-on signal there is and a real power-on
// inside the window IS swallowed. Reaching that needs a skip failure and a
// power-ON within five seconds of each other, which in turn needs the box to
// have gone to standby in between - and that path stands the resume down anyway
// (standbyStoppedRecently, #197).
func TestPowerStateWakeIsNotSuppressedByARecentSkipFailure(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(
		`<updates><errorUpdate>QPLAY_SKIP_NEXT_FAILED</errorUpdate></updates>`))
	c.handleMessage(context.Background(), []byte(
		`<updates><powerStateUpdated>POWER_ON</powerStateUpdated></updates>`))
	if h.powerWakes != 1 {
		t.Fatalf("a powerState ON must resume even right after a skip: wakes=%d", h.powerWakes)
	}
}
