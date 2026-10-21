package webui

import (
	"context"
	"testing"

	"github.com/JRpersonal/streborn/internal/upnp"
)

// #1394: the STOP key that ends a native station before a UPnP push, and the
// PLAY key that brings it back, must be stamped as STR's own KEY, not only as
// an own transport command. The firmware answers a key with the same
// userActivityUpdate a person makes, and only the key stamp tells the gabbo
// classifier that this ping is STR's echo.
func TestNativeStationKeysAreStampedAsOwnKeys(t *testing.T) {
	var keys, cmds int
	s := &Server{renderer: &upnp.Renderer{OnTransportCommand: func() { cmds++ }}}
	s.SetOwnKeyHook(func() { keys++ })

	s.stopNativeStation(context.Background())
	s.resumeNativeStation(context.Background())
	if keys != 2 {
		t.Fatalf("own-key stamps = %d, want 2 (one per key)", keys)
	}
	if cmds != 0 {
		t.Fatalf("the key hook covers the command stamp, got %d extra command stamps", cmds)
	}
}

// Without a key hook wired (tests, older wiring) the keys still stamp an own
// transport command, as before.
func TestNativeStationKeysFallBackToTheCommandStamp(t *testing.T) {
	var cmds int
	s := &Server{renderer: &upnp.Renderer{OnTransportCommand: func() { cmds++ }}}
	s.stopNativeStation(context.Background())
	if cmds != 1 {
		t.Fatalf("command stamps = %d, want 1", cmds)
	}
}
