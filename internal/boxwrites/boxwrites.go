// Package boxwrites is the box-write ledger: a tiny counter of every write
// STR performs against the speaker's own firmware (AddPreset/RemovePreset key
// registrations, setMargeAccount re-onboardings, autonomous UPnP pushes),
// keyed by the box's playback source at write time.
//
// It exists because "who wrote to the box at 3am, and was it asleep" is the
// question every overnight preset-loss bundle needs answered and none could:
// writes reset the firmware's deep-standby countdown and re-onboardings wipe
// the hardware-key registrations, so the ledger makes the write pattern a
// one-grep fact instead of an archaeology project. Counters only, no I/O of
// its own; the agent emits one aggregated WARN per hour at most and serves
// the running totals as a debug-state section.
package boxwrites

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	mu     sync.Mutex
	hour   = map[string]int{} // reset by SnapshotReset (the hourly WARN)
	totals = map[string]int{} // since agent start (the debug section)
	// lastAt is when a KIND was last written, ignoring the source suffix. The
	// ledger was a pure counter until a speaker started being blamed for STR's
	// own writes: an AddPreset sweep makes the firmware activate UPNP and the
	// box leaves whatever it was playing, which the native-preset watchdog then
	// counted as "this speaker cannot keep a native station" (ST20, 2026-08-22,
	// a station recorded as lasting 188 ms). Enough of those strikes latch the
	// native path off for a user whose speaker was never at fault. Knowing WHEN
	// we last wrote is what lets a watchdog tell its own footprint apart.
	lastAt = map[string]time.Time{}
)

// Note records one write of the given kind (e.g. "addpreset", "setmarge",
// "upnp-resume") with the box's playback source at write time. Pass source ""
// when it is unknown; it is recorded as "unknown" so the gap itself is
// visible.
func Note(kind, source string) {
	NoteN(kind, source, 1)
}

// NoteN records n writes at once (a full AddPreset sweep counts every slot).
func NoteN(kind, source string, n int) {
	if n <= 0 {
		return
	}
	src := strings.TrimSpace(source)
	if src == "" {
		src = "unknown"
	}
	key := kind + "@" + src
	mu.Lock()
	hour[key] += n
	totals[key] += n
	lastAt[kind] = time.Now()
	mu.Unlock()
}

// WroteWithin reports whether a write of this kind happened in the last d.
// A watchdog that reacts to what the BOX did calls this to recognise its own
// side effect: the firmware activates UPNP on an AddPreset and abandons the
// current source, milliseconds later, with no way to tell that apart from the
// speaker failing on its own.
func WroteWithin(kind string, d time.Duration) bool {
	mu.Lock()
	defer mu.Unlock()
	t, ok := lastAt[kind]
	return ok && time.Since(t) < d
}

// SnapshotReset returns the writes since the last call and starts a fresh
// window. Empty map = a write-free window (the healthy overnight state).
func SnapshotReset() map[string]int {
	mu.Lock()
	defer mu.Unlock()
	out := hour
	hour = map[string]int{}
	return out
}

// Totals returns a copy of the running totals since agent start.
func Totals() map[string]int {
	mu.Lock()
	defer mu.Unlock()
	out := make(map[string]int, len(totals))
	for k, v := range totals {
		out[k] = v
	}
	return out
}

// Format renders a counter map as a stable, compact one-line summary
// (sorted, "kind@source=n" space-separated) for the hourly WARN.
func Format(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}
