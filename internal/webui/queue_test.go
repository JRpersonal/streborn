package webui

import (
	"math/rand"
	"testing"
)

func mkItems(n int) []queueItem {
	items := make([]queueItem, n)
	for i := range items {
		items[i] = queueItem{URL: string(rune('a' + i)), Title: string(rune('A' + i))}
	}
	return items
}

// titles drains the queue by natural advance and returns the order of item
// titles played, stopping when the queue deactivates or maxSteps is reached.
func titles(q *playQueue, maxSteps int) []string {
	var out []string
	it, ok := q.current()
	for ok && len(out) < maxSteps {
		out = append(out, it.Title)
		it, ok = q.advanceNatural()
	}
	return out
}

func TestQueueSequentialRepeatOff(t *testing.T) {
	q := newPlayQueue()
	q.load(mkItems(3), 0, false, repeatOff)
	got := titles(q, 10)
	want := []string{"A", "B", "C"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if q.isActive() {
		t.Fatalf("queue should be inactive after the last track with repeatOff")
	}
}

func TestQueueStartOffset(t *testing.T) {
	q := newPlayQueue()
	q.load(mkItems(3), 1, false, repeatOff)
	got := titles(q, 10)
	want := []string{"B", "C"}
	if len(got) != len(want) || got[0] != "B" || got[1] != "C" {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestQueueRepeatAllWraps(t *testing.T) {
	q := newPlayQueue()
	q.load(mkItems(3), 0, false, repeatAll)
	got := titles(q, 7)
	want := []string{"A", "B", "C", "A", "B", "C", "A"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestQueueRepeatOneReplays(t *testing.T) {
	q := newPlayQueue()
	q.load(mkItems(3), 0, false, repeatOne)
	got := titles(q, 4)
	for _, g := range got {
		if g != "A" {
			t.Fatalf("repeatOne should replay A, got %v", got)
		}
	}
	// A manual next ignores repeatOne and moves on.
	it, ok := q.next()
	if !ok || it.Title != "B" {
		t.Fatalf("manual next under repeatOne should give B, got %q ok=%v", it.Title, ok)
	}
}

func TestQueueManualNextStopsAtEnd(t *testing.T) {
	q := newPlayQueue()
	q.load(mkItems(2), 0, false, repeatOff)
	if it, ok := q.next(); !ok || it.Title != "B" {
		t.Fatalf("next should give B, got %q ok=%v", it.Title, ok)
	}
	if _, ok := q.next(); ok {
		t.Fatalf("next past the last track with repeatOff should stop")
	}
	if q.isActive() {
		t.Fatalf("queue should be inactive after next past the end")
	}
}

func TestQueuePrev(t *testing.T) {
	q := newPlayQueue()
	q.load(mkItems(3), 0, false, repeatOff)
	q.advanceNatural() // -> B
	if it, ok := q.prev(); !ok || it.Title != "A" {
		t.Fatalf("prev should give A, got %q ok=%v", it.Title, ok)
	}
	// At the front with repeatOff, prev stays on A.
	if it, ok := q.prev(); !ok || it.Title != "A" {
		t.Fatalf("prev at front should stay on A, got %q ok=%v", it.Title, ok)
	}
}

func TestQueuePrevWrapsRepeatAll(t *testing.T) {
	q := newPlayQueue()
	q.load(mkItems(3), 0, false, repeatAll)
	if it, ok := q.prev(); !ok || it.Title != "C" {
		t.Fatalf("prev at front with repeatAll should wrap to C, got %q ok=%v", it.Title, ok)
	}
}

func TestQueueShuffleStartsAtChosenAndCoversAll(t *testing.T) {
	q := newPlayQueue()
	q.rnd = rand.New(rand.NewSource(42)) // deterministic
	q.load(mkItems(5), 2, true, repeatOff)
	first, ok := q.current()
	if !ok || first.Title != "C" {
		t.Fatalf("shuffle should play the chosen start (C) first, got %q", first.Title)
	}
	got := titles(q, 10)
	if len(got) != 5 {
		t.Fatalf("shuffle should play every track once, got %v", got)
	}
	seen := map[string]bool{}
	for _, g := range got {
		if seen[g] {
			t.Fatalf("shuffle replayed %q: %v", g, got)
		}
		seen[g] = true
	}
}

func TestQueueSetShuffleKeepsCurrent(t *testing.T) {
	q := newPlayQueue()
	q.rnd = rand.New(rand.NewSource(7))
	q.load(mkItems(5), 0, false, repeatOff)
	q.advanceNatural() // -> B
	cur, _ := q.current()
	q.setShuffle(true)
	after, ok := q.current()
	if !ok || after.Title != cur.Title {
		t.Fatalf("toggling shuffle should keep the current track %q, got %q", cur.Title, after.Title)
	}
	q.setShuffle(false)
	back, ok := q.current()
	if !ok || back.Title != cur.Title {
		t.Fatalf("toggling shuffle off should keep the current track %q, got %q", cur.Title, back.Title)
	}
}

// TestNowPlayingStandby covers the #219 power-off-during-queue guard: a box that
// went to standby must be detected so the watcher stops the queue instead of
// advancing (which would wake the box and play the next track). A track literally
// named "STANDBY" while actually playing must NOT be mistaken for it.
func TestNowPlayingStandby(t *testing.T) {
	standby := `<?xml version="1.0"?><nowPlaying deviceID="x" source="STANDBY"><ContentItem source="STANDBY" isPresetable="false" /></nowPlaying>`
	if !nowPlayingStandby(standby) {
		t.Fatal("a source=STANDBY nowPlaying must be detected as standby")
	}
	playing := `<?xml version="1.0"?><nowPlaying source="UPNP"><ContentItem source="UPNP" location="http://a"><itemName>STANDBY (band)</itemName></ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying>`
	if nowPlayingStandby(playing) {
		t.Fatal("a playing UPNP track named STANDBY must NOT count as standby")
	}
}

func TestQueueSnapshot(t *testing.T) {
	q := newPlayQueue()
	q.load(mkItems(3), 1, false, repeatAll)
	snap := q.snapshot()
	if !snap.Active || snap.Repeat != "all" || snap.Shuffle {
		t.Fatalf("unexpected snapshot %+v", snap)
	}
	if len(snap.Items) != 3 {
		t.Fatalf("snapshot should list 3 items, got %d", len(snap.Items))
	}
	if snap.Pos != 1 {
		t.Fatalf("snapshot Pos should be the current item index 1, got %d", snap.Pos)
	}
}
