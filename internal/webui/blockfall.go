package webui

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/oled"
)

// The hidden games on the speaker display (internal/oled). A remote code
// starts a round; this side owns everything around it: the music
// the speaker plays over UPnP loopback, noticing when the user takes the
// speaker back, saving the score, and putting the speaker back the way it was
// (same restore as an announcement: previous stream and volume, or standby).

// The speaker pulls the music from this agent over loopback, like the
// announcement audio: the intro loop, then the game-over jingle. One URL each
// per game, so a glance at now_playing tells which one the speaker is on.
const gameMusicPrefix = "http://127.0.0.1:8888/game/"

func gameIntroURL(id string) string { return gameMusicPrefix + id + ".wav" }

func gameOverURL(id string) string { return gameMusicPrefix + id + "-over.wav" }

// blockfallMaxVolume caps the volume during a round; the user's own volume is
// restored afterwards. Waking from standby restores the last volume, which can
// be far louder than a game should start.
const blockfallMaxVolume = 30

// blockfallIntroMusic is how long the intro logo keeps playing once its music
// is confirmed, so the music is heard before the game starts silent.
const blockfallIntroMusic = 3 * time.Second

type blockfallMusic struct {
	mu    sync.Mutex
	done  chan struct{} // closed when the current music must stop; nil = none
	track oled.Track
}

// start switches the stream over to track; a speaker still pulling the
// previous one gets its stream closed.
func (m *blockfallMusic) start(track oled.Track) {
	m.mu.Lock()
	if m.done != nil {
		close(m.done)
	}
	m.done = make(chan struct{})
	m.track = track
	m.mu.Unlock()
}

func (m *blockfallMusic) stop() {
	m.mu.Lock()
	if m.done != nil {
		close(m.done)
		m.done = nil
	}
	m.mu.Unlock()
}

func (m *blockfallMusic) current() (<-chan struct{}, oled.Track) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.done, m.track
}

// handleBlockfallMusic streams the current music to the speaker. Loopback
// only, and only while a round runs.
func (s *Server) handleBlockfallMusic(w http.ResponseWriter, r *http.Request) {
	if host, _, _ := net.SplitHostPort(r.RemoteAddr); !net.ParseIP(host).IsLoopback() {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	done, track := s.blockfall.current()
	if done == nil {
		http.Error(w, "no round running", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	if r.Method == http.MethodHead {
		return
	}
	flush := func() {}
	if f, ok := w.(http.Flusher); ok {
		flush = f.Flush
	}
	merged := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-r.Context().Done():
		}
		close(merged)
	}()
	_ = oled.StreamMusic(w, flush, merged, track)
}

// handleBlockfallScores answers GET with Blockfall's stored scores. Kept for
// the v1.0.5 app, which reads only this one.
func (s *Server) handleBlockfallScores(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, oled.LoadScores("blockfall"))
}

// handleBlockfallScreenshot serves Blockfall's last final screen (v1.0.5 app).
func (s *Server) handleBlockfallScreenshot(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	http.ServeFile(w, r, oled.ScreenshotPath("blockfall"))
}

// arcadeEntry is one game in the /api/box/arcade answer.
type arcadeEntry struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	oled.Scores
}

// handleArcade answers GET with every game's stored scores, for the app. It
// lists all games; the app shows only those that were played.
func (s *Server) handleArcade(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	out := []arcadeEntry{}
	for _, g := range oled.Games {
		out = append(out, arcadeEntry{ID: g.ID, Title: g.Title, Scores: oled.LoadScores(g.ID)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"games": out})
}

// handleArcadeScreenshot serves one game's last final screen as PNG
// (?game=<id>).
func (s *Server) handleArcadeScreenshot(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	g, ok := oled.Game(r.URL.Query().Get("game"))
	if !ok {
		http.Error(w, "no such game", http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, oled.ScreenshotPath(g.ID))
}

// blockfallPlay starts url (serving track) on the speaker and waits until
// now_playing confirms it, sending the command again up to attempts times:
// straight out of standby the speaker can take the play command and still
// stay on INVALID_SOURCE (seen twice on the Portable); a second command fixed
// it each time. It gives up early once current reports false (the round moved
// on to another phase).
func (s *Server) blockfallPlay(ctx context.Context, url string, track oled.Track, attempts int, current func() bool) bool {
	s.blockfall.start(track)
	for attempt := 1; attempt <= attempts; attempt++ {
		if !current() {
			return false
		}
		if err := s.renderer.PlayURLMime(ctx, url, "Blockfall", "", "audio/wav"); err != nil {
			s.logger.Warn("game: music play command failed", "attempt", attempt, "err", err)
		}
		for i := 0; i < 8; i++ {
			time.Sleep(time.Second)
			if !current() {
				return false
			}
			np := fetchNowPlaying(ctx, s.boxHost)
			if np.Location == url && (np.PlayStatus == "PLAY_STATE" || np.PlayStatus == "BUFFERING_STATE") {
				s.logger.Info("game: music playing", "url", url, "attempt", attempt)
				return true
			}
		}
	}
	return false
}

// blockfallTakenOver decides from now_playing whether the user took the
// speaker back (a preset, the app, standby). Our own stream and the
// in-between states of a start do not count, and neither does anything on
// UPnP while the game runs silent: Previous/Skip are the speaker's own skip
// keys too and leave the stopped UPnP source in states of their own.
func blockfallTakenOver(np nowPlayingSnapshot, inGame bool) bool {
	switch {
	case np.Source == "" || np.Source == "INVALID_SOURCE":
		return false
	case strings.HasPrefix(np.Location, gameMusicPrefix):
		return false
	case inGame && np.Source == "UPNP":
		return false
	}
	return true
}

// Round phases, as the runner sees them.
const (
	bfIntro int32 = iota
	bfGame
	bfOver
)

// startGame runs one round of game id end to end. Started by a remote code
// (oled.FeedKey); a second code while a round runs is ignored there.
//
// Music plays only under the intro and the game-over screen. The game itself
// runs silent: Previous/Skip move the piece, and the speaker's firmware runs
// its own skip on the UPnP source for every press, which tore the music down
// mid-game. STR cannot keep that key from the firmware, so there is no stream
// to tear down while the game runs.
func (s *Server) startGame(id string) {
	def, ok := oled.Game(id)
	if s.renderer == nil || !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	prev := s.snapshotNowPlaying(ctx)
	wasStandby := prev.Source == "STANDBY" || prev.Source == ""
	prevVol := s.readVolume(ctx)
	s.logger.Info("game: code entered, starting a round", "game", id, "wasStandby", wasStandby, "source", prev.Source)

	// A game with live music keeps one stream from the intro to the jingle.
	var live *oled.LiveAudio
	introTrack := def.Intro
	if def.Live {
		live = oled.NewLiveAudio()
		introTrack = oled.Track{Live: live}
	}

	stop := make(chan struct{})
	var stopOnce sync.Once
	var phase atomic.Int32
	var takenOver, watching, introGaveUp, overPending atomic.Bool
	var introAt, overUntil atomic.Int64 // unix nanoseconds, 0 = not yet
	inPhase := func(p int32) func() bool {
		return func() bool {
			select {
			case <-stop:
				return false
			default:
			}
			return phase.Load() == p
		}
	}

	// Wake and start the intro music right away; the intro logo holds until
	// it plays (oled caps that wait).
	go func() {
		if wasStandby {
			if err := boxcli.WakeAndWait(ctx, s.boxHost, 8*time.Second, s.logger); err != nil {
				s.logger.Warn("game: wake failed, playing anyway", "err", err)
			}
		}
		if !s.blockfallPlay(ctx, gameIntroURL(id), introTrack, 3, inPhase(bfIntro)) {
			s.logger.Warn("game: intro music did not start, the round goes on without it")
			introGaveUp.Store(true)
			return
		}
		introAt.Store(time.Now().UnixNano())
		watching.Store(true)
		// The wake restores the last volume a moment later; hold the cap
		// through that.
		for i := 0; i < 8; i++ {
			if v := s.readVolume(ctx); v > blockfallMaxVolume {
				_ = boxapi.New(s.boxHost).SetVolume(ctx, blockfallMaxVolume)
			}
			time.Sleep(time.Second)
		}
	}()
	// The user takes the speaker back (a preset, the app, power): the round
	// ends and nothing is restored over what they chose. Only once the intro
	// music was confirmed playing: a start that never took leaves the speaker
	// on INVALID_SOURCE, which is no user action and must not end the round.
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(3 * time.Second):
			}
			if !watching.Load() {
				continue
			}
			np := fetchNowPlaying(ctx, s.boxHost)
			if blockfallTakenOver(np, phase.Load() == bfGame) {
				s.logger.Info("game: speaker taken over, ending the round", "source", np.Source)
				takenOver.Store(true)
				stopOnce.Do(func() { close(stop) })
				return
			}
		}
	}()

	hooks := oled.RoundHooks{
		IntroReady: func() bool {
			at := introAt.Load()
			return introGaveUp.Load() || (at != 0 && time.Since(time.Unix(0, at)) >= blockfallIntroMusic)
		},
		Live: live,
		GameStart: func() {
			phase.Store(bfGame)
			if live != nil {
				live.StartGame()
				return
			}
			s.blockfall.stop()
			go func() {
				if err := s.renderer.Stop(ctx); err != nil {
					s.logger.Info("game: stopping the intro music", "err", err)
				}
			}()
		},
		GameOver: func() {
			phase.Store(bfOver)
			if live != nil {
				// same stream: the jingle reaches the speaker after what it
				// has buffered, about two seconds
				live.GameOver()
				overUntil.Store(time.Now().Add(def.Over.Length() + 3*time.Second).UnixNano())
				return
			}
			overPending.Store(true)
			go func() {
				defer overPending.Store(false)
				if s.blockfallPlay(ctx, gameOverURL(id), def.Over, 1, inPhase(bfOver)) {
					// The speaker reports PLAY_STATE about when the sound starts;
					// keep the screen up through the jingle and its fade.
					overUntil.Store(time.Now().Add(def.Over.Length() + time.Second).UnixNano())
				}
			}()
		},
		OverHold: func() bool {
			return overPending.Load() || time.Now().UnixNano() < overUntil.Load()
		},
	}
	res, err := oled.PlayRound(id, stop, hooks, s.logger.With("comp", "game", "game", id))
	stopOnce.Do(func() { close(stop) })
	s.blockfall.stop()
	if err != nil {
		s.logger.Info("game: round did not run", "err", err)
	}
	if scores, serr := oled.SaveRound(id, res, time.Now()); serr != nil {
		s.logger.Warn("game: could not save the score", "err", serr)
	} else {
		s.logger.Info("game: round over", "score", res.Score, "rows", res.Lines, "reason", res.Reason, "best", scores.Best)
	}
	if takenOver.Load() || res.Reason == "power" {
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	s.restoreAfterAnnounce(ctx, prev, prevVol, wasStandby, prevVol >= 0)
}
