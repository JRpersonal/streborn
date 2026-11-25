package webui

// Deezer on the speaker's own Deezer client (docs/streaming/deezer.md).
//
// A NEW Deezer login cannot work without Bose: the firmware takes the
// account call and then waits on Bose's cloud, and its Deezer client stays
// stuck until a power cycle (A2 is closed, see the doc). What STR can do is
// keep the login a speaker already had (A1, internal/marge/deezerlogin.go)
// and bring it back when the firmware holds an account it cannot use.
//
// GET    /api/deezer/account  where Deezer stands on this speaker: the state
//                             the apps show, the speaker's own DEEZER line
//                             from /sources (masked), the kept login (type
//                             and origin only), the last account step and
//                             the firmware's last relogin retry code
// POST   /api/deezer/account  410 Gone, code "new-login-unsupported": the
//                             e-mail and password sign-in of earlier
//                             versions, refused so an older app cannot hang
//                             the speaker's Deezer client again; the body is
//                             never read
// DELETE /api/deezer/account  removes the account from the firmware and
//                             forgets the kept login; safe to repeat, one
//                             removal at a time (shared with Pandora)
// POST   /api/deezer/recheck  the card's "Check again": runs the recreate
//                             below when it applies, then answers like GET;
//                             also offers a kept login to the speakers of the
//                             home that need one, or on a speaker that needs
//                             one asks them to offer theirs (deezershare.go)
// GET    /api/deezer/login-need, POST /api/deezer/kept-login,
// POST   /api/deezer/offer-request: sharing a kept login with another
//                             speaker (deezershare.go)
//
// The recreate: when STR keeps a Deezer login and the speaker lists DEEZER
// without a working account, the firmware ignores the token /full now serves
// (it only updates the secret it has, the empty one). Removing its account
// and sending it a sourcesUpdated notification makes it read /full again and
// create the account anew, with the token. Proven on a Portable, 2026-11-22.
// It runs once after the agent starts and on "Check again", never on a timer.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/marge"
)

// deezerSource is the firmware's source enum for Deezer.
const deezerSource = "DEEZER"

// deezerDisplayName is the display name passed with the account calls, the
// one AfterTouch sent to a real speaker (gesellix/Bose-SoundTouch#574). The
// removal does not depend on it.
const deezerDisplayName = "Deezer Premium"

// deezerConnectingFor is how long after a recreate the state reads
// "connecting" while the speaker does not list Deezer READY yet: the
// firmware re-reads /full and logs in with Deezer within seconds.
const deezerConnectingFor = 90 * time.Second

// deezerRecreateEvery is the shortest gap between two recreates. A second
// "Check again" inside it only reads the state.
const deezerRecreateEvery = 60 * time.Second

// deezerRetryRejected is the firmware's relogin retry code for a token
// Deezer refused (expired or revoked). It cannot be renewed without Bose.
const deezerRetryRejected = "3330"

// DeezerBackend is the stand-in's side (implemented by *marge.Server).
type DeezerBackend interface {
	DeezerLoginSnapshot() marge.DeezerLoginStatus
	BeginDeezerAttempt(kind string)
	NoteDeezerFirmwareAnswer(status int, errCode, detail string)
	NoteDeezerSourceStatus(status string, hasAccount bool)
	NoteDeezerRecreate(trigger string, notifyStatus int, errText string)
	KeptDeezerAccount() string
	CarriedOverDeezerAccount() string
	ClearKeptDeezer() error
	// Sharing a kept login between the speakers of one home
	// (deezershare.go, internal/marge/deezershare.go).
	DeezerShareKey() string
	SealKeptDeezerLogin(peerKey, peerHash string) (marge.SealedDeezerLogin, error)
	AdoptPeerDeezerLogin(sealed marge.SealedDeezerLogin, listed []string, peer string) (string, error)
	NoteDeezerShare(sent bool, ev marge.DeezerShareEvent)
	NoteDeezerShareRequest(sent bool, ev marge.DeezerShareEvent)
}

// deezerSourceLine is the speaker's own DEEZER entry in /sources.
type deezerSourceLine struct {
	// Status is the firmware's status ("READY", "UNAVAILABLE"), "absent"
	// when it lists no DEEZER entry.
	Status     string `json:"status"`
	HasAccount bool   `json:"hasAccount"`
	// account is the listed account, unmasked, agent side only.
	account string
}

// pickDeezerSource picks the DEEZER entry to report: the READY one with an
// account when there is one (a speaker can list a service once per account
// it ever held, #1630), else the first.
func pickDeezerSource(srcs []boxapi.Source) deezerSourceLine {
	var pick *boxapi.Source
	for i := range srcs {
		it := &srcs[i]
		if !strings.EqualFold(it.Source, deezerSource) {
			continue
		}
		if pick == nil {
			pick = it
		}
		if strings.EqualFold(it.Status, "READY") && strings.TrimSpace(it.SourceAccount) != "" {
			pick = it
			break
		}
	}
	if pick == nil {
		return deezerSourceLine{Status: "absent"}
	}
	acct := strings.TrimSpace(pick.SourceAccount)
	return deezerSourceLine{Status: strings.ToUpper(strings.TrimSpace(pick.Status)), HasAccount: acct != "", account: acct}
}

// DeezerRecreateResult is what a recheck's recreate did.
type DeezerRecreateResult struct {
	Ran bool `json:"ran"`
	// Skipped says why it did not run: "no-kept-login", "not-listed" (the
	// speaker lists no DEEZER entry), "ready" (it already plays), "unreadable"
	// (its source list could not be read), "busy", "ran-recently".
	Skipped string `json:"skipped,omitempty"`
	// Error names the step that failed, without the account.
	Error string `json:"error,omitempty"`
}

// DeezerAccountView is GET /api/deezer/account's answer.
type DeezerAccountView struct {
	// State is what the apps show:
	//   connected      the speaker lists DEEZER READY with an account
	//   connecting     STR handed the kept login back less than a minute and
	//                  a half ago, and the speaker is not READY yet
	//   unavailable    the speaker holds a Deezer account it cannot use
	//   not-connected  no Deezer account on the speaker
	//   unknown        the speaker's source list could not be read
	// An older agent also reported "failed" (a new login that did not work).
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// Detail is a short client-safe text for the state.
	Detail string `json:"detail,omitempty"`
	// Account is the account the speaker lists or STR keeps, masked.
	Account string `json:"account,omitempty"`
	// Stuck is set when the speaker's last Deezer account call answered
	// 1024 or not at all: its Deezer client hangs until a power cycle.
	Stuck bool `json:"stuck,omitempty"`
	// Rejected is set when the speaker is neither connected nor connecting
	// and the firmware's last relogin retry says Deezer refused the token
	// (3330).
	Rejected bool `json:"rejected,omitempty"`
	// Recreate is the recheck's own step, only in POST /api/deezer/recheck.
	Recreate *DeezerRecreateResult   `json:"recreate,omitempty"`
	Source   deezerSourceLine        `json:"source"`
	Login    marge.DeezerLoginStatus `json:"login"`
}

// deezerState derives the state from the speaker's DEEZER line and the last
// account step. readErr is the /sources read's error.
func deezerState(src deezerSourceLine, readErr error, a *marge.DeezerAttempt, now time.Time) (state, reason, detail string) {
	if readErr != nil {
		return "unknown", "", speakerErrorReason(readErr)
	}
	if src.Status == "READY" && src.HasAccount {
		return "connected", "", ""
	}
	if a != nil && a.Kind == "recreate" && a.FirmwareStatus == http.StatusOK && now.Sub(a.At) < deezerConnectingFor {
		return "connecting", "", ""
	}
	if src.HasAccount {
		return "unavailable", "", "the speaker lists Deezer as " + src.Status
	}
	return "not-connected", "", ""
}

// deezerStuck reports whether the last account step left the speaker's
// Deezer client hanging: an answer of 1024 (SOURCE_ALREADY_EXISTS, what the
// firmware answers every Deezer account call after a hung one), or a
// finished recreate the speaker did not answer at all.
func deezerStuck(a *marge.DeezerAttempt, running bool) bool {
	if a == nil {
		return false
	}
	if strings.Contains(a.FirmwareError, "1024") {
		return true
	}
	return !running && (a.Kind == "recreate" || a.Kind == "login") && a.FirmwareStatus == 0
}

// deezerView reads the speaker's /sources once and builds the answer. It also
// hands the DEEZER status to the attempt record, so the debug section shows
// what the speaker listed the last time anyone looked.
func (s *Server) deezerView(ctx context.Context) DeezerAccountView {
	srcs, err := s.nativeBox().GetSources(ctx)
	src := deezerSourceLine{Status: "unknown"}
	if err != nil {
		s.logger.Warn("deezer: could not read the speaker's sources", "err", err)
	} else {
		src = pickDeezerSource(srcs)
		s.deezer.NoteDeezerSourceStatus(src.Status, src.HasAccount)
	}
	login := s.deezer.DeezerLoginSnapshot()
	v := DeezerAccountView{Source: src, Login: login}
	v.State, v.Reason, v.Detail = deezerState(src, err, login.LastAttempt, time.Now())
	v.Stuck = deezerStuck(login.LastAttempt, s.deezerRecreating.Load())
	// Not while a recreate runs: the code then is the one from before it.
	v.Rejected = v.State != "connected" && v.State != "connecting" &&
		login.LastRetry != nil && login.LastRetry.Code == deezerRetryRejected
	acct := src.account
	if acct == "" {
		acct = s.deezer.KeptDeezerAccount()
	}
	if acct != "" {
		v.Account = maskForLog(acct)
	}
	return v
}

// deezerFirmwareAnswer turns the speaker's answer to an account call into the
// attempt record's fields: the HTTP status (200 on success, 0 when it did not
// answer), the firmware's code and name, and its error text with the account
// and the password taken out.
func deezerFirmwareAnswer(err error, user, pass string) (status int, code, detail string) {
	if err == nil {
		return http.StatusOK, "", ""
	}
	var be *boxapi.BoxError
	if errors.As(err, &be) {
		parts := []string{}
		if firmwareNamePattern.MatchString(be.Value) {
			parts = append(parts, be.Value)
		}
		if firmwareNamePattern.MatchString(be.Name) {
			parts = append(parts, be.Name)
		}
		return be.Status, strings.Join(parts, " "), scrubDeezerDetail(be.Detail, user, pass)
	}
	return 0, "", speakerErrorReason(err)
}

// scrubDeezerDetail takes the account and the password out of a firmware
// error text and clips it, so it can be kept in the attempt record.
func scrubDeezerDetail(detail, user, pass string) string {
	d := strings.TrimSpace(detail)
	if user != "" {
		d = strings.ReplaceAll(d, user, "***")
	}
	// A password shorter than this would mangle the text rather than hide
	// anything worth hiding; the firmware is not known to echo it at all.
	if len(pass) >= 3 {
		d = strings.ReplaceAll(d, pass, "***")
	}
	if len(d) > 160 {
		d = d[:160]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, d)
}

// deezerNewLoginGone is POST /api/deezer/account's answer since the e-mail
// and password sign-in came out.
var deezerNewLoginGone = map[string]any{
	"error": "a new Deezer sign-in is not possible without the Bose cloud; a Deezer login the speaker already had keeps working",
	"code":  "new-login-unsupported",
}

func (s *Server) handleDeezerAccount(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "music service settings only allowed from LAN", http.StatusForbidden)
		return
	}
	if s.deezer == nil {
		http.Error(w, "not available on this build", http.StatusNotImplemented)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.deezerView(ctx))
	case http.MethodPost:
		// Never handed on: the firmware takes the call and its Deezer
		// client then hangs until a power cycle. The body (an older app's
		// e-mail and password) is not read.
		s.logger.Info("deezer: refused a new login, it cannot work without Bose", "from", r.RemoteAddr)
		writeJSON(w, http.StatusGone, deezerNewLoginGone)
	case http.MethodDelete:
		res := s.removeAccountShared(ctx, deezerSource, deezerDisplayName, s.deezerAccountStore())
		if res.status == http.StatusOK {
			res.body["status"] = s.deezerView(ctx)
		}
		writeJSON(w, res.status, res.body)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleDeezerRecheck is POST /api/deezer/recheck, the card's "Check again".
// A JSON content type is required (the body is not read), so a cross-site
// form cannot start it.
func (s *Server) handleDeezerRecheck(w http.ResponseWriter, r *http.Request) {
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
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "music service settings need a JSON body", http.StatusUnsupportedMediaType)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res := s.RecreateDeezerFromKeptLogin(ctx, "check-again")
	v := s.deezerView(ctx)
	v.Recreate = &res
	writeJSON(w, http.StatusOK, v)
	// A speaker that keeps a login also offers it to the speakers of the
	// home that need one, and a speaker that needs one asks them for it;
	// that talks to them for some seconds, so not inline.
	s.deezerShare.wg.Add(1)
	go func() {
		defer s.deezerShare.wg.Done()
		octx, ocancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer ocancel()
		s.OfferKeptDeezerLogin(octx, deezerShareTriggerCheck)
		// And a speaker that needs one asks the others to offer theirs.
		s.RequestDeezerOffers(octx)
	}()
}

// RecreateDeezerFromKeptLogin hands the kept Deezer login back to a speaker
// that lists DEEZER without a working account: it removes the speaker's
// account (empty password) and sends it a sourcesUpdated notification, and
// the firmware creates the account again from /full, which carries the kept
// token. Without a kept login it makes no call at all. trigger is "start",
// "check-again" or "peer" (a login another speaker shared, deezershare.go),
// for the log and the record.
func (s *Server) RecreateDeezerFromKeptLogin(ctx context.Context, trigger string) DeezerRecreateResult {
	if s.deezer == nil {
		return DeezerRecreateResult{Skipped: "no-kept-login"}
	}
	kept := s.deezer.KeptDeezerAccount()
	if kept == "" {
		return DeezerRecreateResult{Skipped: "no-kept-login"}
	}
	if !s.deezerRecreateMu.TryLock() {
		return DeezerRecreateResult{Skipped: "busy"}
	}
	defer s.deezerRecreateMu.Unlock()
	if !s.deezerRecreateAt.IsZero() && time.Since(s.deezerRecreateAt) < deezerRecreateEvery {
		return DeezerRecreateResult{Skipped: "ran-recently"}
	}
	box := s.nativeBox()
	srcs, err := box.GetSources(ctx)
	if err != nil {
		s.logger.Warn("deezer: recreate: could not read the speaker's sources", "trigger", trigger, "err", err)
		return DeezerRecreateResult{Skipped: "unreadable"}
	}
	src := pickDeezerSource(srcs)
	switch {
	case src.Status == "absent":
		s.logger.Info("deezer: recreate: not needed, the speaker lists no Deezer account", "trigger", trigger)
		return DeezerRecreateResult{Skipped: "not-listed"}
	case src.Status == "READY" && src.HasAccount:
		s.logger.Info("deezer: recreate: not needed, the speaker's Deezer login works", "trigger", trigger)
		return DeezerRecreateResult{Skipped: "ready"}
	}
	account := src.account
	if account == "" {
		account = kept
	}
	s.deezerRecreateAt = time.Now()
	s.deezerRecreating.Store(true)
	defer s.deezerRecreating.Store(false)
	s.deezer.BeginDeezerAttempt("recreate")
	s.logger.Info("deezer: recreate: the speaker lists Deezer without a working login and STR keeps one, removing the speaker's account",
		"trigger", trigger, "sourceStatus", src.Status, "account", maskForLog(account))
	err = box.RemoveMusicServiceAccount(ctx, deezerSource, deezerDisplayName, account)
	status, code, detail := deezerFirmwareAnswer(err, account, "")
	s.deezer.NoteDeezerFirmwareAnswer(status, code, detail)
	s.logger.Info("deezer: recreate: the speaker answered the removal",
		"firmwareStatus", status, "firmwareError", code, "alreadyRemoved", isSourceAlreadyRemoved(err))
	if err != nil && !isSourceAlreadyRemoved(err) {
		s.deezer.NoteDeezerRecreate(trigger, 0, "")
		what := code
		if what == "" {
			what = "HTTP " + strconv.Itoa(status)
		}
		return DeezerRecreateResult{Ran: true, Error: "the speaker refused the removal (" + what + ")"}
	}
	nerr := box.NotifySourcesUpdated(ctx)
	nStatus, _, _ := deezerFirmwareAnswer(nerr, "", "")
	nText := ""
	if nerr != nil {
		nText = speakerErrorReason(nerr)
	}
	s.deezer.NoteDeezerRecreate(trigger, nStatus, nText)
	s.logger.Info("deezer: recreate: told the speaker its sources changed, it reads the account document again",
		"notifyStatus", nStatus, "err", nText)
	if nerr != nil {
		return DeezerRecreateResult{Ran: true, Error: "the speaker did not take the sources notification (" + nText + ")"}
	}
	return DeezerRecreateResult{Ran: true}
}

// DeezerRecreateAtStart is the recreate's one run after the agent started.
// The agent calls it once in its start-up sequence: without a kept login it
// returns at once without a call to the speaker; otherwise it waits once for
// the firmware to come up and log in by itself, runs the recreate when it
// still applies, and reads the outcome once more after outcomeAfter, for the
// log and the deezer_login debug section. No loop, no timer beyond these two
// waits.
func (s *Server) DeezerRecreateAtStart(ctx context.Context, wait, outcomeAfter time.Duration) {
	if s.deezer == nil || s.deezer.KeptDeezerAccount() == "" {
		return
	}
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return
	}
	s.deezerRecreateAndReport(ctx, "start", outcomeAfter)
}

// deezerRecreateAndReport runs the recreate and, when it ran, reads the
// speaker's Deezer source once more after outcomeAfter, for the log and the
// deezer_login debug section. One wait, no loop.
func (s *Server) deezerRecreateAndReport(ctx context.Context, trigger string, outcomeAfter time.Duration) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	res := s.RecreateDeezerFromKeptLogin(cctx, trigger)
	cancel()
	if !res.Ran {
		s.logger.Info("deezer: recreate skipped", "trigger", trigger, "why", res.Skipped)
		return
	}
	select {
	case <-time.After(outcomeAfter):
	case <-ctx.Done():
		return
	}
	cctx, cancel = context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	v := s.deezerView(cctx)
	retry := ""
	if v.Login.LastRetry != nil {
		retry = v.Login.LastRetry.Code
	}
	s.logger.Info("deezer: recreate: the speaker's Deezer source afterwards",
		"trigger", trigger, "sourceStatus", v.Source.Status, "hasAccount", v.Source.HasAccount, "state", v.State, "retryCode", retry)
}

// deezerAccountStore is Deezer's account on STR's side for a removal: the
// kept login (a new one the firmware registered, or the one the speaker held
// before STR), the carried-over entry, and the attempt record that sees the
// speaker's answer. The removal's record starts when the removal itself runs
// (registered is its first call), not for a DELETE that only waits for one.
func (s *Server) deezerAccountStore() musicAccountStore {
	return musicAccountStore{
		registered: func() string {
			s.deezer.BeginDeezerAttempt("remove")
			return s.deezer.KeptDeezerAccount()
		},
		carriedOver: s.deezer.CarriedOverDeezerAccount,
		clear:       s.deezer.ClearKeptDeezer,
		answered: func(err error) {
			status, code, _ := deezerFirmwareAnswer(err, "", "")
			s.deezer.NoteDeezerFirmwareAnswer(status, code, "")
		},
	}
}
