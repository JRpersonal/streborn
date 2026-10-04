package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The form tells the master which members the app just woke, so it does not
// carry their own power-on resume into every room (fleet run 2026-10-04).
func TestWakeBoxRecordsASpeakerItWokeAndFormZoneSendsIt(t *testing.T) {
	woke := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"awake":true,"woke":true}`))
	}))
	defer woke.Close()
	awake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"awake":true}`))
	}))
	defer awake.Close()

	a := newTestApp()
	if err := a.WakeBox("127.0.0.1", listenPort(t, woke)); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	members := []ZoneMember{{DeviceID: "a", IP: "127.0.0.1"}, {DeviceID: "b", IP: "192.0.2.3"}}
	got := a.groupWakes.recent(members, now)
	if len(got) != 1 || got[0] != "127.0.0.1" {
		t.Fatalf("recent = %v, want the woken speaker only", got)
	}
	if late := a.groupWakes.recent(members, now.Add(groupWakeWindow)); len(late) != 0 {
		t.Fatalf("a wake older than the window still counted: %v", late)
	}

	// An agent that does not say it woke the speaker (already awake, or an old
	// agent) is not recorded.
	b := newTestApp()
	if err := b.WakeBox("127.0.0.1", listenPort(t, awake)); err != nil {
		t.Fatal(err)
	}
	if got := b.groupWakes.recent(members, time.Now()); len(got) != 0 {
		t.Fatalf("recorded a speaker that was not woken: %v", got)
	}

	raw, err := json.Marshal(zoneFormPayload(ZoneSpec{Master: ZoneMember{IP: "192.0.2.1"}, Slaves: members}, nil))
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	_ = json.Unmarshal(raw, &back)
	if _, ok := back["wokenFromStandby"]; ok {
		t.Fatalf("an empty list must be omitted so an old agent sees the old body: %s", raw)
	}
	if back["master"] == nil || back["slaves"] == nil {
		t.Fatalf("the spec fields must stay at the top level: %s", raw)
	}
	raw, _ = json.Marshal(zoneFormPayload(ZoneSpec{Slaves: members}, []string{"127.0.0.1"}))
	_ = json.Unmarshal(raw, &back)
	if l, _ := back["wokenFromStandby"].([]any); len(l) != 1 {
		t.Fatalf("wokenFromStandby missing from %s", raw)
	}
}

// A wake the app stops waiting for (the agent is still walking the speaker out
// of standby) keeps the speaker recorded: dropping it is what let a group formed
// out of idle speakers play (fleet run 2026-10-04).
func TestWakeBoxKeepsTheRecordWhenTheWakeDoesNotAnswer(t *testing.T) {
	a := newTestApp()
	// Nothing listens on this port, so the call fails at the transport.
	if err := a.WakeBox("127.0.0.1", 1); err == nil {
		t.Fatal("expected a transport error")
	}
	members := []ZoneMember{{DeviceID: "a", IP: "127.0.0.1"}}
	if got := a.groupWakes.recent(members, time.Now()); len(got) != 1 {
		t.Fatalf("a wake without an answer must still count as woken, got %v", got)
	}
}
