// Package keyactions holds the built-in actions a remote key can carry
// instead of a webhook or a saved group. The first and so far only one is the
// sleep timer: each press of the key steps it 15 -> 30 -> 60 minutes -> off
// (the stepping itself lives with the timer, internal/webui sleeptimer.go).
//
// Only the thumbs keys can carry an action. On back, forward and play/pause
// the firmware's own reaction always runs too, so an action there would fire
// on top of a skip or a pause; the thumbs keys do nothing on their own.
//
// A thumbs key carries ONE thing: a saved group (internal/groupkeys), a key
// action from here, or a webhook. Group and key action exclude each other and
// both saves check it (Validate here, the group-keys PUT on the other side);
// either of them wins over a webhook on the same key, as the group already
// did.
//
// The document lives on the speaker whose remote is pressed and is written
// only when the app saves it. A press writes nothing.
package keyactions

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/JRpersonal/streborn/internal/atomicfile"
)

// Key ids an action can be bound to: the webhooks trigger ids of the same
// keys, as in internal/groupkeys.
const (
	KeyThumbsUp   = "thumbsUp"
	KeyThumbsDown = "thumbsDown"
)

// ActionSleep steps the sleep timer on every press.
const ActionSleep = "sleep"

// BindableKey reports whether id is a key an action may be bound to.
func BindableKey(id string) bool { return id == KeyThumbsUp || id == KeyThumbsDown }

// knownAction reports whether a is an action this agent can run.
func knownAction(a string) bool { return a == ActionSleep }

// Document is the whole per-speaker file: key id -> action.
type Document struct {
	Keys map[string]string `json:"keys"`
}

// Validate checks the document the app saved and normalises it (trimmed,
// lower-cased action names, empty entries dropped). groupBound reports
// whether a key already carries a saved group on this speaker; nil means no
// key does. The error is shown to the user verbatim.
func (d *Document) Validate(groupBound func(key string) bool) error {
	out := map[string]string{}
	for key, action := range d.Keys {
		action = strings.ToLower(strings.TrimSpace(action))
		if action == "" {
			continue
		}
		if !BindableKey(key) {
			return fmt.Errorf("key %q cannot carry an action, only the thumbs keys can", key)
		}
		if !knownAction(action) {
			return fmt.Errorf("unknown key action %q", action)
		}
		if groupBound != nil && groupBound(key) {
			return fmt.Errorf("key %q already carries a saved group; a key carries either a group or the sleep timer, remove the group first", key)
		}
		out[key] = action
	}
	d.Keys = out
	return nil
}

// Store is the NAND-persisted document.
type Store struct {
	path   string
	logger *slog.Logger

	mu  sync.RWMutex
	doc Document
}

// Load reads the document from path. A missing or empty file is an empty
// document; a file that does not parse is reported and treated as empty.
func Load(path string, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{path: path, logger: logger, doc: Document{Keys: map[string]string{}}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("read key actions: %w", err)
	}
	if len(b) == 0 {
		return s, nil
	}
	var d Document
	if err := json.Unmarshal(b, &d); err != nil {
		return s, fmt.Errorf("parse key actions: %w", err)
	}
	// No group check at load: the group-keys PUT refuses a key that carries
	// an action, so a stored conflict can only come from a hand edit, and the
	// press handler lets the group win then.
	if err := d.Validate(nil); err != nil {
		logger.Warn("key actions: stored document has a defect, ignoring it", "err", err)
		return s, nil
	}
	s.doc = d
	return s, nil
}

// Get returns a copy of the document.
func (s *Store) Get() Document {
	if s == nil {
		return Document{Keys: map[string]string{}}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.doc)
}

// Set validates, replaces and persists the document: the only NAND write in
// this package, on a save from the app.
func (s *Store) Set(d Document, groupBound func(key string) bool) error {
	if err := d.Validate(groupBound); err != nil {
		return err
	}
	s.mu.Lock()
	s.doc = clone(d)
	s.mu.Unlock()
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	// Durable write (fsync + rename), as for the group keys.
	return atomicfile.WriteFile(s.path, b, 0o644)
}

// Action returns the action bound to key ("" when none). nil-safe.
func (s *Store) Action(key string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doc.Keys[key]
}

// Bound reports whether key carries any action. nil-safe.
func (s *Store) Bound(key string) bool { return s.Action(key) != "" }

// Snapshot is the debug-section view.
func (s *Store) Snapshot() any {
	if s == nil {
		return nil
	}
	return s.Get().Keys
}

func clone(d Document) Document {
	out := Document{Keys: make(map[string]string, len(d.Keys))}
	for k, v := range d.Keys {
		out.Keys[k] = v
	}
	return out
}
