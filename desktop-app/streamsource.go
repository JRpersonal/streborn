package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// StreamSourceStation is the public, provider-neutral station-list item.
// ID is opaque and is only sent back to the stream source.
type StreamSourceStation struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	StreamType string `json:"stream_type,omitempty"`
}

// StreamSourcePlayback is what STR needs to start playback: a LAN-reachable
// URL for the speaker plus optional display text.
type StreamSourcePlayback struct {
	StreamURL string `json:"streamURL"`
	Title     string `json:"title"`
}

// streamSourceBase validates an operator-supplied HTTP API root.
func streamSourceBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("enter a complete http:// or https:// server address")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("the stream server address must use http or https")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("the stream server address must not contain credentials, a query, or a fragment")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func streamSourceURL(base, suffix string) (string, error) {
	root, err := streamSourceBase(base)
	if err != nil {
		return "", err
	}
	return root + suffix, nil
}

// streamSourceGet reads a small JSON reply without following redirects.
func (a *App) streamSourceGet(ctx context.Context, requestURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	client := *a.httpClient
	client.Timeout = 6 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stream server answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return body, nil
}

// StreamSourceStations implements the provider-neutral stream-server contract.
func (a *App) StreamSourceStations(baseURL string) ([]StreamSourceStation, error) {
	endpoint, err := streamSourceURL(baseURL, "/api/stations")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.appCtx(), 7*time.Second)
	defer cancel()
	body, err := a.streamSourceGet(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("read stream stations: %w", err)
	}
	var stations []StreamSourceStation
	if err := json.Unmarshal(body, &stations); err != nil {
		var envelope struct {
			Stations []StreamSourceStation `json:"stations"`
		}
		if envErr := json.Unmarshal(body, &envelope); envErr != nil {
			return nil, fmt.Errorf("stream server returned invalid station data")
		}
		stations = envelope.Stations
	}
	clean := make([]StreamSourceStation, 0, len(stations))
	for _, s := range stations {
		s.ID, s.Name = strings.TrimSpace(s.ID), strings.TrimSpace(s.Name)
		if s.ID == "" || s.Name == "" {
			continue
		}
		s.StreamType = strings.ToLower(strings.TrimSpace(s.StreamType))
		if s.StreamType == "" {
			s.StreamType = "audio"
		}
		clean = append(clean, s)
		if len(clean) == 1000 {
			break
		}
	}
	return clean, nil
}

// StreamSourcePlayback returns playback data without opening a live audio
// session. Direct-audio servers are identified by stream_type in the station
// list; only metadata endpoints are requested here.
func (a *App) StreamSourcePlayback(baseURL, stationID, streamType string) (StreamSourcePlayback, error) {
	stationID = strings.TrimSpace(stationID)
	if stationID == "" {
		return StreamSourcePlayback{}, fmt.Errorf("a station id is required")
	}
	endpoint, err := streamSourceURL(baseURL, "/api/stations/"+url.PathEscape(stationID)+"/stream")
	if err != nil {
		return StreamSourcePlayback{}, err
	}

	switch strings.ToLower(strings.TrimSpace(streamType)) {
	case "", "audio":
		return StreamSourcePlayback{StreamURL: endpoint}, nil
	case "json":
		// Continue below to retrieve the small metadata response.
	default:
		return StreamSourcePlayback{}, fmt.Errorf("stream server declared unsupported stream_type %q", streamType)
	}

	ctx, cancel := context.WithTimeout(a.appCtx(), 7*time.Second)
	defer cancel()
	body, err := a.streamSourceGet(ctx, endpoint)
	if err != nil {
		return StreamSourcePlayback{}, fmt.Errorf("read playback metadata: %w", err)
	}

	var wire struct {
		URL        string `json:"url"`
		StreamURL  string `json:"stream_url"`
		Title      string `json:"title"`
		NowPlaying string `json:"now_playing"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return StreamSourcePlayback{}, fmt.Errorf("stream server returned invalid playback data")
	}

	streamURL := strings.TrimSpace(wire.URL)
	if streamURL == "" {
		streamURL = strings.TrimSpace(wire.StreamURL)
	}

	// Validation for the extracted URL
	u, err := url.ParseRequestURI(streamURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return StreamSourcePlayback{}, fmt.Errorf("stream server did not provide a valid playable URL")
	}

	title := strings.TrimSpace(wire.Title)
	if title == "" {
		title = strings.TrimSpace(wire.NowPlaying)
	}

	return StreamSourcePlayback{StreamURL: streamURL, Title: title}, nil
}
