// Package alarm is the alarm clock: a preset, but timed.
//
// An alarm stores a preset SLOT, never a station. The preset store already
// knows how to play radio, Spotify, a saved DLNA queue or a library file, and
// it self-heals (legacy Spotify records, codec MIME, queue reload), so an alarm
// that stored a URL would silently rot the moment the user re-saved that slot.
// "Change what wakes me up" is therefore "save a new preset 3", which costs no
// UI at all.
//
// The document is per speaker and written only when an editor saves it: the
// scheduler keeps what it has already fired in a SEPARATE file (state.go), so
// a fire at 06:30 can never clobber an edit made at 06:29:59.
//
// The wall-clock arithmetic lives in schedule.go and is pure: no Server, no
// box, no I/O, so the awkward cases (a weekday set that wraps past Sunday, the
// spring-forward hour that does not exist, the fall-back hour that happens
// twice) are all table-testable.
package alarm

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/atomicfile"
)

// Limits on the document, so a malformed save cannot grow the NAND file. Eight
// alarms is generous for six presets and bounds the file at about a kilobyte
// (a 2600-track queue preset once filled NAND and broke the next OTA, so every
// list that reaches the flash gets a cap).
const (
	MaxAlarms  = 8
	MaxNameLen = 40
	MaxZoneLen = 64
)

// Alarm is one entry: WHEN, and WHICH OF THE SIX.
type Alarm struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	// Name is a display label for the editor only ("Weekdays").
	Name string `json:"name,omitempty"`
	// Hour and Minute are local to the document's Zone, not UTC.
	Hour   int `json:"hour"`
	Minute int `json:"minute"`
	// Days are the weekdays this alarm repeats on, 0=Sunday .. 6=Saturday,
	// matching time.Weekday. Never empty: see Validate.
	Days []int `json:"days"`
	// Slot is the preset to recall, 1 to 6.
	Slot int `json:"slot"`
	// Volume is the level to set when the alarm fires, 1 to 100. Zero means
	// leave the speaker at whatever level it remembers.
	Volume int `json:"volume,omitempty"`
}

// Document is the whole per-speaker file.
type Document struct {
	// Zone is an IANA name ("Europe/Berlin"). Empty means UTC, and the editor
	// says so on screen: a silently-UTC alarm is the one failure mode that
	// produces "it went off at 07:30".
	Zone   string  `json:"zone"`
	Alarms []Alarm `json:"alarms"`
}

// Location resolves the document's zone. An empty zone is UTC, not an error,
// so a document saved before the editor knew any better still schedules.
func (d Document) Location() (*time.Location, error) {
	if strings.TrimSpace(d.Zone) == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(d.Zone)
}

// Validate checks the document an editor saved and normalises it in place
// (trimmed strings, sorted days, assigned ids). It returns the first problem as
// an error the editor shows verbatim, so the wording is user-facing.
func (d *Document) Validate() error {
	d.Zone = strings.TrimSpace(d.Zone)
	if len(d.Zone) > MaxZoneLen {
		return fmt.Errorf("that time zone name is too long")
	}
	if _, err := d.Location(); err != nil {
		return fmt.Errorf("unknown time zone %q", d.Zone)
	}
	if len(d.Alarms) > MaxAlarms {
		return fmt.Errorf("at most %d alarms", MaxAlarms)
	}
	seen := map[string]bool{}
	for i := range d.Alarms {
		a := &d.Alarms[i]
		a.Name = strings.TrimSpace(a.Name)
		if len(a.Name) > MaxNameLen {
			return fmt.Errorf("the name %q is too long", a.Name)
		}
		if a.Hour < 0 || a.Hour > 23 {
			return fmt.Errorf("alarm %d has an hour outside 0 to 23", i+1)
		}
		if a.Minute < 0 || a.Minute > 59 {
			return fmt.Errorf("alarm %d has a minute outside 0 to 59", i+1)
		}
		if a.Slot < 1 || a.Slot > 6 {
			return fmt.Errorf("an alarm can only play preset 1 to 6")
		}
		if a.Volume < 0 || a.Volume > 100 {
			return fmt.Errorf("alarm volume has to be between 0 and 100")
		}
		// An empty day set is a validation error, never an implicit "every
		// day": a client that sends days:[] would otherwise arm a daily
		// surprise nobody asked for.
		if len(a.Days) == 0 {
			return fmt.Errorf("pick at least one day for alarm %d", i+1)
		}
		day := map[int]bool{}
		for _, w := range a.Days {
			if w < 0 || w > 6 {
				return fmt.Errorf("alarm %d has a day outside Sunday to Saturday", i+1)
			}
			if day[w] {
				return fmt.Errorf("alarm %d names the same day twice", i+1)
			}
			day[w] = true
		}
		sort.Ints(a.Days)
		// An id the editor did not supply is assigned here rather than
		// required of the client: it only has to be stable and unique.
		a.ID = strings.TrimSpace(a.ID)
		if a.ID == "" {
			a.ID = fmt.Sprintf("%02d%02d-%d", a.Hour, a.Minute, i)
		}
		if seen[a.ID] {
			return fmt.Errorf("two alarms share the id %q", a.ID)
		}
		seen[a.ID] = true
	}
	if d.Alarms == nil {
		d.Alarms = []Alarm{}
	}
	return nil
}

// Store is the NAND-persisted document.
type Store struct {
	path   string
	logger *slog.Logger

	mu  sync.RWMutex
	doc Document
}

// New returns a path-less store that never writes. Used by tests and by a dev
// agent with no NAND under it.
func New() *Store {
	return &Store{logger: slog.Default(), doc: Document{Alarms: []Alarm{}}}
}

// Load reads the document from path. A missing file is an empty document and no
// error. A primary that is missing, 0 bytes or corrupt is recovered from the
// durable .bak sibling, because the failure this protects against (a power cut
// at standby truncating the file) is exactly "my alarm did not go off".
func Load(path string, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{path: path, logger: logger, doc: Document{Alarms: []Alarm{}}}
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return s, fmt.Errorf("read alarms: %w", err)
	}
	if len(b) > 0 {
		if doc, perr := parse(b); perr == nil {
			s.doc = doc
			s.warnDefect()
			return s, nil
		}
		// Primary present but corrupt: fall through to the backup.
	}
	if bb, berr := os.ReadFile(path + ".bak"); berr == nil && len(bb) > 0 {
		if doc, perr := parse(bb); perr == nil {
			s.doc = doc
			s.warnDefect()
			// Restore the primary so the box is whole again and a GET stops
			// answering empty.
			_ = atomicfile.WriteFile(path, bb, 0o644)
			logger.Warn("alarms: the stored file was unreadable, recovered from the backup")
			return s, nil
		}
	}
	if len(b) > 0 {
		return s, fmt.Errorf("parse alarms: unknown format in %s", path)
	}
	return s, nil
}

func parse(b []byte) (Document, error) {
	var doc Document
	if err := json.Unmarshal(b, &doc); err != nil {
		return Document{Alarms: []Alarm{}}, err
	}
	if doc.Alarms == nil {
		doc.Alarms = []Alarm{}
	}
	return doc, nil
}

// warnDefect keeps a stored document that no longer validates rather than
// dropping it: one bad entry must not take the working alarms down with it.
// The next save from an editor rewrites the file.
func (s *Store) warnDefect() {
	d := s.doc
	if err := d.Validate(); err != nil {
		s.logger.Warn("alarms: the stored document has a defect, some alarms may not go off", "err", err)
	}
}

// Get returns a copy of the document.
func (s *Store) Get() Document {
	if s == nil {
		return Document{Alarms: []Alarm{}}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneDoc(s.doc)
}

// Set validates, replaces and persists the document. This is the only NAND
// write in this file, and it happens on a save from an editor, never on a fire.
func (s *Store) Set(d Document) error {
	if err := d.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.doc = cloneDoc(d)
	s.mu.Unlock()
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	// Durable write (fsync + rename): a plain write can leave a 0-byte file
	// behind a standby power cut.
	if err := atomicfile.WriteFile(s.path, b, 0o644); err != nil {
		return err
	}
	// The backup only ever holds a document that was written whole.
	_ = atomicfile.WriteFile(s.path+".bak", b, 0o644)
	return nil
}

func cloneDoc(d Document) Document {
	out := Document{Zone: d.Zone, Alarms: make([]Alarm, 0, len(d.Alarms))}
	for _, a := range d.Alarms {
		a.Days = append([]int(nil), a.Days...)
		out.Alarms = append(out.Alarms, a)
	}
	return out
}

// Snapshot is the debug-section view of the store.
func (s *Store) Snapshot() any {
	if s == nil {
		return nil
	}
	d := s.Get()
	enabled := 0
	for _, a := range d.Alarms {
		if a.Enabled {
			enabled++
		}
	}
	return map[string]any{
		"zone":    d.Zone,
		"count":   len(d.Alarms),
		"enabled": enabled,
		"alarms":  d.Alarms,
	}
}
