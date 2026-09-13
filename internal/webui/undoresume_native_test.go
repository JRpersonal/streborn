package webui

import (
	"strings"
	"testing"
)

// The station a wake resumes is the box's own last one, and it comes back as
// LOCAL_INTERNET_RADIO: a source the SPEAKER fetches itself, not one on the
// UPnP transport. This repo already knows what that means, in handleStop's own
// words: "a station the speaker fetches itself is not on the UPnP transport, so
// the stop has to go to the box's own player".
//
// The first version of undoWakeAutoResume shipped with only renderer.Stop, so
// against a native station it sent a UPnP Stop that succeeds, reports success,
// and leaves the music playing. It silenced nothing on exactly the speakers it
// was written for.
func TestTheResumedStationIsOneTheBoxOwns(t *testing.T) {
	// Every source a wake can resume into, and whether the speaker owns it.
	for _, src := range []string{"LOCAL_INTERNET_RADIO", "SPOTIFY", "PRODUCT", "BLUETOOTH", "AUX"} {
		if !boxOwnedSource(src) {
			t.Errorf("%s is played by the speaker itself and must take the key path", src)
		}
	}
	// UPNP is STR's own push, which the UPnP transport does reach.
	for _, src := range []string{"UPNP", "", "STANDBY", "INVALID_SOURCE"} {
		if boxOwnedSource(src) {
			t.Errorf("%s must not be treated as box-owned", src)
		}
	}
}

// The order is the fix. Pinned against the source, because the alternative is a
// hardware session on a sleeping speaker for every future edit of this path.
func TestTheKeyPathIsTriedBeforeTheUpnpStop(t *testing.T) {
	src, err := readSourceFile("playback.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	fn := src[strings.Index(src, "func (s *Server) undoWakeAutoResume"):]
	fn = fn[:strings.Index(fn, "\n}\n")]

	key := strings.Index(fn, `transportKeyFallback(ctx, "STOP")`)
	upnp := strings.Index(fn, "s.renderer.Stop(ctx)")
	if key < 0 {
		t.Fatal("the speaker's own stop key is not sent at all, so a native station is never silenced")
	}
	if upnp < 0 {
		t.Fatal("the UPnP stop is gone; it is still needed for STR's own pushed streams")
	}
	if key > upnp {
		t.Error("the UPnP stop runs first; on a box-owned source it reports success and the music keeps playing")
	}
	// And the key path must end the function, or the UPnP stop runs anyway.
	after := fn[key:]
	if !strings.Contains(after[:strings.Index(after, "\n")+40], "return") {
		t.Error("the key path does not return, so the UPnP stop still runs behind it")
	}
}
