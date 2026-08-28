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
	return map[string]any{
		"reconnectCount":       s.reconnectCount,
		"lastDisconnectReason": s.lastDisconnectReason,
		"lastGapMs":            s.lastGapMs,
		"upstreamURL":          s.healthURL,
		"forwardedBytes":       s.forwardedBytes,
	}
}
