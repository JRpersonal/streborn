// Package boxsources reads the speaker firmware's own source store,
// /mnt/nv/BoseApp-Persistence/<N>/Sources.xml.
//
// That file is where the firmware keeps every linked music service together
// with its login: one <source> per service, the login in the secret and
// secretType attributes, the service and its account in a <sourceKey> child.
// The shape follows what other cloud replacements document and write back
// (AfterTouch, gesellix/Bose-SoundTouch):
//
//	<sources>
//	  <source displayName="..." secret="..." secretType="token">
//	    <sourceKey type="DEEZER" account="..."/>
//	  </source>
//	</sources>
//
// The speaker's /sources API never shows the secret, which is why STR's
// carry-over of an account the speaker had before STR (internal/boxsnapshot)
// could name the service but never log it in (docs/streaming/deezer.md).
//
// The file is firmware-owned. This package only reads it, on demand: no
// timer, no periodic read. Nothing here writes it.
//
// A secret read from this file is the owner's login. It must never be
// logged, never enter a diagnostic bundle and never leave the speaker; the
// debug form (Summary) carries only the service, the secret's type and
// whether one is present.
package boxsources

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Glob matches the firmware's source store. The persistence folder is
// numbered (1 on every speaker seen so far); the glob is the same one the
// other BoseApp-Persistence readers use.
const Glob = "/mnt/nv/BoseApp-Persistence/*/Sources.xml"

// maxFileBytes caps a read. A real Sources.xml is a few kilobytes.
const maxFileBytes = 256 * 1024

// Entry is one <source> of the store. Secret is the login itself.
type Entry struct {
	Type        string // the service enum, e.g. "DEEZER" (sourceKey type)
	Account     string // the service account (sourceKey account)
	DisplayName string
	Secret      string
	SecretType  string
}

// Parse reads the entries of a Sources.xml document. Unknown elements and
// attributes are ignored; a document without <source> elements gives none.
func Parse(data []byte) ([]Entry, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var out []Entry
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "source" {
			continue
		}
		var raw struct {
			DisplayName string `xml:"displayName,attr"`
			Secret      string `xml:"secret,attr"`
			SecretType  string `xml:"secretType,attr"`
			SourceKey   struct {
				Type    string `xml:"type,attr"`
				Account string `xml:"account,attr"`
			} `xml:"sourceKey"`
		}
		if err := dec.DecodeElement(&raw, &se); err != nil {
			return out, err
		}
		out = append(out, Entry{
			Type:        strings.TrimSpace(raw.SourceKey.Type),
			Account:     raw.SourceKey.Account,
			DisplayName: raw.DisplayName,
			Secret:      raw.Secret,
			SecretType:  raw.SecretType,
		})
	}
}

// Find returns the path of the firmware's source store, or "" when the
// speaker has none. With several persistence folders the lowest-numbered
// one wins (sorted, so the choice is stable).
func Find(glob string) string {
	if glob == "" {
		glob = Glob
	}
	m, _ := filepath.Glob(glob)
	if len(m) == 0 {
		return ""
	}
	sort.Strings(m)
	return m[0]
}

// Read finds and parses the source store. A missing file is not an error:
// it gives no entries and an empty path.
func Read(glob string) (path string, entries []Entry, err error) {
	path = Find(glob)
	if path == "" {
		return "", nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return path, nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes))
	if err != nil {
		return path, nil, err
	}
	entries, err = Parse(data)
	return path, entries, err
}

// SummaryEntry is one entry as a diagnostic may show it: never the secret,
// never the account, never the display name.
type SummaryEntry struct {
	Source     string `json:"source"`
	SecretType string `json:"secretType,omitempty"`
	HasSecret  bool   `json:"hasSecret"`
}

// Summary is the debug-state section: whether the store exists, whether it
// parsed, and one line per entry.
type Summary struct {
	Present bool           `json:"present"`
	Error   string         `json:"error,omitempty"`
	Entries []SummaryEntry `json:"entries,omitempty"`
}

// safeToken keeps an enum-like value (a service type or a secret type) and
// replaces anything else with a marker, so a field that unexpectedly holds
// data can never carry it into a diagnostic.
var safeToken = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,40}$`)

func safe(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !safeToken.MatchString(v) {
		return "<other>"
	}
	return v
}

// Summarize turns entries into their debug form.
func Summarize(entries []Entry) []SummaryEntry {
	out := make([]SummaryEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, SummaryEntry{
			Source:     safe(e.Type),
			SecretType: safe(e.SecretType),
			HasSecret:  e.Secret != "",
		})
	}
	return out
}

// DebugSummary reads the store now and returns its debug form. It is called
// per /api/debug/state request, never on a timer.
func DebugSummary(glob string) Summary {
	path, entries, err := Read(glob)
	s := Summary{Present: path != ""}
	if err != nil {
		// The error text of a failed parse can quote the document; keep only
		// that it failed.
		s.Error = "unreadable"
	}
	s.Entries = Summarize(entries)
	return s
}
