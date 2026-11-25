package webui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/marge"
)

// shareTestToken is the fake token the leak checks look for; deezerTestUser
// (keepDeezerLogin's account) is the account.
const shareTestToken = "fake-token-for-tests"

// shareSpeaker is one speaker of the sharing tests: its agent, stand-in, fake
// firmware, log and kept-login file.
type shareSpeaker struct {
	s    *Server
	m    *marge.Server
	box  *fakeNativeBox
	logs *bytes.Buffer
	kept string
}

func newShareSpeaker(t *testing.T) shareSpeaker {
	t.Helper()
	s, _, box, logs := deezerTestServer(t)
	dir := t.TempDir()
	kept := filepath.Join(dir, "deezer-login.json")
	m := marge.New(s.logger, marge.WithKeptDeezerLogin(kept), marge.WithReflectSourcesPath(filepath.Join(dir, "reflect.json")))
	s.deezer = m
	return shareSpeaker{s: s, m: m, box: box, logs: logs, kept: kept}
}

// needsDeezer makes the speaker list Deezer for account without a working login.
func (sp shareSpeaker) needsDeezer(account string) {
	sp.box.sources = []boxapi.Source{{Source: "DEEZER", SourceAccount: account, Status: "UNAVAILABLE"}}
	sp.box.keepAfterRemove = true
}

// sealFor seals the sender's kept login to the receiver, the way the sender
// does after reading the receiver's need signal for account.
func sealFor(t *testing.T, sender, receiver shareSpeaker, account string) marge.SealedDeezerLogin {
	t.Helper()
	key := receiver.m.DeezerShareKey()
	sealed, err := sender.m.SealKeptDeezerLogin(key, marge.DeezerShareAccountHash(key, account))
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func postShare(sp shareSpeaker, remote string, sealed marge.SealedDeezerLogin) (*httptest.ResponseRecorder, deezerShareAnswer) {
	b, _ := json.Marshal(map[string]any{"sealed": sealed})
	w := httptest.NewRecorder()
	sp.s.handleDeezerKeptLogin(w, nativeReq(http.MethodPost, "/api/deezer/kept-login", string(b), remote, "application/json"))
	sp.s.deezerShare.wg.Wait()
	var ans deezerShareAnswer
	_ = json.Unmarshal(w.Body.Bytes(), &ans)
	return w, ans
}

func noLeak(t *testing.T, what string, outs ...string) {
	t.Helper()
	for _, out := range outs {
		if strings.Contains(out, shareTestToken) || strings.Contains(out, deezerTestUser) {
			t.Fatalf("%s leaks the login or the account: %s", what, out)
		}
	}
}

// The receiving side's guards: an unknown caller, an account the speaker does
// not list, a speaker whose Deezer works and a speaker that keeps a login
// already are all refused, and nothing is written.
func TestDeezerShareReceiveGuards(t *testing.T) {
	deezerShareOutcomeWait = 0
	sender := newShareSpeaker(t)
	keepDeezerLogin(t, sender.m)
	recv := newShareSpeaker(t)
	known := map[string]bool{"192.168.1.20": true}
	recv.s.knownPeerFn = func(ip string) bool { return known[ip] }
	recv.needsDeezer(deezerTestUser)

	check := func(name, remote string, sealed marge.SealedDeezerLogin, code int, reason string) {
		t.Helper()
		w, ans := postShare(recv, remote, sealed)
		if w.Code != code || ans.Status != "refused" || ans.Reason != reason {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		noLeak(t, name, w.Body.String())
	}

	check("unknown caller", "192.168.1.99:4000", sealFor(t, sender, recv, deezerTestUser), http.StatusForbidden, "unknown-caller")
	if _, err := os.Stat(recv.kept); !os.IsNotExist(err) {
		t.Fatal("an unknown caller's login was written")
	}

	recv.needsDeezer("other@example.com")
	// The sender would never seal for a hash that does not match; a forged
	// one must still be refused by the receiver itself.
	check("account mismatch", "192.168.1.20:4000", sealFor(t, sender, recv, deezerTestUser), http.StatusConflict, "account-mismatch")

	recv.box.sources = []boxapi.Source{{Source: "DEEZER", SourceAccount: deezerTestUser, Status: "READY"}}
	check("ready", "192.168.1.20:4000", sealFor(t, sender, recv, deezerTestUser), http.StatusConflict, "ready")

	recv.box.sources = nil
	check("not listed", "192.168.1.20:4000", sealFor(t, sender, recv, deezerTestUser), http.StatusConflict, "not-listed")

	w := httptest.NewRecorder()
	recv.s.handleDeezerKeptLogin(w, nativeReq(http.MethodPost, "/api/deezer/kept-login", "{}", "192.168.1.20:4000", "text/plain"))
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form post: %d", w.Code)
	}
	if recv.box.removeCalls != 0 || recv.box.notifyCalls != 0 {
		t.Fatalf("a refused login reached the firmware: %d removals", recv.box.removeCalls)
	}
	if _, err := os.Stat(recv.kept); !os.IsNotExist(err) {
		t.Fatal("a refused login was written")
	}

	// A speaker that keeps a login already never takes a peer's.
	keeper := newShareSpeaker(t)
	keeper.s.knownPeerFn = recv.s.knownPeerFn
	keepDeezerLogin(t, keeper.m)
	keeper.needsDeezer(deezerTestUser)
	before, _ := os.ReadFile(keeper.kept)
	w, ans := postShare(keeper, "192.168.1.20:4000", sealFor(t, sender, keeper, deezerTestUser))
	if w.Code != http.StatusConflict || ans.Reason != "kept" {
		t.Fatalf("kept: %d %s", w.Code, w.Body.String())
	}
	if after, _ := os.ReadFile(keeper.kept); string(after) != string(before) {
		t.Fatal("a kept login was overwritten")
	}
	if sh := recv.m.DeezerLoginSnapshot().Shares; sh == nil || len(sh.Received) != 5 || sh.Received[0].Outcome != "refused:unknown-caller" {
		t.Fatalf("received record = %+v", sh)
	}
	snap, _ := json.Marshal(recv.m.DeezerLoginSnapshot())
	noLeak(t, "logs and snapshot", recv.logs.String(), string(snap))
}

// A known peer's login for the listed account is kept 0600, from "peer", and
// the firmware is told to recreate its account from it.
func TestDeezerShareReceiveAccepts(t *testing.T) {
	deezerShareOutcomeWait = 0
	sender := newShareSpeaker(t)
	keepDeezerLogin(t, sender.m)
	recv := newShareSpeaker(t)
	recv.s.knownPeerFn = func(ip string) bool { return ip == "192.168.1.20" }
	recv.needsDeezer(strings.ToUpper(deezerTestUser))

	w, ans := postShare(recv, "192.168.1.20:4000", sealFor(t, sender, recv, deezerTestUser))
	if w.Code != http.StatusOK || ans.Status != "accepted" {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(recv.kept)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("kept file mode: %v %v", fi, err)
		}
	}
	st := recv.m.DeezerLoginSnapshot()
	if !st.Kept || st.From != "peer" || !strings.HasPrefix(st.FromPeer, "peer#") {
		t.Fatalf("snapshot = %+v", st)
	}
	if recv.box.removeCalls != 1 || recv.box.notifyCalls != 1 {
		t.Fatalf("recreate: %d removals, %d notifications", recv.box.removeCalls, recv.box.notifyCalls)
	}
	if a := recv.m.LastDeezerAttempt(); a == nil || a.Kind != "recreate" || a.Trigger != "peer" {
		t.Fatalf("attempt = %+v", a)
	}
	snap, _ := json.Marshal(recv.m.DeezerLoginSnapshot())
	noLeak(t, "accepted path", w.Body.String(), recv.logs.String(), string(snap))
}

// recordingPeer serves a receiving speaker over HTTP and records every
// request body and answer, for the leak checks.
type recordingPeer struct {
	mu        sync.Mutex
	posts     int
	needReads int
	traffic   bytes.Buffer
}

func (rp *recordingPeer) serve(sp shareSpeaker) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		rec := httptest.NewRecorder()
		switch r.URL.Path {
		case "/api/deezer/login-need":
			sp.s.handleDeezerLoginNeed(rec, r)
		case "/api/deezer/kept-login":
			sp.s.handleDeezerKeptLogin(rec, r)
		default:
			http.NotFound(rec, r)
		}
		rp.mu.Lock()
		if r.URL.Path == "/api/deezer/kept-login" {
			rp.posts++
		} else {
			rp.needReads++
		}
		rp.traffic.Write(body)
		rp.traffic.Write(rec.Body.Bytes())
		rp.mu.Unlock()
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
}

func (rp *recordingPeer) counts() (needReads, posts int) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return rp.needReads, rp.posts
}

func peerLinkTo(srv *httptest.Server, reachable bool) PeerLink {
	return PeerLink{Name: "Kitchen", URL: srv.URL + "/", IP: "127.0.0.1", Reachable: reachable}
}

// The sender posts only to a peer that needs a login for its own account,
// once per agent start, and nothing in the traffic, the answers, the logs or
// the debug sections carries the login or the account.
func TestDeezerShareSendsOnlyToAMatchingPeerOnce(t *testing.T) {
	deezerShareOutcomeWait = 0
	sender := newShareSpeaker(t)
	keepDeezerLogin(t, sender.m)
	recv := newShareSpeaker(t)
	recv.needsDeezer(deezerTestUser)
	rp := &recordingPeer{}
	srv := rp.serve(recv)
	defer srv.Close()
	sender.s.cachedPeersFn = func() []PeerLink { return []PeerLink{peerLinkTo(srv, true)} }

	sender.s.OfferKeptDeezerLogin(t.Context(), deezerShareTriggerStart)
	recv.s.deezerShare.wg.Wait()
	if _, posts := rp.counts(); posts != 1 {
		t.Fatalf("posts = %d", posts)
	}
	if st := recv.m.DeezerLoginSnapshot(); !st.Kept || st.From != "peer" {
		t.Fatalf("receiver = %+v", st)
	}
	if recv.box.removeCalls != 1 || recv.box.notifyCalls != 1 {
		t.Fatalf("receiver recreate: %d removals, %d notifications", recv.box.removeCalls, recv.box.notifyCalls)
	}
	sh := sender.m.DeezerLoginSnapshot().Shares
	if sh == nil || len(sh.Sent) != 1 || sh.Sent[0].Outcome != "accepted" || sh.Sent[0].Trigger != "start" {
		t.Fatalf("sent record = %+v", sh)
	}

	// Once per start: neither Check again nor the peer coming up again posts
	// a second time.
	sender.s.OfferKeptDeezerLogin(t.Context(), deezerShareTriggerCheck)
	sender.s.offerDeezerToPeer(t.Context(), deezerSharePeer{ip: "127.0.0.1", base: srv.URL, reachable: true}, deezerShareTriggerPeer)
	if _, posts := rp.counts(); posts != 1 {
		t.Fatalf("posts after repeats = %d", posts)
	}

	snapS, _ := json.Marshal(sender.m.DeezerLoginSnapshot())
	snapR, _ := json.Marshal(recv.m.DeezerLoginSnapshot())
	rp.mu.Lock()
	traffic := rp.traffic.String()
	rp.mu.Unlock()
	noLeak(t, "the share", traffic, sender.logs.String(), recv.logs.String(), string(snapS), string(snapR))
}

// No post to a peer that does not need a login, needs one for another
// account, or cannot be reached; none at all without a kept login.
func TestDeezerShareSkipsPeersThatDoNotMatch(t *testing.T) {
	deezerShareOutcomeWait = 0
	sender := newShareSpeaker(t)
	recv := newShareSpeaker(t)
	rp := &recordingPeer{}
	srv := rp.serve(recv)
	defer srv.Close()
	sender.s.cachedPeersFn = func() []PeerLink { return []PeerLink{peerLinkTo(srv, true)} }

	// Without a kept login the sender asks nobody.
	recv.needsDeezer(deezerTestUser)
	sender.s.OfferKeptDeezerLogin(t.Context(), deezerShareTriggerStart)
	sender.s.OfferKeptDeezerLoginToPeer(t.Context(), "127.0.0.1", 1)
	if reads, posts := rp.counts(); reads != 0 || posts != 0 {
		t.Fatalf("without a kept login: %d reads, %d posts", reads, posts)
	}

	keepDeezerLogin(t, sender.m)
	outcome := func() string {
		sh := sender.m.DeezerLoginSnapshot().Shares
		if sh == nil || len(sh.Sent) == 0 {
			return ""
		}
		return sh.Sent[len(sh.Sent)-1].Outcome
	}

	recv.box.sources = []boxapi.Source{{Source: "DEEZER", SourceAccount: deezerTestUser, Status: "READY"}}
	sender.s.OfferKeptDeezerLogin(t.Context(), deezerShareTriggerStart)
	if got := outcome(); got != "not-needed:ready" {
		t.Fatalf("READY peer: %q", got)
	}

	recv.needsDeezer("other@example.com")
	sender.s.OfferKeptDeezerLogin(t.Context(), deezerShareTriggerCheck)
	if got := outcome(); got != "hash-mismatch" {
		t.Fatalf("other account: %q", got)
	}

	// A dimmed peer (series-I speakers cannot reach each other's agent) is
	// not asked at all.
	reads, _ := rp.counts()
	sender.s.cachedPeersFn = func() []PeerLink { return []PeerLink{peerLinkTo(srv, false)} }
	sender.s.OfferKeptDeezerLogin(t.Context(), deezerShareTriggerCheck)
	if got := outcome(); got != "unreachable" {
		t.Fatalf("dimmed peer: %q", got)
	}
	if now, _ := rp.counts(); now != reads {
		t.Fatal("a dimmed peer was asked")
	}

	// A peer whose agent predates sharing answers 404.
	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	sender.s.cachedPeersFn = func() []PeerLink { return []PeerLink{peerLinkTo(old, true)} }
	sender.s.OfferKeptDeezerLogin(t.Context(), deezerShareTriggerCheck)
	if got := outcome(); got != "old-agent" {
		t.Fatalf("old agent: %q", got)
	}

	if _, posts := rp.counts(); posts != 0 {
		t.Fatalf("posts = %d", posts)
	}
	if _, err := os.Stat(recv.kept); !os.IsNotExist(err) {
		t.Fatal("the receiver kept a login")
	}
	snap, _ := json.Marshal(sender.m.DeezerLoginSnapshot())
	noLeak(t, "the skips", sender.logs.String(), recv.logs.String(), string(snap))
}

// The need signal names neither the account nor the login, and says why a
// speaker needs nothing.
func TestDeezerLoginNeedSignal(t *testing.T) {
	sp := newShareSpeaker(t)
	get := func() DeezerLoginNeed {
		t.Helper()
		w := httptest.NewRecorder()
		sp.s.handleDeezerLoginNeed(w, nativeReq(http.MethodGet, "/api/deezer/login-need", "", "192.168.1.20:4000", ""))
		if w.Code != http.StatusOK {
			t.Fatalf("need: %d", w.Code)
		}
		noLeak(t, "the need signal", w.Body.String())
		var n DeezerLoginNeed
		_ = json.Unmarshal(w.Body.Bytes(), &n)
		return n
	}
	if n := get(); n.Needs || n.Why != "not-listed" {
		t.Fatalf("no Deezer: %+v", n)
	}
	sp.box.sources = []boxapi.Source{{Source: "DEEZER", Status: "UNAVAILABLE"}}
	if n := get(); n.Needs || n.Why != "no-account" {
		t.Fatalf("no account: %+v", n)
	}
	sp.needsDeezer(deezerTestUser)
	n := get()
	if !n.Needs || n.Key == "" || n.AccountHash != marge.DeezerShareAccountHash(n.Key, deezerTestUser) {
		t.Fatalf("needs: %+v", n)
	}
	sp.box.sourcesErr = io.ErrUnexpectedEOF
	if n := get(); n.Needs || n.Why != "unreadable" {
		t.Fatalf("unreadable: %+v", n)
	}
	sp.box.sourcesErr = nil
	keepDeezerLogin(t, sp.m)
	if n := get(); n.Needs || n.Why != "kept" {
		t.Fatalf("kept: %+v", n)
	}
	w := httptest.NewRecorder()
	sp.s.handleDeezerLoginNeed(w, nativeReq(http.MethodGet, "/api/deezer/login-need", "", "203.0.113.7:4000", ""))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-LAN caller: %d", w.Code)
	}
}
