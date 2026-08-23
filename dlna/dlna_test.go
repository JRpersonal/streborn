package dlna

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPickPlayableRes guards the #139 fix: a DLNA server (Synology) that lists a
// transcoded res before the original must not make STR pick the transcode, which
// left the Bose renderer stuck at "stream starting". The original (DLNA.ORG_CI=0)
// HTTP audio res must win regardless of order, and a single ordinary res must be
// returned unchanged so currently-working libraries do not regress.
func TestPickPlayableRes(t *testing.T) {
	orig := didlR{
		ProtocolInfo: "http-get:*:audio/flac:DLNA.ORG_PN=FLAC;DLNA.ORG_OP=01;DLNA.ORG_CI=0",
		Value:        "http://nas:50002/orig/Songbird.flac",
	}
	transcode := didlR{
		ProtocolInfo: "http-get:*:audio/L16;rate=44100;channels=2:DLNA.ORG_CI=1;DLNA.ORG_OP=00",
		Value:        "http://nas:50002/transcode/Songbird.pcm",
	}

	// Transcode listed first: must still pick the original.
	if got := pickPlayableRes([]didlR{transcode, orig}); got.Value != orig.Value {
		t.Errorf("transcode-first: picked %q, want original %q", got.Value, orig.Value)
	}
	// Original first: unchanged.
	if got := pickPlayableRes([]didlR{orig, transcode}); got.Value != orig.Value {
		t.Errorf("original-first: picked %q, want original %q", got.Value, orig.Value)
	}
	// Single ordinary res: returned as-is (no regression for normal libraries).
	single := didlR{ProtocolInfo: "http-get:*:audio/mpeg:*", Value: "http://nas/x.mp3"}
	if got := pickPlayableRes([]didlR{single}); got.Value != single.Value {
		t.Errorf("single res: picked %q, want %q", got.Value, single.Value)
	}
	// A non-HTTP res first (e.g. internal) must lose to the HTTP audio res.
	internal := didlR{ProtocolInfo: "internal:*:audio/flac:*", Value: "file:///vol/Songbird.flac"}
	if got := pickPlayableRes([]didlR{internal, orig}); got.Value != orig.Value {
		t.Errorf("non-http-first: picked %q, want %q", got.Value, orig.Value)
	}
	// Empty list is safe.
	if got := pickPlayableRes(nil); got.Value != "" {
		t.Errorf("empty: want zero res, got %q", got.Value)
	}
}

// TestParseBrowseResponse_StripsIllegalXMLChars guards #262: a DLNA server that
// surfaced an ID3 comment/genre with a raw U+0001 control character made the
// strict XML parser reject the entire folder ("illegal character code U+0001").
// The offending character must be stripped so the rest of the listing parses.
func TestParseBrowseResponse_StripsIllegalXMLChars(t *testing.T) {
	didl := "&lt;DIDL-Lite&gt;&lt;item id=\"1\" parentID=\"0\"&gt;&lt;title&gt;Song\x01 One&lt;/title&gt;" +
		"&lt;class&gt;object.item.audioItem.musicTrack&lt;/class&gt;&lt;/item&gt;&lt;/DIDL-Lite&gt;"
	soap := "<?xml version=\"1.0\"?>\n<Envelope><Body><BrowseResponse><Result>" + didl +
		"</Result><NumberReturned>1</NumberReturned><TotalMatches>1</TotalMatches></BrowseResponse></Body></Envelope>"
	res, err := parseBrowseResponse([]byte(soap))
	if err != nil {
		t.Fatalf("parseBrowseResponse must tolerate U+0001, got error: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(res.Items))
	}
	if res.Items[0].Title != "Song One" {
		t.Errorf("title = %q, want %q (control char stripped)", res.Items[0].Title, "Song One")
	}
}

// TestDescribeServer covers the manual add-server fallback (#341): a
// valid MediaServer description must resolve to a populated Server
// with an absolute ContentDirectory control URL, and a device without
// a ContentDirectory service (or a non-XML response) must error so
// the app does not persist an unusable manual entry.
func TestDescribeServer(t *testing.T) {
	const goodXML = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <deviceType>urn:schemas-upnp-org:device:MediaServer:1</deviceType>
    <friendlyName>Test Media Server</friendlyName>
    <manufacturer>ACME</manufacturer>
    <modelName>MediaBox 3000</modelName>
    <UDN>uuid:test-1234</UDN>
    <serviceList>
      <service>
        <serviceType>urn:schemas-upnp-org:service:ContentDirectory:1</serviceType>
        <controlURL>/ctl/ContentDir</controlURL>
      </service>
    </serviceList>
  </device>
</root>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rootDesc.xml":
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write([]byte(goodXML))
		case "/no-cds.xml":
			_, _ = w.Write([]byte(`<?xml version="1.0"?><root><device><UDN>uuid:x</UDN><friendlyName>Router</friendlyName></device></root>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := DescribeServer(context.Background(), srv.URL+"/rootDesc.xml")
	if err != nil {
		t.Fatalf("DescribeServer: %v", err)
	}
	if got.UDN != "uuid:test-1234" || got.FriendlyName != "Test Media Server" {
		t.Errorf("server = %+v, want UDN uuid:test-1234 / Test Media Server", got)
	}
	if got.CDSControlURL != srv.URL+"/ctl/ContentDir" {
		t.Errorf("CDSControlURL = %q, want %q", got.CDSControlURL, srv.URL+"/ctl/ContentDir")
	}

	if _, err := DescribeServer(context.Background(), srv.URL+"/no-cds.xml"); err == nil {
		t.Error("device without ContentDirectory: want error, got nil")
	}
	if _, err := DescribeServer(context.Background(), srv.URL+"/missing.xml"); err == nil {
		t.Error("404 description: want error, got nil")
	}
}

func TestStripIllegalXMLChars(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"clean", "Hello World", "Hello World"},
		{"keeps tab/newline/cr", "a\tb\nc\rd", "a\tb\nc\rd"},
		{"drops U+0001", "Song\x01 One", "Song One"},
		{"drops vertical tab and form feed", "a\x0Bb\x0Cc", "abc"},
		{"keeps unicode", "Café é你", "Café é你"},
	} {
		if got := string(stripIllegalXMLChars([]byte(tc.in))); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestSearchCriteriaCoversArtistAndAlbum guards the #666 half that lives in this
// package: the phone remote's fast path used to ask only for dc:title, so a
// query for an artist or an album name came back empty from a server that has
// the track (measured on a FRITZ!Box 6690: the artist query went from zero hits
// to one when the criteria was widened). The desktop Library filters a loaded
// folder over title, artist and album, so the two must ask the same question.
func TestSearchCriteriaCoversArtistAndAlbum(t *testing.T) {
	// The body travels over a channel rather than a plain variable: the handler
	// runs on its own goroutine.
	bodies := make(chan string, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies <- string(b)
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		w.Write([]byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:SearchResponse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><Result></Result><NumberReturned>0</NumberReturned><TotalMatches>0</TotalMatches></u:SearchResponse></s:Body></s:Envelope>`))
	}))
	defer ts.Close()

	srv := Server{CDSControlURL: ts.URL}
	if _, err := Search(context.Background(), srv, `Adri"anne`, 10); err != nil {
		t.Fatalf("search: %v", err)
	}
	got := <-bodies
	for _, want := range []string{"dc:title contains", "upnp:artist contains", "upnp:album contains"} {
		if !strings.Contains(got, want) {
			t.Errorf("the widened criteria is missing %q: %s", want, got)
		}
	}
	// A quote the user typed must stay escaped so it cannot terminate the
	// criteria string.
	if !strings.Contains(got, `Adri\&#34;anne`) && !strings.Contains(got, `Adri\"anne`) {
		t.Errorf("a typed quote is not escaped in the criteria: %s", got)
	}

	// The narrow retry, for servers that index nothing but the title, must stay
	// title-only: it is what makes them answer at all.
	if _, err := SearchTitleOnly(context.Background(), srv, "symbol", 10); err != nil {
		t.Fatalf("title-only search: %v", err)
	}
	got = <-bodies
	if strings.Contains(got, "upnp:artist contains") {
		t.Errorf("SearchTitleOnly must not widen the criteria: %s", got)
	}
}
