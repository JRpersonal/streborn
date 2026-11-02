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
