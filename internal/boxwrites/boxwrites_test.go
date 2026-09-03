package boxwrites

import (
	"reflect"
	"testing"
	"time"
)

// resetLedger clears the package-global ledger so each test starts from a
// write-free state.
func resetLedger() {
	mu.Lock()
	hour = map[string]int{}
	totals = map[string]int{}
	lastAt = map[string]time.Time{}
	mu.Unlock()
}

// backdate moves a kind's last-write timestamp into the past, so window
// expiry can be tested deterministically instead of sleeping.
func backdate(kind string, ago time.Duration) {
	mu.Lock()
	lastAt[kind] = time.Now().Add(-ago)
	mu.Unlock()
}

// WroteWithin is what lets a watchdog tell STR's own footprint apart from the
// speaker failing on its own: a fresh write must be seen inside the window, an
// old one must have expired, and a kind never written must report false.
func TestWroteWithin(t *testing.T) {
	tests := []struct {
		name    string
		arrange func()
		kind    string
		window  time.Duration
		want    bool
	}{
		{
			name:    "fresh write is seen within the window",
			arrange: func() { Note("addpreset", "STANDBY") },
			kind:    "addpreset",
			window:  time.Minute,
			want:    true,
		},
		{
			name: "write older than the window has expired",
			arrange: func() {
				Note("addpreset", "STANDBY")
				backdate("addpreset", time.Hour)
			},
			kind:   "addpreset",
			window: time.Minute,
			want:   false,
		},
		{
			name:    "never-recorded kind reports false",
			arrange: func() { Note("addpreset", "STANDBY") },
			kind:    "setmarge",
			window:  time.Hour,
			want:    false,
		},
		{
			name:    "kind matches regardless of the source it was recorded with",
			arrange: func() { Note("upnp-resume", "SPOTIFY") },
			kind:    "upnp-resume",
			window:  time.Minute,
			want:    true,
		},
		{
			name:    "zero window never matches",
			arrange: func() { Note("addpreset", "STANDBY") },
			kind:    "addpreset",
			window:  0,
			want:    false,
		},
		{
			name:    "non-positive NoteN does not count as a write",
			arrange: func() { NoteN("addpreset", "STANDBY", 0) },
			kind:    "addpreset",
			window:  time.Hour,
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetLedger()
			tt.arrange()
			if got := WroteWithin(tt.kind, tt.window); got != tt.want {
				t.Errorf("WroteWithin(%q, %v) = %v, want %v", tt.kind, tt.window, got, tt.want)
			}
		})
	}
}

// The counter key is "kind@source", an empty source is recorded as "unknown"
// so the gap itself is visible, and non-positive counts are ignored.
func TestNoteCounterKeys(t *testing.T) {
	tests := []struct {
		name    string
		arrange func()
		want    map[string]int
	}{
		{
			name:    "single write keyed by kind and source",
			arrange: func() { Note("addpreset", "STANDBY") },
			want:    map[string]int{"addpreset@STANDBY": 1},
		},
		{
			name: "repeat writes accumulate under the same key",
			arrange: func() {
				Note("addpreset", "STANDBY")
				NoteN("addpreset", "STANDBY", 5)
			},
			want: map[string]int{"addpreset@STANDBY": 6},
		},
		{
			name:    "empty source is recorded as unknown",
			arrange: func() { Note("setmarge", "") },
			want:    map[string]int{"setmarge@unknown": 1},
		},
		{
			name:    "whitespace-only source is recorded as unknown",
			arrange: func() { Note("setmarge", "  ") },
			want:    map[string]int{"setmarge@unknown": 1},
		},
		{
			name: "non-positive counts are ignored",
			arrange: func() {
				NoteN("addpreset", "STANDBY", 0)
				NoteN("addpreset", "STANDBY", -3)
			},
			want: map[string]int{},
		},
		{
			name: "different sources for one kind stay separate keys",
			arrange: func() {
				Note("upnp-resume", "SPOTIFY")
				Note("upnp-resume", "LOCAL_INTERNET_RADIO")
			},
			want: map[string]int{
				"upnp-resume@SPOTIFY":              1,
				"upnp-resume@LOCAL_INTERNET_RADIO": 1,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetLedger()
			tt.arrange()
			if got := Totals(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Totals() = %v, want %v", got, tt.want)
			}
		})
	}
}

// SnapshotReset hands out the hourly window and starts a fresh one; the
// running totals must survive the reset, and an untouched window must come
// back empty (the healthy overnight state).
func TestSnapshotReset(t *testing.T) {
	resetLedger()
	Note("addpreset", "STANDBY")
	NoteN("addpreset", "STANDBY", 2)
	Note("setmarge", "")

	want := map[string]int{"addpreset@STANDBY": 3, "setmarge@unknown": 1}
	if got := SnapshotReset(); !reflect.DeepEqual(got, want) {
		t.Fatalf("first SnapshotReset() = %v, want %v", got, want)
	}
	// The window restarted: a second snapshot with no writes in between is empty.
	if got := SnapshotReset(); len(got) != 0 {
		t.Fatalf("second SnapshotReset() = %v, want empty", got)
	}
	// The since-start totals are not touched by the hourly reset.
	if got := Totals(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Totals() after SnapshotReset = %v, want %v", got, want)
	}
}

// Totals must return a copy: a caller mutating the returned map (the debug
// section serializer) must not corrupt the ledger.
func TestTotalsReturnsCopy(t *testing.T) {
	resetLedger()
	Note("addpreset", "STANDBY")

	got := Totals()
	got["addpreset@STANDBY"] = 999
	got["injected@key"] = 1

	want := map[string]int{"addpreset@STANDBY": 1}
	if again := Totals(); !reflect.DeepEqual(again, want) {
		t.Errorf("Totals() after mutating a previous result = %v, want %v", again, want)
	}
}

// Format renders the hourly WARN line: stable (sorted) key order, compact
// "kind@source=n" pairs, and "" for a write-free window.
func TestFormat(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]int
		want string
	}{
		{
			name: "empty map renders empty string",
			in:   map[string]int{},
			want: "",
		},
		{
			name: "nil map renders empty string",
			in:   nil,
			want: "",
		},
		{
			name: "single entry",
			in:   map[string]int{"addpreset@STANDBY": 6},
			want: "addpreset@STANDBY=6",
		},
		{
			name: "entries are sorted by key",
			in: map[string]int{
				"setmarge@unknown":    1,
				"addpreset@STANDBY":   6,
				"upnp-resume@SPOTIFY": 2,
			},
			want: "addpreset@STANDBY=6 setmarge@unknown=1 upnp-resume@SPOTIFY=2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Format(tt.in); got != tt.want {
				t.Errorf("Format(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
