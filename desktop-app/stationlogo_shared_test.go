package main

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// #1773: "Radio Eure du Blues" streams from a *.ddns.net host and got
// ddns.net's favicon through DuckDuckGo. ResolveStationLogo never asks the
// icon service about a shared or dynamic-DNS host, its root domain, or an IP
// literal, whatever host list the caller hands it.
func TestResolveStationLogoSkipsSharedHosts(t *testing.T) {
	var asked []string
	a := &App{logger: slog.New(slog.DiscardHandler), httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		asked = append(asked, r.URL.String())
		// Every host "has" an icon: only the filter can keep the provider's out.
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/x-icon"}},
			Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}}

	if got := a.ResolveStationLogo("", "", []string{"radioeure.ddns.net", "ddns.net", "192.0.2.7", "x.dyndns.org"}); got != "" {
		t.Fatalf("got %q, want no logo (the letter tile)", got)
	}
	if len(asked) != 0 {
		t.Fatalf("asked about shared hosts: %v", asked)
	}

	got := a.ResolveStationLogo("", "", []string{"radioeure.ddns.net", "www.radio-eure.example"})
	if got != "https://icons.duckduckgo.com/ip3/www.radio-eure.example.ico" {
		t.Fatalf("got %q, want the station's own host's icon", got)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "www.radio-eure.example") {
		t.Errorf("asked %v", asked)
	}
}
