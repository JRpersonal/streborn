package tzrules

import (
	"testing"
	"time"
)

// British Columbia stays on UTC-7 from November 2026 (IANA 2026b). This is the
// change the speaker's own 2022 zone files do not know, so it is the one that
// proves the embedded data is what Load reads.
func TestVancouverIsPermanentUTCMinus7(t *testing.T) {
	loc, err := Load("America/Vancouver")
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{
		time.Date(2026, 11, 9, 5, 38, 0, 0, time.UTC),
		time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC),
	} {
		if _, off := at.In(loc).Zone(); off != -7*3600 {
			t.Fatalf("America/Vancouver at %s: offset %d s, want -25200 (zone data older than 2026b?)", at, off)
		}
	}
}

func TestLoadKnownZonesAndEdges(t *testing.T) {
	for _, name := range []string{"", " ", "UTC"} {
		loc, err := Load(name)
		if err != nil || loc != time.UTC {
			t.Fatalf("Load(%q) = %v, %v; want UTC", name, loc, err)
		}
	}
	loc, err := Load("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	if _, off := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).In(loc).Zone(); off != 2*3600 {
		t.Fatalf("Europe/Berlin summer offset %d", off)
	}
	again, err := Load("Europe/Berlin")
	if err != nil || again != loc {
		t.Fatalf("second Load should come from the cache: %v %v", again, err)
	}
	if _, err := Load("Not/AZone"); err == nil {
		t.Fatal("an unknown zone must be an error")
	}
}
