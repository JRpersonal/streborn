package webui

// Deezer: signing the speaker's own Deezer client in from STR
// (docs/streaming/deezer.md, A2). Built before anyone has seen it work: the
// firmware may not be able to finish a Deezer login without Bose at all. So
// every step is recorded (internal/marge/deezernewlogin.go) and the answer
// says plainly what the speaker did.
//
// GET    /api/deezer/account  where Deezer stands on this speaker: the state
//                             the apps show, the speaker's own DEEZER line
//                             from /sources (masked), the kept login (type
//                             and origin only) and the last attempt
// POST   /api/deezer/account  {"user":"...","password":"..."} hands a Deezer
//                             account to the firmware's own
//                             setMusicServiceAccount, source DEEZER, the
//                             shape AfterTouch sent; one at a time
// DELETE /api/deezer/account  removes it from the firmware and forgets the
//                             kept login; safe to repeat, one removal at a
//                             time (shared with the Pandora removals)
//
// The same rules as the Pandora endpoints (usservices.go): LAN only, a JSON
// body for POST, the password passed straight to the speaker and never
// stored, logged or echoed by STR. Unlike Pandora no switch is needed:
// Deezer is not geo-restricted, and the stand-in offers it on every speaker.
// Unlike the Pandora login, a refusal is logged by its status and code only,
// never as the firmware's raw answer: nobody knows yet what the Deezer client
// echoes.

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
// one AfterTouch sent to a real speaker (gesellix/Bose-SoundTouch#574).
const deezerDisplayName = "Deezer Premium"

// deezerConnectingFor is how long after an accepted login the state reads
// "connecting" rather than "failed" while the speaker does not list Deezer
// READY yet. Longer than the stand-in's request window, so the window's
// registration has landed before the apps call it a failure.
const deezerConnectingFor = 90 * time.Second

// DeezerBackend is the stand-in's side (implemented by *marge.Server).
type DeezerBackend interface {
	DeezerLoginSnapshot() marge.DeezerLoginStatus
	BeginDeezerAttempt(kind string)
	NoteDeezerFirmwareAnswer(status int, errCode, detail string)
	NoteDeezerSourceStatus(status string, hasAccount bool)
	KeptDeezerAccount() string
	CarriedOverDeezerAccount() string
	ClearKeptDeezer() error
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

// DeezerAccountView is GET /api/deezer/account's answer.
type DeezerAccountView struct {
	// State is what the apps show:
	//   connected      the speaker lists DEEZER READY with an account
	//   connecting     STR handed a login over less than a minute and a
	//                  half ago, the speaker took it, and it is not READY yet
	//   failed         the last login was refused, or taken but never
	//                  became READY (Reason says which)
	//   unavailable    the speaker holds a Deezer account it cannot use
	//                  (not logged in), and STR handed none over since
	//   not-connected  no Deezer account on the speaker
	//   unknown        the speaker's source list could not be read
	State string `json:"state"`
	// Reason is the code of a failed state: "refused" (the speaker answered
	// the login with an error), "no-answer" (it did not answer), "not-ready"
	// (it took the login and never listed Deezer READY).
	Reason string `json:"reason,omitempty"`
	// Detail is the firmware's error code and name for "refused", a short
	// client-safe text otherwise.
	Detail string `json:"detail,omitempty"`
	// Account is the account the speaker lists or STR keeps, masked.
	Account string                  `json:"account,omitempty"`
	Source  deezerSourceLine        `json:"source"`
	Login   marge.DeezerLoginStatus `json:"login"`
}

// deezerState derives the state from the speaker's DEEZER line and the last
// attempt. readErr is the /sources read's error.
func deezerState(src deezerSourceLine, readErr error, a *marge.DeezerAttempt, now time.Time) (state, reason, detail string) {
	if readErr != nil {
		return "unknown", "", speakerErrorReason(readErr)
	}
	if src.Status == "READY" && src.HasAccount {
		return "connected", "", ""
	}
	if a != nil && a.Kind == "login" {
		switch {
		case a.FirmwareStatus == 0:
			return "failed", "no-answer", "the speaker did not answer the login"
		case a.FirmwareStatus >= 400:
			d := a.FirmwareError
			if d == "" {
				d = "HTTP " + strconv.Itoa(a.FirmwareStatus)
			}
			return "failed", "refused", d
		case now.Sub(a.At) < deezerConnectingFor:
			return "connecting", "", ""
		default:
			d := "the speaker lists Deezer as " + src.Status
			if src.Status == "absent" {
				d = "the speaker lists no Deezer account"
			}
			return "failed", "not-ready", d
		}
	}
	if src.HasAccount {
		return "unavailable", "", "the speaker lists Deezer as " + src.Status
	}
	return "not-connected", "", ""
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
		s.deezerLogin(ctx, w, r)
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

// deezerLogin is POST /api/deezer/account.
func (s *Server) deezerLogin(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var req struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if !decodeNativeJSON(w, r, &req) {
		return
	}
	req.User = strings.TrimSpace(req.User)
	if req.User == "" || req.Password == "" {
		http.Error(w, "user and password are required", http.StatusBadRequest)
		return
	}
	// One hand-over at a time: a second Connect while the first runs would
	// send the firmware a second login in the middle of the first.
	if !s.deezerLoginMu.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "a Deezer login is already being handed to the speaker",
			"code":  "login-busy",
		})
		return
	}
	defer s.deezerLoginMu.Unlock()
	s.deezer.BeginDeezerAttempt("login")
	s.logger.Info("deezer: handing a new login to the speaker's own client",
		"user", maskForLog(req.User), "from", r.RemoteAddr)
	err := s.nativeBox().SetMusicServiceAccount(ctx, deezerSource, deezerDisplayName, req.User, req.Password)
	status, code, detail := deezerFirmwareAnswer(err, req.User, req.Password)
	s.deezer.NoteDeezerFirmwareAnswer(status, code, detail)
	// Status and code only: the firmware's raw answer could echo the login.
	s.logger.Info("deezer: the speaker answered the new login",
		"firmwareStatus", status, "firmwareError", code, "accepted", err == nil)
	view := s.deezerView(ctx)
	s.logger.Info("deezer: the speaker's Deezer source after the login",
		"sourceStatus", view.Source.Status, "hasAccount", view.Source.HasAccount, "state", view.State)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":  "the speaker refused the Deezer login (" + speakerErrorReason(err) + ")",
			"code":   "account-refused",
			"status": view,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"note":   "the speaker logs in with Deezer next; check GET /api/deezer/account in a minute",
		"status": view,
	})
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
