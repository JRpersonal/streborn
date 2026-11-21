// Package usbmedia serves the music files on a USB stick plugged into the
// speaker as a lazy UPnP ContentDirectory (discussion #1136), so the existing
// library code (desktop Library, phone remote, library keys, the queue) can
// browse and play it like any NAS. See docs/streaming/usb-stick.md.
//
// Three design decisions are pinned down in code and tests here:
//
//   - a stick that carries ANY STR install/recovery or Bose update marker is
//     never treated as a music stick (Classify), so the two kinds of stick can
//     never be mistaken for each other;
//   - every path the LAN can name is resolved through an os.Root, so neither
//     "..", an absolute path nor a symlink can leave the stick;
//   - a Browse reads exactly one directory, on request, and never stats or
//     parses the files in it, so a 64 GB stick costs nothing until a user
//     opens a folder, and nothing at all when nobody does.
//
// The package never writes to the stick. Detection (detect.go) reads sysfs,
// /proc/mounts and the first sector of the partition; serving (cds.go) reads
// directories and the files a client asks for.
package usbmedia

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
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

// String names the kind for logs and the debug state.
func (k Kind) String() string {
	if k == KindMusic {
		return "music"
	}
	return "install"
}

// InstallMarkers are the top-level names that make a stick an install,
// recovery or Bose service stick: every file STR's install wizard puts on a
// stick, the keep-open debug switch, and what the Bose firmware itself reacts
// to (remote_services opens SSH, Update.stu is a USB firmware update).
// Matching is case-insensitive because FAT is.
//
// It is the list of stick_has_str_marker in usb-stick/run.sh and
// usb-stick/rc.local, which decides at boot whether a stick is left alone.
// The two must stay the same: a name only one side knows makes that side treat
// an install stick as a music stick or the other way round.
// TestInstallMarkersMatchTheBootScripts fails when they drift.
var InstallMarkers = []string{
	"run.sh", "install.sh", "rc.local", "autostart.sh", "provision-wlan.sh",
	"iptables-setup.sh", "setup-tls.sh", "presets.json", "version.txt", "str-shim.so",
	"streborn-armv7l", "streborn", "go-librespot", "remote_services", "keep-open",
	"wlan.conf", "wlan.ssid", "wlan.pass", "region.conf", "name.conf", "name.raw", "lang.conf",
	"update.stu",
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
		for _, m := range InstallMarkers {
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

// imageMIME is the cover art a folder may carry. Only these are served.
var imageMIME = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
}

// coverNames are the file names (lower case, without extension) a folder's
// cover goes by, best first. folder.jpg is what Windows Media Player writes,
// cover.jpg what most taggers write.
var coverNames = []string{"cover", "folder", "front", "albumart"}

// coverRank is the place of name in coverNames, or -1 when it is no cover.
func coverRank(name string) int {
	lower := strings.ToLower(name)
	if imageMIME[path.Ext(lower)] == "" {
		return -1
	}
	base := strings.TrimSuffix(lower, path.Ext(lower))
	for i, c := range coverNames {
		if base == c {
			return i
		}
	}
	return -1
}

// ImageMIMEFor returns the MIME of a cover file name, or "" when it is none.
func ImageMIMEFor(name string) string {
	if coverRank(path.Base(name)) < 0 {
		return ""
	}
	return imageMIME[strings.ToLower(path.Ext(name))]
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
	// ID is the object id: the stick-relative slash path as UTF-8 text, "0"
	// for the root (charset.go).
	ID string
	// Path is the same path as the filesystem names it, in the mount's
	// iocharset: what the file URL is built from. Equal to ID on a utf8 mount.
	Path   string
	Title  string
	Folder bool
	// MIME is set for a playable file.
	MIME string
	// Cover is the on-disk path (like Path) of the folder's cover image, ""
	// when the folder has none. Found in the one directory read, never by a
	// stat.
	Cover string
}

// Parent returns the object id of the folder an object id sits in: "0" for a
// top-level entry and for the root itself.
func Parent(id string) string {
	if id == "" || id == "0" {
		return "0"
	}
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[:i]
	}
	return "0"
}

// MaxDirEntries caps one directory read. A folder with more entries is listed
// with the first MaxDirEntries the filesystem hands back; 10,000 entries are
// about 1 MB of transient memory on a speaker that has about 120 MB in all.
const MaxDirEntries = 10000

// Library serves one mounted stick.
type Library struct {
	root  *os.Root
	names nameCodec
}

// Open opens the stick mounted at dir, with the file name charset unknown
// (OpenMounted). The caller decides whether to call it: Classify first, and
// only for KindMusic.
func Open(dir string) (*Library, error) {
	return OpenMounted(dir, "")
}

// OpenMounted opens the stick mounted at dir with the options /proc/mounts
// lists for it, so file names are read in the mount's iocharset (NameCharset).
func OpenMounted(dir, mountOptions string) (*Library, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Library{root: r, names: nameCodec{charset: NameCharset(mountOptions)}}, nil
}

// Charset is the file name charset the stick is read with (NameCharset).
func (l *Library) Charset() string { return l.names.charset }

// onDisk turns a stick-relative UTF-8 path (an object id, already checked by
// relPath) into the path the filesystem names. A charset that maps one text to
// a single name needs no look; for an unknown one the first candidate that
// exists wins. fs.ErrNotExist when the text cannot be a name on this mount.
func (l *Library) onDisk(rel string) (string, error) {
	if rel == "." {
		return rel, nil
	}
	cands := l.names.encode(rel)
	switch len(cands) {
	case 0:
		return "", fs.ErrNotExist
	case 1:
		return cands[0], nil
	}
	for _, c := range cands {
		if _, err := l.root.Lstat(c); err == nil {
			return c, nil
		}
	}
	return cands[0], nil
}

// idOf is the object id (UTF-8) of an on-disk path.
func (l *Library) idOf(raw string) string {
	if raw == "." || raw == "" {
		return "0"
	}
	segs := strings.Split(raw, "/")
	for i, s := range segs {
		segs[i] = l.names.decode(s)
	}
	return strings.Join(segs, "/")
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
	// fs.ValidPath's rules, except that the bytes need not be UTF-8: an
	// on-disk path on an iso8859-1 mount is not (charset.go).
	if strings.ContainsAny(id, "\\\x00") {
		return "", ErrBadID
	}
	for _, seg := range strings.Split(id, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", ErrBadID
		}
		// A hidden folder or file is not listed, so it cannot be named either.
		if hidden(seg) {
			return "", ErrBadID
		}
	}
	return id, nil
}

// Browse returns one page of the folder id: sub-folders first, then playable
// files, each group in natural name order (case-insensitive, numbers by value,
// so "2 x" comes before "10 x" the way a PC shows them), plus the total child
// count for paging. It reads the one directory and nothing below it.
func (l *Library) Browse(id string, start, count int) ([]Entry, int, error) {
	all, _, err := l.list(id)
	if err != nil {
		return nil, 0, err
	}
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

// Truncated reports whether the folder id holds more entries than one read
// lists (MaxDirEntries). It reads the directory once.
func (l *Library) Truncated(id string) bool {
	_, truncated, err := l.list(id)
	return err == nil && truncated
}

// list reads the folder id once and returns its visible children in order.
func (l *Library) list(id string) ([]Entry, bool, error) {
	rel, err := relPath(id)
	if err != nil {
		return nil, false, err
	}
	raw, err := l.onDisk(rel)
	if err != nil {
		return nil, false, err
	}
	f, err := l.root.Open(raw)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	des, err := f.ReadDir(MaxDirEntries + 1)
	if err != nil && len(des) == 0 && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	truncated := len(des) > MaxDirEntries
	if truncated {
		des = des[:MaxDirEntries]
	}
	var dirs, files []Entry
	cover, coverAt := "", len(coverNames)
	for _, de := range des {
		rawName := de.Name()
		if hidden(rawName) {
			continue
		}
		name := l.names.decode(rawName)
		child, rawChild := name, rawName
		if rel != "." {
			child, rawChild = rel+"/"+name, raw+"/"+rawName
		}
		switch {
		case de.IsDir():
			dirs = append(dirs, Entry{ID: child, Path: rawChild, Title: name, Folder: true})
		case de.Type().IsRegular():
			if m := MIMEFor(name); m != "" {
				files = append(files, Entry{ID: child, Path: rawChild, Title: strings.TrimSuffix(name, path.Ext(name)), MIME: m})
			} else if r := coverRank(name); r >= 0 && r < coverAt {
				cover, coverAt = rawChild, r
			}
		}
		// Symlinks and devices are skipped outright: FAT has none, and on any
		// other filesystem they are the one thing that could point elsewhere.
	}
	for i := range files {
		files[i].Cover = cover
	}
	byName := func(s []Entry) {
		sort.SliceStable(s, func(i, j int) bool { return naturalLess(s[i].Title, s[j].Title) })
	}
	byName(dirs)
	byName(files)
	all := make([]Entry, 0, len(dirs)+len(files))
	all = append(all, dirs...)
	all = append(all, files...)
	return all, truncated, nil
}

// Lookup describes one object by its id, without listing a folder: what
// BrowseMetadata answers. A folder gets its cover from at most a few stats of
// the usual cover names.
func (l *Library) Lookup(id string) (Entry, error) {
	rel, err := relPath(id)
	if err != nil {
		return Entry{}, err
	}
	if rel == "." {
		return Entry{ID: "0", Path: ".", Title: "", Folder: true}, nil
	}
	raw, err := l.onDisk(rel)
	if err != nil {
		return Entry{}, err
	}
	fi, err := l.root.Lstat(raw)
	if err != nil {
		return Entry{}, err
	}
	name := path.Base(rel)
	if hidden(name) {
		return Entry{}, fs.ErrNotExist
	}
	switch {
	case fi.IsDir():
		e := Entry{ID: rel, Path: raw, Title: name, Folder: true}
		e.Cover = l.folderCover(raw)
		return e, nil
	case fi.Mode().IsRegular():
		m := MIMEFor(name)
		if m == "" {
			return Entry{}, fs.ErrNotExist
		}
		return Entry{ID: rel, Path: raw, Title: strings.TrimSuffix(name, path.Ext(name)), MIME: m,
			Cover: l.folderCover(path.Dir(raw))}, nil
	}
	return Entry{}, fs.ErrNotExist
}

// folderCover finds a folder's cover by name, best first, with one stat per
// candidate, and returns its on-disk path. rel is the folder's on-disk path.
// The cover names are ASCII, so they are the same in every charset. Only
// Lookup uses it; a Browse finds the cover in its own read.
func (l *Library) folderCover(rel string) string {
	for _, c := range coverNames {
		for _, ext := range []string{".jpg", ".jpeg", ".png"} {
			p := c + ext
			if rel != "." && rel != "" {
				p = rel + "/" + p
			}
			if fi, err := l.root.Lstat(p); err == nil && fi.Mode().IsRegular() {
				return p
			}
		}
	}
	return ""
}

// OpenFile opens a playable file for serving (http.ServeContent gives Range
// support, which the renderer needs for a seekable file). p is the path a file
// URL carries: the on-disk path (Entry.Path). The object id (the UTF-8 form)
// is accepted too, for a client that built the URL from the title text.
func (l *Library) OpenFile(p string) (*os.File, error) {
	rel, err := relPath(p)
	if err != nil || rel == "." {
		return nil, ErrBadID
	}
	if MIMEFor(rel) == "" {
		return nil, fmt.Errorf("usbmedia: not a playable file: %s", rel)
	}
	return l.openRegular(rel)
}

// OpenArt opens a folder's cover image. Only the cover names, only images.
// p is an on-disk path or an object id, as for OpenFile.
func (l *Library) OpenArt(p string) (*os.File, error) {
	rel, err := relPath(p)
	if err != nil || rel == "." {
		return nil, ErrBadID
	}
	if ImageMIMEFor(rel) == "" {
		return nil, fmt.Errorf("usbmedia: not a cover image: %s", rel)
	}
	return l.openRegular(rel)
}

// openRegular opens rel and refuses anything that is not a regular file. The
// Lstat first keeps a symlink from being followed even inside the stick. rel
// is tried as the on-disk path first, then as an object id mapped back to one.
func (l *Library) openRegular(rel string) (*os.File, error) {
	fi, err := l.root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) && utf8.ValidString(rel) {
		if raw, rerr := l.onDisk(rel); rerr == nil && raw != rel {
			if rfi, lerr := l.root.Lstat(raw); lerr == nil {
				rel, fi, err = raw, rfi, nil
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || hidden(path.Base(rel)) {
		return nil, fs.ErrNotExist
	}
	return l.root.Open(rel)
}

// Path prefixes under the agent's :8888 for the stick.
const (
	// FilePrefix is where a playable file is served.
	FilePrefix = "/usb/file/"
	// ArtPrefix is where a folder's cover image is served.
	ArtPrefix = "/usb/art/"
)

// FileURL is the address the renderer is handed for a file. base is the
// scheme+authority the caller wants (http://127.0.0.1:8888 for the box's own
// renderer, the Host the client used otherwise); p is the on-disk path
// (Entry.Path), percent-encoded byte for byte, so a Latin-1 name stays the
// bytes the filesystem knows it by. The real file name stays the last path
// segment because the Bose renderer judges playability by the URL's
// extension.
func FileURL(base, p string) string {
	return strings.TrimRight(base, "/") + FilePrefix + escapeID(p)
}

// ArtURL is the address of a folder's cover image (p as for FileURL).
func ArtURL(base, p string) string {
	return strings.TrimRight(base, "/") + ArtPrefix + escapeID(p)
}

func escapeID(id string) string {
	segs := strings.Split(id, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// naturalLess orders two names the way a file manager does: case-insensitive,
// with a run of digits compared by its value, so "2 Intro" sorts before
// "10 Outro". Equal names keep their order (the caller sorts stably).
func naturalLess(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		da, db := digitRun(a), digitRun(b)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func digitRun(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}
