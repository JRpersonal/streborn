// Package autopair sorgt dafuer dass die Bose Box immer mit dem Stick
// gepaart ist. Wird beim Agent Start ausgefuehrt und kann auch nach
// Box Reboots aktiv getriggert werden.
//
// Pair Flow:
//  1. GET http://<box>:8090/info um margeAccountUUID zu lesen
//  2. Wenn leer: POST http://<box>:8090/setMargeAccount mit PairDeviceWithAccount XML
//  3. Box ruft Stick's Marge Stub /streaming/account/.../device/ auf
//  4. Stub antwortet mit adddeviceresponse (wrap201 Format)
//  5. Box State Machine wechselt nach MargeStateAssociated
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

// Config beschreibt die Pair Identitaet.
type Config struct {
	BoxHost   string // z.B. "127.0.0.1" oder Box LAN IP
	AccountID string // z.B. "stick@local"
	AuthToken string // wird als userAuthToken gesendet
	Email     string // optional
}

// Manager kann den Pair Status pruefen und triggern.
type Manager struct {
	logger *slog.Logger
	cfg    Config
	client *http.Client
}

// New erstellt einen Manager mit sinnvollen Defaults.
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

// IsPaired liest /info und prueft ob margeAccountUUID gesetzt ist.
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

// Pair triggert den Pair Flow durch POST an /setMargeAccount.
// Erfolg = Box antwortet 200 OK (margeAccountUUID wird danach gesetzt).
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

// EnsurePaired prueft den Status und triggert Pair falls noetig.
// Idempotent: laeuft Box bereits gepaart, macht es nichts.
func (m *Manager) EnsurePaired(ctx context.Context) error {
	paired, err := m.IsPaired(ctx)
	if err != nil {
		return fmt.Errorf("status pruefen: %w", err)
	}
	if paired {
		// Auf Debug damit der Heartbeat alle 5 min nicht den Log
		// auf dem NAND vollschreibt. Nur State Changes sind interessant.
		m.logger.Debug("Box ist bereits gepaart, kein Re-Pair noetig")
		return nil
	}
	m.logger.Info("Box nicht gepaart, starte Auto Pair", "accountID", m.cfg.AccountID)
	if err := m.Pair(ctx); err != nil {
		return fmt.Errorf("pair: %w", err)
	}
	m.logger.Info("Box erfolgreich gepaart", "accountID", m.cfg.AccountID)
	return nil
}

// RunBackground laeuft im Hintergrund, paart einmal beim Start nach delay,
// und re-paart wenn Box den Status verliert (alle "interval"). Stop via
// ctx Cancel.
//
// Das delay beim Start gibt der Box Zeit BoseApp Webserver hochzufahren
// nach einem Box Reboot.
func (m *Manager) RunBackground(ctx context.Context, startDelay, interval time.Duration) {
	if startDelay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(startDelay):
		}
	}

	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if err := m.EnsurePaired(ctx); err != nil {
			m.logger.Warn("auto pair fehlgeschlagen, retry beim naechsten Tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// TriggerNow forciert ein Pair-Check Cycle, unabhaengig vom RunBackground
// Ticker. Nuetzlich z.B. wenn boxws ein Reconnect signalisiert.
func (m *Manager) TriggerNow(ctx context.Context) {
	if err := m.EnsurePaired(ctx); err != nil {
		m.logger.Warn("auto pair trigger fehlgeschlagen", "err", err)
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
