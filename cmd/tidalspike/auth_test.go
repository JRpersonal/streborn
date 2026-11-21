package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a settable clock the oauth and api clients read.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestDeviceFlowPendingThenSuccess(t *testing.T) {
	var mu sync.Mutex
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		switch r.URL.Path {
		case "/device_authorization":
			if r.Form.Get("client_id") != "test-client" || r.Form.Get("scope") != defaultScope {
				t.Errorf("device_authorization form = %v", r.Form)
			}
			writeJSON(w, 200, map[string]any{
				"deviceCode": "dev-code", "userCode": "ABCDE",
				"verificationUri": "link.example.com", "verificationUriComplete": "link.example.com/ABCDE",
				"expiresIn": 300, "interval": 2,
			})
		case "/token":
			f := r.Form
			if f.Get("grant_type") != deviceGrantType || f.Get("device_code") != "dev-code" ||
				f.Get("client_id") != "test-client" || f.Get("client_secret") != "test-secret" {
				t.Errorf("token form = %v", f)
			}
			mu.Lock()
			polls++
			n := polls
			mu.Unlock()
			switch n {
			case 1, 2:
				writeJSON(w, 400, map[string]any{"error": "authorization_pending", "error_description": "wait"})
			case 3:
				writeJSON(w, 400, map[string]any{"error": "slow_down"})
			default:
				writeJSON(w, 200, map[string]any{
					"access_token": "acc-1", "refresh_token": "ref-1", "token_type": "Bearer", "expires_in": 3600,
					"user": map[string]any{"userId": 42, "countryCode": "DE"},
				})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	clk := &fakeClock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	var slept []time.Duration
	o := &oauth{
		Base: srv.URL, ClientID: "test-client", ClientSecret: "test-secret",
		Now: clk.Now,
		Sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			clk.Advance(d)
			return nil
		},
	}
	ctx := context.Background()
	da, err := o.startDevice(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if da.UserCode != "ABCDE" || da.Interval != 2 {
		t.Fatalf("device auth = %+v", da)
	}
	ts, err := o.pollDevice(ctx, da)
	if err != nil {
		t.Fatal(err)
	}
	if ts.AccessToken != "acc-1" || ts.RefreshToken != "ref-1" || ts.UserID != 42 || ts.CountryCode != "DE" {
		t.Fatalf("tokens = %+v", ts)
	}
	if polls != 4 {
		t.Fatalf("polls = %d, want 4", polls)
	}
	// pending, pending at the server interval, then slow_down adds 5 s.
	want := []time.Duration{2 * time.Second, 2 * time.Second, 7 * time.Second}
	if len(slept) != len(want) {
		t.Fatalf("slept %v, want %v", slept, want)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Fatalf("slept %v, want %v", slept, want)
		}
	}
	if wantExp := clk.Now().Add(time.Hour); !ts.ExpiresAt.Equal(wantExp) {
		t.Fatalf("ExpiresAt %v, want %v", ts.ExpiresAt, wantExp)
	}
}

func TestDeviceFlowStopsOnExpiredAndDenied(t *testing.T) {
	for _, code := range []string{"expired_token", "access_denied"} {
		t.Run(code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, 400, map[string]any{"error": code})
			}))
			defer srv.Close()
			o := &oauth{Base: srv.URL, ClientID: "c", Sleep: func(context.Context, time.Duration) error {
				t.Fatal("must not sleep after a terminal error")
				return nil
			}}
			_, err := o.pollDevice(context.Background(), &deviceAuth{DeviceCode: "d", ExpiresIn: 60, Interval: 1})
			if err == nil || !strings.Contains(err.Error(), code) {
				t.Fatalf("err = %v, want %s", err, code)
			}
		})
	}
}

func TestDeviceFlowGivesUpAtCodeExpiry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 400, map[string]any{"error": "authorization_pending"})
	}))
	defer srv.Close()
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	o := &oauth{Base: srv.URL, ClientID: "c", Now: clk.Now, Sleep: func(_ context.Context, d time.Duration) error {
		clk.Advance(d)
		return nil
	}}
	_, err := o.pollDevice(context.Background(), &deviceAuth{DeviceCode: "d", ExpiresIn: 10, Interval: 3})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("err = %v, want expiry", err)
	}
}

func TestRefreshKeepsRefreshTokenAndIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "ref-old" ||
			r.Form.Get("client_id") != "c" || r.Form.Get("client_secret") != "s" {
			t.Errorf("refresh form = %v", r.Form)
		}
		// Tidal does not rotate the refresh token and omits user here.
		writeJSON(w, 200, map[string]any{"access_token": "acc-new", "token_type": "Bearer", "expires_in": 600})
	}))
	defer srv.Close()
	o := &oauth{Base: srv.URL, ClientID: "c", ClientSecret: "s"}
	old := &tokenSet{AccessToken: "acc-old", RefreshToken: "ref-old", UserID: 7, CountryCode: "AT"}
	ts, err := o.refresh(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	if ts.AccessToken != "acc-new" || ts.RefreshToken != "ref-old" || ts.UserID != 7 || ts.CountryCode != "AT" {
		t.Fatalf("refreshed = %+v", ts)
	}
}

func TestRefreshErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 400, map[string]any{"error": "invalid_grant", "error_description": "token revoked"})
	}))
	defer srv.Close()
	o := &oauth{Base: srv.URL, ClientID: "c"}
	_, err := o.refresh(context.Background(), &tokenSet{RefreshToken: "r"})
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("err = %v", err)
	}
}

// TestAPIRefreshesExpiredTokenAndSaves covers the path every command takes:
// a stored token past its expiry is refreshed before the call, the new set is
// written back with 0600, and the call goes out with the new bearer.
func TestAPIRefreshesExpiredTokenAndSaves(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"access_token": "acc-new", "refresh_token": "ref-new", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer authSrv.Close()
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer acc-new" {
			writeJSON(w, 401, map[string]any{"status": 401, "subStatus": 11002, "userMessage": "expired"})
			return
		}
		if r.URL.Path != "/sessions" || r.URL.Query().Get("countryCode") != "DE" {
			t.Errorf("request %s", r.URL)
		}
		writeJSON(w, 200, map[string]any{"sessionId": "s", "userId": 42, "countryCode": "DE"})
	}))
	defer apiSrv.Close()

	path := filepath.Join(t.TempDir(), "sub", "tokens.json")
	c := &apiClient{
		Base:      apiSrv.URL,
		OAuth:     &oauth{Base: authSrv.URL, ClientID: "c", Now: clk.Now},
		Tokens:    &tokenSet{AccessToken: "acc-old", RefreshToken: "ref-old", TokenType: "Bearer", ExpiresAt: clk.Now().Add(-time.Minute), UserID: 42, CountryCode: "DE"},
		TokenPath: path,
		Now:       clk.Now,
	}
	s, err := c.session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.UserID != 42 {
		t.Fatalf("session = %+v", s)
	}
	saved, err := loadTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != "acc-new" || saved.RefreshToken != "ref-new" || saved.UserID != 42 || saved.CountryCode != "DE" {
		t.Fatalf("saved = %+v", saved)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("token file mode %v, want 0600", fi.Mode().Perm())
		}
	}
}

// TestAPIRetriesOnceAfter401 covers a token Tidal revoked before its
// recorded expiry.
func TestAPIRetriesOnceAfter401(t *testing.T) {
	refreshes := 0
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes++
		writeJSON(w, 200, map[string]any{"access_token": "acc-new", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer authSrv.Close()
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer acc-new" {
			writeJSON(w, 401, map[string]any{"status": 401})
			return
		}
		writeJSON(w, 200, map[string]any{"userId": 1, "countryCode": "DE"})
	}))
	defer apiSrv.Close()
	c := &apiClient{
		Base:   apiSrv.URL,
		OAuth:  &oauth{Base: authSrv.URL, ClientID: "c"},
		Tokens: &tokenSet{AccessToken: "acc-revoked", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)},
	}
	if _, err := c.session(context.Background()); err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes)
	}
}
