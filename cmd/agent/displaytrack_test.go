package main

import (
	"context"
	"strings"
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
		trackOnSourceChanged("UPNP", name)
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

// Coming back to STR's source redraws the screen at once; staying on it or
// moving between STR's own sources does not.
func TestTrackReturnsWithSTRsSource(t *testing.T) {
	oldSet, oldRefresh := setTrackSource, refreshTrackPanel
	t.Cleanup(func() { setTrackSource, refreshTrackPanel = oldSet, oldRefresh })
	setTrackSource = func(string, bool) {}
	refreshed := make(chan struct{}, 4)
	refreshTrackPanel = func() { refreshed <- struct{}{} }
	trackOnSourceChanged("STANDBY", "UPNP")
	select {
	case <-refreshed:
	case <-time.After(2 * time.Second):
		t.Fatal("return from standby did not redraw")
	}
	trackOnSourceChanged("UPNP", "LOCAL_INTERNET_RADIO")
	trackOnSourceChanged("UPNP", "AUX")
	select {
	case <-refreshed:
		t.Fatal("redraw without a return to STR's source")
	case <-time.After(100 * time.Millisecond):
	}
}

// Every switch reaches the panel model, after the track screen heard of it,
// so the clock can stand in on Bluetooth and AUX and never outlives them.
func TestEverySourceSwitchReachesThePanelModel(t *testing.T) {
	oldSet, oldNote, oldRefresh := setTrackSource, notePanelSource, refreshTrackPanel
	t.Cleanup(func() { setTrackSource, notePanelSource, refreshTrackPanel = oldSet, oldNote, oldRefresh })
	var order []string
	setTrackSource = func(name string, _ bool) { order = append(order, "track:"+name) }
	notePanelSource = func(name string) { order = append(order, "model:"+name) }
	refreshTrackPanel = nil
	trackOnSourceChanged("UPNP", "BLUETOOTH")
	trackOnSourceChanged("BLUETOOTH", "STANDBY")
	want := []string{"track:BLUETOOTH", "model:BLUETOOTH", "track:STANDBY", "model:STANDBY"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order %v, want %v", order, want)
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
