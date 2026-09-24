package recent

import "testing"

// Replaying a media-server track from Recently played started it WITHOUT its
// file type, so the speaker got it through STR's endless-radio relay instead of
// fetching the file itself: the Wave labelled it internet radio, the bitrate
// read as a dash, the track never ended and the card was re-filed under Radio
// (#817).
//
// Three places dropped it. This one is the nastiest, because it means a card
// that was ever written without a mime stays broken for good: the fold fills
// CardArt, CardName, CardURL and Account when they are empty, and simply never
// looked at Mime. So the next play of the same card, carrying the mime, folded
// into the old row and the mime was thrown away again.

func TestAFoldFillsTheMimeItWasMissing(t *testing.T) {
	s := New()
	// A card written first WITHOUT the type, the state every old card is in.
	s.Add(Entry{Source: "upnp", CardKey: "nas|Album", CardName: "Album", CardURL: "http://nas/1.flac"})
	// The same card played again, this time carrying it.
	s.Add(Entry{Source: "upnp", CardKey: "nas|Album", CardName: "Album", CardURL: "http://nas/1.flac", Mime: "audio/flac"})

	got := s.All()
	if len(got) != 1 {
		t.Fatalf("entries = %d, want the second play folded into the first", len(got))
	}
	if got[0].Mime != "audio/flac" {
		t.Errorf("Mime = %q, want audio/flac: an old card can never heal otherwise", got[0].Mime)
	}
}

// Homepage was the other field the fold never filled. Same rule, same fix.
func TestAFoldFillsTheHomepageItWasMissing(t *testing.T) {
	s := New()
	s.Add(Entry{Source: "radio", CardKey: "st|NDR2", CardName: "NDR2", CardURL: "http://stream/ndr2"})
	s.Add(Entry{Source: "radio", CardKey: "st|NDR2", CardName: "NDR2", CardURL: "http://stream/ndr2", Homepage: "https://ndr.de"})

	got := s.All()
	if len(got) != 1 {
		t.Fatalf("entries = %d, want one folded row", len(got))
	}
	if got[0].Homepage != "https://ndr.de" {
		t.Errorf("Homepage = %q, want the one the later play carried", got[0].Homepage)
	}
}

// Filling an empty is not the same as overwriting a good value. A later play
// that happens to carry nothing must not wipe what the card already had.
func TestAFoldNeverClearsAMimeItAlreadyHas(t *testing.T) {
	s := New()
	s.Add(Entry{Source: "upnp", CardKey: "nas|Album", CardName: "Album", CardURL: "http://nas/1.flac", Mime: "audio/flac"})
	s.Add(Entry{Source: "upnp", CardKey: "nas|Album", CardName: "Album", CardURL: "http://nas/1.flac"})

	got := s.All()
	if len(got) != 1 {
		t.Fatalf("entries = %d, want one folded row", len(got))
	}
	if got[0].Mime != "audio/flac" {
		t.Errorf("Mime = %q, want the stored one kept", got[0].Mime)
	}
}

// A genuinely new track inside the same session still appends, so the fold fix
// must not have turned distinct tracks into one row.
func TestADistinctTrackStillAppends(t *testing.T) {
	s := New()
	s.Add(Entry{Source: "upnp", CardKey: "nas|Album", CardName: "Album", Track: "One", Mime: "audio/flac"})
	s.Add(Entry{Source: "upnp", CardKey: "nas|Album", CardName: "Album", Track: "Two", Mime: "audio/flac"})

	if got := s.All(); len(got) != 2 {
		t.Fatalf("entries = %d, want two distinct tracks", len(got))
	}
}
