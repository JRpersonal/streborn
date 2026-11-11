package webui

import (
	"strings"

	"github.com/JRpersonal/streborn/internal/presets"
)

// Artist and album of a single library track, whichever way it was started
// (#1839).
//
// The speaker reports neither for a file it fetches itself. A play from the
// Library names them in its request (lonefilemeta_test.go), but a preset key,
// a favourite, a card of Recently played and the power-on resume start the
// same file from a stored entry, and those entries kept the title and the
// address only. So the entries keep both now (presets.Preset, Favorite,
// recent.Entry, the last-play record), every start carries them into the lone
// file GET /api/queue reports, and an entry stored before that is labelled
// from whatever this agent still holds for the same file. Nothing here asks
// the media server: a key press must not wait on a browse for a label.

// knownTrackLabel returns the artist and album this agent holds for the
// library file at fileURL, and where it found them ("" when it knows none):
// the play on record, the Recently-played ring, or a key that holds the file.
// All three are in memory.
func (s *Server) knownTrackLabel(fileURL string) (artist, album, from string) {
	if fileURL == "" {
		return "", "", ""
	}
	if lp, ok := s.play.snapshot(); ok && lp.boxURL == fileURL && (lp.artist != "" || lp.album != "") {
		return lp.artist, lp.album, "the last play"
	}
	if s.recent != nil {
		ring := s.recent.All() // oldest first
		for i := len(ring) - 1; i >= 0; i-- {
			e := ring[i]
			if e.Source != "upnp" || e.CardURL != fileURL || (e.Artist == "" && e.Album == "") {
				continue
			}
			return cleanTrackText(e.Artist), cleanTrackText(e.Album), "recently played"
		}
	}
	if s.presets != nil {
		for _, p := range s.presets.All() {
			if p.StreamURL != fileURL || !presetKeepsTrackLabel(p) || (p.Artist == "" && p.Album == "") {
				continue
			}
			return cleanTrackText(p.Artist), cleanTrackText(p.Album), "a key"
		}
	}
	return "", "", ""
}

// presetKeepsTrackLabel reports whether p is of a kind that can hold a single
// track's artist and album: the radio-shaped preset a library track is saved
// as. A Spotify, folder or native-service key has none.
func presetKeepsTrackLabel(p presets.Preset) bool {
	switch strings.ToLower(strings.TrimSpace(p.Type)) {
	case "spotify", "queue", presets.TypeNative:
		return false
	}
	return true
}

// boundPresetTrackLabel bounds the artist and album a preset arrives with the
// way a play's are bounded (cleanTrackText), and drops them from a type that
// has none, so no client can grow the preset store through them.
func boundPresetTrackLabel(p *presets.Preset) {
	if !presetKeepsTrackLabel(*p) {
		p.Artist, p.Album = "", ""
		return
	}
	p.Artist, p.Album = cleanTrackText(p.Artist), cleanTrackText(p.Album)
}

// settlePresetTrackLabel is the per-slot save's rule for artist and album:
// bounded, and when the save names neither, filled from what this agent knows
// of the file. That covers every client that does not send them: the hold on
// a key that saves what plays, an older desktop app, and a rename or move
// written by an app that read the key without the fields and would otherwise
// strip them. It returns where a filled label came from, "" when it filled
// none.
func (s *Server) settlePresetTrackLabel(p *presets.Preset) string {
	boundPresetTrackLabel(p)
	if !presetKeepsTrackLabel(*p) || p.Artist != "" || p.Album != "" || !isPlainHTTPURL(p.StreamURL) {
		return ""
	}
	artist, album, from := s.knownTrackLabel(p.StreamURL)
	p.Artist, p.Album = artist, album
	return from
}

// keyTrackLabel is the artist and album a library key is played under: the
// ones stored on it, else what this agent knows of the same file (a key saved
// before they were kept). recovered names where the latter came from and is
// "" for a stored label and when there is none.
func (s *Server) keyTrackLabel(p presets.Preset) (artist, album, recovered string) {
	if artist, album = cleanTrackText(p.Artist), cleanTrackText(p.Album); artist != "" || album != "" {
		return artist, album, ""
	}
	return s.knownTrackLabel(p.StreamURL)
}

// storeKeyTrackLabel writes a label recovered at a recall onto the key it was
// recovered for, once: the next press reads it from the key, also after the
// track has left Recently played. Like the learned length
// (learnSingleTrackLength) this is one store write per key, not one per press;
// a key that changed meanwhile or already has a label is left alone.
func (s *Server) storeKeyTrackLabel(slot int, fileURL, artist, album, from string) {
	if s.presets == nil || (artist == "" && album == "") {
		return
	}
	cur, ok := s.presets.Get(slot)
	if !ok || cur.StreamURL != fileURL || !presetKeepsTrackLabel(cur) || cur.Artist != "" || cur.Album != "" {
		return
	}
	cur.Artist, cur.Album = artist, album
	if err := s.presets.SetSlot(cur); err != nil {
		// The label still serves this play; only the key stays without it.
		s.logger.Warn("library key: could not store the recovered artist and album on the key",
			"slot", slot, "err", err)
		return
	}
	s.logger.Info("library key: stored the artist and album recovered for a key saved without them",
		"slot", slot, "from", from)
}

// setLastPlayFile records a lone library file as the play on record, with the
// artist and album its start named, and returns the new recall generation.
func (s *Server) setLastPlayFile(tr singleTrack) uint64 {
	return s.recordLastPlay(&lastPlayInfo{boxURL: tr.boxURL, title: tr.title, art: tr.art, mime: tr.mime,
		artist: tr.artist, album: tr.album})
}

// lastPlayLoneFile returns the labelled library file on the play record when
// no queue runs. It stands in for currentLoneFile where no end watch follows
// the file: after an agent restart the record comes back from NAND, and the
// power-on resume pushes it again without a new play. Only a record that
// carries an artist or an album qualifies, which only a directly played file
// ever does, so a station or a folder track never shows up here. A client
// still prints it only while the speaker's own location is this file.
func (s *Server) lastPlayLoneFile() (singleTrack, bool) {
	lp, ok := s.play.snapshot()
	if !ok || lp.fromBox || (lp.artist == "" && lp.album == "") {
		return singleTrack{}, false
	}
	if lp.mime == "" || !isPlainHTTPURL(lp.boxURL) || isOwnProxyURL(lp.boxURL) {
		return singleTrack{}, false
	}
	if s.queue != nil && s.queue.isActive() {
		return singleTrack{}, false
	}
	return singleTrack{boxURL: lp.boxURL, title: lp.title, art: lp.art, mime: lp.mime,
		artist: lp.artist, album: lp.album}, true
}
