// remotekeys.go: what the speaker's own remote does to a Spotify playlist
// beyond Next: the Back key going to the previous song (#1678), and the
// shuffle and repeat switches a thumbs key can carry (#1679).
//
// Back. go-librespot's /player/prev follows Spotify's rule: past 3 seconds of
// the song it restarts the song, inside them it goes to the song before. Its
// position is the one the Spotify app shows, i.e. the audio the engine has
// produced, and STR keeps roughly ten seconds of that in flight to the
// speaker on purpose (leadCapSec, boxlag.go). So the engine is always more
// than 3 seconds into a song the room has only just started hearing, and the
// remote's Back could only ever restart: the user never got further back.
//
// RemotePrev decides on what the room hears instead: the engine position less
// the measured buffer lag (the newest boxlag reading, or the lead cap when
// nothing was measured yet). Inside the first remotePrevBackWindow of the
// song heard, Back goes to the song before; later it restarts the song. To go
// back regardless of the engine's own 3-second rule the engine is first put
// to the start of the song, then asked for prev: at position 0 its prev always
// means the song before. The /player/prev API has no allow_seeking switch
// that would say this directly.
//
// Only the remote takes this path. The app's own Back keeps Spotify's
// behaviour (webui transportSkip -> Prev).
package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// remotePrevBackWindow is how much of a song the room may have heard and the
// remote's Back still goes to the song before (#1678: "pressing it in the
// first ~10 seconds").
const remotePrevBackWindow = 10 * time.Second

// remotePrevDefaultLag is the audio in flight to the speaker assumed when no
// buffer-lag reading exists yet: the lead STR keeps on purpose.
const remotePrevDefaultLag = oggLeadCapSec * time.Second

// remotePrevMaxLag bounds a measured lag that is believed. A reading above it
// comes from a box clock that did not restart, and would turn every Back into
// a go-back.
const remotePrevMaxLag = 30 * time.Second

// remotePrevGoesBack reports whether the remote's Back goes to the song before
// (true) or restarts the current one: the engine is enginePos into the song,
// lag of which the speaker has not played yet.
func remotePrevGoesBack(enginePos, lag time.Duration) bool {
	return enginePos-lag < remotePrevBackWindow
}

// heardLag is the audio in flight between the engine and the room: the newest
// measurement when there is a believable one (measured=true), the lead cap
// otherwise.
func (m *Manager) heardLag() (lag time.Duration, measured bool) {
	l := m.LastBoxLag()
	d := time.Duration(l.DiffSec * float64(time.Second))
	if l.At.IsZero() || d < 0 || d > remotePrevMaxLag {
		return remotePrevDefaultLag, false
	}
	return d, true
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

// RemotePrev is the remote's Back key on a Spotify playlist. back reports
// which way it went: true for the song before, false for a restart of the
// current song. The skip cut is armed like for any STR skip, so the old
// song's unsent tail is dropped. A status that cannot be read leaves the
// decision to the engine (a plain prev, Spotify's own rule).
func (m *Manager) RemotePrev(ctx context.Context) (back bool, err error) {
	if m == nil {
		return false, fmt.Errorf("spotify: no engine")
	}
	pos, ok := m.engineTrackPosition(ctx)
	if !ok {
		m.logger.Info("spotify: remote back, engine position unknown, leaving it to the engine")
		m.NoteSkip()
		return false, m.Prev(ctx)
	}
	lag, measured := m.heardLag()
	back = remotePrevGoesBack(pos, lag)
	to := "start of song"
	if back {
		to = "previous song"
	}
	m.logger.Info("spotify: remote back",
		"engineSec", pos.Seconds(), "lagSec", lag.Seconds(), "lagMeasured", measured,
		"heardSec", max(0, (pos-lag).Seconds()), "to", to)
	m.NoteSkip()
	if !back {
		return false, m.Prev(ctx)
	}
	// At position 0 the engine's prev always means the song before. A seek
	// that fails leaves only the plain prev, which then restarts the song:
	// still an answer to the press, and the log above says what was meant.
	if err := m.apiPost(ctx, "/player/seek", `{"position":0}`); err != nil {
		m.logger.Warn("spotify: remote back, could not go to the start of the song first, the engine decides", "err", err)
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
