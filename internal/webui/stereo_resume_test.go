package webui

// Pairing into silence (#705): the master was in standby from a preceding
// dissolve, the partner was audibly playing a preset, and formStereoPair drove
// /addGroup with no wake and no resume capture. The firmware synced the fresh
// pair to the standby master, dragged the playing partner into standby one
// second after pairing (partner log 2026-08-24 18:50:39), and the user reported
// "stereo werkt niet" although both /addGroup calls had succeeded. These tests
// pin the two new pieces: the partner capture (what the pair should play when
// only the partner was playing) and the resume guard semantics that let a
// partner-derived capture actually fire on a master that never played.

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// harrieLocation builds the ContentItem location shape from the #705 bundle:
// a native LOCAL_INTERNET_RADIO selection whose base64 JSON payload carries
// the partner's own loopback stream proxy and art proxy.
func harrieLocation(t *testing.T, streamURL, name, imageURL string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"imageUrl": imageURL, "isRealtime": true, "name": name,
		"streamType": "liveRadio", "streamUrl": streamURL,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return "/station?data=" + base64.RawURLEncoding.EncodeToString(payload)
}

// The #705 case itself: the partner plays a native radio preset whose stream
// and artwork live on the partner's OWN loopback. The capture must decode the
// station and rewrite both URLs to the partner's LAN address on the :17008
// redirect, the route every box can reach another's stream proxy on.
func TestPartnerResumeForPairDecodesTheNativeStationAndRewritesToThePartnersProxy(t *testing.T) {
	np := nowPlayingSnapshot{
		Source:     "LOCAL_INTERNET_RADIO",
		Location:   harrieLocation(t, "http://127.0.0.1:8888/stream/2", "Oude Piraten Hits", "http://127.0.0.1:8888/art?u=abc"),
		ItemName:   "Oude Piraten Hits",
		PlayStatus: "PLAY_STATE",
	}
	lp := partnerResumeForPair(np, "192.0.2.33")
	if lp == nil {
		t.Fatal("a playing partner yielded no capture, so the pair would form into silence again")
	}
	if lp.boxURL != "http://192.0.2.33:17008/stream/2" {
		t.Fatalf("stream URL = %q, want the partner's LAN proxy on :17008", lp.boxURL)
	}
	if lp.title != "Oude Piraten Hits" {
		t.Fatalf("title = %q, want the station name from the location payload", lp.title)
	}
	if lp.art != "http://192.0.2.33:17008/art?u=abc" {
		t.Fatalf("art = %q, want the partner's art proxy on :17008", lp.art)
	}
}

// A UPnP push carries the stream URL directly in the location. When it is
// already externally reachable (the partner was a mirror slave pulling another
// box's proxy) it must be kept as it is, not rewritten.
func TestPartnerResumeForPairKeepsAnExternallyReachableStream(t *testing.T) {
	np := nowPlayingSnapshot{
		Source:     "UPNP",
		Location:   "http://192.0.2.31:17008/stream/1",
		ItemName:   "Radio X",
		PlayStatus: "BUFFERING_STATE",
	}
	lp := partnerResumeForPair(np, "192.0.2.33")
	if lp == nil {
		t.Fatal("a buffering partner is about to play and must be captured")
	}
	if lp.boxURL != "http://192.0.2.31:17008/stream/1" {
		t.Fatalf("stream URL = %q, want it unchanged", lp.boxURL)
	}
	if lp.title != "Radio X" {
		t.Fatalf("title = %q, want the item name", lp.title)
	}
}

// A partner that is not audibly playing has nothing worth restarting on the
// pair: standby, stopped, and unreadable must all yield nil, or the pairing
// would resurrect a stream the user had already stopped.
func TestPartnerResumeForPairStandsDownWhenThePartnerIsNotPlaying(t *testing.T) {
	loc := "http://127.0.0.1:8888/stream/2"
	cases := map[string]nowPlayingSnapshot{
		"standby":    {Source: "STANDBY"},
		"stopped":    {Source: "LOCAL_INTERNET_RADIO", Location: loc, PlayStatus: "STOP_STATE"},
		"paused":     {Source: "UPNP", Location: loc, PlayStatus: "PAUSE_STATE"},
		"unreadable": {},
	}
	for name, np := range cases {
		if lp := partnerResumeForPair(np, "192.0.2.33"); lp != nil {
			t.Errorf("%s partner produced a capture: %+v", name, lp)
		}
	}
}

// A selection with no URL this box could push (native Spotify, Bluetooth, AUX)
// must not block the pairing, only the resume: nil, quietly.
func TestPartnerResumeForPairRefusesAnUnpushableSelection(t *testing.T) {
	np := nowPlayingSnapshot{
		Source:     "SPOTIFY",
		Location:   "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M",
		ItemName:   "Some Playlist",
		PlayStatus: "PLAY_STATE",
	}
	if lp := partnerResumeForPair(np, "192.0.2.33"); lp != nil {
		t.Fatalf("an unpushable selection produced a capture: %+v", lp)
	}
}

// Older agents stored the standard base64 alphabet in preset locations; the
// decoder must accept it, exactly as handleOrionStation does.
func TestDecodeOrionStationLocationAcceptsTheStandardAlphabet(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"name": "WDR 4", "streamUrl": "http://127.0.0.1:8888/stream/4",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	loc := "/station?data=" + base64.StdEncoding.EncodeToString(payload)
	stream, name, _, ok := decodeOrionStationLocation(loc)
	if !ok || stream != "http://127.0.0.1:8888/stream/4" || name != "WDR 4" {
		t.Fatalf("decode = %q %q ok=%v, want the payload back", stream, name, ok)
	}
}

// The guard that makes the partner capture usable at all: the master in #705
// had NO lastPlay of its own (fresh agent, never played), so the old
// resumeIsStale check, which reads a nil current entry as "nothing to resume
// anymore", would have silently dropped the partner's stream. No entry at
// capture time and no entry at resume time means nothing superseded anything:
// the push must fire.
func TestResumeAfterZoneFormPartnerCaptureFiresOnAMasterThatNeverPlayed(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.playStateFn = func() (bool, bool) { return false, false } // idle after the wake
	s.resumeAfterZoneForm(zoneResume{
		push: lastPlayInfo{boxURL: "http://192.0.2.33:17008/stream/2", title: "Oude Piraten Hits", ts: time.Now()},
		ref:  nil, // the master had no lastPlay when the capture was taken
		// A stereo pair is one logical device, so a surviving master stream
		// serves both channels and the skip stays on.
		survivorReachesMembers: true,
	})
	if !rec.has("SetAVTransportURI") {
		t.Fatalf("the partner's stream was captured but never pushed to the pair: %v", rec.list())
	}
}

// The other half of the same guard: a user play that lands on the master
// between the capture and the resume outranks the capture. The live lastPlay
// no longer matches the reference, so the push stands down.
func TestResumeAfterZoneFormStandsDownWhenANewerPlaySupersededTheCapture(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.playStateFn = func() (bool, bool) { return false, false } // idle
	newer := lastPlayInfo{boxURL: "http://127.0.0.1:8888/stream/5", title: "user's pick", ts: time.Now()}
	s.lastPlayMu.Lock()
	s.lastPlay = &newer
	s.lastPlayMu.Unlock()
	s.resumeAfterZoneForm(zoneResume{
		push: lastPlayInfo{boxURL: "http://192.0.2.33:17008/stream/2", title: "captured", ts: time.Now().Add(-time.Minute)},
		ref:  nil, // nothing was on the master when the capture was taken
	})
	if rec.count() != 0 {
		t.Fatalf("a newer user play was clobbered by the pairing resume: %v", rec.list())
	}
}
