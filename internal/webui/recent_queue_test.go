package webui

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/recent"
)

// TestRecentQueueCardRecordsFolderAndTracks covers #220: a DLNA folder played as
// an auto-advancing queue is recorded as one "library" (upnp) Recently-played
// card, with each track the queue pushes hung under it, and later tracks stop
// attaching once the queue is cleared.
func TestRecentQueueCardRecordsFolderAndTracks(t *testing.T) {
	s := &Server{recent: recent.New()}

	// startQueue notes the folder card, then the first push records the first
	// track (which folds into the card placeholder), then auto-advance records more.
	s.recentNoteQueueCard("queue:srv:42", "Jazz", "art.png", "http://nas/1.mp3")
	s.recentNoteQueueTrack("Song A")
	s.recentNoteQueueTrack("Song B")

	all := s.recent.All()
	if len(all) != 2 {
		t.Fatalf("want 2 entries (card folded into first track, then second track), got %d: %+v", len(all), all)
	}
	for _, e := range all {
		if e.Source != "upnp" || e.CardKey != "queue:srv:42" || e.CardName != "Jazz" {
			t.Fatalf("entry not attributed to the folder card: %+v", e)
		}
	}
	if all[0].Track != "Song A" || all[1].Track != "Song B" {
		t.Fatalf("tracks not recorded in order: %+v", all)
	}
	if all[0].CardURL != "http://nas/1.mp3" {
		t.Fatalf("replay target lost: %+v", all[0])
	}

	// After the queue stops (single play / stop / runs out), later tracks must not
	// attach to the now-dead folder card.
	s.recentClearQueueCard()
	s.recentNoteQueueTrack("Song C")
	if got := len(s.recent.All()); got != 2 {
		t.Fatalf("track recorded after the queue card was cleared: want 2 entries, got %d", got)
	}
}

// TestRecentQueueCardNilStore makes sure the helpers are no-ops without a wired
// recent store (dev builds), never panicking.
func TestRecentQueueCardNilStore(t *testing.T) {
	s := &Server{}
	s.recentNoteQueueCard("k", "n", "a", "u")
	s.recentNoteQueueTrack("t")
	s.recentClearQueueCard()
}
