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
type UninstallSTRResult struct {
	Step         string   `json:"step"`
	OK           bool     `json:"ok"`
	StickPresent bool     `json:"stickPresent"`
	Message      string   `json:"message"`
	Log          string   `json:"log"`
	RemovedFiles []string `json:"removedFiles"`
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
func (a *App) UninstallSTR(host string) UninstallSTRResult {
	res := UninstallSTRResult{Step: "start"}
	if host == "" {
		res.Message = "host is required"
		return res
	}
	a.logger.Info("uninstall_str: starting", "host", host)

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
		// FALLBACK: the STR agent was not reachable (e.g. a wedged box). Try the
		// stick-free :17000 unlock like the installer, and restore stock cloud URLs
		// on EVERY exit so a failed attempt never leaves the box dialing the unlock
		// URL (the review-caught gap the install path already guards).
		if tcpReachable(host, 17000, 2*time.Second) {
			a.logger.Info("uninstall_str: STR agent unreachable; trying the :17000 stick-free unlock", "host", host)
			opened, _ := a.enableSSHViaTelnet(host, "")
			if !opened {
				opened, _ = a.enableSSHViaTelnetBootstrap(host, "")
			}
			if opened {
				if rerr := a.resetBoseURLsViaTelnet(host); rerr != nil {
					a.logger.Warn("uninstall_str: could not restore stock boseurls after the :17000 unlock", "host", host, "err", rerr)
				}
				hello, helloErr = sshHandshake(host, 4)
			} else {
				a.restoreStockBoseURLsAndReboot(host)
			}
		}
	}
	if helloErr != nil || !strings.Contains(hello, "STR_SSH_OK") {
		res.Log = hello
		res.Message = "Could not open install access (SSH) to the speaker to remove STR: " + classifySSHError(hello, helloErr)
		return res
	}

	// Step 2: the whole uninstall runs as one script. It self-aborts up
	// front if the stick is still inserted (run.sh / the agent binary on
	// /media/sda1), so we never remove STR only to have Bose reinstall it
	// from the stick on the next boot.
	res.Step = "uninstall"
	const script = `set -u
if [ -e /media/sda1/run.sh ] || [ -e /media/sda1/streborn-armv7l ] || [ -e /media/sda1/streborn ]; then
  echo "STR_UNINSTALL_ABORT_STICK_PRESENT"
  exit 0
fi
REMOVED=""
# Stop the running agent so it cannot rewrite anything mid-uninstall.
pkill -TERM streborn-armv7l 2>/dev/null
sleep 1
pkill -KILL streborn-armv7l 2>/dev/null
# Remove the STR install + the NAND override entry point.
if [ -d /mnt/nv/streborn ]; then rm -rf /mnt/nv/streborn && REMOVED="$REMOVED /mnt/nv/streborn"; fi
if [ -f /mnt/nv/rc.local ]; then rm -f /mnt/nv/rc.local && REMOVED="$REMOVED /mnt/nv/rc.local"; fi
# STR/go-librespot traces that live OUTSIDE /mnt/nv/streborn, so the rm -rf above
# leaves them behind: go-librespot's Spotify oauth dump (can be multi-MB) and a
# stranded SSH-repair install-staging dir. Uninstall is "back to stock", so drop
# every trace, not just the install dir.
if [ -f /mnt/nv/sp-oauth.out ]; then rm -f /mnt/nv/sp-oauth.out && REMOVED="$REMOVED /mnt/nv/sp-oauth.out"; fi
if [ -d /mnt/nv/streborn-install ]; then rm -rf /mnt/nv/streborn-install && REMOVED="$REMOVED /mnt/nv/streborn-install"; fi
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
echo "STR_UNINSTALL_REMOVED:$REMOVED"
`
	out, err := boxSSHOutput(host, script, 40*time.Second)
	res.Log = out
	if err != nil {
		res.Message = "uninstall failed: " + classifySSHError(out, err)
		return res
	}
	if strings.Contains(out, "STR_UNINSTALL_ABORT_STICK_PRESENT") {
		res.StickPresent = true
		res.Step = "stick-present"
		res.Message = "The USB stick is still in the speaker. Pull it out first, " +
			"otherwise the speaker reinstalls STR from the stick on the next reboot. " +
			"Remove the stick and try again."
		a.logger.Info("uninstall_str: aborted, stick still inserted", "host", host)
		return res
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "STR_UNINSTALL_REMOVED:") {
			res.RemovedFiles = append(res.RemovedFiles, strings.Fields(strings.TrimPrefix(line, "STR_UNINSTALL_REMOVED:"))...)
		}
	}
	if len(res.RemovedFiles) == 0 {
		res.Message = "uninstall returned no removed-file list — script may have failed silently. See log."
		return res
	}
	a.logger.Info("uninstall_str: removed", "host", host, "removedCount", len(res.RemovedFiles))

	// The box is going back to stock, so drop its confirmed-STR identity memory:
	// otherwise discovery would keep relabelling the now-stock speaker as STR for
	// up to strKnownTTL and never offer the reinstall it now genuinely needs.
	a.forgetSTRDeviceByHost(host)

	// Step 3: reboot into vanilla Bose. Connection drops mid-command;
	// fire-and-forget so the drop is not treated as a failure.
	res.Step = "reboot"
	_ = boxReboot(host)

	res.Step = "done"
	res.OK = true
	res.Message = fmt.Sprintf("STR removed (%d entries) and the speaker is rebooting into "+
		"factory Bose state. Wait ~60 s. The speaker no longer runs STR; onboard it again "+
		"with the Bose iOS app, or re-insert a prepared STR stick to bring STR back.",
		len(res.RemovedFiles))
	a.logger.Info("uninstall_str: done", "host", host, "removedCount", len(res.RemovedFiles))
	return res
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
