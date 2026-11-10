package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// What the installer reads off the box: the load-settle wait before install.sh, the failure diagnostics, and the stick-copy and USB-power failure markers. Split out of install_str.go.

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
		// The largest NAND entries, so a "No space left on device" report names
		// WHAT occupies /mnt/nv (a stock box with a full NAND usually carries a
		// third-party mod or leftovers no listing ever showed; live case
		// 2026-08-20: 25 of 31 MB used on a box with no STR and no way to tell).
		"echo '# nv usage (largest last)'; du -a /mnt/nv 2>/dev/null | sort -n | tail -25",
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
		// Bound the value before the int64->int conversion: it is a CPU-core count
		// parsed from box output, so anything outside a sane range is bogus and the
		// bound also keeps the conversion safe on 32-bit builds.
		if n, ok := lastIntField(out); ok && n > 0 && n <= 4096 {
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
