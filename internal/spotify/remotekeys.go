// remotekeys.go: what Back does to a Spotify playlist, on the speaker's own
// remote (#1678) and in the apps (#1797), and the shuffle and repeat switches
// a thumbs key can carry (#1679).
//
// Back. go-librespot's /player/prev follows Spotify's rule: past 3 seconds of
// the song it restarts the song, inside them it goes to the song before. Its
// position is the one the Spotify app shows, i.e. the audio the engine has
// produced, and STR keeps roughly ten seconds of that in flight to the
// speaker on purpose (leadCapSec, boxlag.go). So the engine is always more
// than 3 seconds into a song the room has only just started hearing, and Back
// could only ever restart: the user never got further back.
//
// HeardPrev decides on what the room hears instead: the engine position less
// the buffer lag of the box's current attachment (a usable boxlag reading,
// else delivered audio against the wall clock since the attachment's first
// page, else the lead cap; see heardLag). Inside the first heardPrevBackWindow of the
// song heard, Back goes to the song before; later it restarts the song. To go
// back regardless of the engine's own 3-second rule the engine is first put
// to the start of the song, then asked for prev: at position 0 its prev always
// means the song before. The /player/prev API has no allow_seeking switch
// that would say this directly.
//
// Every STR Back takes this path: the remote's key (webui HardwareSkip) and
// the Previous button of the desktop app and the phone page (webui
// transportSkip). The app's Back used to keep the engine's plain prev on the
// reasoning that it is "Spotify's rule", but read off the engine's position
// that rule only ever restarted the song there too (#1797). A Back pressed in
// the Spotify app itself goes to the engine directly and is not STR's to
// decide.
package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// heardPrevBackWindow is how much of a song the room may have heard and Back
// still goes to the song before (#1678: "pressing it in the first ~10
// seconds"). Wider than Spotify's 3 s on purpose: the lag it is measured
// against is read once per track boundary, in whole seconds of the box's
// clock, and a 3 s window would sit inside that error, so an early press
// could still read as a restart. One window for the remote and the apps, so
// the two Back buttons of one speaker agree.
const heardPrevBackWindow = 10 * time.Second

// heardPrevDefaultLag is the audio in flight to the speaker assumed when
// nothing better is known and the running lead cap is not either: the lead STR
// keeps on purpose by default.
const heardPrevDefaultLag = oggLeadCapSec * time.Second

// heardPrevMaxLag bounds a lag that is believed. A reading above it comes from
// a box clock that did not restart, and would turn every Back into a go-back.
const heardPrevMaxLag = 30 * time.Second

// heardPrevGoesBack reports whether Back goes to the song before
// (true) or restarts the current one: the engine is enginePos into the song,
// lag of which the speaker has not played yet.
func heardPrevGoesBack(enginePos, lag time.Duration) bool {
	return enginePos-lag < heardPrevBackWindow
}

// heardLag is the audio in flight between the engine and the room, and where
// that number came from: the current attachment's lag (currentAttachLag: a
// box reading, else delivered audio against the wall clock), else the lead
// cap the drain runs with ("lead-cap").
//
// The attach reading is never taken as the lag (#1797, live Portable): it is
// the zero check with both sides at zero, and believing its 0 made an 8 s
// lag read as none, so a Back 15 s after a skip saw the engine's 23 s as 23 s
// heard and restarted the song.
func (m *Manager) heardLag() (lag time.Duration, source string) {
	if l, src, ok := m.currentAttachLag(time.Now()); ok && l <= heardPrevMaxLag {
		return l, src
	}
	m.mu.Lock()
	lc := m.leadCap
	m.mu.Unlock()
	if lc <= 0 || lc > heardPrevMaxLag {
		lc = heardPrevDefaultLag
	}
	return lc, "lead-cap"
}

// engineTrackPosition reads how far the engine is into the loaded song, from
// go-librespot's /status. ok is false when no song is loaded or the status
// cannot be read.
func (m *Manager) engineTrackPosition(ctx context.Context) (time.Duration, bool) {
	b, err := m.apiGet(ctx, "/status")
	if err != nil {
		return 0, false
	}
	var st struct {
		Track *struct {
			Position *int64 `json:"position"`
		} `json:"track"`
	}
	if json.Unmarshal(b, &st) != nil || st.Track == nil || st.Track.Position == nil {
		return 0, false
	}
	return time.Duration(*st.Track.Position) * time.Millisecond, true
}

// HeardPrev is STR's Back on a Spotify playlist. back reports
// which way it went: true for the song before, false for a restart of the
// current song. The skip cut is armed like for any STR skip, so the old
// song's unsent tail is dropped. A status that cannot be read leaves the
// decision to the engine (a plain prev, Spotify's own rule).
func (m *Manager) HeardPrev(ctx context.Context) (back bool, err error) {
	if m == nil {
		return false, fmt.Errorf("spotify: no engine")
	}
	pos, ok := m.engineTrackPosition(ctx)
	if !ok {
		m.logger.Info("spotify: back, engine position unknown, leaving it to the engine")
		m.NoteSkip()
		return false, m.Prev(ctx)
	}
	lag, lagSource := m.heardLag()
	back = heardPrevGoesBack(pos, lag)
	to := "start of song"
	if back {
		to = "previous song"
	}
	m.logger.Info("spotify: back",
		"engineSec", pos.Seconds(), "lagSec", lag.Seconds(), "lagSource", lagSource,
		"heardSec", max(0, (pos-lag).Seconds()), "to", to)
	m.NoteSkip()
	if !back {
		return false, m.Prev(ctx)
	}
	// At position 0 the engine's prev always means the song before. A seek
	// that fails leaves only the plain prev, which then restarts the song:
	// still an answer to the press, and the log above says what was meant.
	if err := m.apiPost(ctx, "/player/seek", `{"position":0}`); err != nil {
		m.logger.Warn("spotify: back, could not go to the start of the song first, the engine decides", "err", err)
		return false, m.Prev(ctx)
	}
	// Armed again right before the prev: the seek may have produced a track
	// boundary of its own that used the first arm up.
	m.NoteSkip()
	return true, m.Prev(ctx)
}

// Repeat modes the remote's repeat key steps through, in the Spotify app's
// order.
const (
	RepeatOff     = "off"
	RepeatContext = "context"
	RepeatTrack   = "track"
)

// playModes is the shuffle and repeat state of the live engine.
type playModes struct {
	Shuffle       bool `json:"shuffle_context"`
	RepeatContext bool `json:"repeat_context"`
	RepeatTrack   bool `json:"repeat_track"`
}

func (p playModes) repeat() string {
	switch {
	case p.RepeatTrack:
		return RepeatTrack
	case p.RepeatContext:
		return RepeatContext
	}
	return RepeatOff
}

// nextRepeat is the mode one press of the repeat key goes to: off -> the whole
// playlist -> the one song -> off.
func nextRepeat(cur string) string {
	switch cur {
	case RepeatOff:
		return RepeatContext
	case RepeatContext:
		return RepeatTrack
	}
	return RepeatOff
}

func (m *Manager) readPlayModes(ctx context.Context) (playModes, error) {
	b, err := m.apiGet(ctx, "/status")
	if err != nil {
		return playModes{}, err
	}
	var p playModes
	if err := json.Unmarshal(b, &p); err != nil {
		return playModes{}, fmt.Errorf("go-librespot /status: %w", err)
	}
	return p, nil
}

// ToggleShuffle switches the live playlist's shuffle and reports the new
// state. The state is read fresh, so a change made in the Spotify app since
// is what the press flips.
func (m *Manager) ToggleShuffle(ctx context.Context) (on bool, err error) {
	p, err := m.readPlayModes(ctx)
	if err != nil {
		return false, err
	}
	on = !p.Shuffle
	if err := m.apiPost(ctx, "/player/shuffle_context", fmt.Sprintf(`{"shuffle_context":%t}`, on)); err != nil {
		return p.Shuffle, err
	}
	return on, nil
}

// CycleRepeat moves the live playlist's repeat one step (nextRepeat) and
// reports the new mode.
func (m *Manager) CycleRepeat(ctx context.Context) (mode string, err error) {
	p, err := m.readPlayModes(ctx)
	if err != nil {
		return "", err
	}
	mode = nextRepeat(p.repeat())
	if err := m.setRepeatSwitches(ctx, mode != RepeatOff, mode == RepeatTrack); err != nil {
		return p.repeat(), err
	}
	return mode, nil
}

// setRepeatSwitches writes both engine repeat switches. The song switch goes
// first when it goes off and last when it goes on, so the playlist switch in
// between never leaves the engine looping one song the user stepped past.
func (m *Manager) setRepeatSwitches(ctx context.Context, ctxOn, trackOn bool) error {
	if !trackOn {
		if err := m.apiPost(ctx, "/player/repeat_track", `{"repeat_track":false}`); err != nil {
			return err
		}
		return m.apiPost(ctx, "/player/repeat_context", fmt.Sprintf(`{"repeat_context":%t}`, ctxOn))
	}
	if err := m.apiPost(ctx, "/player/repeat_context", fmt.Sprintf(`{"repeat_context":%t}`, ctxOn)); err != nil {
		return err
	}
	return m.apiPost(ctx, "/player/repeat_track", `{"repeat_track":true}`)
}
