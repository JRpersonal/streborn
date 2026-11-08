package main

import (
	"sync"
	"testing"
	"time"
)

// fakePressTrace is a key trace for folderKeyPress.
type fakePressTrace struct {
	mu      sync.Mutex
	healthy bool
	traced  bool
	presses map[string]time.Time
}

func (f *fakePressTrace) Healthy() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.healthy
}

func (f *fakePressTrace) PresetKeysTraced() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.traced
}

func (f *fakePressTrace) LastPhysicalPressSince(name string, since time.Time) (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	at, ok := f.presses[name]
	if !ok || at.Before(since) {
		return time.Time{}, false
	}
	return at, true
}

func (f *fakePressTrace) set(healthy, traced bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthy, f.traced = healthy, traced
}

func (f *fakePressTrace) press(name string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presses[name] = at
}

// folderKeyPress tells the webui whether a person pressed a folder key
// (#1765 follow-up): not known without a running trace that reports preset
// keys, found for a press of that key, waited for when the line trails the
// frame, and "no press" only after the wait.
func TestFolderKeyPressAsksTheKeyTrace(t *testing.T) {
	prev := folderKeyTracePoll
	folderKeyTracePoll = 5 * time.Millisecond
	t.Cleanup(func() { folderKeyTracePoll = prev })
	if folderKeyPress(nil, 5, time.Second) != nil {
		t.Fatal("no key trace must give no answer function")
	}
	tr := &fakePressTrace{presses: map[string]time.Time{}}
	ask := folderKeyPress(tr, 5, 60*time.Millisecond)
	since := time.Now().Add(-2 * time.Second)

	if _, _, known := ask(since); known {
		t.Fatal("a trace that is not running cannot tell")
	}
	tr.set(true, false)
	if _, _, known := ask(since); known {
		t.Fatal("a trace that never read a preset press cannot tell")
	}
	tr.set(true, true)

	start := time.Now()
	if _, found, known := ask(since); found || !known {
		t.Fatalf("no press of key 5: found=%v known=%v, want a known no", found, known)
	}
	if waited := time.Since(start); waited < 50*time.Millisecond {
		t.Fatalf("answered no after %v, want it to wait for a trailing trace line", waited)
	}

	tr.press("PRESET_4", time.Now())
	if _, found, _ := ask(since); found {
		t.Fatal("a press of key 4 is not a press of key 5")
	}

	pressed := time.Now()
	go func() {
		time.Sleep(15 * time.Millisecond)
		tr.press("PRESET_5", pressed)
	}()
	if at, found, known := ask(since); !found || !known || !at.Equal(pressed) {
		t.Fatalf("trailing press of key 5: at=%v found=%v known=%v", at, found, known)
	}
}

// The handler hands the webui the frame's time and, with a key trace, the
// question; without one the webui gets no question and keeps its fallback.
func TestSlotPressCarriesTheKeyTrace(t *testing.T) {
	at := time.Now()
	h := &presetWsHandler{}
	if p := h.slotPress(5, at); !p.At.Equal(at) || p.KeyTrace != nil {
		t.Fatalf("without a key trace: %+v", p)
	}
}
