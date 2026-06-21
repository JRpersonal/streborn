// Package usbstick embeds the SD stick template files via go:embed.
// The desktop app uses it to populate a fresh stick without the user
// needing the repo separately.
package usbstick

import (
	"embed"
	"io/fs"
)

//go:embed *.sh *.local *.json *.txt *.so
var raw embed.FS

// Files returns all embedded stick template files as an io/fs.FS.
// Iterate via fs.WalkDir.
func Files() fs.FS { return raw }

// List returns the names of all embedded files (without path).
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

// Read returns the raw bytes of an embedded file.
func Read(name string) ([]byte, error) {
	return raw.ReadFile(name)
}

// Compile-time check that Files satisfies the fs.FS interface.
var _ fs.FS = Files()
