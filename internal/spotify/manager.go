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
	"bytes"
	"context"
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

	// Drain stdout forever: forward PCM to the current sink, discard
	// otherwise so go-librespot never blocks on a full pipe.
	buf := make([]byte, 16*1024)
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 {
			m.mu.Lock()
			sink := m.sink
			m.mu.Unlock()
			if sink != nil {
				if _, werr := sink.Write(buf[:n]); werr != nil {
					m.mu.Lock()
					if m.sink == sink {
						m.sink = nil
					}
					m.mu.Unlock()
				} else if f, ok := sink.(http.Flusher); ok {
					f.Flush()
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	return cmd.Wait()
}

// Play asks go-librespot to start playing a Spotify URI on this device,
// using its own cached credential. This is the autonomous-recall path:
// the agent calls it on a Spotify preset press, with no app involved.
func (m *Manager) Play(ctx context.Context, uri string) error {
	return m.apiPost(ctx, "/player/play", map[string]any{"uri": uri})
}

// Pause and Resume mirror the obvious controls.
func (m *Manager) Pause(ctx context.Context) error {
	return m.apiPost(ctx, "/player/pause", nil)
}

func (m *Manager) Resume(ctx context.Context) error {
	return m.apiPost(ctx, "/player/resume", nil)
}

func (m *Manager) apiPost(ctx context.Context, path string, body map[string]any) error {
	var r io.Reader
	if body != nil {
		buf := &bytes.Buffer{}
		// tiny hand-rolled JSON: only string values are used here
		buf.WriteByte('{')
		first := true
		for k, v := range body {
			if !first {
				buf.WriteByte(',')
			}
			first = false
			fmt.Fprintf(buf, "%q:%q", k, fmt.Sprint(v))
		}
		buf.WriteByte('}')
		r = buf
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
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	done := make(chan struct{})
	cw := &closeNotifyWriter{w: w, done: done}
	m.mu.Lock()
	m.sink = cw
	m.mu.Unlock()
	m.logger.Info("spotify: box attached to Ogg stream", "remote", r.RemoteAddr)

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
