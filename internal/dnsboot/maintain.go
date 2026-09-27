package dnsboot

// Keeping the bootstrap resolver from outliving its usefulness.
//
// EnsureResolver runs at agent start and writes a resolv.conf when the speaker
// has none. On a boot where the agent starts before the interface is up there is
// no default gateway yet, so the file it writes contains ONLY the public
// fallbacks, and udhcpc appends the household's real resolver to that same file
// afterwards. The order it lands in is the order it stays in:
//
//	# written by STR (dnsboot): the box had no usable nameserver.
//	nameserver 1.1.1.1
//	nameserver 8.8.8.8
//	search lan # wlan0
//	nameserver 192.168.x.x # wlan0
//
// Go's resolver walks that list in order with a five second timeout each, so
// every lookup on such a speaker pays ten seconds before the resolver that
// actually answers is reached. The firmware's audio path gives a station five to
// ten seconds, so the speaker gives up before its own agent has finished looking
// the station up, and NOTHING plays. Measured in a two-speaker household on
// 2026-09-27: one ST20 with this file, ~50 stream attempts, not one upstream
// response, every session ending "bose disconnected" at exactly 5 s or 10 s. The
// ST10 beside it had no STR header in its resolv.conf and played the same
// station fine. Its agent had simply started after wlan0 came up.
//
// So this is a boot-order race in STR, not a difference between the speakers,
// and the same speaker would be fine on the next boot. That is what makes it
// worth a watcher rather than a better guess at startup: whatever the order was,
// the moment a real resolver appears the bootstrap entries must stop being asked
// first.

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"
)

// maintainInterval is how often the file is re-read. It is a small read of a
// small file, and it writes only when the order is actually wrong, so a healthy
// speaker costs nothing after the first pass. The NAND is not touched at all:
// the staged file lives on the writable overlay the bind mount points at.
const maintainInterval = 30 * time.Second

// isBootstrapServer reports whether s is one of the public fallbacks this
// package installs, as opposed to a resolver the household actually runs.
func isBootstrapServer(s string) bool {
	return slices.Contains(publicFallbacks, strings.TrimSpace(s))
}

// MaintainResolver demotes the bootstrap resolvers behind the household's own
// as soon as one appears. It returns when ctx ends.
//
// Deliberately a demotion and not a removal: a household resolver that stops
// answering later leaves the speaker with a working fallback behind it, which is
// the case EnsureResolver exists for. What must not survive is the fallback
// being asked FIRST.
func MaintainResolver(ctx context.Context, logger *slog.Logger) {
	t := time.NewTicker(maintainInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// Once the order is right it stays right unless something rewrites the
		// file, and the next tick notices that too.
		demoteBootstrapResolvers(logger)
	}
}

// demoteBootstrapResolvers rewrites the staged resolv.conf so the household's
// resolvers come first. It reports whether it wrote anything.
func demoteBootstrapResolvers(logger *slog.Logger) bool {
	body, err := os.ReadFile(ourResolv)
	if err != nil {
		// No staged file means this package never acted on this boot, so there
		// is nothing of ours to demote.
		return false
	}
	text := string(body)
	if !strings.Contains(text, "written by STR (dnsboot)") {
		return false
	}

	var head []string   // comments, search lines, anything that is not a nameserver
	var ours []string   // the public fallbacks we installed
	var theirs []string // everything else that answers name lookups
	var sawTheirs bool
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		rest, ok := strings.CutPrefix(trimmed, "nameserver")
		if !ok {
			head = append(head, line)
			continue
		}
		// A nameserver line can carry a trailing comment (udhcpc writes
		// "nameserver 192.168.1.1 # wlan0"), so the address is the first field.
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		if isBootstrapServer(fields[0]) {
			ours = append(ours, line)
			continue
		}
		theirs = append(theirs, line)
		sawTheirs = true
	}
	// Nothing of the household's yet: the bootstrap is still the only thing
	// this speaker has, and it stays where it is.
	if !sawTheirs || len(ours) == 0 {
		return false
	}
	// Already in the right order: the first nameserver in the file is theirs.
	if firstNameserverIsTheirs(text) {
		return false
	}

	var b strings.Builder
	b.WriteString("# written by STR (dnsboot): the box had no usable nameserver.\n")
	b.WriteString("# The household's own resolver appeared later, so it was moved in front of\n")
	b.WriteString("# the fallbacks. Asking a public resolver first costs five seconds per\n")
	b.WriteString("# lookup here, which is longer than the firmware waits for a station.\n")
	for _, l := range head {
		if strings.Contains(l, "written by STR (dnsboot)") || strings.HasPrefix(strings.TrimSpace(l), "# ") {
			continue
		}
		b.WriteString(l + "\n")
	}
	for _, l := range theirs {
		b.WriteString(l + "\n")
	}
	for _, l := range ours {
		b.WriteString(l + "\n")
	}
	if err := os.WriteFile(ourResolv, []byte(b.String()), 0o644); err != nil {
		logger.Warn("dns bootstrap: could not reorder the resolver list", "err", err)
		return false
	}
	logger.Warn("dns bootstrap: the speaker's own resolver appeared, moving it ahead of the fallbacks so lookups stop paying a timeout first",
		"ownResolvers", len(theirs), "fallbacks", len(ours))
	return true
}

// firstNameserverIsTheirs reports whether the first nameserver line in text is
// one the household supplied rather than one of ours.
func firstNameserverIsTheirs(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "nameserver")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		return !isBootstrapServer(fields[0])
	}
	return false
}

// Describe renders the current staged file for the diagnostic bundle, so a
// speaker that is slow to resolve says why without anybody having to guess.
func Describe() string {
	body, err := os.ReadFile(ourResolv)
	if err != nil {
		return ""
	}
	return string(body)
}
