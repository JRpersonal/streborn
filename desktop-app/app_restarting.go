package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// A speaker that has just accepted a restart refuses connections, or does not
// answer at all, for a minute or two while it boots. Telling the user at that
// moment that a firewall or antivirus blocks ST Reborn is wrong, and it sends
// them hunting through security settings for a speaker that is doing exactly
// what it was asked to.
//
// That is what #1768 shows: the restart from the sign-in warning went through
// (the speaker took it at 14:47:04), the warning stayed up because the speaker
// had not gone down yet, the button offered itself again, and a second press
// 79 s later found both of the speaker's ports refusing. The answer under it
// was the firewall paragraph.
//
// So the app remembers when each speaker last accepted a restart, and for
// restartWindow after it a failed connection to that speaker says that it is
// still restarting. The frontend keeps its restart button off for the same
// window (restartwait.js).

// restartWindow is how long after an accepted restart a failed connection is
// put down to the restart. A SoundTouch is back in one to two minutes; a BCO
// chassis with the agent coming up after the firmware takes up to about three.
const restartWindow = 4 * time.Minute

// stillRestartingAdvice is the paragraph a failed connection carries inside
// restartWindow. A constant like the other advice paragraphs, which the update
// report recognises by prefix.
const stillRestartingAdvice = "This speaker is still restarting, give it a few minutes."

// noteRestartRequested records that host accepted a restart request now.
func (a *App) noteRestartRequested(host string) {
	if a == nil || host == "" {
		return
	}
	a.restartRequested.Store(host, time.Now())
}

// restartRequestedAt is when host last accepted a restart in this session,
// zero when it has not or when that is longer ago than restartWindow.
func (a *App) restartRequestedAt(host string) time.Time {
	if a == nil || host == "" {
		return time.Time{}
	}
	v, ok := a.restartRequested.Load(host)
	if !ok {
		return time.Time{}
	}
	at, _ := v.(time.Time)
	if time.Since(at) >= restartWindow {
		a.restartRequested.Delete(host)
		return time.Time{}
	}
	return at
}

// noteSpeakerUptime ends the restart window early once the speaker is seen
// back: an uptime shorter than the time since the request means the restart
// happened and the speaker is up again, so a failure from here on has some
// other cause and gets the usual advice.
func (a *App) noteSpeakerUptime(host, uptimeSec string) {
	at := a.restartRequestedAt(host)
	if at.IsZero() {
		return
	}
	if up, ok := parseUptime(uptimeSec); ok && restartedSince(up, at, time.Now()) {
		a.restartRequested.Delete(host)
	}
}

// restartedSince reports whether a speaker reporting uptime seconds has booted
// after requestedAt.
func restartedSince(uptime int, requestedAt, now time.Time) bool {
	return uptime >= 0 && time.Duration(uptime)*time.Second < now.Sub(requestedAt)
}

// stillRestartingExplains decides whether err, a failure to reach a speaker
// that accepted a restart at requestedAt, is put down to that restart.
//
// Inside the window a refused or unanswered connection, a reset, and even a
// bare status from the firmware's own listener (the agent is not up yet) are
// all what a booting speaker looks like. A cancelled call (app shutdown) and
// the two causes that sit on this PC (no local address, no network at all)
// are not: the speaker's restart does not explain those, and their own advice
// is the right one.
func stillRestartingExplains(err error, requestedAt, now time.Time) bool {
	if err == nil || requestedAt.IsZero() || errors.Is(err, context.Canceled) {
		return false
	}
	since := now.Sub(requestedAt)
	if since < 0 || since >= restartWindow {
		return false
	}
	return !noLocalRoute(err) && !noNetworkHere(err)
}

// stillRestartingHint is err wrapped with stillRestartingAdvice when the
// speaker's recent restart explains it, nil when it does not.
func (a *App) stillRestartingHint(host string, err error) error {
	if !stillRestartingExplains(err, a.restartRequestedAt(host), time.Now()) {
		return nil
	}
	return fmt.Errorf("%w\n\n%s", err, stillRestartingAdvice)
}
