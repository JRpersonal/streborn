package main

import (
	"fmt"
	"sync"
)

// One speaker, one write at a time.
//
// Nothing stopped two installs or updates running against the same speaker at
// once, and a donor's SoundTouch 20 showed what that costs (2026-08-11). His
// update journal carries every line twice, one second apart, and the speaker's
// own log shows the consequence plainly:
//
//	17:30:46 upload started endpoint=agent-update contentLength=13303970 remote=...:50042
//	17:30:48 upload started endpoint=agent-update contentLength=13303970 remote=...:50043
//
// Two 13 MB uploads into a box with 122 MB of RAM, both writing the same
// streborn-armv7l.new. One of them renamed the file into place, the other then
// failed with "rename ...new: no such file or directory" - the 500 that made
// the install look broken. Each landing upload also makes the agent apply and
// reboot, so the speaker went away twice and stayed away for twenty minutes,
// long past the window the app was willing to wait.
//
// None of the downstream repairs (retrying the upload, waiting longer, being
// gentler about a missing engine) address that. This does: the second run is
// refused with a plain answer while the first is still working.
type boxBusy struct {
	mu   sync.Mutex
	busy map[string]string // host -> what is running
}

var writesInFlight = boxBusy{busy: map[string]string{}}

// claim marks the speaker as being written to. The release function must be
// called when the work finishes; it is a no-op if the claim failed.
func (b *boxBusy) claim(host, what string) (release func(), err error) {
	if host == "" {
		return func() {}, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if running, taken := b.busy[host]; taken {
		return func() {}, fmt.Errorf("this speaker is already being written to (%s is running); wait for that to finish", running)
	}
	b.busy[host] = what
	return func() {
		b.mu.Lock()
		delete(b.busy, host)
		b.mu.Unlock()
	}, nil
}
