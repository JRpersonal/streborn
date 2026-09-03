package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JRpersonal/streborn/internal/upnp"
)

// idleBox is a fake speaker whose AVTransport rejects EVERY action with the
// wrong-state fault, which is exactly what a Bose box answers when Pause or
// Stop arrives while nothing is playing. It records what it was asked so the
// tests can prove the request went down the renderer path (transportKeyFallback
// declined) and that no repair actions followed.
type idleBox struct {
	mu      sync.Mutex
	fault   string
	actions []string
}

func (b *idleBox) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		action := r.Header.Get("SOAPACTION")
		if i := strings.LastIndex(action, "#"); i >= 0 {
			action = strings.Trim(action[i+1:], `"`)
		}
		b.mu.Lock()
		b.actions = append(b.actions, action)
		b.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(b.fault))
	}
}

func (b *idleBox) seen() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.actions...)
}

// newIdleBoxServer wires a Server to a fake box the way the wrong-state replay
// harness does. boxHost stays empty on purpose: transportKeyFallback then
// declines (nothing box-owned is playing), which is the fall-through these
// tests need so the UPnP renderer receives the action and answers the fault.
func newIdleBoxServer(t *testing.T, fault string) (*Server, *idleBox) {
	t.Helper()
	rec := &idleBox{fault: fault}
	box := httptest.NewServer(rec.handler())
	t.Cleanup(box.Close)
	s := &Server{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		queue:    newPlayQueue(),
		renderer: &upnp.Renderer{ControlURL: box.URL, Client: box.Client()},
	}
	return s, rec
}

// Pause and Stop are idempotent intents: pressing them while the box is
// already idle means the desired state already holds. The box still answers
// with the UPnP wrong-state fault (a SOAP 500 carrying errorCode 501), and
// before the guard in handlePause/handleStop that raw fault went back to the
// user as a 502. The user must get a calm 200 instead, and the handler must
// not chase the fault with repair pushes.
func TestPauseAndStopOnAnIdleBoxAreIdempotent(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		handle     func(*Server, http.ResponseWriter, *http.Request)
		wantAction string
	}{
		{"pause", "/api/pause", (*Server).handlePause, "Pause"},
		{"stop", "/api/stop", (*Server).handleStop, "Stop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, rec := newIdleBoxServer(t, wrongStateFault)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			tc.handle(s, w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
			}
			var resp map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
			}
			// "not_playing" and not "paused"/"stopped": the answer stays honest
			// about the box having had nothing to act on.
			if resp["status"] != "not_playing" {
				t.Errorf(`status = %q, want "not_playing"`, resp["status"])
			}

			got := rec.seen()
			var sawAction bool
			for _, a := range got {
				switch a {
				case tc.wantAction:
					sawAction = true
				case "SetAVTransportURI", "Play":
					// The wrong-state PLAY repair (clean slate + re-push) must
					// stay out of Pause/Stop: re-pushing a stream because the
					// user asked for silence would be the opposite of their
					// intent.
					t.Errorf("box saw a %s after an idle %s; actions were %v", a, tc.wantAction, got)
				}
			}
			if !sawAction {
				t.Errorf("box never received the %s (transportKeyFallback did not fall through?); actions were %v", tc.wantAction, got)
			}
			// One attempt is enough. Stop in particular must skip the
			// stopped-verification re-issue once the box already said "idle".
			if n := countAction(got, tc.wantAction); n != 1 {
				t.Errorf("box saw %d %s actions, want exactly 1; actions were %v", n, tc.wantAction, got)
			}
		})
	}
}

// The 200 must be earned by the wrong-state fault specifically. Any other
// renderer failure still surfaces as a 502 so a genuinely unwell box is not
// papered over with a fake success.
func TestOtherTransportFaultsStillSurface(t *testing.T) {
	const unrelatedFault = `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>402</errorCode><errorDescription>Invalid Args</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`
	cases := []struct {
		name   string
		path   string
		handle func(*Server, http.ResponseWriter, *http.Request)
	}{
		{"pause", "/api/pause", (*Server).handlePause},
		{"stop", "/api/stop", (*Server).handleStop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newIdleBoxServer(t, unrelatedFault)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			tc.handle(s, w, req)

			if w.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502 (body %s)", w.Code, w.Body.String())
			}
		})
	}
}

// isWrongTransportState gates the idempotent treatment above, so its matching
// is pinned down here: Bose answers with errorCode 501 and the "wrong state"
// text, the AVTransport spec defines 701 for the same situation, and anything
// else must stay a real error.
func TestIsWrongTransportState(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"nil", "", false},
		{"bose 501 fault", "soap Pause status 500: " + wrongStateFault, true},
		{"bare wrong state text", "Action request came in wrong state", true},
		{"spec 701 code", "soap Stop status 500: <errorCode>701</errorCode>", true},
		{"unrelated 402", "soap Pause status 500: <errorCode>402</errorCode> Invalid Args", false},
		{"plain network error", "connection refused", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.msg != "" {
				err = &transportTestErr{tc.msg}
			}
			if got := isWrongTransportState(err); got != tc.want {
				t.Errorf("isWrongTransportState(%v) = %v, want %v", err, got, tc.want)
			}
		})
	}
}

type transportTestErr struct{ msg string }

func (e *transportTestErr) Error() string { return e.msg }

func countAction(actions []string, want string) int {
	n := 0
	for _, a := range actions {
		if a == want {
			n++
		}
	}
	return n
}
