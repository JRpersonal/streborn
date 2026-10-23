// The marge stub (internal/marge) satisfies webui.MargeGroups and the
// optional webui.MargeForwarder with its own method names, so main.go passes
// the server straight to webui.WithMarge and no wrapper type is needed. The
// assertions here are the compile-time proof; a rename on either side fails
// the build here rather than at a nil field on the speaker.

package main

import (
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/webui"
)

var (
	_ webui.MargeGroups    = (*marge.Server)(nil)
	_ webui.MargeForwarder = (*marge.Server)(nil)
)
