package main

import (
	"context"
	"encoding/xml"
	"flag"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProtocolInfoFollowsRealMime(t *testing.T) {
	for mime, want := range map[string]string{
		"audio/flac": "http-get:*:audio/flac:*",
		"audio/mp4":  "http-get:*:audio/mp4:*",
		"":           "http-get:*:audio/mpeg:*",
	} {
		if got := protocolInfo(mime); got != want {
			t.Errorf("protocolInfo(%q) = %q, want %q", mime, got, want)
		}
	}
}

func TestBuildDIDLIsValidXMLAndEscapes(t *testing.T) {
	u := "https://media.example.com/a.flac?token=1800000000~sig&x=1"
	d := buildDIDL(didlMeta{
		Title: "Rock & <Roll>", Artist: `AC"DC`, Album: "Live", Duration: 3*time.Minute + 30*time.Second + 250*time.Millisecond,
		MimeType: "audio/flac", URL: u,
	})
	var doc struct {
		Item struct {
			Title  string `xml:"title"`
			Artist string `xml:"artist"`
			Class  string `xml:"class"`
			Res    struct {
				ProtocolInfo string `xml:"protocolInfo,attr"`
				Duration     string `xml:"duration,attr"`
				Value        string `xml:",chardata"`
			} `xml:"res"`
		} `xml:"item"`
	}
	if err := xml.Unmarshal([]byte(d), &doc); err != nil {
		t.Fatalf("DIDL is not well-formed: %v\n%s", err, d)
	}
	if doc.Item.Title != "Rock & <Roll>" || doc.Item.Artist != `AC"DC` {
		t.Fatalf("title/artist round trip: %+v", doc.Item)
	}
	if doc.Item.Class != "object.item.audioItem.musicTrack" {
		t.Fatalf("class %q", doc.Item.Class)
	}
	if doc.Item.Res.ProtocolInfo != "http-get:*:audio/flac:*" || doc.Item.Res.Value != u || doc.Item.Res.Duration != "0:03:30.250" {
		t.Fatalf("res = %+v", doc.Item.Res)
	}
}

func TestSoapEnvelopeEscapesDIDLOnce(t *testing.T) {
	u := "https://media.example.com/a.flac?a=1&b=2"
	didl := buildDIDL(didlMeta{MimeType: "audio/flac", URL: u})
	env := soapEnvelope("SetAVTransportURI", []soapArg{{"InstanceID", "0"}, {"CurrentURI", u}, {"CurrentURIMetaData", didl}})
	var doc struct {
		Body struct {
			Set struct {
				CurrentURI         string
				CurrentURIMetaData string
			} `xml:"SetAVTransportURI"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal([]byte(env), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Body.Set.CurrentURI != u {
		t.Fatalf("CurrentURI %q", doc.Body.Set.CurrentURI)
	}
	if doc.Body.Set.CurrentURIMetaData != didl {
		t.Fatalf("metadata did not round-trip:\n%s\n%s", doc.Body.Set.CurrentURIMetaData, didl)
	}
}

// fakeSpeaker records the order of volume and AVTransport calls.
type fakeSpeaker struct {
	mu       sync.Mutex
	calls    []string
	setBody  string
	playingN int // GetTransportInfo answers PLAYING from this call on
	infoN    int
}

func (f *fakeSpeaker) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/volume":
			if string(body) != "<volume>5</volume>" {
				t.Errorf("volume body %q", body)
			}
			f.calls = append(f.calls, "volume")
		case "/AVTransport/Control":
			action := r.Header.Get("SOAPACTION")
			name := action[strings.Index(action, "#")+1 : len(action)-1]
			f.calls = append(f.calls, name)
			switch name {
			case "SetAVTransportURI":
				f.setBody = string(body)
			case "GetTransportInfo":
				f.infoN++
				state := "TRANSITIONING"
				if f.infoN >= f.playingN {
					state = "PLAYING"
				}
				_, _ = io.WriteString(w, `<s:Envelope><s:Body><u:GetTransportInfoResponse><CurrentTransportState>`+state+`</CurrentTransportState><CurrentTransportStatus>OK</CurrentTransportStatus></u:GetTransportInfoResponse></s:Body></s:Envelope>`)
			case "GetPositionInfo":
				_, _ = io.WriteString(w, `<s:Envelope><s:Body><u:GetPositionInfoResponse><RelTime>0:00:0`+string(rune('0'+len(f.calls)%10))+`</RelTime></u:GetPositionInfoResponse></s:Body></s:Envelope>`)
			}
		default:
			http.NotFound(w, r)
		}
	})
}

func TestPlayDirectSequence(t *testing.T) {
	f := &fakeSpeaker{playingN: 3}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	sp := &speaker{
		ControlURL: srv.URL + "/AVTransport/Control",
		WebBase:    srv.URL,
		HTTP:       srv.Client(),
		Sleep:      func(context.Context, time.Duration) error { return nil },
		PollEvery:  time.Second,
	}
	u := "https://media.example.com/a.flac?token=1800000000~sig&x=1"
	rep, err := sp.playDirect(context.Background(), didlMeta{Title: "T", MimeType: "audio/flac", URL: u}, 15*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"volume", "SetAVTransportURI", "Play", "GetTransportInfo", "GetTransportInfo", "GetTransportInfo", "volume", "GetPositionInfo", "GetPositionInfo"}
	if strings.Join(f.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls\n got %v\nwant %v", f.calls, want)
	}
	if !rep.ReachedPlaying || rep.FinalState != "PLAYING" || len(rep.Positions) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	// The DIDL inside the SOAP body carries the real mime as protocolInfo.
	meta := html.UnescapeString(xmlField(f.setBody, "CurrentURIMetaData"))
	if !strings.Contains(meta, `protocolInfo="http-get:*:audio/flac:*"`) {
		t.Fatalf("metadata without flac protocolInfo: %s", meta)
	}
	if got := html.UnescapeString(xmlField(f.setBody, "CurrentURI")); got != u {
		t.Fatalf("CurrentURI %q", got)
	}
}

func TestPlayDirectReassertsVolumeEvenWithoutPlaying(t *testing.T) {
	f := &fakeSpeaker{playingN: 1000}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	sp := &speaker{ControlURL: srv.URL + "/AVTransport/Control", WebBase: srv.URL, HTTP: srv.Client(),
		Sleep: func(context.Context, time.Duration) error { return nil }, PollEvery: time.Second}
	rep, err := sp.playDirect(context.Background(), didlMeta{MimeType: "audio/flac", URL: "https://media.example.com/a.flac"}, 2*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ReachedPlaying {
		t.Fatal("reported PLAYING")
	}
	vol := 0
	for _, c := range f.calls {
		if c == "volume" {
			vol++
		}
	}
	if vol != 2 {
		t.Fatalf("volume calls %d, want 2 (%v)", vol, f.calls)
	}
}

func TestProbeSniffsContainer(t *testing.T) {
	bodies := map[string][]byte{
		"/a.flac": append([]byte("fLaC"), make([]byte, 60)...),
		"/a.mp4":  append([]byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', '6'}, make([]byte, 52)...),
		"/a.aac":  {0xFF, 0xF1, 0x50, 0x80},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-63" {
			t.Errorf("range header %q", r.Header.Get("Range"))
		}
		b := bodies[r.URL.Path]
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", "bytes 0-63/31415926")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	for path, want := range map[string]string{"/a.flac": "FLAC (fLaC)", "/a.mp4": "MP4 (ftyp iso6)", "/a.aac": "ADTS AAC"} {
		pr, err := probeURL(context.Background(), srv.Client(), srv.URL+path)
		if err != nil {
			t.Fatal(err)
		}
		if pr.Magic != want || pr.TotalSize != 31415926 || pr.AcceptRanges != "bytes" || pr.Status != 206 {
			t.Errorf("%s: %+v", path, pr)
		}
	}
}

func TestCheckSpeakerInterlock(t *testing.T) {
	cases := []struct {
		ip, allowed string
		ok          bool
	}{
		{"192.0.2.1", "192.0.2.1", true},
		{"192.0.2.2", "192.0.2.1", false},
		{"192.0.2.1", "", false},
		{"", "192.0.2.1", false},
		{"speaker.local", "speaker.local", false},
	}
	for _, tc := range cases {
		if err := checkSpeaker(tc.ip, tc.allowed); (err == nil) != tc.ok {
			t.Errorf("checkSpeaker(%q, %q) = %v, want ok=%v", tc.ip, tc.allowed, err, tc.ok)
		}
	}
}

func TestParseInterleaved(t *testing.T) {
	for _, args := range [][]string{
		{"12345", "--quality", "HIGH", "--probe"},
		{"--quality", "HIGH", "12345", "--probe"},
		{"--probe", "--quality=HIGH", "12345"},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		q := fs.String("quality", "LOSSLESS", "")
		p := fs.Bool("probe", false, "")
		pos, err := parseInterleaved(fs, args)
		if err != nil {
			t.Fatal(err)
		}
		if len(pos) != 1 || pos[0] != "12345" || *q != "HIGH" || !*p {
			t.Errorf("%v: pos %v quality %s probe %v", args, pos, *q, *p)
		}
	}
}

func TestNormQuality(t *testing.T) {
	if q, err := normQuality("hi_res_lossless"); err != nil || q != "HI_RES_LOSSLESS" {
		t.Fatalf("%q %v", q, err)
	}
	if _, err := normQuality("MASTER"); err == nil {
		t.Fatal("MASTER accepted")
	}
}
