// Package mediaservers remembers which DLNA/UPnP media servers the user turned
// into native music sources on this box, so they are still there after a
// reboot.
//
// They are not otherwise. The speaker accepts the registration and keeps it
// across a restart just long enough to look convincing: measured on a Portable,
// the source was present for the first ~70 s after boot and then disappeared.
// The reason is that the box re-checks the account against its marge, STR's
// record of registered sources lives only in memory, and the agent restarted
// with the box, so the box was told the account has no sources and dropped it.
//
// Persisting the user's choice here lets the agent put it back once at startup.
// It stores the user's INTENT, not box state: the box remains the authority on
// what is currently registered, and the agent reconciles towards this list.
package mediaservers

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/JRpersonal/streborn/internal/atomicfile"
)

// Server is one media server the user enabled as a music source.
type Server struct {
	// ID is the UPnP UDN without the "uuid:" prefix, as the box reports it.
	ID string `json:"id"`
	// Name is the server's friendly name, kept so a re-registration after a
	// reboot uses the same label the user saw, and so the box can be told the
	// display name when the source is removed again.
	Name string `json:"name,omitempty"`
}

// Store is the persisted set of enabled servers.
type Store struct {
	path    string
	mu      sync.RWMutex
	servers map[string]Server
}

// New returns an empty in-memory store with no persistence path.
func New() *Store { return &Store{servers: map[string]Server{}} }

// Load reads the store from path. A missing or empty file yields an empty store
// and no error: nothing enabled is the normal state, not a fault.
func Load(path string) (*Store, error) {
	s := &Store{path: path, servers: map[string]Server{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("read media servers: %w", err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return s, nil
	}
	var list []Server
	if err := json.Unmarshal(b, &list); err != nil {
		return s, fmt.Errorf("parse media servers: %w", err)
	}
	for _, srv := range list {
		if id := strings.TrimSpace(srv.ID); id != "" {
			srv.ID = id
			s.servers[id] = srv
		}
	}
	return s, nil
}

// List returns the enabled servers, ordered by name so the UI is stable.
func (s *Store) List() []Server {
	s.mu.RLock()
	out := make([]Server, 0, len(s.servers))
	for _, srv := range s.servers {
		out = append(out, srv)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Has reports whether a server id is enabled.
func (s *Store) Has(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.servers[strings.TrimSpace(id)]
	return ok
}

// Add enables a server and persists. Adding one that is already stored with the
// same name does not write: enabling is idempotent and a repeat must not cost a
// NAND write.
func (s *Store) Add(srv Server) error {
	srv.ID = strings.TrimSpace(srv.ID)
	if srv.ID == "" {
		return fmt.Errorf("media server has no id")
	}
	s.mu.Lock()
	if cur, ok := s.servers[srv.ID]; ok && cur.Name == srv.Name {
		s.mu.Unlock()
		return nil
	}
	s.servers[srv.ID] = srv
	s.mu.Unlock()
	return s.Save()
}

// Remove disables a server and persists. Removing one that is not stored does
// not write, for the same reason Add is idempotent.
func (s *Store) Remove(id string) error {
	id = strings.TrimSpace(id)
	s.mu.Lock()
	_, existed := s.servers[id]
	delete(s.servers, id)
	s.mu.Unlock()
	if !existed {
		return nil
	}
	return s.Save()
}

// Save writes the current set atomically.
func (s *Store) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.path == "" {
		return fmt.Errorf("media server store has no path")
	}
	list := make([]Server, 0, len(s.servers))
	for _, srv := range s.servers {
		list = append(list, srv)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal media servers: %w", err)
	}
	// Durable write (fsync + rename): a plain write+rename can leave the file at
	// 0 bytes after a speaker's standby power-cut.
	if err := atomicfile.WriteFile(s.path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write media servers: %w", err)
	}
	return nil
}
