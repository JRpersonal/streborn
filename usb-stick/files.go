// Package usbstick embedded die SD Stick Template Dateien via go:embed.
// Die Desktop App nutzt das um einen frischen Stick zu bestuecken ohne
// dass der User das Repo separat haben muss.
package usbstick

import (
	"embed"
	"io/fs"
)

//go:embed *.sh *.local *.json *.txt
var raw embed.FS

// Files liefert alle eingebetteten Stick Template Files als io/fs.FS.
// Iteration via fs.WalkDir.
func Files() fs.FS { return raw }

// List liefert die Namen aller eingebetteten Files (ohne Pfad).
func List() ([]string, error) {
	entries, err := raw.ReadDir(".")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Read liefert die rohen Bytes einer eingebetteten Datei.
func Read(name string) ([]byte, error) {
	return raw.ReadFile(name)
}

// Compile-time check dass Files das fs.FS Interface erfuellt.
var _ fs.FS = Files()
