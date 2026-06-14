// In-app STR installer: runs install.sh on a stock Bose SoundTouch
// over SSH, reboots the box, and waits for the STR agent to come
// up on port 8888. Replaces the manual PowerShell wizard step for
// end users who only ever touch the desktop app.
//
// Auth: passwordless root. Bose's stock firmware ships /etc/shadow
// with an empty password hash for root and the default sshd config
// accepts it as long as the remote_services marker is present on
// /media/sda1 (which our stick provisioning writes). No key, no
// password, no UAC.

package main

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/sticksetup"
	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"
	"streborn-app/agentbin"
)

// InstallResult is the JSON-serialisable outcome of an STR install
// attempt. The frontend uses Step to drive live progress and OK +
// Message for the final state.
type InstallResult struct {
	Step string `json:"step"`
	// Code is a stable machine-readable failure class the frontend maps to a
	// localized, step-by-step help checklist (see setup.help.* in the i18n
	// bundles). Empty on success. Distinct from Step (which tracks progress)
	// and Message (the human sentence): Code never changes wording, so the UI
	// can key help text off it across all 11 languages.
	Code     string `json:"code"`
	OK       bool   `json:"ok"`
	Message  string `json:"message"`
	Log      string `json:"log"`
	Firmware string `json:"firmware"` // Bose firmware read from :8090/info, for diagnostics
}

// stickProbePaths are the candidate mount paths checked for the STR
// install.sh on the box. Bose's udev rule normally lands USB sticks
// at /media/sda1 across every model we have observed (ST10
// micro-USB, ST20/30 USB-A — same /etc/udev/scripts/mount.sh), but
// the list is intentionally broad so we never give up on a firmware
// variant that numbers or names mountpoints differently. Probed in
// order: the most common slot first.
var stickProbePaths = []string{
	"/media/sda1", "/media/sdb1", "/media/sdc1", "/media/sdd1",
	"/media/usb", "/media/usb0", "/media/usb1",
	"/media/usbhd-sda1", "/media/usbhd-sdb1",
	"/mnt/usb", "/mnt/usb0", "/mnt/usb1", "/mnt/sda1", "/mnt/sdb1",
	"/run/media/sda1", "/run/media/sdb1",
}

// InstallSTROnBox runs the full install on a box that has a freshly
// provisioned STR stick mounted somewhere under /media or /mnt.
// Steps:
//  1. probe SSH and locate install.sh on the stick
//  2. run "sh <stick>/install.sh install"
//  3. reboot the box
//  4. poll port 8888 for the STR agent, model-aware: 240 s for Series-I /
//     BCO (Portable) and unknown models, 180 s for Series-II (ST10/20/30
//     classic). On timeout, if SSH is still open, reboot once and retry;
//     otherwise return model-specific recovery guidance.
//
// Caller passes the box's home-LAN IP and its model (from discovery, may be
// empty). Returns a step-tagged result even on failure so the UI can show the
// user where it stopped, and captures SSH stderr into res.Log so the user can
// see the actual failure reason instead of an opaque exit code.
func (a *App) InstallSTROnBox(host, model string) (InstallResult, error) {
	res := InstallResult{Step: "start"}
	if host == "" {
		return res, fmt.Errorf("host is required")
	}
	a.logger.Info("install_str: starting", "host", host, "model", model)

	// Read the box firmware (Bose :8090/info, reachable on stock and STR boxes)
	// up front so it lands in the result and the logs. An old firmware is a
	// candidate cause when a box never brings up the agent, so capturing it here
	// lets us confirm or rule it out, and warn the user to update (#114).
	fwNote := ""
	if fw, ferr := a.GetBoxFirmware(host); ferr == nil && fw.Reachable {
		res.Firmware = fw.Short
		a.logger.Info("install_str: box firmware", "host", host, "model", fw.Model,
			"firmware", fw.Firmware, "moduleType", fw.ModuleType, "variant", fw.Variant, "outdated", fw.Outdated)
		if fw.Outdated && fw.Short != "" {
			fwNote = " The speaker firmware is " + fw.Short + ", older than the latest Bose firmware " + latestBoseFirmware +
				". Update the speaker in the Bose SoundTouch app first, then run the install again."
		}
	}

	// Step 0a: preflight TCP reachability on the SSH port. SSH failing with a
	// bare "exit status 255" and no stderr is the opaque error users hit when
	// the box is simply not reachable (wrong/disconnected network, mid-reboot,
	// not yet onboarded to Wi-Fi). Reported on ST10.
	// Checking :22 first lets us return a human instruction instead of the
	// raw SSH exit code.
	res.Step = "preflight"
	if !tcpReachable(host, 22, 4*time.Second) {
		// :22 closed does NOT necessarily mean the box is off the network.
		// Bose only opens sshd while the box boots with the stick inserted
		// (the remote_services marker), so a fully-onboarded box that is
		// reachable on its Bose REST port (:8090) but has :22 closed is the
		// "install window closed" case, not a network problem. An's
		// ST10 diagnostic (06.06.) showed exactly this: 8090 reachable,
		// SSH not. Probe the Bose port so we can tell the two apart and give
		// an instruction the user can actually act on, instead of wrongly
		// blaming the network.
		if tcpReachable(host, 8888, 3*time.Second) {
			res.Code = "already-installed"
			res.Message = "The speaker at " + host + " already answers on the STR agent port (8888), " +
				"so it looks like STR is installed already. Refresh the speaker list. " +
				"If you meant to reinstall, reboot the speaker with the STR stick plugged in first."
			a.logger.Warn("install_str: preflight, :22 closed but :8888 up (already installed?)", "host", host)
			return res, nil
		}
		if tcpReachable(host, 8090, 3*time.Second) {
			res.Code = "install-window-closed"
			res.Message = "The speaker at " + host + " is on the network, but the install access (SSH) is closed. " +
				"Bose only opens it while the speaker boots with the STR stick plugged in. " +
				"Power the speaker off, insert the STR stick, power it back on, then install."
			a.logger.Warn("install_str: preflight, box reachable on :8090 but :22 closed (install window shut)", "host", host)
			return res, nil
		}
		res.Code = "not-reachable"
		res.Message = "The speaker is not reachable on the network (no answer on SSH port 22 or the Bose port 8090 at " + host + "). " +
			"First bring it onto your Wi-Fi with the Bose SoundTouch app and make sure this PC and the speaker are on the same network. " +
			"Then reboot the speaker with the STR stick plugged in and try again."
		a.logger.Warn("install_str: preflight failed, box not reachable on :22 or :8090", "host", host)
		return res, nil
	}

	// Step 0b: SSH itself reachable + authenticated? We do this as a
	// separate trivial command so a connect/auth/algorithm failure
	// surfaces with a specific message instead of looking like a
	// missing stick. The probe also doubles as a warmup so the next
	// SSH call reuses the negotiated host key.
	//
	// On failure we return (res, nil), NOT a wrapped error: Wails delivers
	// only the error to the frontend when the error is non-nil and drops the
	// res value, so the carefully classified res.Message would be lost and the
	// user would see the raw "ssh handshake: exit status 255" again. The
	// frontend renders res.Message + res.Log on res.OK == false.
	res.Step = "ssh-handshake"
	// 4 spaced attempts; see sshHandshake for the #114 sshd-warmup rationale.
	hello, helloErr := sshHandshake(host, 4)
	if helloErr != nil || !strings.Contains(hello, "STR_SSH_OK") {
		res.Log = hello
		res.Code = "ssh-handshake"
		hint := classifySSHError(hello, helloErr)
		res.Message = "SSH handshake to speaker failed: " + hint
		a.logger.Warn("install_str: ssh handshake failed after retries", "host", host, "err", helloErr, "hint", hint)
		return res, nil
	}
	a.logger.Info("install_str: ssh ok", "host", host)

	// Step 1: stick mounted, install.sh present. Retry up to ~60 s
	// because sshd answers before the USB stack has finished
	// mounting the stick on first boot. The probe checks a broad
	// set of candidate paths (see stickProbePaths) and additionally
	// scans /media + /mnt + /run/media for *any* directory that
	// holds an install.sh — so even an entirely new firmware variant
	// is recoverable without a code change.
	res.Step = "check-stick"
	probeCmd := buildStickProbeCmd(stickProbePaths)
	var probe string
	var probeErr error
	stickPath := ""
	for attempt := 0; attempt < 20; attempt++ {
		// 14 s per attempt (was 8 s): on slower boxes the SSH session can stall
		// mid-probe right after boot, and an 8 s cap turned that into
		// "ssh probe failed after retries err=ssh timeout after 8s" even though
		// the box was up (#114). 20 attempts x (14 s + 3 s) still backstops a
		// genuinely dead box.
		probe, probeErr = boxSSHOutput(host, probeCmd, 14*time.Second)
		if probeErr == nil && strings.Contains(probe, "STICKPATH=") {
			for _, line := range strings.Split(probe, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "STICKPATH=") {
					stickPath = strings.TrimPrefix(line, "STICKPATH=")
					break
				}
			}
			if stickPath != "" {
				break
			}
		}
		if attempt == 19 {
			res.Log = probe
			if probeErr != nil {
				res.Code = "ssh-probe"
				hint := classifySSHError(probe, probeErr)
				res.Message = "ssh probe failed after retries: " + hint
				a.logger.Warn("install_str: ssh probe failed after retries", "host", host, "err", probeErr, "hint", hint)
				// (res, nil): keep res.Message reaching the frontend, see ssh-handshake.
				return res, nil
			}
			res.Code = "stick-missing"
			res.Message = "install.sh did not appear under /media, /mnt or /run/media within 60 s. " +
				"On a SoundTouch 30, if no stick mounts at all: try a small plain USB 2.0 stick (4 to 32 GB), avoid SD-card adapters and USB 3 / large drives, and try BOTH USB ports:" +
				"the rear USB-A port and the micro-USB port via a micro-USB OTG adapter. Reboot the speaker after inserting it." +
				"If several sticks in both ports still do not mount, the speaker's USB port may be faulty."
			res.Log = res.Log + "\n\n--- box install diagnostics (SSH up) ---\n" + boxInstallDiag(host)
			return res, nil
		}
		time.Sleep(3 * time.Second)
	}
	a.logger.Info("install_str: stick found", "host", host, "path", stickPath)

	// Step 1c: wait for the box's boot-time CPU storm to subside before
	// launching install.sh. Driven by the actual load average, not a blind
	// timer (see waitForBoxLoad); capped so a permanently busy box still gets
	// its install attempt. Emits live progress so the UI can show "reachable
	// but still finishing boot" instead of a frozen screen.
	res.Step = "settle"
	a.waitForBoxLoad(host, model)

	// Step 2: run install.sh install. The timeout is model-aware: install.sh
	// does heavy on-box work (copy the agent to NAND, rewrite wpa_supplicant,
	// regenerate TLS) and on the bigger/slower models, or over a weak Wi-Fi
	// link, that routinely takes well over a minute. The original flat 60 s
	// cut ST30 first installs off mid-script ("install.sh ssh timeout after
	// 1m0s", #119) even though the SSH session was healthy.
	res.Step = "run-install"
	out, err := boxSSHOutput(host, "sh "+stickPath+"/install.sh install 2>&1", installRunBudget(model))
	res.Log = out
	if err != nil {
		res.Code = "install-error"
		lowOut := strings.ToLower(out)
		// Gather the box diagnostics now, before classifying: the kernel dmesg in
		// here is what lets us tell a genuinely unreadable/oversized stick apart
		// from a USB power dropout (see detectUSBPowerFailure).
		diag := boxInstallDiag(host)
		switch {
		case strings.Contains(err.Error(), "timeout"):
			res.Code = "install-timeout"
		case strings.Contains(lowOut, "input/output error"), strings.Contains(lowOut, "i/o error"):
			// Media-level read failure of install.sh. Distinguish the two causes:
			// a USB power/enumeration dropout (the ST30's port cannot keep VBUS up
			// for a slightly higher-draw stick, so it disconnects mid-read) vs a
			// genuinely unreadable or faulty stick. In the power case the SAME stick
			// installs fine on ST10/ST20, so blaming the stick is misleading; the
			// dmesg VBUS_ERROR / error -110 signature is the discriminator
			// (multiple independent ST30 users, 13.06.2026).
			if detectUSBPowerFailure(diag) {
				res.Code = "stick-usb-power"
			} else {
				res.Code = "stick-io-error"
			}
		}
		hint := classifySSHError(out, err)
		if res.Code == "stick-usb-power" {
			hint = usbPowerHint
		}
		res.Message = "install.sh execution failed: " + hint
		// The rich diagnostics must also reach str.log, not only res.Log:
		// a remote user's str.log is often all we get, and without this it
		// showed just the one-line hint (no kernel/stick/dmesg evidence).
		a.logger.Warn("install_str: install.sh execution failed",
			"host", host, "model", model, "err", err, "code", res.Code,
			"hint", hint, "installOutput", truncForLog(out, 4000), "boxDiag", truncForLog(diag, 8000))

		// The stick could not be read for install.sh, but our SSH session runs
		// over Wi-Fi, independent of the stick's USB power: we already have
		// "ssh ok" by this point. So instead of sending the user off to find a
		// different stick or a powered hub, bypass the stick automatically, the
		// exact reason RepairInstallViaSSH exists: it stages STR's embedded files
		// onto NAND over SSH and installs from there, never touching the stick.
		// Applied up front for any media read failure, whether a USB power dropout
		// (ST30 VBUS) or a genuinely unreadable stick. The classified
		// power/faulty-stick message stays as the fallback for when even the
		// bypass cannot complete (SSH dropped, or a dev build with no embedded
		// binary). Mirrors the post-reboot auto-repair in the wait-agent step.
		if (res.Code == "stick-usb-power" || res.Code == "stick-io-error") &&
			len(agentbin.Bytes()) > 0 && tcpReachable(host, 22, 4*time.Second) {
			a.logger.Info("install_str: media read failed but SSH up, auto NAND-install over SSH (bypassing the stick)",
				"host", host, "code", res.Code)
			res.Step = "stick-copy-repair"
			rr, _ := a.RepairInstallViaSSH(host, model)
			if rr.OK {
				if a.waitForAgent(host, model) == nil {
					res.Step = "done"
					res.OK = true
					res.Message = "The USB stick could not be read during install, so STR was installed directly over the network instead. The agent is up on port 8888."
					return res, nil
				}
				// Installed over the network (stick bypassed); the agent just has
				// not bound :8888 yet. Do NOT tell the user to swap sticks, this is
				// now a plain slow boot.
				a.logger.Warn("install_str: NAND-install over network ran but agent not up yet", "host", host)
				res.Step = "wait-agent"
				res.Code = "agent-not-up"
				res.Message = "STR was installed directly over the network because the USB stick could not be read, but the speaker did not bring up the STR agent on port 8888 in time. It may still be rebooting; refresh the speaker list in a minute." + fwNote
				res.Log = res.Log + "\n\n--- box install diagnostics (SSH up) ---\n" + diag
				return res, nil
			}
			// Bypass itself could not complete: keep the classified cause and the
			// manual Repair-over-SSH offer, and attach the repair output.
			res.Log = res.Log + "\n\n--- SSH NAND-install repair ---\n" + rr.Message + "\n" + rr.Log
			a.logger.Warn("install_str: auto NAND-install over SSH did not complete", "host", host, "repairMsg", rr.Message)
		}

		res.Log = res.Log + "\n\n--- box install diagnostics (SSH up) ---\n" + diag
		// (res, nil): keep res.Message reaching the frontend, see ssh-handshake.
		return res, nil
	}
	if strings.Contains(out, "FEHLER") || strings.Contains(out, "ERROR") {
		res.Code = "install-script-error"
		res.Message = "install.sh reported an error. See log."
		return res, nil
	}
	a.logger.Info("install_str: install.sh ran", "host", host, "outBytes", len(out))

	// Step 3: reboot. The ssh call ends with the box dropping the
	// connection, which manifests as a non-zero exit — that is the
	// expected success path here, not an error.
	res.Step = "reboot"
	_ = boxReboot(host)
	a.logger.Info("install_str: reboot signal sent", "host", host)

	// Step 4: poll port 8888 (the STR agent webui), model-aware (240 s for
	// Series-I/BCO/unknown, 180 s for Series-II). Series-I/BCO boxes boot
	// slowly and the boot-race watchdog (run-override.sh re-fire loop) needs
	// time to win against Bose's service manager; cutting that off early was
	// the main first-install failure (#114).
	res.Step = "wait-agent"
	if err := a.waitForAgent(host, model); err != nil {
		// Specific message + finalize for the "stick read failed, so the agent
		// binary never reached NAND" case. Defined once; used from both the
		// pre-retry detection and the post-retry re-check below.
		const stickCopyMsg = "The speaker started but could not copy the STR program from the USB stick into its memory (stick read error), so it never finished starting up. The stick is most likely faulty or was not plugged in firmly. Re-create the stick, ideally on a different USB 2.0 stick (4 to 32 GB), plug it in firmly, then install again."
		finishStickCopyFailed := func() (InstallResult, error) {
			res.Step = "wait-agent"
			res.Code = "stick-copy-failed"
			res.Message = stickCopyMsg + fwNote
			res.Log = res.Log + "\n\n--- box install diagnostics (SSH up) ---\n" + boxInstallDiag(host)
			return res, nil
		}

		// Shared "how to get help" line, used by every failure branch below.
		logHint := "To get help, save the diagnostic logs (the Save logs button in Speaker Settings, or the link at the bottom of the app window) and attach them to the GitHub issue or email them to str@sichtbar-app.de."

		// If SSH is still reachable the install window is open. Before the blind
		// reboot-retry, check the box's own run.sh log for a deterministic cause a
		// reboot cannot fix: the agent binary never reached the NAND cache because
		// the USB stick read failed mid-copy (flaky/failing stick) and there was
		// no prior cache. A plain reboot just hits the same unreadable stick. The
		// fix is to bypass the stick entirely: stage STR's embedded agent onto
		// NAND over SSH and boot from there. Guarded on SSH still being up so we
		// never double-reboot a box that already left the install window.
		if tcpReachable(host, 22, 4*time.Second) {
			if a.agentLogShowsStickCopyFailure(host) {
				a.logger.Warn("install_str: agent not up, stick->NAND copy failed (unreadable stick)", "host", host)
				if len(agentbin.Bytes()) > 0 {
					a.logger.Info("install_str: auto NAND-copy repair over SSH, bypassing the unreadable stick", "host", host)
					res.Step = "stick-copy-repair"
					rr, _ := a.RepairInstallViaSSH(host, model)
					if rr.OK {
						if a.waitForAgent(host, model) == nil {
							res.Step = "done"
							res.OK = true
							res.Message = "The USB stick could not be read, so STR was installed directly over the network instead. The agent is up on port 8888."
							return res, nil
						}
						// The over-the-network install succeeded (the stick was
						// bypassed), the agent just has not bound :8888 yet. Do NOT
						// tell the user to recreate the stick: this is now a plain
						// slow/failed boot, so return the agent-not-up guidance.
						a.logger.Warn("install_str: NAND-copy repair installed over network but agent still not up", "host", host)
						res.Step = "wait-agent"
						res.Code = "agent-not-up"
						res.Message = "STR was installed directly over the network because the USB stick could not be read, but the speaker did not bring up the STR agent on port 8888 in time. It may still be rebooting; refresh the speaker list in a minute. " + logHint + fwNote
						res.Log = res.Log + "\n\n--- box install diagnostics (SSH up) ---\n" + boxInstallDiag(host)
						return res, nil
					}
					// Repair could not even complete (SSH copy/install failed):
					// keep the stick-copy diagnosis + manual repair offer below.
					res.Log = res.Log + "\n\n--- SSH NAND-copy repair ---\n" + rr.Message + "\n" + rr.Log
					a.logger.Warn("install_str: NAND-copy repair did not complete", "host", host, "repairMsg", rr.Message)
				}
				// No embedded binary (dev build) or the auto-repair could not
				// complete: report the specific stick-copy cause. The manual
				// Repair-over-SSH button is also offered for this code.
				return finishStickCopyFailed()
			}

			// Generic boot-race self-heal: reboot once and retry. This covers the
			// common "shim lost the boot race" case with no user action.
			a.logger.Info("install_str: agent not up, SSH still open, rebooting once and retrying", "host", host)
			res.Step = "reboot-and-retry"
			_ = boxReboot(host)
			if a.waitForAgent(host, model) == nil {
				res.Step = "done"
				res.OK = true
				res.Message = "STR agent is up on port 8888 (after an automatic retry)."
				return res, nil
			}
			// The first wait may have timed out before run.sh logged the copy
			// failure; re-check after the retry so a stick-copy failure that only
			// surfaced now still gets the specific cause, not the generic message.
			if tcpReachable(host, 22, 4*time.Second) && a.agentLogShowsStickCopyFailure(host) {
				return finishStickCopyFailed()
			}
		}
		res.Step = "wait-agent"
		res.Code = "agent-not-up"
		if slowBootModel(model) {
			res.Message = "The speaker is still starting. On Portable / BCO models this can take 2 to 3 minutes. " +
				"Keep the STR stick plugged in, power-cycle the speaker (unplug for 10 seconds, plug back in with the stick in place), " +
				"wait 2 to 3 minutes, then refresh the speaker list. " + logHint
		} else {
			res.Message = "The speaker did not bring up the STR agent on port 8888 in time. " +
				"It may still be rebooting; refresh the speaker list in a minute. " + logHint
		}
		res.Message += fwNote
		return res, nil
	}
	res.Step = "done"
	res.OK = true
	res.Message = "STR agent is up on port 8888."
	return res, nil
}

// buildStickProbeCmd returns a single-line shell command (BusyBox-
// compatible) that prints STICKPATH=<path> for the first candidate
// that holds an executable install.sh, then falls back to scanning
// /media, /mnt and /run/media for *any* directory containing one.
// MISSING is printed if nothing matches so the caller can
// distinguish "no stick" from "ssh died".
func buildStickProbeCmd(paths []string) string {
	var b strings.Builder
	for _, p := range paths {
		b.WriteString(`if [ -e `)
		b.WriteString(p)
		b.WriteString(`/install.sh ]; then echo "STICKPATH=`)
		b.WriteString(p)
		b.WriteString(`"; exit 0; fi; `)
	}
	// Fallback wide scan: any subdir directly under /media /mnt
	// /run/media that holds an install.sh. -maxdepth 2 keeps the
	// scan cheap even on a busy /mnt with many bind mounts.
	b.WriteString(
		`for d in /media /mnt /run/media; do ` +
			`if [ -d "$d" ]; then ` +
			`for cand in $d/*; do ` +
			`if [ -e "$cand/install.sh" ]; then ` +
			`echo "STICKPATH=$cand"; exit 0; ` +
			`fi; done; fi; done; echo MISSING`)
	return b.String()
}

// RepairInstallViaSSH is the install fallback for when the USB stick itself is
// unreadable: install.sh dies with exit 126 / an I/O error because a large or
// faulty stick was force-formatted FAT32 with a block size the speaker's old
// kernel cannot read (ST30 #119). Instead of reading from the stick it stages
// STR's embedded files (install.sh, run.sh, rc.local, the agent binary, ...)
// into a NAND directory over SSH and runs install.sh from there with STR_STICK
// pointing at that directory, bypassing the stick entirely. /mnt/nv is a
// writable ubifs even when the USB mount is dead. Requires SSH up (Bose stock
// root SSH) and a real embedded agent (release build, not a dev stub).
func (a *App) RepairInstallViaSSH(host, model string) (InstallResult, error) {
	res := InstallResult{Step: "repair-ssh"}

	if len(agentbin.Bytes()) == 0 {
		res.Code = "no-embedded-agent"
		res.Message = "this build has no embedded agent binary; SSH repair needs a release build"
		return res, nil
	}

	hello, helloErr := sshHandshake(host, 4)
	if !strings.Contains(hello, "STR_SSH_OK") {
		res.Code = "ssh-failed"
		res.Message = "SSH to speaker failed: " + classifySSHError(hello, helloErr)
		a.logger.Warn("repair_ssh: ssh handshake failed", "host", host, "err", helloErr)
		return res, nil
	}

	v := appVersion
	if appBuild != "" && appBuild != "dev" {
		v = appVersion + "+" + appBuild
	}
	files, err := sticksetup.StickFileSet(agentbin.Bytes(), v)
	if err != nil {
		res.Message = "could not assemble install files: " + err.Error()
		return res, err
	}

	// Stage the files in the box's RAM (tmpfs) first, not straight onto NAND.
	// Two field-driven reasons: (1) the ~11 MB agent binary travels over SSH on
	// what is often weak Wi-Fi, and a stall or truncation mid-stream then only
	// dirties a throwaway RAM copy we byte-verify before trusting it, instead of
	// leaving a half-written file on flash; (2) install.sh's NAND-cache commit
	// (the Layer-1 seed) is then a fast LOCAL cp from RAM, not a flash write held
	// open at network pace. install.sh runs immediately after, so the staged
	// files never need to survive a reboot, which is why tmpfs is safe.
	//
	// Both targets are tight on these boxes (a small tmpfs vs a nearly-full NAND),
	// so before committing the upload we df each candidate and prefer one that
	// actually has room, instead of filling a too-small filesystem and only
	// learning that from the post-write byte-verify. NAND is ubifs, which
	// transparently compresses, so its requirement is discounted (see
	// stageRepairFilesBestEffort). A candidate df says is too small is still tried
	// last rather than skipped (fallback-first).
	res.Step = "repair-stage"
	var rawNeed int64
	for _, data := range files {
		rawNeed += int64(len(data))
	}
	stage, serr := a.stageRepairFilesBestEffort(host, rawNeed, files)
	if serr != nil {
		res.Code = "repair-upload-failed"
		res.Message = "could not copy STR to the speaker: " + serr.Error()
		a.logger.Warn("repair_ssh: staging failed on all targets", "host", host, "err", serr)
		return res, nil
	}

	res.Step = "repair-run"
	out, err := boxSSHOutput(host, "STR_STICK="+stage+" sh "+stage+"/install.sh install 2>&1", installRunBudget(model))
	res.Log = out
	if err != nil {
		res.Code = "repair-install-error"
		hint := classifySSHError(out, err)
		res.Message = "SSH repair install failed: " + hint
		a.logger.Warn("repair_ssh: install failed", "host", host, "model", model, "err", err,
			"hint", hint, "installOutput", truncForLog(out, 4000), "boxDiag", truncForLog(boxInstallDiag(host), 8000))
		return res, nil
	}
	if strings.Contains(out, "FEHLER") || strings.Contains(out, "ERROR") {
		res.Code = "repair-install-script-error"
		res.Message = "install.sh reported an error during SSH repair. See log."
		return res, nil
	}

	res.OK = true
	res.Step = "reboot"
	res.Message = "STR installed over SSH (USB stick bypassed). The speaker will reboot and join your Wi-Fi."
	a.logger.Info("repair_ssh: install ran", "host", host, "outBytes", len(out))
	_ = boxReboot(host)
	return res, nil
}

// stageRepairFiles uploads the embedded install file set into stageDir on the
// box over SSH and verifies every file arrived byte-exact, so a truncated
// weak-Wi-Fi transfer is caught before install.sh trusts it. The agent binary
// is the only large file and gets one upload retry, since a transient stall
// mid-stream is exactly what staging-then-verify is meant to absorb. Returns a
// human-readable error naming the file and where it failed; nil on success.
func (a *App) stageRepairFiles(host, stageDir string, files map[string][]byte) error {
	if out, err := boxSSHOutput(host, "rm -rf "+stageDir+" && mkdir -p "+stageDir+"/bin", 20*time.Second); err != nil {
		return fmt.Errorf("could not create staging dir %s: %s", stageDir, classifySSHError(out, err))
	}
	for name, data := range files {
		if i := strings.LastIndex(name, "/"); i >= 0 {
			_, _ = boxSSHOutput(host, "mkdir -p "+stageDir+"/"+name[:i], 10*time.Second)
		}
		dst := stageDir + "/" + name
		attempts := 1
		if len(data) > 1<<20 { // only the agent binary; everything else is a few KB
			attempts = 2
		}
		var lastErr error
		for try := 0; try < attempts; try++ {
			if out, err := boxSSHUploadStdin(host, "cat > "+dst, bytes.NewReader(data), 120*time.Second); err != nil {
				lastErr = fmt.Errorf("upload %s failed: %s", name, classifySSHError(out, err))
				continue
			}
			// Byte-count check catches a truncated transfer, the common
			// weak-Wi-Fi failure mode, without a full checksum the BusyBox on
			// the box may not carry. Parse the number out of the SSH output
			// instead of string-matching the whole blob: OpenSSH folds its
			// "Warning: Permanently added ... to the list of known hosts."
			// banner (local stderr, forced every connect by
			// UserKnownHostsFile=/dev/null) in ahead of wc's number via
			// CombinedOutput, and an exact-match compare then read a
			// byte-perfect upload as "truncated" and falsely failed the whole
			// SSH NAND-install fallback for ST30 stick-power users (13.06).
			// LogLevel=ERROR now suppresses that banner at the source,
			// but parsing the count keeps the check correct against any
			// residual stderr noise regardless.
			got, _ := boxSSHOutput(host, "wc -c < "+dst+" 2>/dev/null", 10*time.Second)
			if n, ok := lastIntField(got); ok && n == int64(len(data)) {
				lastErr = nil
				break
			}
			recv := "no byte count"
			if n, ok := lastIntField(got); ok {
				recv = strconv.FormatInt(n, 10)
			}
			lastErr = fmt.Errorf("upload %s truncated: speaker received %s of %d bytes", name, recv, len(data))
		}
		if lastErr != nil {
			return lastErr
		}
	}
	_, _ = boxSSHOutput(host, "chmod +x "+stageDir+"/install.sh "+stageDir+"/run.sh "+stageDir+"/streborn-armv7l 2>/dev/null", 15*time.Second)
	return nil
}

// lastIntField returns the LAST whitespace-separated token in out that parses as
// a non-negative integer. The box prints exactly one integer for the commands
// that use this (`wc -c` for the upload byte-count verify, `grep -c` for the CPU
// core count), so the last such token is that number even when local SSH noise
// is folded in ahead of it by CombinedOutput: with UserKnownHostsFile=/dev/null
// OpenSSH prints "Warning: Permanently added '<host>' (<key>) to the list of
// known hosts." on stderr at connect time, which lands before the remote stdout.
// Scanning for the last integer (rather than string-matching the whole blob or
// trusting a fixed field index) keeps these checks correct against that banner
// and any other pre-stdout noise; the banner's own tokens never parse as
// integers (the host is quoted and dotted), so they are ignored regardless of
// ordering. Returns (0, false) when no integer token is present, e.g. the remote
// command produced nothing (a missing file, so the redirect failed), which
// callers treat as "unreadable" / a failed transfer.
func lastIntField(out string) (int64, bool) {
	var n int64
	var ok bool
	for _, f := range strings.Fields(out) {
		if v, err := strconv.ParseInt(f, 10, 64); err == nil && v >= 0 {
			n = v
			ok = true
		}
	}
	return n, ok
}

// repairStageCandidates are the on-box staging targets for the SSH repair, in
// preference order: RAM (tmpfs) first, NAND (ubifs) as the fallback. compress is
// the fraction of the raw upload size we expect to actually need on that
// filesystem. tmpfs is 1.0 (RAM stores data verbatim). /mnt/nv is discounted
// because it is ubifs, which transparently compresses on write, and on top of
// that ubifs df deliberately UNDER-reports free space (its budgeting assumes
// worst-case incompressible data). A stripped ARM ELF plus a few text scripts
// compress comfortably, so 0.6 is a safe discount that stops us needlessly
// rejecting NAND when it really has room. The post-write byte-verify in
// stageRepairFiles is the backstop if the estimate is too optimistic.
var repairStageCandidates = []struct {
	base     string
	compress float64
}{
	{"/tmp", 1.0},
	{"/mnt/nv", 0.6},
}

// stageRepairFilesBestEffort stages files into the first usable on-box directory,
// preferring RAM over NAND. For each candidate it checks free space with df
// (rawNeed scaled by the candidate's compression factor, plus a small headroom):
// candidates with enough room are tried first, the rest last (fallback-first, so
// an unreadable df never blocks an attempt). The first directory that stages and
// byte-verifies wins and is returned. Errors only if every candidate failed.
func (a *App) stageRepairFilesBestEffort(host string, rawNeed int64, files map[string][]byte) (string, error) {
	var fits, tooSmall []string
	for _, c := range repairStageCandidates {
		need := int64(float64(rawNeed)*c.compress) + (2 << 20) // ~2 MB headroom for the dir + fs overhead
		free := freeBytesAtPath(host, c.base)
		a.logger.Info("repair_ssh: staging candidate space",
			"host", host, "dir", c.base, "freeBytes", free, "needBytes", need, "rawNeed", rawNeed)
		if free >= need {
			fits = append(fits, c.base)
		} else {
			tooSmall = append(tooSmall, c.base)
		}
	}
	var lastErr error
	for _, base := range append(fits, tooSmall...) {
		stage := base + "/streborn-install"
		if err := a.stageRepairFiles(host, stage, files); err != nil {
			lastErr = err
			a.logger.Warn("repair_ssh: staging attempt failed", "host", host, "dir", stage, "err", err)
			continue
		}
		a.logger.Info("repair_ssh: staged install files", "host", host, "dir", stage)
		return stage, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no staging directory available on the speaker")
	}
	return "", lastErr
}

// freeBytesAtPath returns the free space in bytes on the filesystem backing path
// on the box via BusyBox `df -k`, or 0 if it cannot be read or parsed (treated as
// "unknown, do not assume room"). Parsing lives in parseDfAvailBytes so it is
// unit-testable without a box.
func freeBytesAtPath(host, path string) int64 {
	out, err := boxSSHOutput(host, "df -k "+path+" 2>/dev/null", 8*time.Second)
	if err != nil {
		return 0
	}
	return parseDfAvailBytes(out)
}

// parseDfAvailBytes pulls the Available column (returned in bytes) out of
// `df -k` output. It reads Available as the 3rd field from the end of the last
// line, which is correct both for the normal one-line row
// ("fs blocks used avail use% mount") and for BusyBox's wrapped form, where a
// long device name pushes the numbers onto the next line
// ("blocks used avail use% mount"): in both, Available is followed by exactly
// Use% and the mountpoint. Returns 0 on any parse failure.
func parseDfAvailBytes(out string) int64 {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0 // header only, or nothing
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 5 {
		return 0
	}
	availKB, err := strconv.ParseInt(fields[len(fields)-3], 10, 64)
	if err != nil || availKB < 0 {
		return 0
	}
	return availKB * 1024
}

// slowBootModel reports whether a box model boots slowly enough to need the
// longer agent-up budget. Series-I / BCO (Portable) is slow, and an empty model
// (discovery had no /info yet, i.e. a freshly bootstrapped box) is treated as
// slow too so a real BCO box is never cut off early (#114, fallback-first).
func slowBootModel(model string) bool {
	m := strings.ToLower(model)
	// "" = discovery had no /info yet (a freshly bootstrapped box). "portable"
	// and "20" are the Series-I/BCO models whose externally reachable :17008
	// REDIRECT comes up late and flaky after the install reboot. An ST20 (the
	// spotty/scm variant is BCO) therefore needs the longer budget and the
	// "still starting, can take a few minutes" messaging too: on #155 an agent
	// that was actually up but not yet reachable was reported as a failed install
	// and the user re-ran the installer. Being generous only costs wait time on a
	// genuine failure; a successful probe returns immediately.
	return m == "" || strings.Contains(m, "portable") ||
		m == "20" || strings.Contains(m, " 20") || strings.Contains(m, "st20") || strings.Contains(m, "_20")
}

// agentWaitBudget is how long to wait for the STR agent to come up on :8888
// after the install reboot, by model. Slow models get 240 s, the rest 180 s.
func agentWaitBudget(model string) time.Duration {
	if slowBootModel(model) {
		return 240 * time.Second
	}
	return 180 * time.Second
}

// installRunBudget is how long install.sh may run over SSH before we give up.
// install.sh does heavy on-box work (copy the agent to NAND, rewrite
// wpa_supplicant, regenerate TLS); on the bigger/slower models, or over a weak
// Wi-Fi link, that routinely takes longer than a minute. The original flat
// 60 s was the main first-install timeout on ST30 (#119), so even the
// "fast" Series-II models now get a generous 180 s here and slow-boot models
// (Series-I / BCO / unknown) get 240 s. This is independent of slowBootModel's
// boot-time messaging: ST30 is not flagged slow-boot but still needs the
// headroom on install.sh itself.
func installRunBudget(model string) time.Duration {
	if slowBootModel(model) {
		return 240 * time.Second
	}
	return 180 * time.Second
}

// InstallProgress is a live, mid-install status pushed to the UI over the
// Wails "install:progress" event. It currently carries the load-settle phase
// so the user sees "speaker reachable but still finishing its boot routine"
// instead of a frozen "installing" line while STR waits for the box's CPU to
// calm down.
type InstallProgress struct {
	Phase       string  `json:"phase"`       // "settle"
	Load        float64 `json:"load"`        // box 1-min load average
	Threshold   float64 `json:"threshold"`   // calm threshold (per-core scaled)
	Busy        bool    `json:"busy"`        // true while still waiting to settle
	RemainingMs int     `json:"remainingMs"` // until the safety cap fires
}

func (a *App) emitInstallProgress(p InstallProgress) {
	if a.ctx != nil {
		wailsrt.EventsEmit(a.ctx, "install:progress", p)
	}
}

// waitForBoxLoad holds off launching the heavy install.sh until the box's CPU
// has actually calmed down, rather than waiting a blind fixed time. A box that
// just powered on with the stick is pegged (NetManager, scmmond and the Bose
// services all start at once), and kicking install.sh into that contention is
// what made the run overshoot the timeout on ST30 (#119).
//
// The real signal is the 1-min load average: we proceed only once it has
// stayed under a per-core threshold for several consecutive samples (a
// sustained-calm streak, ~ calmStreak*sample seconds), so a single transient
// dip does not fool us. The time cap is a pure safety fallback (fallback-first):
// a box that never settles still gets its install attempt rather than hanging,
// and the generous install.sh timeout then backstops it. If the load cannot be
// read at all, we do not block.
func (a *App) waitForBoxLoad(host, model string) {
	const (
		sample     = 2 * time.Second
		calmStreak = 3 // ~6 s of sustained calm before we commit
	)
	threshold := 1.5 * float64(boxCoreCount(host))
	// Safety cap: if the load never drops (e.g. a box pegged permanently by
	// faulty custom firmware), proceed with the install anyway after a long
	// enough wait rather than hanging here. Generous so a genuinely slow boot
	// is never cut off early.
	cap := 120 * time.Second
	if slowBootModel(model) {
		cap = 180 * time.Second
	}
	deadline := time.Now().Add(cap)
	calm := 0
	for {
		load, ok := boxLoad1(host)
		if !ok {
			// Can't read load (unusual SSH/proc state). Don't block; the
			// install.sh timeout still backstops a genuinely wedged box.
			a.emitInstallProgress(InstallProgress{Phase: "settle", Busy: false})
			return
		}
		if load <= threshold {
			calm++
		} else {
			calm = 0
		}
		settled := calm >= calmStreak
		capped := time.Now().After(deadline)
		remaining := int(time.Until(deadline) / time.Millisecond)
		if remaining < 0 {
			remaining = 0
		}
		// On the iteration we decide to proceed, report busy=false so the UI
		// flips back to the "installing" line.
		a.emitInstallProgress(InstallProgress{
			Phase:       "settle",
			Load:        load,
			Threshold:   threshold,
			Busy:        !settled && !capped,
			RemainingMs: remaining,
		})
		if settled {
			a.logger.Info("install_str: box load settled, proceeding", "host", host, "load", load, "threshold", threshold)
			return
		}
		if capped {
			a.logger.Info("install_str: box load did not settle before cap, proceeding anyway", "host", host, "load", load, "threshold", threshold)
			return
		}
		time.Sleep(sample)
	}
}

// boxInstallDiag gathers as much box state as possible over the SSH session that
// truncForLog caps a possibly large multi-line blob so it stays usable as a
// single slog attribute and keeps str.log from ballooning. The install-failure
// path routes the box diagnostics through here into str.log, because str.log IS
// part of the app's diagnostic export while InstallResult.Log is NOT.
func truncForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n...[truncated %d bytes]", len(s)-max)
}

// is already open when an install fails: kernel, firmware, partitions, block
// devices, mounts, free space, stick contents, the remote_services marker, ssh
// procs, and the USB/storage dmesg tail. It runs on ANY install failure where SSH
// is up, so a remote user's saved log carries the evidence (firmware/kernel vs
// port vs media) in one shot, without more web research or another round trip.
func boxInstallDiag(host string) string {
	cmd := strings.Join([]string{
		"echo '# uname'; uname -a 2>&1",
		"echo '# /proc/version'; cat /proc/version 2>&1",
		"echo '# version files'; cat /etc/os-release /etc/version /mnt/nv/*ersion* 2>/dev/null | head -20",
		"echo '# uptime/loadavg'; uptime 2>&1; cat /proc/loadavg 2>&1",
		"echo '# meminfo'; head -3 /proc/meminfo 2>&1",
		"echo '# partitions'; cat /proc/partitions 2>&1",
		"echo '# block devices'; ls -la /dev/sd* /dev/mmcblk* 2>&1",
		"echo '# mounts'; mount 2>&1",
		"echo '# df'; df -h 2>&1",
		"echo '# stick contents'; ls -la /media/sda1 /mnt/usb /run/media/* 2>&1 | head -40",
		// install.sh head + line-ending/exec evidence: exit 126 is usually a
		// CRLF script the box's BusyBox refuses, a non-executable/no-exec mount,
		// or a media read failure mid-script. `od` on the first line reveals
		// trailing 0d (CR); `file`/`head` show whether the script is readable.
		"echo '# install.sh head'; head -5 /media/sda1/install.sh 2>&1",
		"echo '# install.sh line endings (look for 0d)'; head -1 /media/sda1/install.sh 2>&1 | od -c 2>&1 | head -4",
		"echo '# remote_services marker'; ls -la /media/sda1/remote_services /mnt/nv/remote_services 2>&1",
		"echo '# ssh procs'; ps 2>&1 | grep -i ssh | grep -v grep",
		"echo '# dmesg usb/storage'; dmesg 2>/dev/null | grep -iE 'usb|sd[a-z]|vfat|fat|mmc|scsi|i/o error|reset|error' | tail -60",
	}, "; ")
	out, err := boxSSHOutput(host, cmd, 20*time.Second)
	if err != nil {
		return "diagnostics unavailable: " + err.Error()
	}
	return out
}

// boxCoreCount reads the CPU count from /proc/cpuinfo so the load threshold can
// be scaled per core. Defaults to 1 on any read error (the SoundTouch SoCs are
// single- or dual-core, so 1 is the safe conservative floor). grep -c prints a
// single integer, parsed with the banner-tolerant lastIntField rather than
// Atoi(TrimSpace(out)): the latter chokes on the multi-line blob when the
// known_hosts warning is folded in by CombinedOutput, which silently dropped
// every box to the 1-core floor (an over-strict settle threshold) before
// LogLevel=ERROR suppressed the banner.
func boxCoreCount(host string) int {
	out, err := boxSSHOutput(host, "grep -c ^processor /proc/cpuinfo", 8*time.Second)
	if err == nil {
		if n, ok := lastIntField(out); ok && n > 0 {
			return int(n)
		}
	}
	return 1
}

// boxLoad1 returns the box's 1-min load average from /proc/loadavg, and false
// if it could not be read or parsed.
func boxLoad1(host string) (float64, bool) {
	out, err := boxSSHOutput(host, "cat /proc/loadavg", 8*time.Second)
	if err != nil {
		return 0, false
	}
	return parseLoadAvg(out)
}

// parseLoadAvg extracts the 1-minute load average from `cat /proc/loadavg`
// output. /proc/loadavg is a single stdout line whose first field is the 1-min
// average ("0.42 0.31 0.20 1/80 1234"), so this reads the first field of the
// LAST non-empty line: any local SSH noise CombinedOutput folds in (the
// "Warning: Permanently added ... to the list of known hosts." banner forced by
// UserKnownHostsFile=/dev/null) lands on an earlier line and is skipped.
// Returns (0, false) when the last non-empty line's first field is not a
// non-negative float, e.g. /proc/loadavg was unreadable so only the banner
// remains. The banner-tolerance matters: a (0, false) here makes waitForBoxLoad
// skip the load-settle gate entirely (the #119 ST30 install-timeout mitigation),
// so before LogLevel=ERROR the banner silently disabled that gate on every box.
// LogLevel=ERROR suppresses the banner at the source now; parsing defensively
// keeps the gate working if it ever reappears, matching lastIntField.
func parseLoadAvg(out string) (float64, bool) {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		v, err := strconv.ParseFloat(strings.Fields(line)[0], 64)
		if err != nil || v < 0 {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

// stickCopyFailureMarkers are the exact run.sh log lines that prove the agent
// never started because the agent binary could not be copied from the USB stick
// into the NAND cache: the stick read failed mid-copy and, on a first install,
// there was no prior NAND cache to fall back to. Either line means a plain
// reboot will not help (it hits the same unreadable stick), but staging STR's
// embedded binary onto NAND over SSH will. Kept in lock-step with the strings
// usb-stick/run.sh logs (sync_stick_to_nand_always and the BIN resolution).
var stickCopyFailureMarkers = []string{
	"stick -> NAND cp failed",
	"neither NAND cache nor stick binary available",
}

// usbPowerHint is the human sentence for the stick-usb-power code: the install
// I/O error was a USB power dropout, not a faulty stick. Kept here (not in
// classifySSHError) because that classifier only sees the install.sh output,
// while the VBUS verdict comes from the kernel dmesg in the box diagnostics. No
// dashes per the user-facing text convention.
const usbPowerHint = "the speaker's USB port could not keep the stick powered (a power error, not a faulty stick: the same stick installs fine on other models). Use a small, low-power USB 2.0 stick (4 to 16 GB), or connect the stick through a powered (self-powered) USB hub, then try again."

// usbPowerFailureMarkers are the kernel dmesg signatures that prove an install
// I/O error was a USB power / enumeration dropout rather than an unreadable or
// faulty stick: the speaker's USB port could not hold VBUS up under read load,
// so the stick re-enumerates and disconnects mid-install (error -110 = USB
// ETIMEDOUT). Seen on the ST30 with mid-draw sticks that install fine on the
// ST10/ST20 with the SAME stick, so the generic "stick likely faulty" message
// is wrong for this case. Matched lower-cased.
var usbPowerFailureMarkers = []string{
	"vbus_error",
	"error -110",
	"device not accepting address",
	"device descriptor read",
}

// detectUSBPowerFailure reports whether the box diagnostics (its dmesg tail)
// carry the USB power / enumeration dropout signature. Only consulted once an
// I/O error is already established, so a match means "power, not media". Pure
// lower-cased substring match so it is unit-testable without a box.
func detectUSBPowerFailure(diag string) bool {
	low := strings.ToLower(diag)
	for _, m := range usbPowerFailureMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// detectStickCopyFailure reports whether a box log tail shows the stick->NAND
// binary copy failed, leaving the agent with nothing to run. Pure string match
// so it is unit-testable without a box.
func detectStickCopyFailure(logText string) bool {
	for _, m := range stickCopyFailureMarkers {
		if strings.Contains(logText, m) {
			return true
		}
	}
	return false
}

// agentLogShowsStickCopyFailure pulls the box-side run.sh logs over SSH and
// reports whether they carry the stick->NAND copy-failure markers. It reads the
// current boot's tmpfs log AND the previous boot's NAND-persisted copy, because
// the install reboot may have rotated the failing boot's log into previous.log.
// Best-effort: an unreadable log returns false so we fall back to the generic
// path rather than blocking.
func (a *App) agentLogShowsStickCopyFailure(host string) bool {
	out, _ := boxSSHOutput(host, "cat /tmp/streborn-agent.log /mnt/nv/streborn/previous.log 2>/dev/null", 12*time.Second)
	return detectStickCopyFailure(out)
}

// waitForAgent polls until the STR agent actually answers as STR, or the
// model-aware budget elapses. It uses probeSTR, which verifies the agent on
// :8888 AND on the :17008 REDIRECT, so a BCO box (ST20 scm/spotty, Portable)
// whose :8888 is loopback-only and is reachable only via :17008 is recognised
// as up. The old version polled bare TCP on :8888 only, so it declared a
// genuinely running agent "not up" and the install reported failure even though
// the box had come up fine (#114, Baehr ST20). It also verifies the STR agent
// rather than just an open port, so the Bose SoftwareUpdate service that also
// listens on :17008 is not mistaken for the agent.
func (a *App) waitForAgent(host, model string) error {
	budget := agentWaitBudget(model)
	sleep := 2 * time.Second
	if slowBootModel(model) {
		sleep = 3 * time.Second
	}
	deadline := time.Now().Add(budget)
	i := 0
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(a.appCtx(), 5*time.Second)
		_, ok := probeSTR(ctx, host)
		cancel()
		if ok {
			return nil
		}
		// The HTTP probe failed. On SoundTouch 10 (rhino) the desktop cannot
		// reach the agent's HTTP API at all, even when it is running fine: the
		// Bose firewall blocks the agent ports (the WORKING ST10 shows the same
		// reachable8888=False). A pure HTTP probe therefore reports a healthy box
		// as "agent not up" and the install wrongly fails. So every few polls,
		// confirm over SSH whether the agent PROCESS is actually up; that is the
		// authoritative success signal where the API is unreachable from the LAN.
		if i%4 == 3 && a.agentRunningViaSSH(host) {
			a.logger.Info("install_str: agent process is running on the box; treating as up despite the HTTP API being unreachable from the desktop (e.g. ST10 firewall)", "host", host)
			return nil
		}
		i++
		time.Sleep(sleep)
	}
	// Final SSH check before declaring failure, so a running agent on a box whose
	// HTTP API never became desktop-reachable is still recognised as installed.
	if a.agentRunningViaSSH(host) {
		a.logger.Info("install_str: agent process running on the box at the deadline; HTTP API unreachable from the desktop, treating as up", "host", host)
		return nil
	}
	return fmt.Errorf("STR agent on %s not reachable within %s", host, budget)
}

// agentRunningViaSSH reports whether the STR agent process is alive on the box,
// checked over SSH. This is the ground-truth "is the agent up" signal for boxes
// whose HTTP API is not reachable from the desktop (ST10 rhino: the Bose
// firewall blocks the agent ports even when the agent runs). Best-effort: SSH
// down or any error reads as "not running" so it never fakes success.
func (a *App) agentRunningViaSSH(host string) bool {
	if !tcpReachable(host, 22, 3*time.Second) {
		return false
	}
	// [s] keeps the grep from matching its own process line. -c counts matches.
	out, err := boxSSHOutput(host, "ps 2>/dev/null | grep -c '[s]treborn-armv7l'", 8*time.Second)
	if err != nil {
		return false
	}
	n := strings.TrimSpace(out)
	return n != "" && n != "0"
}
