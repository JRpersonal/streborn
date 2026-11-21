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
	"strconv"
	"strings"
	"time"
)

// defaultAPIBase is the unofficial v1 API the Tidal apps (and python-tidal)
// talk to. The official developer API has no playback route, see the README.
const defaultAPIBase = "https://api.tidal.com/v1"

// defaultClientVersion goes out as x-tidal-client-version, like the Android
// app python-tidal impersonates. Override with TIDAL_CLIENT_VERSION if Tidal
// starts rejecting it.
const defaultClientVersion = "2025.7.16"

// refreshMargin refreshes the access token this long before it expires, so a
// request never races the expiry.
const refreshMargin = 60 * time.Second

// apiClient wraps the v1 API with automatic token refresh. A refreshed token
// set is written back to TokenPath (empty TokenPath = keep it in memory).
type apiClient struct {
	Base          string
	HTTP          *http.Client
	OAuth         *oauth
	Tokens        *tokenSet
	TokenPath     string
	ClientVersion string
	Now           func() time.Time
}

func (c *apiClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *apiClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// ensureFresh refreshes the access token when it is expired or about to be.
func (c *apiClient) ensureFresh(ctx context.Context) error {
	if c.Tokens == nil {
		return errors.New("not logged in")
	}
	if !c.Tokens.ExpiresAt.IsZero() && c.now().Add(refreshMargin).Before(c.Tokens.ExpiresAt) {
		return nil
	}
	return c.forceRefresh(ctx)
}

func (c *apiClient) forceRefresh(ctx context.Context) error {
	ts, err := c.OAuth.refresh(ctx, c.Tokens)
	if err != nil {
		return err
	}
	c.Tokens = ts
	slog.Info("access token refreshed", "expires_at", ts.ExpiresAt.Format(time.RFC3339))
	if c.TokenPath != "" {
		if err := saveTokens(c.TokenPath, ts); err != nil {
			return fmt.Errorf("save refreshed tokens: %w", err)
		}
	}
	return nil
}

// apiError is a non-2xx API answer. Tidal's v1 error body carries a status,
// a subStatus and a userMessage; the subStatus is what tells "not available
// in this quality" apart from "token invalid".
type apiError struct {
	Status      int
	SubStatus   int
	UserMessage string
	Body        string
}

func (e *apiError) Error() string {
	if e.UserMessage != "" {
		return fmt.Sprintf("tidal api: HTTP %d (subStatus %d): %s", e.Status, e.SubStatus, e.UserMessage)
	}
	return fmt.Sprintf("tidal api: HTTP %d: %s", e.Status, e.Body)
}

// get issues GET <base>/<path> with countryCode filled in, decodes JSON into
// out, and retries once after a refresh on 401.
func (c *apiClient) get(ctx context.Context, path string, q url.Values, out any) error {
	if err := c.ensureFresh(ctx); err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		err := c.getOnce(ctx, path, q, out)
		var ae *apiError
		if attempt == 0 && errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
			slog.Info("API answered 401, refreshing token once", "path", path)
			if rerr := c.forceRefresh(ctx); rerr != nil {
				return fmt.Errorf("%w (refresh after 401 failed: %w)", err, rerr)
			}
			continue
		}
		return err
	}
	return nil
}

func (c *apiClient) getOnce(ctx context.Context, path string, q url.Values, out any) error {
	vals := url.Values{}
	for k, v := range q {
		vals[k] = v
	}
	if vals.Get("countryCode") == "" && c.Tokens.CountryCode != "" {
		vals.Set("countryCode", c.Tokens.CountryCode)
	}
	u := strings.TrimRight(c.Base, "/") + "/" + strings.TrimLeft(path, "/")
	if len(vals) > 0 {
		u += "?" + vals.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.Tokens.authHeader())
	cv := c.ClientVersion
	if cv == "" {
		cv = defaultClientVersion
	}
	req.Header.Set("x-tidal-client-version", cv)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	slog.Debug("api GET", "path", path, "status", resp.StatusCode, "bytes", len(body))
	if resp.StatusCode != http.StatusOK {
		ae := &apiError{Status: resp.StatusCode, Body: snippet(body)}
		var eb struct {
			SubStatus   int    `json:"subStatus"`
			UserMessage string `json:"userMessage"`
		}
		if json.Unmarshal(body, &eb) == nil {
			ae.SubStatus = eb.SubStatus
			ae.UserMessage = eb.UserMessage
		}
		return ae
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// sessionInfo is GET /sessions: who the token belongs to.
type sessionInfo struct {
	SessionID   string `json:"sessionId"`
	UserID      int64  `json:"userId"`
	CountryCode string `json:"countryCode"`
}

func (c *apiClient) session(ctx context.Context) (*sessionInfo, error) {
	var s sessionInfo
	if err := c.get(ctx, "sessions", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// subscription is a loose read of GET users/<id>/subscription. Only the two
// fields that decide what quality we may ask for are kept.
type subscription struct {
	Status              string `json:"status"`
	HighestSoundQuality string `json:"highestSoundQuality"`
	Subscription        struct {
		Type string `json:"type"`
	} `json:"subscription"`
}

func (c *apiClient) subscription(ctx context.Context) (*subscription, error) {
	var s subscription
	if err := c.get(ctx, "users/"+strconv.FormatInt(c.Tokens.UserID, 10)+"/subscription", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// playlist is one row of users/<id>/playlists.
type playlist struct {
	UUID           string `json:"uuid"`
	Title          string `json:"title"`
	NumberOfTracks int    `json:"numberOfTracks"`
}

// playlists pages through users/<id>/playlists (50 per page).
func (c *apiClient) playlists(ctx context.Context) ([]playlist, error) {
	const page = 50
	var all []playlist
	for offset := 0; ; offset += page {
		var resp struct {
			TotalNumberOfItems int        `json:"totalNumberOfItems"`
			Items              []playlist `json:"items"`
		}
		q := url.Values{"limit": {strconv.Itoa(page)}, "offset": {strconv.Itoa(offset)}}
		if err := c.get(ctx, "users/"+strconv.FormatInt(c.Tokens.UserID, 10)+"/playlists", q, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Items...)
		if len(resp.Items) == 0 || len(all) >= resp.TotalNumberOfItems {
			return all, nil
		}
	}
}

// trackMeta is the subset of GET tracks/<id> the DIDL needs.
type trackMeta struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Duration int    `json:"duration"`
	Artist   struct {
		Name string `json:"name"`
	} `json:"artist"`
	Album struct {
		Title string `json:"title"`
	} `json:"album"`
	AudioQuality string `json:"audioQuality"`
}

func (c *apiClient) track(ctx context.Context, id string) (*trackMeta, error) {
	var t trackMeta
	if err := c.get(ctx, "tracks/"+url.PathEscape(id), nil, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// playbackInfo calls tracks/<id>/playbackinfopostpaywall, the endpoint the
// apps use to get a stream manifest.
func (c *apiClient) playbackInfo(ctx context.Context, id, quality string) (*playbackInfo, error) {
	q := url.Values{
		"playbackmode":      {"STREAM"},
		"audioquality":      {quality},
		"assetpresentation": {"FULL"},
	}
	var pi playbackInfo
	if err := c.get(ctx, "tracks/"+url.PathEscape(id)+"/playbackinfopostpaywall", q, &pi); err != nil {
		return nil, err
	}
	return &pi, nil
}
