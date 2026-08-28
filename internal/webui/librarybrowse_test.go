package webui

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JRpersonal/streborn/internal/mediaservers"
)

// The browse endpoint answers an unauthenticated LAN GET, so it must serve
// only the REGISTERED music sources: without the guard the speaker would be a
// generic proxy for probing arbitrary UPnP devices on the LAN.
func TestLibraryBrowseServesOnlyRegisteredServers(t *testing.T) {
	store, err := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.Add(mediaservers.Server{ID: "AAAA-BBBB", Name: "NAS"}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), mediaServers: store}

	rr := httptest.NewRecorder()
	s.handleLibraryBrowse(rr, httptest.NewRequest("GET", "/api/library/browse?udn=uuid:CCCC-DDDD", nil))
	if rr.Code != 404 {
		t.Errorf("unregistered udn must 404, got %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	s.handleLibraryBrowse(rr, httptest.NewRequest("GET", "/api/library/browse", nil))
	if rr.Code != 400 {
		t.Errorf("missing udn must 400, got %d", rr.Code)
	}
}

// The servers listing is store-only (no network I/O) and normalises to the
// registered set, so the phone can render the entry buttons instantly.
func TestLibraryServersListsTheStore(t *testing.T) {
	store, serr := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
	if serr != nil {
		t.Fatalf("store: %v", serr)
	}
	_ = store.Add(mediaservers.Server{ID: "AAAA-BBBB", Name: "NAS"})
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), mediaServers: store}

	rr := httptest.NewRecorder()
	s.handleLibraryServers(rr, httptest.NewRequest("GET", "/api/library/servers", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var out []struct{ UDN, Name string }
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(out) != 1 || out[0].Name != "NAS" {
		t.Fatalf("want the one registered server, got %+v", out)
	}
}

// The phone page's three new surfaces exist and are wired: the recently-played
// card replaying through /api/play, the media browser paging through
// /api/library/browse, and the remembered-group card re-forming through the
// same zone POST joinPeer uses (mail requests, 2026-08-25).
func TestPhoneRemoteRecentBrowseAndRememberedGroup(t *testing.T) {
	for _, want := range []string{
		"function loadRecent(",           // the ring read
		"'/api/recent'",                  // from the box's own store
		"e.source === 'radio'",           // replay only what this page can start
		"function browseOpen(",           // one Browse page per tap
		"/api/library/browse?udn=",       // the new endpoint
		"function loadBrowseServers(",    // entry buttons
		"function loadRemembered(",       // persisted-zone read
		"Array.isArray(z.remembered)",    // only when no live zone
		"rememberedMembers.map(function", // re-form sends deviceID+ip pairs
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("phone page is missing %q", want)
		}
	}
}

// A box on a segment where it cannot self-discover the media server borrows the
// address from a sibling. The sibling that CAN see the server is often DIMMED on
// the roster (its mDNS sightings went stale), not unreachable, and the old
// reachable-only guard skipped it, so the peer-assist never fired and the phone
// browse got no answer (#726, v0.9.62 regression). resolveViaPeers must ask a
// dimmed peer too.
func TestResolveViaPeersAsksDimmedPeers(t *testing.T) {
	var asked int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/library/locate") {
			atomic.AddInt32(&asked, 1)
		}
		// Empty location: resolveViaPeers then finds nothing and returns false.
		// The point of the test is that the dimmed peer was ASKED at all.
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"udn":"","location":"","ip":""}`)
	}))
	defer peer.Close()

	s := &Server{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		peersFn: func(context.Context) []PeerLink {
			return []PeerLink{{Name: "sibling", URL: peer.URL, Reachable: false}} // DIMMED
		},
	}
	if _, ok := s.resolveViaPeers(context.Background(), "uuid:AAAA-BBBB"); ok {
		t.Fatal("an empty locate reply must not resolve a server")
	}
	if atomic.LoadInt32(&asked) == 0 {
		t.Fatal("a DIMMED peer must still be asked for the server (#726); the reachable-only guard regressed the peer-assist")
	}
}
