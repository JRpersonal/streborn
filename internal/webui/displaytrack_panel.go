package webui

// "Show the current track on the speaker display" on a speaker whose panel
// STR can draw on itself (internal/oled, track.go).
//
// The old way, re-sending the stream with the new title as metadata, makes
// the speaker re-buffer: about a second of silence at every song change. Where
// the panel is STR's to draw on, the setting takes this path instead and the
// audio stream is never touched: every ICY title change goes straight to the
// panel. Everywhere else the re-buffer path stays exactly as it was, because
// it is the only channel those displays render.
//
// The track leaves the panel when the stream it belongs to does: a new
// station (another box URL), the proxy no longer carrying the stream
// (HandleStreamEnded), a stop, standby or a source change away from STR
// (the agent's gabbo hooks), and the setting switched off.

import (
	"strings"

	"github.com/JRpersonal/streborn/internal/oled"
)

// trackPanelSink is the panel side of the setting.
type trackPanelSink interface {
	Supported() bool
	Show(oled.TrackText)
	Clear(reason string)
}

type oledTrackPanel struct{}

func (oledTrackPanel) Supported() bool       { return oled.TrackSupported() }
func (oledTrackPanel) Show(t oled.TrackText) { oled.ShowTrack(t) }
func (oledTrackPanel) Clear(reason string)   { oled.ClearTrack(reason) }

func (s *Server) panelSink() trackPanelSink {
	if s.trackPanel != nil {
		return s.trackPanel
	}
	return oledTrackPanel{}
}

// trackOnPanel reports whether the track setting draws on the panel here
// (no re-buffering) rather than re-sending the stream.
func (s *Server) trackOnPanel() bool { return s.panelSink().Supported() }

// panelTrackText is what the panel shows for a raw ICY StreamTitle, by the
// per-speaker mode: "both" splits it into artist and title (no separator:
// the whole string is the title), "title" and "artist" show that part alone
// and fall back to the whole string, so the panel is never blank. The station
// is the name STR started the stream with.
func (s *Server) panelTrackText(raw string) oled.TrackText {
	s.lastPlayMu.Lock()
	station := ""
	if s.lastPlay != nil {
		station = s.lastPlay.title
	}
	s.lastPlayMu.Unlock()
	t := oled.TrackText{Station: station}
	artist, title := splitStreamTitle(raw)
	switch s.displayTrackMode() {
	case "title":
		t.Title = title
	case "artist":
		t.Title = artist
	default:
		t.Artist, t.Title = artist, title
	}
	if t.Title == "" {
		t.Artist, t.Title = "", strings.TrimSpace(raw)
	}
	return t
}

// showTrackOnPanel puts raw on the panel. No rate limit: drawing costs no
// audio, and the panel only redraws when the text really changed.
func (s *Server) showTrackOnPanel(raw string) {
	s.panelSink().Show(s.panelTrackText(raw))
}

// showTrackOnPanelNow shows the current title right after the setting was
// switched on or its mode changed, when the speaker is playing.
func (s *Server) showTrackOnPanelNow() {
	if !s.displayTrackEnabled() {
		return
	}
	s.lastPlayMu.Lock()
	cur := s.lastICYTitle
	s.lastPlayMu.Unlock()
	if cur == "" {
		return
	}
	if standby, busy := s.boxPlayState(); standby || !busy {
		return
	}
	s.showTrackOnPanel(cur)
}

// clearTrackPanel takes the track off the panel. Cheap when nothing shows;
// a no-op on a speaker without a supported panel.
func (s *Server) clearTrackPanel(reason string) {
	p := s.panelSink()
	if !p.Supported() {
		return
	}
	p.Clear(reason)
}

// HandleStreamEnded is the stream proxy's "the stream the title belonged to
// has ended" (no successor within its grace): whatever plays next is not
// that radio track any more.
func (s *Server) HandleStreamEnded() {
	s.clearTrackPanel("stream ended")
}

// ClearTrackPanel is for the agent's gabbo hooks: a stop, standby or a source
// change away from STR takes the track off the panel.
func (s *Server) ClearTrackPanel(reason string) { s.clearTrackPanel(reason) }
