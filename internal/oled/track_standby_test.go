package oled

import (
	"testing"
	"time"
)

// A power-off goes dark at once but hands the flip thread back only after the
// power key is up (#1798): handed back while the key was still down, the
// flood of flips BoseApp had queued during the hold made a SoundTouch 20 read
// the rest of the press as a second power press, and it switched straight
// back on.
func TestTrackScreenStandbyKeepsThePanelDarkUntilTheKeyIsUp(t *testing.T) {
	s, p := newFakeTrack()
	s.standbyRelease = 150 * time.Millisecond
	s.setSource("LOCAL_INTERNET_RADIO", true)
	s.show(TrackText{Station: "Radio", Title: "Song"})
	waitFor(t, "the track screen", func() bool { return p.held.Load() && p.lastMax.Load() > 0 })

	off := time.Now()
	s.setSource("STANDBY", false)
	waitFor(t, "a black frame", func() bool { return p.lastMax.Load() == 0 })
	if !p.held.Load() {
		t.Fatal("the panel was handed back together with the black frame")
	}
	waitFor(t, "the release after the wait", func() bool { return !p.held.Load() })
	if waited := time.Since(off); waited < 140*time.Millisecond {
		t.Fatalf("handed back after %v, before the power key could be up", waited)
	}
	time.Sleep(30 * time.Millisecond)
	if p.holds.Load() != 1 {
		t.Fatalf("panel taken again in standby: %d holds", p.holds.Load())
	}
}

// A speaker that comes straight back on inside the wait keeps the running
// hold for its screen instead of losing it to the late release.
func TestTrackScreenStandbyWaitEndsWhenTheSpeakerComesBack(t *testing.T) {
	s, p := newFakeTrack()
	s.standbyRelease = 150 * time.Millisecond
	s.setSource("LOCAL_INTERNET_RADIO", true)
	s.show(TrackText{Station: "Radio", Title: "Song"})
	waitFor(t, "the track screen", func() bool { return p.held.Load() && p.lastMax.Load() > 0 })

	s.setSource("STANDBY", false)
	waitFor(t, "a black frame", func() bool { return p.lastMax.Load() == 0 })
	s.setSource("LOCAL_INTERNET_RADIO", true)
	s.show(TrackText{Station: "Radio", Title: "Song"})
	waitFor(t, "the track again", func() bool { return p.lastMax.Load() > 0 })
	time.Sleep(250 * time.Millisecond)
	if !p.held.Load() || p.holds.Load() != 1 {
		t.Fatalf("the late standby release ended the speaker's screen: held=%v holds=%d", p.held.Load(), p.holds.Load())
	}
	s.clear("test")
}

// Only standby waits: any other source hands the panel back at once.
func TestTrackScreenOtherSourcesReleaseAtOnce(t *testing.T) {
	s, p := newFakeTrack()
	s.standbyRelease = time.Second
	s.show(TrackText{Title: "Song"})
	waitFor(t, "the panel to be held", p.held.Load)
	start := time.Now()
	s.setSource("AUX", false)
	waitFor(t, "the release", func() bool { return !p.held.Load() })
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("AUX waited %v like a power-off", d)
	}
}

// A standby with nothing held starts no wait and no release.
func TestTrackScreenStandbyWithoutHoldIsQuiet(t *testing.T) {
	s, p := newFakeTrack()
	s.standbyRelease = time.Second
	start := time.Now()
	s.setSource("STANDBY", false)
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("setSource blocked for %v", d)
	}
	if p.holds.Load() != 0 {
		t.Fatalf("standby took the panel: %d holds", p.holds.Load())
	}
}
