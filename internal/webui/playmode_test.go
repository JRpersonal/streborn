package webui

import (
	"log/slog"
	"path/filepath"
	"testing"
)

func TestPlayModePersistence(t *testing.T) {
	s := &Server{logger: slog.Default(), playModePath: filepath.Join(t.TempDir(), "play-mode")}
	if _, _, ok := s.loadPlayMode(); ok {
		t.Fatal("fresh box: loadPlayMode reported a stored mode")
	}
	s.savePlayMode(true, repeatAll)
	sh, rep, ok := s.loadPlayMode()
	if !ok || !sh || rep != repeatAll {
		t.Fatalf("after save(true, all): got shuffle=%v repeat=%v ok=%v", sh, rep, ok)
	}
	// The explicit OFF must stick too: sticky means "until switched off", and
	// the off is itself a choice the next key press has to honour.
	s.savePlayMode(false, repeatOff)
	sh, rep, ok = s.loadPlayMode()
	if !ok || sh || rep != repeatOff {
		t.Fatalf("after save(false, off): got shuffle=%v repeat=%v ok=%v", sh, rep, ok)
	}
}
