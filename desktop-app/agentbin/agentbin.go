// Package agentbin embeds the compiled ARM stick agent binary AND the
// go-librespot Spotify sidecar into the desktop app so a fresh
// installation can flash a stick (and a later OTA can deliver the
// sidecar) without a separate download.
//
// Empty stub files named streborn-armv7l and go-librespot-armv7l are
// committed so that go:embed succeeds on a clean checkout. CI overwrites
// each stub with the real ARM binary built in an earlier job. On a
// developer machine the stubs stay empty, Available()/GoLibrespotAvailable()
// return false, and callers must fall back to a configured external path.
//
// Local developers who want a full build can run `make wails-build`
// (or the documented manual steps) to populate the agent stub; the
// go-librespot stub is populated by CI from the go-librespot build.
package agentbin

import (
	_ "embed"
)

//go:embed streborn-armv7l
var armBinary []byte

//go:embed go-librespot-armv7l
var goLibrespotBinary []byte

// Bytes returns the embedded ARM binary. Empty on dev builds where
// the stub has not been replaced.
func Bytes() []byte { return armBinary }

// Available reports whether a non-stub binary is embedded.
func Available() bool { return len(armBinary) > 0 }

// Name is the filename under which the binary is written to the
// stick.
const Name = "streborn-armv7l"

// GoLibrespotBytes returns the embedded go-librespot Spotify sidecar
// (ARM, static musl). Empty on dev builds where the stub has not been
// replaced by CI.
func GoLibrespotBytes() []byte { return goLibrespotBinary }

// GoLibrespotAvailable reports whether a real go-librespot binary is
// embedded (so the stick writer / OTA can deliver it).
func GoLibrespotAvailable() bool { return len(goLibrespotBinary) > 0 }

// GoLibrespotName is the filename under which the sidecar is written to
// the stick, matching what usb-stick/run.sh syncs to NAND and what the
// agent runs (/mnt/nv/streborn/bin/go-librespot).
const GoLibrespotName = "go-librespot"
