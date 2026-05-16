// Package sticksetup — embed Helper Tools.
//
// winformat.exe ist ein kleines Helper Programm das FAT32 Volumes via
// FmIfs.dll FormatEx formatiert (kein 32 GB Limit). Wird vom Format
// Schritt einmalig mit UAC Elevation aufgerufen.
//
// Build via:
//   go build -ldflags "-s -w" -o sticksetup/embedded/winformat.exe ./cmd/winformat

package sticksetup

import _ "embed"

//go:embed embedded/winformat.exe
var winformatBinary []byte
