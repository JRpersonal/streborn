package oled

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// The easter eggs: in standby or while playing, a code on the remote starts a
// round of one of the games on the speaker's display. A round ends by itself
// at game over, after 45 s without a key, or after 30 minutes; the caller
// then puts the speaker back the way it was.

// arcadeGame is one game's rules and drawing. Step runs 30 times a second.
type arcadeGame interface {
	Key(key, state int)
	Step()
	Frame(buf []byte)
	// State reports whether the round is over, its score, and its second
	// number (rows for Blockfall, waves for Starguard).
	State() (over bool, score, count int)
	// End finishes the round from outside (idle, time limit).
	End()
}

// GameDef describes one game: how it is started and what it sounds like.
type GameDef struct {
	ID, Title string
	Code      []string // remote sequence, by the key names of the speaker's own trace
	Intro     Track
	Over      Track
	// Live: the round's music runs through the game too and follows it
	// (liveaudio.go). Only for games played without Previous/Skip, which are
	// also the firmware's own skip keys and stop the stream.
	Live    bool
	newGame func(seed uint64) arcadeGame
}

// Games are all games, in the order the app lists them.
var Games = []GameDef{
	{
		ID: "blockfall", Title: "BLOCKFALL",
		Code:    []string{"THUMBS_UP", "THUMBS_UP", "THUMBS_DOWN", "THUMBS_DOWN", "PREV_TRACK", "NEXT_TRACK", "PREV_TRACK", "NEXT_TRACK"},
		Intro:   IntroTrack,
		Over:    OverTrack,
		newGame: func(seed uint64) arcadeGame { return NewBlockfall(seed) },
	},
	{
		ID: "starguard", Title: "STARGUARD",
		Code:    []string{"THUMBS_DOWN", "THUMBS_UP", "THUMBS_DOWN", "THUMBS_UP", "NEXT_TRACK", "NEXT_TRACK", "NEXT_TRACK"},
		Intro:   sgIntroTrack,
		Over:    sgOverTrack,
		Live:    true,
		newGame: func(seed uint64) arcadeGame { return NewStarguard(seed) },
	},
}

// Game looks a game up by its ID.
func Game(id string) (GameDef, bool) {
	for _, g := range Games {
		if g.ID == id {
			return g, true
		}
	}
	return GameDef{}, false
}

const (
	eggWindow     = 12 * time.Second // a whole code must fit in this
	roundIdle     = 45 * time.Second
	roundMax      = 30 * time.Minute
	gameOverShown = 5 * time.Second
)

var (
	gameActive atomic.Bool
	eggMu      sync.Mutex
	eggPresses []eggPress
	eggStart   func(id string)
)

type eggPress struct {
	name string
	at   time.Time
}

// GameActive reports whether a round is running; key bindings (webhooks,
// group keys) stand down meanwhile.
func GameActive() bool { return gameActive.Load() }

// SetGameStarter registers what a code starts (the webui's round runner,
// which owns playback state). It gets the game's ID.
func SetGameStarter(f func(id string)) {
	eggMu.Lock()
	eggStart = f
	eggMu.Unlock()
}

// FeedKey is called for every physical key press the speaker decodes. When
// the last presses spell a game's code, the registered starter runs.
func FeedKey(name string) {
	if GameActive() || !Enabled() || !panelSupported() {
		return
	}
	eggMu.Lock()
	now := time.Now()
	eggPresses = append(eggPresses, eggPress{name, now})
	longest := 0
	for _, g := range Games {
		longest = max(longest, len(g.Code))
	}
	if len(eggPresses) > longest {
		eggPresses = eggPresses[len(eggPresses)-longest:]
	}
	id := matchCode(eggPresses, now)
	start := eggStart
	if id != "" {
		eggPresses = nil
	}
	eggMu.Unlock()
	if id != "" && start != nil {
		go start(id)
	}
}

// matchCode returns the game whose code the most recent presses spell.
func matchCode(p []eggPress, now time.Time) string {
	for _, g := range Games {
		if codeMatches(p, g.Code, now) {
			return g.ID
		}
	}
	return ""
}

func codeMatches(p []eggPress, code []string, now time.Time) bool {
	if len(p) < len(code) {
		return false
	}
	tail := p[len(p)-len(code):]
	if now.Sub(tail[0].at) > eggWindow {
		return false
	}
	for i, k := range code {
		if tail[i].name != k {
			return false
		}
	}
	return true
}

// Result is how a round ended.
type Result struct {
	Score, Lines int
	Reason       string // "game over", "idle", "time", "power", "stopped"
	Screen       []byte // last frame, grey levels 0..15
}

// ErrBusy means the panel or a round is already in use.
var ErrBusy = errors.New("display busy")

// RoundHooks lets the caller run the music around a round. Every hook is
// called from the render loop and must not block. All are optional.
type RoundHooks struct {
	// IntroReady reports whether the intro may end: the caller holds it
	// while the speaker wakes and the intro music starts (capped at introMax).
	IntroReady func() bool
	// GameStart runs when the intro ends and the game begins (music off).
	GameStart func()
	// GameOver runs when the game-over screen appears (jingle on).
	GameOver func()
	// OverHold keeps the game-over screen up past gameOverShown while it
	// reports true (the jingle is still playing), capped at gameOverMax.
	OverHold func() bool
	// Live is the round's live music, handed to a game that drives it.
	Live *LiveAudio
}

const (
	introMin    = 4.5 // seconds, the logo animation without a wait
	introMax    = 16.0
	gameOverMax = 12 * time.Second
)

// PlayRound runs one round of game id: intro, game, game-over screen. It
// blocks until the round ends or stop closes.
func PlayRound(id string, stop <-chan struct{}, hooks RoundHooks, logger *slog.Logger) (Result, error) {
	def, ok := Game(id)
	if !ok {
		return Result{}, errors.New("no such game")
	}
	if !panelSupported() || !boseAppDisplayReady() {
		return Result{}, errors.New("no supported panel")
	}
	if !gameActive.CompareAndSwap(false, true) {
		return Result{}, ErrBusy
	}
	defer gameActive.Store(false)
	if !busy.TryLock() {
		return Result{}, ErrBusy
	}
	defer busy.Unlock()

	keys := make(chan KeyPress, 32)
	keyQuit := make(chan struct{})
	keyDone := make(chan error, 1)
	go func() { keyDone <- traceKeys(keyQuit, keys) }()
	defer func() {
		close(keyQuit)
		select {
		case err := <-keyDone:
			if err != nil {
				logger.Info("game: key trace ended", "err", err)
			}
		case <-time.After(3 * time.Second):
		}
	}()

	seed := uint64(time.Now().UnixNano())
	// The intro holds its logo until the music is up, then zooms out as the
	// plain animation would.
	intro := NewLogo(def.Title, introMin, seed)
	intro.Hold = true
	g := def.newGame(seed)
	if ag, ok := g.(interface{ setAudio(*LiveAudio) }); ok && hooks.Live != nil {
		ag.setAudio(hooks.Live)
	}
	// the game shows the record on its end screen, and NEW HIGHSCORE when
	// this round beats it, so the shared screenshot says it all
	if bg, ok := g.(interface{ setBest(int) }); ok {
		bg.setBest(LoadScores(id).Best)
	}
	var res Result
	phase := 0 // 0 intro, 1 game, 2 game over
	steps := 0 // game steps run so far, at 30 a second of game time
	var gameStart, lastKey, overAt time.Time
	err := play(func(t float64, buf []byte) bool {
		now := time.Now()
		switch phase {
		case 0:
			if t >= introMin-zoomLen && (hooks.IntroReady == nil || hooks.IntroReady() || t >= introMax) {
				intro.Exit(t)
			}
			intro.Frame(t, buf)
			if intro.Done(t) {
				phase, gameStart, lastKey = 1, now, now
				if hooks.GameStart != nil {
					hooks.GameStart()
				}
			}
			return true
		case 1:
			for drained := false; !drained; {
				select {
				case k := <-keys:
					if k.Key == KeyPower && k.State == KeyPressed {
						// the speaker handles power itself; the round just ends
						res.Reason = "power"
						return false
					}
					g.Key(k.Key, k.State)
					lastKey = now
				default:
					drained = true
				}
			}
			// the games are tuned for 30 steps a second, whatever the frame
			// rate; a late frame catches up (capped, so a stall does not
			// fast-forward the game)
			due := int(now.Sub(gameStart) * 30 / time.Second)
			for n := 0; steps < due && n < 4; n++ {
				g.Step()
				steps++
			}
			steps = max(steps, due-4)
			over, _, _ := g.State()
			switch {
			case over:
				res.Reason = "game over"
			case now.Sub(lastKey) > roundIdle:
				res.Reason = "idle"
				g.End()
			case now.Sub(gameStart) > roundMax:
				res.Reason = "time"
				g.End()
			}
			g.Frame(buf)
			if over, _, _ = g.State(); over {
				phase, overAt = 2, now
				res.Screen = append([]byte(nil), buf...)
				if hooks.GameOver != nil {
					hooks.GameOver()
				}
			}
			return true
		default:
			g.Frame(buf)
			shown := now.Sub(overAt)
			return shown < gameOverShown || (hooks.OverHold != nil && hooks.OverHold() && shown < gameOverMax)
		}
	}, nil, roundMax+time.Minute, stop)
	_, res.Score, res.Lines = g.State()
	if res.Reason == "" {
		res.Reason = "stopped"
	}
	if res.Screen == nil {
		res.Screen = make([]byte, FrameSize)
		g.Frame(res.Screen)
	}
	return res, err
}

// Score storage on NAND, one file per game, written once per round so the
// app can collect it and offer to share it. A var so tests can redirect it.
var ScoreDir = "/mnt/nv/streborn"

// ScoresPath is where game id keeps its scores.
func ScoresPath(id string) string { return filepath.Join(ScoreDir, id+".json") }

// ScreenshotPath is where game id keeps its last round's final screen.
func ScreenshotPath(id string) string { return filepath.Join(ScoreDir, id+"-last.png") }

// LastRoundAt is when the most recent round of any game was saved, zero when
// none was. It only stats the score files, so the version endpoint can carry
// it on every probe.
func LastRoundAt() time.Time {
	var last time.Time
	for _, g := range Games {
		if st, err := os.Stat(ScoresPath(g.ID)); err == nil && st.ModTime().After(last) {
			last = st.ModTime()
		}
	}
	return last
}

// Scores is what the app reads back.
type Scores struct {
	Best     int       `json:"best"`
	BestAt   time.Time `json:"bestAt,omitzero"`
	Last     int       `json:"last"`
	LastRows int       `json:"lastRows"`
	LastAt   time.Time `json:"lastAt,omitzero"`
	Rounds   int       `json:"rounds"`
}

// LoadScores reads game id's stored scores; missing means none yet.
func LoadScores(id string) Scores {
	var s Scores
	if b, err := os.ReadFile(ScoresPath(id)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// SaveRound records a finished round of game id and its last screen. Rounds
// that never started (score 0 and nothing cleared) are not worth a NAND write.
func SaveRound(id string, r Result, now time.Time) (Scores, error) {
	s := LoadScores(id)
	if r.Score == 0 && r.Lines == 0 {
		return s, nil
	}
	s.Rounds++
	s.Last, s.LastRows, s.LastAt = r.Score, r.Lines, now
	if r.Score > s.Best {
		s.Best, s.BestAt = r.Score, now
	}
	b, _ := json.Marshal(s)
	if err := writeAtomic(ScoresPath(id), b); err != nil {
		return s, err
	}
	if png := ScreenPNG(r.Screen); png != nil {
		if err := writeAtomic(ScreenshotPath(id), png); err != nil {
			return s, err
		}
	}
	return s, nil
}

// ScreenPNG renders a frame 3x up as a greyscale PNG, the size people share.
func ScreenPNG(frame []byte) []byte {
	if len(frame) != FrameSize {
		return nil
	}
	const k = 3
	img := image.NewGray(image.Rect(0, 0, Width*k, Height*k))
	for y := 0; y < Height*k; y++ {
		for x := 0; x < Width*k; x++ {
			img.SetGray(x, y, color.Gray{Y: min(frame[(y/k)*Width+x/k], 15) * 0x11})
		}
	}
	var b bytes.Buffer
	if png.Encode(&b, img) != nil {
		return nil
	}
	return b.Bytes()
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".str-new"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
