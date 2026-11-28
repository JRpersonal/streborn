package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// defaultAuthBase is Tidal's OAuth2 host. The device flow uses
// <base>/device_authorization and <base>/token, the same two endpoints the
// python-tidal device login uses.
const defaultAuthBase = "https://auth.tidal.com/v1/oauth2"

// defaultScope is the scope the TV / device client asks for.
const defaultScope = "r_usr w_usr w_sub"

// deviceGrantType is the RFC 8628 grant type for polling a device code.
const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// tokenSet is what the spike keeps on disk between runs. It holds only what
// the API calls need: the tokens, their expiry, and the user id and country
// code the v1 API wants on every request. No name, no e-mail.
type tokenSet struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	ExpiresAt    time.Time `json:"expires_at"`
	UserID       int64     `json:"user_id,omitempty"`
	CountryCode  string    `json:"country_code,omitempty"`
}

// authHeader returns the Authorization header value for the access token.
func (t *tokenSet) authHeader() string {
	typ := t.TokenType
	if typ == "" {
		typ = "Bearer"
	}
	return typ + " " + t.AccessToken
}

// oauth is the device-flow client. Every external dependency (base URL, HTTP
// client, clock, sleep) is a field so the tests can drive it against an
// httptest fake without waiting real seconds.
type oauth struct {
	Base         string
	ClientID     string
	ClientSecret string
	Scope        string
	HTTP         *http.Client
	Now          func() time.Time
	Sleep        func(ctx context.Context, d time.Duration) error
}

func (o *oauth) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *oauth) sleep(ctx context.Context, d time.Duration) error {
	if o.Sleep != nil {
		return o.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (o *oauth) httpClient() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (o *oauth) scope() string {
	if o.Scope != "" {
		return o.Scope
	}
	return defaultScope
}

// deviceAuth is the device_authorization response. Tidal answers in
// camelCase, unlike the RFC's snake_case.
type deviceAuth struct {
	DeviceCode              string  `json:"deviceCode"`
	UserCode                string  `json:"userCode"`
	VerificationURI         string  `json:"verificationUri"`
	VerificationURIComplete string  `json:"verificationUriComplete"`
	ExpiresIn               float64 `json:"expiresIn"`
	Interval                float64 `json:"interval"`
}

// tokenResponse covers both the success body and the RFC 6749 error body of
// the token endpoint.
type tokenResponse struct {
	AccessToken      string  `json:"access_token"`
	RefreshToken     string  `json:"refresh_token"`
	TokenType        string  `json:"token_type"`
	ExpiresIn        float64 `json:"expires_in"`
	Error            string  `json:"error"`
	ErrorDescription string  `json:"error_description"`
	User             *struct {
		UserID      int64  `json:"userId"`
		CountryCode string `json:"countryCode"`
	} `json:"user"`
}

// oauthError is a non-2xx token endpoint answer.
type oauthError struct {
	Status      int
	Code        string
	Description string
}

func (e *oauthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("oauth: HTTP %d %s: %s", e.Status, e.Code, e.Description)
	}
	return fmt.Sprintf("oauth: HTTP %d %s", e.Status, e.Code)
}

func (o *oauth) postForm(ctx context.Context, path string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.Base, "/")+path, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.httpClient().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// startDevice asks for a device code and the user code the human types in at
// the verification URL.
func (o *oauth) startDevice(ctx context.Context) (*deviceAuth, error) {
	if o.ClientID == "" {
		return nil, errors.New("TIDAL_CLIENT_ID is not set")
	}
	form := url.Values{"client_id": {o.ClientID}, "scope": {o.scope()}}
	status, body, err := o.postForm(ctx, "/device_authorization", form)
	if err != nil {
		return nil, fmt.Errorf("device_authorization: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("device_authorization: HTTP %d: %s", status, snippet(body))
	}
	var da deviceAuth
	if err := json.Unmarshal(body, &da); err != nil {
		return nil, fmt.Errorf("device_authorization: decode: %w", err)
	}
	if da.DeviceCode == "" || da.UserCode == "" {
		return nil, fmt.Errorf("device_authorization: response without deviceCode/userCode: %s", snippet(body))
	}
	return &da, nil
}

// pollDevice polls the token endpoint until the user has approved the code,
// the code expires, or ctx ends. authorization_pending keeps polling at the
// server's interval, slow_down adds five seconds to it (RFC 8628 3.5), and
// expired_token / access_denied / anything else stops.
func (o *oauth) pollDevice(ctx context.Context, da *deviceAuth) (*tokenSet, error) {
	interval := time.Duration(da.Interval * float64(time.Second))
	if interval < time.Second {
		interval = time.Second
	}
	expires := da.ExpiresIn
	if expires <= 0 {
		expires = 300
	}
	deadline := o.now().Add(time.Duration(expires * float64(time.Second)))
	form := url.Values{
		"client_id":   {o.ClientID},
		"device_code": {da.DeviceCode},
		"grant_type":  {deviceGrantType},
		"scope":       {o.scope()},
	}
	if o.ClientSecret != "" {
		form.Set("client_secret", o.ClientSecret)
	}
	for attempt := 1; ; attempt++ {
		ts, err := o.tokenRequest(ctx, form)
		if err == nil {
			slog.Debug("device flow approved", "attempts", attempt)
			return ts, nil
		}
		var oe *oauthError
		if !errors.As(err, &oe) {
			return nil, err
		}
		switch oe.Code {
		case "authorization_pending":
			slog.Debug("device flow pending", "attempt", attempt, "next", interval)
		case "slow_down":
			interval += 5 * time.Second
			slog.Debug("device flow slow_down", "attempt", attempt, "next", interval)
		default:
			return nil, err
		}
		if !o.now().Add(interval).Before(deadline) {
			return nil, errors.New("device code expired before it was approved")
		}
		if err := o.sleep(ctx, interval); err != nil {
			return nil, err
		}
	}
}

// refresh trades a refresh token for a new access token. Tidal does not
// always rotate the refresh token, so the old one is kept when the answer
// omits it. The user id and country code carry over.
func (o *oauth) refresh(ctx context.Context, old *tokenSet) (*tokenSet, error) {
	if old == nil || old.RefreshToken == "" {
		return nil, errors.New("no refresh token stored, run login first")
	}
	if o.ClientID == "" {
		return nil, errors.New("TIDAL_CLIENT_ID is not set")
	}
	form := url.Values{
		"client_id":     {o.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {old.RefreshToken},
	}
	if o.ClientSecret != "" {
		form.Set("client_secret", o.ClientSecret)
	}
	ts, err := o.tokenRequest(ctx, form)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	if ts.RefreshToken == "" {
		ts.RefreshToken = old.RefreshToken
	}
	if ts.UserID == 0 {
		ts.UserID = old.UserID
	}
	if ts.CountryCode == "" {
		ts.CountryCode = old.CountryCode
	}
	return ts, nil
}

func (o *oauth) tokenRequest(ctx context.Context, form url.Values) (*tokenSet, error) {
	status, body, err := o.postForm(ctx, "/token", form)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	var tr tokenResponse
	if jerr := json.Unmarshal(body, &tr); jerr != nil && status == http.StatusOK {
		return nil, fmt.Errorf("token: decode: %w", jerr)
	}
	if status != http.StatusOK {
		code := tr.Error
		if code == "" {
			code = "unknown_error"
		}
		return nil, &oauthError{Status: status, Code: code, Description: tr.ErrorDescription}
	}
	if tr.AccessToken == "" {
		return nil, errors.New("token: 200 without access_token")
	}
	ts := &tokenSet{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    tr.TokenType,
		ExpiresAt:    o.now().Add(time.Duration(tr.ExpiresIn * float64(time.Second))),
	}
	if tr.User != nil {
		ts.UserID = tr.User.UserID
		ts.CountryCode = tr.User.CountryCode
	}
	return ts, nil
}

// defaultTokenPath is <UserConfigDir>/str-tidalspike/tokens.json, overridable
// with TIDALSPIKE_TOKEN_FILE.
func defaultTokenPath() (string, error) {
	if p := os.Getenv("TIDALSPIKE_TOKEN_FILE"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "str-tidalspike", "tokens.json"), nil
}

func loadTokens(path string) (*tokenSet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no token file at %s, run login first", path)
		}
		return nil, err
	}
	var ts tokenSet
	if err := json.Unmarshal(b, &ts); err != nil {
		return nil, fmt.Errorf("token file %s: %w", path, err)
	}
	return &ts, nil
}

// saveTokens writes the token file with 0600 (directory 0700) via a temp file
// and rename, so an interrupted write never leaves half a token behind.
func saveTokens(path string, ts *tokenSet) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tokens-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		// Windows ignores most mode bits; the error is not fatal there.
		slog.Debug("chmod token temp file", "err", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// snippet trims a response body for an error message.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}
