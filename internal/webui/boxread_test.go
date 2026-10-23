package webui

import (
	"context"
	"errors"
	"html"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// nowPlayingXML builds a minimal now_playing document of the shape the
// firmware emits, for scripting the read seam. Empty fields are left out the
// way the firmware leaves them out: a standby document has no playStatus, a
// torn-down source no location.
func nowPlayingXML(source, playStatus, location, itemName string) string {
	doc := `<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="device-id-here" source="` + html.EscapeString(source) + `">`
	if source != "" {
		doc += `<ContentItem source="` + html.EscapeString(source) + `"`
		if location != "" {
			doc += ` location="` + html.EscapeString(location) + `"`
		}
		doc += ` isPresetable="false">`
		if itemName != "" {
			doc += `<itemName>` + html.EscapeString(itemName) + `</itemName>`
		}
		doc += `</ContentItem>`
	}
	if playStatus != "" {
		doc += `<playStatus>` + playStatus + `</playStatus>`
	}
	return doc + `</nowPlaying>`
}

// scriptNowPlaying scripts Server.boxNowReadFn with a fixed state. An empty
// source means an unreadable speaker (the read fails), which is what every
// old seam's "" stood for.
func scriptNowPlaying(source, playStatus, location string) func(context.Context) (*boxapi.NowPlaying, error) {
	if source == "" {
		return scriptNowPlayingUnreadable()
	}
	return scriptNowPlayingDoc(nowPlayingXML(source, playStatus, location, ""))
}

// scriptNowPlayingDoc scripts the seam with a literal now_playing body.
func scriptNowPlayingDoc(body string) func(context.Context) (*boxapi.NowPlaying, error) {
	return func(context.Context) (*boxapi.NowPlaying, error) {
		return boxapi.ParseNowPlaying([]byte(body)), nil
	}
}

// scriptNowPlayingUnreadable scripts a speaker that does not answer.
func scriptNowPlayingUnreadable() func(context.Context) (*boxapi.NowPlaying, error) {
	return func(context.Context) (*boxapi.NowPlaying, error) {
		return nil, errors.New("connection refused")
	}
}

// A fresh read that fails is recorded as a :8090 outage; a cached body
// younger than the TTL is served by the cached read without a trip to the
// speaker, and an older one is not.
func TestReadBoxNowFeedsOutageTrackerAndCache(t *testing.T) {
	s := &Server{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// A host:port is not a bare host, so the read fails instantly.
		boxHost: "127.0.0.1:1",
	}
	if _, err := s.readBoxNowWithin(time.Second); err == nil {
		t.Fatal("read of an unreachable speaker succeeded")
	}
	s.statusMu.Lock()
	down := !s.boseappDownSince.IsZero()
	s.statusMu.Unlock()
	if !down {
		t.Error("a failed read did not open a :8090 outage")
	}

	body := []byte(nowPlayingXML("UPNP", "PLAY_STATE", "http://127.0.0.1:8888/stream/2", "Radio"))
	s.cacheStatusBody(body)
	np, err := s.readBoxNowCached(context.Background())
	if err != nil {
		t.Fatalf("cached read: %v", err)
	}
	if np.Source != "UPNP" || !np.IsPlaying() || string(np.Raw) != string(body) {
		t.Errorf("cached read = %s", np)
	}
	s.statusMu.Lock()
	s.statusAt = time.Now().Add(-statusCacheTTL - time.Second)
	s.statusMu.Unlock()
	if _, err := s.readBoxNowCached(context.Background()); err == nil {
		t.Error("an expired cache was served instead of a fresh (failing) read")
	}
	s.statusMu.Lock()
	s.statusCode = http.StatusBadGateway
	s.statusAt = time.Now()
	s.statusMu.Unlock()
	if _, ok := s.cachedStatusBody(); ok {
		t.Error("a non-200 body was served from the cache")
	}

	// No speaker configured: the error is the sentinel, not a dial.
	s.boxHost = ""
	if _, err := s.readBoxNowWithin(time.Second); !errors.Is(err, errNoBoxHost) {
		t.Errorf("no box host: err = %v", err)
	}
	if snap := s.readBoxSnapshot(context.Background()); snap.Source != "" {
		t.Errorf("unreadable snapshot = %+v, want zero", snap)
	}
}
