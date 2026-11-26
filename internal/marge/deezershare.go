// Deezer: copy a kept login to another speaker of the same home
// (docs/streaming/deezer.md, "Copying a kept login within a home").
//
// A speaker whose Deezer login is gone and that has no kept copy can never get
// one again: Deezer issues logins only to Bose's app. Another speaker of the
// same home may still keep a login for the same account. This file is the
// stand-in's half of copying it over; the agent-to-agent calls live in
// internal/webui/deezershare.go. A copy happens only when the owner presses
// Copy in the Deezer area, never on its own.
//
// The login never travels in clear. The receiving agent makes an X25519 key
// pair once per run and keeps the private half in RAM; its login summary
// carries the public half and a hash of its account, salted with that key, so
// neither side names the account to the LAN. The sender seals its kept
// login to that key (ephemeral X25519, HKDF-SHA256, AES-256-GCM), and only
// the receiving agent can open it. Nothing here logs, records or returns the
// login or the account in clear.
//
// Untested on hardware: whether a token one speaker holds plays on another
// (most likely it is bound to the Deezer account, not to the device).

package marge

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
)

// deezerShareInfo binds every key and hash to this purpose and version.
const deezerShareInfo = "str-deezer-share-v1"

// deezerShareLogCap bounds the share attempts kept per direction.
const deezerShareLogCap = 8

// Why a share is refused. Each error's text is the reason code the agents
// exchange and log; none of them carries the login or the account.
var (
	ErrDeezerShareNoKeptLogin   = errors.New("no-kept-login")
	ErrDeezerShareHashMismatch  = errors.New("hash-mismatch")
	ErrDeezerShareAlreadyKept   = errors.New("already-kept")
	ErrDeezerShareUndecryptable = errors.New("undecryptable")
	ErrDeezerShareMalformed     = errors.New("malformed")
	ErrDeezerShareAccount       = errors.New("account-mismatch")
	ErrDeezerShareSaveFailed    = errors.New("save-failed")
	ErrDeezerShareOff           = errors.New("not-configured")
	// ErrDeezerShareRefusedBefore: the copy is a login this speaker backed
	// up as refused by Deezer (deezerprevious.go).
	ErrDeezerShareRefusedBefore = errors.New("refused-before")
)

// SealedDeezerLogin is a kept login sealed to one receiving agent's share key.
type SealedDeezerLogin struct {
	// Key is the sender's one-time X25519 public key, base64.
	Key string `json:"key"`
	// Nonce and Box are the AES-256-GCM nonce and ciphertext, base64.
	Nonce string `json:"nonce"`
	Box   string `json:"box"`
}

// sharedDeezerLogin is what a sealed login holds.
type sharedDeezerLogin struct {
	Account     string `json:"account"`
	DisplayName string `json:"displayName,omitempty"`
	SecretType  string `json:"secretType,omitempty"`
	Secret      string `json:"secret"`
}

// DeezerShareEvent is one copy attempt, never the login or the account.
type DeezerShareEvent struct {
	At time.Time `json:"at"`
	// Peer is the other speaker's pseudonym, never its address.
	Peer string `json:"peer"`
	// Trigger is the owner's press that started a copy: "copy" (one
	// speaker) or "copy-all"; on the receiving side "user" for such a copy.
	Trigger string `json:"trigger,omitempty"`
	// Outcome is "accepted", "refused:<code>", or on the sending side also
	// "not-needed:<why>", "hash-mismatch", "unreachable" or "old-agent".
	Outcome string `json:"outcome"`
}

// DeezerShareLog is the deezer_login section's record of copies, oldest
// first.
type DeezerShareLog struct {
	Sent     []DeezerShareEvent `json:"sent,omitempty"`
	Received []DeezerShareEvent `json:"received,omitempty"`
}

// deezerShareState is the Server's share state.
type deezerShareState struct {
	mu       sync.Mutex
	key      *ecdh.PrivateKey
	sent     []DeezerShareEvent
	received []DeezerShareEvent
	// adoptMu keeps one write of the kept login at a time (a copy taken, an
	// older login put back), so two at the same moment cannot both pass the
	// checks before them.
	adoptMu sync.Mutex
}

var b64 = base64.RawURLEncoding

// shareKey returns this run's share key, made on first use.
func (s *Server) shareKey() (*ecdh.PrivateKey, error) {
	s.deezerShare.mu.Lock()
	defer s.deezerShare.mu.Unlock()
	if s.deezerShare.key == nil {
		k, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		s.deezerShare.key = k
	}
	return s.deezerShare.key, nil
}

// DeezerShareKey is this agent's share public key (base64) for its login
// summary, "" when none could be made.
func (s *Server) DeezerShareKey() string {
	k, err := s.shareKey()
	if err != nil {
		s.logger.Warn("deezer share: no share key", slog.String("err", err.Error()))
		return ""
	}
	return b64.EncodeToString(k.PublicKey().Bytes())
}

// DeezerShareAccountHash is the account as a login summary names it: SHA-256
// over the purpose, the receiver's share key and the lowercased account, hex.
// Salted with the key, so one hash says nothing about another run's.
func DeezerShareAccountHash(key, account string) string {
	h := sha256.New()
	h.Write([]byte(deezerShareInfo))
	h.Write([]byte{0})
	h.Write([]byte(key))
	h.Write([]byte{0})
	h.Write([]byte(strings.ToLower(strings.TrimSpace(account))))
	return hex.EncodeToString(h.Sum(nil))
}

// deezerShareAEAD derives the box key from one X25519 exchange.
func deezerShareAEAD(priv *ecdh.PrivateKey, peer *ecdh.PublicKey, senderPub, receiverPub []byte) (cipher.AEAD, error) {
	shared, err := priv.ECDH(peer)
	if err != nil {
		return nil, err
	}
	salt := append(append([]byte{}, senderPub...), receiverPub...)
	key, err := hkdf.Key(sha256.New, shared, salt, deezerShareInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// SealKeptDeezerLogin seals the kept login to a peer's share key, but only
// when the peer's account hash is the kept account's: a peer that needs a
// login for another account gets nothing.
func (s *Server) SealKeptDeezerLogin(peerKey, peerHash string) (SealedDeezerLogin, error) {
	k, ok := s.keptDeezer()
	if !ok {
		return SealedDeezerLogin{}, ErrDeezerShareNoKeptLogin
	}
	if peerHash == "" || DeezerShareAccountHash(peerKey, k.Account) != strings.ToLower(strings.TrimSpace(peerHash)) {
		return SealedDeezerLogin{}, ErrDeezerShareHashMismatch
	}
	rawPeer, err := b64.DecodeString(peerKey)
	if err != nil {
		return SealedDeezerLogin{}, ErrDeezerShareMalformed
	}
	pub, err := ecdh.X25519().NewPublicKey(rawPeer)
	if err != nil {
		return SealedDeezerLogin{}, ErrDeezerShareMalformed
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return SealedDeezerLogin{}, err
	}
	aead, err := deezerShareAEAD(eph, pub, eph.PublicKey().Bytes(), rawPeer)
	if err != nil {
		return SealedDeezerLogin{}, ErrDeezerShareMalformed
	}
	plain, err := json.Marshal(sharedDeezerLogin{Account: k.Account, DisplayName: k.DisplayName, SecretType: k.SecretType, Secret: k.Secret})
	if err != nil {
		return SealedDeezerLogin{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return SealedDeezerLogin{}, err
	}
	box := aead.Seal(nil, nonce, plain, []byte(deezerShareInfo))
	return SealedDeezerLogin{
		Key:   b64.EncodeToString(eph.PublicKey().Bytes()),
		Nonce: b64.EncodeToString(nonce),
		Box:   b64.EncodeToString(box),
	}, nil
}

// openSharedDeezerLogin opens a sealed login with this run's share key.
func (s *Server) openSharedDeezerLogin(sealed SealedDeezerLogin) (sharedDeezerLogin, error) {
	if len(sealed.Box) > 8192 || len(sealed.Key) > 64 || len(sealed.Nonce) > 64 {
		return sharedDeezerLogin{}, ErrDeezerShareMalformed
	}
	priv, err := s.shareKey()
	if err != nil {
		return sharedDeezerLogin{}, ErrDeezerShareUndecryptable
	}
	rawSender, err1 := b64.DecodeString(sealed.Key)
	nonce, err2 := b64.DecodeString(sealed.Nonce)
	box, err3 := b64.DecodeString(sealed.Box)
	if err1 != nil || err2 != nil || err3 != nil {
		return sharedDeezerLogin{}, ErrDeezerShareMalformed
	}
	pub, err := ecdh.X25519().NewPublicKey(rawSender)
	if err != nil {
		return sharedDeezerLogin{}, ErrDeezerShareMalformed
	}
	aead, err := deezerShareAEAD(priv, pub, rawSender, priv.PublicKey().Bytes())
	if err != nil || len(nonce) != aead.NonceSize() {
		return sharedDeezerLogin{}, ErrDeezerShareUndecryptable
	}
	plain, err := aead.Open(nil, nonce, box, []byte(deezerShareInfo))
	if err != nil {
		// A key of an earlier run of this agent, or not sealed to it at all.
		return sharedDeezerLogin{}, ErrDeezerShareUndecryptable
	}
	var l sharedDeezerLogin
	if json.Unmarshal(plain, &l) != nil {
		return sharedDeezerLogin{}, ErrDeezerShareMalformed
	}
	l.Account = strings.TrimSpace(l.Account)
	if l.Account == "" || l.Secret == "" || len(l.Account) > 254 || len(l.Secret) > 4096 ||
		len(l.SecretType) > 64 || len(l.DisplayName) > 128 ||
		hasControl(l.Account) || hasControl(l.SecretType) || hasControl(l.DisplayName) {
		return sharedDeezerLogin{}, ErrDeezerShareMalformed
	}
	return l, nil
}

func hasControl(v string) bool {
	return strings.IndexFunc(v, unicode.IsControl) >= 0
}

// DeezerAdoptOptions says how a copied login may be kept.
type DeezerAdoptOptions struct {
	// Peer is the sender's pseudonym, PeerName its name in the roster.
	Peer, PeerName string
	// Replace lets the copy take the place of a login kept already: only for
	// a copy the owner pressed for, and only while this speaker's Deezer
	// does not work. The replaced login is backed up, never dropped.
	Replace bool
	// CurrentRefused marks the replaced login as refused by Deezer.
	CurrentRefused bool
}

// AdoptPeerDeezerLogin keeps a login another speaker of the home sealed to
// this agent. listed are the Deezer accounts this speaker lists without a
// working login; the copied login is kept only for one of them. A login kept
// already stays unless o.Replace allows otherwise, and then it is backed up
// first (deezerprevious.go). A login this speaker backed up as refused is
// never taken again. It returns the account it was kept for, unmasked, for
// the agent's own recreate (never for display), and whether the kept login
// changed.
func (s *Server) AdoptPeerDeezerLogin(sealed SealedDeezerLogin, listed []string, o DeezerAdoptOptions) (string, bool, error) {
	if s.deezerLoginPath == "" {
		return "", false, ErrDeezerShareOff
	}
	s.deezerShare.adoptMu.Lock()
	defer s.deezerShare.adoptMu.Unlock()
	if _, have := s.keptDeezer(); have && !o.Replace {
		return "", false, ErrDeezerShareAlreadyKept
	}
	l, err := s.openSharedDeezerLogin(sealed)
	if err != nil {
		return "", false, err
	}
	account := ""
	for _, a := range listed {
		if a = strings.TrimSpace(a); a != "" && strings.EqualFold(a, l.Account) {
			// The spelling this speaker's firmware lists: /full names it.
			account = a
			break
		}
	}
	if account == "" {
		return "", false, ErrDeezerShareAccount
	}
	k := keptLogin{Account: account, DisplayName: l.DisplayName, SecretType: l.SecretType,
		Secret: l.Secret, From: "peer", KeptAt: time.Now().UTC(), Peer: o.Peer, PeerName: o.PeerName}
	if cur, have := s.keptDeezer(); have && cur.sameToken(k) {
		s.logger.Info("deezer share: the copied Deezer login is the one kept already",
			slog.String("account", maskAccount(account)), slog.String("peer", o.Peer))
		return account, false, nil
	}
	if refusedBefore(s.deezerLoginPath, k) {
		return "", false, ErrDeezerShareRefusedBefore
	}
	backedUp, err := replaceKeptLogin(s.deezerLoginPath, k, o.CurrentRefused)
	if err != nil {
		s.logger.Warn("deezer share: the copied login could not be kept", slog.String("err", err.Error()))
		return "", false, ErrDeezerShareSaveFailed
	}
	s.logger.Info("deezer share: kept the Deezer login another speaker of the home copied over, the account document hands it back",
		slog.String("account", maskAccount(account)), slog.String("secretType", k.SecretType),
		slog.String("peer", o.Peer), slog.Bool("previousBackedUp", backedUp))
	return account, true, nil
}

// NoteDeezerShare records one copy attempt, sent or received.
func (s *Server) NoteDeezerShare(sent bool, ev DeezerShareEvent) {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	s.deezerShare.mu.Lock()
	defer s.deezerShare.mu.Unlock()
	list := &s.deezerShare.received
	if sent {
		list = &s.deezerShare.sent
	}
	*list = append(*list, ev)
	if n := len(*list); n > deezerShareLogCap {
		*list = append([]DeezerShareEvent(nil), (*list)[n-deezerShareLogCap:]...)
	}
}

// deezerShareLog copies the share record for the debug section; nil before
// the first attempt.
func (s *Server) deezerShareLog() *DeezerShareLog {
	s.deezerShare.mu.Lock()
	defer s.deezerShare.mu.Unlock()
	sh := &s.deezerShare
	if len(sh.sent)+len(sh.received) == 0 {
		return nil
	}
	return &DeezerShareLog{
		Sent:     append([]DeezerShareEvent(nil), sh.sent...),
		Received: append([]DeezerShareEvent(nil), sh.received...),
	}
}
