package main

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// A trimmed but shape-faithful now_playing document as the firmware emits it.
const sampleNowPlaying = `<?xml version="1.0" encoding="UTF-8" ?>
<nowPlaying deviceID="device-id-here" source="LOCAL_INTERNET_RADIO" sourceAccount="">
  <ContentItem source="LOCAL_INTERNET_RADIO" location="/station?data=abc" sourceAccount="" isPresetable="true">
    <itemName>Radio Paradise</itemName>
  </ContentItem>
  <playStatus>PLAY_STATE</playStatus>
</nowPlaying>`

const sampleStandby = `<nowPlaying deviceID="device-id-here" source="STANDBY">
  <playStatus>STOP_STATE</playStatus>
</nowPlaying>`

const sampleSpotifyBuffering = `<nowPlaying source="UPNP">
  <ContentItem source="UPNP" location="http://127.0.0.1:8888/spotify/stream"></ContentItem>
  <playStatus>BUFFERING_STATE</playStatus>
</nowPlaying>`

// The probes in boxprobe.go are boxapi verdicts over one read; this pins the
// ones the recall verify and the memory guard depend on, over the shapes
// they were written against.
func TestNowPlayingVerdicts(t *testing.T) {
	radio := boxapi.ParseNowPlaying([]byte(sampleNowPlaying))
	if radio.Source != "LOCAL_INTERNET_RADIO" {
		t.Errorf("source = %q", radio.Source)
	}
	src, item, status := radio.Summary()
	if src != "LOCAL_INTERNET_RADIO" || item != "Radio Paradise" || status != "PLAY_STATE" {
		t.Errorf("Summary = %q, %q, %q", src, item, status)
	}
	if !radio.IsPlaying() {
		t.Error("IsPlaying(sample) = false")
	}
	standby := boxapi.ParseNowPlaying([]byte(sampleStandby))
	if standby.IsPlaying() {
		t.Error("IsPlaying(standby) = true")
	}
	if src, item, status := standby.Summary(); src != "STANDBY" || item != "" || status != "STOP_STATE" {
		t.Errorf("Summary(standby) = %q, %q, %q", src, item, status)
	}
	buffering := boxapi.ParseNowPlaying([]byte(sampleSpotifyBuffering))
	if !buffering.IsSpotify() {
		t.Error("IsSpotify(buffering) = false")
	}
	// The strict form refuses BUFFERING: re-pointing a box that is merely
	// buffering restarts the track.
	if buffering.ReallySpotify() {
		t.Error("ReallySpotify(buffering) = true")
	}
	if radio.IsSpotify() {
		t.Error("IsSpotify(radio) = true")
	}
}
