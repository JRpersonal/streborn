package streamproxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// A speaker that drops a radio stream asks for it again a few seconds later:
// STR re-pushes the station once it sees the box idle. Every such restart used
// to dial the station from scratch, and on a slow link that took 7 to 9 s
// before the first byte came back (#1245, an ST10 stereo pair whose follower
// failed every 10 to 15 minutes: the drop itself was 2 s, the redial made it
// 10). So when the box closes a live stream while the station is still
// sending, the proxy now keeps that station connection open for heldFor,
// reading on and keeping the newest audio. A fetch of the same station inside
// that time continues on it, starting with what was kept, and needs no dial.
//
// Only a live stream of clean audio is held: a finite file ends on its own and
// is fetched by range, and a stream the box de-interleaves itself carries ICY
// blocks at offsets a new connection could not line up. One connection is held
// at a time; a fetch of anything else lets it go at once.

const (
	// heldFor is how long a dropped station stays connected. The re-push
	// comes about 2 s after the drop; standby or a station change simply let
	// it run out.
	heldFor = 15 * time.Second
	// heldKeep bounds the audio kept while held: 15 s at 256 kbit/s.
	heldKeep = 480 << 10
	// heldClaimWait bounds how long a claim waits for the held read in
	// flight to return. A live station sends several times a second; one
	// that is silent this long is no better than a fresh dial.
	heldClaimWait = 2 * time.Second
	// heldGrab is how long after the box went away the copy loop may still
	// wait for the station's next bytes to hand the connection over, before
	// it is closed like any other.
	heldGrab = 2 * time.Second
)

// titleGate mutes the ICY title parse of one station connection while it is
// held.
//
// The held connection keeps reading the station, and with it the metadata
// blocks the ICY reader parses. Those used to keep setting the live title
// after the box had gone: the end-of-stream wipe cleared the radio song five
// seconds after the box left, the held read parsed the next block a moment
// later and put the song straight back, and nothing wiped it again once the
// held connection ran out. A library track started over a station then showed
// that station's song under its own title for as long as it played (#1394,
// the regression of #274 the hold brought in). Muted from the moment the box
// lets go, unmuted only when a re-fetch of the same station claims it.
type titleGate struct{ muted atomic.Bool }

// heldConn is a station connection the box let go of, kept reading.
type heldConn struct {
	station string
	status  int
	header  http.Header
	src     io.Reader
	titles  *titleGate
	closeFn func()

	mu      sync.Mutex
	ring    *byteRing
	claimed bool
	ended   bool // expired, dropped or the station ended
	at      time.Time
	done    chan struct{}
}

// park takes over a live station connection the box just closed. lead is
// audio already in hand that never reached the box (the read that found the
// box gone), tail what the box got last on this station, so a resume starts
// on the very next byte of what it had.
func (s *Server) park(station string, resp *http.Response, src io.Reader, titles *titleGate, closeFn func(), tail, lead []byte) {
	if titles != nil {
		titles.muted.Store(true)
	}
	h := &heldConn{
		titles:  titles,
		station: station,
		status:  resp.StatusCode,
		header:  resp.Header.Clone(),
		src:     src,
		closeFn: closeFn,
		ring:    newByteRing(heldKeep),
		at:      time.Now(),
		done:    make(chan struct{}),
	}
	// The resumed box gets clean audio, whatever the station interleaves.
	h.header.Del("Icy-Metaint")
	h.ring.write(tail)
	h.ring.write(lead)
	s.heldMu.Lock()
	old := s.held
	s.held = h
	s.heldMu.Unlock()
	if old != nil {
		old.drop()
	}
	hold := s.heldFor
	go h.pump()
	time.AfterFunc(hold, func() {
		if h.expire() {
			s.heldMu.Lock()
			if s.held == h {
				s.held = nil
			}
			s.heldMu.Unlock()
			s.logger.Info("stream proxy: the held station connection ran out unused, closed", "url", station)
		}
	})
}

// pump keeps reading the held station until it is claimed, dropped or ends.
func (h *heldConn) pump() {
	defer close(h.done)
	buf := make([]byte, 16*1024)
	for {
		n, err := h.src.Read(buf)
		h.mu.Lock()
		if n > 0 {
			h.ring.write(buf[:n])
		}
		stop := h.claimed || h.ended || err != nil
		if err != nil && !h.claimed {
			h.ended = true
		}
		h.mu.Unlock()
		if stop {
			return
		}
	}
}

// expire ends an unclaimed held connection; true when it was still waiting.
func (h *heldConn) expire() bool {
	h.mu.Lock()
	if h.claimed {
		h.mu.Unlock()
		return false
	}
	was := h.ended
	h.ended = true
	h.mu.Unlock()
	h.closeFn()
	return !was
}

// drop lets go of a held connection nobody will claim.
func (h *heldConn) drop() {
	h.mu.Lock()
	claimed := h.claimed
	h.ended = true
	h.mu.Unlock()
	if !claimed {
		h.closeFn()
	}
}

// claimHeld returns the held connection for station as a response that
// starts with the kept audio, or nil. A held connection for anything else is
// dropped: the box moved on.
func (s *Server) claimHeld(station string) (*http.Response, time.Duration, int) {
	s.heldMu.Lock()
	h := s.held
	s.held = nil
	s.heldMu.Unlock()
	if h == nil {
		return nil, 0, 0
	}
	if h.station != station {
		h.drop()
		return nil, 0, 0
	}
	h.mu.Lock()
	if h.ended {
		h.mu.Unlock()
		h.closeFn()
		return nil, 0, 0
	}
	h.claimed = true
	h.mu.Unlock()
	select {
	case <-h.done:
	case <-time.After(heldClaimWait):
		h.closeFn()
		return nil, 0, 0
	}
	h.mu.Lock()
	ended := h.ended
	kept := h.ring.bytes()
	h.mu.Unlock()
	if ended {
		// The station closed while held; its last bytes are no stream.
		h.closeFn()
		return nil, 0, 0
	}
	// The box is listening to this station again, so its song is live again.
	if h.titles != nil {
		h.titles.muted.Store(false)
	}
	resp := &http.Response{
		StatusCode:    h.status,
		Status:        http.StatusText(h.status),
		Header:        h.header,
		ContentLength: -1,
		Body:          &heldBody{Reader: io.MultiReader(bytes.NewReader(kept), h.src), closeFn: h.closeFn, titles: h.titles},
	}
	return resp, time.Since(h.at), len(kept)
}

// heldBody is a claimed connection's body; closing it closes the station.
type heldBody struct {
	io.Reader
	once    sync.Once
	closeFn func()
	// titles is the title gate of the ICY reader inside Reader, if any.
	titles *titleGate
}

func (b *heldBody) Close() error {
	b.once.Do(b.closeFn)
	return nil
}

// dropHeldUnless lets go of a held connection for anything but station.
func (s *Server) dropHeldUnless(station string) {
	s.heldMu.Lock()
	h := s.held
	if h == nil || h.station == station {
		s.heldMu.Unlock()
		return
	}
	s.held = nil
	s.heldMu.Unlock()
	h.drop()
}

// dropHeld lets go of any held connection (tests, shutdown).
func (s *Server) dropHeld() {
	s.heldMu.Lock()
	h := s.held
	s.held = nil
	s.heldMu.Unlock()
	if h != nil {
		h.drop()
	}
}

// heldCloser closes resp and cancels the request context it was read under.
func heldCloser(resp *http.Response, cancel context.CancelFunc) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			_ = resp.Body.Close()
		})
	}
}
