package webui

import (
	"regexp"
	"strings"
	"testing"
)

// shorty310's capture, 2026-09-14 22:00, verbatim from the diagnostic bundle on
// discussion #950. SoundTouch 10, firmware 27.0.6, the Bose cloud gone, a FREE
// Spotify account, and the playlist started from the phone rather than from
// STR. The speaker's OWN Connect receiver names the song, the artist, the album
// and the cover, while <itemName> holds the playlist. Twice I told this
// reporter that the song could not be shown here; this response is why that was
// wrong.
const nativeSpotifyNowPlaying = `<?xml version="1.0" encoding="UTF-8" ?>` +
	`<nowPlaying deviceID="DEV#c91ce962" source="SPOTIFY" sourceAccount="SpotifyConnectUserName">` +
	`<ContentItem source="SPOTIFY" type="DO_NOT_RESUME" location="/playback/container/c3BvdGlmeTpwbGF5bGlzdDo1MHU4OFZqOXYwZ1RnbjM4ZGQ4WUNL" ` +
	`sourceAccount="SpotifyConnectUserName" isPresetable="false"><itemName>Best of Matthew west</itemName></ContentItem>` +
	`<track>You Changed My Name</track><artist>Matthew West</artist><album>My Story Your Glory</album>` +
	`<stationName></stationName><art artImageStatus="IMAGE_PRESENT">https://i.scdn.co/image/ab67616d0000b273a8b09534c0a1d25edbcfd094</art>` +
	`<time total="236">25</time><skipEnabled /><playStatus>PLAY_STATE</playStatus>` +
	`<seekSupported value="false" /><account subscriptionType="spotify-connect" />` +
	`<streamType>TRACK_ONDEMAND</streamType><trackID>spotify:track:4WOx3aoT3TfmLewsB2nWat</trackID></nowPlaying>`

// A radio station played by the speaker itself. Here <track> is NOT the song,
// it repeats the station, which is why the phone page has always had to get the
// song from STR's stream proxy instead (#593). This is the response that makes
// "just read <track>" the wrong rule, so both shapes are pinned together.
const nativeRadioNowPlaying = `<?xml version="1.0" encoding="UTF-8" ?>` +
	`<nowPlaying deviceID="DEV#c91ce962" source="LOCAL_INTERNET_RADIO" sourceAccount="">` +
	`<ContentItem source="LOCAL_INTERNET_RADIO" location="http://192.0.2.10:8888/stream/3" isPresetable="true">` +
	`<itemName>1LIVE</itemName></ContentItem>` +
	`<track>1LIVE</track><artist></artist><stationName>1LIVE</stationName>` +
	`<playStatus>PLAY_STATE</playStatus></nowPlaying>`

// The three reads the page performs, kept in step with the page by this test.
// They live here rather than being imported because the page is a single HTML
// file the agent serves; what matters is the SHAPE of the decision.
var (
	reSource = regexp.MustCompile(`source="([^"]+)"`)
	reTrack  = regexp.MustCompile(`<track>([^<]*)</track>`)
	reArtist = regexp.MustCompile(`<artist>([^<]*)</artist>`)
)

func songFromBox(xml string) string {
	src := ""
	if m := reSource.FindStringSubmatch(xml); m != nil {
		src = strings.ToUpper(m[1])
	}
	// Only the speaker's own Spotify receiver. Never in general.
	if src != "SPOTIFY" {
		return ""
	}
	track := ""
	if m := reTrack.FindStringSubmatch(xml); m != nil {
		track = m[1]
	}
	if track == "" {
		return ""
	}
	if m := reArtist.FindStringSubmatch(xml); m != nil && m[1] != "" {
		return m[1] + " · " + track
	}
	return track
}

func TestTheSpeakersOwnSpotifySourceNamesTheSong(t *testing.T) {
	if got, want := songFromBox(nativeSpotifyNowPlaying), "Matthew West · You Changed My Name"; got != want {
		t.Errorf("song = %q, want %q", got, want)
	}
}

// The trap this rule has to survive: a station whose <track> is the station
// name. Reading it would put "1LIVE" where the song belongs, under a heading
// that already says 1LIVE.
func TestARadioStationDoesNotGetItsNameShownTwice(t *testing.T) {
	if got := songFromBox(nativeRadioNowPlaying); got != "" {
		t.Errorf("song = %q, want nothing: on radio <track> is the station, not the song", got)
	}
}

func TestASilentOrOddResponseYieldsNoSong(t *testing.T) {
	for _, xml := range []string{
		`<nowPlaying source="STANDBY"></nowPlaying>`,
		`<nowPlaying source="SPOTIFY"><track></track></nowPlaying>`, // playing nothing yet
		`<nowPlaying source="AUX"><track>whatever</track></nowPlaying>`,
		``,
	} {
		if got := songFromBox(xml); got != "" {
			t.Errorf("song = %q for %q, want nothing", got, xml)
		}
	}
}

// The page must actually do this, or the test above is pinning a rule nothing
// follows.
func TestThePhonePageReadsTheSongFromTheSpeakersOwnSpotifySource(t *testing.T) {
	page := readIndexHTML(t)
	if !strings.Contains(page, "upSrc === 'SPOTIFY'") {
		t.Fatal("the page no longer special-cases the speaker's own Spotify source; if it moved, move this test with it")
	}
	// And it must stay gated on that source: applying it to everything is the
	// radio trap above.
	i := strings.Index(page, "upSrc === 'SPOTIFY'")
	window := page[i:min(i+600, len(page))]
	if !strings.Contains(window, "<track>") || !strings.Contains(window, "<artist>") {
		t.Errorf("the Spotify branch no longer reads track and artist: %s", window[:min(300, len(window))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
