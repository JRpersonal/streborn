package anonymise

import (
	"strings"
	"testing"
)

// A preset's stream URL can carry its login; none of it may reach a bundle.
func TestScrubURLCredentials(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"http://stream.example.com:8000/live?username=jdoe&password=hunter2&type=.mp3",
			"http://stream.example.com:8000/live?username=***&password=***&type=.mp3",
		},
		{
			// inside JSON the ampersand is escaped
			`{"location":"http://stream.example.com/live?user=jdoe&pass=hunter2&x=1"}`,
			`{"location":"http://stream.example.com/live?user=***&pass=***&x=1"}`,
		},
		{
			// inside XML it is an entity
			`<location>http://stream.example.com/a?token=abc123&amp;b=2</location>`,
			`<location>http://stream.example.com/a?token=***&amp;b=2</location>`,
		},
		{
			"location=https://jdoe:hunter2@radio.example.org/stream.aac slot=6",
			"location=https://***@radio.example.org/stream.aac slot=6",
		},
		{
			// a plain stream URL stays exactly as it was
			"http://stream.example.com/radio.mp3?bitrate=128&format=aac",
			"http://stream.example.com/radio.mp3?bitrate=128&format=aac",
		},
	}
	for _, c := range cases {
		if got := scrubURLCredentials(c.in); got != c.want {
			t.Errorf("scrubURLCredentials(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
	// and the shared pass runs it
	if out := ScrubPII("preset slot=6 location=http://s.example.com/x?password=hunter2"); strings.Contains(out, "hunter2") {
		t.Fatalf("ScrubPII leaves the password: %q", out)
	}
}
