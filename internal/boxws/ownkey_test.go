package boxws

import (
	"context"
	"testing"
	"time"
)

// #1394: playing a library track over a radio station the speaker plays itself
// ends that station with the speaker's own STOP key first. The firmware answers
// the key with a userActivityUpdate, the same ping a person's key press makes,
// and then a STOP_STATE. Read as a person, the ping vetoed the own-command
// excusal, the STOP_STATE latched a user stop, and the single-track end watch
// of the library track stood down: the track never stopped at its end.
//
// Bundle sequence (ST20, v1.0.18): STR's key, user-activity frame +116 ms,
// STOP_STATE +208 ms, "treating as user stop".

const ownKeyStopFrame = `<updates><nowPlayingUpdated><nowPlaying source="LOCAL_INTERNET_RADIO"><playStatus>STOP_STATE</playStatus></nowPlaying></nowPlayingUpdated></updates>`

func TestOwnKeyEchoDoesNotVetoTheExcusal(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	// A key was seen before, so this firmware emits userActivity frames and
	// the veto is armed: exactly the reporter's speaker.
	c.thumbMu.Lock()
	c.lastUserActivityAt = time.Now().Add(-time.Minute)
	c.thumbMu.Unlock()

	c.NoteOwnKeyPress()
	c.handleMessage(context.Background(), []byte(`<userActivityUpdate deviceID="x"/>`))
	c.handleMessage(context.Background(), []byte(ownKeyStopFrame))
	if h.userStops != 0 {
		t.Fatalf("the STOP_STATE answering STR's own STOP key latched %d user stop(s), want 0", h.userStops)
	}
}

// The same frames after a plain own SOAP command still mean a person pressed
// stop: only STR's own KEY explains the ping.
func TestKeyPressAfterOwnSOAPCommandStillVetoes(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	c.NoteOwnTransportCommand()
	c.handleMessage(context.Background(), []byte(`<userActivityUpdate deviceID="x"/>`))
	c.handleMessage(context.Background(), []byte(ownKeyStopFrame))
	if h.userStops != 1 {
		t.Fatalf("a person's key next to the STOP_STATE must still count as a user stop, got %d", h.userStops)
	}
}

// A person pressing stop well after STR's own key is a person again.
func TestKeyPressLongAfterOwnKeyStillVetoes(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	c.mu.Lock()
	c.lastOwnKeyAt = time.Now().Add(-10 * time.Second)
	c.lastOwnCmdAt = time.Now().Add(-500 * time.Millisecond)
	c.mu.Unlock()
	c.handleMessage(context.Background(), []byte(`<userActivityUpdate deviceID="x"/>`))
	c.handleMessage(context.Background(), []byte(ownKeyStopFrame))
	if h.userStops != 1 {
		t.Fatalf("a key long after STR's own key is a person's, want 1 user stop, got %d", h.userStops)
	}
}
