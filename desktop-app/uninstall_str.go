// Uninstall STR: SSH into the speaker, remove the STR install entirely,
// reset the Bose-side state to factory OOB, and reboot into vanilla
// Bose. This is the real "back to stock" path. It differs from
// TrueFactoryReset, which deliberately KEEPS STR installed and only
// wipes the Bose network/account state so the box can be re-onboarded
// with the Bose iOS app while STR stays in place.
//
// CRITICAL safeguard: refuse if the STR USB stick is still inserted.
// Bose's SD/USB entry point re-runs the stick's run.sh on the next
// boot, which would reinstall STR right after we removed it. The user
// must pull the stick first.

package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// UninstallSTRResult is the JSON-serialisable outcome of an uninstall
// attempt. StickPresent=true means we aborted because the stick was
// still in the speaker (the frontend shows the pull-the-stick hint).
// Incomplete=true means the removal went through only in part.
type UninstallSTRResult struct {
	Step         string   `json:"step"`
	OK           bool     `json:"ok"`
	StickPresent bool     `json:"stickPresent"`
	Message      string   `json:"message"`
	Log          string   `json:"log"`
	RemovedFiles []string `json:"removedFiles"`
	// Incomplete: STR no longer starts on the speaker (it rebooted into plain
	// Bose), but the paths in LeftoverFiles could not be deleted. OK stays
	// false: this is neither a success nor a removal that did nothing.
	Incomplete    bool     `json:"incomplete"`
	LeftoverFiles []string `json:"leftoverFiles"`
	// StillInstalled: nothing effective came off, STR starts again on the
	// restart that follows. LeftoverFiles lists what is still there. The
	// frontend has its own translated sentence for it.
	StillInstalled bool `json:"stillInstalled"`
}

// UninstallSTR removes STR from the speaker and returns it to vanilla
// Bose factory state, then reboots. SSH must be available (passwordless
// root via the STR install or an inserted stick's remote_services
// marker — but see the stick safeguard below).
//
// Removes:
//   - /mnt/nv/streborn (the whole STR install: agent, certs, presets,
//     run-override.sh, wlan-creds)
//   - /mnt/nv/rc.local (the NAND override entry point, so Bose boots
//     its own stack with no STR hook)
//
// Deliberately does NOT touch the Bose network/account state: the box keeps
// its Wi-Fi, name and language so it stays on the LAN and discoverable after
// the reboot - an uninstall must never knock the speaker off Wi-Fi into the
// Bose setup AP. Only the STR marge redirect in /etc/hosts is dropped (a tmpfs
// bind-mount, cleared by the reboot regardless). A full network/account wipe
// is TrueFactoryReset's job, not this.
//
// After the reboot the speaker is a normal (cloudless) Bose device with no STR,
// still on Wi-Fi, ready to be re-onboarded with the Bose app or to have STR
// reinstalled.
// uninstallScript is the whole uninstall, run as one script on the speaker.
// Package level so a test can assert what it reports; the Go side cannot tell
// "removed nothing because it failed" from "removed nothing because there was
// nothing there" unless the script says which.
//
// Order matters. Everything that writes into /mnt/nv/streborn is stopped
// BEFORE the folder is removed, and only then is it deleted:
//
//   - the run script (run-override.sh, started by rc.local) and its watchdog
//     subshells. They append to setup.log and logs/agent-crash.log the moment
//     the agent dies, and they start the agent again, which writes agent.pid.
//     A file created in the folder while rm walks it is what makes rm end in
//     "Directory not empty" with the folder left behind (seen on a
//     SoundTouch 10, 2026-11-10). They are found by command line, because a
//     subshell carries the script's command line. Lines that contain one of
//     this script's own markers are skipped: that is this script's shell.
//   - the agent and the Spotify engine (go-librespot, which keeps its state
//     in /mnt/nv/streborn/sp-cache and outlives a killed agent).
//
// The script then VERIFIES what is left and says so (STR_UNINSTALL_LEFTOVER),
// instead of trusting rm's exit status. STR_UNINSTALL_STILL_STARTS is added
// when both rc.local and run-override.sh survived, so STR would come back on
// the next boot.
//
// A lost connection must not leave a stopped speaker behind. Once the run
// script and the agent are killed nothing on the speaker starts them again, so
// a session that drops between "stopped" and the app's reboot used to end in
// a speaker with STR half removed and nothing running until someone pulled
// the plug. Two things close that:
//
//   - BEFORE the first kill the script arms its own restart, a detached
//     subshell that reboots the speaker uninstallGuardSeconds after the start,
//     whatever happens to the session. It ignores HUP and holds no handle of
//     the session, the same detached form as rebootCmd. The app's own reboot
//     (one second, right after the script reports) normally comes first and
//     takes the guard down with everything else; the guard is what is left
//     when the app never gets to send it. It is NOT armed on the stick-present
//     refusal, which touches nothing.
//   - the script itself ignores HUP and PIPE, so it finishes the removal and
//     the verification instead of dying at its next echo into a dead session.
//
// The guard's own command line is this script's (a subshell does not exec),
// so stop_supervisors skips it like it skips the script's shell. The
// stick-present marker stays in the first lines for the same reason: the
// kernel hands out only the first page of a command line.
//
// BusyBox v1.19 on the speaker: killall and pidof with several names, no
// pkill -f, no sed -E.
const uninstallScript = `set -u
if [ -e /media/sda1/run.sh ] || [ -e /media/sda1/streborn-armv7l ] || [ -e /media/sda1/streborn ]; then
  echo "STR_UNINSTALL_ABORT_STICK_PRESENT"
  exit 0
fi
# A dropped session must not end this run halfway.
trap '' HUP PIPE
# Restart guard, armed before anything is stopped: whatever happens to this
# session from here on, the speaker reboots by itself.
(trap '' HUP; sleep 120; sync; /sbin/reboot) </dev/null >/dev/null 2>&1 &
echo "STR_UNINSTALL_GUARD_ARMED"
REMOVED=""
SUP=0
# Kill the run script's shells: the main one, its watchdog loops and the
# supervisor subshell that waits for the agent. Never restarted here.
stop_supervisors() {
  SUP=0
  for d in /proc/[0-9]*; do
    [ -r "$d/cmdline" ] || continue
    c=$(tr '\000' ' ' < "$d/cmdline" 2>/dev/null)
    case "$c" in
      *STR_UNINSTALL_*) ;;
      *"/mnt/nv/streborn/run-override.sh"*|*"/media/sd"[a-d]"1/run.sh"*|*"/mnt/nv/rc.local"*)
        kill -9 "${d##*/}" 2>/dev/null
        SUP=$((SUP+1)) ;;
    esac
  done
}
stop_supervisors
# Without the PID file a watchdog that slipped through ends its own loop.
rm -f /mnt/nv/streborn/agent.pid
# Agent and Spotify engine: ask first, so they can close their files.
killall streborn-armv7l go-librespot 2>/dev/null
i=0
while [ $i -lt 5 ] && pidof streborn-armv7l go-librespot >/dev/null 2>&1; do
  sleep 1
  i=$((i+1))
done
# Then insist, until nothing of STR runs any more (bounded).
i=0
while [ $i -lt 10 ]; do
  stop_supervisors
  killall -9 streborn-armv7l go-librespot 2>/dev/null
  if [ "$SUP" = 0 ] && ! pidof streborn-armv7l go-librespot >/dev/null 2>&1; then break; fi
  sleep 1
  i=$((i+1))
done
# remove_path PATH: delete it, and try again after a short pause when it is
# still there (a straggler wrote into it mid-walk). 0 once it is gone.
remove_path() {
  n=0
  while [ $n -lt 5 ]; do
    rm -rf "$1"
    [ -e "$1" ] || break
    n=$((n+1))
    stop_supervisors
    killall -9 streborn-armv7l go-librespot 2>/dev/null
    sleep 1
  done
  if [ $n -gt 0 ]; then echo "STR_UNINSTALL_RETRIED:$1 $n"; fi
  [ ! -e "$1" ]
}
# The STR install, the NAND override entry point, and the STR/go-librespot
# traces that live OUTSIDE /mnt/nv/streborn: go-librespot's Spotify oauth dump
# (can be multi-MB) and a stranded SSH-repair install-staging dir. Uninstall is
# "back to stock", so drop every trace, not just the install dir.
for p in /mnt/nv/streborn /mnt/nv/rc.local /mnt/nv/sp-oauth.out /mnt/nv/streborn-install; do
  if [ -e "$p" ]; then
    remove_path "$p" && REMOVED="$REMOVED $p"
  fi
done
# STR Remove deliberately does NOT touch the Bose network/account state.
# Wiping NetworkProfiles.xml (as this used to, for a "factory OOB" reset) drops
# the box off Wi-Fi into the Bose setup AP, so it vanishes from the LAN - that
# must never happen on an uninstall. The box keeps its Wi-Fi, name and language
# and simply becomes a normal (cloudless) Bose speaker again, still on the LAN
# and discoverable, so STR can be reinstalled. A full network/account wipe is
# TrueFactoryReset's job, not this.
# Drop the STR marge redirect from /etc/hosts. It is a tmpfs bind-mount,
# so unbind it now; a reboot clears it regardless.
umount /etc/hosts 2>/dev/null || true
sync
# Verify instead of trusting rm: look at what is really still there.
LEFT=""
for p in /mnt/nv/streborn /mnt/nv/rc.local /mnt/nv/sp-oauth.out /mnt/nv/streborn-install; do
  if [ -e "$p" ]; then LEFT="$LEFT $p"; fi
done
if [ -n "$LEFT" ]; then
  echo "STR_UNINSTALL_LEFTOVER:$LEFT"
  if [ -f /mnt/nv/rc.local ] && [ -e /mnt/nv/streborn/run-override.sh ]; then echo "STR_UNINSTALL_STILL_STARTS"; fi
  echo "left behind:"
  ls -la $LEFT 2>&1 | head -n 40
elif [ -z "$REMOVED" ]; then
  echo "STR_UNINSTALL_ALREADY_CLEAN"
fi
echo "STR_UNINSTALL_REMOVED:$REMOVED"
`

// uninstallScriptTimeout bounds the script run from the app's side. The
// script stops everything first and waits for it, bounded: up to ~15 s of
// waiting plus the retries of a removal that does not go through.
const uninstallScriptTimeout = 90 * time.Second

// uninstallGuardSeconds is the delay of the restart guard inside
// uninstallScript (the "sleep 120" there; a test keeps the two in step). It is
// longer than uninstallScriptTimeout on purpose: while the app still waits for
// the script, the guard must not pull the speaker away under it.
const uninstallGuardSeconds = 120

// uninstallGuardArmedMarker is what the script prints once its restart guard
// is armed. From that line on the speaker reboots by itself.
const uninstallGuardArmedMarker = "STR_UNINSTALL_GUARD_ARMED"

// uninstallInterruptedMessage is the text for a script run the app lost: the
// SSH call failed or timed out, or the script ended without its report.
// armed says whether the output still showed the restart guard going up.
func uninstallInterruptedMessage(cause string, armed bool) string {
	if armed {
		return "The removal was interrupted (" + cause + "). The speaker restarts by itself within about " +
			"two minutes, so it does not stay stopped. Wait until it is back, then check it: if it still " +
			"shows STR, run Remove again."
	}
	return "uninstall failed: " + cause + ". If the removal had already begun, the speaker restarts by " +
		"itself within about two minutes. Wait until it is back, then run Remove again."
}

// stashedPresetsNote is the sentence about the preset keys kept on this PC,
// empty when none were kept.
func stashedPresetsNote(stashed int) string {
	if stashed <= 0 {
		return ""
	}
	return fmt.Sprintf(" Its %d preset keys were kept on this PC and go back onto the speaker "+
		"the next time STR is installed on it from this app.", stashed)
}

// uninstallReport is what uninstallScript said about its run.
type uninstallReport struct {
	// guardArmed: the script armed its restart guard, so it got past the
	// stick check and the speaker reboots by itself.
	guardArmed bool
	// stickPresent: the script refused, the STR stick is still inserted.
	stickPresent bool
	// alreadyClean: nothing was there to remove and nothing is left.
	alreadyClean bool
	// stillStarts: rc.local and run-override.sh both survived, so STR starts
	// again on the next boot.
	stillStarts bool
	// removed: the paths the script deleted.
	removed []string
	// leftover: the paths that are still on the speaker after the removal.
	leftover []string
}

// parseUninstallOutput reads uninstallScript's markers. Markers count only at
// the start of a line, so a path echoed back inside another line cannot fake
// one.
func parseUninstallOutput(out string) uninstallReport {
	var r uninstallReport
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "STR_UNINSTALL_ABORT_STICK_PRESENT":
			r.stickPresent = true
		case line == uninstallGuardArmedMarker:
			r.guardArmed = true
		case line == "STR_UNINSTALL_ALREADY_CLEAN":
			r.alreadyClean = true
		case line == "STR_UNINSTALL_STILL_STARTS":
			r.stillStarts = true
		case strings.HasPrefix(line, "STR_UNINSTALL_REMOVED:"):
			r.removed = append(r.removed, strings.Fields(strings.TrimPrefix(line, "STR_UNINSTALL_REMOVED:"))...)
		case strings.HasPrefix(line, "STR_UNINSTALL_LEFTOVER:"):
			r.leftover = append(r.leftover, strings.Fields(strings.TrimPrefix(line, "STR_UNINSTALL_LEFTOVER:"))...)
		}
	}
	return r
}

// uninstallOutcome is how a script run that got as far as removing things
// ended.
type uninstallOutcome int

const (
	// uninstallSilent: the script reported neither a removal, nor leftovers,
	// nor a clean speaker. It did not run to its end.
	uninstallSilent uninstallOutcome = iota
	// uninstallClean: there was nothing to remove.
	uninstallClean
	// uninstallRemoved: everything is gone.
	uninstallRemoved
	// uninstallIncomplete: STR no longer starts, but files are left behind.
	uninstallIncomplete
	// uninstallStillInstalled: files are left behind and STR starts again on
	// the next boot.
	uninstallStillInstalled
)

// outcome classifies the report. Leftovers win over everything else: a run
// that removed rc.local and left the folder is not a success, however many
// entries it lists as removed.
func (r uninstallReport) outcome() uninstallOutcome {
	switch {
	case len(r.leftover) > 0 && r.stillStarts:
		return uninstallStillInstalled
	case len(r.leftover) > 0:
		return uninstallIncomplete
	case len(r.removed) > 0:
		return uninstallRemoved
	case r.alreadyClean:
		return uninstallClean
	}
	return uninstallSilent
}

// uninstallLeftoverMessage is the English text of the two leftover outcomes
// (the frontend shows its own translated sentence for the incomplete one).
//
// The advice differs on purpose. While STR still starts, a restart brings the
// agent back and Remove can simply run again. Once STR no longer starts, the
// restarted speaker is a stock one with SSH closed and a second Remove would
// only answer "already removed", so the way to the leftover files is a fresh
// install followed by a removal.
func uninstallLeftoverMessage(r uninstallReport) string {
	left := strings.Join(r.leftover, ", ")
	if r.stillStarts {
		return "STR could not be removed, its files are still on the speaker (" + left + "). " +
			"The speaker is restarting with STR still on it. Wait about 60 s until it is back, " +
			"then run Remove again."
	}
	return "STR no longer starts on this speaker, but some of its files could not be deleted (" + left + "). " +
		"The speaker is restarting as a plain Bose speaker; wait about 60 s. To clear the leftover files, " +
		"install STR on it again from this app and then remove it once more."
}

func (a *App) UninstallSTR(host string) UninstallSTRResult {
	res := UninstallSTRResult{Step: "start"}
	if host == "" {
		res.Message = "host is required"
		return res
	}
	// One writer per speaker, see boxbusy.go: removing STR under a running
	// install or update deletes the folder that run is writing into, and the
	// reboot at the end cuts off whatever is still going.
	release, busyErr := writesInFlight.claim(host, "a removal")
	if busyErr != nil {
		res.Step = "busy"
		res.Message = busyErr.Error()
		a.logger.Info("uninstall_str: refused, the speaker is busy", "host", host, "err", busyErr)
		return res
	}
	defer release()
	a.logger.Info("uninstall_str: starting", "host", host)
	// From here on no engine delivery may land on this speaker (see
	// App.uninstalling). The mark stays after a removal that took STR off the
	// speaker (also one that left files behind); a failed attempt lifts it
	// again so a later update can still complete the engine.
	a.uninstalling.Store(host, struct{}{})
	defer func() {
		if !res.OK && !res.Incomplete {
			a.uninstalling.Delete(host)
		}
	}()

	// Step 1: SSH handshake.
	res.Step = "ssh-handshake"
	hello, helloErr := sshHandshake(host, 4)
	if helloErr != nil || !strings.Contains(hello, "STR_SSH_OK") {
		// A hardened STR box keeps SSH CLOSED after the stick is pulled: since
		// v0.8.1 sshd follows the remote_services / enable-ssh marker, so a box
		// that was installed from a stick and then had the stick removed answers
		// nothing on :22 even though STR is installed and running. That made
		// "STR Remove" fail for every such box (user report).
		//
		// PRIMARY fix: the STR agent is still running here and is root on the box,
		// so ask IT to open SSH (touch the marker + start sshd). No stick, no
		// reboot, no :17000 marge-injection - which fights STR's own autopair on an
		// already-installed box and is unreliable there (that is exactly the path
		// that left a box marge-checking a dead host in testing).
		if a.enableSSHViaAgent(host) {
			hello, helloErr = sshHandshake(host, 4)
		}
	}
	if helloErr != nil || !strings.Contains(hello, "STR_SSH_OK") {
		// Fast path (#683 follow-up): a box STR already removed is back to stock and
		// answers the Bose API on :8090 with NEITHER STR agent port open (:8888 sm2,
		// :17008 BCO). Recognise that here, BEFORE the :17000 stick-free unlock
		// below, so pressing Remove again on an already-stock speaker returns at
		// once. Without this the second Remove ran the whole unlock-then-SSH dance
		// first and took as long as a real removal (shorty310, 2026-09-03).
		if hostAnswersAsStock(host) {
			a.finishAlreadyStock(host, &res, "uninstall_str: speaker is already stock, nothing to remove (fast path)")
			return res
		}
		// FALLBACK: the STR agent was not reachable (e.g. a wedged box). Try the
		// stick-free :17000 unlock like the installer, and restore stock cloud URLs
		// on EVERY exit so a failed attempt never leaves the box dialing the unlock
		// URL (the review-caught gap the install path already guards). The
		// uninstall is the one caller that keeps the factory-reset bootstrap as a
		// second rung after the plain unlock (ensureSSHOpen's bootstrap option).
		if opened, _, _ := a.ensureSSHOpen(host, "", sshUnlockOptions{tag: "uninstall_str", bootstrap: true}); opened {
			hello, helloErr = sshHandshake(host, 4)
		}
	}
	if helloErr != nil || !strings.Contains(hello, "STR_SSH_OK") {
		// #683: a box that STR already fully removed has rebooted back to stock, so
		// there is no STR agent left to open SSH and a stock box keeps :22 closed.
		// Pressing Remove a second time then failed HERE with the raw "exit status
		// 255" SSH error, when the honest answer is that STR is already gone. Tell
		// that case apart from a genuinely unreachable or wedged box: a live stock
		// speaker still answers the Bose API on :8090 while NEITHER STR agent port
		// (:8888 sm2, :17008 BCO) is open. A wedged STR box keeps its agent port
		// open, so it never reaches this branch; a powered-off box answers nothing,
		// so it keeps the reachability error rather than a false "already removed".
		if hostAnswersAsStock(host) {
			a.finishAlreadyStock(host, &res, "uninstall_str: speaker is already stock, nothing to remove")
			return res
		}
		res.Log = hello
		res.Message = "Could not open install access (SSH) to the speaker to remove STR: " + classifySSHError(hello, helloErr)
		return res
	}

	// Keep the speaker's preset keys on this PC before the script below
	// deletes the store together with the STR folder (#882): a reinstall
	// started with an empty store while the firmware still showed the old
	// six keys, and nothing brought them back. The deviceID is resolved
	// here, BEFORE purgeSpeakerState drops that memory further down. See
	// preset_stash.go; the next install from this app puts the keys back.
	stashed := a.stashPresetsBeforeUninstall(host, 0, a.deviceIDForHost(host))

	// Step 2: the whole uninstall runs as one script. It self-aborts up
	// front if the stick is still inserted (run.sh / the agent binary on
	// /media/sda1), so we never remove STR only to have Bose reinstall it
	// from the stick on the next boot.
	res.Step = "uninstall"
	out, err := boxSSHOutput(host, uninstallScript, uninstallScriptTimeout)
	res.Log = out
	rep := parseUninstallOutput(out)
	if err != nil && !rep.stickPresent {
		// The call failed or timed out, and nobody knows how far the script
		// got. If it was past its first kill, STR is stopped on the speaker and
		// nothing there starts it again. The script's own restart guard covers
		// that; a reboot from here as well gets the speaker back sooner when
		// SSH still answers. Restarting a speaker the script never touched
		// costs a minute and nothing else. A stick-present refusal is the one
		// answer that needs no reboot; it is handled below as usual.
		res.Step = "interrupted"
		res.Message = uninstallInterruptedMessage(classifySSHError(out, err), rep.guardArmed)
		a.logger.Warn("uninstall_str: the script run was lost; rebooting the speaker so it does not stay stopped",
			"host", host, "guardArmed", rep.guardArmed, "err", err, "log", out)
		_ = boxReboot(host)
		return res
	}
	if rep.stickPresent {
		res.StickPresent = true
		res.Step = "stick-present"
		res.Message = "The USB stick is still in the speaker. Pull it out first, " +
			"otherwise the speaker reinstalls STR from the stick on the next reboot. " +
			"Remove the stick and try again."
		a.logger.Info("uninstall_str: aborted, stick still inserted", "host", host)
		return res
	}
	res.RemovedFiles = rep.removed
	res.LeftoverFiles = rep.leftover
	// Nothing removed is only a failure when there was something to remove.
	// Running Remove on a speaker that is ALREADY clean is the most ordinary
	// thing in the world: a user who saw no confirmation the first time presses
	// it again. That produced a red error, and worse, this early return skipped
	// everything below it. So the app went on remembering a stock speaker as an
	// STR one (see forgetSTRDeviceByHost) and the reboot that closes root SSH
	// never ran.
	//
	// A reporter walked straight into it on 2026-08-23 and then asked the exact
	// question the bug produces: why did one speaker flip to "READY FOR STR" by
	// itself while the other needed the app restarted. The first came off in one
	// go, the second was the second attempt.
	outcome := rep.outcome()
	alreadyClean := outcome == uninstallClean
	switch outcome {
	case uninstallSilent:
		// The script ended without its report, so it may have died after it
		// stopped STR. Same answer as a lost call: restart the speaker.
		res.Step = "interrupted"
		res.Message = uninstallInterruptedMessage("the script ended without its report, see the log", rep.guardArmed)
		a.logger.Warn("uninstall_str: the script ended without its report; rebooting the speaker so it does not stay stopped",
			"host", host, "guardArmed", rep.guardArmed, "log", out)
		_ = boxReboot(host)
		return res
	case uninstallStillInstalled:
		// Nothing that matters came off: rc.local and the run script are both
		// still there. The script has stopped the agent and its watchdogs, so
		// without a reboot the speaker would sit there with STR installed and
		// nothing running. Restart it: STR comes back up, and with it the
		// agent that opens SSH for the next attempt. The app keeps remembering
		// the speaker as the STR speaker it still is.
		res.Step = "leftover"
		res.StillInstalled = true
		res.Message = uninstallLeftoverMessage(rep)
		a.logger.Warn("uninstall_str: nothing effective was removed, STR still starts; rebooting so it comes back",
			"host", host, "leftover", rep.leftover, "removed", rep.removed, "log", out)
		_ = boxReboot(host)
		return res
	case uninstallIncomplete:
		// rc.local or the run script is gone, so STR no longer starts, but
		// files are left (typically the folder itself). Reported as what it
		// is: not a success. The speaker behaves like a stock one from here
		// on, so the app's memory of it and the reboot follow the success
		// path; a stock speaker left un-rebooted would keep root SSH open.
		res.Incomplete = true
		a.logger.Warn("uninstall_str: STR no longer starts, but files are left behind",
			"host", host, "leftover", rep.leftover, "removed", rep.removed, "log", out)
	case uninstallClean:
		a.logger.Info("uninstall_str: speaker was already clean, nothing to remove", "host", host)
	case uninstallRemoved:
		a.logger.Info("uninstall_str: removed", "host", host, "removedCount", len(res.RemovedFiles))
	}

	// The box is going back to stock: rewrite its cached record as the stock
	// speaker it now is and drop its confirmed-STR identity memory. Merely
	// forgetting the record (the old behaviour) let the next discovery bring
	// the box back as STR with its old agent version, because the box's stock
	// :8090 kept answering and a presence-only sighting keeps a cached STR
	// record alive; the Listen to music card then showed "v0.9.74" on a
	// speaker with no STR on it (2026-09-06 report).
	a.purgeSpeakerState(host, "")
	a.markHostStock(host)

	// Step 3: reboot into vanilla Bose. Connection drops mid-command;
	// fire-and-forget so the drop is not treated as a failure.
	res.Step = "reboot"
	_ = boxReboot(host)

	if res.Incomplete {
		res.Step = "incomplete"
		// The preset keys were stashed before the script ran, and a fresh
		// install starts with a new store, so the note applies here too.
		res.Message = uninstallLeftoverMessage(rep) + stashedPresetsNote(stashed)
		a.logger.Info("uninstall_str: done, incomplete", "host", host, "leftover", rep.leftover)
		return res
	}
	res.Step = "done"
	res.OK = true
	if alreadyClean {
		res.Message = "This speaker already had no STR on it, so there was nothing to remove. " +
			"It is rebooting into factory Bose state; wait about 60 s. To bring STR back, " +
			"install it again from this app."
		a.logger.Info("uninstall_str: done, speaker was already clean", "host", host)
		return res
	}
	res.Message = fmt.Sprintf("STR removed (%d entries) and the speaker is rebooting into "+
		"factory Bose state. Wait ~60 s. The speaker no longer runs STR; onboard it again "+
		"with the Bose iOS app, or re-insert a prepared STR stick to bring STR back.",
		len(res.RemovedFiles))
	res.Message += stashedPresetsNote(stashed)
	a.logger.Info("uninstall_str: done", "host", host, "removedCount", len(res.RemovedFiles))
	return res
}

// hostAnswersAsStock reports whether host looks like a live speaker with no STR
// on it: the Bose API answers on :8090 while neither STR agent port (:8888
// sm2, :17008 BCO) is open.
func hostAnswersAsStock(host string) bool {
	return tcpReachable(host, 8090, 2*time.Second) &&
		!tcpReachable(host, 8888, 1500*time.Millisecond) &&
		!tcpReachable(host, 17008, 1500*time.Millisecond)
}

// finishAlreadyStock is the outcome of Remove on a speaker that has no STR on
// it any more: the app's memory of it is rewritten to the stock speaker it is,
// and res says so. logMsg tells the two places it is recognised apart.
func (a *App) finishAlreadyStock(host string, res *UninstallSTRResult, logMsg string) {
	a.purgeSpeakerState(host, "")
	a.markHostStock(host)
	res.Step = "already-stock"
	res.OK = true
	res.Message = "STR is already removed from this speaker. It is back to a stock Bose speaker, " +
		"so there was nothing to remove and nothing else is needed."
	a.logger.Info(logMsg, "host", host)
}

// enableSSHViaAgent asks the still-running STR agent to open root SSH: the agent
// is root on the box, so POST /api/agent/enable-ssh makes it touch the
// remote_services marker and start sshd (see internal/webui handleAgentEnableSSH).
// This is the clean way to open SSH on a box that already runs STR - no stick, no
// reboot, and none of the :17000 marge-injection that is unreliable against STR's
// own autopair. Returns true once :22 accepts a connection. Best-effort: false if
// the agent is not reachable or SSH does not come up in time.
func (a *App) enableSSHViaAgent(host string) bool {
	bi, ok := probeSTR(a.appCtx(), host)
	if !ok {
		return false
	}
	a.logger.Info("uninstall_str: SSH closed but the STR agent is reachable; asking it to open SSH", "host", host, "port", bi.Port)
	resp, err := a.boxDo(host, bi.Port, http.MethodPost, "/api/agent/enable-ssh", "", "")
	if err != nil {
		a.logger.Warn("uninstall_str: agent enable-ssh request failed", "host", host, "err", err)
		return false
	}
	_ = resp.Body.Close()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if tcpReachable(host, 22, 2*time.Second) {
			a.logger.Info("uninstall_str: the STR agent opened SSH", "host", host)
			return true
		}
		time.Sleep(2 * time.Second)
	}
	a.logger.Info("uninstall_str: agent enable-ssh did not open :22 within budget", "host", host)
	return false
}
