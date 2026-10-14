package webui

import (
	"strings"
	"testing"
)

// The now_playing body from #1170: the track shown on the display re-sent the
// stream with the song as its title, so the speaker reports the song as
// itemName and no station name.
const displayTrackNowPlaying = `<nowPlaying deviceID="device-id-here" source="UPNP" sourceAccount="UPnPUserName"><ContentItem source="UPNP" location="http://192.0.2.1:8888/stream/raw?u=aHR0cHM6Ly9lZGdlMy5wZXRhLmxpdmUzNjUubmV0L2IwNTA1NV8xMjhtcDM&amp;x=1" sourceAccount="UPnPUserName" isPresetable="false"><itemName>Schwarz &amp; Funk - Laid Back</itemName></ContentItem><track>Schwarz &amp; Funk - Laid Back</track><artist></artist><album></album><stationName></stationName><playStatus>PLAY_STATE</playStatus></nowPlaying>`

const displayTrackBoxURL = "http://192.0.2.1:8888/stream/raw?u=aHR0cHM6Ly9lZGdlMy5wZXRhLmxpdmUzNjUubmV0L2IwNTA1NV8xMjhtcDM&x=1"

func TestAnnotateStationNameRestoresTheStation(t *testing.T) {
	got := string(annotateStationName([]byte(displayTrackNowPlaying), displayTrackBoxURL, "Chillout Ibiza FM & Friends"))
	if !strings.Contains(got, "<stationName>Chillout Ibiza FM &amp; Friends</stationName>") {
		t.Fatalf("station not filled in: %s", got)
	}
	if !strings.Contains(got, "<itemName>Schwarz &amp; Funk - Laid Back</itemName>") {
		t.Fatal("itemName changed")
	}
}

func TestAnnotateStationNameLeavesOtherBodiesAlone(t *testing.T) {
	cases := map[string]struct{ body, url, title string }{
		"another stream": {displayTrackNowPlaying, "http://192.0.2.1:8888/stream/3", "Chillout Ibiza FM"},
		"no last play":   {displayTrackNowPlaying, "", ""},
		"speaker names it": {strings.Replace(displayTrackNowPlaying, "<stationName></stationName>", "<stationName>Own Name</stationName>", 1),
			displayTrackBoxURL, "Chillout Ibiza FM"},
		"standby": {`<nowPlaying source="STANDBY"><ContentItem source="STANDBY" isPresetable="false" /></nowPlaying>`, displayTrackBoxURL, "Chillout Ibiza FM"},
	}
	for name, c := range cases {
		if got := string(annotateStationName([]byte(c.body), c.url, c.title)); got != c.body {
			t.Errorf("%s: body changed: %s", name, got)
		}
	}
}
