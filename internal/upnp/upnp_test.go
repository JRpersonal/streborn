package upnp

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func TestXMLEscape(t *testing.T) {
	if got := xmlEscape(`a & b < c > d`); got != `a &amp; b &lt; c &gt; d` {
		t.Errorf("xmlEscape: %q", got)
	}
	if got := xmlEscapeAttr(`x="y"&'z'`); got != `x=&quot;y&quot;&amp;&apos;z&apos;` {
		t.Errorf("xmlEscapeAttr: %q", got)
	}
}

// buildDIDL output is handed to the Bose renderer; it must stay well-formed XML
// even when the stream URL or title carry XML metacharacters (an & in a CDN
// query string is the common case), and it must not leak a raw & that would
// make the renderer reject the SetAVTransportURI metadata.
func TestBuildDIDLMimeWellFormed(t *testing.T) {
	got := buildDIDLMime(
		"http://cdn.example/stream?a=1&b=2",
		"Rock & Roll <Live>",
		"http://logo.example/a&b.jpg",
		"audio/ogg",
		TrackMeta{},
	)
	if err := xml.Unmarshal([]byte(got), new(struct{})); err != nil {
		t.Fatalf("DIDL is not well-formed XML: %v\n%s", err, got)
	}
	if strings.Contains(got, "a=1&b=2") {
		t.Errorf("raw ampersand leaked into DIDL:\n%s", got)
	}
	if !strings.Contains(got, "http-get:*:audio/ogg:*") {
		t.Errorf("mime not propagated into res protocolInfo:\n%s", got)
	}
	if !strings.Contains(got, "albumArtURI") {
		t.Errorf("iconURL not embedded as albumArtURI:\n%s", got)
	}
}

// MimeForCodec must label the whole AAC family audio/aac (an AAC station
// labelled audio/mpeg plays silence, #252) and leave everything else on the
// audio/mpeg default ("" = caller keeps PlayURL).
func TestMimeForCodec(t *testing.T) {
	cases := map[string]string{
		"AAC":        "audio/aac",
		"AAC+":       "audio/aac",
		"aacp":       "audio/aac",
		"HE-AAC":     "audio/aac",
		"audio/aac":  "audio/aac",
		"audio/aacp": "audio/aac",
		" aac ":      "audio/aac",
		"MP3":        "",
		"mp3":        "",
		"OGG":        "",
		"FLAC":       "",
		"UNKNOWN":    "",
		"":           "",
	}
	for codec, want := range cases {
		if got := MimeForCodec(codec); got != want {
			t.Errorf("MimeForCodec(%q) = %q, want %q", codec, got, want)
		}
	}
}

func TestBuildDIDLDefaults(t *testing.T) {
	got := buildDIDL("http://x/y", "", "")
	if !strings.Contains(got, "<dc:title>Stream</dc:title>") {
		t.Errorf("empty title should default to Stream:\n%s", got)
	}
	if strings.Contains(got, "albumArtURI") {
		t.Errorf("no icon should mean no albumArtURI:\n%s", got)
	}
	if !strings.Contains(got, "audio/mpeg") {
		t.Errorf("default mime should be audio/mpeg:\n%s", got)
	}
}

// A finite file has to reach the speaker as a finite file. Without the duration
// the firmware reports a total time of zero and treats the track as an
// open-ended stream: no length in the app, no position to return to after a
// pause, and the queue left guessing when the track ended. Measured against a
// Portable: the duration attribute alone turned total=0 into total=18.
func TestBuildDIDLMimeCarriesTrackLengthAndSeekability(t *testing.T) {
	got := buildDIDLMime(
		"http://nas.example/m/15.mp3", "One Alpha", "", "audio/mpeg",
		TrackMeta{Duration: 3*time.Minute + 25*time.Second, Seekable: true},
	)
	if err := xml.Unmarshal([]byte(got), new(struct{})); err != nil {
		t.Fatalf("DIDL is not well-formed XML: %v\n%s", err, got)
	}
	if !strings.Contains(got, `duration="0:03:25.000"`) {
		t.Errorf("track length missing from the res element:\n%s", got)
	}
	if !strings.Contains(got, "DLNA.ORG_OP=01") {
		t.Errorf("range-seek flag missing from protocolInfo:\n%s", got)
	}
	// A DLNA.ORG_PN profile is format-specific and a wrong one can get the item
	// refused outright, so STR must not guess one.
	if strings.Contains(got, "DLNA.ORG_PN") {
		t.Errorf("a guessed DLNA profile name leaked into protocolInfo:\n%s", got)
	}
}

// Radio is the other half of the contract: no length, no seeking, and the
// metadata must stay byte-for-byte what it always was.
func TestBuildDIDLMimeLeavesStreamsAsStreams(t *testing.T) {
	got := buildDIDLMime("http://cdn.example/live", "1LIVE", "", "audio/mpeg", TrackMeta{})
	if !strings.Contains(got, "http-get:*:audio/mpeg:*") {
		t.Errorf("a stream must keep the wildcard protocolInfo:\n%s", got)
	}
	if strings.Contains(got, "duration=") || strings.Contains(got, "DLNA.ORG") {
		t.Errorf("a stream must carry neither a length nor DLNA flags:\n%s", got)
	}
}

func TestClockString(t *testing.T) {
	for _, c := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0:00:00.000"},
		{5 * time.Second, "0:00:05.000"},
		{75 * time.Second, "0:01:15.000"},
		{2*time.Hour + 3*time.Minute + 4*time.Second + 500*time.Millisecond, "2:03:04.500"},
		{-1 * time.Second, "0:00:00.000"},
	} {
		if got := clockString(c.in); got != c.want {
			t.Errorf("clockString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
