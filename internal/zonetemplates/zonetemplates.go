// Package zonetemplates persists the user's named group templates and the
// single permanent group for Bose SoundTouch multiroom on the group master's
// NAND, so a saved group can be recalled (or, for the permanent one,
// re-formed automatically) after a reboot, standby cycle or Wi-Fi outage.
//
// A template stores the user's INTENT: which boxes belong together and which
// one leads. The box remains the authority on what is currently grouped; the
// agent reconciles towards the permanent template. At most one template is
// permanent at a time, and the store enforces that invariant.
//
// Alongside the templates the store keeps an "out list": members the
// reconciler must leave alone because the user removed them or they started
// playing something of their own. The out list belongs to the permanent
// group, so it is cleared whenever the permanent template changes or is
// deleted.
//
// The on-disk format is a single JSON object; a missing or empty file means
// "no templates", mirroring the lenient zones and media server stores. The
// store is silent: callers log.
package zonetemplates

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JRpersonal/streborn/internal/atomicfile"
)

// Member is one speaker in a template: its stable deviceID plus a last-known
// IP hint, re-resolved via discovery at recall time because DHCP leases
// change.
type Member struct {
	DeviceID string `json:"deviceID"`
	IP       string `json:"ip"`
}

// Template is one saved group: a master, its members, and whether the group
// is the single permanent one the agent keeps re-forming.
type Template struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Master    Member   `json:"master"`
	Members   []Member `json:"members"`
	Permanent bool     `json:"permanent"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

// OutEntry is one member the permanent-group reconciler must leave alone.
type OutEntry struct {
	DeviceID string `json:"deviceID"`
	IP       string `json:"ip"`
	Reason   string `json:"reason"` // "user-removed" | "self-play"
	At       string `json:"at"`
}

// Limits enforced by Upsert.
const (
	maxNameLen   = 48
	maxMembers   = 8
	maxTemplates = 12
)

// file is the on-disk shape.
type file struct {
	Templates []Template `json:"templates"`
	Out       []OutEntry `json:"out,omitempty"`
}

// Store holds the templates and the out list and syncs them to disk.
type Store struct {
	path      string
	mu        sync.RWMutex
	templates []Template
	out       []OutEntry
}

// New returns an empty in-memory store with no persistence path.
func New() *Store { return &Store{} }

// Load reads the store from path. A missing or empty file yields an empty
// store and no error: no templates is the normal state, not a fault. A parse
// error returns an empty store plus the error so the caller can log it and
// continue.
func Load(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("read zone templates: %w", err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return s, nil
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return s, fmt.Errorf("parse zone templates: %w", err)
	}
	for _, t := range f.Templates {
		if t.ID == "" {
			continue
		}
		s.templates = append(s.templates, copyTemplate(t))
	}
	s.out = append([]OutEntry(nil), f.Out...)
	return s, nil
}

// List returns deep copies of all templates, sorted by name
// (case-insensitively) so the UI is stable.
func (s *Store) List() []Template {
	s.mu.RLock()
	out := make([]Template, 0, len(s.templates))
	for _, t := range s.templates {
		out = append(out, copyTemplate(t))
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		ni, nj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if ni != nj {
			return ni < nj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get returns a deep copy of the template with the given id.
func (s *Store) Get(id string) (Template, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if i := s.indexByIDLocked(id); i >= 0 {
		return copyTemplate(s.templates[i]), true
	}
	return Template{}, false
}

// Permanent returns a deep copy of the single permanent template, if any.
func (s *Store) Permanent() (Template, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.templates {
		if t.Permanent {
			return copyTemplate(t), true
		}
	}
	return Template{}, false
}

// Upsert creates or replaces a template and persists, returning the stored
// result. With a non-empty ID it replaces that template (unknown ID is an
// error). With an empty ID, a case-insensitive name match replaces the
// matched template, keeping its ID, CreatedAt, and Permanent flag; otherwise
// a new template is created with a fresh ID. Permanent cannot be set through
// Upsert: a replace preserves the stored flag, a create forces it to false
// (SetPermanent is the only way to flip it). Replacing a template with
// identical content does not bump UpdatedAt and does not write: recall UIs
// re-save freely and a repeat must not cost a NAND write.
func (s *Store) Upsert(t Template) (Template, error) {
	t.Name = strings.TrimSpace(t.Name)
	if err := validate(t); err != nil {
		return Template{}, err
	}
	now := nowUTC()

	s.mu.Lock()
	var idx int
	if t.ID != "" {
		idx = s.indexByIDLocked(t.ID)
		if idx < 0 {
			s.mu.Unlock()
			return Template{}, fmt.Errorf("unknown template id %q", t.ID)
		}
	} else {
		idx = s.indexByNameLocked(t.Name)
	}

	if idx >= 0 {
		cur := s.templates[idx]
		next := copyTemplate(t)
		next.ID = cur.ID
		next.CreatedAt = cur.CreatedAt
		next.Permanent = cur.Permanent
		next.UpdatedAt = cur.UpdatedAt
		if equalTemplates(next, cur) {
			s.mu.Unlock()
			return copyTemplate(cur), nil
		}
		next.UpdatedAt = now
		s.templates[idx] = next
		res := copyTemplate(next)
		s.mu.Unlock()
		if err := s.Save(); err != nil {
			return Template{}, err
		}
		return res, nil
	}

	if len(s.templates) >= maxTemplates {
		s.mu.Unlock()
		return Template{}, fmt.Errorf("too many templates (max %d)", maxTemplates)
	}
	id, err := s.freshIDLocked()
	if err != nil {
		s.mu.Unlock()
		return Template{}, err
	}
	next := copyTemplate(t)
	next.ID = id
	next.Permanent = false
	next.CreatedAt = now
	next.UpdatedAt = now
	s.templates = append(s.templates, next)
	res := copyTemplate(next)
	s.mu.Unlock()
	if err := s.Save(); err != nil {
		return Template{}, err
	}
	return res, nil
}

// Delete removes a template and persists, reporting whether the removed one
// was the permanent template. Deleting the permanent template also clears the
// out list: the exclusions belonged to that group.
func (s *Store) Delete(id string) (removedWasPermanent bool, err error) {
	s.mu.Lock()
	idx := s.indexByIDLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return false, fmt.Errorf("unknown template id %q", id)
	}
	removedWasPermanent = s.templates[idx].Permanent
	s.templates = append(s.templates[:idx], s.templates[idx+1:]...)
	if removedWasPermanent {
		s.out = nil
	}
	s.mu.Unlock()
	return removedWasPermanent, s.Save()
}

// SetPermanent flips the permanent flag of a template and persists. Setting
// one template permanent clears the flag on every other, so at most one
// template is permanent at a time. Both directions clear the out list: the
// exclusions belong to the permanent group that is being changed. Templates
// whose flag actually flips get a fresh UpdatedAt. Nothing changed means no
// write.
func (s *Store) SetPermanent(id string, on bool) error {
	now := nowUTC()
	s.mu.Lock()
	idx := s.indexByIDLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("unknown template id %q", id)
	}
	changed := false
	if on {
		for i := range s.templates {
			want := i == idx
			if s.templates[i].Permanent != want {
				s.templates[i].Permanent = want
				s.templates[i].UpdatedAt = now
				changed = true
			}
		}
	} else if s.templates[idx].Permanent {
		s.templates[idx].Permanent = false
		s.templates[idx].UpdatedAt = now
		changed = true
	}
	if len(s.out) > 0 {
		s.out = nil
		changed = true
	}
	s.mu.Unlock()
	if !changed {
		return nil
	}
	return s.Save()
}

// OutList returns a copy of the out list.
func (s *Store) OutList() []OutEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]OutEntry(nil), s.out...)
}

// IsOut reports whether a member is excluded from permanent-group
// reconciliation. A member matches an entry when the entry's IP equals ip
// (both non-empty) or the entry's deviceID equals deviceID case-insensitively
// (both non-empty).
func (s *Store) IsOut(deviceID, ip string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.out {
		if matchesOut(e, deviceID, ip) {
			return true
		}
	}
	return false
}

// AddOut excludes a member from permanent-group reconciliation and persists.
// Adding a member that already matches an entry does not write: exclusion is
// idempotent and a repeat must not cost a NAND write.
func (s *Store) AddOut(deviceID, ip, reason string) error {
	if deviceID == "" && ip == "" {
		return fmt.Errorf("out entry needs a deviceID or an ip")
	}
	s.mu.Lock()
	for _, e := range s.out {
		if matchesOut(e, deviceID, ip) {
			s.mu.Unlock()
			return nil
		}
	}
	s.out = append(s.out, OutEntry{
		DeviceID: deviceID,
		IP:       ip,
		Reason:   reason,
		At:       nowUTC(),
	})
	s.mu.Unlock()
	return s.Save()
}

// RemoveOut drops every entry matching the member and persists. No match
// means no write, for the same reason AddOut is idempotent.
func (s *Store) RemoveOut(deviceID, ip string) error {
	s.mu.Lock()
	kept := s.out[:0]
	for _, e := range s.out {
		if !matchesOut(e, deviceID, ip) {
			kept = append(kept, e)
		}
	}
	removed := len(kept) != len(s.out)
	s.out = kept
	s.mu.Unlock()
	if !removed {
		return nil
	}
	return s.Save()
}

// ClearOut drops the whole out list and persists. An already-empty list
// returns without writing.
func (s *Store) ClearOut() error {
	s.mu.Lock()
	wasEmpty := len(s.out) == 0
	s.out = nil
	s.mu.Unlock()
	if wasEmpty {
		return nil
	}
	return s.Save()
}

// Save writes the current state atomically.
func (s *Store) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.path == "" {
		return fmt.Errorf("zone template store has no path")
	}
	f := file{Templates: s.templates, Out: s.out}
	if f.Templates == nil {
		f.Templates = []Template{}
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal zone templates: %w", err)
	}
	// Durable write (fsync + rename): a plain write+rename can leave the file at
	// 0 bytes after a speaker's standby power-cut.
	if err := atomicfile.WriteFile(s.path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write zone templates: %w", err)
	}
	return nil
}

// validate checks the caller-settable fields of a template. Name must already
// be trimmed.
func validate(t Template) error {
	if t.Name == "" {
		return fmt.Errorf("template name is empty")
	}
	if utf8.RuneCountInString(t.Name) > maxNameLen {
		return fmt.Errorf("template name longer than %d characters", maxNameLen)
	}
	if len(t.Members) == 0 {
		return fmt.Errorf("template has no members")
	}
	if len(t.Members) > maxMembers {
		return fmt.Errorf("too many members (max %d)", maxMembers)
	}
	for i, m := range t.Members {
		if strings.TrimSpace(m.DeviceID) == "" {
			return fmt.Errorf("member %d has no deviceID", i)
		}
	}
	return nil
}

// indexByIDLocked returns the index of the template with the given id, or -1.
// Callers must hold at least the read lock.
func (s *Store) indexByIDLocked(id string) int {
	if id == "" {
		return -1
	}
	for i, t := range s.templates {
		if t.ID == id {
			return i
		}
	}
	return -1
}

// indexByNameLocked returns the index of the template whose name matches
// case-insensitively, or -1. Callers must hold at least the read lock.
func (s *Store) indexByNameLocked(name string) int {
	for i, t := range s.templates {
		if strings.EqualFold(t.Name, name) {
			return i
		}
	}
	return -1
}

// freshIDLocked allocates an 8-hex-char id not used by any stored template.
// Callers must hold the write lock.
func (s *Store) freshIDLocked() (string, error) {
	for range 32 {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("generate template id: %w", err)
		}
		id := hex.EncodeToString(b[:])
		if s.indexByIDLocked(id) < 0 {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not allocate a unique template id")
}

// matchesOut reports whether an out entry covers the given member, per the
// IsOut matching rule.
func matchesOut(e OutEntry, deviceID, ip string) bool {
	if ip != "" && e.IP != "" && e.IP == ip {
		return true
	}
	if deviceID != "" && e.DeviceID != "" && strings.EqualFold(e.DeviceID, deviceID) {
		return true
	}
	return false
}

// copyTemplate returns a deep copy (the Members slice is not shared).
func copyTemplate(t Template) Template {
	c := t
	c.Members = append([]Member(nil), t.Members...)
	return c
}

// equalTemplates reports whether two templates are deep-equal.
func equalTemplates(a, b Template) bool {
	if a.ID != b.ID || a.Name != b.Name || a.Master != b.Master ||
		a.Permanent != b.Permanent || a.CreatedAt != b.CreatedAt ||
		a.UpdatedAt != b.UpdatedAt || len(a.Members) != len(b.Members) {
		return false
	}
	for i := range a.Members {
		if a.Members[i] != b.Members[i] {
			return false
		}
	}
	return true
}

// nowUTC returns the current time as an RFC3339 UTC timestamp.
func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }
