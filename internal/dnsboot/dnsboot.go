// Package dnsboot repairs a speaker that has no usable DNS resolver.
//
// Field case #487 (2026-07-27, Wave): the box came up with no nameserver the
// agent could see, so Go fell back to its built-in 127.0.0.1:53 / [::1]:53
// pair and EVERY name lookup failed. That single fault produced three
// unrelated-looking symptoms and locked itself in place:
//
//   - Radio: the stream proxy could not resolve any station host, so the box
//     aborted the stream within ~400 ms and displayed "Service Unavailable".
//   - Clock and pairing: every clock-sync host was a NAME, so the clock stayed
//     at the 2015 firmware epoch; autopair is gated on a plausible clock, so
//     the box never paired and displayed "SoundTouch not configured".
//   - Spotify: go-librespot crash-looped on apresolve.spotify.com.
//
// Every repair path STR owns needed the very thing that was broken, so the box
// could not heal across reboots or re-installs. This package breaks that loop:
// it detects the missing resolver and bind-mounts a working resolv.conf over
// the read-only rootfs one, the same trick run.sh already uses for /etc/hosts.
// Fixing it at the file level (rather than only inside our own Go process)
// also repairs the Bose firmware's own name lookups, so its NTP starts working
// and the clock, autopair and the display heal on their own afterwards.
package dnsboot

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	resolvPath = "/etc/resolv.conf"
	// ourResolv lives in tmpfs: the rootfs is mounted read-only on these
	// chassis, so the file we bind-mount from must be somewhere writable.
	ourResolv = "/tmp/streborn-resolv.conf"
	routePath = "/proc/net/route"
)

// publicFallbacks are appended after the router so name resolution still works
// when the gateway does not answer DNS itself.
var publicFallbacks = []string{"1.1.1.1", "8.8.8.8"}

// Nameservers returns the nameserver addresses configured in resolv.conf.
// An empty slice means the box has no usable resolver, which is the fault this
// package exists for. Read-only, safe to call from diagnostics.
func Nameservers() []string {
	return parseNameservers(readFile(resolvPath))
}

// RawResolvConf returns the current resolv.conf contents for the diagnostic
// bundle ("" when the file is missing).
func RawResolvConf() string { return readFile(resolvPath) }

// DefaultGateway returns the box's IPv4 default gateway from /proc/net/route,
// or "" when there is none. This is the best first nameserver candidate: on a
// home LAN the router almost always resolves.
func DefaultGateway() string {
	f, err := os.Open(routePath)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		if first { // header line
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || fields[1] != "00000000" {
			continue // not the default route
		}
		if ip := hexLEToIP(fields[2]); ip != "" {
			return ip
		}
	}
	return ""
}

// EnsureResolver repairs a missing resolver and reports whether it had to act.
// It is a no-op (returning false) when the box already has a nameserver, so it
// is safe to call unconditionally at agent start. Best-effort by design: a box
// where the bind mount is not permitted keeps working exactly as before, and
// the agent's own lookups are additionally covered by the resolver fallback in
// the HTTP clients.
func EnsureResolver(logger *slog.Logger) bool {
	if ns := Nameservers(); len(ns) > 0 {
		return false
	}
	gw := DefaultGateway()
	servers := make([]string, 0, len(publicFallbacks)+1)
	if gw != "" {
		servers = append(servers, gw)
	}
	servers = append(servers, publicFallbacks...)

	var b strings.Builder
	b.WriteString("# written by STR (dnsboot): the box had no usable nameserver.\n")
	for _, s := range servers {
		fmt.Fprintf(&b, "nameserver %s\n", s)
	}
	if err := os.WriteFile(ourResolv, []byte(b.String()), 0o644); err != nil {
		logger.Warn("dns bootstrap: could not stage a resolv.conf", "err", err)
		return false
	}
	// bind-mount over the read-only rootfs file, mirroring the /etc/hosts
	// handling in run.sh. A plain copy would fail on ubifs mounted ro.
	out, err := exec.Command("mount", "--bind", ourResolv, resolvPath).CombinedOutput()
	if err != nil {
		logger.Warn("dns bootstrap: bind mount failed, the agent's own lookups still use the built-in fallback",
			"err", err, "out", strings.TrimSpace(string(out)))
		return false
	}
	logger.Warn("dns bootstrap: the speaker had NO nameserver configured; installed one so radio, clock sync and pairing can work",
		"servers", strings.Join(servers, ","), "gateway", gw)
	return true
}

func parseNameservers(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "nameserver"); ok {
			if v := strings.TrimSpace(rest); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// hexLEToIP converts a /proc/net/route little-endian hex address to dotted
// quad notation ("0101A8C0" -> "192.168.1.1"). Returns "" for a zero or
// malformed value.
func hexLEToIP(h string) string {
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil || v == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", v&0xff, (v>>8)&0xff, (v>>16)&0xff, (v>>24)&0xff)
}
