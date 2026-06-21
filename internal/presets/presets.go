// Package presets reads and writes the preset configuration on the USB stick.
//
// The persistence format accepts two variants:
//
//  1. Array directly:         [{...}, {...}]
//  2. Object with "presets":  {"presets": [{...}, ...]}
//
// Field names are robust against the different wizard versions:
//
//	"slot" or "id"                 -> Slot
//	"name"                          -> Name
//	"stream_url" or "url"          -> StreamURL
//	"type"                          -> Type ("radio", "spotify", ...)
//	"art"                           -> Art (cover image URL, optional)
package presets

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Preset describes a single preset slot.
type Preset struct {
	Slot      int    `json:"slot"`
	Name      string `json:"name"`
	StreamURL string `json:"stream_url"`
	Type      string `json:"type"`
	Art       string `json:"art,omitempty"`
	// Bitrate in kbit/s as reported by radio-browser at save time, 0 when
	// unknown. Persisted so the desktop app can show it on preset buttons
	// and in now-playing without a live lookup. Optional/additive: older
	// presets simply have 0.
	Bitrate int `json:"bitrate,omitempty"`
	// Spotify presets (Type=="spotify") carry these instead of a playable
	// StreamURL: URI is the Spotify resource to play (e.g.
	// "spotify:playlist:..."), recalled via librespot, not UPnP. Account
	// is which Spotify account the preset belongs to, so several household
	// members can each save their own playlists and a tile can show whose
	// it is (a thing the Bose original never did). Both optional/additive.
	URI     string `json:"uri,omitempty"`
	Account string `json:"account,omitempty"`
	// Source labels where a preset came from when it is not a radio-browser
	// station, e.g. the DLNA/UPnP media server name for a preset saved from the
	// Library tab. Purely cosmetic: the desktop app shows it as a small "from"
	// badge on the preset. Optional/additive: radio and Spotify presets leave it
	// empty.
	Source string `json:"source,omitempty"`
	// Homepage is the radio station website, kept so a preset recall can offer
	// the same "website" link as the radio search rows in Recently-played (#135).
	// Optional/additive: presets saved before this, or non-radio, leave it empty.
	Homepage string `json:"homepage,omitempty"`
	// Queue presets (Type=="queue") save a whole DLNA folder as a preset. They
	// carry no single StreamURL/URI; instead Items holds the ordered tracks and
	// Shuffle records whether the folder was saved with shuffle on, so a recall
	// (soft or hardware) restarts the same library play-queue. Both
	// optional/additive: every other preset type leaves them empty.
	Shuffle bool         `json:"shuffle,omitempty"`
	Items   []PresetItem `json:"items,omitempty"`
}

// PresetItem is one track in a queue preset (Type=="queue"). It mirrors the
// agent-side queueItem fields the play path needs, so a saved folder can be
// reloaded straight into the play queue. DurationSec is the track length in
// seconds (0 when the DLNA server reported none).
type PresetItem struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Art         string `json:"art,omitempty"`
	Mime        string `json:"mime,omitempty"`
	DurationSec int    `json:"duration_sec,omitempty"`
}

// rawPreset is the disk format helper. Accepts multiple alias fields.
type rawPreset struct {
	Slot      int          `json:"slot"`
	ID        int          `json:"id"`
	Name      string       `json:"name"`
	StreamURL string       `json:"stream_url"`
	URL       string       `json:"url"`
	Type      string       `json:"type"`
	Art       string       `json:"art"`
	Bitrate   int          `json:"bitrate"`
	URI       string       `json:"uri"`
	Account   string       `json:"account"`
	Source    string       `json:"source"`
	Homepage  string       `json:"homepage"`
	Shuffle   bool         `json:"shuffle"`
	Items     []PresetItem `json:"items"`
}

// rawWrapper supports the object format {"presets": [...]}.
type rawWrapper struct {
	Presets []rawPreset `json:"presets"`
}

// Store holds all presets in memory and synchronizes them with the file.
type Store struct {
	path string
	mu   sync.RWMutex
	data []Preset
}

// New creates an empty Store without a persistence path.
func New() *Store { return &Store{} }

// Load reads the presets.json from the given path.
// If the file does not exist or is empty, an empty Store is returned.
// On parse error an empty Store is also returned plus the error so the
// caller decides whether it crashes or continues.
func Load(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("read presets: %w", err)
	}
	if len(b) == 0 {
		return s, nil
	}

	// First try the array format
	var arr []rawPreset
	if err := json.Unmarshal(b, &arr); err == nil {
		s.data = normalize(arr)
		return s, nil
	}

	// Then the object format with "presets" wrapper
	var wrap rawWrapper
	if err := json.Unmarshal(b, &wrap); err == nil && wrap.Presets != nil {
		s.data = normalize(wrap.Presets)
		return s, nil
	}

	return s, fmt.Errorf("parse presets: unknown format in %s", path)
}

// normalize converts rawPreset into Preset, with alias resolution.
func normalize(in []rawPreset) []Preset {
	out := make([]Preset, 0, len(in))
	for _, p := range in {
		slot := p.Slot
		if slot == 0 {
			slot = p.ID
		}
		stream := p.StreamURL
		if stream == "" {
			stream = p.URL
		}
		typ := p.Type
		if typ == "" {
			typ = "radio"
		}
		out = append(out, Preset{
			Slot:      slot,
			Name:      p.Name,
			StreamURL: stream,
			Type:      typ,
			Art:       p.Art,
			Bitrate:   p.Bitrate,
			URI:       p.URI,
			Account:   p.Account,
			Source:    p.Source,
			Homepage:  p.Homepage,
			Shuffle:   p.Shuffle,
			Items:     p.Items,
		})
	}
	return out
}

// All returns a copy of all presets.
func (s *Store) All() []Preset {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Preset, len(s.data))
	copy(out, s.data)
	return out
}

// Get returns the preset for the given slot.
func (s *Store) Get(slot int) (Preset, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.data {
		if p.Slot == slot {
			return p, true
		}
	}
	return Preset{}, false
}

// SetSlot adds a preset or replaces the existing one for the same slot.
// Persists immediately.
func (s *Store) SetSlot(p Preset) error {
	s.mu.Lock()
	replaced := false
	for i, existing := range s.data {
		if existing.Slot == p.Slot {
			s.data[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		s.data = append(s.data, p)
	}
	s.mu.Unlock()
	return s.Save()
}

// RemoveSlot removes the preset for the given slot.
func (s *Store) RemoveSlot(slot int) error {
	s.mu.Lock()
	out := make([]Preset, 0, len(s.data))
	for _, p := range s.data {
		if p.Slot != slot {
			out = append(out, p)
		}
	}
	s.data = out
	s.mu.Unlock()
	return s.Save()
}

// Save writes the presets back in the object format ({"presets":[...]}).
// That is also the format the wizard writes, so both are compatible.
func (s *Store) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.path == "" {
		return fmt.Errorf("Store has no path")
	}
	wrapper := struct {
		Presets []Preset `json:"presets"`
	}{Presets: s.data}
	b, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize presets: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write presets: %w", err)
	}
	return os.Rename(tmp, s.path)
}
