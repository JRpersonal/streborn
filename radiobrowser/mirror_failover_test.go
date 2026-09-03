package radiobrowser

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// failoverMirror is an httptest stand-in for one mirror in the rotation. Unlike
// fakeMirror it always answers with the SAME fixed status and body, so a row of
// them models a healthy/broken mirror list for fetchJSON's failover loop.
type failoverMirror struct {
	mu   sync.Mutex
	hits int
	srv  *httptest.Server
}

func newFailoverMirror(t *testing.T, status int, body string) *failoverMirror {
	t.Helper()
	m := &failoverMirror{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.hits++
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *failoverMirror) hitCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits
}

// mirrorReply describes what one mirror in a test row answers.
type mirrorReply struct {
	status int
	body   string
}

// fetchJSON must roll through the mirror list until one answers usefully: an
// HTTP error status or a malformed JSON body both count as "this mirror is
// broken, try the next", the first working mirror is promoted to the front,
// and only when EVERY mirror failed does the caller get an error — wrapped
// behind the stable "radio directory unreachable" marker the frontend maps to
// a localized toast.
func TestFetchJSONMirrorFailover(t *testing.T) {
	const okJSON = `[{"name":"Reachable","lastcheckok":1,"lastchecktime":"2026-07-01 09:44:00"}]`

	cases := []struct {
		name    string
		mirrors []mirrorReply
		wantErr bool
		// wantErrContains must appear in the error text (the raw mirror
		// detail kept behind the stable marker). Only checked with wantErr.
		wantErrContains string
		// wantNames is the decoded station list on success.
		wantNames []string
		// wantHits is the expected request count per mirror, in list order.
		wantHits []int
		// wantFront indexes the initial mirror list: the mirror expected at
		// the front of the rotation afterwards. A success promotes its
		// mirror; failures leave the order untouched.
		wantFront int
	}{
		{
			name: "500 then 200 rolls on to the second mirror",
			mirrors: []mirrorReply{
				{status: http.StatusInternalServerError, body: "boom"},
				{status: http.StatusOK, body: okJSON},
			},
			wantNames: []string{"Reachable"},
			wantHits:  []int{1, 1},
			wantFront: 1,
		},
		{
			name: "first mirror success leaves the rest untouched",
			mirrors: []mirrorReply{
				{status: http.StatusOK, body: okJSON},
				{status: http.StatusOK, body: okJSON},
			},
			wantNames: []string{"Reachable"},
			wantHits:  []int{1, 0},
			wantFront: 0,
		},
		{
			name: "all mirrors failing returns a wrapped error",
			mirrors: []mirrorReply{
				{status: http.StatusInternalServerError, body: "boom"},
				{status: http.StatusBadGateway, body: "bad gateway"},
			},
			wantErr: true,
			// lastErr is the LAST mirror's failure.
			wantErrContains: ": 502:",
			wantHits:        []int{1, 1},
			wantFront:       0,
		},
		{
			name: "malformed JSON rolls on to the next mirror",
			mirrors: []mirrorReply{
				{status: http.StatusOK, body: `[{"name":"Broken`},
				{status: http.StatusOK, body: okJSON},
			},
			wantNames: []string{"Reachable"},
			wantHits:  []int{1, 1},
			wantFront: 1,
		},
		{
			name: "all mirrors malformed errors without panicking",
			mirrors: []mirrorReply{
				{status: http.StatusOK, body: `{`},
				{status: http.StatusOK, body: `[{"name":`},
			},
			wantErr:         true,
			wantErrContains: "decode",
			wantHits:        []int{1, 1},
			wantFront:       0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			servers := make([]*failoverMirror, len(tc.mirrors))
			urls := make([]string, len(tc.mirrors))
			for i, mr := range tc.mirrors {
				servers[i] = newFailoverMirror(t, mr.status, mr.body)
				urls[i] = servers[i].srv.URL + "/json"
			}
			httpClient := &http.Client{Timeout: 5 * time.Second}
			c := &Client{
				HTTP:    httpClient,
				ipHTTP:  httpClient,
				mirrors: append([]string(nil), urls...),
			}

			var out []Station
			err := c.fetchJSON(context.Background(), "/stations", &out)

			if tc.wantErr {
				if err == nil {
					t.Fatal("fetchJSON returned nil error, want failure")
				}
				if !strings.Contains(err.Error(), "radio directory unreachable") {
					t.Errorf("error %q missing the stable user-facing marker", err)
				}
				if errors.Unwrap(err) == nil {
					t.Errorf("error %q does not wrap the mirror error", err)
				}
				if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Errorf("error %q does not contain %q", err, tc.wantErrContains)
				}
			} else {
				if err != nil {
					t.Fatalf("fetchJSON: %v", err)
				}
				if !equalNames(out, tc.wantNames...) {
					t.Errorf("stations = %v, want %v", names(out), tc.wantNames)
				}
			}

			for i, want := range tc.wantHits {
				if got := servers[i].hitCount(); got != want {
					t.Errorf("mirror %d hits = %d, want %d", i, got, want)
				}
			}
			if got := c.mirrors[0]; got != urls[tc.wantFront] {
				t.Errorf("front mirror = %q, want %q (initial index %d)", got, urls[tc.wantFront], tc.wantFront)
			}
		})
	}
}
