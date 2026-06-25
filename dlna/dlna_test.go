package dlna

import "testing"

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
