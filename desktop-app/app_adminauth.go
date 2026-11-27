package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Speaker certificate pins and recovery keys are stored in the current user's
// app data directory. The recovery key is high-entropy and lets this desktop
// app reset a forgotten password immediately without a delay or the old one.
type adminLocalData struct {
	Pins     map[string]string `json:"pins"`
	Recovery map[string]string `json:"recovery"`
}

var adminLocalMu sync.Mutex
var adminSessions sync.Map     // speaker host -> short-lived bearer token
var adminEnabledCache sync.Map // speaker host -> whether password protection is enabled

const adminRequestTimeout = 60 * time.Second

func adminDataPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "STR", "admin-access")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "local.json"), nil
}

func readAdminData() (adminLocalData, error) {
	path, err := adminDataPath()
	if err != nil {
		return adminLocalData{}, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return adminLocalData{Pins: map[string]string{}, Recovery: map[string]string{}}, nil
	}
	if err != nil {
		return adminLocalData{}, err
	}
	var d adminLocalData
	if err := json.Unmarshal(b, &d); err != nil {
		return adminLocalData{}, err
	}
	if d.Pins == nil {
		d.Pins = map[string]string{}
	}
	if d.Recovery == nil {
		d.Recovery = map[string]string{}
	}
	return d, nil
}

func writeAdminData(d adminLocalData) error {
	path, err := adminDataPath()
	if err != nil {
		return err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".admin-access-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func adminHostKey(host string) string { return strings.ToLower(strings.TrimSpace(host)) }

func (a *App) AdminTLSFingerprint(host string, port int) (string, error) {
	if port == 0 {
		port = 17008
	}
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	dialer := &net.Dialer{Timeout: 6 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName: "streaming.bose.com", MinVersion: tls.VersionTLS12,
		InsecureSkipVerify: true, // first contact is explicitly confirmed and then pinned
	})
	if err != nil {
		return "", err
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", errors.New("speaker did not provide a TLS certificate")
	}
	sum := sha256.Sum256(state.PeerCertificates[0].Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:])), nil
}

func (a *App) AdminTrustTLSFingerprint(host, fingerprint string) error {
	fingerprint = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(fingerprint), ":", ""))
	if len(fingerprint) != sha256.Size*2 {
		return errors.New("invalid certificate fingerprint")
	}
	if _, err := hex.DecodeString(fingerprint); err != nil {
		return errors.New("invalid certificate fingerprint")
	}
	adminLocalMu.Lock()
	defer adminLocalMu.Unlock()
	d, err := readAdminData()
	if err != nil {
		return err
	}
	key := adminHostKey(host)
	if prior := d.Pins[key]; prior != "" && !strings.EqualFold(prior, fingerprint) {
		return errors.New("the speaker certificate does not match the saved fingerprint; do not trust it unless you have verified the change")
	}
	d.Pins[key] = fingerprint
	return writeAdminData(d)
}

func (a *App) adminTLSClient(host string, port int) (*http.Client, error) {
	if port == 0 {
		port = 17008
	}
	adminLocalMu.Lock()
	d, err := readAdminData()
	adminLocalMu.Unlock()
	if err != nil {
		return nil, err
	}
	pin := d.Pins[adminHostKey(host)]
	if pin == "" {
		// The desktop app accepts the speaker's first certificate automatically
		// so ordinary settings remain usable without a separate login/trust step.
		// The fingerprint is kept for later connections; a changed certificate
		// still stops the request.
		pin, err = a.AdminTLSFingerprint(host, port)
		if err != nil {
			return nil, err
		}
		if err := a.AdminTrustTLSFingerprint(host, pin); err != nil {
			return nil, err
		}
	}
	transport := &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{
		ServerName: "streaming.bose.com", MinVersion: tls.VersionTLS12, InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("speaker did not provide a TLS certificate")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if !strings.EqualFold(hex.EncodeToString(sum[:]), pin) {
				return errors.New("speaker TLS certificate changed; verify it before trusting the new certificate")
			}
			return nil
		},
	}}
	return &http.Client{Transport: transport, Timeout: adminRequestTimeout}, nil
}

func (a *App) adminRequest(host string, port int, method, path, bearer string, payload any) (map[string]any, error) {
	client, err := a.adminTLSClient(host, port)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	if port == 0 {
		port = 17008
	}
	url := "https://" + net.JoinHostPort(host, fmt.Sprint(port)) + path
	req, err := http.NewRequestWithContext(a.appCtx(), method, url, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if path == "/api/admin/recover" {
		req.Header.Set("X-STR-Desktop-Admin", "1")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if detail, ok := result["error"].(string); ok && detail != "" {
			return nil, errors.New(detail)
		}
		return nil, fmt.Errorf("speaker administrator request failed (%s)", resp.Status)
	}
	return result, nil
}

func (a *App) AdminAuthStatus(host string, port int) (map[string]any, error) {
	token := ""
	if v, ok := adminSessions.Load(adminHostKey(host)); ok {
		token = v.(string)
	}
	result, err := a.adminRequest(host, port, http.MethodGet, "/api/admin/status", token, nil)
	if err == nil {
		adminEnabledCache.Store(adminHostKey(host), result["enabled"] == true)
	}
	return result, err
}

func (a *App) AdminSetupPassword(host string, port int, password string) error {
	key := adminHostKey(host)
	adminLocalMu.Lock()
	d, err := readAdminData()
	recoveryKey := d.Recovery[key]
	if err == nil && recoveryKey == "" {
		secret := make([]byte, 32)
		if _, err = rand.Read(secret); err == nil {
			recoveryKey = base64.RawURLEncoding.EncodeToString(secret)
			d.Recovery[key] = recoveryKey
			err = writeAdminData(d)
		}
	}
	adminLocalMu.Unlock()
	if err != nil {
		return err
	}
	result, err := a.adminRequest(host, port, http.MethodPost, "/api/admin/setup", "", map[string]string{"password": password, "recoveryKey": recoveryKey})
	if err != nil {
		return err
	}
	if token, ok := result["token"].(string); ok && token != "" {
		adminSessions.Store(key, token)
	}
	adminEnabledCache.Store(key, true)
	return nil
}

func (a *App) AdminLogin(host string, port int, password string) error {
	result, err := a.adminRequest(host, port, http.MethodPost, "/api/admin/login", "", map[string]string{"password": password})
	if err != nil {
		return err
	}
	token, _ := result["token"].(string)
	if token == "" {
		return errors.New("speaker did not return an administrator session")
	}
	adminSessions.Store(adminHostKey(host), token)
	adminEnabledCache.Store(adminHostKey(host), true)
	return nil
}

func (a *App) AdminChangePassword(host string, port int, password string) error {
	tokenValue, ok := adminSessions.Load(adminHostKey(host))
	if !ok {
		return errors.New("log in as administrator first")
	}
	result, err := a.adminRequest(host, port, http.MethodPut, "/api/admin/password", tokenValue.(string), map[string]string{"password": password})
	if err == nil {
		if token, ok := result["token"].(string); ok && token != "" {
			adminSessions.Store(adminHostKey(host), token)
		}
	}
	return err
}

func (a *App) AdminForceResetPassword(host string, port int, password string) error {
	result, err := a.adminRequest(host, port, http.MethodPost, "/api/admin/recover", "", map[string]string{"password": password})
	if err != nil {
		return err
	}
	if token, ok := result["token"].(string); ok && token != "" {
		adminSessions.Store(adminHostKey(host), token)
	}
	adminEnabledCache.Store(adminHostKey(host), true)
	return nil
}

func (a *App) adminProtectionEnabled(host string, port int) bool {
	key := adminHostKey(host)
	if enabled, ok := adminEnabledCache.Load(key); ok {
		return enabled.(bool)
	}
	// Status is deliberately public and contains no secret. This lets older
	// installs and unlocked-by-default speakers keep using their normal HTTP
	// API until the optional password protection has been enabled.
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/admin/status", "", "")
	if err != nil {
		adminEnabledCache.Store(key, false)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		adminEnabledCache.Store(key, false)
		return false
	}
	var status struct {
		Enabled bool `json:"enabled"`
	}
	if json.NewDecoder(resp.Body).Decode(&status) != nil {
		adminEnabledCache.Store(key, false)
		return false
	}
	adminEnabledCache.Store(key, status.Enabled)
	return status.Enabled
}

func desktopAdminRequired(path, method string) bool {
	if !strings.HasPrefix(path, "/api/") && path != "/spotify/credential" && path != "/api/spotify/credential" {
		return path == "/spotify/quality"
	}
	if path == "/api/admin/status" || path == "/api/admin/login" || path == "/api/admin/setup" || path == "/api/admin/logout" || path == "/api/agent/version" || path == "/api/status" || path == "/api/position" || path == "/api/recent" || path == "/api/peers" || path == "/api/stick/status" {
		return false
	}
	if path == "/api/queue/save-slot" {
		return true
	}
	if path == "/api/play" || strings.HasPrefix(path, "/api/play/") || path == "/api/pause" || path == "/api/resume" || path == "/api/stop" || path == "/api/next" || path == "/api/prev" || path == "/api/seek" || strings.HasPrefix(path, "/api/queue/") || path == "/api/queue" || path == "/api/library/search" || path == "/api/library/servers" || path == "/api/library/browse" || path == "/api/library/locate" || path == "/api/library/play-folder" || path == "/api/pandora/stations" || path == "/api/pandora/play" || path == "/api/deezer/play" || path == "/api/spotify/liked" || path == "/api/spotify/playmode" {
		return false
	}
	if path == "/api/favorites" || path == "/api/favorites/order" {
		return method != http.MethodGet && method != http.MethodHead
	}
	if path == "/api/box/volume" || path == "/api/box/mute" || path == "/api/box/power" || path == "/api/box/source" || path == "/api/box/wake" || path == "/api/box/sleep" || path == "/api/box/zone/volume" || path == "/api/box/presets/recall" {
		return false
	}
	if path == "/api/box/group" {
		return method != http.MethodGet && method != http.MethodHead
	}
	if path == "/api/presets" || strings.HasPrefix(path, "/api/presets/") {
		return method != http.MethodGet && method != http.MethodHead
	}
	return true
}

func (a *App) AdminLogout(host string, port int) error {
	tokenValue, ok := adminSessions.Load(adminHostKey(host))
	if !ok {
		return nil
	}
	_, err := a.adminRequest(host, port, http.MethodPost, "/api/admin/logout", tokenValue.(string), nil)
	adminSessions.Delete(adminHostKey(host))
	return err
}
