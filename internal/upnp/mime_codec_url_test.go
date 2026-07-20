package upnp

import "testing"

// A preset stored without a codec used to be labelled with the audio/mpeg
// default, so an AAC station decoded as MPEG and played silence (#252). A field
// diagnostic showed exactly that: an AAC station saved with an empty codec,
// whose URL plainly reads "aac-64". MimeForCodecOrURL recovers those.
func TestMimeForCodecOrURL(t *testing.T) {
	cases := []struct {
		name, codec, url, want string
	}{
		{"stated AAC wins", "AAC", "http://x/stream.mp3", "audio/aac"},
		{"stated HE-AAC wins", "AAC+", "http://x/stream", "audio/aac"},
		{"stated MP3 is trusted, URL ignored", "MP3", "http://x/aac-64/s", ""},
		{"no codec, AAC in path", "", "http://streams.rsh.de/rsh-sylt/aac-64/streams.rsh.de/", "audio/aac"},
		{"no codec, .aac suffix", "", "http://x/live.aac", "audio/aac"},
		{"no codec, aacp token", "", "http://x/aacp/stream", "audio/aac"},
		{"no codec, format query", "", "http://x/s?format=aac", "audio/aac"},
		{"no codec, MP3 URL", "", "http://icecast.ndr.de/ndr/ndr2/mp3/128/stream.mp3", ""},
		{"no codec, no hint", "", "http://stream.rtlradio.de/schlagerliebe/mp3-192/", ""},
		{"must not match inside a word", "", "http://x/isaachome/stream", ""},
		{"empty everything", "", "", ""},
	}
	for _, c := range cases {
		if got := MimeForCodecOrURL(c.codec, c.url); got != c.want {
			t.Errorf("%s: MimeForCodecOrURL(%q, %q) = %q, want %q", c.name, c.codec, c.url, got, c.want)
		}
	}
}

// MimeForCodec itself must be unchanged: it is the authoritative path when the
// preset states a codec.
func TestMimeForCodecUnchanged(t *testing.T) {
	if MimeForCodec("AAC+") != "audio/aac" {
		t.Fatal("AAC+ must map to audio/aac")
	}
	if MimeForCodec("MP3") != "" {
		t.Fatal("MP3 must keep the audio/mpeg default (empty)")
	}
	if MimeForCodec("") != "" {
		t.Fatal("an absent codec must not be guessed by MimeForCodec itself")
	}
}
