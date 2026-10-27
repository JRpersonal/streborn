package streamproxy

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The new-play gate keeps a speaker STR wakes for a NEW play from playing the
// station it resumes by itself.
//
// A speaker in standby that STR wakes to play something else resumes its own
// last station first, and the play STR sends only takes over a second or two
// later. Muting the level cannot win that race reliably: the firmware sets its
// own level as it comes up, after anything STR wrote (#1557, live ST10
// 2026-10-27: 1LIVE resumed at level 10 at 10.08, the mute only held from
// about 10.8, audio flowing from 10.92). But the resumed station is a station
// STR serves: the speaker fetches it from this proxy. So while a new play is
// being started on a speaker STR just woke, every fetch that is NOT the new
// play's own target is answered with silence, and the station connection held
// from before standby (held.go) is closed at once instead of handed back.
//
// Silence rather than an error or a stalled connection, because silence is
// what the native LOCAL_INTERNET_RADIO path and UPnP both take quietly: a 404
// or 503 surfaces as a firmware error a moment later (INVALID_SOURCE, see
// silence.go, where the group wake learned exactly that), and a connection
// that sends nothing runs into the firmware's buffering timeout and the same
// error, followed by a retry. Silent MP3 frames are just a station that is
// quiet, and the play STR sends a moment later replaces it.
//
// The gate opens again the moment the new play's own fetch arrives, when the
// play fails (so a failed play falls back to the old behaviour instead of
// leaving the speaker silent), a short while after the play was handed over
// without its fetch arriving, and in any case after a bounded hold. Opening it
// ends every silent fetch it is feeding, so nothing stays silent behind it.

// NewPlayTarget names what the new play makes the speaker fetch: a preset
// slot (/stream/<slot>) and/or upstream station URLs (/stream/raw, or a slot
// whose preset carries the URL). The zero value matches nothing.
type NewPlayTarget struct {
	Slot int
	URLs []string
}

// Empty reports whether the target names nothing the proxy would serve.
func (t NewPlayTarget) Empty() bool {
	if t.Slot > 0 {
		return false
	}
	for _, u := range t.URLs {
		if strings.TrimSpace(u) != "" {
			return false
		}
	}
	return true
}

func (t NewPlayTarget) matches(slot int, url string) bool {
	if slot > 0 && slot == t.Slot {
		return true
	}
	url = strings.TrimSpace(url)
	if url == "" {
		return false
	}
	for _, u := range t.URLs {
		if strings.TrimSpace(u) == url {
			return true
		}
	}
	return false
}

type newPlayGate struct {
	mu       sync.Mutex
	gen      uint64
	armed    bool
	sent     bool
	target   NewPlayTarget
	released chan struct{}
	timer    *time.Timer
	blocked  int
}

// ArmNewPlayGate closes the gate for a new play that is about to wake the
// speaker, and drops the held station connection. maxHold bounds how long it
// stays closed if nobody opens it. Returns the generation the other calls
// take, so a later play's gate is never opened by an earlier play's caller.
func (s *Server) ArmNewPlayGate(target NewPlayTarget, maxHold time.Duration) uint64 {
	g := &s.newPlay
	g.mu.Lock()
	s.releaseLocked("a newer play took over")
	g.gen++
	gen := g.gen
	g.armed = true
	g.sent = false
	g.blocked = 0
	g.target = NewPlayTarget{Slot: target.Slot, URLs: append([]string(nil), target.URLs...)}
	g.released = make(chan struct{})
	g.timer = time.AfterFunc(maxHold, func() { s.ReleaseNewPlayGate(gen, "the hold ran out") })
	g.mu.Unlock()
	s.dropHeld()
	s.logger.Info("stream proxy: new play starting on a woken speaker, the station it resumes gets silence",
		"slot", target.Slot, "urls", len(target.URLs))
	return gen
}

// AddNewPlayTarget adds a URL to the target of the gate that is closed right
// now, if any: a preset recall that turns into a library folder learns the
// first track's URL only once the folder is loaded.
func (s *Server) AddNewPlayTarget(url string) {
	if strings.TrimSpace(url) == "" {
		return
	}
	g := &s.newPlay
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.armed {
		g.target.URLs = append(g.target.URLs, url)
	}
}

// NewPlaySent records that the play was handed to the speaker. Its own fetch
// normally arrives within a moment; grace bounds the wait for it from here.
func (s *Server) NewPlaySent(gen uint64, grace time.Duration) {
	g := &s.newPlay
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.armed || g.gen != gen {
		return
	}
	g.sent = true
	if g.timer != nil {
		g.timer.Stop()
	}
	g.timer = time.AfterFunc(grace, func() { s.ReleaseNewPlayGate(gen, "the play's own fetch did not come") })
}

// ReleaseNewPlayGate opens the gate of generation gen (0: whichever is
// closed).
func (s *Server) ReleaseNewPlayGate(gen uint64, why string) {
	g := &s.newPlay
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.armed || (gen != 0 && g.gen != gen) {
		return
	}
	s.releaseLocked(why)
}

// ReleaseSentNewPlayGate opens a gate whose play was already handed over and
// is only waiting for its fetch. Another play on an awake speaker calls it, so
// a gate left waiting cannot silence that play; a gate whose play is still
// being started (a preset recall that continues into a folder) stays closed.
func (s *Server) ReleaseSentNewPlayGate(why string) {
	g := &s.newPlay
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.armed && g.sent {
		s.releaseLocked(why)
	}
}

// NewPlayGateClosed reports whether the gate is closed right now.
func (s *Server) NewPlayGateClosed() bool {
	g := &s.newPlay
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.armed
}

func (s *Server) releaseLocked(why string) {
	g := &s.newPlay
	if !g.armed {
		return
	}
	g.armed = false
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	close(g.released)
	s.logger.Info("stream proxy: new-play gate open", "why", why, "silencedFetches", g.blocked)
}

// newPlayGateHolds decides one fetch against the gate. The new play's own
// fetch opens it and passes; any other fetch while it is closed is held, and
// the returned channel closes when the gate opens.
func (s *Server) newPlayGateHolds(slot int, url string) (bool, <-chan struct{}) {
	g := &s.newPlay
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.armed {
		return false, nil
	}
	if g.target.matches(slot, url) {
		s.releaseLocked("the new play's own fetch arrived")
		return false, nil
	}
	g.blocked++
	return true, g.released
}

// serveGatedSilence feeds silence until the speaker closes the fetch or the
// gate opens.
func (s *Server) serveGatedSilence(ctx context.Context, w http.ResponseWriter, released <-chan struct{}, slot int, url string) {
	s.logger.Info("stream proxy: a new play is starting, the speaker's resumed station gets silence",
		"slot", slot, "url", url)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-released:
			cancel()
		case <-ctx.Done():
		}
	}()
	serveSilence(ctx, w)
}
