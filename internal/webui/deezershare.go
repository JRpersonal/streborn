package webui

// Deezer: the logins of the speakers of one home, and copying a kept login
// from one to another on the owner's press (docs/streaming/deezer.md,
// "Copying a kept login within a home").
//
// A speaker whose Deezer login is gone and that keeps no working copy can
// never get a new one (Deezer issues logins only to Bose's app). When another
// speaker of the home keeps a login for the same account, the owner can copy
// it over from the Deezer area. Nothing is ever copied on its own: no copy at
// the agent start, none when a peer comes up, none on Check again.
//
// GET  /api/deezer/household     the Deezer area's overview: this speaker and
//                                each peer in the roster, its Deezer state,
//                                its kept login's origin and age, its backed-up
//                                older logins, whether it uses the same
//                                account, and which buttons apply. Read when
//                                the owner opens or refreshes the area.
// POST /api/deezer/copy          the owner's Copy: {"target":"<row id>"} (the
//                                peer's address as the roster has it) or
//                                {"all":true}. Sends the kept login, sealed,
//                                to each target that uses the same account and
//                                whose Deezer does not work.
// POST /api/deezer/use-older     the owner's "Use the older login": puts a
//                                backed-up login back when Deezer refuses the
//                                current one (internal/marge/deezerprevious.go).
// GET  /api/deezer/login-summary agent to agent: this speaker's line of the
//                                overview, secret-free: its state, its kept
//                                login's origin, a hash of its account (salted
//                                with this run's share key) and the key. Never
//                                the account in clear.
// POST /api/deezer/kept-login    agent to agent, write-only: a peer's kept
//                                login, sealed to the share key
//                                (internal/marge/deezershare.go). Kept only
//                                from a known peer (or loopback), only for an
//                                account this speaker lists without a working
//                                login. A login kept already is replaced only
//                                by an owner's copy, and then backed up.
//                                Answers {"status","reason"}, never the login.
//
// Every agent-to-agent call tries the peer's agent on :8888 and then on
// :17008 (the REDIRECT the BCO and whitelisted chassis are reached on), with a
// short per-port budget. No timer, no polling.
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

// deezerShareOutcomeWait is how long after a copied login's recreate the
// receiver reads its Deezer source once more, for the log. A variable so the
// tests need not wait.
var deezerShareOutcomeWait = 30 * time.Second

// Triggers of a copy: the owner's press for one speaker or for all of them.
const (
	deezerShareTriggerCopy    = "copy"
	deezerShareTriggerCopyAll = "copy-all"
	// deezerShareUser is what a copy's post says, so the receiver knows an
	// owner pressed for it.
	deezerShareUser = "user"
)

// The overview's states of one speaker's Deezer.
const (
	deezerHHWorks      = "works"
	deezerHHConnecting = "connecting"
	deezerHHNoLogin    = "no-login"
	deezerHHRefused    = "refused"
	deezerHHNotSetUp   = "not-set-up"
	deezerHHUnknown    = "unknown"
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
	// round keeps one copy at a time.
	round sync.Mutex
	// wg tracks the goroutines this side starts (the receiver's recreate),
	// so the tests can wait for them.
	wg sync.WaitGroup
}

// DeezerLoginSummary is GET /api/deezer/login-summary's answer: one
// speaker's line of the overview. Never the login, never the account.
type DeezerLoginSummary struct {
	// Name is the speaker's own name.
	Name string `json:"name,omitempty"`
	// State is "works", "connecting", "no-login", "refused" (Deezer refuses
	// the speaker's login), "not-set-up" or "unknown".
	State string `json:"state"`
	marge.DeezerKeptSummary
	// AccountHash is marge.DeezerShareAccountHash(Key, account) for the
	// account the speaker lists (or keeps a login for); empty without one.
	AccountHash string `json:"accountHash,omitempty"`
	// Key is this run's share public key.
	Key string `json:"key,omitempty"`
}

// deezerShareAnswer is POST /api/deezer/kept-login's answer.
type deezerShareAnswer struct {
	// Status is "accepted" or "refused".
	Status string `json:"status"`
	// Reason is the refusal's code: "unknown-caller", "bad-request",
	// "kept", "ready", "not-listed", "no-account", "unreadable",
	// "account-mismatch", "refused-before", "undecryptable", "malformed",
	// "save-failed", "not-configured".
	Reason string `json:"reason,omitempty"`
}

// deezerPeerName is a peer's pseudonym for the log and the debug section.
func deezerPeerName(ip string) string {
	if ip == "" {
		return "peer#unknown"
	}
	return "peer#" + anonymise.HashShort(ip)
}

// deezerLoginRefused reports whether Deezer refuses the speaker's current
// login: the firmware's last relogin retry is 3330 and came after the kept
// login was written (a login just copied over or put back has not been
// tried yet).
func deezerLoginRefused(login marge.DeezerLoginStatus) bool {
	r := login.LastRetry
	if r == nil || r.Code != deezerRetryRejected {
		return false
	}
	return !login.Kept || login.KeptAt.IsZero() || !r.At.Before(login.KeptAt)
}

// deezerLocal is this speaker's Deezer as the overview and the receiving
// side see it. The accounts are unmasked, agent side only.
type deezerLocal struct {
	state string
	// account is the account the speaker lists (the READY one first), else
	// the kept login's.
	account string
	// listed are the accounts the speaker lists without a working login.
	listed []string
	kept   marge.DeezerKeptSummary
	login  marge.DeezerLoginStatus
}

// deezerLocalState reads the speaker's /sources once.
func (s *Server) deezerLocalState(ctx context.Context) deezerLocal {
	l := deezerLocal{kept: s.deezer.KeptDeezerSummary(), login: s.deezer.DeezerLoginSnapshot()}
	srcs, err := s.nativeBox().GetSources(ctx)
	src := deezerSourceLine{Status: "unknown"}
	if err == nil {
		src = pickDeezerSource(srcs)
		for _, it := range srcs {
			acct := strings.TrimSpace(it.SourceAccount)
			if !strings.EqualFold(it.Source, deezerSource) || acct == "" {
				continue
			}
			if !strings.EqualFold(it.Status, "READY") {
				l.listed = append(l.listed, acct)
			}
		}
	}
	l.account = src.account
	if l.account == "" {
		l.account = s.deezer.KeptDeezerAccount()
	}
	st, _, _ := deezerState(src, err, l.login.LastAttempt, time.Now())
	switch st {
	case "connected":
		l.state = deezerHHWorks
	case "connecting":
		l.state = deezerHHConnecting
	case "unavailable":
		l.state = deezerHHNoLogin
		if deezerLoginRefused(l.login) {
			l.state = deezerHHRefused
		}
	case "not-connected":
		l.state = deezerHHNotSetUp
	default:
		l.state = deezerHHUnknown
	}
	return l
}

// deezerSummary is this speaker's line of the overview, for a peer.
func (s *Server) deezerSummary(ctx context.Context) DeezerLoginSummary {
	l := s.deezerLocalState(ctx)
	out := DeezerLoginSummary{Name: s.ownSpeakerName(), State: l.state, DeezerKeptSummary: l.kept}
	if l.account != "" {
		if key := s.deezer.DeezerShareKey(); key != "" {
			out.Key, out.AccountHash = key, marge.DeezerShareAccountHash(key, l.account)
		}
	}
	return out
}

// ownSpeakerName is this speaker's name, "" when not known.
func (s *Server) ownSpeakerName() string {
	if s.boxNameFn == nil {
		return ""
	}
	name, _ := s.boxNameFn()
	return strings.TrimSpace(name)
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

// deezerRosterName is the roster's name for a peer address, "" when none.
func (s *Server) deezerRosterName(ip string) string {
	if s.cachedPeersFn == nil {
		return ""
	}
	for _, p := range s.cachedPeersFn() {
		if t, ok := deezerSharePeerOf(p); ok && t.ip == ip {
			return strings.TrimSpace(p.Name)
		}
	}
	return ""
}

// deezerMethodGuard answers the common refusals of the Deezer endpoints:
// not from the LAN, no Deezer on this build, another method. write also
// requires a JSON content type, so a cross-site form cannot start it.
func (s *Server) deezerMethodGuard(w http.ResponseWriter, r *http.Request, method string, write bool) bool {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "music service settings only allowed from LAN", http.StatusForbidden)
		return false
	}
	if s.deezer == nil {
		http.Error(w, "not available on this build", http.StatusNotImplemented)
		return false
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if write {
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			http.Error(w, "music service settings need a JSON body", http.StatusUnsupportedMediaType)
			return false
		}
	}
	return true
}

// handleDeezerLoginSummary is GET /api/deezer/login-summary.
func (s *Server) handleDeezerLoginSummary(w http.ResponseWriter, r *http.Request) {
	if !s.deezerMethodGuard(w, r, http.MethodGet, false) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	sum := s.deezerSummary(ctx)
	s.logger.Debug("deezer share: a speaker read this one's Deezer login summary",
		"peer", deezerPeerName(callerIP(r.RemoteAddr)), "state", sum.State, "kept", sum.Kept)
	writeJSON(w, http.StatusOK, sum)
}

// handleDeezerKeptLogin is POST /api/deezer/kept-login, the receiving side.
func (s *Server) handleDeezerKeptLogin(w http.ResponseWriter, r *http.Request) {
	if !s.deezerMethodGuard(w, r, http.MethodPost, false) {
		return
	}
	ip := callerIP(r.RemoteAddr)
	peer := deezerPeerName(ip)
	refuse := func(status int, reason string) {
		s.logger.Info("deezer share: refused a Deezer login another speaker copied over", "peer", peer, "reason", reason)
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
		Sealed  marge.SealedDeezerLogin `json:"sealed"`
		Trigger string                  `json:"trigger"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil || body.Sealed.Box == "" {
		refuse(http.StatusBadRequest, "bad-request")
		return
	}
	user := body.Trigger == deezerShareUser
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	l := s.deezerLocalState(ctx)
	switch {
	case l.state == deezerHHUnknown:
		refuse(http.StatusConflict, "unreadable")
		return
	case l.state == deezerHHWorks:
		refuse(http.StatusConflict, "ready")
		return
	case l.state == deezerHHNotSetUp:
		refuse(http.StatusConflict, "not-listed")
		return
	case len(l.listed) == 0:
		refuse(http.StatusConflict, "no-account")
		return
	case l.kept.Kept && !user:
		// Only an owner's copy replaces a kept login.
		refuse(http.StatusConflict, "kept")
		return
	}
	_, changed, err := s.deezer.AdoptPeerDeezerLogin(body.Sealed, l.listed, marge.DeezerAdoptOptions{
		Peer: peer, PeerName: s.deezerRosterName(ip),
		Replace: user, CurrentRefused: l.state == deezerHHRefused,
	})
	if err != nil {
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
	trigger := ""
	if user {
		trigger = deezerShareUser
	}
	s.logger.Info("deezer share: accepted a Deezer login another speaker of the home copied over, handing it to the firmware",
		"peer", peer, "replacedKept", l.kept.Kept && changed, "sameAsKept", !changed)
	s.deezer.NoteDeezerShare(false, marge.DeezerShareEvent{Peer: peer, Trigger: trigger, Outcome: "accepted"})
	writeJSON(w, http.StatusOK, deezerShareAnswer{Status: "accepted"})
	// The recreate talks to the firmware for some seconds; the sender need
	// not wait for it. It logs its own steps and outcome.
	s.deezerShare.wg.Add(1)
	go func() {
		defer s.deezerShare.wg.Done()
		s.deezerRecreateAndReport(context.Background(), "peer", deezerShareOutcomeWait, true)
	}()
}

// isDeezerShareCode reports whether err is one of the stand-in's reason codes.
func isDeezerShareCode(err error) bool {
	for _, e := range []error{marge.ErrDeezerShareNoKeptLogin, marge.ErrDeezerShareHashMismatch,
		marge.ErrDeezerShareAlreadyKept, marge.ErrDeezerShareUndecryptable, marge.ErrDeezerShareMalformed,
		marge.ErrDeezerShareAccount, marge.ErrDeezerShareSaveFailed, marge.ErrDeezerShareOff,
		marge.ErrDeezerShareRefusedBefore, marge.ErrDeezerNoOlderLogin} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// deezerSharePeer is one peer to call.
type deezerSharePeer struct {
	// id is the roster entry's host (address and port as listed), the
	// overview row's id and a copy's target.
	id   string
	ip   string
	name string
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
	return deezerSharePeer{id: u.Host, ip: ip, name: strings.TrimSpace(p.Name), bases: deezerShareBases(ip, port), reachable: p.Reachable}, true
}

// deezerSharePeers is the roster as call targets, one per entry, in the
// roster's order.
func (s *Server) deezerSharePeers() []deezerSharePeer {
	if s.cachedPeersFn == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []deezerSharePeer
	for _, p := range s.cachedPeersFn() {
		t, ok := deezerSharePeerOf(p)
		if !ok || seen[t.id] {
			continue
		}
		seen[t.id] = true
		if t.name == "" {
			t.name = t.ip
		}
		out = append(out, t)
	}
	return out
}

// deezerShareBlocked says why this speaker has no login worth copying:
// "not-configured", "no-kept-login", "own-login-rejected" (Deezer refuses
// it), or "" when it has one.
func (s *Server) deezerShareBlocked() string {
	switch {
	case s.deezer == nil:
		return "not-configured"
	case s.deezer.KeptDeezerAccount() == "":
		return "no-kept-login"
	}
	if deezerLoginRefused(s.deezer.DeezerLoginSnapshot()) {
		return "own-login-rejected"
	}
	return ""
}

// DeezerHouseholdRow is one speaker of the overview.
type DeezerHouseholdRow struct {
	// ID is the peer's address as the roster lists it, the target of a
	// copy; "" for this speaker.
	ID   string `json:"id"`
	Name string `json:"name"`
	Self bool   `json:"self,omitempty"`
	// State is as in DeezerLoginSummary; Why says why it is "unknown":
	// "unreachable" or "old-agent".
	State string `json:"state"`
	Why   string `json:"why,omitempty"`
	marge.DeezerKeptSummary
	// Account is "same" when the speaker uses this speaker's Deezer account,
	// "other" when it uses another one, "" when that is not known.
	Account string `json:"account,omitempty"`
	// CanCopy: this speaker keeps a login Deezer does not refuse, the row
	// uses the same account, and its Deezer does not work.
	CanCopy bool `json:"canCopy,omitempty"`
	// CanUseOlder: this speaker's Deezer refuses its login and a backed-up
	// older one Deezer has not refused exists (this speaker's row only).
	CanUseOlder bool `json:"canUseOlder,omitempty"`
}

// DeezerHousehold is GET /api/deezer/household's answer.
type DeezerHousehold struct {
	Speakers []DeezerHouseholdRow `json:"speakers"`
	// CanCopy is how many speakers a copy to all would reach.
	CanCopy int `json:"canCopy"`
}

// deezerCanTake reports whether a speaker in state can take a copy: it lists
// Deezer and its login does not work.
func deezerCanTake(state string) bool {
	return state == deezerHHNoLogin || state == deezerHHRefused
}

// deezerAccountRel compares a peer's account hash with this speaker's
// accounts: "same", "other" or "".
func deezerAccountRel(sum DeezerLoginSummary, accounts ...string) string {
	if sum.AccountHash == "" || sum.Key == "" {
		return ""
	}
	named := false
	for _, a := range accounts {
		if a = strings.TrimSpace(a); a == "" {
			continue
		}
		named = true
		if marge.DeezerShareAccountHash(sum.Key, a) == strings.ToLower(strings.TrimSpace(sum.AccountHash)) {
			return "same"
		}
	}
	if !named {
		return ""
	}
	return "other"
}

// deezerHousehold builds the overview: this speaker's line and each peer's,
// read in parallel, once.
func (s *Server) deezerHousehold(ctx context.Context) DeezerHousehold {
	l := s.deezerLocalState(ctx)
	kept := s.deezer.KeptDeezerAccount()
	copyable := s.deezerShareBlocked() == ""
	self := DeezerHouseholdRow{
		Name: s.ownSpeakerName(), Self: true, State: l.state, DeezerKeptSummary: l.kept,
		CanUseOlder: l.state == deezerHHRefused && l.kept.OlderUsable,
	}
	if l.account != "" {
		self.Account = "same"
	}
	out := DeezerHousehold{Speakers: []DeezerHouseholdRow{self}}
	peers := s.deezerSharePeers()
	rows := make([]DeezerHouseholdRow, len(peers))
	var wg sync.WaitGroup
	for i, p := range peers {
		rows[i] = DeezerHouseholdRow{ID: p.id, Name: p.name, State: deezerHHUnknown}
		if !p.reachable {
			// Series-I speakers cannot reach each other's agent.
			rows[i].Why = "unreachable"
			continue
		}
		wg.Add(1)
		go func(i int, p deezerSharePeer) {
			defer wg.Done()
			sum, _, outcome := s.fetchDeezerSummary(ctx, p.bases)
			if outcome != "" {
				rows[i].Why = outcome
				return
			}
			row := &rows[i]
			row.State = deezerShareCode(sum.State)
			// Only the summary's own fields; a peer's text is never echoed.
			row.Kept, row.KeptAt = sum.Kept, sum.KeptAt
			if sum.From != "" {
				row.From = deezerShareCode(sum.From)
			}
			row.FromName = clipName(sum.FromName)
			row.Previous, row.PreviousRefused, row.OlderUsable = clampCount(sum.Previous), clampCount(sum.PreviousRefused), sum.OlderUsable
			if row.Name == p.ip && sum.Name != "" {
				row.Name = clipName(sum.Name)
			}
			row.Account = deezerAccountRel(sum, l.account, kept)
			row.CanCopy = copyable && deezerCanTake(row.State) && deezerAccountRel(sum, kept) == "same"
		}(i, p)
	}
	wg.Wait()
	for _, r := range rows {
		if r.CanCopy {
			out.CanCopy++
		}
		out.Speakers = append(out.Speakers, r)
	}
	return out
}

// clipName keeps a name another agent sent short and printable.
func clipName(v string) string {
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(v))
	if r := []rune(v); len(r) > 64 {
		v = string(r[:64])
	}
	return v
}

func clampCount(n int) int {
	switch {
	case n < 0:
		return 0
	case n > 99:
		return 99
	}
	return n
}

// handleDeezerHousehold is GET /api/deezer/household.
func (s *Server) handleDeezerHousehold(w http.ResponseWriter, r *http.Request) {
	if !s.deezerMethodGuard(w, r, http.MethodGet, false) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.deezerHousehold(ctx))
}

// DeezerCopyResult is one target's outcome of a copy.
type DeezerCopyResult struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Outcome is "accepted", "refused:<code>", "not-needed:<state>",
	// "hash-mismatch" (another account), "unreachable" or "old-agent".
	Outcome string `json:"outcome"`
}

// DeezerCopyAnswer is POST /api/deezer/copy's answer.
type DeezerCopyAnswer struct {
	Results []DeezerCopyResult `json:"results"`
	// Copied is how many speakers took the login.
	Copied int `json:"copied"`
}

// handleDeezerCopy is POST /api/deezer/copy, the owner's Copy.
func (s *Server) handleDeezerCopy(w http.ResponseWriter, r *http.Request) {
	if !s.deezerMethodGuard(w, r, http.MethodPost, true) {
		return
	}
	var body struct {
		Target string `json:"target"`
		All    bool   `json:"all"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil || (body.Target == "") == !body.All {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "name one speaker or all of them", "code": "bad-request"})
		return
	}
	if why := s.deezerShareBlocked(); why != "" {
		s.logger.Info("deezer share: not copying, this speaker has no login worth copying", "why", why)
		writeJSON(w, http.StatusConflict, map[string]any{"error": "this speaker has no Deezer login to copy", "code": why})
		return
	}
	var targets []deezerSharePeer
	trigger := deezerShareTriggerCopyAll
	for _, p := range s.deezerSharePeers() {
		if body.All || p.id == strings.TrimSpace(body.Target) {
			targets = append(targets, p)
		}
	}
	if !body.All {
		trigger = deezerShareTriggerCopy
		if len(targets) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "that speaker is not in this speaker's list", "code": "unknown-target"})
			return
		}
	}
	if !s.deezerShare.round.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "a copy is running already", "code": "busy"})
		return
	}
	defer s.deezerShare.round.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	s.logger.Info("deezer share: the owner asked to copy the kept Deezer login", "trigger", trigger, "targets", len(targets))
	ans := DeezerCopyAnswer{Results: []DeezerCopyResult{}}
	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		outcome := s.copyDeezerTo(ctx, t, trigger)
		ans.Results = append(ans.Results, DeezerCopyResult{ID: t.id, Name: t.name, Outcome: outcome})
		if outcome == "accepted" {
			ans.Copied++
		}
	}
	writeJSON(w, http.StatusOK, ans)
}

// copyDeezerTo copies the kept login to one peer when it still needs it:
// it reads the peer's summary, seals the login to its key when the account
// hash matches, and posts it. The caller holds the round lock.
func (s *Server) copyDeezerTo(ctx context.Context, p deezerSharePeer, trigger string) string {
	peer := deezerPeerName(p.ip)
	note := func(outcome string) string {
		s.deezer.NoteDeezerShare(true, marge.DeezerShareEvent{Peer: peer, Trigger: trigger, Outcome: outcome})
		return outcome
	}
	if !p.reachable {
		s.logger.Info("deezer share: not copying, the speaker's agent is not reachable from here (series-I speakers cannot reach each other's agent)",
			"peer", peer, "trigger", trigger)
		return note("unreachable")
	}
	sum, base, outcome := s.fetchDeezerSummary(ctx, p.bases)
	if outcome != "" {
		s.logger.Info("deezer share: not copying, the speaker did not answer the summary read",
			"peer", peer, "trigger", trigger, "outcome", outcome, "portsTried", len(p.bases))
		return note(outcome)
	}
	if state := deezerShareCode(sum.State); !deezerCanTake(state) {
		s.logger.Info("deezer share: not copying, the speaker does not need a Deezer login", "peer", peer, "trigger", trigger, "state", state)
		return note("not-needed:" + state)
	}
	sealed, err := s.deezer.SealKeptDeezerLogin(sum.Key, sum.AccountHash)
	switch {
	case errors.Is(err, marge.ErrDeezerShareHashMismatch):
		s.logger.Info("deezer share: not copying, the speaker uses another Deezer account", "peer", peer, "trigger", trigger)
		return note("hash-mismatch")
	case err != nil:
		code := "seal-failed"
		if isDeezerShareCode(err) {
			code = err.Error()
		}
		s.logger.Info("deezer share: not copying, the login could not be sealed for the speaker",
			"peer", peer, "trigger", trigger, "reason", code)
		return note("refused:" + code)
	}
	ans, outcome := s.postDeezerShare(ctx, base, sealed)
	switch {
	case outcome != "":
		s.logger.Info("deezer share: sent the kept login, the speaker did not take it", "peer", peer, "trigger", trigger, "outcome", outcome)
		return note(outcome)
	case ans.Status == "accepted":
		s.logger.Info("deezer share: sent the kept login, the speaker accepted it and hands it to its firmware", "peer", peer, "trigger", trigger)
		return note("accepted")
	default:
		s.logger.Info("deezer share: sent the kept login, the speaker refused it", "peer", peer, "trigger", trigger, "reason", deezerShareCode(ans.Reason))
		return note("refused:" + deezerShareCode(ans.Reason))
	}
}

// handleDeezerUseOlder is POST /api/deezer/use-older, the owner's "Use the
// older login". It applies only while Deezer refuses the current login; it
// puts the newest backed-up login for the same account that Deezer has not
// refused back, then hands it to the firmware, and answers like GET
// /api/deezer/account with the recreate's step.
func (s *Server) handleDeezerUseOlder(w http.ResponseWriter, r *http.Request) {
	if !s.deezerMethodGuard(w, r, http.MethodPost, true) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if l := s.deezerLocalState(ctx); l.state != deezerHHRefused {
		s.logger.Info("deezer: not putting an older login back, Deezer does not refuse the current one", "state", l.state)
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Deezer does not refuse this speaker's login", "code": "not-refused"})
		return
	}
	if _, err := s.deezer.UseOlderDeezerLogin(); err != nil {
		code := "save-failed"
		if isDeezerShareCode(err) {
			code = err.Error()
		}
		status := http.StatusConflict
		if code == "save-failed" {
			status = http.StatusInternalServerError
		}
		s.logger.Info("deezer: no older login put back", "reason", code)
		writeJSON(w, status, map[string]any{"error": "no older Deezer login could be put back", "code": code})
		return
	}
	res := s.recreateDeezer(ctx, "use-older", true)
	v := s.deezerView(ctx)
	v.Recreate = &res
	writeJSON(w, http.StatusOK, v)
}

// deezerShareCode keeps a reason code another agent sent printable and short
// before it reaches the log, the debug section or the overview: lower-case
// letters, digits and dashes, at most 32 characters, "unknown" otherwise.
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

// fetchDeezerSummary reads a peer's login summary from the first port that
// answers, and returns that port's base for the post. outcome is "" on
// success, "old-agent" (the peer's agent predates the overview) or
// "unreachable".
func (s *Server) fetchDeezerSummary(ctx context.Context, bases []string) (DeezerLoginSummary, string, string) {
	base, status, body, ok := s.deezerShareCall(ctx, bases, http.MethodGet, "/api/deezer/login-summary", nil, 5*time.Second)
	if !ok {
		return DeezerLoginSummary{}, "", "unreachable"
	}
	var sum DeezerLoginSummary
	if status != http.StatusOK || json.Unmarshal(body, &sum) != nil || sum.State == "" {
		// 404 or 501, or an old agent's page for an unknown path.
		return DeezerLoginSummary{}, base, "old-agent"
	}
	return sum, base, ""
}

// postDeezerShare posts the sealed login, marked as the owner's copy, to the
// base that answered the summary read. outcome is "" when the peer gave an
// answer, else "old-agent" or "unreachable".
func (s *Server) postDeezerShare(ctx context.Context, base string, sealed marge.SealedDeezerLogin) (deezerShareAnswer, string) {
	b, err := json.Marshal(map[string]any{"sealed": sealed, "trigger": deezerShareUser})
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
