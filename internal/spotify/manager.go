// Package spotify runs librespot as a persistent Spotify Connect
// receiver on the speaker and exposes its audio so the box can play it
// over UPnP, the audio plane of the Spotify-preset feature (#78, P1).
//
// Why this shape:
//   - librespot is a Connect receiver; with a cached credential it logs
//     in on its own and stays registered as a device, no app needed.
//   - run with -P/--passthrough + the pipe backend, it writes the raw
//     Ogg/Vorbis stream to stdout (no PCM decode on the weak Cortex-A8;
//     the Bose firmware decodes the Ogg when it plays the stream).
//   - the box plays HTTP audio URLs over UPnP (the radio path). So the
//     manager continuously drains librespot's stdout and, while the box
//     is connected to ServeOgg, forwards the live Ogg to it.
//
// Single consumer by design: one box plays one Spotify stream at a time.
// When no HTTP client is attached the output is discarded so librespot
// never blocks on a full pipe.
package spotify

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Manager supervises one librespot process and brokers its Ogg output to
// at most one HTTP consumer (the speaker).
type Manager struct {
	binPath   string
	cachePath string
	name      string
	logger    *slog.Logger

	mu   sync.Mutex
	sink io.Writer // current HTTP consumer, nil when none
	cmd  *exec.Cmd
	bitr int // librespot --bitrate (96/160/320)
}

// New returns a Manager. binPath is the librespot binary, cachePath the
// credential/cache dir (must already hold credentials.json from a prior
// OAuth login, otherwise librespot cannot authenticate and Run idles).
func New(binPath, cachePath, deviceName string, logger *slog.Logger) *Manager {
	if deviceName == "" {
		deviceName = "ST Reborn"
	}
	return &Manager{binPath: binPath, cachePath: cachePath, name: deviceName, logger: logger, bitr: 160}
}

// Ready reports whether librespot can run: the binary exists and a cached
// credential is present. Without a credential there is nothing to serve.
func (m *Manager) Ready() bool {
	if m.binPath == "" {
		return false
	}
	if fi, err := os.Stat(m.binPath); err != nil || fi.IsDir() {
		return false
	}
	if _, err := os.Stat(m.cachePath + "/credentials.json"); err != nil {
		return false
	}
	return true
}

// Run supervises librespot until ctx is cancelled, restarting it with a
// short backoff if it exits. It returns immediately (idles) when not
// Ready, so callers can start it unconditionally.
func (m *Manager) Run(ctx context.Context) {
	if !m.Ready() {
		m.logger.Info("spotify manager idle: no librespot binary or cached credential")
		return
	}
	for ctx.Err() == nil {
		if err := m.runOnce(ctx); err != nil && ctx.Err() == nil {
			m.logger.Warn("librespot exited, restarting", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (m *Manager) runOnce(ctx context.Context) error {
	// -P passthrough: emit raw Ogg, not PCM. --backend pipe: write it to
	// stdout. Cached credential => no OAuth, auto-connect.
	cmd := exec.CommandContext(ctx, m.binPath,
		"--passthrough",
		"--backend", "pipe",
		"--cache", m.cachePath,
		"--system-cache", m.cachePath,
		"--name", m.name,
		"--bitrate", itoa(m.bitr),
		"--device-type", "speaker",
	)
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

	// Drain stdout forever: forward to the current sink, discard otherwise
	// so librespot never blocks on a full pipe.
	buf := make([]byte, 16*1024)
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 {
			m.mu.Lock()
			sink := m.sink
			m.mu.Unlock()
			if sink != nil {
				if _, werr := sink.Write(buf[:n]); werr != nil {
					// Consumer (box) went away: drop it, keep draining.
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

// ServeOgg streams the live Ogg output to the HTTP client (the box's UPnP
// fetch) until it disconnects. It registers as the single consumer; a new
// request replaces any previous one.
func (m *Manager) ServeOgg(w http.ResponseWriter, r *http.Request) {
	if !m.Ready() {
		http.Error(w, "spotify not configured", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "audio/ogg")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)

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

func itoa(i int) string {
	if i <= 0 {
		return "160"
	}
	// small positive ints only
	buf := [4]byte{}
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// logWriter forwards librespot stderr lines to the agent logger.
type logWriter struct{ logger *slog.Logger }

func newLogWriter(l *slog.Logger) *logWriter { return &logWriter{logger: l} }

func (w *logWriter) Write(p []byte) (int, error) {
	w.logger.Info("librespot", "line", trimEOL(string(p)))
	return len(p), nil
}

func trimEOL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
