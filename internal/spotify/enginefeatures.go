// enginefeatures.go: which optional API behaviour the running go-librespot
// build supports. The fork lists it on GET / ("features"); an older build
// answers GET / without the list and decodes unknown request fields away
// silently, so a request that relies on one has to check first.

package spotify

import (
	"context"
	"encoding/json"
	"time"
)

// engineFeaturePlayShuffleContext: /player/play takes shuffle_context and
// applies it before the context loads, so a shuffled play starts on a random
// track with a single load (JRpersonal/go-librespot#10).
const engineFeaturePlayShuffleContext = "play_shuffle_context"

// engineFeatureProbeTimeout caps the one GET / per engine process. It runs on
// the recall path, so a wedged engine must not hold the press up for long.
const engineFeatureProbeTimeout = 2 * time.Second

// resetEngineFeaturesLocked forgets what the previous engine process
// supported: a new process can be a different build (the OTA sidecar swap).
// Caller holds m.mu.
func (m *Manager) resetEngineFeaturesLocked() {
	m.engineFeatures = nil
	m.enginePrevNoSeek = false
	m.engineFeaturesGen++
}

// engineSupports reports whether the running engine build lists feature on
// GET /. The answer is read once per engine process and kept. A failed read
// counts as "not supported" and is not kept, so the next call asks again.
func (m *Manager) engineSupports(ctx context.Context, feature string) bool {
	m.mu.Lock()
	known, gen := m.engineFeatures, m.engineFeaturesGen
	m.mu.Unlock()
	if known != nil {
		return known[feature]
	}
	pctx, cancel := context.WithTimeout(ctx, engineFeatureProbeTimeout)
	defer cancel()
	features, list, ok := m.readEngineFeatures(pctx)
	if !ok {
		return false
	}
	m.mu.Lock()
	stored := m.engineFeaturesGen == gen && m.engineFeatures == nil
	if stored {
		m.engineFeatures = features
	}
	m.mu.Unlock()
	if stored {
		m.logger.Info("spotify: engine API features", "features", list)
	}
	return features[feature]
}

// readEngineFeatures reads GET /. ok is false when the answer is not the
// engine's root (unreachable, an error page, an unparsable body); an engine
// that answers without "features" is an older build and supports none.
func (m *Manager) readEngineFeatures(ctx context.Context) (set map[string]bool, list []string, ok bool) {
	if m.client == nil {
		return nil, nil, false
	}
	data, err := m.apiGet(ctx, "/")
	if err != nil {
		return nil, nil, false
	}
	var root struct {
		PlaybackReady *bool    `json:"playback_ready"`
		Features      []string `json:"features"`
	}
	if json.Unmarshal(data, &root) != nil || root.PlaybackReady == nil {
		return nil, nil, false
	}
	set = make(map[string]bool, len(root.Features))
	for _, f := range root.Features {
		set[f] = true
	}
	return set, root.Features, true
}

// prevNoSeekKnown reports what engineHasPrevNoSeek has already established,
// without asking the engine.
func (m *Manager) prevNoSeekKnown() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enginePrevNoSeek
}

// enginePrevNoSeekProbeTimeout caps the probe below.
const enginePrevNoSeekProbeTimeout = time.Second

// engineHasPrevNoSeek reports whether the running engine's /player/prev takes
// allow_seeking. The build does not list it as a feature, so the probe asks
// for /player/queue, which reached the fork's master in the same merge
// (JRpersonal/go-librespot 897d5f8, 2026-11-16): an engine that answers it
// with a play order has the switch. Only a yes is kept; anything else (an
// older build's 404, no session, no answer in time) reads as no and is asked
// again when the next song starts (startTrackHeardLocked), while the engine is
// playing and answers at once.
func (m *Manager) engineHasPrevNoSeek(ctx context.Context) bool {
	m.mu.Lock()
	known, gen := m.enginePrevNoSeek, m.engineFeaturesGen
	m.mu.Unlock()
	if known {
		return true
	}
	if m.client == nil {
		return false
	}
	pctx, cancel := context.WithTimeout(ctx, enginePrevNoSeekProbeTimeout)
	defer cancel()
	data, err := m.apiGet(pctx, "/player/queue")
	if err != nil {
		return false
	}
	var q struct {
		Next *[]json.RawMessage `json:"next_tracks"`
	}
	if json.Unmarshal(data, &q) != nil || q.Next == nil {
		return false
	}
	m.mu.Lock()
	if m.engineFeaturesGen == gen {
		m.enginePrevNoSeek = true
	}
	m.mu.Unlock()
	m.logger.Info("spotify: engine goes to the previous song without a rewind (allow_seeking)")
	return true
}
