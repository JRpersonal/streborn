package webui

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JRpersonal/streborn/internal/upnp"
)

// wrongStateFault is what the firmware answered a user's play on 2026-08-05
// (DLF Nova, v0.9.34): HTTP 500 carrying UPnP 501, but a DIFFERENT description
// than the grouped-follower refusal that shares the same code.
const wrongStateFault = `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>501</errorCode><errorDescription>Action request came in wrong state</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`

func TestIsWrongStateRejection(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"field fault", errors.New("SetURI: soap SetAVTransportURI status 500: " + wrongStateFault), true},
		{"gabbo spelling", errors.New("UpnpRcvdContentItemInWrongState"), true},
		{"case-insensitive", errors.New("ACTION REQUEST CAME IN WRONG STATE"), true},
		// The two 501s must stay apart: a grouped follower needs the 409 that
		// tells the user to drive the lead speaker, NOT a transport wipe.
		{"grouped follower is not this", errors.New("SetURI: soap status 500: " + groupedFault), false},
		{"unrelated 402", errors.New("soap status 500: <errorCode>402</errorCode> No URI supplied"), false},
		{"plain network error", errors.New("connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWrongStateRejection(tc.err); got != tc.want {
				t.Errorf("isWrongStateRejection(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// wrongStateBox is a fake box that fails the first SetAVTransportURI with the
// wrong-state fault and accepts everything afterwards, which is exactly what
// the reporter saw: first attempt silent, second attempt played.
type wrongStateBox struct {
	mu      sync.Mutex
	actions []string
	bodies  []string
	setURIs int
}

func (b *wrongStateBox) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		action := r.Header.Get("SOAPACTION")
		switch {
		case strings.Contains(action, "#SetAVTransportURI"):
			action = "SetAVTransportURI"
		case strings.Contains(action, "#Play"):
			action = "Play"
		case strings.Contains(action, "#Stop"):
			action = "Stop"
		}
		b.mu.Lock()
		b.actions = append(b.actions, action)
		b.bodies = append(b.bodies, string(raw))
		first := false
		if action == "SetAVTransportURI" {
			b.setURIs++
			first = b.setURIs == 1
		}
		b.mu.Unlock()
		if first {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(wrongStateFault))
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope><s:Body/></s:Envelope>`))
	}
}

func (b *wrongStateBox) seen() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.actions...)
}

// A box that refuses the first push because its transport is in the wrong state
// must be emptied and pushed again, so the USER's single click plays. Before
// this, the raw SOAP fault went back to the app and only a second manual
// attempt worked.
func TestWrongStatePlayRetriesFromCleanSlate(t *testing.T) {
	rec := &wrongStateBox{}
	box := httptest.NewServer(rec.handler())
	t.Cleanup(box.Close)
	s := &Server{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		queue:    newPlayQueue(),
		renderer: &upnp.Renderer{ControlURL: box.URL, Client: box.Client()},
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/play",
		strings.NewReader(`{"url":"http://stream.example/dlfnova","title":"DLF Nova"}`))
	s.handlePlay(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	if resp["status"] != "playing" {
		t.Errorf(`status = %q, want "playing"`, resp["status"])
	}

	// The repair itself: a Stop and an EMPTY SetAVTransportURI have to sit
	// between the refused push and the retry. Asserting only "it returned 200"
	// would still pass if the retry blindly replayed the same URI, which is the
	// loop this exists to prevent.
	got := rec.seen()
	if len(got) < 4 {
		t.Fatalf("box saw %v, want at least SetAVTransportURI, Stop, SetAVTransportURI, Play", got)
	}
	if got[0] != "SetAVTransportURI" {
		t.Errorf("first action = %q, want SetAVTransportURI", got[0])
	}
	var sawStop, sawClear bool
	rec.mu.Lock()
	for i, a := range rec.actions {
		if i == 0 {
			continue // the refused push
		}
		if a == "Stop" {
			sawStop = true
		}
		if a == "SetAVTransportURI" && strings.Contains(rec.bodies[i], "<CurrentURI></CurrentURI>") {
			sawClear = true
		}
	}
	rec.mu.Unlock()
	if !sawStop {
		t.Errorf("no Stop after the refusal; actions were %v", got)
	}
	if !sawClear {
		t.Errorf("no empty SetAVTransportURI (ClearURI) after the refusal; actions were %v", got)
	}
	if got[len(got)-1] != "Play" {
		t.Errorf("last action = %q, want Play", got[len(got)-1])
	}
}

// A grouped follower must NOT be dragged through the transport wipe: it still
// answers the structured 409 so the app can point at the lead speaker.
func TestGroupedRejectionSkipsTheWrongStateRepair(t *testing.T) {
	var calls int
	var mu sync.Mutex
	box := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(groupedFault))
	}))
	t.Cleanup(box.Close)
	s := &Server{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		queue:    newPlayQueue(),
		renderer: &upnp.Renderer{ControlURL: box.URL, Client: box.Client()},
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/play",
		strings.NewReader(`{"url":"http://stream.example/relax","title":"Test Station"}`))
	s.handlePlay(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("box saw %d calls, want exactly 1 (no Stop/ClearURI/retry on a grouped follower)", calls)
	}
}
