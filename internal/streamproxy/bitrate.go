// bitrate.go: real stream bitrate detection — icy-br header parsing, the
// throughput-measurement bookkeeping, and the bitrate endpoint.

package streamproxy

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// CurrentBitrate returns the detected bitrate (kbps) of the stream being
// proxied right now, or 0 if unknown.
func (s *Server) CurrentBitrate() int {
	s.brMu.Lock()
	defer s.brMu.Unlock()
	return s.curBitrate
}

// beginStream marks url as the stream now playing and seeds curBitrate:
// the icy-br value if the station sent one, else a value already measured
// for this URL in a previous play (so a reconnect or a re-play does not
// briefly show "-"), else 0 (unknown, to be measured). Crucially it always
// updates curURL so switching from a station that had a bitrate to one that
// does not clears the stale number instead of leaving the old one showing.
func (s *Server) beginStream(url string, icyBr int) (known bool) {
	s.brMu.Lock()
	defer s.brMu.Unlock()
	s.curURL = url
	if icyBr > 0 {
		s.curBitrate = icyBr
		s.measuredBr[url] = icyBr
		return true
	}
	if br, ok := s.measuredBr[url]; ok {
		s.curBitrate = br
		return true
	}
	s.curBitrate = 0
	return false
}

// rememberBitrate stores a throughput-measured bitrate for url and makes it
// the current value, so internal reconnects reuse it rather than measuring
// a fresh (and slightly different) number on every UI poll. The map is
// capped so the ad-hoc search-play path cannot grow it without bound.
func (s *Server) rememberBitrate(url string, br int) {
	if br <= 0 {
		return
	}
	s.brMu.Lock()
	defer s.brMu.Unlock()
	if len(s.measuredBr) > 64 {
		s.measuredBr = make(map[string]int)
	}
	s.measuredBr[url] = br
	s.curURL = url
	s.curBitrate = br
}

// icyBitrate pulls the real stream bitrate (kbps) from the Icecast/
// Shoutcast response headers, 0 if none present. icy-br is sometimes a
// comma list ("128,128"); the first value is used.
func icyBitrate(h http.Header) int {
	for _, k := range []string{"icy-br", "ice-bitrate", "x-audiocast-bitrate"} {
		v := h.Get(k)
		if v == "" {
			continue
		}
		if i := strings.IndexByte(v, ','); i >= 0 {
			v = v[:i]
		}
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// standardBitrates are the common MP3/AAC stream rates a throughput
// measurement is snapped to, so the displayed value is stable and
// realistic instead of a jittery raw number.
var standardBitrates = []int{32, 48, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 448, 512}

// roundStandardBitrate snaps a throughput-derived kbps to the nearest
// common audio stream rate. Steady-state throughput of a playing stream
// sits very close to its real bitrate, so the nearest standard rate is the
// honest value. Anything above the highest audio rate is still buffer-fill
// burst, not a real bitrate, and returns 0 (shown as "-" rather than a
// misleading number like 1310).
func roundStandardBitrate(kbps int) int {
	if kbps <= 0 || kbps > 600 {
		return 0
	}
	best, bestDelta := 0, 1<<30
	for _, std := range standardBitrates {
		d := kbps - std
		if d < 0 {
			d = -d
		}
		if d < bestDelta {
			bestDelta, best = d, std
		}
	}
	return best
}

// handleBitrate returns the real bitrate (kbps) of the stream currently
// proxied, detected from the upstream icy-br header or a throughput
// sample. 0 means unknown. Cheap: a single guarded int read. The desktop
// app fetches this once per station change (not on a timer) to keep box
// load minimal.
func (s *Server) handleBitrate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"bitrate":%d}`, s.CurrentBitrate())
}
