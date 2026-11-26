// Deezer: keep the login a speaker already holds (docs/streaming/deezer.md).
//
// The firmware keeps every linked service's login in its own Sources.xml and
// rebuilds its source list from the account document (/full) on every boot.
// STR's stand-in used to name a carried-over Deezer source with an EMPTY
// credential, because the speaker's /sources never shows the token: the entry
// survived, but the firmware marked it UNAVAILABLE and a Deezer key answered
// 1036 "not logged in". A cloud replacement that hands the original token back
// keeps Deezer READY and playing (gesellix/Bose-SoundTouch#521).
//
// So the agent reads the Deezer entry of Sources.xml once per start, BEFORE the
// stand-in listens (CaptureKeptDeezerLogin), keeps it in a 0600 file on the
// speaker, and /full hands it back to the speaker itself. A speaker without a
// Deezer login in Sources.xml is untouched: no file is written and every
// document is byte for byte what it was.
//
// The login is the owner's. /full reveals it only to a loopback caller (the
// firmware reaches the stand-in through /etc/hosts), every other client gets
// "***", and no log line, debug section or diagnostic bundle carries it or the
// account. The one way it leaves the speaker is sealed to another STR speaker
// of the home with the same account, and only when the owner presses Copy in
// the Deezer area (deezershare.go).
//
// A kept login that a different one replaces is never dropped: it moves to a
// small backup file beside the kept one first (deezerprevious.go).

package marge

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/atomicfile"
	"github.com/JRpersonal/streborn/internal/boxsources"
)

// keptLogin is a service login the speaker held, as kept on the speaker.
type keptLogin struct {
	Account     string `json:"account"`
	DisplayName string `json:"displayName,omitempty"`
	SecretType  string `json:"secretType,omitempty"`
	Secret      string `json:"secret"`
	// From is "sources-file", "registration", "peer" (copied from another
	// speaker on the owner's press) or "restored" (an older backed-up login
	// put back, deezerprevious.go).
	From   string    `json:"from"`
	KeptAt time.Time `json:"keptAt"`
	// Peer is the pseudonym of the speaker a login came from when From is
	// "peer" (deezershare.go), never its address; PeerName is that speaker's
	// name as the roster knew it, for the Deezer area's overview.
	Peer     string `json:"peer,omitempty"`
	PeerName string `json:"peerName,omitempty"`
}

// sameLogin reports whether two records hold the same login (the time and the
// origin aside), so an unchanged capture writes nothing.
func (k keptLogin) sameLogin(o keptLogin) bool {
	return k.Account == o.Account && k.DisplayName == o.DisplayName &&
		k.SecretType == o.SecretType && k.Secret == o.Secret
}

// WithKeptDeezerLogin wires the file the speaker's own Deezer login is kept
// in (e.g. /mnt/nv/streborn/deezer-login.json). Empty keeps the feature off.
func WithKeptDeezerLogin(path string) Option {
	return func(s *Server) { s.deezerLoginPath = path }
}

func loadKeptLogin(path string) (keptLogin, bool) {
	if path == "" {
		return keptLogin{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return keptLogin{}, false
	}
	var k keptLogin
	if json.Unmarshal(b, &k) != nil || k.Secret == "" || strings.TrimSpace(k.Account) == "" {
		return keptLogin{}, false
	}
	return k, true
}

func saveKeptLogin(path string, k keptLogin) error {
	b, err := json.Marshal(k)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(path, b, 0o600)
}

// CaptureKeptDeezerLogin keeps the speaker's own Deezer login. The agent calls
// it once per start, before the stand-in listens, so it reads Sources.xml
// before STR answers the firmware's first account sync of this boot.
//
// It writes only when Sources.xml holds a DEEZER entry with a login and the
// kept file does not already hold the same login. Without such an entry it
// does nothing at all, and an existing kept login is left as it is: the
// firmware may have rewritten its file since (an older STR answered without
// the token), and the kept copy is then the only one.
//
// It reports whether a login is kept after the call.
func CaptureKeptDeezerLogin(sourcesGlob, storePath string, logger *slog.Logger) bool {
	if storePath == "" {
		return false
	}
	prev, hadPrev := loadKeptLogin(storePath)
	_, entries, err := boxsources.Read(sourcesGlob)
	if err != nil {
		// Never the error text: a parse error can quote the file.
		logger.Warn("deezer: the speaker's source store could not be read", slog.Bool("loginKept", hadPrev))
		return hadPrev
	}
	for _, e := range entries {
		if !strings.EqualFold(e.Type, "DEEZER") || e.Secret == "" || strings.TrimSpace(e.Account) == "" {
			continue
		}
		k := keptLogin{
			Account: strings.TrimSpace(e.Account), DisplayName: e.DisplayName,
			SecretType: e.SecretType, Secret: e.Secret,
			From: "sources-file", KeptAt: time.Now().UTC(),
		}
		if hadPrev && prev.sameLogin(k) {
			return true
		}
		if hadPrev && refusedBefore(storePath, k) {
			// Deezer refused this one before and the owner put an older
			// login back: the firmware's file still holds the refused one
			// until its account is created anew. Keeping it would undo the
			// swap.
			logger.Info("deezer: Sources.xml holds a Deezer login Deezer refused before, keeping the one kept now",
				slog.String("account", maskAccount(prev.Account)))
			return true
		}
		backedUp, err := replaceKeptLogin(storePath, k, false)
		if err != nil {
			logger.Warn("deezer: the speaker's own Deezer login could not be kept", slog.String("err", err.Error()))
			return hadPrev
		}
		logger.Info("deezer: kept the speaker's own Deezer login, the account document hands it back",
			slog.String("account", maskAccount(k.Account)), slog.String("secretType", k.SecretType),
			slog.Bool("replaced", hadPrev), slog.Bool("previousBackedUp", backedUp))
		return true
	}
	if hadPrev {
		logger.Info("deezer: Sources.xml holds no Deezer login, keeping the one kept earlier",
			slog.String("account", maskAccount(prev.Account)))
	}
	return hadPrev
}

// keptDeezer returns the kept Deezer login, if any.
func (s *Server) keptDeezer() (keptLogin, bool) {
	return loadKeptLogin(s.deezerLoginPath)
}

// keptDeezerFor attaches the kept login to a carried-over DEEZER entry with
// the same account. It returns the credential type and value to render;
// reveal=false masks the value.
func (s *Server) keptDeezerFor(account string, reveal bool) (typ, value string, ok bool) {
	k, have := s.keptDeezer()
	if !have || !strings.EqualFold(strings.TrimSpace(account), k.Account) {
		return "", "", false
	}
	typ = k.SecretType
	if typ == "" {
		typ = "token"
	}
	value = k.Secret
	if !reveal {
		value = "***"
	}
	return typ, value, true
}

// isDeezerRegistration reports whether an addSource body registers Deezer
// (provider id 14 or sourcename DEEZER).
func isDeezerRegistration(body string) bool {
	return strings.TrimSpace(firstXMLValue(body, "sourceproviderid")) == "14" ||
		strings.EqualFold(strings.TrimSpace(firstXMLValue(body, "sourcename")), "DEEZER")
}

// respondDeezerAddSource answers the firmware's own registration of a Deezer
// source in the shape it asked for, instead of filing it as a media server.
// A registration that carries a credential replaces the kept login, so a
// login the speaker makes itself is the one /full hands back from then on;
// the login it replaces is backed up first (deezerprevious.go).
func (s *Server) respondDeezerAddSource(w http.ResponseWriter, r *http.Request, body string) {
	username := strings.TrimSpace(firstXMLValue(body, "username"))
	credType, cred := credentialOf(body)
	kept := false
	if s.deezerLoginPath != "" && cred != "" && username != "" {
		prev, hadPrev := s.keptDeezer()
		k := keptLogin{Account: username, DisplayName: prev.DisplayName, SecretType: credType,
			Secret: cred, From: "registration", KeptAt: time.Now().UTC()}
		if hadPrev && prev.sameLogin(k) {
			kept = true
		} else if _, err := replaceKeptLogin(s.deezerLoginPath, k, false); err != nil {
			s.logger.Warn("deezer: the registered login could not be kept", slog.String("err", err.Error()))
		} else {
			kept = true
		}
	}
	s.noteDeezerRegistration(cred != "")
	s.logger.Info("deezer: source registration from the box",
		slog.String("path", r.URL.Path), slog.String("username", maskAccount(username)),
		slog.String("credentialType", credType), slog.Bool("hasCredential", cred != ""),
		slog.Bool("kept", kept))
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.Header().Set("METHOD_NAME", "addSource")
	id := "100"
	if src, ok := s.keptDeezerSource(); ok {
		id = src.id
	}
	w.Header().Set("ETag", `"str-source-`+id+`"`)
	w.Header().Set("Location", r.URL.Path)
	w.WriteHeader(http.StatusCreated)
	// The echo never carries the credential: the firmware has it, and the
	// answer is what a LAN client replaying the call would see.
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>` +
		`<source id="` + id + `" type="Audio" status="READY"><credential type="` + xmlEscapeText(credType) + `"></credential>` +
		`<name>Deezer</name><username>` + xmlEscapeText(username) + `</username>` +
		`<sourceproviderid>14</sourceproviderid><sourcename>DEEZER</sourcename></source>`))
}

// DeezerLoginStatus is the diagnostic view of the kept login: never the
// login, never the account.
type DeezerLoginStatus struct {
	Configured bool   `json:"configured"`
	Kept       bool   `json:"kept"`
	SecretType string `json:"secretType,omitempty"`
	// From is where the kept login came from: "sources-file" (the speaker's
	// own Sources.xml), "registration" (the firmware registered it) or
	// "peer" (copied from another speaker of the home on the owner's press,
	// deezershare.go) or "restored" (an older backed-up login put back).
	From   string    `json:"from,omitempty"`
	KeptAt time.Time `json:"keptAt,omitempty"`
	// FromPeer is the pseudonym of the speaker the login was copied from, set
	// when From is "peer".
	FromPeer string `json:"fromPeer,omitempty"`
	// PreviousCount is how many older logins are backed up beside the kept
	// one (at most 3), PreviousRefused how many of them Deezer refused.
	PreviousCount   int `json:"previousCount"`
	PreviousRefused int `json:"previousRefused,omitempty"`
	// Shares are the last copies this agent sent to its peers and received
	// from them since it started (each one an owner's press); nil before
	// the first.
	Shares *DeezerShareLog `json:"shares,omitempty"`
	// LastAttempt is the last recreate or removal STR handed to the
	// firmware since the agent started, and what followed
	// (deezernewlogin.go); nil when there was none.
	LastAttempt *DeezerAttempt `json:"lastAttempt,omitempty"`
	// LastRetry is the firmware's last Deezer relogin retry code from its
	// syslog (3351 no token, 3330 token rejected by Deezer); nil before one.
	LastRetry *DeezerRetry `json:"lastRetry,omitempty"`
}

// DeezerLoginSnapshot reports whether a Deezer login is kept, for the debug
// state. Read on demand.
func (s *Server) DeezerLoginSnapshot() DeezerLoginStatus {
	st := DeezerLoginStatus{Configured: s.deezerLoginPath != ""}
	if k, ok := s.keptDeezer(); ok {
		st.Kept, st.SecretType, st.From, st.KeptAt, st.FromPeer = true, k.SecretType, k.From, k.KeptAt, k.Peer
	}
	for _, r := range s.previousDeezer() {
		st.PreviousCount++
		if r.Refused {
			st.PreviousRefused++
		}
	}
	st.Shares = s.deezerShareLog()
	st.LastAttempt = s.LastDeezerAttempt()
	st.LastRetry = s.lastDeezerRetry()
	return st
}
