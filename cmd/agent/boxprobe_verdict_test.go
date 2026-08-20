package main

import "testing"

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

func TestNowPlayingVerdicts(t *testing.T) {
	if got := firstAttr(sampleNowPlaying, "source"); got != "LOCAL_INTERNET_RADIO" {
		t.Errorf("firstAttr source = %q", got)
	}
	src, item, status := nowPlayingSummary(sampleNowPlaying)
	if src != "LOCAL_INTERNET_RADIO" || item != "Radio Paradise" || status != "PLAY_STATE" {
		t.Errorf("nowPlayingSummary = %q, %q, %q", src, item, status)
	}
	if !nowPlayingIsPlaying(sampleNowPlaying) {
		t.Error("nowPlayingIsPlaying(sample) = false")
	}
	if nowPlayingIsPlaying(sampleStandby) {
		t.Error("nowPlayingIsPlaying(standby) = true")
	}
	if src, item, status := nowPlayingSummary(sampleStandby); src != "STANDBY" || item != "" || status != "STOP_STATE" {
		t.Errorf("nowPlayingSummary(standby) = %q, %q, %q", src, item, status)
	}
	if !nowPlayingIsSpotify(sampleSpotifyBuffering) {
		t.Error("nowPlayingIsSpotify(buffering) = false")
	}
	// The strict form refuses BUFFERING: re-pointing a box that is merely
	// buffering restarts the track.
	if nowPlayingReallySpotify(sampleSpotifyBuffering) {
		t.Error("nowPlayingReallySpotify(buffering) = true")
	}
	if nowPlayingIsSpotify(sampleNowPlaying) {
		t.Error("nowPlayingIsSpotify(radio) = true")
	}
}
