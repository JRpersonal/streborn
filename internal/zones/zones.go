// Package zones persists this box's multiroom membership on NAND so a zone
// auto-reforms after a reboot, standby cycle or Wi-Fi outage, without the user
// re-grouping every day (the historical Bose SoundTouch pain point, #70).
//
// A box is in at most one zone at a time, so the store holds a single optional
// Zone as the current group. Beside it the store can keep ONE standing
// permanent group: when a group that is not permanent takes the place of a
// permanent one (a group key, speakers added by hand), the permanent group is
// set aside instead of lost, and it becomes the current group again once the
// temporary one ends (Clear). Members are keyed by deviceID, which is stable; the LAN IP is stored as
// a hint but is re-resolved via discovery at reform time because DHCP leases
// change. When Master equals this box's own deviceID, this box leads the zone
// and Slaves lists the followers; otherwise this box is a slave of Master.
//
// The on-disk format is a single JSON object; a missing or empty file means
// "standalone" (no zone), mirroring the lenient presets store.
package zones

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/JRpersonal/streborn/internal/atomicfile"
)

// Member is one speaker in a zone: its stable deviceID plus a last-known IP
// hint and optional firmware role (e.g. "NORMAL", or "LEFT"/"RIGHT" for a
// stereo pair).
type Member struct {
	DeviceID string `json:"deviceID"`
	IP       string `json:"ip,omitempty"`
	Role     string `json:"role,omitempty"`
}

// Zone is this box's persisted membership.
type Zone struct {
	// Master is the deviceID of the zone master. If it equals this box's own
	// deviceID the box leads the zone (Slaves are the followers).
	Master   string   `json:"master"`
	MasterIP string   `json:"masterIP,omitempty"`
	Slaves   []Member `json:"slaves,omitempty"`
	// Stereo marks a left/right stereo pair rather than a multiroom zone.
	Stereo bool `json:"stereo,omitempty"`
	// Mode is how the group plays in sync: "native" (the firmware distributes
	// the master's source to slaves, tightest sync) or "mirror" (each slave's
	// box independently pulls the master's stream URL via UPnP, works more
	// widely but looser sync). Empty is treated as "native". User-switchable so
	// the beta can compare both on real hardware.
	Mode string `json:"mode,omitempty"`
	// Name is an optional user label for the group.
	Name string `json:"name,omitempty"`
	// Permanent opts this group into the play-triggered re-form (#70): when
	// its master starts music, standby members are woken and the zone is
	// re-asserted. Off by default; the user chooses it when forming the group.
	Permanent bool `json:"permanent,omitempty"`
}

// Mirror reports whether this zone uses the per-agent mirror path.
func (z Zone) Mirror() bool { return z.Mode == "mirror" }

// IsMaster reports whether this box (selfDeviceID) leads the zone.
func (z Zone) IsMaster(selfDeviceID string) bool {
	return z.Master != "" && z.Master == selfDeviceID
}

// Store holds the single optional zone and syncs it to disk.
type Store struct {
	path string
	mu   sync.RWMutex
	zone *Zone // nil = standalone
	// standing is the permanent group a temporary group was formed on top of.
	// It is only ever set while zone is set, and Clear brings it back.
	standing *Zone
}

// fileDoc is the on-disk shape: the current group's fields at the top level,
// exactly as before, plus the standing permanent group under its own key. An
// older agent reads the same file and simply does not see the standing one.
type fileDoc struct {
	Zone
	Standing *Zone `json:"standing,omitempty"`
}

// New returns an empty in-memory store with no persistence path.
func New() *Store { return &Store{} }

// Load reads zones.json from path. A missing or empty file yields an empty
// (standalone) store and no error. A parse error returns an empty store plus
// the error so the caller decides whether to continue.
func Load(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("read zones: %w", err)
	}
	if len(b) == 0 {
		return s, nil
	}
	var d fileDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return s, fmt.Errorf("parse zones: %w", err)
	}
	if d.Standing != nil && d.Standing.Master == "" {
		d.Standing = nil
	}
	// An object with no master is standalone. A standing group without a
	// current one cannot be written by this store, but if a file says so the
	// standing group is what the user has.
	if d.Master == "" {
		s.zone = d.Standing
		return s, nil
	}
	z := d.Zone
	s.zone = &z
	s.standing = d.Standing
	return s, nil
}

// Get returns the persisted zone and true if this box is in one.
func (s *Store) Get() (Zone, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.zone == nil {
		return Zone{}, false
	}
	return *s.zone, true
}

// Standing returns the permanent group that is set aside while a temporary
// group is the current one, and true if there is one.
func (s *Store) Standing() (Zone, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.standing == nil {
		return Zone{}, false
	}
	return *s.standing, true
}

// Set persists the zone membership as the current group.
//
// A permanent group is never lost to one that is not. The store used to hold
// exactly one record, so a group key or a speaker added by hand replaced the
// user's permanent group for good, and the permanent group had to be set up
// again by hand every time (reported 2026-11-02, two groups on one main
// speaker). Now a non-permanent group on top of a permanent one sets the
// permanent group aside, and Clear brings it back. A new PERMANENT group is the
// user's new standing choice, so it drops whatever was set aside.
func (s *Store) Set(z Zone) error {
	s.mu.Lock()
	zc := z
	switch {
	case zc.Permanent:
		s.standing = nil
	case s.zone != nil && s.zone.Permanent && s.standing == nil:
		prev := *s.zone
		s.standing = &prev
	}
	s.zone = &zc
	s.mu.Unlock()
	return s.Save()
}

// Clear ends the current group and persists. When a permanent group was set
// aside for it, that permanent group becomes the current one again, so it forms
// on its main speaker's next play exactly as before the temporary group;
// otherwise the box is back to standalone. An already-empty store returns
// without writing: repeated dissolve calls on a standalone box (field: dissolve
// retry storms) must not cost a NAND write each, and an in-memory-nil store
// already mirrors the standalone on-disk state.
func (s *Store) Clear() error {
	s.mu.Lock()
	wasSet := s.zone != nil
	s.zone, s.standing = s.standing, nil
	s.mu.Unlock()
	if !wasSet {
		return nil
	}
	return s.Save()
}

// Forget drops the current group AND any permanent group set aside for it.
// It is the user deleting the saved group, not a group ending.
func (s *Store) Forget() error {
	s.mu.Lock()
	wasSet := s.zone != nil || s.standing != nil
	s.zone, s.standing = nil, nil
	s.mu.Unlock()
	if !wasSet {
		return nil
	}
	return s.Save()
}

// Save writes the current state atomically. Standalone is persisted as an
// empty JSON object so the file's presence still signals "managed by STR".
func (s *Store) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.path == "" {
		return fmt.Errorf("zones store has no path")
	}
	var b []byte
	var err error
	if s.zone == nil {
		b = []byte("{}\n")
	} else if b, err = json.MarshalIndent(fileDoc{Zone: *s.zone, Standing: s.standing}, "", "  "); err != nil {
		return fmt.Errorf("marshal zones: %w", err)
	}
	// Durable write (fsync + rename): a plain write+rename can leave the file at
	// 0 bytes after a speaker's standby power-cut.
	if err := atomicfile.WriteFile(s.path, b, 0o644); err != nil {
		return fmt.Errorf("write zones: %w", err)
	}
	return nil
}
