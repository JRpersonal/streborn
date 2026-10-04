package webui

import "testing"

// GET /api/queue names the Recently-played card of the folder the queue plays,
// so the app can keep the folder card's "now playing" mark past the first
// track (#1065). A stopped queue names no card.
func TestQueueSnapshotWithCard(t *testing.T) {
	s := &Server{queue: &playQueue{}}
	s.recentQueueCard = recentCardCtx{key: "queue:uuid:srv:64$1"}

	if got := s.queueSnapshotWithCard().Card; got != "" {
		t.Fatalf("inactive queue: Card = %q, want empty", got)
	}
	s.queue.items = []queueItem{{Title: "a"}, {Title: "b"}}
	s.queue.order = []int{0, 1}
	s.queue.active = true
	if got := s.queueSnapshotWithCard().Card; got != "queue:uuid:srv:64$1" {
		t.Fatalf("active queue: Card = %q, want the folder card key", got)
	}
}
