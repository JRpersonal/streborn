package webui

import (
	"context"
	"fmt"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// A shuffled folder key started from the app or the phone opened on the same
// song every time: the app recall passed index 0, which the queue pins to the
// front, while the hardware key already passed -1 (#490). Seen on a 44-song
// folder that began with the same song on every press (#1701).
func TestAppRecallOfAShuffledFolderKeyStartsOnARandomSong(t *testing.T) {
	o := &offProbes{serves: true, hostAnswers: true}
	s, _ := offPlayServer(t, o)
	key := offFolderKey(6, "MinimServer")
	key.Shuffle = true
	key.Items = nil
	for i := 0; i < 20; i++ {
		key.Items = append(key.Items, presets.PresetItem{URL: fmt.Sprintf("%s?n=%d", offTrackURL, i), Title: fmt.Sprintf("Song %d", i)})
	}
	if err := s.presets.SetSlot(key); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < 15; i++ {
		if _, ref := s.playSlot(context.Background(), 6); ref != nil {
			t.Fatalf("the folder key was refused: %+v", ref)
		}
		cur, ok := s.queue.current()
		if !ok {
			t.Fatal("no queue after the folder key")
		}
		seen[cur.URL] = true
	}
	if len(seen) < 3 {
		t.Fatalf("a shuffled folder key opened on %d distinct songs in 15 starts, want a random one each time", len(seen))
	}
}
