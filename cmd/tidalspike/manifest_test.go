package main

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// Fixtures are synthetic: same shape as Tidal's manifests, example.com hosts,
// made-up tokens.

const btsFLAC = `{"mimeType":"audio/flac","codecs":"flac","encryptionType":"NONE","urls":["https://media.example.com/mediatracks/abc/0.flac?token=1800000000~c2lnbmF0dXJl"]}`

const dashFLAC = `<?xml version='1.0' encoding='UTF-8'?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" profiles="urn:mpeg:dash:profile:isoff-main:2011" type="static" minBufferTime="PT3.993S" mediaPresentationDuration="PT3M30.000S">
 <Period id="0">
  <AdaptationSet id="0" contentType="audio" mimeType="audio/mp4" segmentAlignment="true">
   <Representation id="FLAC,44100,16" codecs="flac" bandwidth="1015809" audioSamplingRate="44100">
    <SegmentTemplate timescale="44100" initialization="https://media.example.com/mt/abc/0.mp4?token=1800000000~sig" media="https://media.example.com/mt/abc/$Number$.mp4?token=1800000000~sig" startNumber="1">
     <SegmentTimeline>
      <S d="176128" r="51"/>
      <S d="40000"/>
     </SegmentTimeline>
    </SegmentTemplate>
   </Representation>
   <Representation id="low" codecs="flac" bandwidth="1000" audioSamplingRate="44100">
    <SegmentTemplate timescale="44100" initialization="https://media.example.com/low/0.mp4" media="https://media.example.com/low/$Number$.mp4"/>
   </Representation>
  </AdaptationSet>
 </Period>
</MPD>`

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestDecodeBTSManifestRawFLAC(t *testing.T) {
	pi := &playbackInfo{AudioQuality: "LOSSLESS", ManifestMimeType: manifestBTS, Manifest: b64(btsFLAC), BitDepth: 16, SampleRate: 44100}
	si, err := decodeManifest(pi)
	if err != nil {
		t.Fatal(err)
	}
	if si.Kind != "BTS" || si.MimeType != "audio/flac" || si.Codecs != "flac" || si.Container != "raw FLAC" {
		t.Fatalf("stream = %+v", si)
	}
	if si.Encrypted || si.Encryption != "NONE" {
		t.Fatalf("encryption = %v %q", si.Encrypted, si.Encryption)
	}
	u, ok := si.directURL()
	if !ok || !strings.HasSuffix(strings.SplitN(u, "?", 2)[0], "/0.flac") {
		t.Fatalf("directURL = %q %v", u, ok)
	}
	if si.SampleRate != 44100 {
		t.Fatalf("sample rate %d", si.SampleRate)
	}
	exp, where, ok := urlExpiry(u)
	if !ok || exp.Unix() != 1_800_000_000 || !strings.Contains(where, "token") {
		t.Fatalf("expiry = %v %q %v", exp, where, ok)
	}
	if !strings.Contains(si.verdict(), "straight to the speaker") {
		t.Fatalf("verdict = %q", si.verdict())
	}
}

func TestDecodeBTSManifestVariants(t *testing.T) {
	cases := []struct {
		name, manifest, container string
		encrypted                 bool
	}{
		{"flac in mp4", `{"mimeType":"audio/mp4","codecs":"flac","encryptionType":"NONE","urls":["https://x.example.com/a.mp4"]}`, "FLAC-in-MP4", false},
		{"aac", `{"mimeType":"audio/mp4","codecs":"mp4a.40.2","encryptionType":"NONE","urls":["https://x.example.com/a.m4a"]}`, "AAC-in-MP4", false},
		{"encrypted", `{"mimeType":"audio/flac","codecs":"flac","encryptionType":"OLD_AES","keyId":"a2V5","urls":["https://x.example.com/a.flac"]}`, "raw FLAC", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			si, err := decodeManifest(&playbackInfo{ManifestMimeType: manifestBTS, Manifest: b64(tc.manifest)})
			if err != nil {
				t.Fatal(err)
			}
			if si.Container != tc.container || si.Encrypted != tc.encrypted {
				t.Fatalf("container %q encrypted %v, want %q %v", si.Container, si.Encrypted, tc.container, tc.encrypted)
			}
			if tc.encrypted && !strings.HasPrefix(si.verdict(), "ENCRYPTED") {
				t.Fatalf("verdict = %q", si.verdict())
			}
		})
	}
}

func TestDecodeBTSManifestErrors(t *testing.T) {
	if _, err := decodeManifest(&playbackInfo{ManifestMimeType: manifestBTS, Manifest: "!!!"}); err == nil {
		t.Fatal("bad base64 accepted")
	}
	if _, err := decodeManifest(&playbackInfo{ManifestMimeType: manifestBTS, Manifest: b64(`{"mimeType":"audio/flac","urls":[]}`)}); err == nil {
		t.Fatal("empty urls accepted")
	}
	if _, err := decodeManifest(&playbackInfo{ManifestMimeType: "application/x-unknown", Manifest: b64("{}")}); err == nil {
		t.Fatal("unknown manifest type accepted")
	}
}

func TestDecodeDASHManifest(t *testing.T) {
	pi := &playbackInfo{AudioQuality: "LOSSLESS", ManifestMimeType: manifestDASH, Manifest: b64(dashFLAC)}
	si, err := decodeManifest(pi)
	if err != nil {
		t.Fatal(err)
	}
	if si.Kind != "DASH" || si.MimeType != "audio/mp4" || si.Codecs != "flac" || si.Container != "FLAC-in-fMP4" {
		t.Fatalf("stream = %+v", si)
	}
	if si.Encrypted || si.Encryption != "NONE" {
		t.Fatalf("encryption = %v %q", si.Encrypted, si.Encryption)
	}
	if si.Bandwidth != 1015809 || si.SampleRate != 44100 {
		t.Fatalf("picked representation bw=%d sr=%d", si.Bandwidth, si.SampleRate)
	}
	if si.Duration != 210*time.Second {
		t.Fatalf("duration %v", si.Duration)
	}
	// 52 segments from r=51 plus one tail segment, plus the init segment.
	if si.SegmentCount != 53 || len(si.URLs) != 54 {
		t.Fatalf("segments %d urls %d", si.SegmentCount, len(si.URLs))
	}
	if !strings.Contains(si.URLs[0], "/0.mp4") || !strings.Contains(si.URLs[1], "/1.mp4") || !strings.Contains(si.URLs[53], "/53.mp4") {
		t.Fatalf("urls: first %q second %q last %q", si.URLs[0], si.URLs[1], si.URLs[53])
	}
	if _, ok := si.directURL(); ok {
		t.Fatal("DASH must never count as a direct URL")
	}
	if !strings.Contains(si.verdict(), "segment joiner") {
		t.Fatalf("verdict = %q", si.verdict())
	}
}

func TestDecodeDASHRepeatToEndAndProtection(t *testing.T) {
	mpd := strings.Replace(dashFLAC, `<S d="176128" r="51"/>
      <S d="40000"/>`, `<S d="176128" r="-1"/>`, 1)
	mpd = strings.Replace(mpd, `segmentAlignment="true">`, `segmentAlignment="true"><ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>`, 1)
	si, err := decodeManifest(&playbackInfo{ManifestMimeType: manifestDASH, Manifest: b64(mpd)})
	if err != nil {
		t.Fatal(err)
	}
	// 210 s * 44100 / 176128 = 52.58, so 53 segments.
	if si.SegmentCount != 53 {
		t.Fatalf("segments %d, want 53", si.SegmentCount)
	}
	if !si.Encrypted || si.Encryption != "cenc" {
		t.Fatalf("encryption = %v %q", si.Encrypted, si.Encryption)
	}
}

func TestURLExpiryShapes(t *testing.T) {
	cases := []struct {
		url  string
		want int64
	}{
		{"https://cdn.example.com/a.flac?Expires=1800000001&Signature=x&Key-Pair-Id=y", 1_800_000_001},
		{"https://cdn.example.com/a.flac?hdnea=st=1799990000~exp=1800000002~acl=/*~hmac=ab", 1_800_000_002},
		{"https://cdn.example.com/a.flac?token=1800000003~c2ln", 1_800_000_003},
		{"https://cdn.example.com/a.flac", 0},
		{"https://cdn.example.com/a.flac?token=abc", 0},
	}
	for _, tc := range cases {
		got, _, ok := urlExpiry(tc.url)
		if tc.want == 0 {
			if ok {
				t.Errorf("%s: unexpected expiry %v", tc.url, got)
			}
			continue
		}
		if !ok || got.Unix() != tc.want {
			t.Errorf("%s: got %v ok=%v, want %d", tc.url, got, ok, tc.want)
		}
	}
}

func TestRedactURLDropsSignature(t *testing.T) {
	got := redactURL("https://media.example.com/mediatracks/abc/0.flac?token=1800000000~secret&x=1")
	if strings.Contains(got, "secret") || strings.Contains(got, "abc") {
		t.Fatalf("redacted = %q", got)
	}
	if got != "https://media.example.com/mediatracks/...?<2 params redacted>" {
		t.Fatalf("redacted = %q", got)
	}
}

func TestParseISODuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"PT3M30.000S": 210 * time.Second,
		"PT1H":        time.Hour,
		"PT0.5S":      500 * time.Millisecond,
		"bogus":       0,
	} {
		if got := parseISODuration(in); got != want {
			t.Errorf("%s = %v, want %v", in, got, want)
		}
	}
}
