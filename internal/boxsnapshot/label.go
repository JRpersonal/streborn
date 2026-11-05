package boxsnapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

// The speaker's last-known display name and model, kept on NAND.
//
// The agent announces itself seconds after boot, long before the Bose firmware
// serves /info, and until then it has neither: the mDNS record and the version
// endpoint carried a placeholder name and the generic model "SoundTouch". That
// is harmless when the firmware follows within a minute, but a firmware that is
// slow to start, or does not start at all, left the speaker in the desktop app
// as a nameless "SoundTouch" for as long as it stayed down (field case
// 2026-11-05, a Wave after an agent update). The last answer the firmware gave
// is a far better stand-in, and the live answer still replaces it.
//
// Written once per change, never periodically: a speaker's standby countdown is
// reset by NAND writes, and the poll that feeds this runs every 30 seconds.

const labelPath = "/mnt/nv/streborn/box-label.json"

// LabelPath is where the agent keeps the last-known name and model.
func LabelPath() string { return labelPath }

// Label is the speaker's last-known display name and model as the firmware
// reported them.
type Label struct {
	Name  string `json:"name,omitempty"`
	Model string `json:"model,omitempty"`
}

// LabelStore holds the persisted label and writes it back only when a field
// actually changes. Nil-safe: a nil store reads empty and never writes.
type LabelStore struct {
	path string
	mu   sync.Mutex
	cur  Label
}

// OpenLabel loads the label stored at path. A missing or unreadable file is an
// empty label, never an error: the speaker then simply starts with the
// placeholders it always had.
func OpenLabel(path string) *LabelStore {
	s := &LabelStore{path: path}
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var l Label
	if json.Unmarshal(data, &l) != nil {
		return s
	}
	s.cur = cleanLabel(l)
	return s
}

// Get returns the current label.
func (s *LabelStore) Get() Label {
	if s == nil {
		return Label{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Note records what the firmware just reported. An empty field means "not
// reported", never "clear it". It writes to NAND only when the stored label
// changes, and reports whether it did.
func (s *LabelStore) Note(name, model string) (changed bool, err error) {
	if s == nil {
		return false, nil
	}
	in := cleanLabel(Label{Name: name, Model: model})
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cur
	if in.Name != "" {
		next.Name = in.Name
	}
	if in.Model != "" {
		next.Model = in.Model
	}
	if next == s.cur {
		return false, nil
	}
	s.cur = next
	if s.path == "" {
		return true, nil
	}
	data, err := json.Marshal(next)
	if err != nil {
		return true, err
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return true, err
	}
	return true, os.Rename(tmp, s.path)
}

// cleanLabel trims both fields and drops values that must not be persisted: a
// name that is not valid UTF-8 (a Latin-1 umlaut straight from the firmware,
// #1582) would come back from JSON altered, never compare equal to the live
// name again and be rewritten on every boot, and the generic model is not
// knowledge worth keeping.
func cleanLabel(l Label) Label {
	l.Name = strings.TrimSpace(l.Name)
	l.Model = strings.TrimSpace(l.Model)
	if !utf8.ValidString(l.Name) {
		l.Name = ""
	}
	if l.Model == "SoundTouch" || !utf8.ValidString(l.Model) {
		l.Model = ""
	}
	return l
}
