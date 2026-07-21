package streamproxy

// HLS support (#124). The Bose UPnP player consumes a single continuous
// MP3/AAC byte stream; it cannot follow an HLS playlist or decode the
// MPEG-TS/fMP4 segment containers HLS ships. BBC Radio 4 and the other
// BBC stations are HLS-only, so without this they never play.
//
// serveHLS turns an HLS playlist into exactly that continuous stream: it
// follows the (live) media playlist, fetches each new segment in order,
// demuxes the audio elementary stream out of the MPEG-TS container (or
// passes a raw ADTS-AAC / MP3 segment straight through), and writes the
// result to the box as one endless ADTS/MP3 stream. fMP4/CMAF segments and
// DASH (.mpd) are not handled yet and fall back to the not-playable path.
//
// This is the staged plan from #124: stage 1 (playlist following) + stage 2
// (MPEG-TS -> ADTS). Stage 3 (fMP4 -> ADTS) is a follow-up.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// hlsRefreshFloor / hlsRefreshCeil bound how long we wait before re-fetching a
// live media playlist. Half the target duration is the usual HLS guidance;
// the bounds stop a degenerate playlist (no/ën huge target duration) from
// busy-looping or stalling.
const (
	hlsRefreshFloor = 1 * time.Second
	hlsRefreshCeil  = 8 * time.Second
)

// serveHLS plays an HLS stream to the box until the box disconnects or the
// stream ends. It resolves a master playlist to a media playlist, then loops
// fetching new segments and demuxing them to a continuous audio stream. It
// returns nil on a clean end (box gone / VOD ENDLIST) and an error when the
// stream could not be played at all (so the caller can report not-playable).
func (s *Server) serveHLS(ctx context.Context, w http.ResponseWriter, r *http.Request, playlistURL string) error {
	mediaURL, err := s.resolveHLSMedia(ctx, playlistURL)
	if err != nil {
		return err
	}

	flusher, _ := w.(http.Flusher)
	headersSent := false
	var lastSeq int64 = -1 // media sequence of the last segment we sent (-1 = none yet)
	firstPass := true
	loggedPlaylist := false
	var lastWrite time.Time // when we last sent audio bytes to the box
	var bytesToBox int64    // total audio bytes handed to the box this stream
	consecFetchFail := 0    // consecutive segment-fetch failures (dropout signal)
	hlsStart := time.Now()  // for time-to-first-audio and total-played timing

	for {
		if r.Context().Err() != nil {
			s.logger.Info("hls: box disconnected", "url", playlistURL)
			return nil
		}
		body, err := s.fetchText(ctx, mediaURL)
		if err != nil {
			// A transient media-playlist fetch error mid-stream is not fatal: wait
			// and retry. Only a failure before we ever sent audio is reported up.
			if !headersSent {
				return fmt.Errorf("hls: media playlist fetch failed: %w", err)
			}
			if !sleepCtx(r.Context(), hlsRefreshFloor) {
				return nil
			}
			continue
		}
		pl := parseMediaPlaylist(body, mediaURL)
		// Diagnostic: a high TARGETDURATION (long segments) means the live
		// playlist advances slowly; with our <=8s refresh that yields several
		// empty rounds in a row and a long no-bytes gap to the box. Some boxes
		// (SoundTouch 20 spotty/SCM observed) close the idle TCP connection at
		// ~20s and STR then reconnect-loops every ~20s. Logging targetDur here
		// makes that visible in a diagnostic bundle without a live box (#... spotty).
		if !loggedPlaylist {
			loggedPlaylist = true
			s.logger.Info("hls: media playlist", "targetDur", pl.targetDur,
				"segments", len(pl.segments), "live", !pl.endList)
		}
		if pl.isFMP4 {
			// Stage 3 (fMP4 -> ADTS) not implemented yet. Report not-playable so
			// the box shows a clear state instead of silence.
			if !headersSent {
				return fmt.Errorf("hls: fMP4/CMAF segments not supported yet")
			}
			return nil
		}
		if len(pl.segments) == 0 {
			if !headersSent {
				return fmt.Errorf("hls: media playlist has no segments")
			}
			if !sleepCtx(r.Context(), hlsRefreshFloor) {
				return nil
			}
			continue
		}

		// Dedup is by media sequence number, not by URL. Each segment's
		// sequence is EXT-X-MEDIA-SEQUENCE + its index; we only play segments
		// whose sequence is greater than the last one we sent (lastSeq). This
		// is O(1) in memory and correct for arbitrarily large DVR windows.
		// A bounded "seen URL" set is not: once the sliding window is bigger
		// than the set it forgets old URLs and replays them from the top --
		// hr1 ships a ~5h / ~4500-segment window, which is what surfaced this.
		//
		// First pass of a LIVE stream: arm lastSeq just below the live edge so
		// we start on the last few segments, not the whole back-catalogue. VOD
		// (ENDLIST): play from the beginning (lastSeq stays -1).
		if firstPass {
			if !pl.endList && len(pl.segments) > 3 {
				lastSeq = pl.mediaSeq + int64(len(pl.segments)) - 1 - 3
			}
			firstPass = false
		}

		// Encoder restart / sequence rewind: if the whole playlist now sits at
		// or below our cursor, re-arm at the new live edge instead of stalling
		// forever on a sequence that will never come.
		if lastSeq >= 0 && !pl.endList {
			lastInPlaylist := pl.mediaSeq + int64(len(pl.segments)) - 1
			if lastInPlaylist < lastSeq {
				s.logger.Warn("hls: media sequence rewound, re-arming at live edge",
					"lastSeq", lastSeq, "playlistEnd", lastInPlaylist)
				lastSeq = pl.mediaSeq - 1
				if len(pl.segments) > 3 {
					lastSeq = pl.mediaSeq + int64(len(pl.segments)) - 1 - 3
				}
			}
		}

		played := 0
		for i, seg := range pl.segments {
			if r.Context().Err() != nil {
				return nil
			}
			seq := pl.mediaSeq + int64(i)
			if seq <= lastSeq {
				continue
			}
			// Advance the cursor before fetching: a CDN hiccup on this segment
			// then skips forward instead of retrying stale audio out of order.
			lastSeq = seq

			data, err := s.fetchBytes(ctx, seg)
			if err != nil {
				consecFetchFail++
				// A single skipped segment is normal (CDN hiccup); several in a
				// row starve the box and are a dropout cause (#185), so surface
				// that once at WARN instead of only Debug.
				if consecFetchFail == 1 || consecFetchFail%5 == 0 {
					s.logger.Warn("hls: segment fetch failing, box may stall",
						"url", seg, "err", err, "consecFails", consecFetchFail)
				} else {
					s.logger.Debug("hls: segment fetch failed, skipping", "url", seg, "err", err)
				}
				continue
			}
			consecFetchFail = 0
			audio, ok := demuxSegment(data)
			if !ok {
				if !headersSent {
					return fmt.Errorf("hls: unsupported segment format (need MPEG-TS AAC/MP3 or raw ADTS/MP3)")
				}
				// Mid-stream odd segment: skip it rather than tearing down.
				s.logger.Debug("hls: skipping unsupported segment", "url", seg)
				continue
			}
			if !headersSent {
				w.Header().Set("Content-Type", "audio/aac")
				w.WriteHeader(http.StatusOK)
				headersSent = true
				s.clearFailure(playlistURL)
				s.logger.Info("hls: streaming to box", "media", mediaURL,
					"firstAudioMs", time.Since(hlsStart).Milliseconds())
			}
			if _, err := w.Write(audio); err != nil {
				s.logger.Info("hls: box write failed, ending", "err", err,
					"bytesToBox", bytesToBox, "playedSec", int(time.Since(hlsStart).Seconds()))
				return nil
			}
			if flusher != nil {
				flusher.Flush()
			}
			bytesToBox += int64(len(audio))
			lastWrite = time.Now()
			played++
		}

		if pl.endList {
			s.logger.Info("hls: VOD playlist ended")
			return nil
		}
		// Live: wait roughly half the target duration before refreshing for new
		// segments. If we played nothing this round, the floor wait avoids a busy
		// loop against an unchanged playlist.
		wait := time.Duration(pl.targetDur) * time.Second / 2
		if wait < hlsRefreshFloor {
			wait = hlsRefreshFloor
		}
		if wait > hlsRefreshCeil {
			wait = hlsRefreshCeil
		}
		// When a round delivers no new audio (playlist has not advanced yet), the
		// box gets no bytes for the whole wait. Several such rounds in a row push
		// the no-bytes gap past a box's TCP idle timeout (~20s on spotty/SCM),
		// which then closes the stream and reconnect-loops. Surface that gap so a
		// bundle shows it; the real fix depends on whether the box idle-closes
		// despite a full buffer (needs a live spotty capture to confirm).
		if played == 0 && !lastWrite.IsZero() {
			if idle := time.Since(lastWrite); idle > 12*time.Second {
				s.logger.Warn("hls: no new segment, box socket idle (box may drop the stream)",
					"idleSec", int(idle.Seconds()), "targetDur", pl.targetDur)
			}
		}
		if !sleepCtx(r.Context(), wait) {
			return nil
		}
	}
}

// resolveHLSMedia fetches playlistURL and, if it is a master playlist, picks an
// audio variant and returns its media-playlist URL; if it is already a media
// playlist, returns it unchanged.
func (s *Server) resolveHLSMedia(ctx context.Context, playlistURL string) (string, error) {
	body, err := s.fetchText(ctx, playlistURL)
	if err != nil {
		return "", fmt.Errorf("hls: playlist fetch failed: %w", err)
	}
	if !strings.Contains(body, "#EXT-X-STREAM-INF") {
		return playlistURL, nil // already a media playlist
	}
	media := pickMasterVariant(body, playlistURL)
	if media == "" {
		return "", fmt.Errorf("hls: no variant found in master playlist")
	}
	return media, nil
}

// pickMasterVariant returns the media-playlist URL of the lowest-bandwidth
// variant in a master playlist, resolved against baseURL. Radio masters list a
// handful of bitrates of the same audio; the lowest is the kindest to the box
// and to a constrained uplink, and the audio content is identical.
func pickMasterVariant(body, baseURL string) string {
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	bestBW := -1
	best := ""
	pendingBW := -1
	pending := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF") {
			pending = true
			pendingBW = parseBandwidth(line)
			continue
		}
		if pending && line != "" && !strings.HasPrefix(line, "#") {
			if bestBW < 0 || (pendingBW >= 0 && pendingBW < bestBW) {
				bestBW = pendingBW
				best = resolveURL(baseURL, line)
			}
			pending = false
		}
	}
	return best
}

// parseBandwidth pulls the BANDWIDTH attribute out of an #EXT-X-STREAM-INF line,
// or -1 if absent.
func parseBandwidth(line string) int {
	i := strings.Index(line, "BANDWIDTH=")
	if i < 0 {
		return -1
	}
	rest := line[i+len("BANDWIDTH="):]
	n := 0
	got := false
	for _, c := range rest {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
		got = true
	}
	if !got {
		return -1
	}
	return n
}

// mediaPlaylist is the parsed shape of an HLS media playlist.
type mediaPlaylist struct {
	segments  []string // absolute segment URLs, in order
	mediaSeq  int64    // #EXT-X-MEDIA-SEQUENCE: sequence number of segments[0] (0 if absent)
	targetDur int      // #EXT-X-TARGETDURATION seconds (0 if absent)
	endList   bool     // #EXT-X-ENDLIST present (VOD, not live)
	isFMP4    bool     // #EXT-X-MAP present (fMP4/CMAF init segment) -> stage 3
}

// parseMediaPlaylist parses a media playlist body, resolving segment URIs
// against baseURL.
func parseMediaPlaylist(body, baseURL string) mediaPlaylist {
	var pl mediaPlaylist
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#EXT-X-ENDLIST"):
			pl.endList = true
		case strings.HasPrefix(line, "#EXT-X-MAP"):
			// An init segment means fMP4/CMAF segments follow.
			pl.isFMP4 = true
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			pl.mediaSeq = atoi64Safe(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"))
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			pl.targetDur = atoiSafe(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"))
		case strings.HasPrefix(line, "#"):
			// Other tags (EXTINF, MEDIA-SEQUENCE, ...) are not needed: we
			// dedupe segments by URL, so we do not have to track sequence
			// numbers to know which are new.
			continue
		default:
			pl.segments = append(pl.segments, resolveURL(baseURL, line))
		}
	}
	return pl
}

// demuxSegment turns one HLS segment into the audio bytes the box can play:
// MPEG-TS is demuxed to its audio elementary stream (ADTS-AAC or MP3), a raw
// ADTS/MP3 segment passes through, and anything else (fMP4, unknown) returns
// ok=false so the caller treats the stream as not playable.
func demuxSegment(seg []byte) (audio []byte, ok bool) {
	if len(seg) < 4 {
		return nil, false
	}
	// fMP4/CMAF: a box-structured segment ("....ftyp/styp/moof/sidx"). Not
	// supported yet (stage 3). Needs at least the 8-byte box header.
	if len(seg) >= 8 {
		switch string(seg[4:8]) {
		case "ftyp", "styp", "moof", "sidx", "moov":
			return nil, false
		}
	}
	// MPEG-TS: 0x47 sync byte, packets of 188 bytes.
	if seg[0] == 0x47 && len(seg) >= 188 {
		if out := tsExtractAudio(seg); len(out) > 0 {
			return out, true
		}
		return nil, false
	}
	// Raw ADTS-AAC: frame sync 0xFFF, layer bits 00 (so byte1 is 0xF0..0xF1 /
	// 0xF8..0xF9 etc.). Pass through.
	if seg[0] == 0xFF && (seg[1]&0xF6) == 0xF0 {
		return seg, true
	}
	// MP3 frame sync (0xFFE.) or an ID3 tag preceding MP3. Pass through.
	if seg[0] == 0xFF && (seg[1]&0xE0) == 0xE0 {
		return seg, true
	}
	if string(seg[0:3]) == "ID3" {
		return seg, true
	}
	return nil, false
}

// ---- small helpers ----

// resolveURL resolves a possibly-relative HLS URI against the playlist URL.
func resolveURL(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

// atoiSafe parses leading digits of s into an int (0 on none); tolerant of a
// trailing ".000" or comment that some playlists append.
func atoiSafe(s string) int {
	s = strings.TrimSpace(s)
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// atoi64Safe parses leading digits of s into an int64 (0 on none). Media
// sequence numbers on long-running live streams can exceed int32 range.
func atoi64Safe(s string) int64 {
	s = strings.TrimSpace(s)
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

// sleepCtx waits d or until ctx is done; returns false if ctx ended.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// fetchText GETs url and returns the body as a string, with the proxy's SSRF
// guard and a bounded read.
func (s *Server) fetchText(ctx context.Context, raw string) (string, error) {
	b, err := s.fetchBytes(ctx, raw)
	return string(b), err
}

// fetchBytes GETs url through the proxy's guarded client and returns the body.
func (s *Server) fetchBytes(ctx context.Context, raw string) ([]byte, error) {
	if err := safeHTTPURL(raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "STR-Proxy/1.0")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &upstreamStatusError{Code: resp.StatusCode, Status: resp.Status}
	}
	// Segments are small (a few seconds of audio); 8 MB is a generous ceiling.
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}
