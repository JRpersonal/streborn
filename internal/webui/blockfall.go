package webui

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/oled"
)

// Blockfall, the hidden game on the Portable's display (internal/oled). The
// remote code starts a round; this side owns everything around it: the music
// the speaker plays over UPnP loopback, noticing when the user takes the
// speaker back, saving the score, and putting the speaker back the way it was
// (same restore as an announcement: previous stream and volume, or standby).

// blockfallMusicURL is where the speaker pulls the game music from: this
// agent, over loopback, like the announcement audio.
const blockfallMusicURL = "http://127.0.0.1:8888/game/blockfall.wav"

// blockfallMaxVolume caps the volume during a round; the user's own volume is
// restored afterwards. Waking from standby restores the last volume, which can
// be far louder than a game should start.
const blockfallMaxVolume = 30

type blockfallMusic struct {
	mu   sync.Mutex
	done chan struct{} // closed when the round's music must stop; nil = none
}

func (m *blockfallMusic) start() {
	m.mu.Lock()
	m.done = make(chan struct{})
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

func (m *blockfallMusic) current() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.done
}

// handleBlockfallMusic streams the round's music to the speaker. Loopback
// only, and only while a round runs.
func (s *Server) handleBlockfallMusic(w http.ResponseWriter, r *http.Request) {
	if host, _, _ := net.SplitHostPort(r.RemoteAddr); !net.ParseIP(host).IsLoopback() {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	done := s.blockfall.current()
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
	_ = oled.StreamMusic(w, flush, merged)
}

// handleBlockfallScores answers GET with the stored scores, for the app.
func (s *Server) handleBlockfallScores(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, oled.LoadScores())
}

// handleBlockfallScreenshot serves the last round's final screen as PNG.
func (s *Server) handleBlockfallScreenshot(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	http.ServeFile(w, r, oled.ScreenshotPath)
}

// startBlockfall runs one round end to end. Started by the remote code
// (oled.FeedKey); a second code while a round runs is ignored there.
func (s *Server) startBlockfall() {
	if s.renderer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	prev := s.snapshotNowPlaying(ctx)
	wasStandby := prev.Source == "STANDBY" || prev.Source == ""
	prevVol := s.readVolume(ctx)
	s.logger.Info("blockfall: code entered, starting a round", "wasStandby", wasStandby, "source", prev.Source)

	stop := make(chan struct{})
	var stopOnce sync.Once
	var takenOver, musicPlaying atomic.Bool
	// Wake and start the music right away, in parallel with the intro, so the
	// speaker is up when the game begins.
	go func() {
		if wasStandby {
			if err := boxcli.WakeAndWait(ctx, s.boxHost, 8*time.Second, s.logger); err != nil {
				s.logger.Warn("blockfall: wake failed, playing anyway", "err", err)
			}
		}
		s.blockfall.start()
		// Straight out of standby the speaker can take the play command and
		// still stay on INVALID_SOURCE (seen twice on the Portable); a second
		// command fixed it each time. So verify, and send it again.
		for attempt := 1; attempt <= 3 && !musicPlaying.Load(); attempt++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := s.renderer.PlayURLMime(ctx, blockfallMusicURL, "Blockfall", "", "audio/wav"); err != nil {
				s.logger.Warn("blockfall: music play command failed", "attempt", attempt, "err", err)
			}
			for i := 0; i < 8 && !musicPlaying.Load(); i++ {
				time.Sleep(time.Second)
				np := fetchNowPlaying(ctx, s.boxHost)
				if np.Location == blockfallMusicURL && (np.PlayStatus == "PLAY_STATE" || np.PlayStatus == "BUFFERING_STATE") {
					musicPlaying.Store(true)
					s.logger.Info("blockfall: music playing", "attempt", attempt)
				}
			}
		}
		if !musicPlaying.Load() {
			s.logger.Warn("blockfall: music did not start, the round goes on without it")
			return
		}
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
	// ends and nothing is restored over what they chose. Only once the music
	// was confirmed playing: a start that never took leaves the speaker on
	// INVALID_SOURCE, which is no user action and must not end the round.
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(3 * time.Second):
			}
			if !musicPlaying.Load() {
				continue
			}
			np := fetchNowPlaying(ctx, s.boxHost)
			if np.Source != "" && np.Source != "INVALID_SOURCE" && np.Location != blockfallMusicURL {
				s.logger.Info("blockfall: speaker taken over, ending the round", "source", np.Source)
				takenOver.Store(true)
				stopOnce.Do(func() { close(stop) })
				return
			}
		}
	}()

	res, err := oled.PlayRound(stop, nil, s.logger.With("comp", "blockfall"))
	stopOnce.Do(func() { close(stop) })
	s.blockfall.stop()
	if err != nil {
		s.logger.Info("blockfall: round did not run", "err", err)
	}
	if scores, serr := oled.SaveRound(res, time.Now()); serr != nil {
		s.logger.Warn("blockfall: could not save the score", "err", serr)
	} else {
		s.logger.Info("blockfall: round over", "score", res.Score, "rows", res.Lines, "reason", res.Reason, "best", scores.Best)
	}
	if takenOver.Load() || res.Reason == "power" {
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	s.restoreAfterAnnounce(ctx, prev, prevVol, wasStandby, prevVol >= 0)
}
