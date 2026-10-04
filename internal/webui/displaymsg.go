package webui

// Short messages on the speaker display when a key cannot play.
//
// A hardware key that cannot play used to end in silence: the speaker showed
// its own "no source" screen or nothing at all, and the only place the reason
// was written down was the agent log. On speakers with a screen STR now puts a
// short, plain sentence there instead ("No internet connection").
//
// The firmware offers no way to write text to the display. A spike on FW
// 27.0.6 (docs/FIRMWARE-NOTES.md, "Writing text to the display") checked the
// :17000 service console and every :8090 endpoint: the console's `display`
// group takes no option that shows text, `oled` only copies canned test
// bitmaps, and /speaker is a gated audio path. So the message travels the one
// channel the display does render, the now-playing title: STR points the
// speaker at a few seconds of digital silence served by the agent itself,
// titled with the message, and stops it again after displayMsgHold. Nothing
// audible plays and the volume is never touched.
//
// The guard rails, each for a reason:
//   - only on a speaker with a display (its own /capabilities clockDisplay
//     flag; an ST10 says false),
//   - never in standby, never while anything plays, never in a group,
//   - once per message per displayMsgRepeat, so a user pressing a dead key
//     five times sees it once rather than five silent-track interruptions,
//   - no timer of its own: everything runs on the key press that failed, and
//     the only scheduled work is the one-shot stop.
//   - the setting is per speaker and on by default.

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxurl"
)

// DisplayMsgKind names one message the speaker display can show.
type DisplayMsgKind string

const (
	// DisplayMsgSpotifyLogin: a Spotify key was pressed but the speaker holds
	// no Spotify login it can use.
	DisplayMsgSpotifyLogin DisplayMsgKind = "spotify-login"
	// DisplayMsgStationDown: a radio key's station did not answer. Upgraded to
	// DisplayMsgNoInternet when the speaker cannot reach the internet at all.
	DisplayMsgStationDown DisplayMsgKind = "station-unreachable"
	// DisplayMsgNoInternet: the speaker has no internet connection.
	DisplayMsgNoInternet DisplayMsgKind = "no-internet"
	// DisplayMsgIfOffline is a request, not a message: show
	// DisplayMsgNoInternet if the speaker is offline, otherwise nothing. Used
	// where a key failed for a reason that is only worth telling the user when
	// it is the connection (a Spotify play that never started).
	DisplayMsgIfOffline DisplayMsgKind = "if-offline"
)

const (
	// defaultDisplayMsgPath is the NAND flag file for the per-speaker switch.
	// Absent means on (the default); "0" turns it off.
	defaultDisplayMsgPath = "/mnt/nv/streborn/display-messages"
	// displayMsgAudioPath is where the box fetches the silent message track.
	displayMsgAudioPath = "/display-message.mp3"
	// displayMsgHold is how long the message stays on screen before STR stops
	// the silent track again.
	displayMsgHold = 10 * time.Second
	// displayMsgRepeat is the minimum gap between two showings of the same
	// message.
	displayMsgRepeat = 5 * time.Minute
	// displayMsgSettle is the pause between the failure and the push. The
	// firmware is usually still tearing down its own failed attempt at that
	// moment, and a push into the teardown is overwritten by it.
	displayMsgSettle = 2 * time.Second
	// displayMsgSilence is how much silence the track holds. Longer than the
	// hold, so the track never ends by itself before STR stops it.
	displayMsgSilence = 15 * time.Second
)

// displayMsgRecord is one showing (or one decision not to show), kept for the
// diagnostic section.
type displayMsgRecord struct {
	Kind    DisplayMsgKind `json:"kind"`
	Text    string         `json:"text,omitempty"`
	Lang    int            `json:"sysLanguage,omitempty"`
	At      time.Time      `json:"at"`
	Outcome string         `json:"outcome"`
}

// displayMsgState is the Server's display-message bookkeeping.
type displayMsgState struct {
	mu        sync.Mutex
	lastShown map[DisplayMsgKind]time.Time
	busy      bool   // a message is on screen (or about to be)
	token     uint64 // identifies the showing the pending stop belongs to
	shown     displayMsgRecord
	attempt   displayMsgRecord

	// Test seams. Zero values mean the real behaviour.
	path         string
	settle, hold time.Duration
	nowPlayingFn func() (boxNowPlaying, bool)
	langFn       func() int
	hasDisplayFn func() (has, ok bool)
	after        func(time.Duration, func())
}

// boxNowPlaying is the slice of /now_playing the gate needs.
type boxNowPlaying struct {
	Source     string `xml:"source,attr"`
	PlayStatus string `xml:"playStatus"`
	Item       struct {
		Location string `xml:"location,attr"`
	} `xml:"ContentItem"`
}

// displayMsgTexts holds the messages per Bose sysLanguage (2 German, 3
// English, 4 Spanish, 5 French, 7 Dutch). Anything else gets English. Kept
// short: the smaller screens scroll anything longer than about 25 characters.
var displayMsgTexts = map[int]map[DisplayMsgKind]string{
	3: {
		DisplayMsgSpotifyLogin: "Spotify: tap this speaker once in the Spotify app",
		DisplayMsgStationDown:  "Station not responding, try again later",
		DisplayMsgNoInternet:   "No internet connection",
	},
	2: {
		DisplayMsgSpotifyLogin: "Spotify: Lautsprecher einmal in der Spotify-App antippen",
		DisplayMsgStationDown:  "Sender antwortet nicht, später nochmal versuchen",
		DisplayMsgNoInternet:   "Keine Internetverbindung",
	},
	4: {
		DisplayMsgSpotifyLogin: "Spotify: toca este altavoz una vez en la app de Spotify",
		DisplayMsgStationDown:  "La emisora no responde, inténtalo más tarde",
		DisplayMsgNoInternet:   "Sin conexión a Internet",
	},
	5: {
		DisplayMsgSpotifyLogin: "Spotify : touchez cette enceinte une fois dans l'app Spotify",
		DisplayMsgStationDown:  "La station ne répond pas, réessayez plus tard",
		DisplayMsgNoInternet:   "Pas de connexion Internet",
	},
	7: {
		DisplayMsgSpotifyLogin: "Spotify: tik deze speaker één keer aan in de Spotify-app",
		DisplayMsgStationDown:  "Zender reageert niet, probeer het later opnieuw",
		DisplayMsgNoInternet:   "Geen internetverbinding",
	},
}

// displayMsgText returns the message for kind in the speaker's language,
// falling back to English.
func displayMsgText(kind DisplayMsgKind, sysLanguage int) string {
	if t, ok := displayMsgTexts[sysLanguage][kind]; ok {
		return t
	}
	return displayMsgTexts[3][kind]
}

// modelHasDisplay is the fallback when the box did not answer /capabilities:
// the model name the agent already knows. known is false for a name not in
// the table, which the caller treats as "no display" (silence beats a guess).
func modelHasDisplay(model string) (has, known bool) {
	m := strings.ToLower(model)
	switch {
	case m == "":
		return false, false
	case strings.Contains(m, "soundtouch 10"), strings.Contains(m, "soundtouch 300"),
		strings.Contains(m, "sa-4"), strings.Contains(m, "sa-5"),
		strings.Contains(m, "cinemate"), strings.Contains(m, "wireless link"):
		return false, true
	case strings.Contains(m, "soundtouch 20"), strings.Contains(m, "soundtouch 30"),
		strings.Contains(m, "portable"), strings.Contains(m, "wave"):
		return true, true
	}
	return false, false
}

// displayMsgBlockedBy says why the speaker's current state rules a message
// out, or "" when it is free. Fails closed on an unreadable state.
func displayMsgBlockedBy(np boxNowPlaying, ok bool) string {
	switch {
	case !ok:
		return "speaker state unknown"
	case strings.EqualFold(np.Source, "STANDBY"):
		return "standby"
	case np.PlayStatus == "PLAY_STATE":
		return "something is playing"
	}
	return ""
}

// displayMessagesEnabled reads the per-speaker switch. On unless the flag
// file says otherwise.
func (s *Server) displayMessagesEnabled() bool {
	b, err := os.ReadFile(s.displayMsgFlagPath())
	if err != nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(string(b))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

func (s *Server) displayMsgFlagPath() string {
	if s.displayMsg.path != "" {
		return s.displayMsg.path
	}
	return defaultDisplayMsgPath
}

// speakerHasDisplay asks the firmware first and the model name second.
func (s *Server) speakerHasDisplay() bool {
	if s.displayMsg.hasDisplayFn != nil {
		has, _ := s.displayMsg.hasDisplayFn()
		return has
	}
	if s.boxHost != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		has, ok := boxapi.New(s.boxHost).HasDisplay(ctx)
		cancel()
		if ok {
			return has
		}
	}
	if s.boxNameFn != nil {
		_, model := s.boxNameFn()
		has, _ := modelHasDisplay(model)
		return has
	}
	return false
}

func (s *Server) displayMsgNowPlaying() (boxNowPlaying, bool) {
	if s.displayMsg.nowPlayingFn != nil {
		return s.displayMsg.nowPlayingFn()
	}
	var np boxNowPlaying
	if s.boxHost == "" {
		return np, false
	}
	cl := &http.Client{Timeout: 4 * time.Second}
	resp, err := cl.Get("http://" + s.boxHost + ":8090/now_playing")
	if err != nil {
		return np, false
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil || resp.StatusCode != http.StatusOK || xml.Unmarshal(b, &np) != nil {
		return np, false
	}
	return np, true
}

func (s *Server) displayMsgLanguage() int {
	if s.displayMsg.langFn != nil {
		return s.displayMsg.langFn()
	}
	if s.boxHost == "" {
		return 3
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	n, err := boxapi.New(s.boxHost).GetSysLanguage(ctx)
	if err != nil {
		return 3
	}
	return n
}

func (s *Server) displayMsgDur(override, def time.Duration) time.Duration {
	if override != 0 {
		return override
	}
	return def
}

// ShowKeyMessage puts the message for kind on the speaker display if every
// guard allows it. It returns at once; the work runs beside the caller, which
// is a key press handler that must not wait on the speaker.
func (s *Server) ShowKeyMessage(kind DisplayMsgKind) {
	go s.showKeyMessage(kind, false)
}

// showKeyMessage is ShowKeyMessage's body. preview skips the repeat limit
// (the app's "show a test message"), never the other guards. It returns the
// outcome, "shown" or the reason it was not.
func (s *Server) showKeyMessage(kind DisplayMsgKind, preview bool) string {
	if !s.displayMessagesEnabled() {
		return s.noteDisplayMsgAttempt(kind, "switched off for this speaker")
	}
	if kind == DisplayMsgStationDown || kind == DisplayMsgIfOffline {
		// One on-demand reachability check, on the failure itself (no timer).
		offline := s.onlineFn != nil && !s.onlineFn()
		switch {
		case offline:
			kind = DisplayMsgNoInternet
		case kind == DisplayMsgIfOffline:
			return s.noteDisplayMsgAttempt(kind, "speaker is online, nothing to say")
		}
	}

	d := &s.displayMsg
	d.mu.Lock()
	if d.busy {
		d.mu.Unlock()
		return s.noteDisplayMsgAttempt(kind, "another message is on screen")
	}
	if last, ok := d.lastShown[kind]; ok && !preview && time.Since(last) < displayMsgRepeat {
		d.mu.Unlock()
		return s.noteDisplayMsgAttempt(kind, "shown recently")
	}
	d.busy = true
	d.mu.Unlock()
	shown := false
	defer func() {
		if !shown {
			d.mu.Lock()
			d.busy = false
			d.mu.Unlock()
		}
	}()

	if !s.speakerHasDisplay() {
		return s.noteDisplayMsgAttempt(kind, "speaker has no display")
	}
	if s.renderer == nil {
		return s.noteDisplayMsgAttempt(kind, "no renderer")
	}
	time.Sleep(s.displayMsgDur(d.settle, displayMsgSettle))
	if s.boxInZone() {
		return s.noteDisplayMsgAttempt(kind, "speaker is in a group")
	}
	np, ok := s.displayMsgNowPlaying()
	if why := displayMsgBlockedBy(np, ok); why != "" {
		return s.noteDisplayMsgAttempt(kind, why)
	}
	lang := s.displayMsgLanguage()
	text := displayMsgText(kind, lang)

	d.mu.Lock()
	d.token++
	token := d.token
	d.mu.Unlock()
	url := fmt.Sprintf("http://%s%s?m=%s&n=%d", boxurl.Authority, displayMsgAudioPath, kind, token)

	s.boxCmdMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	err := s.renderer.PlayURLMime(ctx, url, text, "", "audio/mpeg")
	cancel()
	s.boxCmdMu.Unlock()
	if err != nil {
		s.logger.Warn("display message: the speaker refused the message track", "kind", kind, "err", err)
		return s.noteDisplayMsgAttempt(kind, "push failed")
	}
	gen := s.RecallGeneration()
	now := time.Now()
	d.mu.Lock()
	if d.lastShown == nil {
		d.lastShown = map[DisplayMsgKind]time.Time{}
	}
	d.lastShown[kind] = now
	d.shown = displayMsgRecord{Kind: kind, Text: text, Lang: lang, At: now, Outcome: "shown"}
	d.attempt = d.shown
	d.mu.Unlock()
	shown = true
	s.logger.Info("display message shown", "kind", kind, "text", text, "sysLanguage", lang)

	after := d.after
	if after == nil {
		after = func(dur time.Duration, f func()) { time.AfterFunc(dur, f) }
	}
	after(s.displayMsgDur(d.hold, displayMsgHold), func() { s.endDisplayMessage(token, gen) })
	return "shown"
}

// endDisplayMessage stops the message track, unless the user started
// something else in the meantime: a newer recall, or a different item on the
// speaker. Either way the slot is released for the next message.
func (s *Server) endDisplayMessage(token, gen uint64) {
	d := &s.displayMsg
	defer func() {
		d.mu.Lock()
		if d.token == token {
			d.busy = false
		}
		d.mu.Unlock()
	}()
	if s.RecallGeneration() != gen {
		s.logger.Info("display message: a new play took over, leaving it alone")
		return
	}
	np, ok := s.displayMsgNowPlaying()
	if !ok || !strings.Contains(np.Item.Location, displayMsgAudioPath) {
		s.logger.Info("display message: the speaker moved on by itself, leaving it alone", "source", np.Source)
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := s.renderer.Stop(ctx); err != nil {
		s.logger.Warn("display message: stopping the message track failed", "err", err)
		return
	}
	// Empty the transport too, so a chassis that re-selects the last UPnP item
	// when it leaves standby (#197) has no message to bring back.
	if err := s.renderer.ClearURI(ctx); err != nil {
		s.logger.Debug("display message: clearing the transport failed", "err", err)
	}
	s.logger.Info("display message ended")
}

// noteDisplayMsgAttempt records a decision not to show and returns its reason.
func (s *Server) noteDisplayMsgAttempt(kind DisplayMsgKind, why string) string {
	s.displayMsg.mu.Lock()
	s.displayMsg.attempt = displayMsgRecord{Kind: kind, At: time.Now(), Outcome: why}
	s.displayMsg.mu.Unlock()
	s.logger.Info("display message not shown", "kind", kind, "reason", why)
	return why
}

// DisplayMessageSnapshot is the diagnostic section: the setting, the last
// message shown and the last decision taken.
func (s *Server) DisplayMessageSnapshot() any {
	s.displayMsg.mu.Lock()
	defer s.displayMsg.mu.Unlock()
	out := map[string]any{"enabled": s.displayMessagesEnabled()}
	if !s.displayMsg.shown.At.IsZero() {
		out["last_shown"] = s.displayMsg.shown
	}
	if !s.displayMsg.attempt.At.IsZero() {
		out["last_decision"] = s.displayMsg.attempt
	}
	return out
}

// handleDisplayMessageAudio serves the message track: displayMsgSilence of
// MPEG-1 Layer III frames that decode to digital silence.
func (s *Server) handleDisplayMessageAudio(w http.ResponseWriter, r *http.Request) {
	body := silentMP3(displayMsgSilence)
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// silentMP3 builds d of silence as MPEG-1 Layer III, 32 kbit/s, 48 kHz, mono.
// At that rate a frame is exactly 96 bytes with no padding, and a frame whose
// side information is all zero carries no audio data at all: every decoder
// outputs zeros for it. 1152 samples per frame.
func silentMP3(d time.Duration) []byte {
	const frameLen = 96
	frames := int(d.Seconds()*48000/1152) + 1
	out := make([]byte, frames*frameLen)
	for i := 0; i < frames; i++ {
		f := out[i*frameLen:]
		// sync + MPEG-1 + Layer III + no CRC; 32 kbit/s, 48 kHz, no padding;
		// mono, no emphasis.
		f[0], f[1], f[2], f[3] = 0xFF, 0xFB, 0x14, 0xC0
	}
	return out
}

// handleDisplayMessages is the per-speaker setting.
//
//	GET  -> {"enabled":true,"hasDisplay":true,"lastShown":{...}}
//	POST {"enabled":false}            stores the switch
//	POST {"preview":"no-internet"}    shows that message now (all guards but
//	                                  the repeat limit apply)
func (s *Server) handleDisplayMessages(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		out := map[string]any{
			"enabled":    s.displayMessagesEnabled(),
			"hasDisplay": s.speakerHasDisplay(),
		}
		s.displayMsg.mu.Lock()
		if !s.displayMsg.shown.At.IsZero() {
			out["lastShown"] = s.displayMsg.shown
		}
		s.displayMsg.mu.Unlock()
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var body struct {
			Enabled *bool  `json:"enabled"`
			Preview string `json:"preview"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if body.Preview != "" {
			kind := DisplayMsgKind(body.Preview)
			if _, ok := displayMsgTexts[3][kind]; !ok {
				http.Error(w, "unknown message", http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"outcome": s.showKeyMessage(kind, true)})
			return
		}
		if body.Enabled == nil {
			http.Error(w, "enabled missing", http.StatusBadRequest)
			return
		}
		val := "0"
		if *body.Enabled {
			val = "1"
		}
		if err := persistFlagFile(s.displayMsgFlagPath(), val); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = exec.Command("sync").Run()
		s.logger.Info("display messages set", "enabled", *body.Enabled)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": *body.Enabled})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
