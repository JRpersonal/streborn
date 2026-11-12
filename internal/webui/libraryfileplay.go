// A music-library file started through POST /api/play by a client that does
// not say it is one.
//
// The MIME in the play request is what tells the agent a URL is a FILE on a
// media server and not a radio stream. The desktop app and the phone page
// send it, read back from the Recently-played card. A third-party remote that
// replays such a card sends only the card's URL and name (#1871, 2026-11-12).
// The agent then took the file for a radio station: it handed it to the
// speaker as a station, answered "playing", and filed a new card under Radio
// next to the album's Library card.
//
// So a URL the Recently-played ring or a folder key knows as a library file
// is played and filed as one, whoever asks and whatever they leave out. Both
// are in memory.

package webui

// knownLibraryFile reports whether this agent holds fileURL as a
// music-library file, and the MIME and length it keeps for it: the newest
// Library card of the Recently-played ring that replays this URL, else a
// folder key that holds the file. Only an entry that carries a MIME counts,
// because the MIME is what the play needs. All in memory; nothing asks the
// media server.
func (s *Server) knownLibraryFile(fileURL string) (mime string, durationSec int, ok bool) {
	if fileURL == "" {
		return "", 0, false
	}
	if s.recent != nil {
		ring := s.recent.All() // oldest first
		for i := len(ring) - 1; i >= 0; i-- {
			e := ring[i]
			if e.Source != "upnp" || e.CardURL != fileURL || e.Mime == "" {
				continue
			}
			return e.Mime, e.DurationSec, true
		}
	}
	if s.presets != nil {
		for _, p := range s.presets.All() {
			if p.Type != "queue" {
				continue
			}
			for _, it := range p.Items {
				if it.URL == fileURL && it.Mime != "" {
					return it.Mime, it.DurationSec, true
				}
			}
		}
	}
	return "", 0, false
}

// adoptKnownLibraryFile completes a play request that names no MIME for a URL
// this agent knows as a library file, so the play goes the library way (direct,
// seekable, filed under Library) instead of the radio way.
func (s *Server) adoptKnownLibraryFile(req *playRequest) {
	if req.Mime != "" {
		return
	}
	mime, dur, ok := s.knownLibraryFile(req.URL)
	if !ok {
		return
	}
	s.logger.Info("play request: no MIME sent, but this agent knows the URL as a music-library file, playing it as one",
		"title", req.Title, "mime", mime, "url", req.URL)
	req.Mime = mime
	if req.DurationSec <= 0 {
		req.DurationSec = dur
	}
}
