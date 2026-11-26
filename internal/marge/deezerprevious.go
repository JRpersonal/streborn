// Deezer: back up a kept login before another one replaces it
// (docs/streaming/deezer.md, "Older logins").
//
// No new Deezer login can be made since the Bose cloud is gone, so a kept
// login may be the last working copy anywhere. Every time a DIFFERENT login
// takes the kept one's place (the speaker's own Sources.xml changed, the
// firmware registered a new one, the owner copied one over from another
// speaker, or put an older one back), the kept login moves to a small backup
// file beside it first: deezer-login-previous.json, mode 0600, at most three
// entries, newest first, each with when it was retired and whether Deezer
// refused it. The owner can put a backed-up login back from the Deezer area
// when Deezer refuses the current one; a refused one is never offered again,
// so swapping never goes back and forth.
//
// The file follows the kept one's rules: nothing here logs, records or returns
// the login or the account in clear. Forgetting Deezer on STR's side
// (ClearKeptDeezer) deletes it with the kept file.

package marge

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/atomicfile"
)

// deezerPreviousFile is the backup file's name, beside the kept file.
const deezerPreviousFile = "deezer-login-previous.json"

// deezerPreviousCap is how many older logins are backed up at most.
const deezerPreviousCap = 3

// ErrDeezerNoOlderLogin: no backed-up login for the kept account that Deezer
// has not refused.
var ErrDeezerNoOlderLogin = errors.New("no-older-login")

// deezerSwapCrashAfter is a test seam: a swap stops after this many of its
// three writes, as if the process died there. 0 runs every write.
var deezerSwapCrashAfter = 0

// retiredLogin is a kept login a different one replaced.
type retiredLogin struct {
	keptLogin
	RetiredAt time.Time `json:"retiredAt"`
	// Refused is set when Deezer had refused this login when it was retired.
	Refused bool `json:"refused,omitempty"`
}

// previousLoginPath is the backup file beside keptPath, "" without one.
func previousLoginPath(keptPath string) string {
	if keptPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(keptPath), deezerPreviousFile)
}

// sameToken reports whether two records hold the same token for the same
// account, whatever their display names: the backups compare by this.
func (k keptLogin) sameToken(o keptLogin) bool {
	return k.Secret == o.Secret && k.SecretType == o.SecretType &&
		strings.EqualFold(strings.TrimSpace(k.Account), strings.TrimSpace(o.Account))
}

// usable reports whether a record holds a login at all.
func (k keptLogin) usable() bool {
	return k.Secret != "" && strings.TrimSpace(k.Account) != ""
}

// loadPreviousLogins reads the backup list, newest first. Entries without a
// login are dropped.
func loadPreviousLogins(keptPath string) []retiredLogin {
	p := previousLoginPath(keptPath)
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var list []retiredLogin
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	out := list[:0]
	for _, r := range list {
		if r.usable() {
			out = append(out, r)
		}
	}
	return out
}

// savePreviousLogins writes the backup list (0600, atomically); an empty list
// removes the file.
func savePreviousLogins(keptPath string, list []retiredLogin) error {
	p := previousLoginPath(keptPath)
	if p == "" {
		return nil
	}
	if len(list) == 0 {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(p, b, 0o600)
}

// withRetired puts old at the head of list, as retired now. An entry with
// the same login is folded into it (a login Deezer refused stays refused).
// No cap: the caller caps when nothing can be lost any more.
func withRetired(list []retiredLogin, old keptLogin, refused bool, now time.Time) []retiredLogin {
	head := retiredLogin{keptLogin: old, RetiredAt: now.UTC(), Refused: refused}
	out := []retiredLogin{head}
	for _, r := range list {
		if r.sameToken(old) {
			out[0].Refused = out[0].Refused || r.Refused
			continue
		}
		out = append(out, r)
	}
	return out
}

// withoutLogin drops every entry holding k's login.
func withoutLogin(list []retiredLogin, k keptLogin) []retiredLogin {
	out := make([]retiredLogin, 0, len(list))
	for _, r := range list {
		if !r.sameToken(k) {
			out = append(out, r)
		}
	}
	return out
}

func capPrevious(list []retiredLogin) []retiredLogin {
	if len(list) > deezerPreviousCap {
		return list[:deezerPreviousCap]
	}
	return list
}

// listHolds reports whether list holds k's login.
func listHolds(list []retiredLogin, k keptLogin) bool {
	for _, r := range list {
		if r.sameToken(k) {
			return true
		}
	}
	return false
}

// replaceKeptLogin makes k the kept login. A different login kept before is
// backed up FIRST, so a process that dies between the writes leaves it in
// both files rather than in neither; when the backup cannot be written the
// kept login is left alone. When k itself was backed up, it leaves the list
// only after it is kept (the swap's order, UseOlderDeezerLogin). refusedOld
// marks the replaced one as refused by Deezer. backedUp reports whether a
// login was backed up. An empty k is never kept.
func replaceKeptLogin(keptPath string, k keptLogin, refusedOld bool) (backedUp bool, err error) {
	if !k.usable() {
		return false, ErrDeezerShareMalformed
	}
	prev, had := loadKeptLogin(keptPath)
	if had && prev.sameLogin(k) {
		return false, nil
	}
	if had && prev.sameToken(k) {
		// The same login under another display name: nothing to back up.
		return false, saveKeptLogin(keptPath, k)
	}
	list := loadPreviousLogins(keptPath)
	held := listHolds(list, k)
	if had {
		list = withRetired(list, prev, refusedOld, time.Now())
		step := list
		if !held {
			step = capPrevious(list)
		}
		if err := savePreviousLogins(keptPath, step); err != nil {
			return false, err
		}
	}
	if err := saveKeptLogin(keptPath, k); err != nil {
		return false, err
	}
	if held {
		// A failure here only leaves k listed twice; reads skip it.
		_ = savePreviousLogins(keptPath, capPrevious(withoutLogin(list, k)))
	}
	return had, nil
}

// refusedBefore reports whether k is backed up as a login Deezer refused.
func refusedBefore(keptPath string, k keptLogin) bool {
	for _, r := range loadPreviousLogins(keptPath) {
		if r.Refused && r.sameToken(k) {
			return true
		}
	}
	return false
}

// previousDeezer is the backup list without the login kept now (a swap or a
// replacement that stopped half way can leave it in both).
func (s *Server) previousDeezer() []retiredLogin {
	list := loadPreviousLogins(s.deezerLoginPath)
	if k, ok := s.keptDeezer(); ok {
		list = withoutLogin(list, k)
	}
	return list
}

// olderDeezerFor picks the backed-up login to put back for account: the
// newest one for the same account that Deezer has not refused.
func (s *Server) olderDeezerFor(account string) (retiredLogin, bool) {
	for _, r := range s.previousDeezer() {
		if !r.Refused && strings.EqualFold(strings.TrimSpace(r.Account), strings.TrimSpace(account)) {
			return r, true
		}
	}
	return retiredLogin{}, false
}

// DeezerKeptSummary is what the Deezer area's overview shows about the kept
// login of one speaker: never the login, never the account.
type DeezerKeptSummary struct {
	Kept bool `json:"kept"`
	// From is "sources-file", "registration", "peer" or "restored".
	From   string    `json:"from,omitempty"`
	KeptAt time.Time `json:"keptAt,omitempty"`
	// FromName is the name of the speaker a copied login came from.
	FromName string `json:"fromName,omitempty"`
	// Previous is how many older logins are backed up, PreviousRefused how
	// many of them Deezer refused.
	Previous        int `json:"previous"`
	PreviousRefused int `json:"previousRefused,omitempty"`
	// OlderUsable is set when a backed-up login for the kept account exists
	// that Deezer has not refused: "Use the older login" can put it back.
	OlderUsable bool `json:"olderUsable,omitempty"`
}

// KeptDeezerSummary reads the kept login's summary. Read on demand.
func (s *Server) KeptDeezerSummary() DeezerKeptSummary {
	var out DeezerKeptSummary
	k, ok := s.keptDeezer()
	if ok {
		out.Kept, out.From, out.KeptAt = true, k.From, k.KeptAt
		if k.From == "peer" {
			out.FromName = k.PeerName
		}
		_, out.OlderUsable = s.olderDeezerFor(k.Account)
	}
	for _, r := range s.previousDeezer() {
		out.Previous++
		if r.Refused {
			out.PreviousRefused++
		}
	}
	return out
}

// UseOlderDeezerLogin puts the newest backed-up login for the kept account
// that Deezer has not refused back in place, and backs the current one up
// marked as refused, so the two never swap back and forth. The caller makes
// sure Deezer refuses the current login, and hands the firmware the restored
// one afterwards. It returns the account, unmasked, for that recreate only.
//
// The writes run in an order that loses no login if the process dies between
// them: first the list with the current login added (the chosen one still in
// it), then the kept file, then the list without the chosen one, capped.
func (s *Server) UseOlderDeezerLogin() (string, error) {
	if s.deezerLoginPath == "" {
		return "", ErrDeezerShareOff
	}
	s.deezerShare.adoptMu.Lock()
	defer s.deezerShare.adoptMu.Unlock()
	cur, ok := s.keptDeezer()
	if !ok {
		return "", ErrDeezerShareNoKeptLogin
	}
	chosen, ok := s.olderDeezerFor(cur.Account)
	if !ok {
		return "", ErrDeezerNoOlderLogin
	}
	now := time.Now().UTC()
	step1 := withRetired(loadPreviousLogins(s.deezerLoginPath), cur, true, now)
	if err := savePreviousLogins(s.deezerLoginPath, step1); err != nil {
		s.logger.Warn("deezer: the current login could not be backed up, not swapping", slog.String("err", err.Error()))
		return "", ErrDeezerShareSaveFailed
	}
	if deezerSwapCrashAfter == 1 {
		return "", ErrDeezerShareSaveFailed
	}
	k := chosen.keptLogin
	k.From, k.KeptAt, k.Peer, k.PeerName = "restored", now, "", ""
	if err := saveKeptLogin(s.deezerLoginPath, k); err != nil {
		s.logger.Warn("deezer: the older login could not be put back", slog.String("err", err.Error()))
		return "", ErrDeezerShareSaveFailed
	}
	if deezerSwapCrashAfter == 2 {
		return "", ErrDeezerShareSaveFailed
	}
	if err := savePreviousLogins(s.deezerLoginPath, capPrevious(withoutLogin(step1, k))); err != nil {
		// The restored login is kept; it is only listed twice until the
		// next replacement rewrites the list.
		s.logger.Warn("deezer: the backup list could not be tidied after the swap", slog.String("err", err.Error()))
	}
	s.logger.Info("deezer: put an older backed-up Deezer login back, the refused one is backed up",
		slog.String("account", maskAccount(k.Account)), slog.String("secretType", k.SecretType),
		slog.Time("olderRetiredAt", chosen.RetiredAt))
	return k.Account, nil
}
