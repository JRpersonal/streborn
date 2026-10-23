package webui

// The sleep timer on a remote key, and the countdown on the group members'
// displays.
//
// A thumbs key bound to the sleep timer (internal/keyactions, saved from the
// desktop app at /api/keyactions) steps it on every press:
//
//	not running -> 15 min -> 30 min -> 60 min -> off -> 15 min ...
//
// The step is taken from the duration the running timer was ARMED with, not
// from the time left: the next press goes to the smallest of 15, 30 and 60
// that is longer than it, and past 60 to off. So a timer the phone set to 45
// minutes goes to 60, one set to 90 (or an alarm's 2-hour auto-off) goes off,
// and tapping through never repeats a step just because a minute has passed
// (with the time left, a 15-minute timer a few seconds old would step to 15
// again and the press would look like it did nothing).
//
// The key always arms a GROUP timer. On a speaker that is not grouped that is
// the same as a single one (fireSleep finds no members); in a group the whole
// group goes off, which is what a key on the remote of one of them means.
//
// Every group member with an STR agent gets the countdown, and the notice for
// a key press, over POST /api/display/sleep on its agent. It only DRAWS there:
// the arming speaker stays the one that switches the members off, as before.
// A member that misses the message, runs no STR or has no panel loses nothing
// but the picture; it is still switched off at the end.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/keyactions"
	"github.com/JRpersonal/streborn/internal/oled"
)

// sleepSteps are what the key cycles through before off.
var sleepSteps = []time.Duration{15 * time.Minute, 30 * time.Minute, 60 * time.Minute}

// nextSleepStep is the duration the next key press arms, 0 for off: the
// smallest step longer than what the running timer was armed with, 15 when
// none runs. See the file header for why it is not the time left.
func nextSleepStep(active bool, armed time.Duration) time.Duration {
	if !active {
		return sleepSteps[0]
	}
	for _, st := range sleepSteps {
		if st > armed {
			return st
		}
	}
	return 0
}

// sleepKeyMinGap drops a second press event inside this window. The key
// trace already folds the trace line and the analytics line of one press
// into one event (internal/boxlog dedupWindow); this only guards against a
// stray repeat of the same event. Deliberately short: tapping through the
// steps about once a second must step every time.
const sleepKeyMinGap = 250 * time.Millisecond

// SleepKeyPress is the press handler of a key bound to the sleep timer. It
// steps the timer and shows the step on the display. Wired into the key
// handler in cmd/agent; returns at once (the group push runs on its own).
func (s *Server) SleepKeyPress() {
	now := time.Now()
	s.sleep.mu.Lock()
	if !s.sleep.lastKey.IsZero() && now.Sub(s.sleep.lastKey) < sleepKeyMinGap {
		s.sleep.mu.Unlock()
		s.logger.Debug("sleep key: press ignored, same press reported twice")
		return
	}
	s.sleep.lastKey = now
	next := nextSleepStep(s.sleep.timer != nil, s.sleep.armed)
	s.sleep.mu.Unlock()

	if next == 0 {
		s.logger.Info("sleep key pressed", "step", "off")
		s.cancelSleepNotify("key", true)
		showSleepNotice(0)
		return
	}
	s.logger.Info("sleep key pressed", "step", int(next/time.Minute))
	s.armSleepNotify(next, true, true)
	showSleepNotice(int(next / time.Minute))
}

// showSleepNotice puts the step on this speaker's panel (0 = off). It shows
// even with the notices switched off, like identify: it is the only answer
// to a key the user just pressed.
func showSleepNotice(minutes int) {
	if !oled.Supported() {
		return
	}
	n := oled.SleepOff()
	if minutes > 0 {
		n = oled.SleepSet(minutes)
	}
	n.Bypass = true
	oled.Show(n)
}

// sleepPeerMsg is what a group member's agent gets at /api/display/sleep.
type sleepPeerMsg struct {
	// RemainingSec is the time left, 0 when the timer was switched off.
	// Seconds, not a deadline: the speakers' clocks need not agree.
	RemainingSec int `json:"remainingSec"`
	// Notice asks the member to show the step as a notice too.
	Notice bool `json:"notice,omitempty"`
}

var sleepPeerClient = &http.Client{Timeout: 3 * time.Second}

// goSleepPush runs a push round off the caller's goroutine. A seam
// (goSleepPushFn), so the tests can run it in line and see what was sent.
func (s *Server) goSleepPush(f func()) {
	if s.goSleepPushFn != nil {
		s.goSleepPushFn(f)
		return
	}
	go f()
}

// sleepPushPeer sends msg to the agent at ip, on either agent port (8888 on
// sm2, 17008 through the REDIRECT on a BCO chassis). A seam for the tests
// (sleepPushPeerFn).
func (s *Server) sleepPushPeer(ctx context.Context, ip string, msg sleepPeerMsg) error {
	if s.sleepPushPeerFn != nil {
		return s.sleepPushPeerFn(ctx, ip, msg)
	}
	body, _ := json.Marshal(msg)
	var lastErr error
	for _, port := range []string{"17008", "8888"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"http://"+net.JoinHostPort(ip, port)+"/api/display/sleep", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := sleepPeerClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("port %s: status %d", port, resp.StatusCode)
	}
	return lastErr
}

// pushSleepToGroup hands the current countdown (or its end) to every live
// group member's agent. Rounds run one at a time and each sends the state as
// it is when the round starts, so the last round always carries the newest
// step, whatever order the presses' rounds were scheduled in.
func (s *Server) pushSleepToGroup(notice bool) {
	s.sleep.pushMu.Lock()
	defer s.sleep.pushMu.Unlock()

	gctx, gcancel := context.WithTimeout(context.Background(), 3*time.Second)
	members, grouped, _ := s.groupView(gctx)
	gcancel()
	if !grouped {
		return
	}
	s.sleep.mu.Lock()
	msg := sleepPeerMsg{Notice: notice}
	if s.sleep.timer != nil {
		msg.RemainingSec = max(1, int(time.Until(s.sleep.deadline).Seconds()))
	}
	s.sleep.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var (
		wg           sync.WaitGroup
		mu           sync.Mutex
		sent, failed int
	)
	for _, m := range members {
		if m.IsSelf || m.IP == "" {
			continue
		}
		wg.Add(1)
		go func(name, ip string) {
			defer wg.Done()
			err := s.sleepPushPeer(ctx, ip, msg)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				// Debug: a member without STR (or switched off) fails here
				// every time and loses nothing but the picture.
				s.logger.Debug("sleep timer: a group member did not take the countdown", "member", name, "ip", ip, "err", err)
				return
			}
			sent++
		}(m.Name, m.IP)
	}
	wg.Wait()
	s.logger.Info("sleep timer: countdown handed to the group", "remainingSec", msg.RemainingSec, "members", sent, "unreached", failed)
}

// handleDisplaySleep is the member's side: draw a group's sleep countdown
// that another speaker armed.
//
//	POST {"remainingSec":N,"notice":bool}   N>0 shows the countdown, 0 clears it
//
// LAN only. It draws and nothing else: no timer is armed here, the speaker
// that armed it switches this one off. A speaker without a panel answers
// {"shown":false}, still 200.
func (s *Server) handleDisplaySleep(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var msg sleepPeerMsg
	if !decodeJSONRequest(w, r, 1024, &msg) {
		return
	}
	if msg.RemainingSec < 0 || msg.RemainingSec > sleepMaxMinutes*60 {
		http.Error(w, "remainingSec must be 0..43200", http.StatusBadRequest)
		return
	}
	if msg.RemainingSec == 0 {
		// The group's timer ended. A timer of this speaker's own, if one runs,
		// takes its countdown back.
		s.sleep.mu.Lock()
		own := time.Time{}
		if s.sleep.timer != nil {
			own = s.sleep.deadline
		}
		s.sleep.mu.Unlock()
		oled.SetSleepDeadline(own)
	} else {
		oled.SetSleepDeadline(time.Now().Add(time.Duration(msg.RemainingSec) * time.Second))
	}
	s.logger.Info("sleep timer: group countdown from another speaker", "remainingSec", msg.RemainingSec, "from", r.RemoteAddr)
	if !oled.Supported() {
		writeJSON(w, http.StatusOK, map[string]any{"shown": false, "reason": "no display"})
		return
	}
	if msg.Notice {
		showSleepNotice((msg.RemainingSec + 59) / 60)
	}
	writeJSON(w, http.StatusOK, map[string]any{"shown": true})
}

// handleKeyActions serves the per-speaker key-action document: which key
// carries the sleep timer (thumbs keys) or snooze (thumbs keys and AUX, see
// alarmsnooze.go).
//
//	GET /api/keyactions -> {"keys":{"thumbsUp":"sleep","aux":"snooze"}}
//	PUT /api/keyactions <- the whole document; validated, then persisted
//
// LAN only. The PUT is the one NAND write; a press writes nothing. A key that
// carries a saved group is refused with 409: a thumbs key carries one of the
// two, and the app says which to remove.
func (s *Server) handleKeyActions(w http.ResponseWriter, r *http.Request) {
	if s.keyActions == nil {
		http.Error(w, "key actions not configured", http.StatusServiceUnavailable)
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.keyActions.Get())
	case http.MethodPut:
		var d keyactions.Document
		if !decodeJSONRequest(w, r, 4096, &d) {
			return
		}
		groupBound := func(key string) bool { return s.groupKeys.Bound(key) }
		if err := d.Validate(groupBound); err != nil {
			code := http.StatusBadRequest
			for key := range d.Keys {
				if groupBound(key) {
					code = http.StatusConflict
				}
			}
			http.Error(w, err.Error(), code)
			return
		}
		if err := s.keyActions.Set(d, groupBound); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.logger.Info("key actions: saved from the app", "keys", d.Keys)
		writeJSON(w, http.StatusOK, s.keyActions.Get())
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
