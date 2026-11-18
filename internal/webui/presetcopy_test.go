package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// fakeCopyPeer is a target speaker's agent as the copy sees it.
type fakeCopyPeer struct {
	rows string // presetRows on /api/agent/version: "2" or ""
	bulk int    // status for PUT /api/presets: 0 = 200
	body string // body for a non-200 bulk answer

	mu      sync.Mutex
	calls   []string // "METHOD /path"
	bulkSet []presets.Preset
	slots   []int  // per-slot PUTs
	cred    string // the Spotify login posted
	srv     *httptest.Server
}

func newFakeCopyPeer(t *testing.T, rows string) *fakeCopyPeer {
	t.Helper()
	f := &fakeCopyPeer{rows: rows}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/api/agent/version":
			out := map[string]string{"version": "v1.6.2"}
			if f.rows != "" {
				out["presetRows"] = f.rows
			}
			writeJSON(w, http.StatusOK, out)
		case r.URL.Path == "/api/presets" && r.Method == http.MethodPut:
			if f.bulk != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(f.bulk)
				_, _ = io.WriteString(w, f.body)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&f.bulkSet)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		case strings.HasPrefix(r.URL.Path, "/api/presets/") && r.Method == http.MethodPut:
			var p presets.Preset
			_ = json.NewDecoder(r.Body).Decode(&p)
			f.slots = append(f.slots, p.Slot)
			writeJSON(w, http.StatusOK, p)
		case r.URL.Path == "/api/box/sync-presets":
			writeJSON(w, http.StatusOK, map[string]any{"synced": 6})
		case r.URL.Path == "/spotify/credential" && r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			f.cred = string(b)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCopyPeer) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// copyFleet wires peers by address: ip -> fake agent. A nil fake is a speaker
// that is in the roster but does not answer.
func copyFleet(t *testing.T, s *Server, peers []PeerLink, fakes map[string]*fakeCopyPeer) {
	t.Helper()
	origBase, origClient := fleetPeerBase, fleetClient
	origWindow, origStep := presetCopySettleWindow, presetCopySettleStep
	t.Cleanup(func() {
		fleetPeerBase, fleetClient = origBase, origClient
		presetCopySettleWindow, presetCopySettleStep = origWindow, origStep
	})
	presetCopySettleWindow, presetCopySettleStep = 0, time.Millisecond
	fleetClient = func(timeout time.Duration) *http.Client { return &http.Client{Timeout: timeout} }
	fleetPeerBase = func(ip string, _ int) string {
		if f := fakes[ip]; f != nil {
			return f.srv.URL
		}
		return "http://127.0.0.1:1" // refused
	}
	s.peersFn = func(context.Context) []PeerLink { return peers }
}

func copySource(t *testing.T, ps ...presets.Preset) *Server {
	t.Helper()
	st := presets.New()
	_ = st.SetSlots(ps) // no path: kept in memory, the write error is expected
	return &Server{
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		presets:           st,
		spotifyExportCred: func() ([]byte, error) { return []byte("LOGIN-BLOB"), nil },
	}
}

var copyKeys = []presets.Preset{
	{Slot: 1, Name: "SWR3", StreamURL: "http://example.invalid/swr3", Type: "radio"},
	{Slot: 2, Name: "Focus", Type: "spotify", URI: "spotify:playlist:abc"},
	{Slot: 8, Name: "Jazz", StreamURL: "http://example.invalid/jazz", Type: "radio"},
}

func postCopy(t *testing.T, s *Server, remote, body string) (int, presetCopyAnswer, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, presetCopyAPIPath, strings.NewReader(body))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	s.handlePresetCopy(rec, req)
	var ans presetCopyAnswer
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &ans)
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	return rec.Code, ans, raw
}

const copyLAN = "192.168.1.20:5555"

func TestPresetCopyOneTargetCarriesBothRowsAndTheSpotifyLogin(t *testing.T) {
	s := copySource(t, copyKeys...)
	kitchen := newFakeCopyPeer(t, "2")
	copyFleet(t, s, []PeerLink{{Name: "Kitchen", IP: "10.0.0.2", URL: "http://10.0.0.2:8888/", Reachable: true}},
		map[string]*fakeCopyPeer{"10.0.0.2": kitchen})

	code, ans, _ := postCopy(t, s, copyLAN, `{"targets":["10.0.0.2"]}`)
	if code != http.StatusOK || ans.Done != 1 || ans.Total != 1 || ans.Keys != 3 {
		t.Fatalf("code=%d answer=%+v", code, ans)
	}
	r := ans.Results[0]
	if !r.OK || r.Copied != 3 || r.Row1Only || r.Spotify != presetCopySpotifyCopied || r.Name != "Kitchen" {
		t.Fatalf("result = %+v", r)
	}
	if len(kitchen.bulkSet) != 3 || kitchen.bulkSet[2].Slot != 8 || kitchen.bulkSet[1].URI != "spotify:playlist:abc" {
		t.Errorf("bulk set = %+v", kitchen.bulkSet)
	}
	if kitchen.cred != "LOGIN-BLOB" {
		t.Errorf("credential = %q", kitchen.cred)
	}
	want := []string{"GET /api/agent/version", "PUT /api/presets", "POST /api/box/sync-presets", "POST /spotify/credential"}
	if got := kitchen.sent(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

func TestPresetCopyAllGoesToEveryAnsweringSpeaker(t *testing.T) {
	s := copySource(t, copyKeys[0], copyKeys[2])
	a, b := newFakeCopyPeer(t, "2"), newFakeCopyPeer(t, "2")
	copyFleet(t, s, []PeerLink{
		{Name: "Kitchen", IP: "10.0.0.2", Reachable: true},
		{Name: "Bedroom", IP: "10.0.0.3", Reachable: true},
		{Name: "Garage", IP: "10.0.0.4", Reachable: false}, // not offered, not contacted
	}, map[string]*fakeCopyPeer{"10.0.0.2": a, "10.0.0.3": b})

	code, ans, _ := postCopy(t, s, copyLAN, `{"all":true}`)
	if code != http.StatusOK || ans.Done != 2 || ans.Total != 2 {
		t.Fatalf("code=%d answer=%+v", code, ans)
	}
	// Sorted by name.
	if ans.Results[0].Name != "Bedroom" || ans.Results[1].Name != "Kitchen" {
		t.Errorf("order = %+v", ans.Results)
	}
	for _, f := range []*fakeCopyPeer{a, b} {
		if len(f.bulkSet) != 2 {
			t.Errorf("bulk set = %+v", f.bulkSet)
		}
		// No Spotify key in the set: the login stays here.
		if f.cred != "" {
			t.Errorf("credential sent without a Spotify key")
		}
	}
	if ans.Results[0].Spotify != "" {
		t.Errorf("spotify = %q", ans.Results[0].Spotify)
	}
}

func TestPresetCopyOneRowTargetGetsRowOneOnly(t *testing.T) {
	s := copySource(t, copyKeys...)
	old := newFakeCopyPeer(t, "")
	copyFleet(t, s, []PeerLink{{Name: "Office", IP: "10.0.0.5", Reachable: true}},
		map[string]*fakeCopyPeer{"10.0.0.5": old})

	_, ans, _ := postCopy(t, s, copyLAN, `{"targets":["10.0.0.5"]}`)
	r := ans.Results[0]
	if !r.OK || r.Copied != 2 || !r.Row1Only || r.NotCopied != 1 {
		t.Fatalf("result = %+v", r)
	}
	for _, p := range old.bulkSet {
		if p.Slot > 6 {
			t.Errorf("row-2 key %d went to a one-row speaker", p.Slot)
		}
	}
}

func TestPresetCopyConflictWritesNothingAndSaysWhichStation(t *testing.T) {
	s := copySource(t, copyKeys...)
	k := newFakeCopyPeer(t, "2")
	k.bulk = http.StatusConflict
	k.body = `{"code":"already-on-slot","slot":4,"name":"SWR3","payloadSlot":1}`
	copyFleet(t, s, []PeerLink{{Name: "Kitchen", IP: "10.0.0.2", Reachable: true}},
		map[string]*fakeCopyPeer{"10.0.0.2": k})

	code, ans, _ := postCopy(t, s, copyLAN, `{"targets":["10.0.0.2"]}`)
	r := ans.Results[0]
	if code != http.StatusOK || r.OK || ans.Done != 0 || r.Code != presetCopyCodeConflict ||
		r.Conflict == nil || r.Conflict.Name != "SWR3" || r.Conflict.Slot != 4 {
		t.Fatalf("code=%d result=%+v", code, r)
	}
	for _, c := range k.sent() {
		if c == "POST /api/box/sync-presets" || c == "POST /spotify/credential" {
			t.Errorf("after a refused set the copy still sent %s", c)
		}
	}
}

func TestPresetCopyPartialFailureReportsEachSpeaker(t *testing.T) {
	s := copySource(t, copyKeys[0])
	ok := newFakeCopyPeer(t, "2")
	bad := newFakeCopyPeer(t, "2")
	bad.bulk = http.StatusInternalServerError
	bad.body = "disk full"
	copyFleet(t, s, []PeerLink{
		{Name: "Kitchen", IP: "10.0.0.2", Reachable: true},
		{Name: "Bedroom", IP: "10.0.0.3", Reachable: true},
		{Name: "Office", IP: "10.0.0.4", Reachable: true}, // listed, but nothing answers
	}, map[string]*fakeCopyPeer{"10.0.0.2": ok, "10.0.0.3": bad})

	code, ans, _ := postCopy(t, s, copyLAN, `{"all":true}`)
	if code != http.StatusOK || ans.Done != 1 || ans.Total != 3 {
		t.Fatalf("code=%d answer=%+v", code, ans)
	}
	by := map[string]presetCopyResult{}
	for _, r := range ans.Results {
		by[r.Name] = r
	}
	if !by["Kitchen"].OK {
		t.Errorf("Kitchen = %+v", by["Kitchen"])
	}
	if by["Bedroom"].Code != presetCopyCodeRefused || !strings.Contains(by["Bedroom"].Error, "disk full") {
		t.Errorf("Bedroom = %+v", by["Bedroom"])
	}
	if by["Office"].Code != presetCopyCodeUnreachable {
		t.Errorf("Office = %+v", by["Office"])
	}
}

func TestPresetCopyOldTargetGetsTheKeysOneByOne(t *testing.T) {
	s := copySource(t, copyKeys...)
	old := newFakeCopyPeer(t, "2")
	old.bulk = http.StatusMethodNotAllowed
	copyFleet(t, s, []PeerLink{{Name: "Kitchen", IP: "10.0.0.2", Reachable: true}},
		map[string]*fakeCopyPeer{"10.0.0.2": old})

	_, ans, _ := postCopy(t, s, copyLAN, `{"targets":["10.0.0.2"]}`)
	if r := ans.Results[0]; !r.OK || r.Copied != 3 {
		t.Fatalf("result = %+v", r)
	}
	if got := fmt.Sprint(old.slots); got != "[1 2 8]" {
		t.Errorf("per-slot writes = %s", got)
	}
}

func TestPresetCopyNeverContactsAnAddressOutsideTheRoster(t *testing.T) {
	s := copySource(t, copyKeys...)
	k := newFakeCopyPeer(t, "2")
	copyFleet(t, s, []PeerLink{{Name: "Kitchen", IP: "10.0.0.2", Reachable: true}},
		map[string]*fakeCopyPeer{"10.0.0.2": k, "10.0.0.9": k})

	_, ans, _ := postCopy(t, s, copyLAN, `{"targets":["10.0.0.9"]}`)
	if ans.Total != 1 || ans.Results[0].Code != presetCopyCodeUnknown || ans.Results[0].OK {
		t.Fatalf("answer = %+v", ans)
	}
	if got := k.sent(); len(got) != 0 {
		t.Errorf("an address outside the roster was contacted: %v", got)
	}
}

func TestPresetCopyWithoutASpotifyLoginStillCopies(t *testing.T) {
	s := copySource(t, copyKeys...)
	s.spotifyExportCred = func() ([]byte, error) { return nil, errors.New("no login") }
	k := newFakeCopyPeer(t, "2")
	copyFleet(t, s, []PeerLink{{Name: "Kitchen", IP: "10.0.0.2", Reachable: true}},
		map[string]*fakeCopyPeer{"10.0.0.2": k})

	_, ans, _ := postCopy(t, s, copyLAN, `{"targets":["10.0.0.2"]}`)
	if r := ans.Results[0]; !r.OK || r.Spotify != presetCopySpotifyNoLogin {
		t.Fatalf("result = %+v", r)
	}
	if k.cred != "" {
		t.Errorf("credential = %q", k.cred)
	}
}

func TestPresetCopyRequestGates(t *testing.T) {
	s := copySource(t, copyKeys...)
	copyFleet(t, s, nil, nil)

	if code, _, _ := postCopy(t, s, "203.0.113.5:1234", `{"all":true}`); code != http.StatusForbidden {
		t.Errorf("off-LAN caller: %d", code)
	}
	if code, _, raw := postCopy(t, s, copyLAN, `{}`); code != http.StatusBadRequest || raw["code"] != presetCopyCodeInvalid {
		t.Errorf("no targets named: %d %v", code, raw)
	}
	if code, _, raw := postCopy(t, s, copyLAN, `{"all":true}`); code != http.StatusConflict || raw["code"] != presetCopyCodeNoTargets {
		t.Errorf("empty roster: %d %v", code, raw)
	}
	req := httptest.NewRequest(http.MethodGet, presetCopyAPIPath, nil)
	req.RemoteAddr = copyLAN
	rec := httptest.NewRecorder()
	s.handlePresetCopy(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", rec.Code)
	}

	empty := copySource(t)
	copyFleet(t, empty, []PeerLink{{Name: "Kitchen", IP: "10.0.0.2", Reachable: true}}, nil)
	if code, _, raw := postCopy(t, empty, copyLAN, `{"all":true}`); code != http.StatusConflict || raw["code"] != presetCopyCodeNoPresets {
		t.Errorf("no keys: %d %v", code, raw)
	}

	s.presetCopyMu.Lock()
	code, _, raw := postCopy(t, s, copyLAN, `{"all":true}`)
	s.presetCopyMu.Unlock()
	if code != http.StatusConflict || raw["code"] != presetCopyCodeBusy {
		t.Errorf("second run: %d %v", code, raw)
	}
}
