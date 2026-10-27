// Package keyactions holds the built-in actions a remote key can carry
// instead of a webhook or a saved group:
//
//   - "sleep" steps the sleep timer on each press, 15 -> 30 -> 60 minutes ->
//     off (the stepping itself lives with the timer, internal/webui
//     sleepkey.go).
//   - "snooze" snoozes a ringing alarm (discussion #1415, internal/webui
//     alarmsnooze.go). Outside a ringing alarm it does nothing.
//   - "bassUp" and "bassDown" move the speaker's bass one step up or down on
//     each press (discussion #1494, internal/webui basskey.go), within the
//     range the speaker reports. On a speaker without adjustable bass they do
//     nothing.
//
// Which key may carry which action follows from what the firmware does with
// the key on its own. The thumbs keys do nothing by themselves, so every
// action may go there. On back, forward and play/pause the firmware's own
// reaction always runs too, so no action goes there. AUX is the one key in
// between: the firmware always switches to the AUX input, and STR only sees
// the press as that source change. That is exactly what silences a ringing
// alarm, so snooze may go on AUX; the sleep timer and the bass steps may not,
// because each press would also switch the input away from whatever plays.
//
// A key carries ONE thing: a saved group (internal/groupkeys, thumbs keys
// only), a key action from here, or a webhook. Group and key action exclude
// each other and both saves check it (Validate here, the group-keys PUT on
// the other side); either of them wins over a webhook on the same key, as the
// group already did.
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
	// KeyAux is the AUX key, seen as the source change to AUX.
	KeyAux = "aux"
)

// The actions a key can carry.
const (
	// ActionSleep steps the sleep timer on every press.
	ActionSleep = "sleep"
	// ActionSnooze snoozes a ringing alarm and does nothing otherwise.
	ActionSnooze = "snooze"
	// ActionBassUp raises the speaker's bass one step on every press.
	ActionBassUp = "bassUp"
	// ActionBassDown lowers the speaker's bass one step on every press.
	ActionBassDown = "bassDown"
)

// actions maps the lower-cased name of every known action to its canonical
// spelling, so a save in any case normalises to the constants above.
var actions = map[string]string{
	"sleep":    ActionSleep,
	"snooze":   ActionSnooze,
	"bassup":   ActionBassUp,
	"bassdown": ActionBassDown,
}

func thumbs(id string) bool { return id == KeyThumbsUp || id == KeyThumbsDown }

// BindableKey reports whether id is a key some action may be bound to.
func BindableKey(id string) bool { return thumbs(id) || id == KeyAux }

// knownAction reports whether a is an action this agent can run.
func knownAction(a string) bool { _, ok := actions[strings.ToLower(a)]; return ok }

// IsBass reports whether a is one of the two bass steps.
func IsBass(a string) bool { return a == ActionBassUp || a == ActionBassDown }

// describe names an action in an error the user reads.
func describe(a string) string {
	switch a {
	case ActionSleep:
		return "the sleep timer"
	case ActionBassUp, ActionBassDown:
		return "the bass step"
	}
	return a
}

// AllowedOn reports whether action may be bound to key. See the package
// comment for why AUX takes snooze but neither the sleep timer nor the bass
// steps.
func AllowedOn(action, key string) bool {
	switch action {
	case ActionSleep, ActionBassUp, ActionBassDown:
		return thumbs(key)
	case ActionSnooze:
		return thumbs(key) || key == KeyAux
	}
	return false
}

// Document is the whole per-speaker file: key id -> action.
type Document struct {
	Keys map[string]string `json:"keys"`
}

// Validate checks the document the app saved and normalises it (trimmed,
// action names in their canonical spelling, empty entries dropped). groupBound reports
// whether a key already carries a saved group on this speaker; nil means no
// key does. The error is shown to the user verbatim.
func (d *Document) Validate(groupBound func(key string) bool) error {
	out := map[string]string{}
	for key, action := range d.Keys {
		action = strings.TrimSpace(action)
		if action == "" {
			continue
		}
		if !BindableKey(key) {
			return fmt.Errorf("key %q cannot carry an action, only the thumbs keys and AUX can", key)
		}
		if !knownAction(action) {
			return fmt.Errorf("unknown key action %q", action)
		}
		action = actions[strings.ToLower(action)]
		if !AllowedOn(action, key) {
			return fmt.Errorf("%s can only go on a thumbs key: AUX always switches the input as well", describe(action))
		}
		if groupBound != nil && groupBound(key) {
			return fmt.Errorf("key %q already carries a saved group; a key carries either a group or a key action, remove the group first", key)
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
