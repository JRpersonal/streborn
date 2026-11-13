package main

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

// #1898: the network was slow right after the app started, the lookup of a
// station's own logo timed out, and the key kept the lower-ranked icon the
// lookup fell back to for the whole app run, while the phone page showed the
// station's logo. A lookup that got no reply is asked again after logoSoftTTL;
// a definite answer is still cached for the run.
func TestResolveStationLogoUnansweredNotCachedForever(t *testing.T) {
	const fav = "https://www.station.example/logo.png"
	const ddg = "https://icons.duckduckgo.com/ip3/station.example.ico"
	down := true
	calls := 0
	a := &App{logger: slog.New(slog.DiscardHandler), httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if down && r.URL.String() == fav {
			return nil, errors.New("context deadline exceeded")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/png"}},
			Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}}

	if got := a.ResolveStationLogo(fav, "", []string{"station.example"}); got != ddg {
		t.Fatalf("while the logo times out: got %q, want the icon-service fallback", got)
	}
	if got := a.ResolveStationLogo(fav, "", []string{"station.example"}); got != ddg || calls != 2 {
		t.Fatalf("inside the soft window: got %q after %d lookups, want the fallback after 2", got, calls)
	}

	// The window has passed and the station's server answers now.
	down = false
	a.logoMu.Lock()
	for k, s := range a.logoSoft {
		s.until = time.Now().Add(-time.Second)
		a.logoSoft[k] = s
	}
	a.logoMu.Unlock()
	if got := a.ResolveStationLogo(fav, "", []string{"station.example"}); got != fav {
		t.Fatalf("after the soft window: got %q, want the station's own logo", got)
	}
	// A definite answer is cached for the run.
	before := calls
	if got := a.ResolveStationLogo(fav, "", []string{"station.example"}); got != fav || calls != before {
		t.Errorf("definite answer not cached: got %q, %d new lookups", got, calls-before)
	}
}

// A real "no logo" (every host answered, none with an icon) stays cached for
// the run, so the letter tile does not cost a lookup on every redraw.
func TestResolveStationLogoAnsweredMissCached(t *testing.T) {
	calls := 0
	a := &App{logger: slog.New(slog.DiscardHandler), httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{"Content-Type": []string{"image/png"}},
			Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}}
	for i := 0; i < 2; i++ {
		if got := a.ResolveStationLogo("https://www.station.example/logo.png", "", []string{"station.example"}); got != "" {
			t.Fatalf("got %q, want no logo", got)
		}
	}
	if calls != 2 {
		t.Errorf("%d lookups, want 2 (favicon + icon service, once)", calls)
	}
}
