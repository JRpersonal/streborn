package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/upnp"
)

// #1314: a recall that ran out of retries left an ST10 in INVALID_SOURCE, a
// solid amber light with nothing playing. settleExhaustedRecall now puts the
// speaker into a clean state, and never wakes a sleeping one or overrides a
// source somebody picked.
func TestSettleExhaustedRecall(t *testing.T) {
	cases := []struct {
		name       string
		source     string
		superseded bool // a newer play landed after the press
		userStop   bool // the user stopped / powered off after the press
		want       string
		wantSOAP   bool // a UPnP Stop reached the speaker
		wantSleep  bool // a standby call reached the speaker
	}{
		{name: "amber no-source goes back to standby", source: "INVALID_SOURCE", want: "standby", wantSleep: true},
		{name: "stuck on STR's transport is stopped", source: "UPNP", want: "stop", wantSOAP: true},
		{name: "a sleeping speaker is never touched", source: "STANDBY"},
		{name: "an unreadable source is left alone", source: ""},
		{name: "a source the user picked is left alone", source: "BLUETOOTH"},
		{name: "a native station is left alone", source: nativeRadioSource},
		{name: "a newer play owns the speaker", source: "INVALID_SOURCE", superseded: true},
		{name: "a user stop since the press is respected", source: "UPNP", userStop: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			soapStop, standby := false, false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.Header.Get("SOAPACTION"), "#Stop") {
					mu.Lock()
					soapStop = true
					mu.Unlock()
				}
				_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope><s:Body/></s:Envelope>`))
			}))
			t.Cleanup(srv.Close)
			prev := exhaustedStandbySend
			exhaustedStandbySend = func(context.Context, string) error {
				mu.Lock()
				standby = true
				mu.Unlock()
				return nil
			}
			t.Cleanup(func() { exhaustedStandbySend = prev })

			src := c.source
			s := &Server{
				logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
				boxHost:      "192.0.2.1",
				renderer:     &upnp.Renderer{ControlURL: srv.URL, Client: srv.Client()},
				boxNowReadFn: scriptNowPlaying(src, "", ""),
			}
			started := time.Now().Add(-30 * time.Second)
			const gen = 7
			s.play.recallGen = gen
			if c.superseded {
				s.play.recallGen = gen + 1
			}
			if c.userStop {
				s.NoteUserStop()
			}

			if got := s.settleExhaustedRecall(gen, started); got != c.want {
				t.Errorf("settleExhaustedRecall = %q, want %q", got, c.want)
			}
			mu.Lock()
			defer mu.Unlock()
			if soapStop != c.wantSOAP {
				t.Errorf("UPnP Stop sent = %v, want %v", soapStop, c.wantSOAP)
			}
			if standby != c.wantSleep {
				t.Errorf("standby sent = %v, want %v", standby, c.wantSleep)
			}
		})
	}
}

// The clean-up is a deliberate stop: the source drop it causes must not be
// read as a spontaneous firmware drop and re-pushed by the #419 recovery.
func TestSettleExhaustedRecallCountsAsADeliberateStop(t *testing.T) {
	prev := exhaustedStandbySend
	exhaustedStandbySend = func(context.Context, string) error { return nil }
	t.Cleanup(func() { exhaustedStandbySend = prev })
	s := &Server{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		boxHost:      "192.0.2.1",
		boxNowReadFn: scriptNowPlaying("INVALID_SOURCE", "", ""),
	}
	if got := s.settleExhaustedRecall(0, time.Now().Add(-time.Minute)); got != "standby" {
		t.Fatalf("settleExhaustedRecall = %q, want standby", got)
	}
	if !s.userStoppedRecently() {
		t.Error("the clean-up standby was not recorded as a deliberate stop")
	}
}
