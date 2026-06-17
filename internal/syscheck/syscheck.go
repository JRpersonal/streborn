//go:build linux

// Package syscheck logs a one-shot "STR system check" at agent startup: the
// box's kernel, CPU, memory, NAND headroom and whether the optional
// go-librespot Spotify sidecar is actually deployed. It exists so every
// diagnostic bundle answers, at a glance, whether the prerequisites for a clean
// STR run are met on THIS box, instead of having to infer them.
//
// Concretely it settles questions like #45/#105 "why is Spotify unavailable on
// this ST20": go-librespot reaches the box only via the USB stick's boot-time
// sync to NAND, never via the agent OTA, so a box installed with an old stick or
// only ever OTA-updated has no binary at all. A bare "spotify not logged in"
// gives no hint of that; this block shows go_librespot=MISSING outright. It also
// records the kernel/CPU/NEON so a future genuine arch mismatch (a model on a
// different SoC) is visible rather than guessed.
package syscheck

import (
	"bufio"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"syscall"
)

// Run emits the system-check block at INFO. glrPath is the expected go-librespot
// binary path (so the check reports whether the Spotify sidecar is deployed).
// Best-effort throughout: a probe that cannot read its source is reported as
// "unknown" rather than failing agent startup. Safe to call once at boot.
func Run(logger *slog.Logger, glrPath string) {
	kernel := firstLine("/proc/version")
	model, features := cpuInfo()
	neon := strings.Contains(" "+features+" ", " neon ")
	glr := goLibrespotState(glrPath)

	logger.Info("STR system check",
		"kernel", kernel,
		"arch", runtime.GOARCH,
		"cpu", model,
		"neon", neon,
		"cpu_features", features,
		"mem_total", memTotal(),
		"nand_free", nandFree("/mnt/nv"),
		"go_librespot", glr,
		"go_librespot_path", glrPath,
	)
}

// goLibrespotState reports the Spotify sidecar's deployment state in one word
// the log reader can act on: "MISSING" (not delivered to this box, the #45 cause),
// or "present:<bytes>" when the binary is in place. It deliberately does NOT exec
// the binary: the Spotify manager supervises and logs the real process, and a
// second short-lived instance would collide on the zeroconf advert.
func goLibrespotState(path string) string {
	if path == "" {
		return "unconfigured"
	}
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "MISSING"
		}
		return "unknown(" + err.Error() + ")"
	}
	if fi.IsDir() {
		return "MISSING(is-dir)"
	}
	return "present:" + itoa(fi.Size()) + "B"
}

// cpuInfo returns the first "model name"/"Processor" value and the "Features"
// line from /proc/cpuinfo (so NEON/VFP availability is on record).
func cpuInfo() (model, features string) {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := splitColon(line)
		if !ok {
			continue
		}
		switch k {
		case "model name", "Processor":
			if model == "" {
				model = v
			}
		case "Features":
			if features == "" {
				features = v
			}
		}
	}
	return model, features
}

// memTotal returns the MemTotal line value from /proc/meminfo (e.g. "...kB"), or
// "" if unreadable.
func memTotal() string {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := splitColon(sc.Text()); ok && k == "MemTotal" {
			return v
		}
	}
	return ""
}

// nandFree returns free bytes on the NAND persistent volume, "" on error. NAND
// pressure is a real STR failure mode (preset/credential writes fail when it
// fills), so it belongs in the prerequisite snapshot.
func nandFree(path string) string {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return ""
	}
	return itoa(int64(st.Bavail)*int64(st.Bsize)) + "B"
}

func firstLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := string(b)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// splitColon splits "key   : value" lines (the /proc/*info layout) on the first
// colon, trimming surrounding whitespace.
func splitColon(line string) (key, value string, ok bool) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
