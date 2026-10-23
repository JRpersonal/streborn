package webui

// Sleep timer: switch this speaker, or the whole group, off after a while.
//
// Two rules shape the whole design.
//
// The FIRST is that deep standby is sacred. A sleep timer sounds like the
// opposite of a keep-awake feature, and it is, but it is trivially easy to
// build one that keeps a speaker up: poll the box every minute to show a
// countdown, or fire a power-off into a box that fell asleep on its own and
// wake it doing so. So the timer never touches the speaker while it runs (it is
// a Go timer, nothing more), and when it fires it READS the source first and
// stands down if the speaker is already in standby. Nothing here may ever be
// the reason a speaker is awake.
//
// The SECOND is that it does not survive an agent restart, deliberately. Making
// it persistent would mean a NAND write every time somebody sets it, and the
// failure it protects against is "the speaker does not fall asleep", which is
// the harmless direction. A speaker that reboots at 3am and then stays on until
// morning is a far smaller problem than one extra flash write per bedtime.
//
// A remote key can step the timer too (sleepkey.go): 15, 30, 60 minutes, off.
// While a timer runs, the deadline is handed to STR's own screen on the
// speaker's panel once (internal/oled SetSleepDeadline), which counts down
// without asking the speaker anything; a group timer hands it to every
// member's agent the same way (POST /api/display/sleep).

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/oled"
)

// sleepMaxMinutes bounds the timer. Longer than any bedtime and short enough
// that a mistyped value cannot arm something a week out.
const sleepMaxMinutes = 12 * 60

// Seams for the tests. The two box calls below reach the firmware on a fixed
// port (:8090), so a test server on a random port can never be reached through
// them: without these, every test would silently take the "cannot read it,
// stand down" path and prove nothing about the decision it claims to test. A
// test installs sleepReadSourceFn / sleepStandbyFn on its own Server;
// production (nil) always uses the real implementations.

// sleepReadSource reads the source a speaker reports right now.
func (s *Server) sleepReadSource(ctx context.Context, host string) string {
	if s.sleepReadSourceFn != nil {
		return s.sleepReadSourceFn(ctx, host)
	}
	return fetchNowPlaying(ctx, host).Source
}

// sleepStandby switches a speaker off.
func (s *Server) sleepStandby(ctx context.Context, host string) error {
	if s.sleepStandbyFn != nil {
		return s.sleepStandbyFn(ctx, host)
	}
	return boxapi.New(host).Standby(ctx)
}

// sleepState is the armed timer. Zero value means "not armed".
type sleepState struct {
	mu       sync.Mutex
	timer    *time.Timer
	deadline time.Time
	group    bool
	// armed is the duration the running timer was armed with (from the
	// phone, an alarm or the key): the key steps on from it (sleepkey.go).
	armed time.Duration
	// lastKey is when the key last stepped the timer, for the debounce.
	lastKey time.Time
	// peers: a group countdown went out to the members and was not cleared
	// since, so a cancel has to tell them.
	peers bool
	// pushMu serialises the rounds that hand the countdown to the members,
	// so a quick 15 -> 30 -> 60 cannot arrive at a member out of order.
	pushMu sync.Mutex
	// gen invalidates a timer that fired after being cancelled or replaced: the
	// callback captures the generation it was armed with and does nothing when
	// the state has moved on. Cheaper and less racy than trying to prove
	// time.Timer.Stop won the race.
	gen uint64
}

// handleSleep serves the sleep timer.
//
//	GET                                  -> {"active":bool,"remainingSec":N,"group":bool}
//	POST {"minutes":N,"group":bool}      -> arm (minutes<=0 cancels)
//	DELETE                               -> cancel
func (s *Server) handleSleep(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.sleepStatus())
	case http.MethodDelete:
		s.cancelSleep("user")
		writeJSON(w, http.StatusOK, s.sleepStatus())
	case http.MethodPost:
		var req struct {
			Minutes int  `json:"minutes"`
			Group   bool `json:"group"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Minutes <= 0 {
			// Log the ASK, not just the effect. Cancelling when nothing runs is
			// a no-op, and cancelSleep stays quiet for it, so a phone that kept
			// sending minutes=0 by mistake left no trace at all: a 2026-08-08
			// bundle from an owner reporting "it does nothing" contained not one
			// sleep line to explain why (#487). What was requested is now always
			// on the record.
			s.logger.Info("sleep timer: cancel requested", "group", req.Group)
			s.cancelSleep("user")
			writeJSON(w, http.StatusOK, s.sleepStatus())
			return
		}
		if req.Minutes > sleepMaxMinutes {
			http.Error(w, "minutes must be 1..720", http.StatusBadRequest)
			return
		}
		s.armSleep(time.Duration(req.Minutes)*time.Minute, req.Group)
		writeJSON(w, http.StatusOK, s.sleepStatus())
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) sleepStatus() map[string]any {
	s.sleep.mu.Lock()
	defer s.sleep.mu.Unlock()
	if s.sleep.timer == nil {
		return map[string]any{"active": false}
	}
	remaining := int(time.Until(s.sleep.deadline).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	return map[string]any{"active": true, "remainingSec": remaining, "group": s.sleep.group, "minutes": int(s.sleep.armed / time.Minute)}
}

// armSleep replaces any running timer. Setting a new one while one is armed is
// the common case (the user changes their mind), not an error.
func (s *Server) armSleep(d time.Duration, group bool) { s.armSleepNotify(d, group, false) }

// armSleepNotify is armSleep; notice asks the group members to show the step
// as a notice too (a key press), not only the countdown.
func (s *Server) armSleepNotify(d time.Duration, group, notice bool) {
	s.sleep.mu.Lock()
	if s.sleep.timer != nil {
		s.sleep.timer.Stop()
	}
	s.sleep.gen++
	gen := s.sleep.gen
	deadline := time.Now().Add(d)
	s.sleep.deadline = deadline
	s.sleep.group = group
	s.sleep.armed = d
	s.sleep.timer = time.AfterFunc(d, func() { s.fireSleep(gen) })
	push := group || s.sleep.peers
	if group {
		s.sleep.peers = true
	}
	s.sleep.mu.Unlock()
	s.logger.Info("sleep timer armed", "minutes", int(d.Minutes()), "group", group)
	oled.SetSleepDeadline(deadline)
	if push {
		s.goSleepPush(func() { s.pushSleepToGroup(notice) })
	}
}

func (s *Server) cancelSleep(why string) { s.cancelSleepNotify(why, false) }

// sleepGeneration is the running timer's generation, for a caller that has to
// cancel exactly the timer it armed later (an alarm's switch-off on a snooze).
func (s *Server) sleepGeneration() uint64 {
	s.sleep.mu.Lock()
	defer s.sleep.mu.Unlock()
	return s.sleep.gen
}

// cancelSleepIfGen cancels the timer only while it is still the one armed at
// generation gen: a timer the user set since is theirs and stays.
func (s *Server) cancelSleepIfGen(gen uint64, why string) bool {
	s.sleep.mu.Lock()
	match := s.sleep.timer != nil && s.sleep.gen == gen
	s.sleep.mu.Unlock()
	if !match {
		return false
	}
	s.cancelSleep(why)
	return true
}

// cancelSleepNotify is cancelSleep; notice as for armSleepNotify.
func (s *Server) cancelSleepNotify(why string, notice bool) {
	s.sleep.mu.Lock()
	had := s.sleep.timer != nil
	if had {
		s.sleep.timer.Stop()
		s.sleep.timer = nil
	}
	s.sleep.gen++
	s.sleep.deadline = time.Time{}
	s.sleep.armed = 0
	push := s.sleep.peers
	s.sleep.peers = false
	s.sleep.mu.Unlock()
	if had {
		s.logger.Info("sleep timer cancelled", "why", why)
	}
	oled.SetSleepDeadline(time.Time{})
	if push {
		s.goSleepPush(func() { s.pushSleepToGroup(notice) })
	}
}

// fireSleep switches the speaker off, and the rest of the group with it.
func (s *Server) fireSleep(gen uint64) {
	s.sleep.mu.Lock()
	stale := gen != s.sleep.gen
	group := s.sleep.group
	if !stale {
		s.sleep.timer = nil
		s.sleep.deadline = time.Time{}
		s.sleep.armed = 0
		// the members' own countdowns run out at the same moment
		s.sleep.peers = false
	}
	s.sleep.mu.Unlock()
	if stale {
		return // cancelled or replaced while the timer was running out
	}
	oled.SetSleepDeadline(time.Time{})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Read first, act second. A speaker that already went to standby on its own
	// must be left exactly where it is: on some chassis the request itself is
	// what wakes it, so "switch off something already off" would be the one way
	// this feature could keep a speaker awake. See the file header.
	if src := s.sleepReadSource(ctx, s.boxHost); src == "STANDBY" || src == "" {
		s.logger.Info("sleep timer: the speaker is already in standby, standing down", "source", src)
		return
	}

	// Mark the power-off as deliberate BEFORE it happens, or the UPNP->STANDBY
	// flip it causes is classified as a spontaneous firmware drop and the
	// recovery machinery switches the speaker back on (#419).
	s.NoteUserStop()
	if err := s.sleepStandby(ctx, s.boxHost); err != nil {
		s.logger.Warn("sleep timer: could not switch this speaker off", "err", err)
	} else {
		s.logger.Info("sleep timer fired, speaker off")
	}
	if !group {
		return
	}
	// The same resolver the volume control uses. Reading membership from the
	// stored document here meant the "switch the whole group off" box was
	// OFFERED from the live view and ACTED ON from the document, so on a
	// follower it switched off one speaker.
	sctx, scancel := context.WithTimeout(context.Background(), 3*time.Second)
	members, grouped, _ := s.groupView(sctx)
	scancel()
	if !grouped {
		return
	}
	for _, m := range members {
		if m.IsSelf || m.IP == "" {
			continue
		}
		if err := s.sleepStandby(ctx, m.IP); err != nil {
			s.logger.Warn("sleep timer: a group member stayed on", "member", m.Name, "ip", m.IP, "err", err)
			continue
		}
		s.logger.Info("sleep timer: group member off", "member", m.Name)
	}
}
