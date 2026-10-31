package groupkeys

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestOkOrError: the zone endpoints' JSON verdicts are honoured, and a body
// that is not JSON (an older agent's index page) never passes as success.
func TestOkOrError(t *testing.T) {
	if err := okOrError([]byte(`{"ok":true,"mode":"native"}`)); err != nil {
		t.Fatalf("ok:true must pass, got %v", err)
	}
	if err := okOrError([]byte(`{"mode":"native"}`)); err != nil {
		t.Fatalf("an answer without ok must pass, got %v", err)
	}
	if err := okOrError([]byte(`{"ok":false,"error":"setZone: boom"}`)); err == nil || err.Error() != "setZone: boom" {
		t.Fatalf("ok:false must carry the reason, got %v", err)
	}
	if err := okOrError([]byte(`<!doctype html><html>`)); err == nil {
		t.Fatal("an HTML body must not pass as success")
	}
}

// TestHTTPClientAgainstAgent drives the HTTP client against a fake agent: the
// zone read, the form body, the dissolve, the idle read and the resume all
// reach the endpoints the desktop app uses, on the port that answers.
func TestHTTPClientAgainstAgent(t *testing.T) {
	var formed formBody
	mux := http.NewServeMux()
	mux.HandleFunc("/api/box/zone", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"master":"AAAA","senderIP":"192.0.2.10","members":[{"deviceID":"BBBB","ip":"192.0.2.11"}]}`))
		case http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&formed)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<nowPlaying source="STANDBY"><playStatus>STOP_STATE</playStatus></nowPlaying>`))
	})
	var powered bool
	mux.HandleFunc("/api/box/power", func(w http.ResponseWriter, r *http.Request) {
		powered = true
		_, _ = w.Write([]byte(`{"on":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	portN, err := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	if err != nil {
		t.Fatal(err)
	}

	c := NewHTTPClient(HTTPOptions{
		// The test server is the "main speaker": point every attempt at it.
		PortHint: func(string) int { return portN },
	})
	master := Member{DeviceID: "AAAA", IP: "127.0.0.1"}
	ctx := context.Background()
	lz, err := c.LiveZone(ctx, master)
	if err != nil {
		t.Fatal(err)
	}
	if lz.Master != "AAAA" || len(lz.Members) != 1 || lz.Members[0].IP != "192.0.2.11" {
		t.Fatalf("live zone %+v", lz)
	}
	tpl := Template{Name: "Evening", Master: master, Members: []Member{{DeviceID: "BBBB", IP: "192.0.2.11"}}, Permanent: true}
	if _, err := c.Form(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	if formed.Name != "Evening" || !formed.Permanent || formed.Mode != "native" || len(formed.Slaves) != 1 || formed.Master.IP != "127.0.0.1" {
		t.Fatalf("form body %+v", formed)
	}
	if err := c.Dissolve(ctx, master); err != nil {
		t.Fatal(err)
	}
	idle, err := c.Idle(ctx, master)
	if err != nil || !idle {
		t.Fatalf("idle=%v err=%v", idle, err)
	}
	if err := c.PlayLast(ctx, master); err != nil || !powered {
		t.Fatalf("play last: err=%v powered=%v", err, powered)
	}
}

// testClientFor points a client at srv as the main speaker.
func testClientFor(t *testing.T, srv *httptest.Server) (MasterClient, Member) {
	t.Helper()
	portN, err := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	if err != nil {
		t.Fatal(err)
	}
	return NewHTTPClient(HTTPOptions{PortHint: func(string) int { return portN }}), Member{DeviceID: "AAAA", IP: "127.0.0.1"}
}

// A zone read the agent answers with 502 (the Bose web server behind it timed
// out) is tried once more before the press gives up.
func TestLiveZoneRetriesAGatewayError(t *testing.T) {
	old := liveZoneRetryDelay
	liveZoneRetryDelay = time.Millisecond
	t.Cleanup(func() { liveZoneRetryDelay = old })
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, `Get "http://127.0.0.1:8090/getZone": context deadline exceeded`, http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"master":"","members":[]}`))
	}))
	defer srv.Close()
	c, master := testClientFor(t, srv)
	if _, err := c.LiveZone(context.Background(), master); err != nil {
		t.Fatalf("retry did not recover: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

// A stale now-playing (served from cache while the firmware does not answer)
// that says the speaker plays is still trusted: read as idle, the press pushed
// the last STR station over Bluetooth, AUX or Spotify. A stale body that does
// not say it plays is unknown, which the caller treats as idle.
func TestIdleOnAStaleStatus(t *testing.T) {
	for _, tc := range []struct {
		body     string
		wantIdle bool
		wantErr  bool
	}{
		{`<nowPlaying source="BLUETOOTH"><playStatus>PLAY_STATE</playStatus></nowPlaying>`, false, false},
		{`<nowPlaying source="STANDBY"></nowPlaying>`, true, true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-STR-Status-Stale", "1")
			_, _ = w.Write([]byte(tc.body))
		}))
		c, master := testClientFor(t, srv)
		idle, err := c.Idle(context.Background(), master)
		srv.Close()
		if idle != tc.wantIdle || (err != nil) != tc.wantErr {
			t.Errorf("%s: idle=%v err=%v, want idle=%v err=%v", tc.body, idle, err, tc.wantIdle, tc.wantErr)
		}
	}
}
