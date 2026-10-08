package webui

import "strings"

// StreamArtChain returns the full pipe-separated logo candidate chain STR
// knows for a radio stream URL, or "" when it knows none (#968). The speaker's
// station descriptor carries ONE picture (firstArtURL picked it for the
// display), so a key saved by the speaker's own hold gesture would otherwise
// get a different, often worse, logo than the same station saved from the
// app. Looked up, in order: a preset already holding that stream, the station
// card that plays now, and the Recently-played ring (newest first).
func (s *Server) StreamArtChain(streamURL string) string {
	u := strings.TrimSpace(streamURL)
	if u == "" {
		return ""
	}
	if s.presets != nil {
		for _, p := range s.presets.All() {
			if strings.TrimSpace(p.StreamURL) == u && strings.TrimSpace(p.Art) != "" {
				return p.Art
			}
		}
	}
	s.recentMu.Lock()
	cur := s.recentRadioCard
	s.recentMu.Unlock()
	if strings.TrimSpace(cur.url) == u && strings.TrimSpace(cur.art) != "" {
		return cur.art
	}
	if s.recent != nil {
		all := s.recent.All()
		for i := len(all) - 1; i >= 0; i-- {
			e := all[i]
			if e.Source == "radio" && strings.TrimSpace(e.CardURL) == u && strings.TrimSpace(e.CardArt) != "" {
				return e.CardArt
			}
		}
	}
	return ""
}
