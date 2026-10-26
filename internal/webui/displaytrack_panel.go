package webui

// The speaker display during playback, on a speaker whose panel STR can draw
// on itself (internal/oled, track.go).
//
// Two independent settings decide what STR draws there while one of its own
// sources plays (radio, Spotify through STR, the network library):
//
//	track on,  clock off  station on top, artist and title below
//	track on,  clock on   the same, with the time at the right of the top line
//	track off, clock on   station on top, the time large below
//	track off, clock off  STR leaves the panel to the speaker
//
// The clock setting reaches beyond STR's own sources: on the speaker's
// Bluetooth and AUX inputs, where the firmware shows only a connection
// picture, STR draws the time large with the input's name on top instead
// (oled.ShowSourceClock). With the clock off those inputs keep the
// firmware's picture, as before.
//
// The old way of showing the track, re-sending the stream with the new title
// as metadata, makes the speaker re-buffer: about a second of silence at every
// song change. Where the panel is STR's to draw on, the setting takes this
// path instead and the audio stream is never touched. Everywhere else the
// re-buffer path stays exactly as it was, because it is the only channel those
// displays render, and the clock is not offered.
//
// What is on the panel is kept here as one model (panelModel), fed by every
// source that knows the current song: the radio stream's ICY titles, the
// Spotify engine's track changes and the library queue. Each change redraws
// from the model and the settings, so switching a setting or a source shows
// the right screen at once instead of at the next song.
//
// The panel is let go when the stream it belongs to is: the radio proxy no
// longer carrying the stream (HandleStreamEnded), standby or a source change
// away from STR (the agent's gabbo hooks), and both settings switched off.

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/oled"
)

// trackPanelSink is the panel side of the setting.
type trackPanelSink interface {
	Supported() bool
	Show(oled.TrackText)
	ShowSourceClock(source string, t oled.TrackText)
	ClearSourceClock(source, reason string)
	Clear(reason string)
}

type oledTrackPanel struct{}

func (oledTrackPanel) Supported() bool       { return oled.TrackSupported() }
func (oledTrackPanel) Show(t oled.TrackText) { oled.ShowTrack(t) }
func (oledTrackPanel) Clear(reason string)   { oled.ClearTrack(reason) }
func (oledTrackPanel) ShowSourceClock(source string, t oled.TrackText) {
	oled.ShowSourceClock(source, t)
}
func (oledTrackPanel) ClearSourceClock(source, reason string) {
	oled.ClearSourceClock(source, reason)
}

func (s *Server) panelSink() trackPanelSink {
	if s.trackPanel != nil {
		return s.trackPanel
	}
	return oledTrackPanel{}
}

// trackOnPanel reports whether the track setting draws on the panel here
// (no re-buffering) rather than re-sending the stream.
func (s *Server) trackOnPanel() bool { return s.panelSink().Supported() }

// Kinds of playback the panel model knows.
const (
	panelRadio   = "radio"   // a stream with ICY titles, or one STR knows only by name
	panelSpotify = "spotify" // the Spotify engine's stream
	panelLibrary = "library" // a file from a network library
)

// panelModel is what plays, as far as the panel is concerned. Guarded by
// Server.panelMu.
type panelModel struct {
	kind string
	// station is the name on the top line: the Spotify preset or "Spotify",
	// the library folder. Empty for radio means the name STR started the
	// stream with (lastPlay).
	station string
	// artist and title of the current song; raw is the whole ICY StreamTitle
	// for radio, the fallback when the mode's part is missing.
	artist, title, raw string
	// ended: the radio stream this belonged to is gone. The panel stays off
	// until a new title or a new stream.
	ended bool
}

// panelState is the panel bookkeeping; embedded in Server.
type panelState struct {
	panelMu sync.Mutex
	panel   panelModel
	// spotifyNow is the Spotify engine's current song (track, artist), kept
	// even while something else plays.
	spotifyNow [2]string
	// boxSource is the source the speaker last switched to (the agent's
	// gabbo hook, PanelSourceChanged); empty until the first switch.
	boxSource string

	hour12Mu    sync.Mutex
	hour12      bool
	hour12Known bool
	hour12Tried time.Time
	hour12Read  func() (hour12, ok bool) // test seam; nil reads the speaker
}

// spotifyPanelName is the top line for Spotify started from the Spotify app,
// where STR knows no preset name.
const spotifyPanelName = "Spotify"

// panelNewStream is setLastPlay's: a different stream started. Its name is
// all STR knows until the first song arrives.
func (s *Server) panelNewStream(boxURL, title string) {
	m := panelModel{kind: panelRadio}
	if strings.Contains(boxURL, "/spotify/") {
		m = panelModel{kind: panelSpotify, station: title}
	}
	s.panelMu.Lock()
	s.panel = m
	s.panelMu.Unlock()
	s.refreshTrackPanel()
}

// panelRadioTitle is a new ICY StreamTitle. A title arriving while Spotify or
// the library plays is a late one from the stream before and is dropped.
func (s *Server) panelRadioTitle(raw string) {
	s.panelMu.Lock()
	if s.panel.kind == panelSpotify || s.panel.kind == panelLibrary {
		s.panelMu.Unlock()
		return
	}
	artist, title := splitStreamTitle(raw)
	s.panel.kind = panelRadio
	s.panel.artist, s.panel.title, s.panel.raw = artist, title, strings.TrimSpace(raw)
	s.panel.ended = false
	s.panelMu.Unlock()
	s.refreshTrackPanel()
}

// PanelSpotifyTrack is the Spotify engine's "the song changed". It is kept
// even while something else plays, so Spotify started from the app later
// shows its song at once.
func (s *Server) PanelSpotifyTrack(track, artist string) {
	s.panelMu.Lock()
	s.spotifyNow = [2]string{track, artist}
	isSpotify := s.panel.kind == panelSpotify
	if isSpotify {
		s.panel.artist, s.panel.title, s.panel.raw = artist, track, joinArtistTitle(artist, track)
	}
	s.panelMu.Unlock()
	if isSpotify {
		s.refreshTrackPanel()
	}
}

// PanelSpotifyActive is "the speaker was just pointed at the Spotify stream"
// from the Spotify app: the panel shows the engine's current song.
func (s *Server) PanelSpotifyActive() {
	s.panelMu.Lock()
	track, artist := s.spotifyNow[0], s.spotifyNow[1]
	s.panel = panelModel{kind: panelSpotify, station: spotifyPanelName, artist: artist, title: track, raw: joinArtistTitle(artist, track)}
	s.panelMu.Unlock()
	s.refreshTrackPanel()
}

// panelLibraryTrack is a library file that started: folder is the queue's
// folder name ("" for a single file), the song by artist and title.
func (s *Server) panelLibraryTrack(folder, artist, title string) {
	s.panelMu.Lock()
	s.panel = panelModel{kind: panelLibrary, station: folder, artist: artist, title: title, raw: joinArtistTitle(artist, title)}
	s.panelMu.Unlock()
	s.refreshTrackPanel()
}

func joinArtistTitle(artist, title string) string {
	if artist == "" {
		return title
	}
	if title == "" {
		return artist
	}
	return artist + " - " + title
}

// panelText is what the panel shows for the model and the two settings;
// false when STR leaves the panel to the speaker.
func (s *Server) panelText() (oled.TrackText, bool) {
	trackOn, clockOn := s.displayTrackEnabled(), s.displayClockEnabled()
	if !trackOn && !clockOn {
		return oled.TrackText{}, false
	}
	s.panelMu.Lock()
	m := s.panel
	s.panelMu.Unlock()
	if m.ended || m.kind == "" {
		return oled.TrackText{}, false
	}
	if m.kind == panelRadio {
		if lp := s.play.current(); lp != nil {
			m.station = lp.title
		}
	}
	t := oled.TrackText{Station: m.station}
	if trackOn {
		switch s.displayTrackMode() {
		case "title":
			t.Title = m.title
		case "artist":
			t.Title = m.artist
		default:
			t.Artist, t.Title = m.artist, m.title
		}
		if t.Title == "" {
			t.Artist, t.Title = "", m.raw
		}
		// Nothing known about the song (a station without titles, the first
		// seconds of a stream): its name is the screen.
		if t.Title == "" {
			t.Station, t.Title = "", m.station
		}
	}
	if clockOn {
		t.Clock, t.Hour12 = true, s.speakerHour12()
	}
	if t.Station == "" && t.Artist == "" && t.Title == "" && !t.Clock {
		return oled.TrackText{}, false
	}
	return t, true
}

// refreshTrackPanel draws the model with the current settings, or lets the
// panel go when there is nothing to draw. A no-op on a speaker without a
// supported panel.
func (s *Server) refreshTrackPanel() {
	p := s.panelSink()
	if !p.Supported() {
		return
	}
	if src, label, ok := s.panelInput(); ok {
		// one of the speaker's own inputs: the clock or the firmware's picture
		if s.displayClockEnabled() {
			p.ShowSourceClock(src, oled.TrackText{Station: label, Clock: true, Hour12: s.speakerHour12()})
			return
		}
		// the clock was switched off here: end the input clock, but only that.
		// src was read a moment ago; when the speaker has left the input since,
		// the screen now up belongs to the new source and stays.
		p.ClearSourceClock(src, "nothing to show")
		return
	}
	if t, ok := s.panelText(); ok {
		p.Show(t)
		return
	}
	p.Clear("nothing to show")
}

// panelInput reports whether the speaker is on one of its own inputs where
// the clock may stand in for the firmware's picture (oled.SourceClockLabel),
// with the firmware's source name and the label shown on top.
func (s *Server) panelInput() (src, label string, ok bool) {
	s.panelMu.Lock()
	src = s.boxSource
	s.panelMu.Unlock()
	label, ok = oled.SourceClockLabel(src)
	return src, label, ok
}

// PanelSourceChanged is for the agent's gabbo hook: the speaker switched to
// src. Switching to Bluetooth or AUX puts the clock up there when the setting
// asks for it; leaving them is the hook's own business (the panel side drops
// the input clock on any switch, and a return to STR's source redraws via
// RefreshTrackPanel). Does not block: it runs on the gabbo read loop, and
// the redraw may ask the speaker for its time format.
func (s *Server) PanelSourceChanged(src string) {
	s.panelMu.Lock()
	s.boxSource = src
	s.panelMu.Unlock()
	// With the clock off there is nothing to do: the panel side has already
	// let go on the switch, and the firmware's picture stays as before.
	if _, ok := oled.SourceClockLabel(src); ok && s.trackOnPanel() && s.displayClockEnabled() {
		go s.refreshTrackPanel()
	}
}

// RefreshTrackPanel is for the agent's gabbo hook: the speaker is back on one
// of STR's sources, so the screen for what plays comes back without waiting
// for the next song.
func (s *Server) RefreshTrackPanel() { s.refreshTrackPanel() }

// showTrackOnPanelNow redraws right after a display setting changed. It does
// not ask the speaker whether it plays: BoseApp is the part that stops
// answering for minutes at a time (measured on a Portable, 2026-10-13
// 18:26, 160 s), and a setting switched on in such a window then stayed
// invisible until the next song. The panel side already keeps off a speaker
// in standby or on another source (oled.SetTrackSource), and the model is
// empty until something of STR's played.
func (s *Server) showTrackOnPanelNow() {
	s.refreshTrackPanel()
}

// applyPanelSetting shows the change of a display setting at once: the new
// screen while the speaker plays, or the panel handed back when both are off.
// Async, so the POST returns before any box I/O.
func (s *Server) applyPanelSetting() {
	if !s.trackOnPanel() {
		return
	}
	if !s.displayTrackEnabled() && !s.displayClockEnabled() {
		go s.clearTrackPanel("setting off")
		return
	}
	go s.showTrackOnPanelNow()
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
// has ended" (no successor within its grace): whatever plays next is not that
// radio track any more. Spotify and the library do not run through the proxy,
// so a radio stream ending behind them leaves their screen alone.
func (s *Server) HandleStreamEnded() {
	s.panelMu.Lock()
	radio := s.panel.kind == panelRadio
	if radio {
		s.panel.ended = true
	}
	s.panelMu.Unlock()
	if _, _, onInput := s.panelInput(); onInput {
		// the speaker left the stream for Bluetooth or AUX: whatever the
		// panel shows there is not the radio's to end
		return
	}
	if radio {
		s.clearTrackPanel("stream ended")
	}
}

// ClearTrackPanel is for the agent's gabbo hooks: a stop, standby or a source
// change away from STR takes the track off the panel.
func (s *Server) ClearTrackPanel(reason string) { s.clearTrackPanel(reason) }

// ---- the clock setting ----

// defaultDisplayClockPath is the NAND flag file for "show the time on the
// speaker display during playback" (default off); "1" means on.
const defaultDisplayClockPath = "/mnt/nv/streborn/display-clock-on-playback"

// clockPathFor keeps a test override of displayTrackPath and the clock flag
// together, like modePathFor.
func clockPathFor(enabledPath string) string {
	if enabledPath == "" || enabledPath == defaultDisplayTrackPath {
		return defaultDisplayClockPath
	}
	return enabledPath + ".clock"
}

// displayClockEnabled reports whether the time is shown during playback. Only
// meaningful where STR draws on the panel.
func (s *Server) displayClockEnabled() bool {
	return readBoolFlag(clockPathFor(s.displayTrackPath), false)
}

// hour12Retry spaces out reads of the speaker's time format after one failed.
const hour12Retry = 5 * time.Minute

// speakerHour12 reports whether the speaker's own clock is set to 12 hours,
// so STR's time reads the same as the speaker's standby clock. Read from the
// firmware once and kept; a failed read counts as 24 hours and is tried again
// at most every few minutes, never in a loop.
func (s *Server) speakerHour12() bool {
	s.hour12Mu.Lock()
	defer s.hour12Mu.Unlock()
	if s.hour12Known || (!s.hour12Tried.IsZero() && time.Since(s.hour12Tried) < hour12Retry) {
		return s.hour12
	}
	s.hour12Tried = time.Now()
	read := s.hour12Read
	if read == nil {
		read = s.readSpeakerHour12
	}
	if h12, ok := read(); ok {
		s.hour12, s.hour12Known = h12, true
	}
	return s.hour12
}

// forgetSpeakerHour12 makes the next clock draw read the format again: the
// clock setting was just switched, a cheap moment to pick up a change.
func (s *Server) forgetSpeakerHour12() {
	s.hour12Mu.Lock()
	s.hour12Known, s.hour12Tried = false, time.Time{}
	s.hour12Mu.Unlock()
}

// readSpeakerHour12 asks the firmware's /clockDisplay for its time format.
func (s *Server) readSpeakerHour12() (hour12, ok bool) {
	if s.boxHost == "" {
		return false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+s.boxHost+":8090/clockDisplay", nil)
	if err != nil {
		return false, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil || resp.StatusCode != http.StatusOK {
		return false, false
	}
	return parseClockHour12(body)
}

// parseClockHour12 reads timeFormat from a /clockDisplay answer:
// TIME_FORMAT_12HOUR_ID or TIME_FORMAT_24HOUR_ID.
func parseClockHour12(body []byte) (hour12, ok bool) {
	var cd struct {
		Config struct {
			TimeFormat string `xml:"timeFormat,attr"`
		} `xml:"clockConfig"`
	}
	if xml.Unmarshal(body, &cd) != nil || cd.Config.TimeFormat == "" {
		return false, false
	}
	return strings.Contains(cd.Config.TimeFormat, "12"), true
}

// handleDisplayClock reads or sets "show the time on the speaker display
// during playback" (default off). GET returns {supported, enabled}, where
// supported says STR draws on this speaker's panel (the setting does nothing
// elsewhere); POST {enabled} persists it and redraws at once.
func (s *Server) handleDisplayClock(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"supported": s.trackOnPanel(), "enabled": s.displayClockEnabled()})
	case http.MethodPost:
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if !decodeJSONRequest(w, r, 4096, &body) {
			return
		}
		val := "0"
		if body.Enabled {
			val = "1"
		}
		if err := persistFlagFile(clockPathFor(s.displayTrackPath), val); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = exec.Command("sync").Run()
		s.logger.Info("display-clock set", "enabled", body.Enabled)
		s.forgetSpeakerHour12()
		s.applyPanelSetting()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": body.Enabled})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
