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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Action is one configured trigger action. Type selects the transport: an HTTP
// request (the original and default), a generic UDP packet, or a Wake-on-LAN
// magic packet. Keeping UDP generic (host/port/payload) lets users wire up their
// own UDP integrations (OSC, custom devices, home automation) without STR having
// to add each one; WoL is just the turnkey convenience layered on top (#187).
type Action struct {
	// Enabled gates firing without losing the configuration.
	Enabled bool `json:"enabled"`
	// Type is "http" (default when empty), "udp", or "wol".
	Type string `json:"type,omitempty"`

	// --- http ---
	// Method defaults to GET when empty.
	Method string `json:"method,omitempty"`
	// URL is the full request target (http/https). Required for an http action.
	URL string `json:"url,omitempty"`
	// Body is an optional request body (sent for non-GET methods).
	Body string `json:"body,omitempty"`
	// ContentType sets the Content-Type header when a body is sent.
	ContentType string `json:"content_type,omitempty"`

	// --- udp (generic) ---
	// Host/Port are the UDP target. Payload is the data, decoded per PayloadEnc
	// ("text" default, "hex", or "base64") before sending.
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	Payload    string `json:"payload,omitempty"`
	PayloadEnc string `json:"payload_enc,omitempty"`

	// --- wol ---
	// MAC is the target machine's hardware address (any common separator). The
	// magic packet is broadcast to Host:Port, defaulting to 255.255.255.255:9.
	MAC string `json:"mac,omitempty"`
}

// Configured reports whether the action has the fields its Type needs to fire.
// Replaces the old URL-only check so a udp/wol action (no URL) is not treated as
// unconfigured.
func (a Action) Configured() bool {
	switch a.Type {
	case "udp":
		return a.Host != "" && a.Port > 0
	case "wol":
		return a.MAC != ""
	default: // "" / "http"
		return a.URL != ""
	}
}

// Webhook interaction modes for the per-remote-key buttons.
const (
	// ModeAdditional: the box's normal reaction still happens (preset plays, AUX
	// switches input, power toggles) AND the webhook fires. The default.
	ModeAdditional = "additional"
	// ModeReplace: only the webhook fires, not the box's normal reaction.
	// Honored ONLY for preset keys 1-6, where STR drives the playback and can
	// withhold it (the user also clears the STR preset for that slot). For aux
	// and power the firmware switches input / toggles power regardless of STR,
	// so replace degrades to additional there.
	ModeReplace = "replace"
)

// Trigger is one configured action plus how it interacts with the box's own
// reaction to the key press. Used for the per-remote-key buttons.
type Trigger struct {
	Action
	Mode string `json:"mode,omitempty"`
}

// Config is the full webhook configuration. Thumb is a dedicated field for
// on-disk back-compat with the first release, which only had the thumbs trigger.
// Buttons holds the per-remote-key triggers added later, keyed by id:
// "preset1".."preset6", "aux", "power".
type Config struct {
	Thumb   Action             `json:"thumb"`
	Buttons map[string]Trigger `json:"buttons,omitempty"`
}

// Store is a NAND-persisted Config with a mutex and an HTTP client for firing.
type Store struct {
	path   string
	logger *slog.Logger

	mu  sync.RWMutex
	cfg Config

	client *http.Client

	// fireMu serializes fires and enforces a minimum gap PER trigger id so a
	// burst on one key fires the target once, while different keys stay
	// independent.
	fireMu     sync.Mutex
	lastFireAt map[string]time.Time
}

// Load reads the config from path (missing file is fine: empty config).
func Load(path string, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{
		path:       path,
		logger:     logger,
		client:     &http.Client{Timeout: 8 * time.Second},
		lastFireAt: make(map[string]time.Time),
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

// rateLimited reports whether id fired within the last 2s, recording now as the
// last fire when it returns false. Per-id so a burst on one key fires once while
// different keys stay independent.
func (s *Store) rateLimited(id string) bool {
	s.fireMu.Lock()
	defer s.fireMu.Unlock()
	if last, ok := s.lastFireAt[id]; ok && time.Since(last) < 2*time.Second {
		return true
	}
	s.lastFireAt[id] = time.Now()
	return false
}

// FireThumb fires the configured thumb action if enabled. Rate-limited per id.
// Runs the request in the caller's context; errors are logged, not returned,
// because the caller is an event handler with nowhere to surface them.
func (s *Store) FireThumb(ctx context.Context) {
	s.mu.RLock()
	a := s.cfg.Thumb
	s.mu.RUnlock()
	if !a.Enabled || !a.Configured() {
		return
	}
	if s.rateLimited("thumb") {
		s.logger.Debug("webhook thumb: suppressed (rate limit)")
		return
	}
	s.fire(ctx, a)
}

// Button returns the configured trigger for id ("preset1".."preset6", "aux",
// "power") only when it exists, is enabled, and has a URL.
func (s *Store) Button(id string) (Trigger, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.cfg.Buttons[id]
	if !ok || !t.Enabled || !t.Configured() {
		return Trigger{}, false
	}
	return t, true
}

// ButtonReplaceEnabled reports whether id has an enabled webhook in replace
// mode, i.e. the caller should withhold the box's normal reaction. Only
// meaningful for preset keys; aux/power callers ignore it.
func (s *Store) ButtonReplaceEnabled(id string) bool {
	t, ok := s.Button(id)
	return ok && t.Mode == ModeReplace
}

// FireButton fires the webhook configured for id if enabled. Rate-limited per
// id. Returns whether a configured+enabled action existed, so the caller can
// log a press even when it was rate-limited.
func (s *Store) FireButton(ctx context.Context, id string) bool {
	t, ok := s.Button(id)
	if !ok {
		return false
	}
	if s.rateLimited(id) {
		s.logger.Debug("webhook button: suppressed (rate limit)", "id", id)
		return true
	}
	s.fire(ctx, t.Action)
	return true
}

// Fire runs a single action immediately (used by the manual test endpoint).
func (s *Store) Fire(ctx context.Context, a Action) (int, error) {
	return s.fireOnce(ctx, a)
}

// target returns a short human label for the action's destination, for logging.
func (a Action) target() string {
	switch a.Type {
	case "udp":
		return fmt.Sprintf("udp %s:%d", a.Host, a.Port)
	case "wol":
		return "wol " + a.MAC
	default:
		return a.URL
	}
}

func (s *Store) fire(ctx context.Context, a Action) {
	code, err := s.fireOnce(ctx, a)
	if err != nil {
		s.logger.Warn("webhook fire failed", "target", a.target(), "err", err)
		return
	}
	s.logger.Info("webhook fired", "target", a.target(), "status", code)
}

// fireOnce dispatches on the action transport and returns a result code (the
// HTTP status for http; the bytes sent for udp/wol) plus an error.
func (s *Store) fireOnce(ctx context.Context, a Action) (int, error) {
	switch a.Type {
	case "udp":
		return fireUDP(a)
	case "wol":
		return fireWOL(a)
	default: // "" / "http"
		return s.fireHTTP(ctx, a)
	}
}

func (s *Store) fireHTTP(ctx context.Context, a Action) (int, error) {
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

// fireUDP sends a single generic UDP packet to Host:Port. Payload is decoded per
// PayloadEnc so users can send text, hex, or base64. Returns bytes sent.
func fireUDP(a Action) (int, error) {
	if a.Host == "" || a.Port <= 0 {
		return 0, fmt.Errorf("udp: host and port required")
	}
	payload, err := decodePayload(a.Payload, a.PayloadEnc)
	if err != nil {
		return 0, err
	}
	return sendUDPPacket(a.Host, a.Port, payload)
}

// fireWOL sends a Wake-on-LAN magic packet (6x 0xFF then the MAC repeated 16
// times) to the target, broadcast to 255.255.255.255:9 by default. Returns bytes
// sent. The speaker is on the LAN, so it is the right place to emit it (#187).
func fireWOL(a Action) (int, error) {
	mac, err := net.ParseMAC(strings.TrimSpace(a.MAC))
	if err != nil || len(mac) != 6 {
		return 0, fmt.Errorf("wol: invalid MAC %q", a.MAC)
	}
	packet := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		packet = append(packet, 0xFF)
	}
	for i := 0; i < 16; i++ {
		packet = append(packet, mac...)
	}
	host := a.Host
	if host == "" {
		host = "255.255.255.255"
	}
	port := a.Port
	if port <= 0 {
		port = 9
	}
	return sendUDPPacket(host, port, packet)
}

// decodePayload turns the configured payload string into bytes per enc:
// "" / "text" = literal UTF-8, "hex" = hex digits (whitespace/colons ignored),
// "base64" = standard base64.
func decodePayload(payload, enc string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "hex":
		clean := strings.NewReplacer(" ", "", "\t", "", "\n", "", ":", "", "-", "").Replace(payload)
		return hex.DecodeString(clean)
	case "base64":
		return base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	default:
		return []byte(payload), nil
	}
}
