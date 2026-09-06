package groupkeys

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
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
	if err := c.Form(ctx, tpl); err != nil {
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
