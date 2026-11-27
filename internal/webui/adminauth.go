package webui

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	adminSessionCookie = "str_admin_session"
	desktopAdminHeader = "X-STR-Desktop-Admin"
	adminSessionAge    = 12 * time.Hour
	adminHashRounds    = 120_000
	adminMinPassword   = 8
)

type adminPasswordFile struct {
	Version      int    `json:"version"`
	Salt         string `json:"salt"`
	Hash         string `json:"hash"`
	Rounds       int    `json:"rounds"`
	RecoveryHash string `json:"recoveryHash,omitempty"`
}

type adminSession struct {
	expires time.Time
}

type adminAuthState struct {
	mu       sync.Mutex
	path     string
	password *adminPasswordFile
	broken   bool
	sessions map[[32]byte]adminSession
	// failed logins are bounded per remote address to slow password guessing.
	failed map[string]loginFailure
}

type loginFailure struct {
	window time.Time
	count  int
}

func newAdminAuthState(path string) (*adminAuthState, error) {
	a := &adminAuthState{path: path, sessions: make(map[[32]byte]adminSession), failed: make(map[string]loginFailure)}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		a.broken = true
		return a, err
	}
	var cfg adminPasswordFile
	if err := json.Unmarshal(b, &cfg); err != nil {
		a.broken = true
		return a, err
	}
	if cfg.Version != 1 || cfg.Rounds < 10_000 || cfg.Rounds > 1_000_000 {
		a.broken = true
		return a, errors.New("unsupported administrator password file")
	}
	salt, saltErr := hex.DecodeString(cfg.Salt)
	hash, hashErr := hex.DecodeString(cfg.Hash)
	if saltErr != nil || len(salt) != 16 || hashErr != nil || len(hash) != sha256.Size {
		a.broken = true
		return a, errors.New("invalid administrator password file")
	}
	a.password = &cfg
	return a, nil
}

func (a *adminAuthState) enabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.password != nil || a.broken
}

func (a *adminAuthState) authenticated(r *http.Request) bool {
	token := ""
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	} else if c, err := r.Cookie(adminSessionCookie); err == nil {
		token = c.Value
	}
	if token == "" {
		return false
	}
	key := sha256.Sum256([]byte(token))
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[key]
	if !ok || !s.expires.After(now) {
		delete(a.sessions, key)
		return false
	}
	return true
}

func (a *adminAuthState) setPassword(password string) error {
	cfg, err := makePasswordFile(password)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.password != nil {
		cfg.RecoveryHash = a.password.RecoveryHash
	}
	if err := a.persist(cfg); err != nil {
		return err
	}
	a.password = &cfg
	a.broken = false
	a.sessions = make(map[[32]byte]adminSession)
	return nil
}

func (a *adminAuthState) setInitialPassword(password, recoveryKey string) error {
	cfg, err := makePasswordFile(password)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.password != nil || a.broken {
		return errors.New("administrator password is already set")
	}
	cfg.RecoveryHash = hashRecoveryKey(recoveryKey)
	if err := a.persist(cfg); err != nil {
		return err
	}
	a.password = &cfg
	a.sessions = make(map[[32]byte]adminSession)
	return nil
}

func hashRecoveryKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (a *adminAuthState) recoverPassword(recoveryKey, password string) error {
	cfg, err := makePasswordFile(password)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.password == nil || a.broken || a.password.RecoveryHash == "" || !hmac.Equal([]byte(a.password.RecoveryHash), []byte(hashRecoveryKey(recoveryKey))) {
		return errors.New("administrator recovery key is invalid")
	}
	cfg.RecoveryHash = a.password.RecoveryHash
	if err := a.persist(cfg); err != nil {
		return err
	}
	a.password = &cfg
	a.sessions = make(map[[32]byte]adminSession)
	return nil
}

func makePasswordFile(password string) (adminPasswordFile, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return adminPasswordFile{}, err
	}
	cfg := adminPasswordFile{Version: 1, Salt: hex.EncodeToString(salt), Rounds: adminHashRounds}
	cfg.Hash = hex.EncodeToString(pbkdf2SHA256([]byte(password), salt, cfg.Rounds, sha256.Size))
	return cfg, nil
}

func (a *adminAuthState) persist(cfg adminPasswordFile) error {
	if a.path == "" {
		return errors.New("administrator password storage is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(a.path), ".admin-auth-*")
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
	return os.Rename(name, a.path)
}

func (a *adminAuthState) checkPassword(password string) bool {
	a.mu.Lock()
	cfg := a.password
	a.mu.Unlock()
	if cfg == nil {
		return false
	}
	salt, _ := hex.DecodeString(cfg.Salt)
	want, _ := hex.DecodeString(cfg.Hash)
	got := pbkdf2SHA256([]byte(password), salt, cfg.Rounds, len(want))
	return hmac.Equal(got, want)
}

func (a *adminAuthState) newSession(w http.ResponseWriter, secureCookie bool) (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	key := sha256.Sum256([]byte(token))
	expires := time.Now().Add(adminSessionAge)
	a.mu.Lock()
	for k, v := range a.sessions {
		if !v.expires.After(time.Now()) {
			delete(a.sessions, k)
		}
	}
	a.sessions[key] = adminSession{expires: expires}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: adminSessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: int(adminSessionAge.Seconds()), Expires: expires})
	return token, nil
}

func (a *adminAuthState) clearSession(w http.ResponseWriter, r *http.Request) {
	tokens := []string{}
	if c, err := r.Cookie(adminSessionCookie); err == nil {
		tokens = append(tokens, c.Value)
	}
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		tokens = append(tokens, strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
	}
	if len(tokens) > 0 {
		a.mu.Lock()
		for _, token := range tokens {
			delete(a.sessions, sha256.Sum256([]byte(token)))
		}
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: adminSessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func (a *adminAuthState) allowLogin(remote string) bool {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for ip, failure := range a.failed {
		if now.Sub(failure.window) >= time.Minute {
			delete(a.failed, ip)
		}
	}
	f := a.failed[remote]
	if now.Sub(f.window) >= time.Minute {
		f = loginFailure{window: now}
	}
	if f.count >= 5 {
		a.failed[remote] = f
		return false
	}
	f.count++
	a.failed[remote] = f
	return true
}

func (a *adminAuthState) clearFailures(remote string) {
	a.mu.Lock()
	delete(a.failed, remote)
	a.mu.Unlock()
}

func (s *Server) handleAdminAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a := s.adminAuth
	if a == nil {
		http.Error(w, "administrator authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.URL.Path {
	case "/api/admin/status":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": a.enabled(), "authenticated": a.authenticated(r)})
	case "/api/admin/setup":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		if r.TLS == nil {
			http.Error(w, "HTTPS required", http.StatusUpgradeRequired)
			return
		}
		if !adminAuthRequestFromSameOrigin(r) || !adminAuthJSONRequest(r) {
			http.Error(w, "same-origin JSON request required", http.StatusForbidden)
			return
		}
		if a.enabled() {
			http.Error(w, "administrator password is already set", http.StatusConflict)
			return
		}
		var body struct {
			Password    string `json:"password"`
			RecoveryKey string `json:"recoveryKey"`
		}
		if !decodeJSONRequest(w, r, 4096, &body) {
			return
		}
		if utf8.RuneCountInString(body.Password) < adminMinPassword || len(body.Password) > 1024 || len(body.RecoveryKey) < 32 {
			http.Error(w, "password must be between 8 and 1024 bytes", http.StatusBadRequest)
			return
		}
		if err := a.setInitialPassword(body.Password, body.RecoveryKey); err != nil {
			if err.Error() == "administrator password is already set" {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, "could not save administrator password", http.StatusInternalServerError)
			return
		}
		token, err := a.newSession(w, true)
		if err != nil {
			http.Error(w, "could not create administrator session", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": true, "authenticated": true, "token": token})
	case "/api/admin/login":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		if r.TLS == nil {
			http.Error(w, "HTTPS required", http.StatusUpgradeRequired)
			return
		}
		if !adminAuthRequestFromSameOrigin(r) || !adminAuthJSONRequest(r) {
			http.Error(w, "same-origin JSON request required", http.StatusForbidden)
			return
		}
		remote := remoteHost(r.RemoteAddr)
		if !a.allowLogin(remote) {
			http.Error(w, "too many attempts; wait one minute", http.StatusTooManyRequests)
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		if !decodeJSONRequest(w, r, 4096, &body) {
			return
		}
		if !a.checkPassword(body.Password) {
			http.Error(w, "incorrect password", http.StatusUnauthorized)
			return
		}
		a.clearFailures(remote)
		token, err := a.newSession(w, true)
		if err != nil {
			http.Error(w, "could not create administrator session", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": true, "authenticated": true, "token": token})
	case "/api/admin/recover":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		if r.TLS == nil {
			http.Error(w, "HTTPS required", http.StatusUpgradeRequired)
			return
		}
		if !adminAuthJSONRequest(r) {
			http.Error(w, "JSON request required", http.StatusBadRequest)
			return
		}
		var body struct {
			RecoveryKey string `json:"recoveryKey"`
			Password    string `json:"password"`
		}
		if !decodeJSONRequest(w, r, 4096, &body) {
			return
		}
		if utf8.RuneCountInString(body.Password) < adminMinPassword || len(body.Password) > 1024 {
			http.Error(w, "password must be between 8 and 1024 bytes", http.StatusBadRequest)
			return
		}
		if r.Header.Get(desktopAdminHeader) == "1" {
			// Desktop reset works even when password setup happened elsewhere
			// (for example, on the phone) and no recovery key was provisioned.
			if err := a.setPassword(body.Password); err != nil {
				http.Error(w, "could not save administrator password", http.StatusInternalServerError)
				return
			}
		} else if err := a.recoverPassword(body.RecoveryKey, body.Password); err != nil {
			http.Error(w, "recovery failed", http.StatusUnauthorized)
			return
		}
		token, err := a.newSession(w, true)
		if err != nil {
			http.Error(w, "could not create administrator session", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": token})
	case "/api/admin/logout":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		if r.TLS == nil {
			http.Error(w, "HTTPS required", http.StatusUpgradeRequired)
			return
		}
		if !adminAuthRequestFromSameOrigin(r) {
			http.Error(w, "same-origin request required", http.StatusForbidden)
			return
		}
		a.clearSession(w, r)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case "/api/admin/password":
		if r.TLS == nil {
			http.Error(w, "HTTPS required", http.StatusUpgradeRequired)
			return
		}
		if r.Method == http.MethodDelete {
			if !adminAuthRequestFromSameOrigin(r) {
				http.Error(w, "same-origin request required", http.StatusForbidden)
				return
			}
			if !a.authenticated(r) {
				http.Error(w, "administrator authentication required", http.StatusUnauthorized)
				return
			}
			if err := os.Remove(a.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				http.Error(w, "could not remove administrator password", http.StatusInternalServerError)
				return
			}
			a.mu.Lock()
			a.password = nil
			a.sessions = make(map[[32]byte]adminSession)
			a.mu.Unlock()
			a.clearSession(w, r)
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "enabled": false})
			return
		}
		if !requireMethod(w, r, http.MethodPut) {
			return
		}
		if !adminAuthRequestFromSameOrigin(r) || !adminAuthJSONRequest(r) {
			http.Error(w, "same-origin JSON request required", http.StatusForbidden)
			return
		}
		if !a.authenticated(r) {
			http.Error(w, "administrator authentication required", http.StatusUnauthorized)
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		if !decodeJSONRequest(w, r, 4096, &body) {
			return
		}
		if utf8.RuneCountInString(body.Password) < adminMinPassword || len(body.Password) > 1024 {
			http.Error(w, "password must be between 8 and 1024 bytes", http.StatusBadRequest)
			return
		}
		if err := a.setPassword(body.Password); err != nil {
			http.Error(w, "could not save administrator password", http.StatusInternalServerError)
			return
		}
		token, err := a.newSession(w, true)
		if err != nil {
			http.Error(w, "could not create administrator session", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": token})
	default:
		http.NotFound(w, r)
	}
}

func adminAuthJSONRequest(r *http.Request) bool {
	return strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json")
}

func adminAuthRequestFromSameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	expected := "http://" + r.Host
	if r.TLS != nil {
		expected = "https://" + r.Host
	}
	return strings.EqualFold(strings.TrimRight(origin, "/"), expected)
}

func remoteHost(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}

// adminRequired classifies configuration and maintenance API calls centrally,
// so an alternate client cannot bypass the interface's lock. Playback, volume,
// and reading/selecting an existing preset remain open as requested.
func adminRequired(path, method string) bool {
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

func (s *Server) adminAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/admin/") {
			s.handleAdminAuth(w, r)
			return
		}
		if s.adminAuth != nil && s.adminAuth.enabled() && adminRequired(r.URL.Path, r.Method) {
			if r.TLS == nil {
				http.Error(w, "HTTPS required", http.StatusUpgradeRequired)
				return
			}
			// The desktop app can use this shortcut over TLS so users can manage
			// their speaker without a second password prompt. This is deliberately
			// not an app identity check: any TLS client that can reach this API can
			// send the same header.
			if r.Header.Get(desktopAdminHeader) == "1" {
				next.ServeHTTP(w, r)
				return
			}
			if !s.adminAuth.authenticated(r) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":"administrator authentication required","code":"admin-auth-required"}`)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") != "" && !adminAuthRequestFromSameOrigin(r) {
				http.Error(w, "same-origin request required", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func pbkdf2SHA256(password, salt []byte, rounds, size int) []byte {
	result := make([]byte, size)
	var block [4]byte
	mac := hmac.New(sha256.New, password)
	u := make([]byte, 0, sha256.Size)
	t := make([]byte, sha256.Size)
	for blockNum, offset := uint32(1), 0; offset < size; blockNum++ {
		block[0], block[1], block[2], block[3] = byte(blockNum>>24), byte(blockNum>>16), byte(blockNum>>8), byte(blockNum)
		mac.Reset()
		_, _ = mac.Write(salt)
		_, _ = mac.Write(block[:])
		u = mac.Sum(u[:0])
		copy(t, u)
		for i := 1; i < rounds; i++ {
			mac.Reset()
			_, _ = mac.Write(u)
			u = mac.Sum(u[:0])
			for j := range t {
				t[j] ^= u[j]
			}
		}
		offset += copy(result[offset:], t)
	}
	return result
}
