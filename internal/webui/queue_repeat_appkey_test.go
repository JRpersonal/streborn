package webui

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// The app's recall of a folder key starts with the sticky play mode, the same
// as the hardware key; it used to start with repeat off every time (#1765).
func TestAppRecallOfAFolderKeyKeepsTheStickyRepeat(t *testing.T) {
	o := &offProbes{serves: true, hostAnswers: true}
	s, _ := offPlayServer(t, o)
	withPlayMode(t, s, "shuffle=0 repeat=all\n")
	key := offFolderKey(5, "MinimServer")
	if err := s.presets.SetSlot(key); err != nil {
		t.Fatal(err)
	}
	if _, ref := s.playSlot(context.Background(), 5); ref != nil {
		t.Fatalf("the folder key was refused: %+v", ref)
	}
	snap := s.queue.snapshot()
	if snap.Repeat != "all" || snap.Shuffle {
		t.Fatalf("repeat=%s shuffle=%v, want the sticky repeat all and shuffle off", snap.Repeat, snap.Shuffle)
	}
}

// Without a sticky mode the key's stored shuffle and repeat off still apply.
func TestAppRecallOfAFolderKeyWithoutStickyModeUsesTheKey(t *testing.T) {
	o := &offProbes{serves: true, hostAnswers: true}
	s, _ := offPlayServer(t, o)
	s.playModePath = filepath.Join(t.TempDir(), "play-mode") // never written
	key := offFolderKey(5, "MinimServer")
	key.Shuffle = true
	if err := s.presets.SetSlot(key); err != nil {
		t.Fatal(err)
	}
	if _, ref := s.playSlot(context.Background(), 5); ref != nil {
		t.Fatalf("the folder key was refused: %+v", ref)
	}
	snap := s.queue.snapshot()
	if snap.Repeat != "off" || !snap.Shuffle {
		t.Fatalf("repeat=%s shuffle=%v, want the key's shuffle and repeat off", snap.Repeat, snap.Shuffle)
	}
}

// The folder key's play mode helper is the one place both recalls ask.
func TestFolderKeyPlayModePrefersTheStickyMode(t *testing.T) {
	s := &Server{playModePath: filepath.Join(t.TempDir(), "play-mode")}
	if sh, rep := s.folderKeyPlayMode(presets.Preset{Shuffle: true}); !sh || rep != repeatOff {
		t.Fatalf("no sticky mode: shuffle=%v repeat=%s", sh, rep)
	}
	withPlayMode(t, s, "shuffle=0 repeat=one\n")
	if sh, rep := s.folderKeyPlayMode(presets.Preset{Shuffle: true}); sh || rep != repeatOne {
		t.Fatalf("sticky mode: shuffle=%v repeat=%s", sh, rep)
	}
}
