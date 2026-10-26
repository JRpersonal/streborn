package webui

// Pre-arming the length of the next album track.
//
// A library queue advances by polling: there is no gapless path on the Bose
// firmware (SetNextAVTransportURI answers 401 Invalid Action on 27.0.6), so
// each track is a fresh push. When a track carries no length and the box
// reports no total either (a FRITZ!Box media server's folder view), the only
// end signal is the frozen-position net, which waits for the position to stand
// still for 15 s: 14 to 18 s of silence between two album tracks.
//
// So, while one track plays, STR looks up the length of the next one, in the
// background and once per track (no timer, nothing sent to the speaker):
//
//   - a length learned earlier in this agent run (trackLens), which only ever
//     holds a length the box really played to, or one a server sent;
//   - for an MP3, the frame count of its Xing/Info or VBRI header.
//
// Both are given to the track as its real length, so the box gets a total and
// the end is timed by it. A bare bitrate x size guess is NOT: for a VBR file
// without such a header it is wrong, and a wrong length handed to the box can
// end the track at the wrong place. It is kept as a hint the watcher only uses
// to time its next poll and to accept a freeze sooner.

import (
	"context"
	"net/http"
	"time"
)

// prearmTimeout bounds the reads against the media server for one track.
const prearmTimeout = 10 * time.Second

// Where a pre-armed length came from, as the log line names it.
const (
	prearmLearned = "learned"
	prearmExact   = "xing"
	prearmHint    = "cbr-hint"
	prearmNone    = "none"
)

// prearmNextLength looks up the length of the track a natural end of the
// current one plays next, when that track has none. It returns at once; the
// lookup runs in the background. Call it after a queue push.
func (s *Server) prearmNextLength() {
	if s.queue == nil {
		return
	}
	it, ok := s.queue.peekNatural()
	if !ok || it.Duration > 0 || it.Mime == "" || !isPlainHTTPURL(it.URL) {
		return
	}
	if s.lengthHints.get(it.URL) > 0 {
		return // estimated on an earlier lap
	}
	go s.prearmLength(it)
}

// prearmLength runs the lookup for one track and stores what it found.
func (s *Server) prearmLength(it queueItem) {
	ctx, cancel := context.WithTimeout(s.queueCtx(), prearmTimeout)
	defer cancel()
	d, src := s.lookupQueueLength(ctx, it)
	changed := 0
	switch src {
	case prearmLearned, prearmExact:
		changed = s.queue.setLength(it.URL, d)
	case prearmHint:
		s.lengthHints.put(it.URL, d)
	}
	s.logger.Info("queue: pre-armed length", "title", it.Title, "source", src,
		"lengthSec", int(d.Seconds()), "tracks", changed)
}

// lookupQueueLength finds a length for it and names where it came from.
func (s *Server) lookupQueueLength(ctx context.Context, it queueItem) (time.Duration, string) {
	if d := s.trackLens.get(it.URL); d > 0 {
		return d, prearmLearned
	}
	if !isMP3Mime(it.Mime) {
		return 0, prearmNone
	}
	probe := s.prearmProbeFn
	if probe == nil {
		probe = defaultPrearmProbe
	}
	d, exact := probe(ctx, it.URL)
	switch {
	case d <= 0:
		return 0, prearmNone
	case exact:
		return d, prearmExact
	}
	return d, prearmHint
}

// defaultPrearmProbe is the MP3 length read used when a Server sets no
// prearmProbeFn. The package tests replace it (TestMain) so a test that only
// advances a queue never reaches for the network.
var defaultPrearmProbe = probeMP3Length

// probeMP3Length reads the size of the MP3 at url and estimates its length
// from its first frame. exact is true only for a Xing/Info or VBRI frame count.
func probeMP3Length(ctx context.Context, url string) (time.Duration, bool) {
	cl := customFileProbeClient()
	verdict, size := probeFileStep(ctx, cl, http.MethodHead, url)
	if verdict == fileUnknown {
		verdict, size = probeFileStep(ctx, cl, http.MethodGet, url)
	}
	if verdict != fileFinite || size <= 0 {
		return 0, false
	}
	return estimateMP3LengthKind(ctx, cl, url, size)
}
