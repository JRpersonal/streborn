// Package webhooks holds user-configured HTTP requests that STR fires when a
// box trigger occurs. The first use is the remote's thumbs keys: the box only
// emits a generic <userActivityUpdate/> for them (no up/down identity), so STR
// cannot tell thumb-up from thumb-down. What it CAN do is detect a "lone" user
// activity (a key press with no accompanying volume/now-playing/preset change)
// and fire one configured request, which the user points at e.g. a smart-home
// toggle. See internal/boxws for the detection heuristic.
//
// The config is persisted on NAND so it survives a stick removal, the same
// place the agent keeps its other durable state.
package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

// Action is one configured HTTP request.
type Action struct {
	// Enabled gates firing without losing the configured URL.
	Enabled bool `json:"enabled"`
	// Method defaults to GET when empty.
	Method string `json:"method"`
	// URL is the full request target (http/https). Required when enabled.
	URL string `json:"url"`
	// Body is an optional request body (sent for non-GET methods).
	Body string `json:"body,omitempty"`
	// ContentType sets the Content-Type header when a body is sent.
	ContentType string `json:"content_type,omitempty"`
}

// Config is the full webhook configuration. Today it carries a single "thumb"
// action (the remote thumbs keys cannot be told apart, so they share one
// trigger, suited to an on/off toggle). Kept as a struct so more triggers can
// be added later without breaking the on-disk shape.
type Config struct {
	Thumb Action `json:"thumb"`
}

// Store is a NAND-persisted Config with a mutex and an HTTP client for firing.
type Store struct {
	path   string
	logger *slog.Logger

	mu  sync.RWMutex
	cfg Config

	client *http.Client

	// fireMu serializes fires and enforces a minimum gap so a burst of
	// triggers does not hammer the target.
	fireMu     sync.Mutex
	lastFireAt time.Time
}

// Load reads the config from path (missing file is fine: empty config).
func Load(path string, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{
		path:   path,
		logger: logger,
		client: &http.Client{Timeout: 8 * time.Second},
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("read webhooks config: %w", err)
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.cfg); err != nil {
		return s, fmt.Errorf("parse webhooks config: %w", err)
	}
	return s, nil
}

// Get returns a copy of the current config.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Set replaces the config and persists it atomically.
func (s *Store) Set(c Config) error {
	s.mu.Lock()
	s.cfg = c
	s.mu.Unlock()
	return s.save(c)
}

func (s *Store) save(c Config) error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".new"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// FireThumb fires the configured thumb action if enabled. It is rate-limited
// (one fire per 2s) so a burst of detections triggers the target once. Runs the
// request in the caller's context; errors are logged, not returned, because the
// caller is an event handler with nowhere to surface them.
func (s *Store) FireThumb(ctx context.Context) {
	s.mu.RLock()
	a := s.cfg.Thumb
	s.mu.RUnlock()
	if !a.Enabled || a.URL == "" {
		return
	}

	s.fireMu.Lock()
	if !s.lastFireAt.IsZero() && time.Since(s.lastFireAt) < 2*time.Second {
		s.fireMu.Unlock()
		s.logger.Debug("webhook thumb: suppressed (rate limit)")
		return
	}
	s.lastFireAt = time.Now()
	s.fireMu.Unlock()

	s.fire(ctx, a)
}

// Fire runs a single action immediately (used by the manual test endpoint).
func (s *Store) Fire(ctx context.Context, a Action) (int, error) {
	return s.fireOnce(ctx, a)
}

func (s *Store) fire(ctx context.Context, a Action) {
	code, err := s.fireOnce(ctx, a)
	if err != nil {
		s.logger.Warn("webhook fire failed", "url", a.URL, "err", err)
		return
	}
	s.logger.Info("webhook fired", "url", a.URL, "status", code)
}

func (s *Store) fireOnce(ctx context.Context, a Action) (int, error) {
	method := a.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if a.Body != "" && method != http.MethodGet {
		body = bytes.NewReader([]byte(a.Body))
	}
	req, err := http.NewRequestWithContext(ctx, method, a.URL, body)
	if err != nil {
		return 0, err
	}
	if a.Body != "" && method != http.MethodGet {
		ct := a.ContentType
		if ct == "" {
			ct = "application/json"
		}
		req.Header.Set("Content-Type", ct)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}
