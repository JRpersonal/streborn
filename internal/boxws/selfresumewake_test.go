package boxws

import (
	"context"
	"testing"
	"time"
)

// The #1771 power-on shape: the speaker resumes STR's UPnP source itself after a
// real standby, is refused, and gives up, with neither powerStateUpdated nor a
// DO_NOT_RESUME restore. See selfresumewake.go.

func npFrame(src string) []byte {
	return []byte(`<updates><nowPlayingUpdated><nowPlaying source="` + src + `"/></nowPlayingUpdated></updates>`)
}

const err1036Frame = `<updates><errorUpdate><error value="1036" name="UNABLE_TO_PROCESS_NOT_LOGGED_IN" severity="Unrecoverable">UpnpRcvdContentItemInWrongState</error></errorUpdate></updates>`

// standbyFor puts the client into STANDBY and backdates the moment it got
// there, as if the speaker had been off that long.
func standbyFor(c *Client, d time.Duration) {
	c.handleMessage(context.Background(), npFrame("UPNP"))
	c.handleMessage(context.Background(), npFrame("STANDBY"))
	c.mu.Lock()
	c.standbyEnteredAt = time.Now().Add(-d)
	c.mu.Unlock()
}

func TestSelfResumeGiveUpAfterStandbyIsAPowerOn(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	standbyFor(c, 14*time.Hour)
	c.handleMessage(context.Background(), npFrame("UPNP"))
	c.handleMessage(context.Background(), []byte(err1036Frame))
	c.handleMessage(context.Background(), npFrame("INVALID_SOURCE"))
	if h.powerWakes != 1 {
		t.Fatalf("the speaker's own failed resume after a standby is a power-on: wakes=%d", h.powerWakes)
	}
	// The box then settles on a native source; nothing more fires.
	c.handleMessage(context.Background(), npFrame("LOCAL_INTERNET_RADIO"))
	if h.powerWakes != 1 {
		t.Fatalf("one power-on, one resume: wakes=%d", h.powerWakes)
	}
}

func TestSelfResumeShortStandbyFlapIsNotAPowerOn(t *testing.T) {
	// #197 / #419: the firmware's own power-off oscillation flips
	// UPNP <-> STANDBY within moments. No resume may come of it.
	h := &recHandler{}
	c := newTestClient(h)
	standbyFor(c, 200*time.Millisecond)
	c.handleMessage(context.Background(), npFrame("UPNP"))
	c.handleMessage(context.Background(), npFrame("INVALID_SOURCE"))
	if h.powerWakes != 0 {
		t.Fatalf("a standby flap must not read as a power-on: wakes=%d", h.powerWakes)
	}
}

func TestSelfResumeNeedsUPNPStraightOutOfStandby(t *testing.T) {
	// UPNP -> INVALID_SOURCE alone is an ordinary failed play (#22), not a
	// power-on, even long after a standby.
	h := &recHandler{}
	c := newTestClient(h)
	standbyFor(c, time.Hour)
	c.handleMessage(context.Background(), npFrame("LOCAL_INTERNET_RADIO"))
	c.handleMessage(context.Background(), npFrame("UPNP"))
	c.handleMessage(context.Background(), npFrame("INVALID_SOURCE"))
	if h.powerWakes != 0 {
		t.Fatalf("only UPNP straight out of standby counts: wakes=%d", h.powerWakes)
	}
}

func TestSelfResumeGiveUpTooLateIsNotAPowerOn(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	standbyFor(c, time.Hour)
	c.handleMessage(context.Background(), npFrame("UPNP"))
	c.mu.Lock()
	c.upnpFromStandbyAt = time.Now().Add(-10 * time.Second)
	c.mu.Unlock()
	c.handleMessage(context.Background(), npFrame("INVALID_SOURCE"))
	if h.powerWakes != 0 {
		t.Fatalf("a give-up long after the wake is something else: wakes=%d", h.powerWakes)
	}
}

func TestPowerOnResumesOnceWhicheverSignalComesFirst(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	standbyFor(c, time.Hour)
	c.handleMessage(context.Background(), npFrame("UPNP"))
	c.handleMessage(context.Background(), npFrame("INVALID_SOURCE"))
	// The same power-on also restores the selection as DO_NOT_RESUME.
	c.handleMessage(context.Background(), []byte(
		`<updates><nowSelectionUpdated><preset id="0">`+
			`<ContentItem source="INVALID_SOURCE" type="DO_NOT_RESUME"/>`+
			`</preset></nowSelectionUpdated></updates>`))
	if h.powerWakes != 1 {
		t.Fatalf("one power-on must resume once: wakes=%d", h.powerWakes)
	}
	// A later, separate power-on resumes again.
	c.mu.Lock()
	c.lastPowerWakeAt = time.Now().Add(-time.Minute)
	c.mu.Unlock()
	c.handleMessage(context.Background(), []byte(`<updates><powerStateUpdated>POWER_ON</powerStateUpdated></updates>`))
	if h.powerWakes != 2 {
		t.Fatalf("a separate power-on resumes again: wakes=%d", h.powerWakes)
	}
}

func TestSelfResumeGiveUpAfterAFailedSkipIsATeardown(t *testing.T) {
	// #827: a failed remote skip tears the UPnP source down; that is not a
	// power-on, whatever the shape.
	h := &recHandler{}
	c := newTestClient(h)
	standbyFor(c, time.Hour)
	c.handleMessage(context.Background(), npFrame("UPNP"))
	c.noteSkipFailure()
	c.handleMessage(context.Background(), npFrame("INVALID_SOURCE"))
	if h.powerWakes != 0 {
		t.Fatalf("a give-up right after a failed skip is a teardown: wakes=%d", h.powerWakes)
	}
}
