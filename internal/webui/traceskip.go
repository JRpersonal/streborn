package webui

// Next and Previous on the remote, as the speaker's key trace reports them.
//
// On Spotify and the play queue STR skips on the speaker's behalf when the
// firmware reports its own failed skip on the gabbo bus (QPLAY_SKIP_*_FAILED,
// HardwareSkip). That frame comes only while the speaker plays STR's UPnP
// stream, and the speaker tears that stream down on the very press that sends
// it. A second press inside the next second or two lands in the teardown, finds
// no UPnP source, and the firmware sends nothing for it. So a quick double
// press of Back, the usual way to go to the previous song when the first press
// only restarts the current one, did exactly what a single press does: the
// second press was lost (Jens' ST30, 2026-11-21: "die Zurück-Taste startet das
// aktuelle Lied von vorne, bei Doppeldruck sollte das vorherige laufen").
//
// The key trace (internal/boxlog) names every press, the second one included.
// It is the second signal here, paired with the frame the same way the radio
// step pairs them (radiostep.go): a traced press that has a frame of its own
// within traceSkipPairWindow, before or after it, is that frame's press and
// does nothing; a traced press without one skips by itself. The frame keeps
// the lead: it acts at once and with the re-attach the teardown needs, while a
// traced press waits for the frame before deciding, so one press never skips
// twice. Two real presses each bring their own pair, so the count stays right
// whatever order the reports arrive in.
//
// A speaker that follows a group answers none of this. Its audio comes from
// the group's main speaker, which handles the remote itself; the follower's
// own idea of what it plays is whatever it played before it joined. Live on
// the same day: the kitchen ST10 following the ST30 read the ST30's Back as a
// radio step from the station it had played before the group formed, and its
// recall put the whole group on that station in the middle of a Spotify
// playlist.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/presets"
)

// traceSkipPairWindow is how far apart the frame and the trace line of ONE
// press may arrive. The trace was measured to trail the frame by up to about a
// second on the Portable (cmd/agent/keytrace.go keyTraceWait); it also leads
// it by a few tens of milliseconds at times.
const traceSkipPairWindow = 1200 * time.Millisecond

// followerReadBudget bounds the zone read that tells a follower apart. It
// runs on every remote skip of the main speaker, so it is kept short; an
// unreadable zone counts as standing alone, as before.
const followerReadBudget = 1500 * time.Millisecond

// traceSkipState pairs the two reports of a press. frames are gabbo frames no
// traced press has claimed yet; pending are traced presses waiting for theirs.
type traceSkipState struct {
	mu      sync.Mutex
	frames  []time.Time
	pending []*traceSkipPress
}

type traceSkipPress struct {
	at      time.Time
	claimed bool
}

// noteFrame records a gabbo skip frame at t. A traced press waiting for its
// frame is claimed by it; otherwise the frame waits for its trace line.
func (st *traceSkipState) noteFrame(t time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked(t)
	for i, p := range st.pending {
		if absDur(t.Sub(p.at)) <= traceSkipPairWindow {
			p.claimed = true
			st.pending = append(st.pending[:i], st.pending[i+1:]...)
			return
		}
	}
	st.frames = append(st.frames, t)
}

// tracePress records a traced press at t and reports whether it is its own
// press: true when no frame belongs to it, after waiting up to wait for one.
func (st *traceSkipState) tracePress(t time.Time, wait time.Duration) bool {
	st.mu.Lock()
	st.pruneLocked(t)
	if len(st.frames) > 0 {
		st.frames = st.frames[1:]
		st.mu.Unlock()
		return false
	}
	p := &traceSkipPress{at: t}
	st.pending = append(st.pending, p)
	st.mu.Unlock()

	time.Sleep(wait)

	st.mu.Lock()
	defer st.mu.Unlock()
	if p.claimed {
		return false
	}
	for i, q := range st.pending {
		if q == p {
			st.pending = append(st.pending[:i], st.pending[i+1:]...)
			break
		}
	}
	return true
}

func (st *traceSkipState) pruneLocked(now time.Time) {
	kept := st.frames[:0]
	for _, f := range st.frames {
		if now.Sub(f) <= traceSkipPairWindow {
			kept = append(kept, f)
		}
	}
	st.frames = kept
}

// followsGroupForKeys reports whether this speaker follows a group, so the
// remote's Next and Previous belong to the group's main speaker. Only a
// follower the zone names as such: a speaker that cannot tell its role, or
// cannot read its zone, keeps its keys.
func (s *Server) followsGroupForKeys(ctx context.Context) bool {
	if s.groupFollowerFn != nil {
		f, _ := s.groupFollowerFn()
		return f
	}
	if s.boxHost == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, followerReadBudget)
	defer cancel()
	c := boxapi.New(s.boxHost)
	z, err := c.GetZone(ctx)
	if err != nil {
		return false
	}
	follower, known := zoneRoleFromMaster(z.Master, s.localDeviceID(ctx, c, ""))
	return follower && known
}

// RemoteTrackKey answers a Next/Prev press the speaker's key trace reported
// (cmd/agent keytrace.go). While a radio key plays it steps through the radio
// keys (#1619); on Spotify and the play queue it skips when the gabbo frame
// missed the press. A group follower leaves the press to its main speaker.
// Call it off the key reader's goroutine: it waits for the frame and may
// recall a station.
func (s *Server) RemoteTrackKey(forward bool) {
	at := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if s.followsGroupForKeys(ctx) {
		s.logger.Info("remote key: this speaker follows a group, its main speaker answers Next and Previous", "forward", forward)
		return
	}
	if _, handled := s.radioStep.step(ctx, s, forward, radioStepViaKeyTrace, at); handled {
		return
	}
	s.traceSkipPress(ctx, forward, at)
}

// traceSkipPress skips Spotify or the play queue for a traced press the gabbo
// frame did not report. It does nothing unless STR's own stream is what the
// speaker plays, or is tearing down: a press on Bluetooth, AUX or a source the
// speaker plays itself is the firmware's to answer.
func (s *Server) traceSkipPress(ctx context.Context, forward bool, at time.Time) {
	if !s.traceSkipApplies(ctx) {
		return
	}
	wait := s.traceSkipWait
	if wait == 0 {
		wait = traceSkipPairWindow
	}
	if !s.traceSkip.tracePress(at, wait) {
		return
	}
	// Read again: the wait was long enough for the speaker to move on.
	if !s.traceSkipApplies(ctx) {
		return
	}
	// A Spotify key skips the engine directly, as HardwareSkip does: in the
	// teardown the stream is detached, and transportSkip would read that as
	// no Spotify and skip the play queue instead. The first press's re-attach
	// (or the soft skip's own re-push) brings the speaker back.
	var src string
	var err error
	if presets.ValidSlot(s.currentSpotifySlot()) && s.spotifySkip != nil {
		s.enqueueSpotifySkip(spotifySkipReq{forward: forward})
		src = "spotify"
	} else {
		src, err = s.transportSkip(ctx, forward)
	}
	if err != nil {
		s.logger.Warn("remote key: the press the speaker sent no skip frame for could not skip", "forward", forward, "source", src, "err", err)
		return
	}
	s.logger.Info("remote key: skipped for a press the speaker sent no skip frame for (a quick second press)", "forward", forward, "source", src)
}

// traceSkipApplies reports whether a traced Next/Prev is STR's to skip: a
// Spotify key's stream or the play queue is STR's last play, and the speaker
// is on that stream or in the teardown of its own failed skip.
func (s *Server) traceSkipApplies(ctx context.Context) bool {
	queue := s.queue != nil && s.queue.isActive()
	if !queue && !presets.ValidSlot(s.currentSpotifySlot()) && !s.spotifyIsStreaming() {
		return false
	}
	rctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	np, err := s.readBoxNow(rctx)
	if err != nil {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(np.Source)) {
	case "INVALID_SOURCE":
		return true
	case "UPNP":
		loc := np.Location()
		return loc == "" || boxapi.StreamPath(loc) == boxapi.StreamPath(s.play.currentURL())
	}
	return false
}
