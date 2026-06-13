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
