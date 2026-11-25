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
//
// The sender side runs at three existing moments, never on a timer: once
// after the agent start (after the start recreate), when a peer comes up in
// the peer roster, and on the Deezer card's Check again. It posts to a peer at
// most once per agent start, and only to a peer whose need signal names the
// kept account. Untested on hardware: whether one speaker's token plays on
// another.

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
	deezerShareTriggerStart = "start"
	deezerShareTriggerPeer  = "peer-seen"
	deezerShareTriggerCheck = "check-again"
)

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

// deezerSharePeer is one peer to offer the kept login to.
type deezerSharePeer struct {
	ip        string
	base      string // http://ip:port
	reachable bool
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
	return deezerSharePeer{ip: ip, base: "http://" + u.Host, reachable: p.Reachable}, true
}

// deezerShareReady reports whether this speaker has a login worth offering,
// logging why not. quiet logs at debug level (a peer coming up is frequent
// and most speakers keep no Deezer login).
func (s *Server) deezerShareReady(trigger string) bool {
	if s.deezer == nil {
		return false
	}
	logf := s.logger.Info
	if trigger == deezerShareTriggerPeer {
		logf = s.logger.Debug
	}
	if s.deezer.KeptDeezerAccount() == "" {
		logf("deezer share: not offering a login to other speakers, this speaker keeps no Deezer login", "trigger", trigger)
		return false
	}
	if st := s.deezer.DeezerLoginSnapshot(); st.LastRetry != nil && st.LastRetry.Code == deezerRetryRejected {
		logf("deezer share: not offering a login to other speakers, Deezer rejected this speaker's own kept login (3330)", "trigger", trigger)
		return false
	}
	return true
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
// it needs one. port 0 means the agent's default port.
func (s *Server) OfferKeptDeezerLoginToPeer(ctx context.Context, ip string, port int) {
	if !s.deezerShareReady(deezerShareTriggerPeer) || net.ParseIP(ip) == nil {
		return
	}
	if port == 0 {
		port = 8888
	}
	s.deezerShare.round.Lock()
	defer s.deezerShare.round.Unlock()
	s.offerDeezerToPeer(ctx, deezerSharePeer{ip: ip, base: "http://" + net.JoinHostPort(ip, strconv.Itoa(port)), reachable: true}, deezerShareTriggerPeer)
}

// offerDeezerToPeer runs one peer's offer. The caller holds the round lock.
func (s *Server) offerDeezerToPeer(ctx context.Context, p deezerSharePeer, trigger string) {
	peer := deezerPeerName(p.ip)
	note := func(outcome string) {
		s.deezer.NoteDeezerShare(true, marge.DeezerShareEvent{Peer: peer, Trigger: trigger, Outcome: outcome})
	}
	st := &s.deezerShare
	st.mu.Lock()
	if st.offered == nil {
		st.offered, st.checkedAt = map[string]bool{}, map[string]time.Time{}
	}
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
	need, outcome := s.fetchDeezerNeed(ctx, p.base)
	if outcome != "" {
		s.logger.Info("deezer share: not offering, the speaker did not answer the need check",
			"peer", peer, "trigger", trigger, "outcome", outcome)
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
	ans, outcome := s.postDeezerShare(ctx, p.base, sealed)
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
	return &http.Client{Timeout: 15 * time.Second}
}

// fetchDeezerNeed reads a peer's need signal. outcome is "" on success,
// "old-agent" (the peer's agent predates sharing) or "unreachable".
func (s *Server) fetchDeezerNeed(ctx context.Context, base string) (DeezerLoginNeed, string) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, base+"/api/deezer/login-need", nil)
	if err != nil {
		return DeezerLoginNeed{}, "unreachable"
	}
	resp, err := s.deezerShareClient().Do(req)
	if err != nil {
		return DeezerLoginNeed{}, "unreachable"
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNotImplemented {
		return DeezerLoginNeed{}, "old-agent"
	}
	var need DeezerLoginNeed
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&need) != nil {
		// An old agent answers an unknown path with its page.
		return DeezerLoginNeed{}, "old-agent"
	}
	return need, ""
}

// postDeezerShare posts the sealed login. outcome is "" when the peer gave
// an answer, else "old-agent" or "unreachable".
func (s *Server) postDeezerShare(ctx context.Context, base string, sealed marge.SealedDeezerLogin) (deezerShareAnswer, string) {
	b, err := json.Marshal(map[string]any{"sealed": sealed})
	if err != nil {
		return deezerShareAnswer{}, "unreachable"
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, base+"/api/deezer/kept-login", bytes.NewReader(b))
	if err != nil {
		return deezerShareAnswer{}, "unreachable"
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.deezerShareClient().Do(req)
	if err != nil {
		return deezerShareAnswer{}, "unreachable"
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return deezerShareAnswer{}, "old-agent"
	}
	var ans deezerShareAnswer
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&ans) != nil || ans.Status == "" {
		return deezerShareAnswer{}, "unreachable"
	}
	if ans.Status != "accepted" && ans.Reason == "" {
		ans.Reason = "http-" + strconv.Itoa(resp.StatusCode)
	}
	return ans, ""
}
