// Package sticksetup embedded helper tools.
//
// winformat.exe is a small helper that formats FAT32 volumes via
// FmIfs.dll FormatEx (no 32 GB limit). The format step calls it
// once with UAC elevation.
//
// An empty stub file at embedded/winformat.exe is committed so that
// go:embed succeeds on a clean checkout. CI overwrites the stub
// with the real binary during release. Local developers who need a
// working winformat can build it with:
//
//	go build -ldflags "-s -w" -o sticksetup/embedded/winformat.exe ./cmd/winformat

package sticksetup

import _ "embed"

//go:embed embedded/winformat.exe
var winformatBinary []byte
