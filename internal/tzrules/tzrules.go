// Package tzrules resolves IANA time zone names from the zone data STR ships
// in the agent, never from the speaker's own.
//
// The speaker does have zone files: the Bose firmware carries
// /usr/share/zoneinfo (2.4 MB, files dated 2022 on FW 27.0.6), and that is
// the first place time.LoadLocation looks on Linux. The time/tzdata import
// only fills in when no system file exists, so with it the agent still read
// the firmware's outdated rules. On a Portable set to America/Vancouver
// (2026-11-09) the agent and the display both said UTC-8 where British
// Columbia has been UTC-7 since November 2026, so the standby clock check
// saw nothing to correct and alarms would have gone off an hour late.
//
// zoneinfo.zip is Go's lib/time/zoneinfo.zip, the IANA database in the form
// time/tzdata embeds. To refresh it, take the file from the newest Go release
// (lib/time/update.bash in that release names the IANA version) and update
// DataVersion; TestVancouverIsPermanentUTCMinus7 fails on data older than
// 2026b.
package tzrules

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// DataVersion is the IANA release zoneinfo.zip was built from.
const DataVersion = "2026c"

//go:embed zoneinfo.zip
var zoneinfoZip []byte

var (
	indexOnce sync.Once
	index     map[string]*zip.File
	indexErr  error

	cacheMu sync.Mutex
	cache   = map[string]*time.Location{}
)

func loadIndex() {
	r, err := zip.NewReader(bytes.NewReader(zoneinfoZip), int64(len(zoneinfoZip)))
	if err != nil {
		indexErr = fmt.Errorf("tzrules: embedded zone data unreadable: %w", err)
		return
	}
	index = make(map[string]*zip.File, len(r.File))
	for _, f := range r.File {
		index[f.Name] = f
	}
}

// Load returns the location for an IANA zone name from STR's own zone data.
// "" and "UTC" are UTC. A name the embedded data does not know falls back to
// time.LoadLocation, so a zone added to the system before STR's data was
// refreshed still resolves.
func Load(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "UTC" {
		return time.UTC, nil
	}
	if name == "Local" {
		return time.LoadLocation(name)
	}
	cacheMu.Lock()
	if loc, ok := cache[name]; ok {
		cacheMu.Unlock()
		return loc, nil
	}
	cacheMu.Unlock()

	indexOnce.Do(loadIndex)
	if indexErr == nil {
		if f, ok := index[name]; ok {
			loc, err := fromZip(name, f)
			if err != nil {
				return nil, err
			}
			cacheMu.Lock()
			cache[name] = loc
			cacheMu.Unlock()
			return loc, nil
		}
	}
	return time.LoadLocation(name)
}

func fromZip(name string, f *zip.File) (*time.Location, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("tzrules: %s: %w", name, err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("tzrules: %s: %w", name, err)
	}
	return time.LoadLocationFromTZData(name, data)
}
