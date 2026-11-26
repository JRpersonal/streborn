// Deezer: the account steps STR hands to the speaker's own Deezer client
// (docs/streaming/deezer.md).
//
// A new Deezer login cannot work without Bose (A2 is closed, see the doc), so
// the only steps left are a recreate of the kept login (the firmware drops its
// UNAVAILABLE account and re-creates it from /full, which now carries the
// token, internal/webui/deezer.go) and a removal.
//
// The stand-in records each step: when it started and why, what the firmware
// answered, every request the firmware makes to the stand-in in the minute
// after (method, host, path, size; never a body), whether a Deezer source was
// registered, and the DEEZER status the speaker lists afterwards. The record
// is the deezer_login debug section's lastAttempt, so one bundle answers what
// happened. No timer: the window is a time comparison made when a request
// arrives.
//
// A registered login and a kept one are one thing on STR's side: the
// registration writes the same kept-login file A1 fills from Sources.xml, and
// /full serves it back the same way. Deezer is therefore not a row of
// nativeServices (usservices.go): that list's own store, US gate and /full
// entry would name the same account twice.

package marge

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxsnapshot"
)

// deezerAttemptWindow is how long after a login attempt the stand-in's
// requests are recorded with it. The firmware's Pandora registration follows
// its login within seconds; a minute covers a slow Deezer round trip.
const deezerAttemptWindow = 60 * time.Second

// deezerAttemptHitCap bounds the requests kept per attempt.
const deezerAttemptHitCap = 40

// DeezerStandInHit is one request the firmware made to the stand-in during
// an attempt's window. Never the body: it can carry the credential.
type DeezerStandInHit struct {
	// AfterMs is the time since the attempt started.
	AfterMs   int64  `json:"afterMs"`
	Method    string `json:"method"`
	Host      string `json:"host,omitempty"`
	Path      string `json:"path"`
	BodyBytes int    `json:"bodyBytes"`
	// Deezer is set when the path or the body names Deezer.
	Deezer bool `json:"deezer,omitempty"`
}

// DeezerAttempt is the last account step STR handed to the firmware and what
// followed. Never the password, the token or the account.
type DeezerAttempt struct {
	// Kind is "recreate" (drop the speaker's unusable account so it re-creates
	// it from /full with the kept token) or "remove". An older agent also
	// wrote "login" (a new login, no longer offered).
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
	// Trigger is why a recreate ran: "start" (once after the agent started)
	// or "check-again" (the card's button).
	Trigger string `json:"trigger,omitempty"`
	// NotifyStatus is the HTTP status of the recreate's sourcesUpdated
	// notification (0: not sent or no answer), NotifyError its error.
	NotifyStatus int    `json:"notifyStatus,omitempty"`
	NotifyError  string `json:"notifyError,omitempty"`
	// FirmwareStatus is the HTTP status the speaker answered the account call
	// with, 0 when it did not answer.
	FirmwareStatus int `json:"firmwareStatus"`
	// FirmwareError is the firmware's error code and name ("1005
	// MUSIC_SERVICE_LOGIN_FAILED"), "" on success or when it gave none.
	FirmwareError string `json:"firmwareError,omitempty"`
	// FirmwareDetail is the firmware's error text with the account and the
	// password taken out, clipped.
	FirmwareDetail string `json:"firmwareDetail,omitempty"`
	// SourceStatus is the DEEZER status the speaker listed in /sources the
	// last time STR read it after the attempt ("READY", "UNAVAILABLE",
	// "absent"), "" before the first read.
	SourceStatus     string    `json:"sourceStatus,omitempty"`
	SourceHasAccount bool      `json:"sourceHasAccount"`
	SourceCheckedAt  time.Time `json:"sourceCheckedAt,omitempty"`
	// Registered is set when the firmware registered a Deezer source with the
	// stand-in during the window, RegisteredCredential when that registration
	// carried a credential (the kept-login file then holds it).
	Registered           bool `json:"registered"`
	RegisteredCredential bool `json:"registeredCredential"`
	// StandInRequests are the firmware's requests to the stand-in during
	// the window, oldest first; StandInDropped counts those past the cap.
	StandInRequests []DeezerStandInHit `json:"standInRequests"`
	StandInDropped  int                `json:"standInDropped,omitempty"`
}

// NoteDeezerRecreate records why a recreate ran and how the speaker answered
// its sourcesUpdated notification (status 0 and errText "" when it was not
// sent). errText must be free of the account.
func (s *Server) NoteDeezerRecreate(trigger string, notifyStatus int, errText string) {
	s.deezerMu.Lock()
	defer s.deezerMu.Unlock()
	if s.deezerAttempt == nil {
		return
	}
	s.deezerAttempt.Trigger = trigger
	s.deezerAttempt.NotifyStatus = notifyStatus
	s.deezerAttempt.NotifyError = errText
}

// SetDeezerRetrySource wires the reader of the firmware's last Deezer relogin
// retry code (internal/boxlog DeezerLastRetry). Nil leaves it unreported.
func (s *Server) SetDeezerRetrySource(fn func() (code string, at time.Time, ok bool)) {
	s.deezerMu.Lock()
	s.deezerRetry = fn
	s.deezerMu.Unlock()
}

// DeezerRetry is the firmware's last Deezer relogin retry, read from its
// syslog: why its Deezer client cannot log in.
type DeezerRetry struct {
	Code    string    `json:"code"`
	Meaning string    `json:"meaning"`
	At      time.Time `json:"at"`
}

// DeezerRetryMeaning names a relogin retry code. Seen on a Portable (27.0.6):
// 3351 while the firmware holds no Deezer token, 3330 once a token reached
// Deezer and Deezer refused it (expired, revoked or not a real one).
func DeezerRetryMeaning(code string) string {
	switch code {
	case "3351":
		return "no token: the speaker holds no Deezer login it could use"
	case "3330":
		return "token rejected: Deezer refused the speaker's login (expired or revoked)"
	default:
		return "unknown code"
	}
}

// lastDeezerRetry reads the retry source, nil without one or before the
// firmware logged a retry.
func (s *Server) lastDeezerRetry() *DeezerRetry {
	s.deezerMu.Lock()
	fn := s.deezerRetry
	s.deezerMu.Unlock()
	if fn == nil {
		return nil
	}
	code, at, ok := fn()
	if !ok || code == "" {
		return nil
	}
	return &DeezerRetry{Code: code, Meaning: DeezerRetryMeaning(code), At: at}
}

// BeginDeezerAttempt starts a new attempt record ("recreate" or "remove") and
// its request window. It replaces the previous record.
func (s *Server) BeginDeezerAttempt(kind string) {
	s.deezerMu.Lock()
	s.deezerAttempt = &DeezerAttempt{Kind: kind, At: time.Now().UTC(), StandInRequests: []DeezerStandInHit{}}
	s.deezerMu.Unlock()
	s.logger.Info("deezer: attempt started, recording the firmware's stand-in requests",
		slog.String("kind", kind), slog.Duration("window", deezerAttemptWindow))
}

// NoteDeezerFirmwareAnswer records the speaker's answer to the account call.
// errCode and detail must already be free of the account and the password.
func (s *Server) NoteDeezerFirmwareAnswer(status int, errCode, detail string) {
	s.deezerMu.Lock()
	defer s.deezerMu.Unlock()
	if s.deezerAttempt == nil {
		return
	}
	s.deezerAttempt.FirmwareStatus = status
	s.deezerAttempt.FirmwareError = errCode
	s.deezerAttempt.FirmwareDetail = detail
}

// NoteDeezerSourceStatus records the DEEZER status the speaker lists, read
// after the attempt (the account call, and every status read after it).
func (s *Server) NoteDeezerSourceStatus(status string, hasAccount bool) {
	s.deezerMu.Lock()
	defer s.deezerMu.Unlock()
	if s.deezerAttempt == nil {
		return
	}
	s.deezerAttempt.SourceStatus = status
	s.deezerAttempt.SourceHasAccount = hasAccount
	s.deezerAttempt.SourceCheckedAt = time.Now().UTC()
}

// LastDeezerAttempt returns a copy of the last attempt, nil when there was
// none since the agent started.
func (s *Server) LastDeezerAttempt() *DeezerAttempt {
	s.deezerMu.Lock()
	defer s.deezerMu.Unlock()
	if s.deezerAttempt == nil {
		return nil
	}
	cp := *s.deezerAttempt
	cp.StandInRequests = append([]DeezerStandInHit{}, s.deezerAttempt.StandInRequests...)
	return &cp
}

// noteDeezerRegistration marks the attempt as answered by a registration.
func (s *Server) noteDeezerRegistration(withCredential bool) {
	s.deezerMu.Lock()
	defer s.deezerMu.Unlock()
	if s.deezerAttempt == nil || time.Since(s.deezerAttempt.At) > deezerAttemptWindow {
		return
	}
	s.deezerAttempt.Registered = true
	if withCredential {
		s.deezerAttempt.RegisteredCredential = true
	}
}

// deezerRequestNamed reports whether a stand-in request names Deezer, by its
// path or by a source in its body. The body itself is never logged.
func deezerRequestNamed(path, body string) bool {
	if strings.Contains(strings.ToLower(path), "deezer") {
		return true
	}
	if body == "" {
		return false
	}
	return strings.Contains(body, "<sourcename>DEEZER</sourcename>") ||
		strings.Contains(body, "<sourceproviderid>14</sourceproviderid>") ||
		strings.Contains(body, `source="DEEZER"`)
}

// noteDeezerRequest is the spy's Deezer hook, called for every stand-in
// request. Inside an attempt's window the request is recorded with the
// attempt and logged; outside it, only a request that names Deezer is logged
// (rare: a registration, or the firmware asking about the source).
func (s *Server) noteDeezerRequest(r *http.Request, path string, bodyBytes int, named bool) {
	now := time.Now()
	s.deezerMu.Lock()
	a := s.deezerAttempt
	inWindow := a != nil && now.Sub(a.At) <= deezerAttemptWindow
	var after time.Duration
	if inWindow {
		after = now.Sub(a.At)
		if len(a.StandInRequests) < deezerAttemptHitCap {
			a.StandInRequests = append(a.StandInRequests, DeezerStandInHit{
				AfterMs: after.Milliseconds(), Method: r.Method, Host: r.Host,
				Path: path, BodyBytes: bodyBytes, Deezer: named,
			})
		} else {
			a.StandInDropped++
		}
	}
	s.deezerMu.Unlock()
	switch {
	case inWindow:
		s.logger.Info("deezer: stand-in request during the login attempt",
			slog.Int64("afterMs", after.Milliseconds()),
			slog.String("method", r.Method), slog.String("host", r.Host),
			slog.String("path", path), slog.Int("bodyBytes", bodyBytes),
			slog.Bool("namesDeezer", named))
	case named:
		s.logger.Info("marge: Deezer request from the box",
			slog.String("method", r.Method), slog.String("path", path),
			slog.Int("bodyBytes", bodyBytes))
	}
}

// KeptDeezerAccount returns the account of the kept Deezer login unmasked,
// for the agent's own removal call to the firmware. Never for display. ""
// when none is kept.
func (s *Server) KeptDeezerAccount() string {
	k, ok := s.keptDeezer()
	if !ok {
		return ""
	}
	return k.Account
}

// CarriedOverDeezerAccount returns the Deezer account the speaker held before
// STR, as the reflect file carries it over, unmasked, for the removal call.
// Never for display. "" when there is none.
func (s *Server) CarriedOverDeezerAccount() string {
	for _, r := range s.reflected() {
		if strings.EqualFold(strings.TrimSpace(r.Source), "DEEZER") && strings.TrimSpace(r.Account) != "" {
			return strings.TrimSpace(r.Account)
		}
	}
	return ""
}

// ClearKeptDeezer forgets the Deezer login on STR's side: the kept-login file
// and the carried-over entry in the reflect file. Without the second, /full
// would keep naming the account and the firmware would pick it up again at
// its next account sync (the same reason ClearNative drops it, #1101). The
// backed-up older logins go too (deezerprevious.go): forgetting means
// forgetting.
func (s *Server) ClearKeptDeezer() error {
	if s.deezerLoginPath != "" {
		if err := os.Remove(s.deezerLoginPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(previousLoginPath(s.deezerLoginPath)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if s.reflectPath != "" {
		removed, err := boxsnapshot.RemoveReflect(s.reflectPath, "DEEZER")
		if err != nil {
			return err
		}
		if removed {
			s.logger.Info("deezer: carried-over account dropped from the reflect file")
		}
	}
	s.logger.Info("deezer: the kept login is forgotten")
	return nil
}
