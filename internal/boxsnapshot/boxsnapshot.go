// Package boxsnapshot captures the box's own preset list and source list once,
// as early as possible after STR starts, and persists it to NAND.
//
// Why this exists: some SoundTouch owners had account-linked cloud sources
// (Deezer, Amazon Music, ...) bound to hardware presets. Those kept working
// after the Bose cloud shutdown because the box plays them via its own cached
// account token and never reached streaming.bose.com for playback. The moment
// STR comes up it answers streaming.bose.com locally; the box's next account
// sync rebuilds its source list from STR's marge stub, which does not advertise
// those providers, so the box drops the source and every preset bound to it
// (live report, 2x ST10, 2026-06-17: Deezer presets 3/4 vanished).
//
// STR cannot carry those services over yet (a Deezer integration is on the
// roadmap). Until then the least we can do is NOT lose the record silently:
// snapshot the box's presets + sources before they are dropped, persist it, and
// let the desktop app warn the user and show what was there. The snapshot is
// also the input a future Deezer integration needs to restore the links.
package boxsnapshot

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Preset is one preset slot as the box reports it on GET /presets.
type Preset struct {
	Slot          int    `json:"slot"`
	Source        string `json:"source"`
	Type          string `json:"type"`
	Location      string `json:"location"`
	SourceAccount string `json:"sourceAccount"`
	Name          string `json:"name"`
}

// Source is one entry from GET /sources.
type Source struct {
	Source        string `json:"source"`
	SourceAccount string `json:"sourceAccount"`
	Status        string `json:"status"`
	DisplayName   string `json:"displayName"`
}

// Snapshot is the persisted record. CapturedAt is the agent's wall clock at
// capture time (the box clock can be wrong right after boot); it is advisory.
type Snapshot struct {
	CapturedAt   int64    `json:"capturedAt"`
	DeviceID     string   `json:"deviceID"`
	Presets      []Preset `json:"presets"`
	Sources      []Source `json:"sources"`
	LostServices []string `json:"lostServices"`
	LostPresets  []Preset `json:"lostPresets"`
}

// cloudServices are account-linked music services STR cannot carry over after
// it takes over the box's cloud endpoints. Anything here that the box had bound
// to a preset or listed as a source is flagged as "lost". Built-in/local
// sources (AUX, BLUETOOTH, UPNP, ...) and the ones STR serves itself (Spotify,
// internet radio) are never flagged. Match is on the first underscore-delimited
// token so DEEZER_HIFI etc. still hit.
var cloudServices = map[string]bool{
	"DEEZER":   true,
	"AMAZON":   true,
	"PANDORA":  true,
	"SIRIUSXM": true,
	"IHEART":   true,
	"NPR":      true,
	"QQMUSIC":  true,
	"TIDAL":    true,
}

func isCloudService(source string) bool {
	s := strings.ToUpper(strings.TrimSpace(source))
	if s == "" {
		return false
	}
	if i := strings.IndexByte(s, '_'); i > 0 {
		s = s[:i]
	}
	return cloudServices[s]
}

const defaultPath = "/mnt/nv/streborn/box-snapshot.json"
const reflectPath = "/mnt/nv/streborn/reflect-sources.json"

// DefaultPath is where the agent persists the snapshot on the box NAND.
func DefaultPath() string { return defaultPath }

// ReflectPath is where the list of account-linked cloud sources to keep
// advertising to the box (Deezer "Path A") is persisted. The marge stub reads
// it and reflects these back into the account/source-provider responses so the
// box never drops them; the snapshot capture seeds it and the app's restore
// action appends to it.
func ReflectPath() string { return reflectPath }

// ReflectSource is one account-linked cloud source STR keeps advertising so the
// box plays it via its own cached account token (e.g. Deezer via its ARL).
type ReflectSource struct {
	Source  string `json:"source"`  // e.g. "DEEZER"
	Account string `json:"account"` // sourceAccount, e.g. "1456373802"
	Name    string `json:"name"`    // friendly name, e.g. "Deezer"
}

// LoadReflect reads the reflect-sources file. Missing/unreadable/corrupt -> nil
// (Path A then no-ops, which is the safe default on boxes that never had a cloud
// source).
func LoadReflect(path string) []ReflectSource {
	if path == "" {
		path = reflectPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []ReflectSource
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

// MergeReflect writes the union (by source+account) of the existing reflect file
// and add into path, so capture and restore can both contribute without
// clobbering each other. Empty add with an existing file is a no-op.
func MergeReflect(path string, add []ReflectSource) error {
	if path == "" {
		path = reflectPath
	}
	if len(add) == 0 {
		return nil
	}
	key := func(r ReflectSource) string {
		return strings.ToUpper(strings.TrimSpace(r.Source)) + "|" + strings.TrimSpace(r.Account)
	}
	merged := LoadReflect(path)
	seen := map[string]bool{}
	for _, r := range merged {
		seen[key(r)] = true
	}
	for _, r := range add {
		if r.Source == "" {
			continue
		}
		if !seen[key(r)] {
			seen[key(r)] = true
			merged = append(merged, r)
		}
	}
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// reflectFromSources derives reflect entries from a captured /sources list: the
// account-linked cloud sources (Deezer, ...) that STR cannot serve itself.
func reflectFromSources(sources []Source) []ReflectSource {
	var out []ReflectSource
	for _, s := range sources {
		if isCloudService(s.Source) {
			out = append(out, ReflectSource{
				Source:  strings.ToUpper(strings.TrimSpace(s.Source)),
				Account: s.SourceAccount,
				Name:    cloudDisplayName(s.Source, s.DisplayName),
			})
		}
	}
	return out
}

// cloudDisplayName returns a friendly provider name, preferring the box's own
// display name when it is not just the account placeholder.
func cloudDisplayName(source, display string) string {
	d := strings.TrimSpace(display)
	if d != "" && !strings.HasSuffix(strings.ToLower(d), "username") {
		return d
	}
	s := strings.ToUpper(strings.TrimSpace(source))
	if i := strings.IndexByte(s, '_'); i > 0 {
		s = s[:i]
	}
	if s == "" {
		return source
	}
	return s[:1] + strings.ToLower(s[1:])
}

// Capture runs once: it waits (bounded) for the box REST API to answer, reads
// /sources and /presets, and writes the snapshot to path exactly once. It never
// overwrites an existing file, so the earliest pre-takeover state is what sticks
// even across agent restarts. Safe to run in a goroutine; respects ctx.
func Capture(ctx context.Context, boxHost, path string, logger *slog.Logger) {
	if boxHost == "" {
		return
	}
	if path == "" {
		path = defaultPath
	}
	if _, err := os.Stat(path); err == nil {
		// Already captured. Never overwrite: a later read could miss a
		// source the box has since dropped.
		logger.Debug("box snapshot already present, skipping", "path", path)
		return
	}
	client := &http.Client{Timeout: 6 * time.Second}
	// The box REST API (:8090) comes up ~20-45s into a cold boot. Poll until it
	// answers with a real source list, then capture immediately. Bounded so a
	// box that never answers does not leave a goroutine spinning forever.
	const (
		pollEvery = 4 * time.Second
		window    = 3 * time.Minute
	)
	deadline := time.Now().Add(window)
	for {
		deviceID, sources, err := fetchSources(ctx, client, boxHost)
		if err == nil && len(sources) > 0 {
			snap := build(ctx, client, boxHost, deviceID, sources)
			if err := write(path, snap); err != nil {
				logger.Warn("box snapshot write failed", "err", err, "path", path)
				return
			}
			// Seed Path A: keep advertising the account-linked cloud sources the
			// box already had so it does not drop them on the next account sync.
			if refl := reflectFromSources(snap.Sources); len(refl) > 0 {
				if err := MergeReflect(reflectPath, refl); err != nil {
					logger.Warn("reflect-sources seed failed", "err", err)
				} else {
					logger.Info("reflect-sources seeded from snapshot", "count", len(refl))
				}
			}
			logger.Info("box snapshot captured",
				"path", path,
				"presets", len(snap.Presets),
				"lostServices", strings.Join(snap.LostServices, ","))
			return
		}
		if time.Now().After(deadline) {
			logger.Debug("box snapshot: box REST API never returned a source list within window, giving up")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollEvery):
		}
	}
}

func build(ctx context.Context, client *http.Client, boxHost, deviceID string, sources []Source) Snapshot {
	presets, _ := fetchPresets(ctx, client, boxHost)
	snap := Snapshot{
		CapturedAt: time.Now().Unix(),
		DeviceID:   deviceID,
		Presets:    presets,
		Sources:    sources,
	}
	snap.LostServices, snap.LostPresets = analyze(presets, sources)
	return snap
}

// analyze returns the distinct cloud services at risk and the presets bound to
// them. A service counts if it is bound to a preset OR listed as a (non
// UNAVAILABLE) source, so we catch both a Deezer preset and a Deezer source
// that has no preset yet.
func analyze(presets []Preset, sources []Source) (services []string, lostPresets []Preset) {
	seen := map[string]bool{}
	add := func(src string) {
		s := strings.ToUpper(strings.TrimSpace(src))
		if i := strings.IndexByte(s, '_'); i > 0 {
			s = s[:i]
		}
		if s != "" && !seen[s] {
			seen[s] = true
			services = append(services, s)
		}
	}
	for _, p := range presets {
		if isCloudService(p.Source) {
			lostPresets = append(lostPresets, p)
			add(p.Source)
		}
	}
	for _, s := range sources {
		if isCloudService(s.Source) && !strings.EqualFold(s.Status, "UNAVAILABLE") {
			add(s.Source)
		}
	}
	return services, lostPresets
}

func fetchSources(ctx context.Context, client *http.Client, boxHost string) (string, []Source, error) {
	body, err := get(ctx, client, boxHost, "/sources")
	if err != nil {
		return "", nil, err
	}
	return parseSources(body)
}

func fetchPresets(ctx context.Context, client *http.Client, boxHost string) ([]Preset, error) {
	body, err := get(ctx, client, boxHost, "/presets")
	if err != nil {
		return nil, err
	}
	return parsePresets(body)
}

func get(ctx context.Context, client *http.Client, boxHost, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s:8090%s", boxHost, path), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: status %d", path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256*1024))
}

func parseSources(body []byte) (string, []Source, error) {
	var raw struct {
		DeviceID string `xml:"deviceID,attr"`
		Items    []struct {
			Source        string `xml:"source,attr"`
			SourceAccount string `xml:"sourceAccount,attr"`
			Status        string `xml:"status,attr"`
			Name          string `xml:",chardata"`
		} `xml:"sourceItem"`
	}
	if err := xml.Unmarshal(body, &raw); err != nil {
		return "", nil, err
	}
	out := make([]Source, 0, len(raw.Items))
	for _, it := range raw.Items {
		out = append(out, Source{
			Source:        it.Source,
			SourceAccount: it.SourceAccount,
			Status:        it.Status,
			DisplayName:   strings.TrimSpace(it.Name),
		})
	}
	return raw.DeviceID, out, nil
}

func parsePresets(body []byte) ([]Preset, error) {
	var raw struct {
		Presets []struct {
			ID      int `xml:"id,attr"`
			Content struct {
				Source        string `xml:"source,attr"`
				Type          string `xml:"type,attr"`
				Location      string `xml:"location,attr"`
				SourceAccount string `xml:"sourceAccount,attr"`
				ItemName      string `xml:"itemName"`
			} `xml:"ContentItem"`
		} `xml:"preset"`
	}
	if err := xml.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]Preset, 0, len(raw.Presets))
	for _, p := range raw.Presets {
		out = append(out, Preset{
			Slot:          p.ID,
			Source:        p.Content.Source,
			Type:          p.Content.Type,
			Location:      p.Content.Location,
			SourceAccount: p.Content.SourceAccount,
			Name:          strings.TrimSpace(p.Content.ItemName),
		})
	}
	return out, nil
}

// Load reads a persisted snapshot. Used by the app's restore action.
func Load(path string) (Snapshot, error) {
	if path == "" {
		path = defaultPath
	}
	var snap Snapshot
	data, err := os.ReadFile(path)
	if err != nil {
		return snap, err
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		return snap, err
	}
	return snap, nil
}

// ParsePresetsXML parses a box /presets XML dump (e.g. one the user saved before
// installing STR) into presets, so the restore action can write them back.
func ParsePresetsXML(body []byte) ([]Preset, error) {
	return parsePresets(body)
}

// CloudPresets returns the subset of presets bound to an account-linked cloud
// service STR cannot serve itself (Deezer, ...).
func CloudPresets(presets []Preset) []Preset {
	var out []Preset
	for _, p := range presets {
		if isCloudService(p.Source) {
			out = append(out, p)
		}
	}
	return out
}

// ReflectFromPresets derives reflect entries (source + account) from a set of
// cloud presets, so restoring presets also re-advertises their sources.
func ReflectFromPresets(presets []Preset) []ReflectSource {
	seen := map[string]bool{}
	var out []ReflectSource
	for _, p := range presets {
		if !isCloudService(p.Source) {
			continue
		}
		k := strings.ToUpper(p.Source) + "|" + p.SourceAccount
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, ReflectSource{
			Source:  strings.ToUpper(strings.TrimSpace(p.Source)),
			Account: p.SourceAccount,
			Name:    cloudDisplayName(p.Source, ""),
		})
	}
	return out
}

func write(path string, snap Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
