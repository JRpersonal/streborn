// The left/right balance of a stereo pair: GET and POST /api/box/balance.
// Split out of zones_stereo.go.

package webui

import (
	"context"
	"net/http"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// handleBoxBalance reads and writes the left/right balance of a stereo pair.
//
//	GET  /api/box/balance -> {"available":bool,"min":-7,"max":7,"actual":0,...}
//	POST /api/box/balance {"target":-3} -> {"ok":true,"verified":true,"target":-3,...}
//
// Deliberately its OWN endpoint rather than a field on the zone read, and
// deliberately on a short budget. The firmware's /balance does not answer at
// all while the speaker is in deep standby: it does not refuse, it hangs (12 s
// and counting, measured 2026-08-04). The zone read is polled by the app every
// few seconds, so folding balance into it would have put a multi-second stall
// into a hot path for every speaker that happens to be asleep.
//
// The WRITE does not go over HTTP. Every POST /balance to the firmware hung the
// endpoint until the speaker was woken again, including the exact body the
// community reference sends, so STR showed the balance read-only from v0.9.35
// until 2026-09-10 and this comment said the firmware accepted no write that
// sticks. It was one interface short: gesellix reported on #70, with the source
// of the web app Bose ships on the speaker itself, that Bose's own client never
// writes balance over HTTP either and sends it on the gabbo WebSocket instead.
// internal/boxws could not send at all, so no path existed from which to try.
// It can now, and the write goes there (see boxws.SetBalance).
func (s *Server) handleBoxBalance(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.readBoxBalance(w, r)
	case http.MethodPost:
		s.writeBoxBalance(w, r)
	default:
		requireMethod(w, r, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) readBoxBalance(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	b, err := boxapi.New(s.boxHost).GetBalance(ctx)
	if err != nil {
		// A speaker asleep or otherwise not answering is not an error worth
		// showing: report "no balance to display" and let the caller move on.
		s.logger.Debug("balance: not readable", "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "reason": "unreachable"})
		return
	}
	// settable says whether this agent has a socket to write on at all, so the
	// app can render a slider instead of a read-out without having to know which
	// agent version it is talking to.
	writeJSON(w, http.StatusOK, balanceWithSettable(b, s.balanceWriteFn != nil))
}

// balanceWithSettable is boxapi.Balance plus the one field the app needs that
// the firmware does not report. Built as a map rather than a wrapper struct so
// the read answer keeps exactly the field names it had before.
func balanceWithSettable(b boxapi.Balance, settable bool) map[string]any {
	return map[string]any{
		"available": b.Available, "min": b.Min, "max": b.Max,
		"default": b.Default, "target": b.Target, "actual": b.Actual,
		"settable": settable && b.Available,
	}
}

// balanceWriteReq is the POST body. A POINTER so a missing field is told apart
// from a deliberate 0, which is the centre position and the most likely value
// anybody sends.
type balanceWriteReq struct {
	Target *int `json:"target"`
}

// balanceVerifyBudget bounds the read-back after a write. The firmware applies
// the value asynchronously and broadcasts balanceUpdated when it has; the write
// itself is fire-and-forget on the socket, so a read-back is the only evidence
// the value took. Kept short, because the app is waiting on it, and a miss is
// reported as "not verified" rather than as a failure: the balance may well
// have moved a moment after the answer went out.
const balanceVerifyBudget = 2500 * time.Millisecond

// writeBoxBalance sets the balance of the pair this speaker masters.
//
// Order matters. The current balance is read FIRST, for three things that come
// out of the same call: whether this speaker is in a pair at all (an unpaired
// one reports available=false), the bounds to clamp against, and the proof that
// the speaker is awake enough to answer, which the read-back afterwards depends
// on.
//
// Either member can be written to; the value belongs to the pair and both
// members report it (measured 2026-09-10). So this does not check for
// mastership, and a user who opens the settings of the right-hand speaker gets a
// working slider rather than a refusal.
func (s *Server) writeBoxBalance(w http.ResponseWriter, r *http.Request) {
	if s.balanceWriteFn == nil {
		// No socket client wired (an agent running without the box WebSocket,
		// and every test that does not need one). Say so plainly instead of
		// pretending to have written.
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "unsupported"})
		return
	}
	var req balanceWriteReq
	if !decodeJSONRequest(w, r, 1<<10, &req) {
		return
	}
	if req.Target == nil {
		http.Error(w, "target required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	c := boxapi.New(s.boxHost)
	cur, err := c.GetBalance(ctx)
	if err != nil {
		s.logger.Info("balance: cannot write, the speaker did not answer the read", "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "unreachable"})
		return
	}
	if !cur.Available {
		// No pair on this speaker, so there is no balance to move. A refusal
		// rather than an error: a stale pair record in the app is exactly how a
		// write would arrive here, and that is worth reporting plainly.
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "reason": "noPair", "available": false,
		})
		return
	}

	// Clamp against what the SPEAKER reports, never against a constant. The
	// community reference assumes -50..+50 and the firmware does not agree, so a
	// value from a client built against that range has to land inside this range
	// rather than be written through or rejected.
	want := clampInt(*req.Target, cur.Min, cur.Max)
	dev := s.localDeviceID(ctx, c, "")
	if dev == "" {
		s.logger.Info("balance: cannot write, this speaker did not report its own deviceID")
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "noDeviceID"})
		return
	}
	if err := s.balanceWriteFn(dev, want); err != nil {
		s.logger.Info("balance: the write did not leave", "err", err, "target", want)
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "sendFailed"})
		return
	}

	// Read it back on a budget of its own, not on what is left of the request's:
	// the read above may have used most of that, and a verify that expires
	// because its predecessor was slow would report a good write as unverified.
	vctx, vcancel := context.WithTimeout(context.WithoutCancel(r.Context()), balanceVerifyBudget)
	defer vcancel()
	after, verified := verifyBalance(vctx, c, want)
	if verified {
		s.logger.Info("balance: set", "target", want, "was", cur.Target)
	} else {
		s.logger.Info("balance: written, but the read-back did not show it yet",
			"target", want, "readBack", after.Target)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "verified": verified,
		"requested": *req.Target, "target": want,
		"min": cur.Min, "max": cur.Max,
		"actual": after.Actual, "available": true, "settable": true,
	})
}

// verifyBalance polls the firmware read until it reports the value that was
// written, or until the budget runs out. It compares targetBalance, not
// actualBalance: the target is what was asked for and appears at once, while
// the actual is what the pair currently realises and can still be ramping.
func verifyBalance(ctx context.Context, c *boxapi.Client, want int) (boxapi.Balance, bool) {
	var last boxapi.Balance
	for {
		b, err := c.GetBalance(ctx)
		if err == nil {
			last = b
			if b.Target == want {
				return b, true
			}
		}
		select {
		case <-ctx.Done():
			return last, false
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func clampInt(v, lo, hi int) int {
	if lo > hi {
		return v
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// SetBalanceWriteFn wires boxws.SetBalance so the balance endpoint has a socket
// to write on. Left nil, the endpoint stays read-only and says so, which is
// what a build without the box WebSocket gets.
func (s *Server) SetBalanceWriteFn(fn func(deviceID string, target int) error) {
	s.balanceWriteFn = fn
}
