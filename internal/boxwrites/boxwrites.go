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
)

var (
	mu     sync.Mutex
	hour   = map[string]int{} // reset by SnapshotReset (the hourly WARN)
	totals = map[string]int{} // since agent start (the debug section)
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
	mu.Unlock()
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
