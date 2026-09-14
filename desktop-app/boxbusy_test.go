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

// The refusal must carry the token the window matches on. The two sides of
// that contract live in different languages, so a rename here and a stale
// constant in main.js would silently turn a "your second click was turned
// away" hint back into the failure report with a copyable Go error that
// Sascha was shown for a SoundTouch 30 update that was running fine
// (2026-09-14).
func TestABusyRefusalIsMarkedForTheWindow(t *testing.T) {
	const jsConstant = "STR_BUSY:" // desktop-app/frontend/src/main.js, BOX_BUSY_TOKEN
	if errBoxBusyToken != jsConstant {
		t.Fatalf("errBoxBusyToken = %q but main.js matches %q", errBoxBusyToken, jsConstant)
	}
	b := boxBusy{busy: map[string]string{}}
	release, err := b.claim("192.0.2.10", "an update")
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	defer release()
	_, err = b.claim("192.0.2.10", "an update")
	if err == nil {
		t.Fatal("a second write on the same speaker was allowed")
	}
	if !strings.HasPrefix(err.Error(), errBoxBusyToken) {
		t.Errorf("refusal = %q, want it to start with %q", err, errBoxBusyToken)
	}
	// Another speaker is untouched: the guard is per box, not global.
	if _, err := b.claim("192.0.2.11", "an update"); err != nil {
		t.Errorf("a different speaker was refused: %v", err)
	}
}
