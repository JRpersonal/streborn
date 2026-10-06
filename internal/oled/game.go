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
	"sync"
	"sync/atomic"
	"time"
)

// The easter egg: in standby or while playing, the remote code
// thumbs up, thumbs up, thumbs down, thumbs down, previous, skip, previous,
// skip starts a round of Blockfall on the Portable's display. A round ends by
// itself at game over, after 45 s without a key, or after 30 minutes; the
// caller then puts the speaker back the way it was.

// eggCode is the remote sequence, by the key names of the speaker's own
// trace (internal/boxlog).
var eggCode = []string{"THUMBS_UP", "THUMBS_UP", "THUMBS_DOWN", "THUMBS_DOWN", "PREV_TRACK", "NEXT_TRACK", "PREV_TRACK", "NEXT_TRACK"}

const (
	eggWindow     = 12 * time.Second // the whole code must fit in this
	roundIdle     = 45 * time.Second
	roundMax      = 30 * time.Minute
	gameOverShown = 5 * time.Second
)

var (
	gameActive atomic.Bool
	eggMu      sync.Mutex
	eggPresses []eggPress
	eggStart   func()
)

type eggPress struct {
	name string
	at   time.Time
}

// GameActive reports whether a round is running; key bindings (webhooks,
// group keys) stand down meanwhile.
func GameActive() bool { return gameActive.Load() }

// SetGameStarter registers what the code starts (the webui's round runner,
// which owns playback state).
func SetGameStarter(f func()) {
	eggMu.Lock()
	eggStart = f
	eggMu.Unlock()
}

// FeedKey is called for every physical key press the speaker decodes. When
// the last presses spell the code, the registered starter runs.
func FeedKey(name string) {
	if GameActive() || !Enabled() || !panelSupported() {
		return
	}
	eggMu.Lock()
	now := time.Now()
	eggPresses = append(eggPresses, eggPress{name, now})
	if len(eggPresses) > len(eggCode) {
		eggPresses = eggPresses[len(eggPresses)-len(eggCode):]
	}
	match := codeMatches(eggPresses, now)
	start := eggStart
	if match {
		eggPresses = nil
	}
	eggMu.Unlock()
	if match && start != nil {
		go start()
	}
}

func codeMatches(p []eggPress, now time.Time) bool {
	if len(p) != len(eggCode) || now.Sub(p[0].at) > eggWindow {
		return false
	}
	for i, k := range eggCode {
		if p[i].name != k {
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

// PlayRound runs one round: intro, game, game-over screen. It blocks until the
// round ends or stop closes. onStart is called once the panel is ours and the
// round is about to begin (the caller starts the music there).
func PlayRound(stop <-chan struct{}, onStart func(), logger *slog.Logger) (Result, error) {
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
				logger.Info("blockfall: key trace ended", "err", err)
			}
		case <-time.After(3 * time.Second):
		}
	}()

	seed := uint64(time.Now().UnixNano())
	intro := NewLogo("BLOCKFALL", 4.5, seed)
	g := NewBlockfall(seed)
	var res Result
	phase := 0 // 0 intro, 1 game, 2 game over
	var gameStart, lastKey, overAt time.Time
	started := false
	err := play(func(t float64, buf []byte) bool {
		now := time.Now()
		switch phase {
		case 0:
			intro.Frame(t, buf)
			if intro.Done(t) {
				phase, gameStart, lastKey = 1, now, now
				if onStart != nil && !started {
					started = true
					onStart()
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
			// play() runs at 20 fps; the game is tuned for 30 steps a second
			g.Step()
			if g.frame%2 == 0 {
				g.Step()
			}
			switch {
			case g.Over:
				res.Reason = "game over"
			case now.Sub(lastKey) > roundIdle:
				res.Reason = "idle"
				g.Over = true
			case now.Sub(gameStart) > roundMax:
				res.Reason = "time"
				g.Over = true
			}
			g.Frame(buf)
			if g.Over {
				phase, overAt = 2, now
				res.Screen = append([]byte(nil), buf...)
			}
			return true
		default:
			g.Frame(buf)
			return now.Sub(overAt) < gameOverShown
		}
	}, nil, roundMax+time.Minute, stop)
	res.Score, res.Lines = g.Score, g.Lines
	if res.Reason == "" {
		res.Reason = "stopped"
	}
	if res.Screen == nil {
		res.Screen = make([]byte, FrameSize)
		g.Frame(res.Screen)
	}
	return res, err
}

// Score storage on NAND, written once per round so the app can collect it and
// offer to share it. Vars so tests can redirect them.
var (
	ScoresPath     = "/mnt/nv/streborn/blockfall.json"
	ScreenshotPath = "/mnt/nv/streborn/blockfall-last.png"
)

// Scores is what the app reads back.
type Scores struct {
	Best     int       `json:"best"`
	BestAt   time.Time `json:"bestAt,omitzero"`
	Last     int       `json:"last"`
	LastRows int       `json:"lastRows"`
	LastAt   time.Time `json:"lastAt,omitzero"`
	Rounds   int       `json:"rounds"`
}

// LoadScores reads the stored scores; missing means none yet.
func LoadScores() Scores {
	var s Scores
	if b, err := os.ReadFile(ScoresPath); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// SaveRound records a finished round and its last screen. Rounds that never
// started (score 0 and no rows) are not worth a NAND write.
func SaveRound(r Result, now time.Time) (Scores, error) {
	s := LoadScores()
	if r.Score == 0 && r.Lines == 0 {
		return s, nil
	}
	s.Rounds++
	s.Last, s.LastRows, s.LastAt = r.Score, r.Lines, now
	if r.Score > s.Best {
		s.Best, s.BestAt = r.Score, now
	}
	b, _ := json.Marshal(s)
	if err := writeAtomic(ScoresPath, b); err != nil {
		return s, err
	}
	if png := ScreenPNG(r.Screen); png != nil {
		if err := writeAtomic(ScreenshotPath, png); err != nil {
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
