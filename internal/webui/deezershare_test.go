package webui

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/marge"
)

// shareTestToken is the fake token the leak checks look for; deezerTestUser
// (keepDeezerLogin's account) is the account.
const shareTestToken = "fake-token-for-tests"

// shareSpeaker is one speaker of the copy tests: its agent, stand-in, fake
// firmware, log and kept-login file.
type shareSpeaker struct {
	s    *Server
	m    *marge.Server
	box  *fakeNativeBox
	logs *bytes.Buffer
	kept string
}

func newShareSpeaker(t *testing.T, name string) shareSpeaker {
	t.Helper()
	s, _, box, logs := deezerTestServer(t)
	dir := t.TempDir()
	kept := filepath.Join(dir, "deezer-login.json")
	m := marge.New(s.logger, marge.WithKeptDeezerLogin(kept), marge.WithReflectSourcesPath(filepath.Join(dir, "reflect.json")))
	s.deezer = m
	s.boxNameFn = func() (string, string) { return name, "SoundTouch 10" }
	// No call may fall back to a real agent port on the test machine; the
	// fallback test sets its own.
	old := deezerShareAgentPorts
	deezerShareAgentPorts = nil
	t.Cleanup(func() { deezerShareAgentPorts = old })
	return shareSpeaker{s: s, m: m, box: box, logs: logs, kept: kept}
}

// needsDeezer makes the speaker list Deezer for account without a working login.
func (sp shareSpeaker) needsDeezer(account string) {
	sp.box.sources = []boxapi.Source{{Source: "DEEZER", SourceAccount: account, Status: "UNAVAILABLE"}}
	sp.box.keepAfterRemove = true
}

// works makes the speaker list Deezer READY for account.
func (sp shareSpeaker) works(account string) {
	sp.box.sources = []boxapi.Source{{Source: "DEEZER", SourceAccount: account, Status: "READY"}}
}

// refuses makes Deezer refuse the speaker's login (the firmware's 3330).
func (sp shareSpeaker) refuses(account string) {
	sp.needsDeezer(account)
	sp.m.SetDeezerRetrySource(func() (string, time.Time, bool) { return "3330", time.Now(), true })
}

// sealFor seals the sender's kept login to the receiver, the way the sender
// does after reading the receiver's summary for account.
func sealFor(t *testing.T, sender, receiver shareSpeaker, account string) marge.SealedDeezerLogin {
	t.Helper()
	key := receiver.m.DeezerShareKey()
	sealed, err := sender.m.SealKeptDeezerLogin(key, marge.DeezerShareAccountHash(key, account))
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func postShare(sp shareSpeaker, remote string, sealed marge.SealedDeezerLogin, trigger string) (*httptest.ResponseRecorder, deezerShareAnswer) {
	b, _ := json.Marshal(map[string]any{"sealed": sealed, "trigger": trigger})
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
// (without an owner's copy) are all refused, and nothing is written.
func TestDeezerShareReceiveGuards(t *testing.T) {
	deezerShareOutcomeWait = 0
	sender := newShareSpeaker(t, "Kitchen")
	keepDeezerLogin(t, sender.m)
	recv := newShareSpeaker(t, "Bedroom")
	known := map[string]bool{"192.168.1.20": true}
	recv.s.knownPeerFn = func(ip string) bool { return known[ip] }
	recv.needsDeezer(deezerTestUser)

	check := func(sp shareSpeaker, name, remote string, sealed marge.SealedDeezerLogin, trigger string, code int, reason string) {
		t.Helper()
		w, ans := postShare(sp, remote, sealed, trigger)
		if w.Code != code || ans.Status != "refused" || ans.Reason != reason {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		noLeak(t, name, w.Body.String())
	}

	check(recv, "unknown caller", "192.168.1.99:4000", sealFor(t, sender, recv, deezerTestUser), deezerShareUser, http.StatusForbidden, "unknown-caller")
	if _, err := os.Stat(recv.kept); !os.IsNotExist(err) {
		t.Fatal("an unknown caller's login was written")
	}

	recv.needsDeezer("other@example.com")
	// The sender would never seal for a hash that does not match; a forged
	// one must still be refused by the receiver itself.
	check(recv, "account mismatch", "192.168.1.20:4000", sealFor(t, sender, recv, deezerTestUser), deezerShareUser, http.StatusConflict, "account-mismatch")

	recv.works(deezerTestUser)
	check(recv, "ready", "192.168.1.20:4000", sealFor(t, sender, recv, deezerTestUser), deezerShareUser, http.StatusConflict, "ready")

	recv.box.sources = nil
	check(recv, "not listed", "192.168.1.20:4000", sealFor(t, sender, recv, deezerTestUser), deezerShareUser, http.StatusConflict, "not-listed")

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

	// A speaker that keeps a login takes a peer's only on an owner's copy.
	keeper := newShareSpeaker(t, "Office")
	keeper.s.knownPeerFn = recv.s.knownPeerFn
	keepDeezerLogin(t, keeper.m)
	keeper.needsDeezer(deezerTestUser)
	before, _ := os.ReadFile(keeper.kept)
	check(keeper, "kept", "192.168.1.20:4000", sealFor(t, sender, keeper, deezerTestUser), "", http.StatusConflict, "kept")
	if after, _ := os.ReadFile(keeper.kept); string(after) != string(before) {
		t.Fatal("a kept login was overwritten")
	}
	if sh := recv.m.DeezerLoginSnapshot().Shares; sh == nil || len(sh.Received) != 5 || sh.Received[0].Outcome != "refused:unknown-caller" {
		t.Fatalf("received record = %+v", sh)
	}
	snap, _ := json.Marshal(recv.m.DeezerLoginSnapshot())
	noLeak(t, "logs and snapshot", recv.logs.String(), string(snap))
}

// A known peer's login for the listed account is kept 0600, from "peer" with
// the sender's name, and the firmware is told to recreate its account.
func TestDeezerShareReceiveAccepts(t *testing.T) {
	deezerShareOutcomeWait = 0
	sender := newShareSpeaker(t, "Kitchen")
	keepDeezerLogin(t, sender.m)
	recv := newShareSpeaker(t, "Bedroom")
	recv.s.knownPeerFn = func(ip string) bool { return ip == "192.168.1.20" }
	recv.s.cachedPeersFn = func() []PeerLink {
		return []PeerLink{{Name: "Kitchen", URL: "http://192.168.1.20:8888/", IP: "192.168.1.20", Reachable: true}}
	}
	recv.needsDeezer(strings.ToUpper(deezerTestUser))

	w, ans := postShare(recv, "192.168.1.20:4000", sealFor(t, sender, recv, deezerTestUser), deezerShareUser)
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
	if sum := recv.m.KeptDeezerSummary(); sum.FromName != "Kitchen" {
		t.Fatalf("summary = %+v", sum)
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

// The "already kept" guard relaxes for an owner's copy only, and only while
// the receiver's Deezer does not work; the receiver's own login is then
// backed up (marked refused when Deezer refused it), never dropped.
func TestDeezerShareRelaxedGuardOnlyForANonWorkingReceiver(t *testing.T) {
	deezerShareOutcomeWait = 0
	sender := newShareSpeaker(t, "Kitchen")
	if err := os.WriteFile(sender.kept, []byte(`{"account":"`+deezerTestUser+`","secretType":"token","secret":"`+shareTestToken+`-sender","from":"registration"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	recv := newShareSpeaker(t, "Bedroom")
	keepDeezerLogin(t, recv.m)
	recv.s.knownPeerFn = func(string) bool { return true }

	// Works: never replaced, not even on an owner's copy.
	recv.works(deezerTestUser)
	if _, ans := postShare(recv, "192.168.1.20:4000", sealFor(t, sender, recv, deezerTestUser), deezerShareUser); ans.Reason != "ready" {
		t.Fatalf("working receiver: %+v", ans)
	}
	// Does not work, but no owner's copy: refused.
	recv.refuses(deezerTestUser)
	if _, ans := postShare(recv, "192.168.1.20:4000", sealFor(t, sender, recv, deezerTestUser), ""); ans.Reason != "kept" {
		t.Fatalf("not the owner's copy: %+v", ans)
	}
	if sum := recv.m.KeptDeezerSummary(); sum.Previous != 0 || sum.From != "registration" {
		t.Fatalf("a refused copy changed the kept login: %+v", sum)
	}
	// Does not work, owner's copy: taken, the old one backed up as refused.
	if _, ans := postShare(recv, "192.168.1.20:4000", sealFor(t, sender, recv, deezerTestUser), deezerShareUser); ans.Status != "accepted" {
		t.Fatalf("owner's copy: %+v", ans)
	}
	sum := recv.m.KeptDeezerSummary()
	if !sum.Kept || sum.From != "peer" || sum.Previous != 1 || sum.PreviousRefused != 1 {
		t.Fatalf("after the copy = %+v", sum)
	}
	prev, err := os.ReadFile(filepath.Join(filepath.Dir(recv.kept), "deezer-login-previous.json"))
	if err != nil || !strings.Contains(string(prev), shareTestToken) {
		t.Fatalf("the replaced login is not backed up: %v", err)
	}
	snap, _ := json.Marshal(recv.m.DeezerLoginSnapshot())
	noLeak(t, "the relaxed guard", recv.logs.String(), string(snap))
}

// recordingPeer serves a speaker's agent over HTTP and records every request
// body and answer, for the leak checks and the request counts.
type recordingPeer struct {
	mu        sync.Mutex
	posts     int
	summaries int
	traffic   bytes.Buffer
}

func (rp *recordingPeer) serve(sp shareSpeaker) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		rec := httptest.NewRecorder()
		switch r.URL.Path {
		case "/api/deezer/login-summary":
			sp.s.handleDeezerLoginSummary(rec, r)
		case "/api/deezer/kept-login":
			sp.s.handleDeezerKeptLogin(rec, r)
		default:
			http.NotFound(rec, r)
		}
		rp.mu.Lock()
		if r.URL.Path == "/api/deezer/kept-login" {
			rp.posts++
		} else {
			rp.summaries++
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

func (rp *recordingPeer) counts() (summaries, posts int) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return rp.summaries, rp.posts
}

func (rp *recordingPeer) seen() string {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return rp.traffic.String()
}

func peerLinkTo(srv *httptest.Server, name string, reachable bool) PeerLink {
	return PeerLink{Name: name, URL: srv.URL + "/", IP: "127.0.0.1", Reachable: reachable}
}

func hostOf(srv *httptest.Server) string {
	u, _ := url.Parse(srv.URL)
	return u.Host
}

func household(t *testing.T, sp shareSpeaker) (DeezerHousehold, string) {
	t.Helper()
	w := httptest.NewRecorder()
	sp.s.handleDeezerHousehold(w, nativeReq(http.MethodGet, "/api/deezer/household", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("household: %d %s", w.Code, w.Body.String())
	}
	var hh DeezerHousehold
	if err := json.Unmarshal(w.Body.Bytes(), &hh); err != nil {
		t.Fatal(err)
	}
	return hh, w.Body.String()
}

func rowNamed(t *testing.T, hh DeezerHousehold, name string) DeezerHouseholdRow {
	t.Helper()
	for _, r := range hh.Speakers {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no row %q in %+v", name, hh.Speakers)
	return DeezerHouseholdRow{}
}

// The overview names every speaker of the home with its Deezer state, its
// kept login's origin and backups, and whether it uses the same account; Copy
// applies only where this speaker keeps a login, the target uses the same
// account and its Deezer does not work. Nothing carries the login or the
// account, and reading it copies nothing.
func TestDeezerHouseholdOverviewAndCopyEligibility(t *testing.T) {
	deezerShareOutcomeWait = 0
	me := newShareSpeaker(t, "Kitchen")
	keepDeezerLogin(t, me.m)
	me.works(deezerTestUser)
	needs, ready, other, notSet := newShareSpeaker(t, "Bedroom"), newShareSpeaker(t, "Office"), newShareSpeaker(t, "Garage"), newShareSpeaker(t, "Hall")
	needs.needsDeezer(deezerTestUser)
	ready.works(deezerTestUser)
	keepDeezerLogin(t, ready.m)
	other.refuses("other@example.com")
	rps := map[string]*recordingPeer{}
	var links []PeerLink
	for name, sp := range map[string]shareSpeaker{"Bedroom": needs, "Office": ready, "Garage": other, "Hall": notSet} {
		rp := &recordingPeer{}
		srv := rp.serve(sp)
		t.Cleanup(srv.Close)
		rps[name] = rp
		links = append(links, peerLinkTo(srv, name, true))
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	links = append(links, PeerLink{Name: "Porch", URL: dead.URL + "/", IP: "127.0.0.1", Reachable: false})
	me.s.cachedPeersFn = func() []PeerLink { return links }

	hh, raw := household(t, me)
	if len(hh.Speakers) != 6 || !hh.Speakers[0].Self || hh.Speakers[0].Name != "Kitchen" {
		t.Fatalf("rows = %+v", hh.Speakers)
	}
	self := hh.Speakers[0]
	if self.State != "works" || !self.Kept || self.From != "registration" || self.KeptAt.IsZero() || self.CanCopy || self.CanUseOlder {
		t.Fatalf("self = %+v", self)
	}
	b := rowNamed(t, hh, "Bedroom")
	if b.State != "no-login" || b.Account != "same" || !b.CanCopy || b.Kept || b.ID == "" {
		t.Fatalf("Bedroom = %+v", b)
	}
	o := rowNamed(t, hh, "Office")
	if o.State != "works" || o.Account != "same" || o.CanCopy || !o.Kept {
		t.Fatalf("Office = %+v", o)
	}
	g := rowNamed(t, hh, "Garage")
	if g.State != "refused" || g.Account != "other" || g.CanCopy {
		t.Fatalf("Garage = %+v", g)
	}
	h := rowNamed(t, hh, "Hall")
	if h.State != "not-set-up" || h.Account != "" || h.CanCopy {
		t.Fatalf("Hall = %+v", h)
	}
	if p := rowNamed(t, hh, "Porch"); p.State != "unknown" || p.Why != "unreachable" || p.CanCopy {
		t.Fatalf("Porch = %+v", p)
	}
	if hh.CanCopy != 1 {
		t.Fatalf("canCopy = %d", hh.CanCopy)
	}
	for name, rp := range rps {
		if _, posts := rp.counts(); posts != 0 {
			t.Fatalf("reading the overview copied to %s", name)
		}
	}

	// A speaker whose own login Deezer refuses offers no Copy.
	me.refuses(deezerTestUser)
	if hh, _ := household(t, me); hh.CanCopy != 0 || rowNamed(t, hh, "Bedroom").CanCopy {
		t.Fatalf("a refused login is offered for copying: %+v", hh)
	}
	// Nor does a speaker that keeps none.
	bare := newShareSpeaker(t, "Attic")
	bare.works(deezerTestUser)
	bare.s.cachedPeersFn = me.s.cachedPeersFn
	if hh, _ := household(t, bare); hh.CanCopy != 0 {
		t.Fatalf("a speaker without a kept login offers Copy: %+v", hh)
	}
	var traffic []string
	for _, rp := range rps {
		traffic = append(traffic, rp.seen())
	}
	noLeak(t, "the overview", append(traffic, raw, me.logs.String(), needs.logs.String())...)
}

func copyCall(sp shareSpeaker, body string) (*httptest.ResponseRecorder, DeezerCopyAnswer) {
	w := httptest.NewRecorder()
	sp.s.handleDeezerCopy(w, nativeReq(http.MethodPost, "/api/deezer/copy", body, "192.168.1.5:5000", "application/json"))
	var ans DeezerCopyAnswer
	_ = json.Unmarshal(w.Body.Bytes(), &ans)
	return w, ans
}

// Copy sends the kept login to the one speaker named, which takes it and
// hands it to its firmware; Copy to all reaches only the speakers that need
// it. Nothing in the traffic, the answers, the logs or the debug sections
// carries the login or the account.
func TestDeezerCopyOnTheOwnersPress(t *testing.T) {
	deezerShareOutcomeWait = 0
	me := newShareSpeaker(t, "Kitchen")
	keepDeezerLogin(t, me.m)
	me.works(deezerTestUser)
	a, b, c := newShareSpeaker(t, "Bedroom"), newShareSpeaker(t, "Office"), newShareSpeaker(t, "Garage")
	a.needsDeezer(deezerTestUser)
	b.needsDeezer(deezerTestUser)
	c.needsDeezer("other@example.com")
	rpA, rpB, rpC := &recordingPeer{}, &recordingPeer{}, &recordingPeer{}
	srvA, srvB, srvC := rpA.serve(a), rpB.serve(b), rpC.serve(c)
	defer srvA.Close()
	defer srvB.Close()
	defer srvC.Close()
	me.s.cachedPeersFn = func() []PeerLink {
		return []PeerLink{peerLinkTo(srvA, "Bedroom", true), peerLinkTo(srvB, "Office", true), peerLinkTo(srvC, "Garage", true)}
	}

	w, ans := copyCall(me, `{"target":"`+hostOf(srvA)+`"}`)
	a.s.deezerShare.wg.Wait()
	if w.Code != http.StatusOK || ans.Copied != 1 || len(ans.Results) != 1 || ans.Results[0].Outcome != "accepted" || ans.Results[0].Name != "Bedroom" {
		t.Fatalf("copy to one: %d %s", w.Code, w.Body.String())
	}
	if st := a.m.DeezerLoginSnapshot(); !st.Kept || st.From != "peer" {
		t.Fatalf("Bedroom = %+v", st)
	}
	if a.box.removeCalls != 1 || a.box.notifyCalls != 1 {
		t.Fatalf("Bedroom recreate: %d removals, %d notifications", a.box.removeCalls, a.box.notifyCalls)
	}
	if _, posts := rpB.counts(); posts != 0 {
		t.Fatal("a copy to one reached another speaker")
	}
	sh := me.m.DeezerLoginSnapshot().Shares
	if sh == nil || len(sh.Sent) != 1 || sh.Sent[0].Outcome != "accepted" || sh.Sent[0].Trigger != "copy" {
		t.Fatalf("sent record = %+v", sh)
	}

	// Copy to all: Office takes it, Garage uses another account, Bedroom
	// is in the middle of signing in with the copy it took.
	_, all := copyCall(me, `{"all":true}`)
	b.s.deezerShare.wg.Wait()
	got := map[string]string{}
	for _, r := range all.Results {
		got[r.Name] = r.Outcome
	}
	if all.Copied != 1 || got["Office"] != "accepted" || got["Garage"] != "hash-mismatch" || got["Bedroom"] != "not-needed:connecting" {
		t.Fatalf("copy to all = %+v", all)
	}
	if _, posts := rpC.counts(); posts != 0 {
		t.Fatal("another account's speaker got the login")
	}

	// Refusals of the endpoint itself.
	if w, _ := copyCall(me, `{"target":"192.0.2.77:8888"}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown target: %d", w.Code)
	}
	if w, _ := copyCall(me, `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no target: %d", w.Code)
	}
	w = httptest.NewRecorder()
	me.s.handleDeezerCopy(w, nativeReq(http.MethodPost, "/api/deezer/copy", `{"all":true}`, "192.168.1.5:5000", "text/plain"))
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form post: %d", w.Code)
	}
	w = httptest.NewRecorder()
	me.s.handleDeezerCopy(w, nativeReq(http.MethodPost, "/api/deezer/copy", `{"all":true}`, "203.0.113.7:5000", "application/json"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-LAN caller: %d", w.Code)
	}
	bare := newShareSpeaker(t, "Attic")
	bare.s.cachedPeersFn = me.s.cachedPeersFn
	if w, _ := copyCall(bare, `{"all":true}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "no-kept-login") {
		t.Fatalf("nothing kept: %d %s", w.Code, w.Body.String())
	}

	snapMe, _ := json.Marshal(me.m.DeezerLoginSnapshot())
	snapA, _ := json.Marshal(a.m.DeezerLoginSnapshot())
	noLeak(t, "the copy", rpA.seen(), rpB.seen(), rpC.seen(), me.logs.String(), a.logs.String(), b.logs.String(),
		string(snapMe), string(snapA), w.Body.String())
}

// Nothing is copied on its own: not at the agent start, not on Check again
// on either side, and no code path but the owner's Copy reaches the post.
func TestDeezerNoAutomaticCopy(t *testing.T) {
	deezerShareOutcomeWait = 0
	keeper := newShareSpeaker(t, "Kitchen")
	keepDeezerLogin(t, keeper.m)
	keeper.needsDeezer(deezerTestUser)
	needy := newShareSpeaker(t, "Bedroom")
	needy.needsDeezer(deezerTestUser)
	keeperRP, needyRP := &recordingPeer{}, &recordingPeer{}
	keeperSrv, needySrv := keeperRP.serve(keeper), needyRP.serve(needy)
	defer keeperSrv.Close()
	defer needySrv.Close()
	keeper.s.cachedPeersFn = func() []PeerLink { return []PeerLink{peerLinkTo(needySrv, "Bedroom", true)} }
	needy.s.cachedPeersFn = func() []PeerLink { return []PeerLink{peerLinkTo(keeperSrv, "Kitchen", true)} }

	keeper.s.DeezerRecreateAtStart(t.Context(), 0, 0)
	needy.s.DeezerRecreateAtStart(t.Context(), 0, 0)
	deezerRecheck(keeper.s, "application/json")
	deezerRecheck(needy.s, "application/json")
	if s, p := needyRP.counts(); s != 0 || p != 0 {
		t.Fatalf("the keeper called the needy speaker on its own: %d reads, %d posts", s, p)
	}
	if s, p := keeperRP.counts(); s != 0 || p != 0 {
		t.Fatalf("the needy speaker called the keeper on its own: %d reads, %d posts", s, p)
	}
	if _, err := os.Stat(needy.kept); !os.IsNotExist(err) {
		t.Fatal("the needy speaker got a login nobody pressed for")
	}

	// Only handleDeezerCopy calls copyDeezerTo, and only copyDeezerTo seals
	// and posts a login.
	callers := deezerCallersIn(t, "deezershare.go")
	for callee, want := range map[string]string{
		"copyDeezerTo":        "handleDeezerCopy",
		"postDeezerShare":     "copyDeezerTo",
		"SealKeptDeezerLogin": "copyDeezerTo",
	} {
		got := callers[callee]
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s is called from %v, want only %s", callee, got, want)
		}
	}
	for _, f := range []string{"deezer.go", "server.go", "options.go"} {
		for callee, from := range deezerCallersIn(t, f) {
			if callee == "copyDeezerTo" || callee == "postDeezerShare" || callee == "SealKeptDeezerLogin" {
				t.Errorf("%s calls %s from %v", f, callee, from)
			}
		}
	}
}

// deezerCallersIn maps each called function or method name in file to the
// functions that call it.
func deezerCallersIn(t *testing.T, file string) map[string][]string {
	t.Helper()
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch c := call.Fun.(type) {
			case *ast.Ident:
				name = c.Name
			case *ast.SelectorExpr:
				name = c.Sel.Name
			}
			if name != "" {
				out[name] = append(out[name], fn.Name.Name)
			}
			return true
		})
	}
	return out
}

// The login summary another agent reads names neither the account nor the
// login, and says the speaker's state.
func TestDeezerLoginSummary(t *testing.T) {
	sp := newShareSpeaker(t, "Kitchen")
	get := func() DeezerLoginSummary {
		t.Helper()
		w := httptest.NewRecorder()
		sp.s.handleDeezerLoginSummary(w, nativeReq(http.MethodGet, "/api/deezer/login-summary", "", "192.168.1.20:4000", ""))
		if w.Code != http.StatusOK {
			t.Fatalf("summary: %d", w.Code)
		}
		noLeak(t, "the summary", w.Body.String())
		var n DeezerLoginSummary
		_ = json.Unmarshal(w.Body.Bytes(), &n)
		return n
	}
	if n := get(); n.State != "not-set-up" || n.AccountHash != "" || n.Name != "Kitchen" {
		t.Fatalf("no Deezer: %+v", n)
	}
	sp.needsDeezer(deezerTestUser)
	n := get()
	if n.State != "no-login" || n.Key == "" || n.AccountHash != marge.DeezerShareAccountHash(n.Key, deezerTestUser) {
		t.Fatalf("no login: %+v", n)
	}
	sp.box.sourcesErr = io.ErrUnexpectedEOF
	if n := get(); n.State != "unknown" {
		t.Fatalf("unreadable: %+v", n)
	}
	sp.box.sourcesErr = nil
	keepDeezerLogin(t, sp.m)
	sp.refuses(deezerTestUser)
	if n := get(); n.State != "refused" || !n.Kept || n.From != "registration" {
		t.Fatalf("refused: %+v", n)
	}
	w := httptest.NewRecorder()
	sp.s.handleDeezerLoginSummary(w, nativeReq(http.MethodGet, "/api/deezer/login-summary", "", "203.0.113.7:4000", ""))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-LAN caller: %d", w.Code)
	}
}

// Use the older login: only while Deezer refuses the current one, it puts
// the backed-up one back, hands it to the firmware, and never swaps back.
func TestDeezerUseOlderLogin(t *testing.T) {
	sp := newShareSpeaker(t, "Kitchen")
	use := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		sp.s.handleDeezerUseOlder(w, nativeReq(http.MethodPost, "/api/deezer/use-older", "{}", "192.168.1.5:5000", "application/json"))
		return w
	}
	// An older login for the account (kept first), then the current one.
	if err := os.WriteFile(sp.kept, []byte(`{"account":"`+deezerTestUser+`","secretType":"token","secret":"`+shareTestToken+`-older","from":"sources-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	keepDeezerLogin(t, sp.m)
	sp.works(deezerTestUser)
	if w := use(); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "not-refused") {
		t.Fatalf("working login: %d %s", w.Code, w.Body.String())
	}
	sp.refuses(deezerTestUser)
	hh, hhRaw := household(t, sp)
	if !hh.Speakers[0].CanUseOlder || hh.Speakers[0].Previous != 1 {
		t.Fatalf("self row = %+v", hh.Speakers[0])
	}
	w := use()
	if w.Code != http.StatusOK {
		t.Fatalf("use older: %d %s", w.Code, w.Body.String())
	}
	v := deezerViewOf(t, w, "")
	if v.Recreate == nil || !v.Recreate.Ran || v.Login.From != "restored" || v.Rejected {
		t.Fatalf("answer = %+v", v)
	}
	if sp.box.removeCalls != 1 || sp.box.notifyCalls != 1 {
		t.Fatalf("recreate: %d removals, %d notifications", sp.box.removeCalls, sp.box.notifyCalls)
	}
	// The refused one is backed up as refused and never offered back.
	if hh, _ := household(t, sp); hh.Speakers[0].CanUseOlder || hh.Speakers[0].PreviousRefused != 1 || hh.Speakers[0].OlderUsable {
		t.Fatalf("after the swap = %+v", hh.Speakers[0])
	}
	if _, err := sp.m.UseOlderDeezerLogin(); err == nil {
		t.Fatal("a second swap went back to the refused login")
	}
	w2 := httptest.NewRecorder()
	sp.s.handleDeezerUseOlder(w2, nativeReq(http.MethodPost, "/api/deezer/use-older", "{}", "192.168.1.5:5000", "text/plain"))
	if w2.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form post: %d", w2.Code)
	}
	snap, _ := json.Marshal(sp.m.DeezerLoginSnapshot())
	noLeak(t, "use older", w.Body.String(), hhRaw, sp.logs.String(), string(snap))
}

// closedPort is a local port nothing listens on: a connection is refused.
func closedPort(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	srv.Close()
	return port
}

func portOf(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(u.Port())
	return p
}

// A peer is tried on its listed port first and then on the other agent
// ports (the :8888 then :17008 order), not given up as unreachable.
func TestDeezerShareFallsBackToTheSecondAgentPort(t *testing.T) {
	deezerShareOutcomeWait = 0
	sender := newShareSpeaker(t, "Kitchen")
	keepDeezerLogin(t, sender.m)
	recv := newShareSpeaker(t, "Bedroom")
	recv.needsDeezer(deezerTestUser)
	rp := &recordingPeer{}
	srv := rp.serve(recv)
	defer srv.Close()
	dead := closedPort(t)
	deezerShareAgentPorts = []int{portOf(t, srv)}
	sender.s.cachedPeersFn = func() []PeerLink {
		return []PeerLink{{Name: "Bedroom", URL: "http://127.0.0.1:" + strconv.Itoa(dead) + "/", IP: "127.0.0.1", Reachable: true}}
	}
	_, ans := copyCall(sender, `{"all":true}`)
	recv.s.deezerShare.wg.Wait()
	if reads, posts := rp.counts(); reads != 1 || posts != 1 || ans.Copied != 1 {
		t.Fatalf("over the second port: %d reads, %d posts, %+v", reads, posts, ans)
	}

	deezerShareAgentPorts = []int{8888, 17008}
	got := deezerShareBases("192.0.2.5", 17008)
	if len(got) != 2 || got[0] != "http://192.0.2.5:17008" || got[1] != "http://192.0.2.5:8888" {
		t.Fatalf("known 17008: %v", got)
	}
	got = deezerShareBases("192.0.2.5", 0)
	if len(got) != 2 || got[0] != "http://192.0.2.5:8888" || got[1] != "http://192.0.2.5:17008" {
		t.Fatalf("unknown port: %v", got)
	}
	if p, ok := deezerSharePeerOf(PeerLink{URL: "http://192.0.2.5:8888/", IP: "192.0.2.5"}); !ok || len(p.bases) != 2 || p.bases[1] != "http://192.0.2.5:17008" || p.id != "192.0.2.5:8888" {
		t.Fatalf("roster entry on 8888: %+v", p)
	}
}

// Deezer refuses the current login only when the firmware's 3330 came after
// that login was kept: a login just copied over or put back is not refused
// by a code from before it.
func TestDeezerLoginRefusedOnlyAfterTheKeptLogin(t *testing.T) {
	now := time.Now()
	at := func(code string, d time.Duration) *marge.DeezerRetry {
		return &marge.DeezerRetry{Code: code, At: now.Add(d)}
	}
	for name, c := range map[string]struct {
		login marge.DeezerLoginStatus
		want  bool
	}{
		"no retry":           {marge.DeezerLoginStatus{Kept: true, KeptAt: now}, false},
		"3351":               {marge.DeezerLoginStatus{Kept: true, KeptAt: now, LastRetry: at("3351", time.Minute)}, false},
		"3330 after":         {marge.DeezerLoginStatus{Kept: true, KeptAt: now, LastRetry: at("3330", time.Minute)}, true},
		"3330 before":        {marge.DeezerLoginStatus{Kept: true, KeptAt: now, LastRetry: at("3330", -time.Minute)}, false},
		"3330, nothing kept": {marge.DeezerLoginStatus{LastRetry: at("3330", -time.Hour)}, true},
	} {
		if got := deezerLoginRefused(c.login); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
