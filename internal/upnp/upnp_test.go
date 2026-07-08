package upnp

import (
	"encoding/xml"
	"strings"
	"testing"
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
