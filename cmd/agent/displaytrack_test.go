package main

import (
	"context"
	"testing"
	"time"
)

func TestTrackSourceFollowsSTRsSources(t *testing.T) {
	type call struct {
		name string
		ours bool
	}
	got := make(chan call, 8)
	old := setTrackSource
	setTrackSource = func(name string, ours bool) { got <- call{name, ours} }
	t.Cleanup(func() { setTrackSource = old })

	want := map[string]bool{
		"UPNP": true, "LOCAL_INTERNET_RADIO": true,
		"STANDBY": false, "AUX": false, "BLUETOOTH": false, "TUNEIN": false,
	}
	for name := range want {
		trackOnSourceChanged(name)
	}
	for range want {
		select {
		case c := <-got:
			if want[c.name] != c.ours {
				t.Errorf("source %s: ours=%v, want %v", c.name, c.ours, want[c.name])
			}
		case <-time.After(2 * time.Second):
			t.Fatal("source never reported")
		}
	}
}

func TestVolumeChangeHandsThePanelBackBriefly(t *testing.T) {
	got := make(chan time.Duration, 1)
	old := pauseTrack
	pauseTrack = func(d time.Duration, _ string) { got <- d }
	t.Cleanup(func() { pauseTrack = old })
	(&presetWsHandler{}).OnVolumeChanged(context.Background())
	select {
	case d := <-got:
		if d <= 0 {
			t.Fatalf("pause %v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no pause")
	}
}
