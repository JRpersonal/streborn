// Package agentbin embeds the compiled ARM stick agent binary into
// the desktop app so a fresh installation can flash a new stick
// without a separate download.
//
// An empty stub file with the name streborn-armv7l is committed so
// that go:embed succeeds on a clean checkout. CI overwrites the
// stub with the real ARM binary built in an earlier job. On a
// developer machine the stub stays empty, Available() returns
// false, and callers must fall back to a configured external path.
//
// Local developers who want a full build can run `make wails-build`
// (or the documented manual steps) to populate the stub.
package agentbin

import (
	_ "embed"
)

//go:embed streborn-armv7l
var armBinary []byte

// Bytes returns the embedded ARM binary. Empty on dev builds where
// the stub has not been replaced.
func Bytes() []byte { return armBinary }

// Available reports whether a non-stub binary is embedded.
func Available() bool { return len(armBinary) > 0 }

// Name is the filename under which the binary is written to the
// stick.
const Name = "streborn-armv7l"
