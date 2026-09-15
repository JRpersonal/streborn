package alarm

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/atomicfile"
)

// StateStore remembers which due instant each alarm has already been fired for.
//
// It is a SEPARATE file from the document on purpose. The scheduler writes this
// one and an editor writes the other, so a fire at 06:30 cannot clobber an edit
// made at 06:29:59, and the read-modify-write lost update that a shared file
// would invite is structurally impossible.
type StateStore struct {
	path   string
	logger *slog.Logger

	mu    sync.Mutex
	fired map[string]time.Time
	last  FireOutcome
}

// FireOutcome is the last thing an alarm did, for the status block and the
// debug section. It is deliberately in memory only: persisting it would mean a
// second NAND write per alarm, minutes after the first, to answer a question
// ("did it work last night?") that a running agent can already answer.
type FireOutcome struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	Slot   int       `json:"slot"`
	OK     bool      `json:"ok"`
	Detail string    `json:"detail,omitempty"`
}

type stateFile struct {
	Fired map[string]time.Time `json:"fired"`
}

// LoadState reads the fired state from path. Anything unreadable is treated as
// empty and never fatal: the cost of losing this file is at worst one alarm
// firing twice within its grace window, which is a far smaller problem than an
// agent that refuses to start.
func LoadState(path string, logger *slog.Logger) *StateStore {
	if logger == nil {
		logger = slog.Default()
	}
	s := &StateStore{path: path, logger: logger, fired: map[string]time.Time{}}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return s
	}
	var f stateFile
	if err := json.Unmarshal(b, &f); err != nil {
		logger.Warn("alarms: the fired-state file is unreadable, starting from empty", "err", err)
		return s
	}
	for id, t := range f.Fired {
		s.fired[id] = t
	}
	return s
}

// FiredFor returns the due instant this alarm was last fired for.
func (s *StateStore) FiredFor(id string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fired[id]
}

// MarkFired records that id has been fired for the due instant, and persists it.
//
// The caller writes this BEFORE waking the speaker, not after. A crash midway
// through a fire then costs one missed alarm instead of a re-fire loop on a box
// whose memory guard reboots it.
func (s *StateStore) MarkFired(id string, due time.Time) error {
	s.mu.Lock()
	s.fired[id] = due
	s.mu.Unlock()
	return s.save()
}

// NoteOutcome records how the last fire actually went.
func (s *StateStore) NoteOutcome(o FireOutcome) {
	s.mu.Lock()
	s.last = o
	s.mu.Unlock()
}

// LastOutcome returns the last fire's outcome, and whether there was one.
func (s *StateStore) LastOutcome() (FireOutcome, bool) {
	if s == nil {
		return FireOutcome{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, !s.last.At.IsZero()
}

// Prune drops ids that the document no longer contains, so deleting alarms over
// a year does not grow the file forever. It writes only when something went.
func (s *StateStore) Prune(keep map[string]bool) {
	s.mu.Lock()
	changed := false
	for id := range s.fired {
		if !keep[id] {
			delete(s.fired, id)
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		_ = s.save()
	}
}

func (s *StateStore) save() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	f := stateFile{Fired: make(map[string]time.Time, len(s.fired))}
	for id, t := range s.fired {
		f.Fired[id] = t
	}
	s.mu.Unlock()
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(s.path, b, 0o644); err != nil {
		return fmt.Errorf("write alarm state: %w", err)
	}
	return nil
}
