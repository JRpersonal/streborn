package main

import (
	"strings"
	"sync"
	"testing"
)

// Two writes to one speaker at the same time is the thing that broke a donor's
// install: both uploads wrote the same .new file, the second rename found it
// gone, and the speaker rebooted twice.
func TestSecondWriteToTheSameSpeakerIsRefused(t *testing.T) {
	b := boxBusy{busy: map[string]string{}}
	release, err := b.claim("192.0.2.10", "an install")
	if err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	if _, err := b.claim("192.0.2.10", "an update"); err == nil {
		t.Fatal("a second write to the same speaker was allowed")
	} else if !strings.Contains(err.Error(), "an install") {
		t.Errorf("the refusal should name what is running, got %q", err)
	}
	release()
	if _, err := b.claim("192.0.2.10", "an update"); err != nil {
		t.Errorf("the speaker stayed locked after the first write finished: %v", err)
	}
}

// A speaker must never be blocked by work on a DIFFERENT speaker: updating a
// fleet one box at a time is the normal case.
func TestOtherSpeakersAreUnaffected(t *testing.T) {
	b := boxBusy{busy: map[string]string{}}
	if _, err := b.claim("192.0.2.10", "an update"); err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	if _, err := b.claim("192.0.2.11", "an update"); err != nil {
		t.Errorf("a second speaker was refused: %v", err)
	}
}

// The claim is taken from UI callbacks, so it has to hold under concurrency:
// exactly one winner, and the speaker free again afterwards.
func TestOnlyOneWriterWinsUnderRace(t *testing.T) {
	b := boxBusy{busy: map[string]string{}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	releases := []func(){}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := b.claim("192.0.2.10", "an update")
			if err == nil {
				mu.Lock()
				won++
				releases = append(releases, rel)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d writers won the claim, want exactly 1", won)
	}
	for _, r := range releases {
		r()
	}
	if _, err := b.claim("192.0.2.10", "an update"); err != nil {
		t.Errorf("speaker still locked after release: %v", err)
	}
}
