// Package zones persists this box's multiroom membership on NAND so a zone
// auto-reforms after a reboot, standby cycle or Wi-Fi outage, without the user
// re-grouping every day (the historical Bose SoundTouch pain point, #70).
//
// A box is in at most one zone at a time, so the store holds a single optional
// Zone. Members are keyed by deviceID, which is stable; the LAN IP is stored as
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
	var z Zone
	if err := json.Unmarshal(b, &z); err != nil {
		return s, fmt.Errorf("parse zones: %w", err)
	}
	// An object with no master is standalone.
	if z.Master == "" {
		return s, nil
	}
	s.zone = &z
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

// Set persists the zone membership, replacing any existing one.
func (s *Store) Set(z Zone) error {
	s.mu.Lock()
	zc := z
	s.zone = &zc
	s.mu.Unlock()
	return s.Save()
}

// Clear drops the membership (back to standalone) and persists.
func (s *Store) Clear() error {
	s.mu.Lock()
	s.zone = nil
	s.mu.Unlock()
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
	} else if b, err = json.MarshalIndent(s.zone, "", "  "); err != nil {
		return fmt.Errorf("marshal zones: %w", err)
	}
	// Durable write (fsync + rename): a plain write+rename can leave the file at
	// 0 bytes after a speaker's standby power-cut.
	if err := atomicfile.WriteFile(s.path, b, 0o644); err != nil {
		return fmt.Errorf("write zones: %w", err)
	}
	return nil
}
