package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/upnp"
)

// Field test 2026-11-08 (Portable, #1765 follow-up): key 5 holds a two-track
// folder. Pressed on the sleeping speaker, the speaker re-selected key 5 by
// itself 13 s later while STR was still waking it for the folder; that frame
// waited behind the folder's own start and then skipped its first track. The
// speaker was switched off and on in between, and came back on track 2.

// gatedBox is a speaker whose first SetAVTransportURI hangs until release, the
// way a sleeping speaker holds the folder's first push while it wakes.
type gatedBox struct {
	rec     soapRecorder
	once    sync.Once
	entered chan struct{}
	gate    chan struct{}
}

func (b *gatedBox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("SOAPACTION"), "#SetAVTransportURI") {
		first := false
		b.once.Do(func() { first = true })
		if first {
			close(b.entered)
			<-b.gate
		}
	}
	b.rec.ServeHTTP(w, r)
}

func (b *gatedBox) setURIs() int {
	n := 0
	for _, a := range b.rec.list() {
		if a == "SetAVTransportURI" {
			n++
		}
	}
	return n
}

func gatedFolderServer(t *testing.T) (*Server, *gatedBox) {
	t.Helper()
	s, _ := folderResumeServer(t)
	box := &gatedBox{entered: make(chan struct{}), gate: make(chan struct{})}
	srv := httptest.NewServer(box)
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		select {
		case <-box.gate:
		default:
			close(box.gate)
		}
	})
	s.renderer = &upnp.Renderer{ControlURL: srv.URL, Client: srv.Client()}
	return s, box
}

// noKeyPress is a key trace that runs and read no press of the key.
func noKeyPress(time.Time) (time.Time, bool, bool) { return time.Time{}, false, true }

// keyPressAt is a key trace that read a press of the key at at.
func keyPressAt(at time.Time) func(time.Time) (time.Time, bool, bool) {
	return func(since time.Time) (time.Time, bool, bool) {
		if at.Before(since) {
			return time.Time{}, false, true
		}
		return at, true, true
	}
}

func queueTitle(s *Server) string {
	it, _ := s.queue.current()
	return it.Title
}

// The field order of events: press, re-selection while the folder starts,
// power off, power on. The folder plays its first track throughout and comes
// back on it after the power-on.
func TestFolderKeyReselectedWhileItsFolderStartsDoesNotSkipAndResumesTheSameTrack(t *testing.T) {
	s, box := gatedFolderServer(t)
	items := resumeTracks(2)
	seedFolderKey(t, s, 5, "STR Resume Test", items)

	first := make(chan bool, 1)
	go func() { first <- s.RecallSlot(context.Background(), 5) }()
	select {
	case <-box.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the press never handed the first track to the speaker")
	}

	// 19:36:28.816: the speaker selects key 5 by itself while it wakes.
	second := make(chan bool, 1)
	go func() {
		second <- s.RecallSlotPress(context.Background(), 5, SlotPress{At: time.Now(), KeyTrace: noKeyPress})
	}()
	select {
	case handled := <-second:
		if !handled {
			t.Fatal("the re-selection of a folder key was not claimed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the re-selection waited for the folder's start instead of standing down at once")
	}
	// The same frame on a speaker without a key trace stands down too.
	if !s.RecallSlotPress(context.Background(), 5, SlotPress{At: time.Now()}) {
		t.Fatal("the re-selection without a key trace was not claimed")
	}

	close(box.gate)
	if !<-first {
		t.Fatal("the press was not claimed")
	}
	if got := queueTitle(s); got != "Song 0" {
		t.Fatalf("the folder plays %q after the start, want its first track", got)
	}
	if n := box.setURIs(); n != 1 {
		t.Fatalf("the speaker was handed %d tracks, want only the first one", n)
	}

	// Switched off during track 1, on again.
	s.stopQueueForStandby("the box was powered off")
	if !resumeLocked(t, s) {
		t.Fatal("the power-on resume did not continue the folder")
	}
	if got := queueTitle(s); got != "Song 0" {
		t.Fatalf("the folder came back on %q, want the track it was switched off on", got)
	}

	// The speaker re-selects its key as it wakes: with a key trace that shows
	// no press, and without a key trace inside the resume window.
	for _, press := range []SlotPress{{At: time.Now(), KeyTrace: noKeyPress}, {At: time.Now()}} {
		if !s.RecallSlotPress(context.Background(), 5, press) {
			t.Fatal("the power-on re-selection was not claimed")
		}
		if got := queueTitle(s); got != "Song 0" {
			t.Fatalf("the power-on re-selection moved the folder to %q, want it to play on", got)
		}
	}
}

// startedFolder starts the folder on key slot the way a press does and waits
// until the first track was handed over.
func startedFolder(t *testing.T, s *Server, slot, tracks int) {
	t.Helper()
	seedFolderKey(t, s, slot, "Folder", resumeTracks(tracks))
	if !s.RecallSlot(context.Background(), slot) {
		t.Fatal("the press was not claimed")
	}
	if got := queueTitle(s); got != "Song 0" {
		t.Fatalf("the folder starts on %q, want its first track", got)
	}
	time.Sleep(5 * time.Millisecond) // the presses below come after the track start
}

func TestOnlyANewKeyPressSkipsTheFolderThatPlays(t *testing.T) {
	s, _ := folderResumeServer(t)
	startedFolder(t, s, 5, 4)
	ctx := context.Background()

	// The speaker selected the key by itself.
	s.RecallSlotPress(ctx, 5, SlotPress{At: time.Now(), KeyTrace: noKeyPress})
	if got := queueTitle(s); got != "Song 0" {
		t.Fatalf("a selection without a key press skipped to %q", got)
	}
	// The press the trace read is the one that started the folder.
	s.RecallSlotPress(ctx, 5, SlotPress{At: time.Now(), KeyTrace: keyPressAt(time.Now().Add(-time.Second))})
	if got := queueTitle(s); got != "Song 0" {
		t.Fatalf("the press that started the folder skipped it to %q", got)
	}

	// A person presses the key again: next track.
	pressed := time.Now()
	s.RecallSlotPress(ctx, 5, SlotPress{At: time.Now(), KeyTrace: keyPressAt(pressed)})
	if got := queueTitle(s); got != "Song 1" {
		t.Fatalf("a new press of the key plays %q, want the next track", got)
	}
	time.Sleep(5 * time.Millisecond)
	// The speaker announces the same press a second time (inside a group).
	s.RecallSlotPress(ctx, 5, SlotPress{At: time.Now(), KeyTrace: keyPressAt(pressed)})
	if got := queueTitle(s); got != "Song 1" {
		t.Fatalf("the echo of one press skipped a second time, to %q", got)
	}

	// A speaker without a key trace keeps the old behaviour: a press skips.
	s.RecallSlotPress(ctx, 5, SlotPress{At: time.Now()})
	if got := queueTitle(s); got != "Song 2" {
		t.Fatalf("a press without a key trace plays %q, want the next track", got)
	}
}

// A press that waited behind something that moved the folder on does not skip
// the track that took its place.
func TestAPressThatWaitedStandsDownWhenTheFolderMovedOn(t *testing.T) {
	s, _ := folderResumeServer(t)
	startedFolder(t, s, 5, 3)
	press := SlotPress{At: time.Now()}
	why, gen, _, trace := s.folderKeyNotARepress(press)
	if why != "" {
		t.Fatalf("a press on the playing folder was refused: %s", why)
	}
	// While the press waits for the box command lock, the folder advances.
	s.boxCmdMu.Lock()
	s.queue.next()
	s.setQueueTiming(3 * time.Minute)
	s.boxCmdMu.Unlock()

	if _, ok, stale, err := s.skipFolderForPress(5, gen, press.At, trace); ok || err != nil || stale == "" {
		t.Fatalf("skip after the folder moved on: ok=%v stale=%q err=%v, want a stand-down", ok, stale, err)
	}
	if got := queueTitle(s); got != "Song 1" {
		t.Fatalf("the waiting press moved the folder to %q", got)
	}
}

// The speaker went off while a skip's push was still under way: the queue had
// moved on, the play record still named the track that played. The folder is
// kept at that track instead of being dropped.
func TestStandbyDuringASkipKeepsTheTrackThatPlayed(t *testing.T) {
	s, _ := folderResumeServer(t)
	items := resumeTracks(4)
	s.queue.load(items, 0, false, repeatOff)
	s.queue.next()
	cur, _ := s.queue.current()
	s.setLastPlay(queueBoxURL(cur.URL, cur.Mime), cur.Title, cur.Art, cur.Mime)
	s.queue.next() // the skip: the push of Song 2 has not landed

	s.stopQueueForStandby("the box was powered off")
	if !resumeLocked(t, s) {
		t.Fatal("the folder was dropped by a standby during a skip")
	}
	if got := queueTitle(s); got != "Song 1" {
		t.Fatalf("the folder came back on %q, want the track that played when the speaker went off", got)
	}
}

// The speaker resumes its last key on power-on and says so as a selection of
// that key: the folder the standby ended continues at its track. A person
// pressing the key starts the folder from the top, as before.
func TestSpeakerReselectingTheKeyAtPowerOnContinuesTheKeptFolder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		press func() SlotPress
		want  string
	}{
		{"speaker by itself", func() SlotPress { return SlotPress{At: time.Now(), KeyTrace: noKeyPress} }, "Song 2"},
		{"person pressing", func() SlotPress { return SlotPress{At: time.Now(), KeyTrace: keyPressAt(time.Now())} }, "Song 0"},
		{"no key trace", func() SlotPress { return SlotPress{At: time.Now()} }, "Song 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := folderResumeServer(t)
			items := resumeTracks(4)
			p := seedFolderKey(t, s, 5, "Folder", items)
			s.queue.load(items, 0, false, repeatOff)
			s.queue.next()
			s.queue.next()
			s.queueMu.Lock()
			s.queueFolder = slotQueueCard(5, p)
			s.queueMu.Unlock()
			cur, _ := s.queue.current()
			s.setLastPlay(queueBoxURL(cur.URL, cur.Mime), cur.Title, cur.Art, cur.Mime)
			s.stopQueueForStandby("the box was powered off")

			if !s.RecallSlotPress(context.Background(), 5, tc.press()) {
				t.Fatal("the folder key was not claimed")
			}
			if got := queueTitle(s); got != tc.want {
				t.Fatalf("the folder plays %q, want %q", got, tc.want)
			}
		})
	}
}
