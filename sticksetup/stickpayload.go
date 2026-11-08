package sticksetup

// STR's own install files on a stick, for the firmware ladder.
//
// Bose's USB updater wants a stick that holds only Update.stu. A stick the
// install wizard prepared first carries STR's install payload at its root
// (scripts, the 19 MB agent, the Spotify engine, the config files), and a
// SoundTouch 20 Series I on 9.0.41 showed the hand after 4 + Volume minus but
// never a progress bar, again and again, with Update.stu sitting beside those
// files (support case, 2026-11). So before the ladder writes Update.stu it
// takes STR's own files off the stick. Only names STR itself writes there are
// removed (the wizard's payload, plus the setup.log and cap*.ogg the speaker's
// run.sh writes back), and only on a stick that carries a file no other
// software writes; anything else stays where it is and is reported instead.

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	usbstick "github.com/JRpersonal/streborn/usb-stick"
)

// stickExtraFileNames are the files WriteStickFiles writes beside the
// embedded templates: the agent, the Spotify engine, the SSH marker and the
// version stamp.
var stickExtraFileNames = []string{"streborn-armv7l", "go-librespot", "remote_services", "version.txt"}

// stickConfigFileNames are the files the wizard's config writers put on the
// stick (WriteWLANConfig, WriteRegionConfig, WriteNameConfig,
// WriteLangConfig).
var stickConfigFileNames = []string{"wlan.conf", "wlan.ssid", "wlan.pass", "region.conf", "name.conf", "name.raw", "lang.conf"}

// strStickMarkers are payload files no other software writes. A stick counts
// as STR-prepared only when one of them is at its root, so a user's own
// "install.sh" or "version.txt" on an unrelated stick is never touched.
var strStickMarkers = []string{"streborn-armv7l", "str-shim.so", "iptables-setup.sh", "setup-tls.sh", "provision-wlan.sh"}

// PayloadFileNames lists, sorted, every file the install wizard writes to the
// root of a stick: the embedded templates, the binaries, the markers and the
// config files. A test keeps it in step with the writers.
func PayloadFileNames() []string {
	set := map[string]bool{}
	if names, err := usbstick.List(); err == nil {
		for _, n := range names {
			if !strings.HasSuffix(n, ".go") {
				set[n] = true
			}
		}
	}
	for _, n := range stickExtraFileNames {
		set[n] = true
	}
	for _, n := range stickConfigFileNames {
		set[n] = true
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// payloadNameSet is PayloadFileNames in lower case, plus the ".new" name
// writeFile uses while it writes, so a half-written payload file goes too.
func payloadNameSet() map[string]bool {
	set := map[string]bool{}
	for _, n := range PayloadFileNames() {
		l := strings.ToLower(n)
		set[l] = true
		set[l+".new"] = true
	}
	return set
}

// IsSTRPayloadStick reports whether the root of the stick carries a file only
// STR's install wizard writes.
func IsSTRPayloadStick(stickPath string) bool {
	for _, m := range strStickMarkers {
		if st, err := os.Lstat(filepath.Join(stickPath, m)); err == nil && st.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// strPayloadEntries returns the root entries of an STR-prepared stick that are
// STR payload files (regular files only, matched without regard to case, as
// FAT32 does). Empty on any other stick.
func strPayloadEntries(stickPath string) []os.DirEntry {
	if !IsSTRPayloadStick(stickPath) {
		return nil
	}
	entries, err := os.ReadDir(stickPath)
	if err != nil {
		return nil
	}
	set := payloadNameSet()
	var out []os.DirEntry
	for _, e := range entries {
		if e.Type().IsRegular() && (set[strings.ToLower(e.Name())] || speakerWrittenName(e.Name())) {
			out = append(out, e)
		}
	}
	return out
}

// speakerWrittenName reports whether name is one of the files the speaker's
// STR run.sh writes back to the stick it booted from: setup.log, and the
// cap*.ogg audio captures it clears again (usb-stick/run.sh). They exist only
// on an STR stick, so they go with the payload.
func speakerWrittenName(name string) bool {
	l := strings.ToLower(name)
	if l == "setup.log" {
		return true
	}
	return strings.HasPrefix(l, "cap") && strings.HasSuffix(l, ".ogg")
}

// OtherFilesSummary describes what OtherFiles lists without naming anything:
// the count, the total size of the files, and the sorted set of extensions
// (lower case, "(none)" for a file without one, "(folder)" for a folder). The
// firmware ladder logs this instead of the names, because the user's own file
// names can be personal and diagnostic bundles are posted in public.
func OtherFilesSummary(stickPath string) (count int, totalBytes int64, exts []string) {
	set := map[string]bool{}
	for _, n := range OtherFiles(stickPath) {
		count++
		if strings.HasSuffix(n, "/") {
			set["(folder)"] = true
			continue
		}
		if fi, err := os.Lstat(filepath.Join(stickPath, n)); err == nil {
			totalBytes += fi.Size()
		}
		ext := strings.ToLower(filepath.Ext(n))
		if ext == "" || ext == strings.ToLower(n) {
			ext = "(none)"
		}
		set[ext] = true
	}
	for e := range set {
		exts = append(exts, e)
	}
	sort.Strings(exts)
	return count, totalBytes, exts
}

// STRPayloadBytes is the space STR's payload files take on the stick, 0 on a
// stick STR did not prepare. RemoveSTRPayload frees it before the firmware
// write, so the firmware ladder counts it as free.
func STRPayloadBytes(stickPath string) int64 {
	var n int64
	for _, e := range strPayloadEntries(stickPath) {
		if fi, err := e.Info(); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// RemoveSTRPayload deletes STR's own install files from the root of an
// STR-prepared stick and returns the names it removed. A stick without an STR
// marker is left as it is. Folders, links and every name STR does not write
// are never touched. An error names the files that could not be removed; the
// ones that could are still returned.
func RemoveSTRPayload(stickPath string) ([]string, error) {
	var removed []string
	var errs []error
	for _, e := range strPayloadEntries(stickPath) {
		if err := os.Remove(filepath.Join(stickPath, e.Name())); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, e.Name())
	}
	return removed, errors.Join(errs...)
}

// OtherFiles lists, sorted, the root entries of the stick besides Update.stu
// and the entries the PC's operating system creates by itself (see
// osStickEntries). Folders carry a trailing "/". Empty when the root cannot be
// read.
func OtherFiles(stickPath string) []string {
	entries, err := os.ReadDir(stickPath)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if name == strings.ToLower(FirmwareFileName) || osStickEntries[name] || strings.HasPrefix(name, "._") {
			continue
		}
		n := e.Name()
		if e.IsDir() {
			n += "/"
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
