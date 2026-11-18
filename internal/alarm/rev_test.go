package alarm

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The revision is what lets an editor say "replace the copy I read" (discussion
// #1918): every save bumps it, a reload keeps it, and what a caller puts in
// Document.Rev is ignored.
func TestStoreRevisionBumpsAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarms.json")
	s, err := Load(path, quietLogger())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.Rev() != 0 {
		t.Fatalf("a fresh store is at revision %d, want 0", s.Rev())
	}
	if err := s.Set(Document{Alarms: []Alarm{weekdayAlarm()}, Rev: 41}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if s.Rev() != 1 || s.Get().Rev != 1 {
		t.Fatalf("after one save rev = %d (Get %d), want 1: the caller's 41 must be ignored", s.Rev(), s.Get().Rev)
	}
	stored, err := s.SetAt(Document{Alarms: []Alarm{weekdayAlarm()}}, 1)
	if err != nil {
		t.Fatalf("SetAt the current revision: %v", err)
	}
	if stored.Rev != 2 || s.Rev() != 2 {
		t.Fatalf("SetAt answered rev %d, store at %d, want 2", stored.Rev, s.Rev())
	}
	again, err := Load(path, quietLogger())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again.Rev() != 2 {
		t.Errorf("rev after a restart = %d, want 2: it has to survive an agent restart", again.Rev())
	}
	// The backup carries it too, so a recovery does not send it backwards.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	recovered, err := Load(path, quietLogger())
	if err != nil {
		t.Fatalf("reload from the backup: %v", err)
	}
	if recovered.Rev() != 2 {
		t.Errorf("rev after a backup recovery = %d, want 2", recovered.Rev())
	}
}

// A file written before revisions existed has none and loads at 0, so an
// editor that read it can still save with rev 0.
func TestStoreRevisionOfAnOldFileIsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarms.json")
	if err := os.WriteFile(path, []byte(`{"zone":"","alarms":[{"id":"a","enabled":true,"hour":6,"minute":30,"days":[1],"slot":3}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path, quietLogger())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.Rev() != 0 {
		t.Fatalf("rev = %d, want 0", s.Rev())
	}
	if _, err := s.SetAt(s.Get(), 0); err != nil {
		t.Fatalf("SetAt 0 on an old file: %v", err)
	}
}

// A save at a revision that is no longer the stored one writes nothing.
func TestStoreSetAtRefusesAStaleRevision(t *testing.T) {
	s := New()
	if err := s.Set(Document{Alarms: []Alarm{weekdayAlarm()}}); err != nil {
		t.Fatalf("set: %v", err)
	}
	off := weekdayAlarm()
	off.Enabled = false
	if err := s.Set(Document{Alarms: []Alarm{off}}); err != nil { // another editor
		t.Fatalf("set: %v", err)
	}
	if _, err := s.SetAt(Document{Alarms: []Alarm{weekdayAlarm()}}, 1); !errors.Is(err, ErrStale) {
		t.Fatalf("SetAt a stale revision = %v, want ErrStale", err)
	}
	if got := s.Get(); got.Alarms[0].Enabled || got.Rev != 2 {
		t.Errorf("a stale save reached the store: %+v", got)
	}
	// An invalid document is refused for what it is, not for its revision.
	if _, err := s.SetAt(Document{Alarms: []Alarm{{Hour: 99, Slot: 3, Days: []int{1}}}}, 2); err == nil || errors.Is(err, ErrStale) {
		t.Errorf("an invalid document at the current revision = %v, want a validation error", err)
	}
}

// Two editors saving the same revision at once: exactly one wins.
func TestStoreSetAtOneWinnerPerRevision(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.SetAt(Document{Alarms: []Alarm{weekdayAlarm()}}, 0); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won != 1 || s.Rev() != 1 {
		t.Errorf("%d saves at rev 0 won, store at rev %d; want exactly one and rev 1", won, s.Rev())
	}
}
