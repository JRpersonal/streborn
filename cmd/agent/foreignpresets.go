package main

// Foreign preset preservation.
//
// The box treats marge's /preset/list answer as the cloud's source of truth
// and REPLACES its own preset list with it on every re-onboarding. STR's
// answer used to contain only the STR store, so any slot the user still had
// from the Bose days - a Deezer Flow, a TuneIn station, a STORED_MUSIC album -
// silently fell out of the box's own list the next time the box re-read its
// cloud presets (field case 2026-08-17: the user edited STR slot 2, the box
// re-onboarded, and its Deezer slot 3 was gone, while the DEEZER source
// itself survived via the reflect-sources Path A).
//
// This store remembers the box's own presets that STR did NOT write, persisted
// on NAND, so marge can keep serving them alongside the STR slots and the
// firmware never sees a cloud answer that starves them out. Fed from the box's
// gabbo presetsUpdated frames (live) and from the boot-time :8090/presets seed
// read, i.e. from what the BOX says it has - not from the pre-takeover
// snapshot, which the app-side restore action already covers.

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"

	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/webui"
)

// foreignPreset is one box-owned preset slot STR did not write.
type foreignPreset struct {
	Slot          int    `json:"slot"`
	Source        string `json:"source"`
	Type          string `json:"type"`
	Location      string `json:"location"`
	SourceAccount string `json:"sourceAccount"`
	Name          string `json:"name"`
}

// foreignPresetStore persists the foreign slots across reboots. All values are
// stored UNESCAPED (plain text); escaping happens once, at serve time.
type foreignPresetStore struct {
	mu      sync.Mutex
	path    string
	logger  *slog.Logger
	entries map[int]foreignPreset
}

func newForeignPresetStore(path string, logger *slog.Logger) *foreignPresetStore {
	s := &foreignPresetStore{path: path, logger: logger, entries: map[int]foreignPreset{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var list []foreignPreset
	if err := json.Unmarshal(data, &list); err != nil {
		logger.Warn("foreign presets: persisted file unreadable, starting empty", "err", err)
		return s
	}
	for _, e := range list {
		if e.Slot >= 1 && e.Slot <= 6 {
			s.entries[e.Slot] = e
		}
	}
	if len(s.entries) > 0 {
		logger.Info("foreign presets: loaded persisted box-owned slots", "count", len(s.entries))
	}
	return s
}

// isForeignBoxPreset reports whether a box preset was NOT written by STR: it
// carries a location that is neither STR's UPnP stream proxy nor a native
// orion radio form. isOwnBoxPresetLocation alone is not enough here: the box
// reports STR's own native slots with the RELATIVE "/station?data=" location
// (the baseUrl-relative form), which that strict predicate does not match, and
// preserving those would let a deleted STR preset resurrect itself as a
// "foreign" one. Old-cloud orion radio presets are excluded with them - they
// are the same station-descriptor shape, and the STR store, not this
// preservation, is the authority on radio slots. An empty location cannot be
// re-served and is skipped.
func isForeignBoxPreset(location string) bool {
	return location != "" && !isOwnBoxPresetLocation(location) && !isNativeRadioLocation(location)
}

// NoteBoxList ingests a FULL box preset report (gabbo presetsUpdated frame or
// the boot seed read) and replaces the foreign set with what the report says.
//
// An EMPTY report while entries are held is deliberately ignored: the known
// firmware wipe (the box dropping its whole list around a re-login) reports
// exactly that, and honouring it would erase the memory this store exists to
// keep. A foreign slot therefore only disappears when a NON-empty report no
// longer carries it - e.g. the user overwrote the slot with an STR preset.
func (s *foreignPresetStore) NoteBoxList(bps []webui.BoxPreset) {
	fresh := map[int]foreignPreset{}
	for _, p := range bps {
		loc := xmlEntityUnescape(p.Location)
		if p.Slot < 1 || p.Slot > 6 || !isForeignBoxPreset(loc) {
			continue
		}
		fresh[p.Slot] = foreignPreset{
			Slot:          p.Slot,
			Source:        xmlEntityUnescape(p.Source),
			Type:          xmlEntityUnescape(p.Type),
			Location:      loc,
			SourceAccount: xmlEntityUnescape(p.SourceAccount),
			Name:          xmlEntityUnescape(p.Name),
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(bps) == 0 && len(s.entries) > 0 {
		return
	}
	if reflect.DeepEqual(fresh, s.entries) {
		return // no change, no NAND write (presetsUpdated arrives in bursts)
	}
	s.entries = fresh
	s.persistLocked()
}

func (s *foreignPresetStore) persistLocked() {
	list := make([]foreignPreset, 0, len(s.entries))
	for _, e := range s.entries {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Slot < list[j].Slot })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		s.logger.Warn("foreign presets: persist failed", "err", err)
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		s.logger.Warn("foreign presets: persist failed", "err", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		s.logger.Warn("foreign presets: persist failed", "err", err)
		return
	}
	s.logger.Info("foreign presets: preserving box-owned slots in the marge preset answer", "count", len(list))
}

// MargePresets returns the foreign slots as marge presets, escaped for the
// text/template (see the marge.Preset escaping contract), skipping any slot in
// taken (the STR store owns those). Sorted by slot for a stable answer.
func (s *foreignPresetStore) MargePresets(taken map[int]bool) []marge.Preset {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]marge.Preset, 0, len(s.entries))
	for slot, e := range s.entries {
		if taken[slot] {
			continue
		}
		out = append(out, marge.Preset{
			ID:            e.Slot,
			Source:        margeXMLEscape(e.Source),
			Type:          margeXMLEscape(e.Type),
			Location:      margeXMLEscape(e.Location),
			SourceAccount: margeXMLEscape(e.SourceAccount),
			ItemName:      margeXMLEscape(e.Name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// DebugState is the /api/debug/state section: which box-owned slots STR is
// currently preserving, so a bundle answers "did STR know about the Deezer
// preset when the box dropped it" without SSH.
func (s *foreignPresetStore) DebugState() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]foreignPreset, 0, len(s.entries))
	for _, e := range s.entries {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Slot < list[j].Slot })
	return map[string]any{"count": len(list), "slots": list}
}
