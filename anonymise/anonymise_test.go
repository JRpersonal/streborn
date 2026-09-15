// Tests for the scrubbers, moved here with them when the speaker itself
// needed them (the phone diagnostic button). They test the private
// helpers, so they have to live in the package.
package anonymise

import (
	"encoding/base64"
	"strings"
	"testing"
)

// The scrub must not eat what a bundle is actually read for. Radio station and
// preset names are free text in quotes and have to survive.
func TestScrubPII_LeavesStationNamesAlone(t *testing.T) {
	in := `preset 3 recalled: 'Radio Swiss Jazz' via UPnP, station "BBC Radio 4" queued`
	got := scrubPII(in)
	if !strings.Contains(got, "Radio Swiss Jazz") || !strings.Contains(got, "BBC Radio 4") {
		t.Fatalf("station names must survive the scrub:\n%s", got)
	}
}

// A reporter's real media-server address sat in a PUBLIC bundle on issue #844,
// inside a bundle the exporter had already anonymised. The log line's proxy host
// was correctly masked to 192.0.2.1 while the payload beside it still decoded to
// 192.168.1.120, because STR's own proxy carries the upstream as base64 and a
// regex over text cannot see into it.
//
// Third instance of the hole this file already carries comments about: a value
// that survives because it is encoded rather than written out.
func TestAnAddressHiddenInAProxyPayloadIsScrubbed(t *testing.T) {
	// Verbatim from the 844 bundle.
	const line = `url="http://192.0.2.1:8888/stream/raw?u=aHR0cDovLzE5Mi4xNjguMS4xMjA6NTAwMDIvbS9ORExOQS8yNjYxOC5mbGFj"`

	got := scrubPII(line)
	if strings.Contains(got, "192.168.1.120") {
		t.Fatalf("the address is still in plain sight: %s", got)
	}
	// The point is what it DECODES to, which is how it got out in the first place.
	m := proxyPayloadRegex.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("the proxy URL was mangled away entirely: %s", got)
	}
	dec, _, ok := decodeProxyPayload(m[2])
	if !ok {
		t.Fatalf("the rewritten payload no longer decodes: %s", got)
	}
	if strings.Contains(dec, "192.168.1.120") {
		t.Fatalf("the payload still decodes to the reporter's address: %s", dec)
	}
	// The path has to survive, or the bundle stops being evidence.
	if !strings.Contains(dec, "/m/NDLNA/26618.flac") {
		t.Fatalf("the upstream path was lost, so the log line says nothing any more: %s", dec)
	}
}

// Both encodings appear in the field, and a payload can wrap another proxy URL.
func TestProxyPayloadScrubHandlesEveryWrapperShape(t *testing.T) {
	inner := base64.RawURLEncoding.EncodeToString([]byte("http://192.168.5.9:8200/x.mp3"))
	for name, s := range map[string]string{
		"raw url": "/stream/raw?u=" + inner,
		"std":     "/stream/raw?u=" + base64.StdEncoding.EncodeToString([]byte("http://192.168.5.9:8200/x.mp3")),
		"doubled": "/stream/raw?u=" + base64.RawURLEncoding.EncodeToString([]byte("http://192.0.2.1:8888/stream/raw?u="+inner)),
	} {
		if got := scrubPII(s); strings.Contains(got, "192.168.5.9") {
			t.Fatalf("%s: address survived in the text: %s", name, got)
		} else if m := proxyPayloadRegex.FindStringSubmatch(got); m != nil {
			if dec, _, ok := decodeProxyPayload(m[2]); ok && strings.Contains(dec, "192.168.5.9") {
				t.Fatalf("%s: address survived one level down: %s", name, dec)
			}
		}
	}
}

// A payload that is not a URL is left alone. A bundle is evidence, and mangling
// a value nobody can read is worse than leaving it.
func TestANonURLPayloadIsLeftExactlyAsFound(t *testing.T) {
	for _, s := range []string{
		"/stream/raw?u=bm90LWEtdXJs", // "not-a-url"
		"/stream/raw?u=!!!notbase64!!!",
		"/stream/raw?u=",
	} {
		if got := scrubPII(s); got != scrubIdentities(s) {
			t.Fatalf("an unreadable payload was rewritten: %q -> %q", s, got)
		}
	}
}
