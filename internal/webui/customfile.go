package webui

// A custom URL preset that points at a FILE (#830).
//
// A preset typed in by hand (Source empty) has always been recalled as a radio
// station: the speaker gets the stream proxy's /stream/<slot> and an open-ended
// DIDL with no length and no seekability. For a live station that is right. For
// a finite file on a NAS (http://nas.local/podcast/latest.mp3) it is not: the
// firmware has no position to return to, so Play/Pause on the remote started
// the episode over from the beginning. A library track saved from the Library
// tab never had the problem, because it is handed to the speaker as the file
// itself (librarykey.go).
//
// So a custom preset is played the library way when, and only when, its URL is
// positively a finite, seekable audio file. The URL suffix cannot tell: plenty
// of live stations end in .mp3 (.../ndr2.mp3). The server's answer can:
//
//   - a file has a size (Content-Length, or the total of a Content-Range) and
//     serves byte ranges (Accept-Ranges: bytes, or a 206 to a ranged GET);
//   - a station has neither, and nearly always sends icy-* headers.
//
// Anything short of positive evidence, including every error, keeps today's
// proxy path. The verdict is cached per URL for the agent run, so a press costs
// at most one probe per URL and nothing is ever written to the speaker's flash.

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/netutil"
	"github.com/JRpersonal/streborn/internal/presets"
)

const (
	// customFileProbeStep bounds each request of the probe. A file server on
	// the LAN answers headers in milliseconds; a slow answer is no evidence and
	// keeps the proxy path.
	customFileProbeStep = 2 * time.Second
	// customFileRetryAfter is how long an inconclusive probe (unreachable NAS,
	// timeout) is trusted before the next press may ask again. A definite
	// verdict is kept for the agent run.
	customFileRetryAfter = 10 * time.Minute
	// customFileProbesCap bounds the verdict cache; six keys and a handful of
	// app plays never come near it. The oldest entry goes first.
	customFileProbesCap = 32
)

// fileVerdict is what a probe concluded about a URL.
type fileVerdict int

const (
	// fileUnknown: the probe failed or was inconclusive. Proxy path.
	fileUnknown fileVerdict = iota
	// fileStream: a live stream, or a file the server will not serve in
	// ranges. Proxy path.
	fileStream
	// fileFinite: a sized file served in byte ranges. Direct path.
	fileFinite
)

func (v fileVerdict) String() string {
	switch v {
	case fileStream:
		return "stream"
	case fileFinite:
		return "file"
	}
	return "unknown"
}

// fileProbe is one cached verdict.
type fileProbe struct {
	verdict fileVerdict
	size    int64         // file size in bytes, 0 when not a file
	length  time.Duration // estimated play length, 0 when unknown
	at      time.Time
}

// fileProbes caches the verdict per URL. The zero value is ready to use.
type fileProbes struct {
	mu    sync.Mutex
	byURL map[string]fileProbe
	order []string
}

// get returns the cached verdict for url when it is still valid.
func (c *fileProbes) get(url string, now time.Time) (fileProbe, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fp, ok := c.byURL[url]
	if !ok {
		return fileProbe{}, false
	}
	if fp.verdict == fileUnknown && now.Sub(fp.at) >= customFileRetryAfter {
		return fileProbe{}, false
	}
	return fp, true
}

func (c *fileProbes) put(url string, fp fileProbe) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byURL == nil {
		c.byURL = make(map[string]fileProbe)
	}
	if _, have := c.byURL[url]; !have {
		c.order = append(c.order, url)
		if len(c.order) > customFileProbesCap {
			delete(c.byURL, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.byURL[url] = fp
}

// customFileProbeClient is a seam for the tests: the guarded client refuses
// loopback, and a test server is loopback. The guard still lets the LAN
// through, which is where a NAS lives; the stream proxy fetches the same URL
// with the same rule anyway, so the probe opens no new target.
var customFileProbeClient = func() *http.Client {
	return netutil.GuardedClient(customFileProbeStep)
}

// directFileMime returns the MIME a preset is handed to the speaker directly
// with, or "" when it plays through the radio proxy: a library track
// (libraryFileMime), or a custom URL preset that a probe showed to be a finite
// file (#830). May probe the URL once; call it outside any box lock that
// cannot wait a few seconds.
func (s *Server) directFileMime(ctx context.Context, p presets.Preset) string {
	if mime := libraryFileMime(p); mime != "" {
		return mime
	}
	return s.customFileMime(ctx, p)
}

// customFileMime is directFileMime's branch for a preset typed in by hand.
func (s *Server) customFileMime(ctx context.Context, p presets.Preset) string {
	switch strings.ToLower(strings.TrimSpace(p.Type)) {
	case "spotify", "queue", presets.TypeNative:
		return ""
	}
	if p.Source != "" || !isPlainHTTPURL(p.StreamURL) {
		return ""
	}
	mime := mimeFromURL(p.StreamURL)
	if mime == "" {
		return ""
	}
	if s.customFileProbe(ctx, p.StreamURL, mime).verdict != fileFinite {
		return ""
	}
	return mime
}

// customFileLength is the play length a probe estimated for url, 0 when none.
// Only reads the cache: it never probes.
func (s *Server) customFileLength(url string) time.Duration {
	fp, ok := s.fileProbes.get(url, time.Now())
	if !ok || fp.verdict != fileFinite {
		return 0
	}
	return fp.length
}

// customFileProbe returns the cached verdict for url, probing it first when
// there is none.
func (s *Server) customFileProbe(ctx context.Context, url, mime string) fileProbe {
	if fp, ok := s.fileProbes.get(url, time.Now()); ok {
		return fp
	}
	start := time.Now()
	fp := probeCustomFile(ctx, customFileProbeClient(), url, mime)
	fp.at = time.Now()
	s.fileProbes.put(url, fp)
	s.logger.Info("custom preset URL probed: file or stream",
		"url", url, "verdict", fp.verdict.String(), "size", fp.size,
		"lengthSec", int(fp.length.Seconds()), "probeMs", time.Since(start).Milliseconds())
	return fp
}

// probeCustomFile asks the server what url is. HEAD first, the cheapest
// question; a server that does not answer HEAD usefully (405, no size, no
// ranges advertised) gets a GET for the first byte, which a range-capable file
// server answers with a 206 and its total size, and a station answers with a
// 200 that is closed at once.
func probeCustomFile(ctx context.Context, cl *http.Client, url, mime string) fileProbe {
	if netutil.SafeHTTPURL(url) != nil {
		return fileProbe{verdict: fileUnknown}
	}
	verdict, size := probeFileStep(ctx, cl, http.MethodHead, url)
	if verdict == fileUnknown {
		verdict, size = probeFileStep(ctx, cl, http.MethodGet, url)
	}
	fp := fileProbe{verdict: verdict}
	if verdict != fileFinite {
		return fp
	}
	fp.size = size
	if mime == "audio/mpeg" {
		// An MP3 does not say how long it is unless the speaker is told, and
		// without the length the firmware has no position to come back to.
		// Other containers (m4a) carry it in their own header.
		fp.length = estimateMP3Length(ctx, cl, url, size)
	}
	return fp
}

// probeFileStep runs one request of the probe and classifies the answer.
func probeFileStep(ctx context.Context, cl *http.Client, method, url string) (fileVerdict, int64) {
	ctx, cancel := context.WithTimeout(ctx, customFileProbeStep)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return fileUnknown, 0
	}
	// A station only shows its icy-* headers to a client that asks like a
	// player does, and those headers are the clearest "this is a stream".
	req.Header.Set("Icy-MetaData", "1")
	req.Header.Set("User-Agent", "STR/file-check")
	ranged := method == http.MethodGet
	if ranged {
		req.Header.Set("Range", "bytes=0-0")
	}
	resp, err := cl.Do(req)
	if err != nil {
		return fileUnknown, 0
	}
	// Never drain: on a station the body is endless.
	defer resp.Body.Close()
	return classifyFileResponse(resp, ranged)
}

// classifyFileResponse is the probe's rule, separate so it can be tested
// without a network. ranged says the request carried Range: bytes=0-0.
func classifyFileResponse(resp *http.Response, ranged bool) (fileVerdict, int64) {
	for k := range resp.Header {
		if strings.HasPrefix(strings.ToLower(k), "icy-") {
			return fileStream, 0
		}
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "text/") || strings.Contains(ct, "mpegurl") || strings.Contains(ct, "scpls") {
		// A page or a playlist, never a file to hand over as audio.
		return fileStream, 0
	}
	if ranged {
		switch resp.StatusCode {
		case http.StatusPartialContent:
			if total := contentRangeTotal(resp.Header.Get("Content-Range")); total > 0 {
				return fileFinite, total
			}
			return fileUnknown, 0
		case http.StatusOK:
			// The range was ignored: either a live stream (no size) or a file
			// the server will not seek in. Neither can resume after a pause
			// without the proxy, so both stay on it.
			return fileStream, 0
		}
		return fileUnknown, 0
	}
	if resp.StatusCode != http.StatusOK {
		return fileUnknown, 0
	}
	size, _ := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("Content-Length")), 10, 64)
	if size > 0 && strings.EqualFold(strings.TrimSpace(resp.Header.Get("Accept-Ranges")), "bytes") {
		return fileFinite, size
	}
	return fileUnknown, 0
}

// contentRangeTotal returns the total size of a "bytes a-b/total" header, or 0
// when it is missing, malformed or "*".
func contentRangeTotal(h string) int64 {
	i := strings.LastIndexByte(h, '/')
	if i < 0 || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(h)), "bytes") {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(h[i+1:]), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// fetchRange GETs bytes [from, from+n) of url. The body is capped at n so a
// server that ignores the range cannot make it read a whole file.
func fetchRange(ctx context.Context, cl *http.Client, url string, from, n int64) []byte {
	ctx, cancel := context.WithTimeout(ctx, customFileProbeStep)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "STR/file-check")
	req.Header.Set("Range", "bytes="+strconv.FormatInt(from, 10)+"-"+strconv.FormatInt(from+n-1, 10))
	resp, err := cl.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	// A 200 is the right bytes only when the range started at the top.
	partial := resp.StatusCode == http.StatusPartialContent
	whole := resp.StatusCode == http.StatusOK && from == 0
	if !partial && !whole {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, n))
	return b
}
