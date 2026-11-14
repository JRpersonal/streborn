package main

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// #1898, second bundle: on one PC the station's own logo failed the app's
// check on every restart while the phone page drew it, and the key wore the
// stream host's icon. A logo the check could not confirm is now handed to the
// window first, with the fallback behind it; only a definite "not there"
// drops it.
func TestResolveStationLogoUnconfirmedLogoTriedFirst(t *testing.T) {
	const fav = "https://www.station.example/logo.png"
	const ddg = "https://icons.duckduckgo.com/ip3/stream.example.ico"
	for _, c := range []struct {
		name   string
		status int
		ctype  string
		err    error
		want   string
	}{
		{"refused by the host", http.StatusForbidden, "text/html", nil, fav + "|" + ddg},
		{"server error", http.StatusInternalServerError, "", nil, fav + "|" + ddg},
		{"no answer", 0, "", errors.New("tls: handshake failure"), fav + "|" + ddg},
		{"confirmed image", http.StatusOK, "image/png", nil, fav},
		{"not found", http.StatusNotFound, "text/html", nil, ddg},
		{"gone", http.StatusGone, "", nil, ddg},
		{"a page, not an image", http.StatusOK, "text/html", nil, ddg},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := &App{logger: slog.New(slog.DiscardHandler), httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == fav {
					if c.err != nil {
						return nil, c.err
					}
					return &http.Response{StatusCode: c.status, Header: http.Header{"Content-Type": []string{c.ctype}},
						Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/x-icon"}},
					Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			})}}
			if got := a.ResolveStationLogo(fav, "", []string{"stream.example"}); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// With no fallback anywhere, an unconfirmed logo is the whole answer: the
// window tries it and ends on the monogram.
func TestResolveStationLogoUnconfirmedLogoWithoutFallback(t *testing.T) {
	const fav = "https://cover.example/playlist.jpg"
	a := &App{logger: slog.New(slog.DiscardHandler), httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}}
	if got := a.ResolveStationLogo(fav, "", nil); got != fav {
		t.Fatalf("got %q, want the cover itself", got)
	}
}
