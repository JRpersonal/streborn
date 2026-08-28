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

	"github.com/JRpersonal/streborn/dlna"
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

// A server registered under one UDN that later advertises a DIFFERENT UDN (WD and
// Twonky regenerate their UPnP UUID on a restart/reconfigure) must still be found
// by its friendly name, or a strict UDN resolve loses it forever even though it
// is right there (#733). rematchByName recovers it, but only on a UNIQUE name.
func TestRematchByNameRecoversUUIDRegeneratingServer(t *testing.T) {
	store, err := mediaservers.Load(filepath.Join(t.TempDir(), "ms.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	// Registered under the OLD udn, name "Twonky".
	_ = store.Add(mediaservers.Server{ID: "OLD-UDN-1111", Name: "Twonky"})
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), mediaServers: store}
	key := udnKey("OLD-UDN-1111")

	// Discovery now sees the same server under a NEW udn, plus an unrelated one.
	found := []dlna.Server{
		{UDN: "uuid:NEW-UDN-9999", FriendlyName: "Twonky", Location: "http://192.0.2.50:9000/desc.xml", CDSControlURL: "http://192.0.2.50:9000/ctl"},
		{UDN: "uuid:OTHER-2222", FriendlyName: "Backupserver", Location: "http://192.0.2.51:8200/desc.xml", CDSControlURL: "http://192.0.2.51:8200/ctl"},
	}
	got, ok := s.rematchByName(key, found)
	if !ok || got.CDSControlURL != "http://192.0.2.50:9000/ctl" {
		t.Fatalf("expected to recover the renamed-UUID server, got ok=%v srv=%+v", ok, got)
	}
	// It must remember the address under the ORIGINAL key so recall reaches it.
	s.mediaLocMu.Lock()
	loc := s.mediaLoc[key]
	s.mediaLocMu.Unlock()
	if loc != "http://192.0.2.50:9000/desc.xml" {
		t.Fatalf("location not remembered under the original key, got %q", loc)
	}

	// Ambiguous: two servers share the name -> refuse rather than guess.
	found2 := []dlna.Server{
		{UDN: "uuid:A", FriendlyName: "Twonky", Location: "http://a/d", CDSControlURL: "http://a/c"},
		{UDN: "uuid:B", FriendlyName: "Twonky", Location: "http://b/d", CDSControlURL: "http://b/c"},
	}
	if _, ok := s.rematchByName(key, found2); ok {
		t.Fatal("two servers sharing the name must NOT be matched")
	}

	// serverMatchesKey: UDN match or registered-name match, nothing else.
	if !s.serverMatchesKey(dlna.Server{UDN: "uuid:OLD-UDN-1111"}, key) {
		t.Fatal("exact UDN must match")
	}
	if !s.serverMatchesKey(dlna.Server{UDN: "uuid:NEW", FriendlyName: "twonky"}, key) {
		t.Fatal("registered name (case-insensitive) must match")
	}
	if s.serverMatchesKey(dlna.Server{UDN: "uuid:NEW", FriendlyName: "Something else"}, key) {
		t.Fatal("a different name and UDN must NOT match")
	}
}
