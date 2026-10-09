// Tests for the skip-storm log limit (logstorm.go, #1077).

package spotify

import (
	"log/slog"
	"testing"
	"time"
)

// The storm from the field: 51 skipped tracks. A few lines go out verbatim,
// the rest is one summary line, and the hook still sees every line.
func TestSkipStormIsSummarised(t *testing.T) {
	rec := &recHandler{}
	var seen int
	w := newLogWriter(slog.New(rec), func(string) { seen++ })
	clock := time.Now()
	w.storm.now = func() time.Time { return clock }
	for i := 0; i < 51; i++ {
		_, _ = w.Write([]byte(`level=error msg="failed retrieving aes key with code 1"` + "\n"))
		_, _ = w.Write([]byte(refusalLine + "\n"))
		clock = clock.Add(50 * time.Millisecond)
	}
	_, _ = w.Write([]byte(`level=error msg="stopping after 51 consecutive unplayable tracks"` + "\n"))
	if seen != 103 {
		t.Fatalf("the line hook saw %d lines, want all 103", seen)
	}
	verbatim := len(rec.withMsg("go-librespot"))
	sums := rec.withMsg("held back")
	if len(sums) != 1 {
		t.Fatalf("%d summary lines, want 1", len(sums))
	}
	// skipStormVerbatim storm lines, the final line, the summary itself
	// (its message also starts with "go-librespot").
	if verbatim != skipStormVerbatim+2 {
		t.Fatalf("%d go-librespot lines logged, want %d", verbatim, skipStormVerbatim+2)
	}
	var suppressed int64
	sums[0].Attrs(func(a slog.Attr) bool {
		if a.Key == "suppressed" {
			suppressed = a.Value.Int64()
		}
		return true
	})
	if suppressed != 102-skipStormVerbatim {
		t.Fatalf("summary counts %d held-back lines, want %d", suppressed, 102-skipStormVerbatim)
	}
}

// A single unavailable track, or runs far apart, are logged in full.
func TestSkipLinesFarApartAreLoggedInFull(t *testing.T) {
	rec := &recHandler{}
	w := newLogWriter(slog.New(rec), nil)
	clock := time.Now()
	w.storm.now = func() time.Time { return clock }
	for i := 0; i < 10; i++ {
		_, _ = w.Write([]byte(refusalLine + "\n"))
		clock = clock.Add(skipStormGap + time.Second)
	}
	if n := len(rec.withMsg("go-librespot")); n != 10 {
		t.Fatalf("%d lines logged, want all 10", n)
	}
	if n := len(rec.withMsg("held back")); n != 0 {
		t.Fatalf("%d summary lines for lines that were never held back", n)
	}
}
