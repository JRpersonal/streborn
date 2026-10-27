package main

// The install guard: install_str must never start on a speaker that is
// already running STR, or that an update from this app was moving to a new
// agent minutes ago.
//
// Field case 2026-10-21 (SoundTouch 20 Series I, scm, firmware 27.0.6, app
// v1.0.18). An update from v0.9.34 was accepted at 16:36:56, the speaker was
// slow to come back, and the verify window ended at 16:43:10 on "NOT
// CONFIRMED - box unreachable". The owner then prepared a USB stick, put it
// into the speaker and restarted it. At 17:03:59 the stick install started on
// that speaker: SSH was open (the stick), the stick was found, and nothing in
// the install path knew that this app had updated the very same speaker 27
// minutes earlier. At 17:04:26 discovery confirmed the update after all: the
// agent was up on :17008, on the new build. The install kept going anyway and
// failed at 17:06:11 because install.sh had vanished from the stick (status
// 127). From 17:07 the speaker answered only ping, with its display dark and
// the stick still in it.
//
// The update had worked. Every step the install took from there on was a
// second writer on a speaker whose new agent was starting up: it reads the
// stick, overwrites the boot scripts and the agent cache on NAND, and
// restarts the speaker, while the running agent enforces its own copy of the
// boot script and restarts the speaker to apply a mismatch. Two writers on
// the boot path of a speaker with a stick in its shared USB port is exactly
// the state that leaves a box unable to boot.
//
// So the install refuses, before it changes anything, in two cases:
//
//   - this app started an agent update on the speaker within otaVerifyMemoTTL
//     and has not seen it confirmed (or the post-OTA pin is still live): the
//     update is still finishing, and the owner is told to wait and look again;
//   - the STR agent on the speaker answers /api/agent/version: STR is already
//     running there, and there is nothing to install. Updating it is the
//     update's job, which talks to the agent and never to the boot scripts
//     from outside.
//
// The second check runs twice: when the install starts, and again right
// before anything is written over SSH, because the two can be minutes apart
// (the stick probe alone waits up to a minute for the stick to mount) and the
// field case's agent came up in exactly that gap.
//
// What stays open: a speaker whose agent does not answer and that this app
// did not update recently, which is every genuine first install and every
// stick recovery of a speaker STR has stopped running on. RepairInstallViaSSH
// is a separate, explicit recovery action and is not gated here.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// installCodeUpdateFinishing is the result code of an install refused because
// an update from this app is still finishing on the speaker.
const installCodeUpdateFinishing = "update-finishing"

// installCodeSTRRunning is the result code of an install refused because the
// STR agent on the speaker answers: STR is already running there.
const installCodeSTRRunning = "str-running"

// installAgentProbeTimeout bounds each port of the "is the agent up" probe.
// The agent answers its version in milliseconds; what costs time is the port
// that does not answer at all (a chassis that DROPs :17008, or Bose's own
// SoftwareUpdate daemon that accepts on :17008 and never replies).
const installAgentProbeTimeout = 4 * time.Second

// installAgentProbePort is the port the agent probe asks first. Zero means the
// usual order (the cached port, then :17008 and :8888); tests point it at an
// httptest listener.
var installAgentProbePort = 0

// recentOTAHold reports how much longer the install must wait because this app
// started an agent update on host (or on the same speaker at another address)
// that has not been confirmed yet. ok is false when nothing holds it.
//
// Two records say an update is in flight. The post-OTA pin covers the push and
// the first reboot (otaRebootGrace). The verify memo covers the rest: it is
// written when the update starts, marked when the verify window ends without
// a verdict, and removed the moment the update is confirmed, by the poll or
// by a later discovery sighting (confirmLateOTA). The hold lasts as long as
// the memo is allowed to correct the journal, otaVerifyMemoTTL from the later
// of the two moments, which in the field case would have held the install
// until long after the agent came back at 21 minutes past the window.
func (a *App) recentOTAHold(host string) (since time.Duration, remaining time.Duration, ok bool) {
	now := time.Now()
	id := a.cachedDeviceID(host)

	a.discMu.Lock()
	pinAt, pinned := a.otaPinned[host]
	a.discMu.Unlock()
	if pinned && now.Sub(pinAt) <= otaRebootGrace {
		since, remaining, ok = now.Sub(pinAt), otaRebootGrace-now.Sub(pinAt), true
	}

	a.otaVerifyMu.Lock()
	defer a.otaVerifyMu.Unlock()
	for h, m := range a.otaVerify {
		// The memo for this address, unless it provably names another speaker;
		// or the memo for this speaker at the address it was updated on.
		sameAddr := h == host && (m.deviceID == "" || id == "" || strings.EqualFold(m.deviceID, id))
		sameSpeaker := id != "" && m.deviceID != "" && strings.EqualFold(m.deviceID, id)
		if !sameAddr && !sameSpeaker {
			continue
		}
		start := m.at
		last := m.at
		if m.unconfirmedAt.After(last) {
			last = m.unconfirmedAt
		}
		if start.IsZero() || last.IsZero() {
			continue
		}
		left := otaVerifyMemoTTL - now.Sub(last)
		if left <= 0 {
			continue
		}
		if !ok || left > remaining {
			since, remaining, ok = now.Sub(start), left, true
		}
	}
	return since, remaining, ok
}

// runningAgentVersion asks the speaker's STR agent for its version and
// returns it when the agent answers. A port that is not the agent (a 404 from
// a stock port, a daemon that never replies) is not an answer.
func (a *App) runningAgentVersion(host string) (string, bool) {
	if host == "" {
		return "", false
	}
	resp, err := a.boxDoTimeout(host, installAgentProbePort, http.MethodGet, "/api/agent/version", "", "", installAgentProbeTimeout)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var out map[string]any
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return "", false
	}
	v, _ := out["version"].(string)
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	if b, _ := out["build"].(string); strings.TrimSpace(b) != "" {
		v += " (build " + strings.TrimSpace(b) + ")"
	}
	return v, true
}

// installHeldByOTA ends the install, before it touches the speaker, when an
// update from this app is still finishing on it. Returns true when it did.
func (a *App) installHeldByOTA(r *installRun) bool {
	since, remaining, held := a.recentOTAHold(r.host)
	if !held {
		return false
	}
	r.res.Step = "guard"
	r.res.Code = installCodeUpdateFinishing
	r.res.Message = fmt.Sprintf("ST Reborn put new software on this speaker %s ago, and that update is still finishing: "+
		"a speaker can take up to half an hour to come back after an update. Nothing was changed on the speaker. "+
		"Leave it switched on, take out any USB stick you inserted since, and press Refresh in the speaker list in a few minutes. "+
		"Once it shows its ST Reborn version again, the update is done and there is nothing to install. "+
		"If it is still not back in %s, unplug it for ten seconds, plug it back in and refresh the list again.",
		roundForText(since), roundForText(remaining))
	a.logger.Warn("install_str: refused, an update from this app is still finishing on this speaker; not touching it",
		"host", r.host, "updateStarted", since.Round(time.Second), "holdLeft", remaining.Round(time.Second))
	return true
}

// installRefusedSTRRunning ends the install, before anything is written, when
// the STR agent on the speaker answers. at names the step for the log.
// Returns true when it did.
func (a *App) installRefusedSTRRunning(r *installRun, at string) bool {
	ver, up := a.runningAgentVersion(r.host)
	if !up {
		return false
	}
	r.res.Step = "guard"
	r.res.Code = installCodeSTRRunning
	r.res.Message = "ST Reborn " + ver + " is already running on this speaker, so there is nothing to install and the install changed nothing. " +
		"Take out any USB stick you inserted for the install, then refresh the speaker list; " +
		"if the speaker needs a newer version, update it from its entry in the list."
	a.logger.Warn("install_str: refused, the STR agent on this speaker answers; not touching it",
		"host", r.host, "at", at, "agent", ver)
	return true
}

// roundForText words a duration for a message: whole minutes from two minutes
// up, seconds below.
func roundForText(d time.Duration) string {
	if d < 2*time.Minute {
		s := int(d.Round(time.Second).Seconds())
		if s < 1 {
			s = 1
		}
		return fmt.Sprintf("%d seconds", s)
	}
	return fmt.Sprintf("%d minutes", int(d.Round(time.Minute).Minutes()))
}
