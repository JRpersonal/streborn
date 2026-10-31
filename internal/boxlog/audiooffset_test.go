package boxlog

import (
	"testing"
	"time"
)

// Line shapes from an ST10 (rhino, FW 27.0.6): the offset lines as the
// 2026-10-31 field bundle carried them, the stale-URL lines as measured on
// 2026-10-22 (docs/FIRMWARE-NOTES.md). Host and pids are placeholders.
const (
	lineAudioOffset585 = `Oct 31 12:00:17 rhino local0.info STSCertified[1990]: [(002300):UpnpAccount:INFO]HandleAudioStatus: Audio path offset 585`
	lineAudioOffset586 = `Oct 31 12:00:18 rhino local0.info STSCertified[1990]: [(002300):UpnpAccount:INFO]HandleAudioStatus: Audio path offset 586`
	lineUpnpStateTime  = `Oct 31 12:00:18 rhino local0.info STSCertified[1990]: [(002300):UpnpState:INFO]CommonHandleAudioStatus: PLAYING end 0 time 586`
	lineStaleURLID     = `Oct 22 14:10:03 rhino local0.info APServer[1877]: [(002101):AudioIF:INFO]Audio Path state 'PLAYING error #0 for stale UrlID 12`
	lineURLIDMismatch  = `Oct 22 14:10:03 rhino local0.info APServer[1877]: [(002101):AudioIF:INFO]IsCurrentUrlId(urlId:12) - urlId mismatch. m_urlId:14`
)

// TestAudioPathOffsetTracksTheNewestLine: the offset is the speaker's play
// clock, so the newest line wins and carries the time it was read. The lines
// are never classified: one a second would flood the event ring.
func TestAudioPathOffsetTracksTheNewestLine(t *testing.T) {
	r := &Reader{forensics: newForensics()}
	if _, _, ok := r.AudioPathOffset(); ok {
		t.Fatal("no offset line read yet, want ok=false")
	}
	t0 := time.Date(2026, 10, 31, 12, 0, 17, 0, time.UTC)
	if _, _, classified := r.forensics.observe(lineAudioOffset585, t0); classified {
		t.Fatal("an offset line must not be classified")
	}
	r.forensics.observe(lineAudioOffset586, t0.Add(time.Second))
	off, at, ok := r.AudioPathOffset()
	if !ok || off != 586 || !at.Equal(t0.Add(time.Second)) {
		t.Fatalf("AudioPathOffset = %d at %v ok=%v, want 586 at %v", off, at, ok, t0.Add(time.Second))
	}
	// Unrelated lines, the UpnpState line with the same number among them,
	// change nothing.
	r.forensics.observe(lineUpnpStateTime, t0.Add(2*time.Second))
	r.forensics.observe(lineSelect, t0.Add(2*time.Second))
	off, at, _ = r.AudioPathOffset()
	if off != 586 || !at.Equal(t0.Add(time.Second)) {
		t.Fatalf("unrelated lines moved the offset to %d at %v", off, at)
	}
	if got := r.forensics.eventsSnapshot()["classified"]; got != uint64(1) {
		t.Fatalf("classified = %v, want only the Select line", got)
	}
	var nilReader *Reader
	if _, _, ok := nilReader.AudioPathOffset(); ok {
		t.Fatal("nil reader must report ok=false")
	}
}

// TestStaleURLIDLines: both lines of the frozen resume's discard are
// classified, reach the event ring, and stamp StaleURLIDSince; they are not a
// playback failure (the speaker sits in PLAYING).
func TestStaleURLIDLines(t *testing.T) {
	for _, line := range []string{lineStaleURLID, lineURLIDMismatch} {
		r := &Reader{forensics: newForensics()}
		t0 := time.Date(2026, 10, 22, 14, 10, 3, 0, time.UTC)
		if r.StaleURLIDSince(time.Time{}) {
			t.Fatal("no stale line read yet")
		}
		ev, _, ok := r.forensics.observe(line, t0)
		if !ok || ev.Class != ClassPlayStaleURL {
			t.Fatalf("%q classified as %q ok=%v, want %q", line, ev.Class, ok, ClassPlayStaleURL)
		}
		if ClassPlayStaleURL.Playback() {
			t.Fatal("a stale URL id is not a playback failure")
		}
		if _, failed := r.LastPlaybackFailure(time.Minute); failed {
			t.Fatal("a stale URL id must not read as a playback failure")
		}
		if !r.StaleURLIDSince(t0) || !r.StaleURLIDSince(t0.Add(-time.Second)) {
			t.Fatal("StaleURLIDSince must see the line at and before its stamp")
		}
		if r.StaleURLIDSince(t0.Add(time.Second)) {
			t.Fatal("StaleURLIDSince must not see a line older than t")
		}
	}
	var nilReader *Reader
	if nilReader.StaleURLIDSince(time.Time{}) {
		t.Fatal("nil reader must report false")
	}
}

// TestReplayedRingLinesDoNotCountAsNow: logread -f prints the whole ring
// before it follows, and the reader restarts it whenever it ends. A replayed
// offset or stale-URL line arrives with a stamp minutes behind the moment it
// is read; it must not become the play clock or a stale URL "since" a resume,
// while the same line read live does.
func TestReplayedRingLinesDoNotCountAsNow(t *testing.T) {
	r := &Reader{forensics: newForensics()}
	// The reader restarts logread at 12:05:00; the ring still holds the
	// offset line and a stale-URL line of 12:00:18.
	restart := time.Date(2026, 10, 31, 12, 5, 0, 0, time.UTC)
	staleLine := `Oct 31 12:00:18 rhino local0.info APServer[1877]: [(002101):AudioIF:INFO]Audio Path state 'PLAYING error #0 for stale UrlID 12`
	r.forensics.observe(lineAudioOffset586, restart)
	if ev, _, ok := r.forensics.observe(staleLine, restart); !ok || ev.Class != ClassPlayStaleURL {
		t.Fatalf("a replayed stale-URL line is still classified for the event ring, got %q ok=%v", ev.Class, ok)
	}
	if off, _, ok := r.AudioPathOffset(); ok {
		t.Fatalf("a replayed offset line became the play clock: %d", off)
	}
	if r.StaleURLIDSince(restart.Add(-time.Minute)) {
		t.Fatal("a replayed stale-URL line counted as logged since a minute ago")
	}
	// Read live (within a second of its stamp) the same lines count.
	live := time.Date(2026, 10, 31, 12, 0, 18, 400*int(time.Millisecond), time.UTC)
	r.forensics.observe(lineAudioOffset586, live)
	r.forensics.observe(staleLine, live)
	if off, at, ok := r.AudioPathOffset(); !ok || off != 586 || !at.Equal(live) {
		t.Fatalf("live offset line = %d at %v ok=%v, want 586 at %v", off, at, ok, live)
	}
	if !r.StaleURLIDSince(live) {
		t.Fatal("a live stale-URL line must count")
	}
}

func TestLineIsBacklog(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 5, 0, time.UTC)
	cases := []struct {
		stamp string
		want  bool
	}{
		{"Jan  1 00:00:04", false}, // one second old: live
		{"Jan  1 00:00:05", false}, // same second
		{"Jan  1 00:00:00", false}, // five seconds: still live
		{"Jan  1 00:00:20", false}, // ahead of now: never backlog
		{"Dec 31 23:59:50", true},  // last year's December, fifteen seconds old
		{"Dec 31 23:59:00", true},  // a minute old
		{"", false},                // no stamp
		{"not a stamp at all", false},
	}
	for _, c := range cases {
		if got := lineIsBacklog(c.stamp, now); got != c.want {
			t.Errorf("lineIsBacklog(%q) = %v, want %v", c.stamp, got, c.want)
		}
	}
}

func TestParseAudioPathOffset(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"HandleAudioStatus: Audio path offset 0", 0, true},
		{"HandleAudioStatus: Audio path offset 12345", 12345, true},
		{"HandleAudioStatus: Audio path offset 42 trailing", 42, true},
		{"HandleAudioStatus: Audio path offset ", 0, false},
		{"HandleAudioStatus: Audio path offset x", 0, false},
		{"GetOffsetAdjustMicroseconds: offset adjust 0", 0, false},
	}
	for _, c := range cases {
		got, ok := parseAudioPathOffset(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseAudioPathOffset(%q) = %d,%v want %d,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}
