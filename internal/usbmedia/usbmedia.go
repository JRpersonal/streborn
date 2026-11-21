// Package usbmedia is a SPIKE prototype (discussion #1136): serve the music
// files on a USB stick plugged into the speaker as a lazy UPnP
// ContentDirectory, so the existing library code (desktop Library, phone
// remote, library keys, the queue) can browse and play it like any NAS.
//
// Nothing imports this package. It exists to pin down three design decisions
// in code and tests, see docs/streaming/usb-stick.md:
//
//   - a stick that carries ANY STR install/recovery or Bose update marker is
//     never treated as a music stick (Classify), so the two kinds of stick can
//     never be mistaken for each other;
//   - every path the LAN can name is resolved through an os.Root, so neither
//     "..", an absolute path nor a symlink can leave the stick;
//   - a Browse reads exactly one directory, on request, and never stats or
//     parses the files in it, so a 64 GB stick costs nothing until a user
//     opens a folder, and nothing at all when nobody does.
package usbmedia

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
)

// Kind is what a mounted stick is for.
type Kind int

const (
	// KindMusic is a stick with no STR or Bose marker: safe to serve.
	KindMusic Kind = iota
	// KindInstall is an STR install/recovery stick (or one carrying a Bose
	// firmware image or service marker). Never served, never written.
	KindInstall
)

// installMarkers are the top-level names that make a stick an install,
// recovery or Bose service stick. The first group is what run.sh, rc.local and
// the agent's stickMountDir already key on; the second is what the Bose
// firmware itself reacts to (a USB firmware update, the SSH service gate).
// Matching is case-insensitive because FAT is.
var installMarkers = []string{
	"install.sh", "run.sh", "rc.local", "streborn-armv7l", "streborn", "version.txt",
	"wlan.conf", "wlan.ssid", "wlan.pass",
	"update.stu", "remote_services",
}

// Classify reports what the stick mounted at root is for. An unreadable root
// counts as an install stick: when in doubt the stick is simply not served.
// The cost of that mistake is a music stick that does not show up; the cost of
// the opposite one is an install stick, Wi-Fi credentials included, browsable
// from the LAN.
func Classify(root string) (Kind, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return KindInstall, err
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		for _, m := range installMarkers {
			if name == m {
				return KindInstall, nil
			}
		}
	}
	return KindMusic, nil
}

// audioMIME maps a lower-case file extension to the DIDL protocolInfo MIME.
// Only formats the renderer's ConnectionManager lists as a sink (measured on
// ST10, ST30 and Portable, FW 27.0.6) are offered; anything else is not shown.
var audioMIME = map[string]string{
	".mp3":  "audio/mpeg",
	".flac": "audio/flac",
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".wav":  "audio/wav",
	".wma":  "audio/x-ms-wma",
}

// MIMEFor returns the MIME for a file name, or "" when it is not playable.
func MIMEFor(name string) string {
	return audioMIME[strings.ToLower(path.Ext(name))]
}

// hidden reports names a user never wants to see: dot files (macOS
// AppleDouble "._x" included) and the Windows system folders FAT sticks carry.
func hidden(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch strings.ToLower(name) {
	case "system volume information", "$recycle.bin", "recycler":
		return true
	}
	return false
}

// Entry is one child in a Browse page.
type Entry struct {
	// ID is the object id: the stick-relative slash path, "0" for the root.
	ID     string
	Title  string
	Folder bool
	// MIME is set for a playable file.
	MIME string
}

// Library serves one mounted stick.
type Library struct {
	root *os.Root
}

// Open opens the stick mounted at dir. The caller decides whether to call it:
// Classify first, and only for KindMusic.
func Open(dir string) (*Library, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Library{root: r}, nil
}

// Close releases the root handle, which must happen before an unmount.
func (l *Library) Close() error { return l.root.Close() }

// ErrBadID is returned for an object id that does not name a path inside the
// stick.
var ErrBadID = errors.New("usbmedia: object id outside the stick")

// relPath turns an object id into a stick-relative path, rejecting anything
// that is not a plain local path. os.Root is the real guard (it also refuses a
// symlink that points out); this check keeps the error explicit.
func relPath(id string) (string, error) {
	if id == "0" || id == "" {
		return ".", nil
	}
	if strings.Contains(id, "\\") || !fs.ValidPath(id) {
		return "", ErrBadID
	}
	return id, nil
}

// Browse returns one page of the folder id: sub-folders first, then playable
// files, each group sorted by name (case-insensitive, so the order matches what
// a user sees on a PC), plus the total child count for paging. It reads the one
// directory and nothing below it.
func (l *Library) Browse(id string, start, count int) ([]Entry, int, error) {
	rel, err := relPath(id)
	if err != nil {
		return nil, 0, err
	}
	f, err := l.root.Open(rel)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	des, err := f.ReadDir(-1)
	if err != nil {
		return nil, 0, err
	}
	var dirs, files []Entry
	for _, de := range des {
		name := de.Name()
		if hidden(name) {
			continue
		}
		child := name
		if rel != "." {
			child = rel + "/" + name
		}
		switch {
		case de.IsDir():
			dirs = append(dirs, Entry{ID: child, Title: name, Folder: true})
		case de.Type().IsRegular():
			if m := MIMEFor(name); m != "" {
				files = append(files, Entry{ID: child, Title: strings.TrimSuffix(name, path.Ext(name)), MIME: m})
			}
		}
		// Symlinks and devices are skipped outright: FAT has none, and on any
		// other filesystem they are the one thing that could point elsewhere.
	}
	byName := func(s []Entry) {
		sort.Slice(s, func(i, j int) bool { return strings.ToLower(s[i].ID) < strings.ToLower(s[j].ID) })
	}
	byName(dirs)
	byName(files)
	all := make([]Entry, 0, len(dirs)+len(files))
	all = append(all, dirs...)
	all = append(all, files...)
	total := len(all)
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end := total
	if count > 0 && start+count < end {
		end = start + count
	}
	return all[start:end], total, nil
}

// OpenFile opens a playable file for serving (http.ServeContent gives Range
// support, which the renderer needs for a seekable file).
func (l *Library) OpenFile(id string) (*os.File, error) {
	rel, err := relPath(id)
	if err != nil || rel == "." {
		return nil, ErrBadID
	}
	if MIMEFor(rel) == "" {
		return nil, fmt.Errorf("usbmedia: not a playable file: %s", rel)
	}
	return l.root.Open(rel)
}

// FileURL is the address the renderer is handed for a file. base is the
// scheme+authority the caller wants (http://127.0.0.1:8888 for the box's own
// renderer, the Host the client used otherwise). The real file name stays the
// last path segment because the Bose renderer judges playability by the URL's
// extension.
func FileURL(base, id string) string {
	segs := strings.Split(id, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.TrimRight(base, "/") + "/usb/file/" + strings.Join(segs, "/")
}
