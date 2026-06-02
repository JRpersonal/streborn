// Package presets liest und schreibt die Preset Konfiguration auf dem USB Stick.
//
// Das Persistenz Format akzeptiert zwei Varianten:
//
//  1. Array direkt:           [{...}, {...}]
//  2. Object mit "presets":   {"presets": [{...}, ...]}
//
// Feld Namen sind robust gegen die verschiedenen Wizard Versionen:
//
//   "slot" oder "id"               -> Slot
//   "name"                          -> Name
//   "stream_url" oder "url"        -> StreamURL
//   "type"                          -> Type ("radio", "spotify", ...)
//   "art"                           -> Art (Coverbild URL, optional)
package presets

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Preset beschreibt einen einzelnen Preset Slot.
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
}

// rawPreset ist der Disk Format Helper. Akzeptiert mehrere Alias Felder.
type rawPreset struct {
	Slot      int    `json:"slot"`
	ID        int    `json:"id"`
	Name      string `json:"name"`
	StreamURL string `json:"stream_url"`
	URL       string `json:"url"`
	Type      string `json:"type"`
	Art       string `json:"art"`
	Bitrate   int    `json:"bitrate"`
}

// rawWrapper unterstuetzt das Object Format {"presets": [...]}.
type rawWrapper struct {
	Presets []rawPreset `json:"presets"`
}

// Store hält alle Presets im Speicher und synchronisiert sie mit der Datei.
type Store struct {
	path string
	mu   sync.RWMutex
	data []Preset
}

// New erzeugt einen leeren Store ohne Persistenz Pfad.
func New() *Store { return &Store{} }

// Load liest die presets.json vom angegebenen Pfad.
// Existiert die Datei nicht oder ist leer, wird ein leerer Store zurueck.
// Bei Parse Fehler wird ebenfalls ein leerer Store geliefert plus der Fehler
// damit der Aufrufer entscheidet ob er crasht oder weitermacht.
func Load(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("presets lesen: %w", err)
	}
	if len(b) == 0 {
		return s, nil
	}

	// Erst versuche das Array Format
	var arr []rawPreset
	if err := json.Unmarshal(b, &arr); err == nil {
		s.data = normalize(arr)
		return s, nil
	}

	// Dann das Object Format mit "presets" Wrapper
	var wrap rawWrapper
	if err := json.Unmarshal(b, &wrap); err == nil && wrap.Presets != nil {
		s.data = normalize(wrap.Presets)
		return s, nil
	}

	return s, fmt.Errorf("presets parsen: unbekanntes Format in %s", path)
}

// normalize wandelt rawPreset in Preset um, mit Alias Aufloesung.
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
		})
	}
	return out
}

// All liefert eine Kopie aller Presets.
func (s *Store) All() []Preset {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Preset, len(s.data))
	copy(out, s.data)
	return out
}

// Get liefert den Preset für den angegebenen Slot.
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

// SetSlot fuegt ein Preset hinzu oder ersetzt das vorhandene fuer den
// gleichen Slot. Persistiert sofort.
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

// RemoveSlot entfernt das Preset fuer den angegebenen Slot.
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

// Save schreibt die Presets im Object Format ({"presets":[...]}) zurueck.
// Das ist auch das Format das der Wizard schreibt, damit beides kompatibel ist.
func (s *Store) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.path == "" {
		return fmt.Errorf("Store hat keinen Pfad")
	}
	wrapper := struct {
		Presets []Preset `json:"presets"`
	}{Presets: s.data}
	b, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return fmt.Errorf("presets serialisieren: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("presets schreiben: %w", err)
	}
	return os.Rename(tmp, s.path)
}
