package streamproxy

import "time"

// noteReconnect records one upstream disconnect that forces a reconnect, for the
// radio_stream_health debug section and a single consolidated WARN. reason is a
// coarse tag (eof|read-fail); connBytes/connDur describe the connection that
// just ended; gap is how long the box has gone without audio. A station switch
// (different upstream URL) restarts the tally so the numbers describe the
// station currently playing, not a lifetime total.
//
// This is instrumentation only: it changes no playback behavior. It exists so a
// reporter's next diagnostic bundle answers "how often, and why" for an
// intermittent dropout without hand-counting log lines (Erich, ORF Vorarlberg,
// 2026-08-28): reason=eof at a steady cadence points at CDN token expiry;
// reason=read-fail with erratic timing points at the box's own uplink.
// noteHealthStart records the station a proxied stream is serving, the moment
// it starts rather than the first time it drops. Called from noteStreamStart,
// which already runs at every proxy handler start, so every path is covered.
//
// Until this existed, every field of radio_stream_health was written only by
// noteReconnect, so a station that had been playing happily for hours reported
// an empty upstreamURL and zeroes across the board. In a diagnostic bundle that
// reads as "no radio is playing", which is the opposite of the truth and is
// exactly the wrong way round for a dropout report: the healthy stretch is
// invisible and only the trouble is recorded.
//
// It cost a live misreading on 2026-09-24, where the empty section was taken as
// proof that a test stream was not running through the proxy at all while the
// log showed it plainly.
//
// A station switch restarts the tally here too, by the same rule noteReconnect
// uses, so the numbers always describe the station currently playing.
func (s *Server) noteHealthStart(url string) {
	if url == "" {
		return
	}
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	if url != s.healthURL {
		s.healthURL = url
		s.forwardedBytes = 0
		s.reconnectCount = 0
		s.lastDisconnectReason = ""
		s.lastGapMs = 0
	}
	s.playingSince = time.Now()
}

func (s *Server) noteReconnect(url, reason string, connBytes int64, connDur, gap time.Duration) {
	s.healthMu.Lock()
	if url != s.healthURL { // a station switch restarts the tally
		s.healthURL = url
		s.forwardedBytes = 0
		s.reconnectCount = 0
	}
	s.reconnectCount++
	s.lastDisconnectReason = reason
	s.lastGapMs = gap.Milliseconds()
	s.forwardedBytes += connBytes
	count, total := s.reconnectCount, s.forwardedBytes
	s.healthMu.Unlock()

	s.logger.Warn("radio upstream disconnect",
		"url", url, "reason", reason,
		"connBytes", connBytes, "connectedSec", int(connDur.Seconds()),
		"gapMs", gap.Milliseconds(), "reconnectCount", count, "forwardedBytesTotal", total)
}

// HealthSnapshot backs the radio_stream_health debug section registered in
// cmd/agent. Read lazily at /api/debug/state fetch time, like the other
// RegisterDebugSection providers.
func (s *Server) HealthSnapshot() any {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	out := map[string]any{
		"reconnectCount":       s.reconnectCount,
		"lastDisconnectReason": s.lastDisconnectReason,
		"lastGapMs":            s.lastGapMs,
		"upstreamURL":          s.healthURL,
		// forwardedBytes only tallies CONNECTIONS THAT HAVE ENDED, because it is
		// summed when one closes. A stream still running contributes nothing to
		// it yet, so 0 next to a live upstreamURL means "no drop so far", not
		// "no audio".
		"forwardedBytes": s.forwardedBytes,
	}
	if !s.playingSince.IsZero() {
		out["playingSince"] = s.playingSince.UTC().Format(time.RFC3339)
		out["playingForSec"] = int(time.Since(s.playingSince).Seconds())
	}
	return out
}
