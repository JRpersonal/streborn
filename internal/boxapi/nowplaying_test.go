package boxapi

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Golden samples: real /now_playing shapes collected from the tests and
// fixtures across the repo (cmd/agent, internal/webui, internal/boxcli),
// plus the shapes the old substring readers were written against. Every
// predicate below is run over every sample against a byte-for-byte replica
// of the reader it replaces, so the truth tables are pinned, and the few
// places where the typed reader answers differently are listed by name in
// expectedDivergences with the reason.
var nowPlayingSamples = []struct {
	name string
	body string
}{
	{"radio playing (boxprobe fixture)", `<?xml version="1.0" encoding="UTF-8" ?>
<nowPlaying deviceID="device-id-here" source="LOCAL_INTERNET_RADIO" sourceAccount="">
  <ContentItem source="LOCAL_INTERNET_RADIO" location="/station?data=abc" sourceAccount="" isPresetable="true">
    <itemName>Radio Paradise</itemName>
  </ContentItem>
  <playStatus>PLAY_STATE</playStatus>
</nowPlaying>`},
	{"standby with STOP_STATE", `<nowPlaying deviceID="device-id-here" source="STANDBY">
  <playStatus>STOP_STATE</playStatus>
</nowPlaying>`},
	{"spotify buffering", `<nowPlaying source="UPNP">
  <ContentItem source="UPNP" location="http://127.0.0.1:8888/spotify/stream"></ContentItem>
  <playStatus>BUFFERING_STATE</playStatus>
</nowPlaying>`},
	{"spotify playing", `<nowPlaying deviceID="x" source="UPNP" sourceAccount="UPnPUserName"><ContentItem source="UPNP" location="http://127.0.0.1:8888/spotify/stream?gen=3" sourceAccount="UPnPUserName" isPresetable="false"><itemName>Spotify</itemName></ContentItem><track>Spotify</track><playStatus>PLAY_STATE</playStatus></nowPlaying>`},
	{"standby bare", `<nowPlaying source="STANDBY"></nowPlaying>`},
	{"standby with ContentItem", `<nowPlaying deviceID="x" source="STANDBY"><ContentItem source="STANDBY" isPresetable="false" /></nowPlaying>`},
	{"network standby", `<nowPlaying deviceID="x" source="NETWORK_STANDBY"><ContentItem source="NETWORK_STANDBY" isPresetable="false" /></nowPlaying>`},
	{"invalid source bare", `<nowPlaying source="INVALID_SOURCE" />`},
	{"invalid source with ContentItem", `<nowPlaying deviceID="DEV#1" source="INVALID_SOURCE"><ContentItem source="INVALID_SOURCE" isPresetable="false" /></nowPlaying>`},
	{"invalid source with STOP_STATE (spotify drop)", `<nowPlaying source="INVALID_SOURCE"><playStatus>STOP_STATE</playStatus></nowPlaying>`},
	{"invalid source on a native descriptor", `<nowPlaying source="INVALID_SOURCE"><ContentItem source="LOCAL_INTERNET_RADIO" type="DO_NOT_RESUME" location="/station?data=eyJuYW1lIjoiV0RSIn0="/></nowPlaying>`},
	{"invalid source on a foreign stream", `<nowPlaying source="INVALID_SOURCE"><ContentItem source="UPNP" location="http://192.0.2.50:8095/stream/flow.mp3?token=s3cr3tsession"/></nowPlaying>`},
	{"invalid source on a library track", `<nowPlaying source="INVALID_SOURCE"><ContentItem source="UPNP" location="http://192.0.2.28:9000/disk/track-42.flac"/></nowPlaying>`},
	{"foreign stream playing", `<nowPlaying source="UPNP"><ContentItem source="UPNP" location="http://192.0.2.50:8095/stream/flow.mp3?token=s3cr3tsession"/><playStatus>PLAY_STATE</playStatus></nowPlaying>`},
	{"own stream playing", `<nowPlaying source="UPNP"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/2"/><playStatus>PLAY_STATE</playStatus></nowPlaying>`},
	{"stale play state on another stream (1036)", `<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="x" source="UPNP"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/3" /><playStatus>PLAY_STATE</playStatus></nowPlaying>`},
	{"play state without a location", `<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="x" source="UPNP"><playStatus>PLAY_STATE</playStatus></nowPlaying>`},
	{"UPNP no status", `<nowPlaying source="UPNP"/>`},
	{"UPNP play state only", `<nowPlaying deviceID="DEV#1" source="UPNP"><playStatus>PLAY_STATE</playStatus></nowPlaying>`},
	{"radio buffering", `<nowPlaying source="LOCAL_INTERNET_RADIO"><playStatus>BUFFERING_STATE</playStatus></nowPlaying>`},
	{"AUX playing", `<nowPlaying source="AUX"><playStatus>PLAY_STATE</playStatus></nowPlaying>`},
	{"AUX track only", `<nowPlaying source="AUX"><track>whatever</track></nowPlaying>`},
	{"bluetooth paused with time", `<nowPlaying deviceID="x" source="BLUETOOTH" sourceAccount=""><ContentItem source="BLUETOOTH" location="" sourceAccount="" isPresetable="false"><itemName>Phone</itemName></ContentItem><track>Song</track><artist>Band</artist><album>LP</album><time total="236">25</time><playStatus>PAUSE_STATE</playStatus></nowPlaying>`},
	{"SETUP source", `<nowPlaying deviceID="x" source="SETUP"><ContentItem source="SETUP" isPresetable="false" /></nowPlaying>`},
	{"display track with entities (#1170)", `<nowPlaying deviceID="device-id-here" source="UPNP" sourceAccount="UPnPUserName"><ContentItem source="UPNP" location="http://192.0.2.1:8888/stream/raw?u=aHR0cHM6Ly9lZGdlMy5wZXRhLmxpdmUzNjUubmV0L2IwNTA1NV8xMjhtcDM&amp;x=1" sourceAccount="UPnPUserName" isPresetable="false"><itemName>Schwarz &amp; Funk - Laid Back</itemName></ContentItem><track>Schwarz &amp; Funk - Laid Back</track><artist></artist><album></album><stationName></stationName><playStatus>PLAY_STATE</playStatus></nowPlaying>`},
	{"hold-save UPnP file with entities", `<nowPlaying deviceID="AABBCCDDEEFF" source="UPNP" sourceAccount="UPnPUserName"><ContentItem source="UPNP" location="http://192.0.2.28:50002/m/a.mp3?x=1&amp;y=2" sourceAccount="UPnPUserName" isPresetable="false"><itemName>One Alpha</itemName></ContentItem></nowPlaying>`},
	{"native Spotify Connect (#950)", `<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="DEV#c91ce962" source="SPOTIFY" sourceAccount="SpotifyConnectUserName"><ContentItem source="SPOTIFY" type="DO_NOT_RESUME" location="/playback/container/c3BvdGlmeTpwbGF5bGlzdDo1MHU4OFZqOXYwZ1RnbjM4ZGQ4WUNL" sourceAccount="SpotifyConnectUserName" isPresetable="false"><itemName>Best of Matthew west</itemName></ContentItem><track>You Changed My Name</track><artist>Matthew West</artist><album>My Story Your Glory</album><stationName></stationName><art artImageStatus="IMAGE_PRESENT">https://i.scdn.co/image/ab67616d0000b273a8b09534c0a1d25edbcfd094</art><time total="236">25</time><skipEnabled /><playStatus>PLAY_STATE</playStatus><seekSupported value="false" /><account subscriptionType="spotify-connect" /><streamType>TRACK_ONDEMAND</streamType><trackID>spotify:track:4WOx3aoT3TfmLewsB2nWat</trackID></nowPlaying>`},
	{"native radio (#593)", `<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="DEV#c91ce962" source="LOCAL_INTERNET_RADIO" sourceAccount=""><ContentItem source="LOCAL_INTERNET_RADIO" location="http://192.0.2.10:8888/stream/3" isPresetable="true"><itemName>1LIVE</itemName></ContentItem><track>1LIVE</track><artist></artist><stationName>1LIVE</stationName><playStatus>PLAY_STATE</playStatus></nowPlaying>`},
	{"Pandora (#1101)", `<?xml version="1.0" encoding="UTF-8" ?>
<nowPlaying deviceID="device-id-here" source="PANDORA" sourceAccount="listener@example.com">
  <ContentItem source="PANDORA" type="stationurl" location="4071226281950183516" sourceAccount="listener@example.com" isPresetable="true">
    <itemName>Little Big Town Radio</itemName>
    <containerArt>https://example.com/art.jpg</containerArt>
  </ContentItem>
  <track>Boondocks</track><artist>Little Big Town</artist>
  <stationName>Little Big Town Radio</stationName>
  <playStatus>PLAY_STATE</playStatus>
</nowPlaying>`},
	{"announcement clip playing", `<nowPlaying source="UPNP"><ContentItem source="UPNP" location="http://127.0.0.1:8888/announce/audio?n=4"/><playStatus>PLAY_STATE</playStatus></nowPlaying>`},
	{"track titled STANDBY (band)", `<nowPlaying source="UPNP"><ContentItem source="UPNP" location="http://a"><itemName>STANDBY (band)</itemName></ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying>`},
	{"Latin-1 umlaut in itemName", `<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="x" source="UPNP"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/1"><itemName>K` + "\xfc" + `che</itemName></ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying>`},
	{"truncated mid-document", `<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="x" source="UPNP"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/5"><itemName>Cut off</itemName></ContentItem><playStatus>BUFFERING_STATE</playStatus><art artImageStatus="IMAGE_PRESENT">https://example.com/a-very-long`},
	{"empty body", ``},
	{"whitespace body", "  \n"},
}

// Replicas of the readers this package replaced, verbatim in spirit.
var (
	legacyRootSource = regexp.MustCompile(`<nowPlaying[^>]*\bsource="([^"]*)"`)
	legacyPlayStatus = regexp.MustCompile(`<playStatus>([^<]+)</playStatus>`)
	legacyLocation   = regexp.MustCompile(`\blocation="([^"]*)"`)
	legacyHoldSource = regexp.MustCompile(`<nowPlaying[^>]*\ssource="([^"]*)"`)
	legacyHoldLoc    = regexp.MustCompile(`<ContentItem[^>]*\slocation="([^"]*)"`)
)

// webui/queueplay.go nowPlayingStandby.
func legacyQueueStandby(body string) bool {
	if m := legacyRootSource.FindStringSubmatch(body); m != nil {
		return strings.Contains(m[1], "STANDBY")
	}
	return false
}

// webui/queueplay.go nowPlayingSourceTornDown.
func legacyTornDown(body string) bool {
	if m := legacyRootSource.FindStringSubmatch(body); m != nil {
		return strings.Contains(m[1], "INVALID_SOURCE")
	}
	return false
}

// webui/queueplay.go nowPlayingStatus.
func legacyStatus(body string) string {
	if m := legacyPlayStatus.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	switch {
	case strings.Contains(body, "PLAY_STATE"):
		return "PLAY_STATE"
	case strings.Contains(body, "BUFFERING_STATE"):
		return "BUFFERING_STATE"
	case strings.Contains(body, "PAUSE_STATE"):
		return "PAUSE_STATE"
	case strings.Contains(body, "STOP_STATE"):
		return "STOP_STATE"
	}
	return ""
}

// webui/resume_standby.go boxPlayStateDetail (body-wide scans).
func legacyPlayStateDetail(body string) (standby, busy, buffering bool) {
	standby = strings.Contains(body, "STANDBY")
	buffering = strings.Contains(body, "BUFFERING_STATE")
	busy = strings.Contains(body, "PLAY_STATE") || buffering || strings.Contains(body, "PAUSE_STATE")
	return
}

// cmd/agent/boxprobe.go firstAttr / firstElem.
func legacyFirstAttr(doc, name string) string {
	key := name + `="`
	i := strings.Index(doc, key)
	if i < 0 {
		return ""
	}
	rest := doc[i+len(key):]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}

func legacyFirstElem(doc, name string) string {
	key := "<" + name + ">"
	i := strings.Index(doc, key)
	if i < 0 {
		return ""
	}
	rest := doc[i+len(key):]
	if j := strings.IndexByte(rest, '<'); j >= 0 {
		return rest[:j]
	}
	return ""
}

// cmd/agent/boxprobe.go nowPlayingIsPlaying / IsURL / IsSpotify / ReallySpotify.
func legacyIsPlaying(doc string) bool {
	return strings.Contains(doc, "PLAY_STATE") || strings.Contains(doc, "BUFFERING_STATE")
}

func legacyIsURL(doc, wantURL string) bool {
	if !legacyIsPlaying(doc) {
		return false
	}
	want := StreamPath(wantURL)
	if want == "" || !strings.Contains(doc, `location="`) {
		return true
	}
	return strings.Contains(doc, want)
}

func legacyIsSpotify(doc string) bool {
	return strings.Contains(doc, "spotify/stream") && legacyIsPlaying(doc)
}

func legacyReallySpotify(doc string) bool {
	return strings.Contains(doc, "spotify/stream") && strings.Contains(doc, "PLAY_STATE")
}

// webui/recall_verify.go boxPlayLocation's location read.
func legacyRecallLocation(body string) string {
	if m := legacyLocation.FindStringSubmatch(body); m != nil {
		r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'")
		return r.Replace(m[1])
	}
	return ""
}

// webui/resume_foreign.go contentLocations.
func legacyLocations(body string) []string {
	ms := legacyLocation.FindAllStringSubmatch(body, -1)
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m[1])
	}
	return out
}

// webui/holdsave.go parseNowPlayingSourceLocation.
func legacyHoldSourceLocation(b []byte) (source, location string, ok bool) {
	m := legacyHoldSource.FindSubmatch(b)
	if m == nil {
		return "", "", false
	}
	source = string(m[1])
	if l := legacyHoldLoc.FindSubmatch(b); l != nil {
		location = html.UnescapeString(string(l[1]))
	}
	return source, location, true
}

// expectedDivergences names the (sample, predicate) pairs where the typed
// reader deliberately answers differently from the replica, with the reason.
// Anything not listed here must agree.
var expectedDivergences = map[string]string{
	"track titled STANDBY (band)/playStateDetail.standby":  "body-wide STANDBY scan took a track title for a sleeping speaker; the typed reader reads the source attribute (queueplay's regex already did)",
	"display track with entities (#1170)/summary.itemName": "Summary decodes entities (Schwarz & Funk), the raw element scan did not; log-only field",
	"Latin-1 umlaut in itemName/summary.itemName":          "Summary repairs the Latin-1 byte to UTF-8 (Küche), the raw element scan passed the byte through; log-only field",
	"display track with entities (#1170)/isURL(http://192.0.2.1:8888/stream/raw?u=aHR0cHM6Ly9lZGdlMy5wZXRhLmxpdmUzNjUubmV0L2IwNTA1NV8xMjhtcDM&x=1)": "the substring reader compared the want path against the undecoded body, so a location with &amp; never matched a want URL with &; IsURL compares the decoded location (recall URLs are /stream/N without a query, so no live verdict changes)",
}

func TestNowPlayingGoldenAgreesWithLegacyReaders(t *testing.T) {
	seen := map[string]bool{}
	check := func(sample, pred string, legacy, got any) {
		t.Helper()
		key := sample + "/" + pred
		equal := false
		switch l := legacy.(type) {
		case []string:
			g := got.([]string)
			equal = len(l) == len(g)
			for i := 0; equal && i < len(l); i++ {
				equal = l[i] == g[i]
			}
		default:
			equal = legacy == got
		}
		if reason, listed := expectedDivergences[key]; listed {
			seen[key] = true
			if equal {
				// A listed divergence that no longer diverges is stale: keep
				// the list honest.
				t.Errorf("%s: listed as divergent (%s) but legacy and typed agree on %v", key, reason, got)
			}
			return
		}
		if !equal {
			t.Errorf("%s: legacy %v, typed %v", key, legacy, got)
		}
	}
	for _, sm := range nowPlayingSamples {
		body := sm.body
		np := ParseNowPlaying([]byte(body))

		check(sm.name, "queueStandby", legacyQueueStandby(body), np.IsStandby())
		check(sm.name, "tornDown", legacyTornDown(body), np.SourceTornDown())
		check(sm.name, "status", legacyStatus(body), np.Status())

		standby, busy, buffering := legacyPlayStateDetail(body)
		check(sm.name, "playStateDetail.standby", standby, np.IsStandby())
		check(sm.name, "playStateDetail.busy", busy, np.IsBusy())
		check(sm.name, "playStateDetail.buffering", buffering, np.IsBuffering())

		check(sm.name, "isPlaying", legacyIsPlaying(body), np.IsPlaying())
		check(sm.name, "source", legacyFirstAttr(body, "source"), np.Source)
		src, item, status := np.Summary()
		check(sm.name, "summary.source", legacyFirstAttr(body, "source"), src)
		check(sm.name, "summary.itemName", legacyFirstElem(body, "itemName"), item)
		check(sm.name, "summary.playStatus", legacyFirstElem(body, "playStatus"), status)

		for _, want := range []string{
			"http://127.0.0.1:8888/stream/1",
			"http://127.0.0.1:8888/stream/2",
			"http://127.0.0.1:8888/stream/3",
			"http://127.0.0.1:8888/stream/5",
			"http://192.0.2.10:8888/stream/3",
			"http://192.0.2.1:8888/stream/raw?u=aHR0cHM6Ly9lZGdlMy5wZXRhLmxpdmUzNjUubmV0L2IwNTA1NV8xMjhtcDM&x=1",
			"http://127.0.0.1:8888/spotify/stream",
			"https://radio.example/live.mp3",
			"",
		} {
			check(sm.name, "isURL("+want+")", legacyIsURL(body, want), np.IsURL(want))
		}
		check(sm.name, "isSpotify", legacyIsSpotify(body), np.IsSpotify())
		check(sm.name, "reallySpotify", legacyReallySpotify(body), np.ReallySpotify())
		check(sm.name, "onSpotifyStream", strings.Contains(body, "spotify/stream"), np.OnSpotifyStream())

		check(sm.name, "recallLocation", legacyRecallLocation(body), np.Location())
		check(sm.name, "locations", legacyLocations(body), np.Locations())

		hs, hl, hok := legacyHoldSourceLocation([]byte(body))
		check(sm.name, "hold.ok", hok, np.Source != "")
		if hok {
			check(sm.name, "hold.source", hs, np.Source)
			check(sm.name, "hold.location", hl, np.Location())
		}
	}
	for key := range expectedDivergences {
		if !seen[key] {
			t.Errorf("expectedDivergences lists %q but no sample/predicate produced it", key)
		}
	}
}

// The fields the substring readers never offered, pinned on the richest
// samples.
func TestParseNowPlayingFields(t *testing.T) {
	np := ParseNowPlaying([]byte(nowPlayingSamples[26].body)) // native Spotify Connect
	if !np.Parsed {
		t.Fatal("native Spotify body did not parse")
	}
	if np.DeviceID != "DEV#c91ce962" || np.Source != "SPOTIFY" || np.SourceAccount != "SpotifyConnectUserName" {
		t.Errorf("root attrs = %q %q %q", np.DeviceID, np.Source, np.SourceAccount)
	}
	ci := np.ContentItem
	if ci.Source != "SPOTIFY" || ci.Type != "DO_NOT_RESUME" || ci.IsPresetable || ci.ItemName != "Best of Matthew west" ||
		ci.Location != "/playback/container/c3BvdGlmeTpwbGF5bGlzdDo1MHU4OFZqOXYwZ1RnbjM4ZGQ4WUNL" {
		t.Errorf("ContentItem = %+v", ci)
	}
	if np.Track != "You Changed My Name" || np.Artist != "Matthew West" || np.Album != "My Story Your Glory" {
		t.Errorf("track fields = %q %q %q", np.Track, np.Artist, np.Album)
	}
	if np.Art != "https://i.scdn.co/image/ab67616d0000b273a8b09534c0a1d25edbcfd094" || np.ArtStatus != "IMAGE_PRESENT" {
		t.Errorf("art = %q status %q", np.Art, np.ArtStatus)
	}
	if !np.HasTime || np.Total != 236*time.Second || np.Position != 25*time.Second {
		t.Errorf("time = %v/%v hasTime %v", np.Position, np.Total, np.HasTime)
	}
	if np.PlayStatus != "PLAY_STATE" || !np.IsPlaying() || np.IsBuffering() || np.IsStandby() {
		t.Errorf("status verdicts off: %s", np)
	}

	pandora := ParseNowPlaying([]byte(nowPlayingSamples[28].body))
	if pandora.ContentItem.ContainerArt != "https://example.com/art.jpg" || !pandora.ContentItem.IsPresetable || pandora.StationName != "Little Big Town Radio" {
		t.Errorf("Pandora fields = %+v station %q", pandora.ContentItem, pandora.StationName)
	}

	ent := ParseNowPlaying([]byte(nowPlayingSamples[24].body))
	if ent.ContentItem.Location != "http://192.0.2.1:8888/stream/raw?u=aHR0cHM6Ly9lZGdlMy5wZXRhLmxpdmUzNjUubmV0L2IwNTA1NV8xMjhtcDM&x=1" {
		t.Errorf("entity-decoded location = %q", ent.ContentItem.Location)
	}
	if ent.ContentItem.ItemName != "Schwarz & Funk - Laid Back" {
		t.Errorf("entity-decoded itemName = %q", ent.ContentItem.ItemName)
	}

	latin := ParseNowPlaying([]byte(nowPlayingSamples[31].body))
	if !latin.Parsed || latin.ContentItem.ItemName != "Küche" {
		t.Errorf("Latin-1 body: parsed %v itemName %q", latin.Parsed, latin.ContentItem.ItemName)
	}

	cut := ParseNowPlaying([]byte(nowPlayingSamples[32].body))
	if cut.Parsed {
		t.Error("truncated body reported as parsed")
	}
	if cut.Source != "UPNP" || cut.Location() != "http://127.0.0.1:8888/stream/5" || cut.Status() != "BUFFERING_STATE" || !cut.IsPlaying() {
		t.Errorf("salvaged truncated body: %s", cut)
	}
	if !cut.IsURL("http://192.0.2.9:17008/stream/5") || cut.IsURL("http://127.0.0.1:8888/stream/1") {
		t.Error("salvaged body: IsURL verdict off")
	}

	empty := ParseNowPlaying(nil)
	if empty.Parsed || empty.Source != "" || empty.Status() != "" || empty.IsStandby() || empty.IsBusy() {
		t.Errorf("empty body: %s", empty)
	}
}

func TestNowPlayingSampleIndexesStillPointAtTheRightSamples(t *testing.T) {
	want := map[int]string{
		24: "display track with entities (#1170)",
		26: "native Spotify Connect (#950)",
		28: "Pandora (#1101)",
		31: "Latin-1 umlaut in itemName",
		32: "truncated mid-document",
	}
	for i, name := range want {
		if nowPlayingSamples[i].name != name {
			t.Errorf("sample %d is %q, want %q (TestParseNowPlayingFields indexes by position)", i, nowPlayingSamples[i].name, name)
		}
	}
}

// The client read: the body comes back raw, the deadline is the caller's,
// and a non-200 is a typed BoxError that still carries the body.
func TestClientNowPlaying(t *testing.T) {
	status := http.StatusOK
	body := nowPlayingSamples[0].body
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/now_playing" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	// The client builds http://host:8090; point its HTTP client at the test
	// server by rewriting the dial instead.
	c := New("127.0.0.1")
	c.HTTP = &http.Client{Transport: rewriteTo(srv.Listener.Addr().String())}

	np, err := c.NowPlaying(context.Background())
	if err != nil {
		t.Fatalf("NowPlaying: %v", err)
	}
	if string(np.Raw) != body {
		t.Errorf("Raw differs from the wire body")
	}
	if np.Source != "LOCAL_INTERNET_RADIO" || !np.IsPlaying() {
		t.Errorf("parsed: %s", np)
	}

	status = http.StatusInternalServerError
	body = `<errors deviceID="AABBCCDDEEFF"><error value="1019" name="SOURCE_UNAVAILABLE" severity="Unknown">boom</error></errors>`
	_, err = c.NowPlaying(context.Background())
	var be *BoxError
	if !errors.As(err, &be) || be.Status != 500 || be.Value != "1019" || be.Path != "/now_playing" {
		t.Fatalf("non-200: err = %v, want a typed BoxError with value 1019", err)
	}

	// A host that is not a bare address (the "127.0.0.1:1" the webui tests
	// use to force an instant failure) fails without touching the network.
	bad := New("127.0.0.1:1")
	if _, err := bad.NowPlaying(context.Background()); err == nil {
		t.Error("host with a port: expected an error")
	}
}

func TestGetRawKeepsTheBytes(t *testing.T) {
	const sources = `<sources deviceID="x"><sourceItem source="PANDORA" sourceAccount="listener@example.com" status="READY" isLocal="false">listener@example.com</sourceItem></sources>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sources" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(sources))
	}))
	defer srv.Close()
	c := New("127.0.0.1")
	c.HTTP = &http.Client{Transport: rewriteTo(srv.Listener.Addr().String())}
	got, err := c.GetRaw(context.Background(), "/sources")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sources {
		t.Errorf("GetRaw changed the body: %q", got)
	}
	if _, err := c.GetRaw(context.Background(), "/presets"); err == nil {
		t.Error("404 came back as success")
	}
}

// rewriteTo is a RoundTripper that sends every request to addr, whatever
// host the client put in the URL.
type rewriteTo string

func (r rewriteTo) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Host = string(r)
	return http.DefaultTransport.RoundTrip(req)
}
