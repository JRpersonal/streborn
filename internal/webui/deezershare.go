package webui

// Deezer: share a kept login between the speakers of one home
// (docs/streaming/deezer.md, "Sharing a kept login within a home").
//
// A speaker whose Deezer login is gone and that keeps no copy can never get a
// new one (Deezer issues logins only to Bose's app). When another speaker of
// the home keeps a login for the same account, its agent copies it over:
//
// GET  /api/deezer/login-need  the need signal, secret-free: whether this
//                              speaker lists Deezer without a working login
//                              and keeps none, and if so a hash of the account
//                              (salted with this run's share key) and the key.
//                              Never the account in clear.
// POST /api/deezer/kept-login  write-only: a peer's kept login, sealed to the
//                              share key (internal/marge/deezershare.go). Kept
//                              only from a known peer (or loopback), only for
//                              an account this speaker lists without a working
//                              login, and never over a login kept already.
//                              Answers {"status","reason"}, never the login.
// POST /api/deezer/offer-request  a speaker that needs a login asks this one
//                              to offer its kept login (no body secrets).
//                              From a known peer (or loopback) only, at most
//                              once per requester every 3 minutes; runs the
//                              normal offer towards that requester alone.
//
// The sender side runs at existing moments, never on a timer: once after
// the agent start (after the start recreate), when a peer comes up in the
// peer roster, on the Deezer card's Check again, and when a peer that needs a
// login asks (its own Check again). It posts to a peer at most once per agent
// start, and only to a peer whose need signal names the kept account. Every
// call tries the peer's agent on :8888 and then on :17008 (the REDIRECT the
// BCO and whitelisted chassis are reached on), with a short per-port budget.
// Untested on hardware: whether one speaker's token plays on another.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/anonymise"
	"github.com/JRpersonal/streborn/internal/marge"
)

// deezerShareRecheckGap is the shortest gap between two need checks of the
// same peer when it comes up again in the roster (a flapping peer).
const deezerShareRecheckGap = 3 * time.Minute

// deezerShareOutcomeWait is how long after a shared login's recreate the
// receiver reads its Deezer source once more, for the log. A variable so the
// tests need not wait.
var deezerShareOutcomeWait = 30 * time.Second

// Triggers of the sending side.
const (
	deezerShareTriggerStart   = "start"
	deezerShareTriggerPeer    = "peer-seen"
	deezerShareTriggerCheck   = "check-again"
	deezerShareTriggerRequest = "offer-request"
)

// deezerShareAgentPorts are the ports a peer's agent answers on: :8888
// directly (sm2) and the :17008 REDIRECT (BCO and whitelisted chassis, see
// docs/MODEL-VARIANTS.md). A variable so the tests can point it at their own
// servers.
var deezerShareAgentPorts = []int{8888, 17008}

// deezerSharePortBudget is how long one port may take to accept the
// connection before the next one is tried: an open LAN port answers in
// milliseconds, a blocked one black-holes the SYN.
const deezerSharePortBudget = 1500 * time.Millisecond

// deezerShareDefaultClient makes the agent-to-agent calls: the short connect
// budget per port, no proxy, no idle connections kept open.
var deezerShareDefaultClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext:       (&net.Dialer{Timeout: deezerSharePortBudget}).DialContext,
		DisableKeepAlives: true,
	},
}

// deezerShareSender is the sending side's state for this agent run.
type deezerShareSender struct {
	// round keeps one offer at a time, so a peer that comes up during the
	// start round is not asked twice at once.
	round sync.Mutex
	mu    sync.Mutex
	// offered holds the peers a login was posted to this run (once each).
	offered map[string]bool
	// checkedAt is when a peer's need signal was last read.
	checkedAt map[string]time.Time
	// askedAt is when this speaker last asked a peer to offer (the
	// requester's side); requestedAt when a peer last asked this one (the
	// offering side). Both keep one request per peer every
	// deezerShareRecheckGap.
	askedAt     map[string]time.Time
	requestedAt map[string]time.Time
	// wg tracks the goroutines this side starts (the receiver's recreate,
	// Check again's offer round), so the tests can wait for them.
	wg sync.WaitGroup
}

// DeezerLoginNeed is GET /api/deezer/login-need's answer.
type DeezerLoginNeed struct {
	Needs bool `json:"needs"`
	// Why says why not: "kept" (a login is kept already), "ready" (Deezer
	// works), "not-listed" (no Deezer account on the speaker), "no-account"
	// (Deezer listed without an account), "unreadable" (the speaker's
	// sources could not be read), "no-key".
	Why string `json:"why,omitempty"`
	// AccountHash is marge.DeezerShareAccountHash(Key, account).
	AccountHash string `json:"accountHash,omitempty"`
	// Key is this run's share public key.
	Key string `json:"key,omitempty"`
}

// deezerShareAnswer is POST /api/deezer/kept-login's answer.
type deezerShareAnswer struct {
	// Status is "accepted" or "refused".
	Status string `json:"status"`
	// Reason is the refusal's code: "unknown-caller", "bad-request",
	// "already-kept", "ready", "not-listed", "no-account", "unreadable",
	// "account-mismatch", "undecryptable", "malformed", "save-failed",
	// "not-configured".
	Reason string `json:"reason,omitempty"`
}

// deezerPeerName is a peer's pseudonym for the log and the debug section.
func deezerPeerName(ip string) string {
	if ip == "" {
		return "peer#unknown"
	}
	return "peer#" + anonymise.HashShort(ip)
}

// deezerLoginNeed works out this speaker's need signal. listed are the
// Deezer accounts it lists without a working login, unmasked, agent side only.
func (s *Server) deezerLoginNeed(ctx context.Context) (need DeezerLoginNeed, listed []string) {
	if s.deezer.KeptDeezerAccount() != "" {
		return DeezerLoginNeed{Why: "kept"}, nil
	}
	srcs, err := s.nativeBox().GetSources(ctx)
	if err != nil {
		return DeezerLoginNeed{Why: "unreadable"}, nil
	}
	seen := false
	for _, src := range srcs {
		if !strings.EqualFold(src.Source, deezerSource) {
			continue
		}
		seen = true
		acct := strings.TrimSpace(src.SourceAccount)
		if strings.EqualFold(src.Status, "READY") && acct != "" {
			return DeezerLoginNeed{Why: "ready"}, nil
		}
		if acct != "" {
			listed = append(listed, acct)
		}
	}
	switch {
	case !seen:
		return DeezerLoginNeed{Why: "not-listed"}, nil
	case len(listed) == 0:
		return DeezerLoginNeed{Why: "no-account"}, nil
	}
	key := s.deezer.DeezerShareKey()
	if key == "" {
		return DeezerLoginNeed{Why: "no-key"}, nil
	}
	return DeezerLoginNeed{Needs: true, Key: key, AccountHash: marge.DeezerShareAccountHash(key, listed[0])}, listed
}

// callerIP is the caller's address without the port.
func callerIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return strings.TrimSpace(host)
}

// isKnownDeezerPeer reports whether the caller is loopback or a speaker in
// this agent's peer roster.
func (s *Server) isKnownDeezerPeer(ip string) bool {
	if p := net.ParseIP(ip); p != nil && p.IsLoopback() {
		return true
	}
	return s.knownPeerFn != nil && s.knownPeerFn(ip)
}

// handleDeezerLoginNeed is GET /api/deezer/login-need.
func (s *Server) handleDeezerLoginNeed(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "music service settings only allowed from LAN", http.StatusForbidden)
		return
	}
	if s.deezer == nil {
		http.Error(w, "not available on this build", http.StatusNotImplemented)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	need, _ := s.deezerLoginNeed(ctx)
	s.logger.Info("deezer share: a speaker asked whether this one needs a Deezer login",
		"peer", deezerPeerName(callerIP(r.RemoteAddr)), "needs", need.Needs, "why", need.Why)
	writeJSON(w, http.StatusOK, need)
}

// handleDeezerKeptLogin is POST /api/deezer/kept-login, the receiving side.
func (s *Server) handleDeezerKeptLogin(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "music service settings only allowed from LAN", http.StatusForbidden)
		return
	}
	if s.deezer == nil {
		http.Error(w, "not available on this build", http.StatusNotImplemented)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := callerIP(r.RemoteAddr)
	peer := deezerPeerName(ip)
	refuse := func(status int, reason string) {
		s.logger.Info("deezer share: refused a Deezer login another speaker offered", "peer", peer, "reason", reason)
		s.deezer.NoteDeezerShare(false, marge.DeezerShareEvent{Peer: peer, Outcome: "refused:" + reason})
		writeJSON(w, status, deezerShareAnswer{Status: "refused", Reason: reason})
	}
	if !s.isKnownDeezerPeer(ip) {
		refuse(http.StatusForbidden, "unknown-caller")
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		refuse(http.StatusUnsupportedMediaType, "bad-request")
		return
	}
	var body struct {
		Sealed marge.SealedDeezerLogin `json:"sealed"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil || body.Sealed.Box == "" {
		refuse(http.StatusBadRequest, "bad-request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	need, listed := s.deezerLoginNeed(ctx)
	if !need.Needs {
		refuse(http.StatusConflict, need.Why)
		return
	}
	if _, err := s.deezer.AdoptPeerDeezerLogin(body.Sealed, listed, peer); err != nil {
		code := err.Error()
		status := http.StatusConflict
		if errors.Is(err, marge.ErrDeezerShareMalformed) || errors.Is(err, marge.ErrDeezerShareUndecryptable) {
			status = http.StatusBadRequest
		}
		if !isDeezerShareCode(err) {
			code = "save-failed"
		}
		refuse(status, code)
		return
	}
	s.logger.Info("deezer share: accepted a Deezer login another speaker of the home offered, handing it to the firmware", "peer", peer)
	s.deezer.NoteDeezerShare(false, marge.DeezerShareEvent{Peer: peer, Outcome: "accepted"})
	writeJSON(w, http.StatusOK, deezerShareAnswer{Status: "accepted"})
	// The recreate talks to the firmware for some seconds; the sender need
	// not wait for it. It logs its own steps and outcome.
	s.deezerShare.wg.Add(1)
	go func() {
		defer s.deezerShare.wg.Done()
		s.deezerRecreateAndReport(context.Background(), "peer", deezerShareOutcomeWait)
	}()
}

// isDeezerShareCode reports whether err is one of the stand-in's reason codes.
func isDeezerShareCode(err error) bool {
	for _, e := range []error{marge.ErrDeezerShareNoKeptLogin, marge.ErrDeezerShareHashMismatch,
		marge.ErrDeezerShareAlreadyKept, marge.ErrDeezerShareUndecryptable, marge.ErrDeezerShareMalformed,
		marge.ErrDeezerShareAccount, marge.ErrDeezerShareSaveFailed, marge.ErrDeezerShareOff} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// deezerSharePeer is one peer to call.
type deezerSharePeer struct {
	ip string
	// bases are the peer's agent addresses in the order they are tried
	// (http://ip:port): the port the roster knows first, then the others.
	bases     []string
	reachable bool
}

// deezerShareBases lists the addresses a peer's agent is tried on: port
// first when it is known (not 0), then every other agent port.
func deezerShareBases(ip string, port int) []string {
	var out []string
	if port > 0 && port < 65536 {
		out = append(out, "http://"+net.JoinHostPort(ip, strconv.Itoa(port)))
	}
	for _, p := range deezerShareAgentPorts {
		if p != port {
			out = append(out, "http://"+net.JoinHostPort(ip, strconv.Itoa(p)))
		}
	}
	return out
}

// deezerSharePeerOf turns a roster entry into a target; ok is false for an
// entry without a usable address.
func deezerSharePeerOf(p PeerLink) (deezerSharePeer, bool) {
	u, err := url.Parse(strings.TrimSpace(p.URL))
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return deezerSharePeer{}, false
	}
	ip := strings.TrimSpace(p.IP)
	if ip == "" {
		ip = u.Hostname()
	}
	if net.ParseIP(ip) == nil || !strings.EqualFold(ip, u.Hostname()) {
		return deezerSharePeer{}, false
	}
	port, _ := strconv.Atoi(u.Port())
	return deezerSharePeer{ip: ip, bases: deezerShareBases(ip, port), reachable: p.Reachable}, true
}

// deezerSharePeerAt is the target for a peer known only by its address (a
// requester): its roster entry's port first when the roster has one.
func (s *Server) deezerSharePeerAt(ip string) deezerSharePeer {
	if s.cachedPeersFn != nil {
		for _, p := range s.cachedPeersFn() {
			if t, ok := deezerSharePeerOf(p); ok && t.ip == ip {
				t.reachable = true
				return t
			}
		}
	}
	return deezerSharePeer{ip: ip, bases: deezerShareBases(ip, 0), reachable: true}
}

// deezerShareBlocked says why this speaker has no login worth offering:
// "not-configured", "no-kept-login", "own-login-rejected" (its firmware's last
// relogin retry was 3330), or "" when it has one.
func (s *Server) deezerShareBlocked() string {
	switch {
	case s.deezer == nil:
		return "not-configured"
	case s.deezer.KeptDeezerAccount() == "":
		return "no-kept-login"
	}
	if st := s.deezer.DeezerLoginSnapshot(); st.LastRetry != nil && st.LastRetry.Code == deezerRetryRejected {
		return "own-login-rejected"
	}
	return ""
}

// deezerShareReady reports whether this speaker has a login worth offering,
// logging why not. A peer coming up logs at debug level (it is frequent and
// most speakers keep no Deezer login).
func (s *Server) deezerShareReady(trigger string) bool {
	why := s.deezerShareBlocked()
	switch why {
	case "":
		return true
	case "not-configured":
		return false
	}
	logf := s.logger.Info
	if trigger == deezerShareTriggerPeer {
		logf = s.logger.Debug
	}
	if why == "no-kept-login" {
		logf("deezer share: not offering a login to other speakers, this speaker keeps no Deezer login", "trigger", trigger)
	} else {
		logf("deezer share: not offering a login to other speakers, Deezer rejected this speaker's own kept login (3330)", "trigger", trigger)
	}
	return false
}

// OfferKeptDeezerLogin offers the kept login to every speaker in the peer
// roster that needs one for the same account: once after the agent start
// and on Check again. It never runs on a timer.
func (s *Server) OfferKeptDeezerLogin(ctx context.Context, trigger string) {
	if !s.deezerShareReady(trigger) {
		return
	}
	if s.cachedPeersFn == nil {
		s.logger.Info("deezer share: not offering, no peer roster", "trigger", trigger)
		return
	}
	s.deezerShare.round.Lock()
	defer s.deezerShare.round.Unlock()
	peers := s.cachedPeersFn()
	s.logger.Info("deezer share: offering the kept Deezer login to speakers that need it", "trigger", trigger, "peers", len(peers))
	for _, p := range peers {
		t, ok := deezerSharePeerOf(p)
		if !ok {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		s.offerDeezerToPeer(ctx, t, trigger)
	}
}

// OfferKeptDeezerLoginToPeer is the roster's hook for a peer that came up
// (new, or reachable again): it offers the kept login to that one peer when
// it needs one. port 0 means not known: both agent ports are tried.
func (s *Server) OfferKeptDeezerLoginToPeer(ctx context.Context, ip string, port int) {
	if !s.deezerShareReady(deezerShareTriggerPeer) || net.ParseIP(ip) == nil {
		return
	}
	s.deezerShare.round.Lock()
	defer s.deezerShare.round.Unlock()
	s.offerDeezerToPeer(ctx, deezerSharePeer{ip: ip, bases: deezerShareBases(ip, port), reachable: true}, deezerShareTriggerPeer)
}

// initLocked makes the state's maps. The caller holds st.mu.
func (st *deezerShareSender) initLocked() {
	if st.offered == nil {
		st.offered = map[string]bool{}
		st.checkedAt = map[string]time.Time{}
		st.askedAt = map[string]time.Time{}
		st.requestedAt = map[string]time.Time{}
	}
}

// offerDeezerToPeer runs one peer's offer. The caller holds the round lock.
func (s *Server) offerDeezerToPeer(ctx context.Context, p deezerSharePeer, trigger string) {
	peer := deezerPeerName(p.ip)
	note := func(outcome string) {
		s.deezer.NoteDeezerShare(true, marge.DeezerShareEvent{Peer: peer, Trigger: trigger, Outcome: outcome})
	}
	st := &s.deezerShare
	st.mu.Lock()
	st.initLocked()
	if st.offered[p.ip] {
		st.mu.Unlock()
		s.logger.Debug("deezer share: not offering, a login went to this speaker already since the agent started", "peer", peer, "trigger", trigger)
		return
	}
	if trigger == deezerShareTriggerPeer && time.Since(st.checkedAt[p.ip]) < deezerShareRecheckGap {
		st.mu.Unlock()
		s.logger.Debug("deezer share: not asking, this speaker was asked moments ago", "peer", peer)
		return
	}
	st.checkedAt[p.ip] = time.Now()
	st.mu.Unlock()

	if !p.reachable {
		// Series-I speakers cannot reach each other's agent; the app does
		// not relay this, so such a pair is skipped.
		s.logger.Info("deezer share: not offering, the speaker's agent is not reachable from here (series-I speakers cannot reach each other's agent)",
			"peer", peer, "trigger", trigger)
		note("unreachable")
		return
	}
	need, base, outcome := s.fetchDeezerNeed(ctx, p.bases)
	if outcome != "" {
		s.logger.Info("deezer share: not offering, the speaker did not answer the need check",
			"peer", peer, "trigger", trigger, "outcome", outcome, "portsTried", len(p.bases))
		note(outcome)
		return
	}
	if !need.Needs {
		s.logger.Info("deezer share: not offering, the speaker does not need a Deezer login",
			"peer", peer, "trigger", trigger, "why", deezerShareCode(need.Why))
		note("not-needed:" + deezerShareCode(need.Why))
		return
	}
	sealed, err := s.deezer.SealKeptDeezerLogin(need.Key, need.AccountHash)
	switch {
	case errors.Is(err, marge.ErrDeezerShareHashMismatch):
		s.logger.Info("deezer share: not offering, the speaker needs a login for another Deezer account",
			"peer", peer, "trigger", trigger)
		note("hash-mismatch")
		return
	case err != nil:
		code := "seal-failed"
		if isDeezerShareCode(err) {
			code = err.Error()
		}
		s.logger.Info("deezer share: not offering, the login could not be sealed for the speaker",
			"peer", peer, "trigger", trigger, "reason", code)
		note("refused:" + code)
		return
	}
	// Once per agent start, whatever the answer.
	st.mu.Lock()
	st.offered[p.ip] = true
	st.mu.Unlock()
	ans, outcome := s.postDeezerShare(ctx, base, sealed)
	switch {
	case outcome != "":
		s.logger.Info("deezer share: sent the kept login, the speaker did not take it",
			"peer", peer, "trigger", trigger, "outcome", outcome)
		note(outcome)
	case ans.Status == "accepted":
		s.logger.Info("deezer share: sent the kept login, the speaker accepted it and hands it to its firmware",
			"peer", peer, "trigger", trigger)
		note("accepted")
	default:
		s.logger.Info("deezer share: sent the kept login, the speaker refused it",
			"peer", peer, "trigger", trigger, "reason", deezerShareCode(ans.Reason))
		note("refused:" + deezerShareCode(ans.Reason))
	}
}

// RequestDeezerOffers is the pull behind Check again on a speaker that needs a
// login: it asks each reachable speaker in the roster, at most once every 3
// minutes each, to offer its kept login (POST /api/deezer/offer-request). A
// speaker that keeps one for the same account then runs its normal offer
// towards this one. No timer.
func (s *Server) RequestDeezerOffers(ctx context.Context) {
	if s.deezer == nil {
		return
	}
	need, _ := s.deezerLoginNeed(ctx)
	if !need.Needs {
		s.logger.Info("deezer share: not asking other speakers for a login, this speaker does not need one", "why", need.Why)
		return
	}
	if s.cachedPeersFn == nil {
		s.logger.Info("deezer share: not asking other speakers for a login, no peer roster")
		return
	}
	peers := s.cachedPeersFn()
	s.logger.Info("deezer share: this speaker needs a Deezer login, asking the other speakers to offer theirs", "peers", len(peers))
	for _, p := range peers {
		t, ok := deezerSharePeerOf(p)
		if !ok {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		s.askDeezerOffer(ctx, t)
	}
}

// askDeezerOffer asks one peer to offer its kept login.
func (s *Server) askDeezerOffer(ctx context.Context, p deezerSharePeer) {
	peer := deezerPeerName(p.ip)
	note := func(outcome string) {
		s.deezer.NoteDeezerShareRequest(true, marge.DeezerShareEvent{Peer: peer, Trigger: deezerShareTriggerCheck, Outcome: outcome})
	}
	st := &s.deezerShare
	st.mu.Lock()
	st.initLocked()
	if time.Since(st.askedAt[p.ip]) < deezerShareRecheckGap {
		st.mu.Unlock()
		s.logger.Info("deezer share: not asking, this speaker was asked for a login moments ago", "peer", peer)
		return
	}
	st.askedAt[p.ip] = time.Now()
	st.mu.Unlock()
	if !p.reachable {
		s.logger.Info("deezer share: not asking, the speaker's agent is not reachable from here (series-I speakers cannot reach each other's agent)", "peer", peer)
		note("unreachable")
		return
	}
	_, status, body, ok := s.deezerShareCall(ctx, p.bases, http.MethodPost, "/api/deezer/offer-request", []byte("{}"), 10*time.Second)
	if !ok {
		s.logger.Info("deezer share: the speaker did not answer the request for a login", "peer", peer, "portsTried", len(p.bases))
		note("unreachable")
		return
	}
	var ans deezerShareAnswer
	if status == http.StatusNotFound || json.Unmarshal(body, &ans) != nil || ans.Status == "" {
		s.logger.Info("deezer share: the speaker's agent predates sharing a Deezer login", "peer", peer, "status", status)
		note("old-agent")
		return
	}
	if ans.Status == "offering" {
		s.logger.Info("deezer share: the speaker will offer its kept Deezer login if it has one for this account", "peer", peer)
		note("offering")
		return
	}
	s.logger.Info("deezer share: the speaker refused the request for a login", "peer", peer, "reason", deezerShareCode(ans.Reason))
	note("refused:" + deezerShareCode(ans.Reason))
}

// handleDeezerOfferRequest is POST /api/deezer/offer-request: a speaker that
// needs a login asks this one to offer its kept login. The body is not read.
func (s *Server) handleDeezerOfferRequest(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "music service settings only allowed from LAN", http.StatusForbidden)
		return
	}
	if s.deezer == nil {
		http.Error(w, "not available on this build", http.StatusNotImplemented)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := callerIP(r.RemoteAddr)
	peer := deezerPeerName(ip)
	refuse := func(status int, reason string) {
		s.logger.Info("deezer share: refused another speaker's request for a login", "peer", peer, "reason", reason)
		s.deezer.NoteDeezerShareRequest(false, marge.DeezerShareEvent{Peer: peer, Outcome: "refused:" + reason})
		writeJSON(w, status, deezerShareAnswer{Status: "refused", Reason: reason})
	}
	if !s.isKnownDeezerPeer(ip) {
		refuse(http.StatusForbidden, "unknown-caller")
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		refuse(http.StatusUnsupportedMediaType, "bad-request")
		return
	}
	st := &s.deezerShare
	st.mu.Lock()
	st.initLocked()
	if time.Since(st.requestedAt[ip]) < deezerShareRecheckGap {
		st.mu.Unlock()
		refuse(http.StatusTooManyRequests, "rate-limited")
		return
	}
	st.requestedAt[ip] = time.Now()
	already := st.offered[ip]
	st.mu.Unlock()
	if already {
		refuse(http.StatusConflict, "already-offered")
		return
	}
	if why := s.deezerShareBlocked(); why != "" {
		refuse(http.StatusConflict, why)
		return
	}
	s.logger.Info("deezer share: another speaker asked for a Deezer login, offering the kept one to it", "peer", peer)
	s.deezer.NoteDeezerShareRequest(false, marge.DeezerShareEvent{Peer: peer, Outcome: "offering"})
	writeJSON(w, http.StatusAccepted, deezerShareAnswer{Status: "offering"})
	// The normal offer, towards the requester alone: its need signal, the
	// hash match, the sealing and its own guards decide.
	target := s.deezerSharePeerAt(ip)
	st.wg.Add(1)
	go func() {
		defer st.wg.Done()
		octx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		st.round.Lock()
		defer st.round.Unlock()
		s.offerDeezerToPeer(octx, target, deezerShareTriggerRequest)
	}()
}

// deezerShareCode keeps a reason code another agent sent printable and short
// before it reaches the log and the debug section: lower-case letters, digits
// and dashes, at most 32 characters, "unknown" otherwise.
func deezerShareCode(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 32 {
		return "unknown"
	}
	for _, r := range v {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return "unknown"
		}
	}
	return v
}

// deezerShareClient is the client for the agent-to-agent calls.
func (s *Server) deezerShareClient() *http.Client {
	if s.deezerShareHTTP != nil {
		return s.deezerShareHTTP
	}
	return deezerShareDefaultClient
}

// deezerShareCall makes one agent-to-agent call, trying each base in order
// until one answers at all (any HTTP status). It returns that base, the
// status and up to 4 KiB of the body; ok is false when no port answered.
func (s *Server) deezerShareCall(ctx context.Context, bases []string, method, path string, body []byte, timeout time.Duration) (base string, status int, respBody []byte, ok bool) {
	for _, b := range bases {
		if ctx.Err() != nil {
			return "", 0, nil, false
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(cctx, method, b+path, rd)
		if err != nil {
			cancel()
			continue
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := s.deezerShareClient().Do(req)
		if err != nil {
			// Not this port (refused or black-holed): the next one.
			cancel()
			continue
		}
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		cancel()
		return b, resp.StatusCode, rb, true
	}
	return "", 0, nil, false
}

// fetchDeezerNeed reads a peer's need signal from the first port that
// answers, and returns that port's base for the post. outcome is "" on
// success, "old-agent" (the peer's agent predates sharing) or "unreachable".
func (s *Server) fetchDeezerNeed(ctx context.Context, bases []string) (DeezerLoginNeed, string, string) {
	base, status, body, ok := s.deezerShareCall(ctx, bases, http.MethodGet, "/api/deezer/login-need", nil, 5*time.Second)
	if !ok {
		return DeezerLoginNeed{}, "", "unreachable"
	}
	var need DeezerLoginNeed
	if status != http.StatusOK || json.Unmarshal(body, &need) != nil {
		// 404 or 501, or an old agent's page for an unknown path.
		return DeezerLoginNeed{}, base, "old-agent"
	}
	return need, base, ""
}

// postDeezerShare posts the sealed login to the base that answered the need
// check. outcome is "" when the peer gave an answer, else "old-agent" or
// "unreachable".
func (s *Server) postDeezerShare(ctx context.Context, base string, sealed marge.SealedDeezerLogin) (deezerShareAnswer, string) {
	b, err := json.Marshal(map[string]any{"sealed": sealed})
	if err != nil {
		return deezerShareAnswer{}, "unreachable"
	}
	_, status, body, ok := s.deezerShareCall(ctx, []string{base}, http.MethodPost, "/api/deezer/kept-login", b, 15*time.Second)
	if !ok {
		return deezerShareAnswer{}, "unreachable"
	}
	if status == http.StatusNotFound {
		return deezerShareAnswer{}, "old-agent"
	}
	var ans deezerShareAnswer
	if json.Unmarshal(body, &ans) != nil || ans.Status == "" {
		return deezerShareAnswer{}, "unreachable"
	}
	if ans.Status != "accepted" && ans.Reason == "" {
		ans.Reason = "http-" + strconv.Itoa(status)
	}
	return ans, ""
}
