// Regression test for #490: a playlist recalled from a preset always opened
// with the same (first) track even with shuffle on, because the preset path
// has no chosen track and passed index 0, which buildOrder pins to the front.

package webui

import "testing"

func TestLoadShuffleWithoutChoiceVariesFirstTrack(t *testing.T) {
	items := make([]queueItem, 20)
	for i := range items {
		items[i] = queueItem{URL: string(rune('a' + i))}
	}
	seen := map[int]bool{}
	// One queue, reloaded: a fresh newPlayQueue() per iteration would seed its
	// generator from the wall clock, and on a coarse-resolution clock (Windows)
	// every queue in a tight loop draws the SAME seed, which would make this
	// test pass or fail for reasons that have nothing to do with the fix.
	q := newPlayQueue()
	for i := 0; i < 40; i++ {
		q.load(items, -1, true, repeatOff)
		cur, ok := q.current()
		if !ok {
			t.Fatal("queue must be active after load")
		}
		for idx, it := range items {
			if it.URL == cur.URL {
				seen[idx] = true
				break
			}
		}
	}
	if len(seen) < 3 {
		t.Fatalf("shuffle without a chosen track must not keep opening on the same item, distinct first tracks: %d", len(seen))
	}
}

func TestLoadShuffleWithChoiceKeepsIt(t *testing.T) {
	items := []queueItem{{URL: "a"}, {URL: "b"}, {URL: "c"}, {URL: "d"}, {URL: "e"}}
	for i := 0; i < 20; i++ {
		q := newPlayQueue()
		q.load(items, 3, true, repeatOff)
		cur, ok := q.current()
		if !ok || cur.URL != "d" {
			t.Fatalf("an explicitly chosen track must still play first, got %v (ok=%v)", cur.URL, ok)
		}
	}
}
