package webui

import (
	"strings"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
)

// StreamArtChain returns the full pipe-separated logo candidate chain STR
// knows for a radio stream URL, or "" when it knows none (#968). The speaker's
// station descriptor carries ONE picture (firstArtURL picked it for the
// display), so a key saved by the speaker's own hold gesture would otherwise
// get a different, often worse, logo than the same station saved from the
// app. Looked up, in order: a preset already holding that stream, the station
// card that plays now, and the Recently-played ring (newest first).
func (s *Server) StreamArtChain(streamURL string) string {
	return s.streamLookup(streamURL,
		func(p presets.Preset) string { return p.Art },
		func(c recentCardCtx) string { return c.art },
		func(e recent.Entry) string { return e.CardArt })
}

// StreamHomepage returns the station website STR knows for a radio stream URL,
// or "" when it knows none, from the same three places as StreamArtChain. A
// key tile derives its logo from the homepage once the stored chain holds only
// icon-service entries, so a key saved by the speaker's hold without it fell
// back to the stream host's icon and showed a different logo than the same
// station saved from Find stations (#1217).
func (s *Server) StreamHomepage(streamURL string) string {
	return s.streamLookup(streamURL,
		func(p presets.Preset) string { return p.Homepage },
		func(c recentCardCtx) string { return c.homepage },
		func(e recent.Entry) string { return e.Homepage })
}

// streamLookup finds the first non-empty field for streamURL: a preset holding
// the stream, then the station card that plays now, then the Recently-played
// ring, newest first.
func (s *Server) streamLookup(streamURL string, fromPreset func(presets.Preset) string,
	fromCard func(recentCardCtx) string, fromEntry func(recent.Entry) string) string {
	u := strings.TrimSpace(streamURL)
	if u == "" {
		return ""
	}
	if s.presets != nil {
		for _, p := range s.presets.All() {
			if v := strings.TrimSpace(fromPreset(p)); strings.TrimSpace(p.StreamURL) == u && v != "" {
				return v
			}
		}
	}
	s.recentMu.Lock()
	cur := s.recentRadioCard
	s.recentMu.Unlock()
	if v := strings.TrimSpace(fromCard(cur)); strings.TrimSpace(cur.url) == u && v != "" {
		return v
	}
	if s.recent != nil {
		all := s.recent.All()
		for i := len(all) - 1; i >= 0; i-- {
			e := all[i]
			if v := strings.TrimSpace(fromEntry(e)); e.Source == "radio" && strings.TrimSpace(e.CardURL) == u && v != "" {
				return v
			}
		}
	}
	return ""
}
