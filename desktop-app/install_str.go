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
		switch {
		case strings.Contains(err.Error(), "timeout"):
			res.Code = "install-timeout"
		case strings.Contains(lowOut, "input/output error"), strings.Contains(lowOut, "i/o error"):
			// Media-level read failure of install.sh, see classifySSHError.
			res.Code = "stick-io-error"
		}
		hint := classifySSHError(out, err)
		res.Message = "install.sh execution failed: " + hint
		diag := boxInstallDiag(host)
		// The rich diagnostics must also reach str.log, not only res.Log:
		// a remote user's str.log is often all we get, and without this it
		// showed just the one-line hint (no kernel/stick/dmesg evidence).
		a.logger.Warn("install_str: install.sh execution failed",
			"host", host, "model", model, "err", err, "code", res.Code,
			"hint", hint, "installOutput", truncForLog(out, 4000), "boxDiag", truncForLog(diag, 8000))
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
		// If SSH is still reachable the install window is open, so reboot once
		// and retry before giving up. This self-heals the common "shim lost the
		// boot race" case with no user action. Guarded on SSH still being up so
		// we never double-reboot a box that already left the install window.
		if tcpReachable(host, 22, 4*time.Second) {
			a.logger.Info("install_str: agent not up, SSH still open, rebooting once and retrying", "host", host)
			res.Step = "reboot-and-retry"
			_ = boxReboot(host)
			if a.waitForAgent(host, model) == nil {
				res.Step = "done"
				res.OK = true
				res.Message = "STR agent is up on port 8888 (after an automatic retry)."
				return res, nil
			}
		}
		res.Step = "wait-agent"
		res.Code = "agent-not-up"
		logHint := "To get help, save the diagnostic logs (the Save logs button in Speaker Settings, or the link at the bottom of the app window) and attach them to the GitHub issue or email them to str@sichtbar-app.de."
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
	const stage = "/mnt/nv/streborn-install"

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

	res.Step = "repair-stage"
	if out, err := boxSSHOutput(host, "rm -rf "+stage+" && mkdir -p "+stage+"/bin", 20*time.Second); err != nil {
		res.Code = "repair-stage-failed"
		res.Message = "could not create NAND staging dir: " + classifySSHError(out, err)
		a.logger.Warn("repair_ssh: mkdir failed", "host", host, "err", err, "out", truncForLog(out, 1000))
		return res, nil
	}

	// Push each file via SSH stdin (same upload path the OTA uses). The agent
	// binary (~10 MB) is the large one; everything else is a few KB.
	for name, data := range files {
		if i := strings.LastIndex(name, "/"); i >= 0 {
			_, _ = boxSSHOutput(host, "mkdir -p "+stage+"/"+name[:i], 10*time.Second)
		}
		if out, err := boxSSHUploadStdin(host, "cat > "+stage+"/"+name, bytes.NewReader(data), 120*time.Second); err != nil {
			res.Code = "repair-upload-failed"
			res.Message = "could not upload " + name + " to NAND: " + classifySSHError(out, err)
			a.logger.Warn("repair_ssh: upload failed", "host", host, "file", name, "err", err)
			return res, nil
		}
	}
	_, _ = boxSSHOutput(host, "chmod +x "+stage+"/install.sh "+stage+"/run.sh "+stage+"/streborn-armv7l 2>/dev/null", 15*time.Second)

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

// slowBootModel reports whether a box model boots slowly enough to need the
// longer agent-up budget. Series-I / BCO (Portable) is slow, and an empty model
// (discovery had no /info yet, i.e. a freshly bootstrapped box) is treated as
// slow too so a real BCO box is never cut off early (#114, fallback-first).
func slowBootModel(model string) bool {
	m := strings.ToLower(model)
	return m == "" || strings.Contains(m, "portable")
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
			Busy:        !(settled || capped),
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
// single- or dual-core, so 1 is the safe conservative floor).
func boxCoreCount(host string) int {
	out, err := boxSSHOutput(host, "grep -c ^processor /proc/cpuinfo", 8*time.Second)
	if err == nil {
		if n, e := strconv.Atoi(strings.TrimSpace(out)); e == nil && n > 0 {
			return n
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
	fields := strings.Fields(out)
	if len(fields) < 1 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
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
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(a.appCtx(), 5*time.Second)
		_, ok := probeSTR(ctx, host)
		cancel()
		if ok {
			return nil
		}
		time.Sleep(sleep)
	}
	return fmt.Errorf("STR agent on %s not reachable within %s", host, budget)
}
