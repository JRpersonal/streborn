package boxws

import (
	"context"
	"encoding/xml"
	"testing"
)

// volHandler is recHandler plus the optional volume hooks.
type volHandler struct {
	recHandler
	changed int
	levels  []int
	mutes   []bool
}

func (h *volHandler) OnVolumeChanged(context.Context)            { h.changed++ }
func (h *volHandler) OnVolumeLevel(_ context.Context, level int) { h.levels = append(h.levels, level) }
func (h *volHandler) OnVolumeMute(_ context.Context, muted bool) { h.mutes = append(h.mutes, muted) }

// #1748: the mute flag in the volume frame reaches the handler, so the phone
// remote can follow a mute set in the app or with the remote.
func TestVolumeUpdatedPassesTheMuteFlag(t *testing.T) {
	h := &volHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(`<updates deviceID="AABBCCDDEEFF"><volumeUpdated><volume>`+
		`<targetvolume>9</targetvolume><actualvolume>9</actualvolume><muteenabled>true</muteenabled>`+
		`</volume></volumeUpdated></updates>`))
	c.handleMessage(context.Background(), []byte(`<updates deviceID="AABBCCDDEEFF"><volumeUpdated><volume>`+
		`<targetvolume>9</targetvolume><actualvolume>9</actualvolume><muteenabled>false</muteenabled>`+
		`</volume></volumeUpdated></updates>`))
	if len(h.mutes) != 2 || !h.mutes[0] || h.mutes[1] {
		t.Fatalf("want [true false], got %v", h.mutes)
	}
}

// A frame without the flag says nothing about the mute: no call, never a
// "not muted" that would clear a mute the speaker still holds.
func TestVolumeUpdatedWithoutMuteFlagReportsNothing(t *testing.T) {
	h := &volHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(`<updates deviceID="AABBCCDDEEFF"><volumeUpdated><volume>`+
		`<targetvolume>4</targetvolume></volume></volumeUpdated></updates>`))
	c.handleMessage(context.Background(), []byte(`<updates deviceID="AABBCCDDEEFF"><volumeUpdated /></updates>`))
	if len(h.mutes) != 0 {
		t.Fatalf("want no mute report, got %v", h.mutes)
	}
}

// The frame shape measured on a Portable and an ST10 (FW 27.0.x) carries the
// new level; the Spotify engine needs it so the app's slider follows the
// speaker.
func TestVolumeUpdatedPassesTheLevel(t *testing.T) {
	h := &volHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(`<updates deviceID="AABBCCDDEEFF"><volumeUpdated><volume>`+
		`<targetvolume>4</targetvolume><actualvolume>3</actualvolume><muteenabled>false</muteenabled>`+
		`</volume></volumeUpdated></updates>`))
	if h.changed != 1 {
		t.Fatalf("OnVolumeChanged must still fire, got %d", h.changed)
	}
	if len(h.levels) != 1 || h.levels[0] != 4 {
		t.Fatalf("want the target level 4, got %v", h.levels)
	}
}

// A bare <volumeUpdated/> (older captures) must report "unknown", never 0: a
// 0 would push the Spotify slider to mute.
func TestBareVolumeUpdatedReportsUnknownLevel(t *testing.T) {
	h := &volHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(`<updates deviceID="AABBCCDDEEFF"><volumeUpdated /></updates>`))
	if len(h.levels) != 1 || h.levels[0] != -1 {
		t.Fatalf("want -1 for a frame without a level, got %v", h.levels)
	}
}

func TestVolumeLevelFallsBackToActual(t *testing.T) {
	var f gabboFrame
	if err := xml.Unmarshal([]byte(`<updates><volumeUpdated><volume><actualvolume>0</actualvolume></volume></volumeUpdated></updates>`), &f); err != nil {
		t.Fatal(err)
	}
	if f.VolumeUpdated == nil || f.VolumeUpdated.level() != 0 {
		t.Fatalf("want actual 0, got %+v", f.VolumeUpdated)
	}
}
