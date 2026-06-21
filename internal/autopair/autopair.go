// Package autopair makes sure the Bose box is always paired with the
// stick. Runs at agent start and can also be actively triggered after
// box reboots.
//
// Pair flow:
//  1. GET http://<box>:8090/info to read margeAccountUUID
//  2. If empty: POST http://<box>:8090/setMargeAccount with PairDeviceWithAccount XML
//  3. Box calls the stick's marge stub /streaming/account/.../device/
//  4. Stub answers with adddeviceresponse (wrap201 format)
//  5. Box state machine transitions to MargeStateAssociated
package autopair

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	defaultAccountID = "stick@local"
	defaultToken     = "stick-local-auth"
	defaultEmail     = "stick@local"
)

// Config describes the pair identity.
type Config struct {
	BoxHost   string // e.g. "127.0.0.1" or box LAN IP
	AccountID string // e.g. "stick@local"
	AuthToken string // sent as userAuthToken
	Email     string // optional
}

// Manager can check and trigger the pair status.
type Manager struct {
	logger *slog.Logger
	cfg    Config
	client *http.Client

	// lastPaired is the result of the most recent EnsurePaired call.
	// nil = unknown (no successful status read yet), &true / &false
	// for the last known state. Used to emit phase-marker logs only
	// on transitions so a diagnostic bundle has a clean timeline
	// without the 5-min heartbeat drowning everything else.
	lastPaired *bool
	tickCount  int
}

// New creates a Manager with sensible defaults.
func New(logger *slog.Logger, cfg Config) *Manager {
	if cfg.BoxHost == "" {
		cfg.BoxHost = "127.0.0.1"
	}
	if cfg.AccountID == "" {
		cfg.AccountID = defaultAccountID
	}
	if cfg.AuthToken == "" {
		cfg.AuthToken = defaultToken
	}
	if cfg.Email == "" {
		cfg.Email = defaultEmail
	}
	return &Manager{
		logger: logger,
		cfg:    cfg,
		client: &http.Client{Timeout: 8 * time.Second},
	}
}

// IsPaired reads /info and checks whether margeAccountUUID is set.
func (m *Manager) IsPaired(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s:8090/info", m.cfg.BoxHost), nil)
	if err != nil {
		return false, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return false, err
	}
	return hasMargeUUID(body), nil
}

var uuidRe = regexp.MustCompile(`<margeAccountUUID>([^<]+)</margeAccountUUID>`)

func hasMargeUUID(body []byte) bool {
	m := uuidRe.FindSubmatch(body)
	return len(m) == 2 && len(strings.TrimSpace(string(m[1]))) > 0
}

// Pair triggers the pair flow via POST to /setMargeAccount.
// Success = box answers 200 OK (margeAccountUUID is set afterwards).
func (m *Manager) Pair(ctx context.Context) error {
	body := buildPairXML(m.cfg.AccountID, m.cfg.AuthToken, m.cfg.Email)
	url := fmt.Sprintf("http://%s:8090/setMargeAccount", m.cfg.BoxHost)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/xml")
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("setMargeAccount status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// EnsurePaired checks the status and triggers Pair if needed.
// Idempotent: if the box is already paired, it does nothing.
//
// Before the first Pair attempt this also checks that the box's own
// clock is past 2024. The Bose firmware's RTC reads 2015 right after
// power-on and only catches up once the box reaches NTP. Calling Pair
// while the clock is in 2015 fails with `tls: expired certificate`
// (the box sees STR's 2026-issued cert as not-yet-valid even though
// it's already backdated, depending on how far back NotBefore goes).
// Gating here lets the periodic ticker simply retry until the clock
// is sane.
func (m *Manager) EnsurePaired(ctx context.Context) error {
	paired, err := m.IsPaired(ctx)
	if err != nil {
		// Status read failure is a phase marker on its own: it tells the
		// diagnostic bundle whether the box was reachable at all during
		// e.g. the standby window. Logged at WARN even though the loop
		// will retry, because "box silent for N ticks" is exactly the
		// signal we need for #60.
		m.logger.Warn("autopair phase: /info read failed", "err", err)
		return fmt.Errorf("check status: %w", err)
	}
	m.recordPairedState(paired)
	if paired {
		// Debug-level on the steady-state tick; the every-Nth heartbeat
		// emitted by RunBackground keeps the diagnostic bundle honest
		// without flooding the log on a healthy box.
		m.logger.Debug("box already paired, no re-pair needed")
		return nil
	}
	if ok, when := m.boxClockSane(ctx); !ok {
		m.logger.Info("auto pair deferred, box clock not yet synced (will retry next tick)",
			"boxDate", when)
		return nil
	}
	m.logger.Warn("autopair phase: box not paired, starting auto pair", "accountID", m.cfg.AccountID)
	if err := m.Pair(ctx); err != nil {
		return fmt.Errorf("pair: %w", err)
	}
	m.logger.Warn("autopair phase: box paired successfully", "accountID", m.cfg.AccountID)
	return nil
}

// recordPairedState emits a phase marker on every transition (paired
// <-> not paired). The first observation also counts as a transition,
// so the diagnostic bundle always carries an explicit "initial state"
// line right after agent start.
func (m *Manager) recordPairedState(paired bool) {
	if m.lastPaired == nil {
		m.logger.Warn("autopair phase: initial state observed", "paired", paired)
		v := paired
		m.lastPaired = &v
		return
	}
	if *m.lastPaired != paired {
		m.logger.Warn("autopair phase: paired state changed",
			"from", *m.lastPaired, "to", paired)
		v := paired
		m.lastPaired = &v
	}
}

// boxClockSane returns true if the box's own clock — as reported by
// the Date header on /info — is past 2024. Returns false on any error
// reading the header so callers default to "not sane" and retry
// later. The second return value is the parsed (or raw) date string
// for logging.
func (m *Manager) boxClockSane(ctx context.Context) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s:8090/info", m.cfg.BoxHost), nil)
	if err != nil {
		return false, "request-build-failed"
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return false, "request-failed"
	}
	defer resp.Body.Close()
	dh := resp.Header.Get("Date")
	if dh == "" {
		// Older Bose firmware variants do not always emit a Date
		// header on /info. Be lenient: if the box did answer, fall
		// through and let Pair proceed — the worst case is a single
		// failed handshake that the ticker retries.
		return true, "no-date-header"
	}
	t, err := http.ParseTime(dh)
	if err != nil {
		return true, "unparseable: " + dh
	}
	if t.Year() < 2024 {
		return false, t.UTC().Format(time.RFC3339)
	}
	return true, t.UTC().Format(time.RFC3339)
}

// RunBackground runs in the background, pairs once at start after delay,
// and re-pairs when the box loses the status (every "interval"). Stop via
// ctx cancel.
//
// The delay at start gives the box time to bring up the BoseApp web
// server after a box reboot.
func (m *Manager) RunBackground(ctx context.Context, startDelay, interval time.Duration) {
	if startDelay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(startDelay):
		}
	}

	// Every 6th tick (~30 min at the default 5-min interval) emit a
	// phase-marker heartbeat at WARN even when nothing changed, so a
	// diagnostic bundle proves the autopair loop is still alive across
	// the standby window. Without this, a healthy paired box looks
	// indistinguishable from a stalled goroutine in the log.
	const heartbeatEvery = 6
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		m.tickCount++
		if err := m.EnsurePaired(ctx); err != nil {
			m.logger.Warn("auto pair failed, will retry next tick", "err", err)
		} else if m.tickCount%heartbeatEvery == 0 {
			state := "unknown"
			if m.lastPaired != nil {
				if *m.lastPaired {
					state = "paired"
				} else {
					state = "not paired"
				}
			}
			m.logger.Warn("autopair phase: heartbeat",
				"tick", m.tickCount, "state", state, "interval", interval.String())
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// TriggerNow forces a pair-check cycle, independent of the RunBackground
// ticker. Useful e.g. when boxws signals a reconnect.
func (m *Manager) TriggerNow(ctx context.Context) {
	if err := m.EnsurePaired(ctx); err != nil {
		m.logger.Warn("auto pair trigger failed", "err", err)
	}
}

func buildPairXML(accountID, token, email string) string {
	return `<?xml version="1.0" encoding="UTF-8" ?>` +
		`<PairDeviceWithAccount>` +
		`<accountId>` + xmlEscape(accountID) + `</accountId>` +
		`<userAuthToken>` + xmlEscape(token) + `</userAuthToken>` +
		`<accountEmail>` + xmlEscape(email) + `</accountEmail>` +
		`</PairDeviceWithAccount>`
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
