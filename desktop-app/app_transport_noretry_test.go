package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestDeliveredWriteIsNotRetriedOnTheOtherPort pins the fleet finding of
// 2026-10-04: a stereo pairing POST reached the agent on :8888, the agent spent
// longer than the app's budget on its two-sided check, and the app then sent the
// same POST to :17008, which a SoundTouch 10 does not have. The user saw
// "timed out (also tried :17008)" instead of the agent's own verdict. A write
// that was delivered must stay on its port.
func TestDeliveredWriteIsNotRetriedOnTheOtherPort(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer slow.Close()
	defer close(release)

	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()

	old := altAgentPortFor
	otherPort := listenPort(t, other)
	altAgentPortFor = func(int) int { return otherPort }
	defer func() { altAgentPortFor = old }()

	a := newTestApp()
	port := listenPort(t, slow)
	_, err := a.boxDoTimeout("127.0.0.1", port, http.MethodPost, "/api/box/zone", "application/json", `{"stereo":true}`, 300*time.Millisecond)
	if err == nil {
		t.Fatal("want a timeout error, got nil")
	}
	if n := otherHits.Load(); n != 0 {
		t.Errorf("the POST was repeated on the other port %d time(s); a delivered write must not be retried", n)
	}
	if strings.Contains(err.Error(), "also tried") {
		t.Errorf("error %q summarises other ports; it must be this port's own error", err)
	}
	if p, ok := a.cachedPort("127.0.0.1"); !ok || p != port {
		t.Errorf("cached port = %d (present=%v), want %d: the agent that took the request is the right port", p, ok, port)
	}
}

// TestReadStillFallsBackAfterATimeout keeps the self-heal for reads: a GET that
// times out on one port is harmless to repeat on the other.
func TestReadStillFallsBackAfterATimeout(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer slow.Close()
	defer close(release)

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"v1"}`))
	}))
	defer other.Close()

	old := altAgentPortFor
	otherPort := listenPort(t, other)
	altAgentPortFor = func(int) int { return otherPort }
	defer func() { altAgentPortFor = old }()

	a := newTestApp()
	resp, err := a.boxDoTimeout("127.0.0.1", listenPort(t, slow), http.MethodGet, "/api/agent/version", "", "", 300*time.Millisecond)
	if err != nil {
		t.Fatalf("GET should have fallen back to the other port, got %v", err)
	}
	resp.Body.Close()
}

func TestIdempotentMethod(t *testing.T) {
	for m, want := range map[string]bool{
		http.MethodGet: true, http.MethodHead: true, http.MethodOptions: true,
		http.MethodPost: false, http.MethodPut: false, http.MethodDelete: false,
	} {
		if got := idempotentMethod(m); got != want {
			t.Errorf("idempotentMethod(%s) = %v, want %v", m, got, want)
		}
	}
}

func TestStereoBudgetCoversTheAgentsWorstCase(t *testing.T) {
	// two member wakes (8 s each) + verify (15 s) + undo (10 s) + firmware call
	// and margin; anything at or under the measured 36 s + wake is too short.
	if stereoZoneCallTimeout < 75*time.Second {
		t.Errorf("stereoZoneCallTimeout = %s, too short for the pairing check and rollback", stereoZoneCallTimeout)
	}
}
