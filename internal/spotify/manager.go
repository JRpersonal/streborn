// Package spotify runs go-librespot as a persistent Spotify Connect
// receiver on the speaker and exposes its audio so the box can play it
// over UPnP, the audio plane of the Spotify-preset feature (#78, P1).
//
// Why go-librespot (devgianlu) and not librespot-org:
//   - Hardware preset buttons 1..6 must recall a saved Spotify playlist
//     autonomously, with no phone app present. That needs the box to be
//     able to say "play URI X" by itself. librespot-org has no local
//     control API, so its only autonomous path is the Spotify Web API
//     with a refreshable OAuth token stored on the box (a security
//     surface we do not want). go-librespot ships a local HTTP API:
//     POST /player/play {uri} plays a URI using its own cached
//     credential, no token plane. See Play below.
//   - GPL-3.0 is fine here: go-librespot runs as a separate sidecar
//     process (exec + localhost HTTP). STR merely aggregates it; the
//     agent stays MIT. The binary is built, attested, audited and
//     credited separately.
//
// Audio shape:
//   - go-librespot runs with the STR Ogg-passthrough patch
//     (.github/patches/go-librespot-passthrough.patch) and
//     audio_output_pipe_passthrough. We point audio_output_pipe at
//     /dev/stdout so it writes the raw Ogg/Vorbis to its stdout (it logs
//     to stderr); the manager drains that and ServeOgg streams it to the
//     box, which decodes the Ogg natively over UPnP. This roughly halves
//     CPU on the weak A8 vs streaming decoded PCM (validated live).
//
// Credentials: zeroconf with persist_credentials. The user taps the
// device once in the Spotify app (the natural "connect to a speaker"
// flow); go-librespot persists the reusable credential under configDir
// and auto-logs-in on every later start, so API-driven recall works
// with no controller attached.
//
// Single consumer by design: one box plays one Spotify stream at a time.
// When no HTTP client is attached the audio is discarded so go-librespot
// never blocks on a full pipe.
package spotify

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Manager supervises one go-librespot process and brokers its PCM output
// (as a live WAV stream) to at most one HTTP consumer (the speaker),
// plus drives playback through go-librespot's local HTTP API.
type Manager struct {
	binPath   string
	configDir string
	name      string
	apiAddr   string // host:port of go-librespot's HTTP API
	logger    *slog.Logger
	bitr      int // 96/160/320
	client    *http.Client

	mu   sync.Mutex
	sink io.Writer // current HTTP consumer, nil when none
	cmd  *exec.Cmd
	// headerPages holds the current track's Ogg header pages (the BOS page
	// with the Vorbis identification header plus the comment/setup pages).
	// The drain captures them as they stream past; ServeOgg replays them to
	// a freshly-attached box before the live data, so a box that joins
	// mid-track still gets the headers it needs to start decoding (the next
	// real BOS is a whole track away). This is the Icecast late-joiner
	// pattern.
	headerPages []byte
}

// New returns a Manager. binPath is the go-librespot binary, configDir
// the config + credential directory (config.yml is written there on
// Run; the persisted zeroconf credential lives there after the first
// Spotify-app tap).
func New(binPath, configDir, deviceName string, logger *slog.Logger) *Manager {
	if deviceName == "" {
		deviceName = "ST Reborn"
	}
	return &Manager{
		binPath:   binPath,
		configDir: configDir,
		name:      deviceName,
		apiAddr:   "127.0.0.1:3678",
		logger:    logger,
		bitr:      160,
		client:    &http.Client{Timeout: 5 * time.Second},
	}
}

// Ready reports whether go-librespot can run: the binary exists. The
// device advertises over zeroconf even before the first tap, so we start
// it whenever the binary is present; playback control just returns an
// error until a credential is cached.
func (m *Manager) Ready() bool {
	if m.binPath == "" {
		return false
	}
	if fi, err := os.Stat(m.binPath); err != nil || fi.IsDir() {
		return false
	}
	return true
}

// configYAML is the go-librespot config the manager writes. /dev/stdout as
// the pipe + passthrough makes go-librespot emit the raw Ogg/Vorbis on its
// stdout (no decode); the box decodes it natively, which on the weak A8
// roughly halves CPU vs streaming decoded PCM. The API server gives us
// local playback control; zeroconf + persist gives a tap-once,
// auto-login-forever credential. Passthrough needs the STR patch
// (.github/patches/go-librespot-passthrough.patch) baked into the binary.
func (m *Manager) configYAML() string {
	host, port := splitHostPort(m.apiAddr)
	var b strings.Builder
	fmt.Fprintf(&b, "device_name: %q\n", m.name)
	b.WriteString("device_type: speaker\n")
	fmt.Fprintf(&b, "bitrate: %d\n", m.bitr)
	b.WriteString("audio_backend: pipe\n")
	b.WriteString("audio_output_pipe: /dev/stdout\n")
	b.WriteString("audio_output_pipe_format: s16le\n")
	b.WriteString("audio_output_pipe_passthrough: true\n")
	b.WriteString("server:\n")
	b.WriteString("  enabled: true\n")
	fmt.Fprintf(&b, "  address: %s\n", host)
	fmt.Fprintf(&b, "  port: %s\n", port)
	b.WriteString("credentials:\n")
	b.WriteString("  type: zeroconf\n")
	b.WriteString("  zeroconf:\n")
	b.WriteString("    persist_credentials: true\n")
	return b.String()
}

func (m *Manager) ensureConfig() error {
	if err := os.MkdirAll(m.configDir, 0o755); err != nil {
		return err
	}
	// No audio cache handling needed: go-librespot does not cache audio to
	// disk (verified in its source; only the tiny config + credential files
	// land in configDir). The NAND-filling cache seen earlier was the old
	// librespot (Rust, --cache), not go-librespot.
	return os.WriteFile(filepath.Join(m.configDir, "config.yml"), []byte(m.configYAML()), 0o644)
}

// Run supervises go-librespot until ctx is cancelled, restarting it with
// a short backoff if it exits. It returns immediately (idles) when not
// Ready, so callers can start it unconditionally.
func (m *Manager) Run(ctx context.Context) {
	if !m.Ready() {
		m.logger.Info("spotify manager idle: no go-librespot binary")
		return
	}
	if err := m.ensureConfig(); err != nil {
		m.logger.Warn("spotify: cannot write config, manager idle", "err", err)
		return
	}
	for ctx.Err() == nil {
		if err := m.runOnce(ctx); err != nil && ctx.Err() == nil {
			m.logger.Warn("go-librespot exited, restarting", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (m *Manager) runOnce(ctx context.Context) error {
	// go-librespot uses pflag: the long flag needs two dashes (-config_dir
	// is misparsed as a shorthand cluster). HOME is forced into the
	// writable config dir because the box rootfs is read-only and
	// go-librespot otherwise tries to create ~/.config.
	cmd := exec.CommandContext(ctx, m.binPath, "--config_dir", m.configDir)
	cmd.Env = append(os.Environ(), "HOME="+m.configDir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = newLogWriter(m.logger)
	if err := cmd.Start(); err != nil {
		return err
	}
	m.mu.Lock()
	m.cmd = cmd
	m.mu.Unlock()

	// Drain go-librespot's Ogg output page by page and forward whole pages to
	// the box. While no box is attached, capture the current track's header
	// pages and pause go-librespot so it does not race to the end of the
	// playlist unheard; ServeOgg resumes it and replays the headers when a
	// box joins, so a mid-track joiner can still decode.
	r := bufio.NewReaderSize(stdout, 256*1024)
	var hdr []byte
	capturing := false
	paused := false
	for {
		page, err := readOggPage(r)
		if err != nil {
			break
		}

		// Maintain the current track's header pages: a BOS page starts a
		// track (Vorbis identification header), the following granule<=0
		// pages carry comment/setup, the first audio page (granule>0) ends
		// the header sequence.
		htype := page[5]
		gran := int64(binary.LittleEndian.Uint64(page[6:14]))
		switch {
		case htype&0x02 != 0: // BOS
			hdr = append([]byte(nil), page...)
			capturing = true
		case capturing && gran > 0: // first audio page
			m.mu.Lock()
			m.headerPages = hdr
			m.mu.Unlock()
			capturing = false
		case capturing:
			hdr = append(hdr, page...)
		}

		m.mu.Lock()
		sink := m.sink
		haveHdr := len(m.headerPages) > 0
		m.mu.Unlock()

		if sink != nil {
			paused = false
			m.forward(sink, page)
			continue
		}
		// No consumer: once a track's headers are captured, pause go-librespot
		// so it stops producing (no racing) until a box attaches and ServeOgg
		// resumes it.
		if !paused && haveHdr {
			pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_ = m.Pause(pctx)
			cancel()
			paused = true
		}
		if ctx.Err() != nil {
			break
		}
	}
	return cmd.Wait()
}

// forward writes p to the box sink; on write error it drops the sink.
func (m *Manager) forward(sink io.Writer, p []byte) {
	if _, err := sink.Write(p); err != nil {
		m.mu.Lock()
		if m.sink == sink {
			m.sink = nil
		}
		m.mu.Unlock()
		return
	}
	if f, ok := sink.(http.Flusher); ok {
		f.Flush()
	}
}

// readOggPage reads one complete Ogg page from r, syncing to the "OggS"
// capture pattern. The returned slice is a whole page (27-byte header +
// segment table + body).
func readOggPage(r *bufio.Reader) ([]byte, error) {
	for { // sync to "OggS"
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != 'O' {
			continue
		}
		p, err := r.Peek(3)
		if err != nil {
			return nil, err
		}
		if p[0] == 'g' && p[1] == 'g' && p[2] == 'S' {
			if _, err := r.Discard(3); err != nil {
				return nil, err
			}
			break
		}
	}
	// 23 bytes after "OggS": version, header_type, granule(8), serial(4),
	// page_seq(4), crc(4), page_segments(1).
	rest := make([]byte, 23)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, err
	}
	numSegs := int(rest[22])
	segs := make([]byte, numSegs)
	if _, err := io.ReadFull(r, segs); err != nil {
		return nil, err
	}
	bodyLen := 0
	for _, s := range segs {
		bodyLen += int(s)
	}
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	page := make([]byte, 0, 4+23+numSegs+bodyLen)
	page = append(page, 'O', 'g', 'g', 'S')
	page = append(page, rest...)
	page = append(page, segs...)
	page = append(page, body...)
	return page, nil
}

// Play asks go-librespot to start playing a Spotify URI on this device,
// using its own cached credential. This is the autonomous-recall path:
// the agent calls it on a Spotify preset press, with no app involved.
//
// The recall calls Play FIRST, then points the box at /spotify/stream.ogg.
// With the sink-gated drain (see runOnce) go-librespot blocks on its full
// output pipe until the box attaches, so the pipe holds the track's start
// incl. the Ogg headers; the box therefore receives the stream from the
// beginning and can decode it.
func (m *Manager) Play(ctx context.Context, uri string) error {
	return m.apiPost(ctx, "/player/play", `{"uri":`+jsonString(uri)+`}`)
}

// Pause and Resume mirror the obvious controls.
func (m *Manager) Pause(ctx context.Context) error {
	return m.apiPost(ctx, "/player/pause", "")
}

func (m *Manager) Resume(ctx context.Context) error {
	return m.apiPost(ctx, "/player/resume", "")
}

// jsonString quotes a string as a JSON value.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (m *Manager) apiPost(ctx context.Context, path string, body string) error {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+m.apiAddr+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("go-librespot %s: %w", path, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("go-librespot %s: status %d", path, resp.StatusCode)
	}
	return nil
}

// ServeOgg streams go-librespot's live Ogg/Vorbis passthrough output to the
// HTTP client (the box's UPnP fetch) until it disconnects. It registers as
// the single consumer; a new request replaces any previous one. No header is
// prepended: the box decodes the raw Ogg directly.
func (m *Manager) ServeOgg(w http.ResponseWriter, r *http.Request) {
	if !m.Ready() {
		http.Error(w, "spotify not configured", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "audio/ogg")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)

	// Replay the current track's cached Ogg header pages first so a box that
	// joins mid-track has the identification/comment/setup headers it needs
	// to initialise the decoder; the live pages (forwarded by the drain)
	// then follow and are decodable even though they start mid-track.
	m.mu.Lock()
	hdr := append([]byte(nil), m.headerPages...)
	m.mu.Unlock()
	if len(hdr) > 0 {
		if _, err := w.Write(hdr); err != nil {
			return
		}
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	done := make(chan struct{})
	cw := &closeNotifyWriter{w: w, done: done}
	m.mu.Lock()
	m.sink = cw
	m.mu.Unlock()
	m.logger.Info("spotify: box attached to Ogg stream", "remote", r.RemoteAddr, "headerBytes", len(hdr))

	// The drain pauses go-librespot while no box is attached; resume it so
	// the live stream flows to this box.
	rctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	_ = m.Resume(rctx)
	cancel()

	select {
	case <-r.Context().Done():
	case <-done:
	}
	m.mu.Lock()
	if m.sink == cw {
		m.sink = nil
	}
	m.mu.Unlock()
	m.logger.Info("spotify: box detached from Ogg stream")
}

// closeNotifyWriter signals done on the first failed write so ServeOgg
// returns when the box drops the connection.
type closeNotifyWriter struct {
	w    io.Writer
	done chan struct{}
	once sync.Once
}

func (c *closeNotifyWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if err != nil {
		c.once.Do(func() { close(c.done) })
	}
	return n, err
}

func (c *closeNotifyWriter) Flush() {
	if f, ok := c.w.(http.Flusher); ok {
		f.Flush()
	}
}

func splitHostPort(addr string) (host, port string) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1", "3678"
	}
	return h, p
}

// logWriter forwards go-librespot stderr lines to the agent logger.
type logWriter struct{ logger *slog.Logger }

func newLogWriter(l *slog.Logger) *logWriter { return &logWriter{logger: l} }

func (w *logWriter) Write(p []byte) (int, error) {
	w.logger.Info("go-librespot", "line", trimEOL(string(p)))
	return len(p), nil
}

func trimEOL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
