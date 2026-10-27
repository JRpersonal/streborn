package boxws

import (
	"context"
	"testing"
)

// The play queue ends a track the speaker could not fetch as soon as the
// speaker says so (webui queuebadurl.go). The frame is the field one: a 3101
// AUDIO_ERROR_BAD_URL for a media-server file that answered 404.
func TestBoxErrorCallbackGetsTheBadURLFrame(t *testing.T) {
	c := newTestClient(&recHandler{})
	type got struct{ value, name, detail string }
	var seen []got
	c.SetOnBoxError(func(value, name, detail string) {
		seen = append(seen, got{value, name, detail})
	})
	c.handleMessage(context.Background(), []byte(
		`<errorUpdate><error value="3101" name="AUDIO_ERROR_BAD_URL" severity="Unknown">`+
			`AudioPath error2, reason 1</error></errorUpdate>`))
	if len(seen) != 1 {
		t.Fatalf("callback fired %d times, want once (synchronously)", len(seen))
	}
	if seen[0].value != "3101" || seen[0].name != "AUDIO_ERROR_BAD_URL" || seen[0].detail != "AudioPath error2, reason 1" {
		t.Fatalf("callback got %+v", seen[0])
	}
}

// No callback registered: an error frame is handled exactly as before.
func TestBoxErrorWithoutCallback(t *testing.T) {
	c := newTestClient(&recHandler{})
	c.handleMessage(context.Background(), []byte(
		`<errorUpdate><error value="3101" name="AUDIO_ERROR_BAD_URL" severity="Unknown"></error></errorUpdate>`))
}
