package webui

// The speaker's own Bose software has stopped answering.
//
// Field case (maintainer's SoundTouch 30, scm chassis, 2026-11-21): from one
// moment on every request to the firmware webserver (BoseApp, :8090) timed out
// for more than ten minutes. UPnP (:8091) and the agent answered fine, so the
// speaker looked alive from the network, but the standby clock stopped, skips
// were slow, autopair and the us-services source list failed on every try,
// and nothing told the owner. A power cycle fixed it; BoseApp had been up for
// 267 hours.
//
// The outage tracker existed already, but only the app's status poll fed it,
// and nobody was polling. It is now fed from boxapi itself
// (boxapi.ObserveReachability, wired in cmd/agent), so every :8090 read the
// agent makes anyway counts: the autopair /info heartbeat (at least every five
// minutes, on its own transport), the preset reconciler, the standby clock,
// the phone remote, the source list. No timer or probe of its own: a speaker
// nobody reads from is not judged, and that is fine, because nobody is looking
// at it either.
//
// The verdict is deliberately slow and conservative, because the advice it
// leads to is "pull the plug":
//   - only transport failures count (boxapi passes a firmware error reply as
//     an answer: the webserver is alive);
//   - the outage has lasted boseappUnresponsiveAfter with at least
//     boseappUnresponsiveMinFails failed reads and not a single answer in
//     between (one answer clears it);
//   - the agent has been up for boseappBootSettle, so a speaker still booting
//     its firmware is not flagged;
//   - the last failed read is recent (boseappVerdictStale), so a verdict is
//     never kept alive by reads that stopped long ago;
//   - not while a Bose firmware image is being flashed
//     (boseappFirmwareFlashQuiet): the firmware is down on purpose then, and
//     unplugging it mid-flash is the one thing that must never be suggested.
//
// Standby never trips it: :8090 answers in standby.
//
// Surfaced as boxHealth "firmware-unresponsive" on /api/agent/version, which
// the desktop app and the phone remote turn into the power-cycle hint.

import (
	"sync"
	"time"
)

const (
	// boseappUnresponsiveAfter is how long :8090 must have failed every read
	// before the speaker is reported. A BoseApp restart or a slow moment is
	// over well within it.
	boseappUnresponsiveAfter = 3 * time.Minute
	// boseappUnresponsiveMinFails is how many failed reads the outage must
	// contain: a duration spanned by one or two reads is not evidence.
	boseappUnresponsiveMinFails = 3
	// boseappBootSettle is how long after the agent started a failure may
	// count toward the verdict at all. The agent comes up with the speaker,
	// and the firmware's own webserver can take minutes after that.
	boseappBootSettle = 5 * time.Minute
	// boseappVerdictStale bounds how old the latest failed read may be. Longer
	// than the autopair heartbeat's steady five minutes plus its read deadline,
	// so the steady cadence alone keeps a real outage reported.
	boseappVerdictStale = 11 * time.Minute
	// boseappFirmwareFlashQuiet is how long after a Bose firmware hand-over no
	// verdict is reported: the upload, the flash and the restart all fit.
	boseappFirmwareFlashQuiet = 45 * time.Minute
)

// boseappOutageKeep bounds the episode ring; the freeze family produces a
// handful of long episodes, not thousands, so a short ring is plenty.
const boseappOutageKeep = 10

// agentStartedAt is when this agent process started, for boseappBootSettle.
var agentStartedAt = time.Now()

// boseappOutage is one closed episode of the firmware webserver (:8090) not
// answering.
type boseappOutage struct {
	Start   string `json:"start"`
	End     string `json:"end"`
	Seconds int    `json:"seconds"`
	// Fails is how many reads failed during the episode.
	Fails int `json:"fails"`
	// Flagged says whether the episode was reported as firmware-unresponsive.
	Flagged bool `json:"flagged,omitempty"`
}

// boseappTracker is the :8090 outage record. Its own mutex, not statusMu:
// it is fed from inside boxapi on whatever goroutine made the request, and
// must never wait on a lock somebody holds across a call to the speaker.
type boseappTracker struct {
	mu        sync.Mutex
	downSince time.Time // first failed read of the current outage; zero while answering
	lastFail  time.Time // most recent failed read of the current outage
	fails     int       // failed reads in the current outage
	flagged   bool      // the current outage was reported (logged, boxHealth)
	outages   []boseappOutage
	// blips counts outages of a single failed read that the next read cleared.
	// Not kept as episodes: with every read feeding the tracker they would push
	// the long episodes a bundle is read for out of the ring.
	blips int
	// firmwareHandoverAt is when a Bose firmware image was last handed to the
	// speaker's update page.
	firmwareHandoverAt time.Time
}

// unresponsiveAt is the verdict, with the tracker's lock held.
func (t *boseappTracker) unresponsiveAt(now, started time.Time) bool {
	if t.downSince.IsZero() || t.fails < boseappUnresponsiveMinFails {
		return false
	}
	if now.Sub(started) < boseappBootSettle {
		return false
	}
	if now.Sub(t.downSince) < boseappUnresponsiveAfter {
		return false
	}
	if now.Sub(t.lastFail) >= boseappVerdictStale {
		return false
	}
	if !t.firmwareHandoverAt.IsZero() && now.Sub(t.firmwareHandoverAt) < boseappFirmwareFlashQuiet {
		return false
	}
	return true
}

// NoteBoseAppReach feeds one :8090 round trip's verdict into the outage
// tracker: answered is true for any HTTP answer, false for a transport failure
// or timeout. Registered with boxapi.ObserveReachability by the agent.
func (s *Server) NoteBoseAppReach(answered bool) {
	s.noteBoseAppFetchAt(answered, time.Now())
}

func (s *Server) noteBoseAppFetchAt(ok bool, now time.Time) {
	t := &s.boseapp
	t.mu.Lock()
	if ok {
		if t.downSince.IsZero() {
			t.mu.Unlock()
			return
		}
		ep := boseappOutage{
			Start:   t.downSince.Format(time.RFC3339),
			End:     now.Format(time.RFC3339),
			Seconds: int(now.Sub(t.downSince).Seconds()),
			Fails:   t.fails,
			Flagged: t.flagged,
		}
		wasFlagged := t.flagged
		if t.fails >= 2 || t.flagged {
			t.outages = append(t.outages, ep)
			if len(t.outages) > boseappOutageKeep {
				t.outages = t.outages[len(t.outages)-boseappOutageKeep:]
			}
		} else {
			t.blips++
		}
		t.downSince, t.lastFail, t.fails, t.flagged = time.Time{}, time.Time{}, 0, false
		t.mu.Unlock()
		if wasFlagged && s.logger != nil {
			s.logger.Info("boseapp: the speaker's firmware webserver (:8090) answers again; the power-cycle hint is cleared",
				"downSec", ep.Seconds, "failedReads", ep.Fails)
		}
		return
	}
	if t.downSince.IsZero() {
		t.downSince = now
	}
	t.lastFail = now
	t.fails++
	report := !t.flagged && t.unresponsiveAt(now, agentStartedAt)
	if report {
		t.flagged = true
	}
	since, fails := t.downSince, t.fails
	t.mu.Unlock()
	if report && s.logger != nil {
		s.logger.Warn("boseapp: the speaker's firmware webserver (:8090) has stopped answering while the agent runs; telling the user to power-cycle the speaker",
			"downSec", int(now.Sub(since).Seconds()), "failedReads", fails)
	}
}

// noteBoseFirmwareHandover records that a Bose firmware image is being handed
// to the speaker's update page, which keeps the verdict quiet while it flashes.
func (s *Server) noteBoseFirmwareHandover() {
	s.boseapp.mu.Lock()
	s.boseapp.firmwareHandoverAt = time.Now()
	s.boseapp.mu.Unlock()
}

// boseappUnresponsive reports whether the firmware webserver is flagged as
// unresponsive right now, and since when it has not answered.
func (s *Server) boseappUnresponsive(now time.Time) (bool, time.Time) {
	t := &s.boseapp
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.flagged && t.unresponsiveAt(now, agentStartedAt) {
		return true, t.downSince
	}
	return false, time.Time{}
}

// BoseAppHealth is the /api/debug/state section with the :8090 outage record:
// whether the firmware webserver is answering right now, for how long it has
// been dead if not, whether that was reported to the user, and the recent
// closed outage episodes. Observation rides on the agent's own reads (see the
// top of this file), so a stretch with no read shows no data rather than
// false uptime.
func (s *Server) BoseAppHealth() any {
	now := time.Now()
	t := &s.boseapp
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]any{
		"currentlyDown": !t.downSince.IsZero(),
		"episodes":      append([]boseappOutage(nil), t.outages...),
		"blips":         t.blips,
		"unresponsive":  t.flagged && t.unresponsiveAt(now, agentStartedAt),
	}
	if !t.downSince.IsZero() {
		out["downSince"] = t.downSince.Format(time.RFC3339)
		out["downForSec"] = int(now.Sub(t.downSince).Seconds())
		out["failedReads"] = t.fails
		out["lastFailSec"] = int(now.Sub(t.lastFail).Seconds())
	}
	if !t.firmwareHandoverAt.IsZero() {
		out["firmwareHandoverSec"] = int(now.Sub(t.firmwareHandoverAt).Seconds())
	}
	return out
}
