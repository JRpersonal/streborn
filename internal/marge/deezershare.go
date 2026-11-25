// Deezer: share a kept login with another speaker of the same home
// (docs/streaming/deezer.md, "Sharing a kept login within a home").
//
// A speaker whose Deezer login is gone and that has no kept copy can never get
// one again: Deezer issues logins only to Bose's app. Another speaker of the
// same home may still keep a login for the same account. This file is the
// stand-in's half of copying it over; the agent-to-agent calls live in
// internal/webui/deezershare.go.
//
// The login never travels in clear. The receiving agent makes an X25519 key
// pair once per run and keeps the private half in RAM; its need signal
// carries the public half and a hash of the account it needs, salted with that
// key, so neither side names the account to the LAN. The sender seals its kept
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

// DeezerShareEvent is one share attempt, never the login or the account.
type DeezerShareEvent struct {
	At time.Time `json:"at"`
	// Peer is the other speaker's pseudonym, never its address.
	Peer string `json:"peer"`
	// Trigger is why a sender looked at the peer: "start", "peer-seen",
	// "check-again" or "offer-request" (the peer asked); "" on the receiving
	// side.
	Trigger string `json:"trigger,omitempty"`
	// Outcome is "accepted", "refused:<code>", or on the sending side also
	// "not-needed:<why>", "hash-mismatch", "unreachable" or "old-agent". An
	// offer request is "offering" when the peer said it will offer.
	Outcome string `json:"outcome"`
}

// DeezerShareLog is the deezer_login section's record of share attempts,
// oldest first.
type DeezerShareLog struct {
	Sent     []DeezerShareEvent `json:"sent,omitempty"`
	Received []DeezerShareEvent `json:"received,omitempty"`
	// RequestsSent are the offer requests a speaker that needs a login sent
	// its peers (Check again); RequestsReceived the ones this speaker got.
	RequestsSent     []DeezerShareEvent `json:"requestsSent,omitempty"`
	RequestsReceived []DeezerShareEvent `json:"requestsReceived,omitempty"`
}

// deezerShareState is the Server's share state.
type deezerShareState struct {
	mu       sync.Mutex
	key      *ecdh.PrivateKey
	sent     []DeezerShareEvent
	received []DeezerShareEvent
	reqSent  []DeezerShareEvent
	reqRecv  []DeezerShareEvent
	// adoptMu keeps one adoption at a time, so two peers that offer at the
	// same moment cannot both pass the "nothing kept yet" check.
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

// DeezerShareKey is this agent's share public key (base64) for its need
// signal, "" when none could be made.
func (s *Server) DeezerShareKey() string {
	k, err := s.shareKey()
	if err != nil {
		s.logger.Warn("deezer share: no share key", slog.String("err", err.Error()))
		return ""
	}
	return b64.EncodeToString(k.PublicKey().Bytes())
}

// DeezerShareAccountHash is the account as a need signal names it: SHA-256
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

// AdoptPeerDeezerLogin keeps a login another speaker of the home sealed to
// this agent. listed are the Deezer accounts this speaker lists without a
// working login; the shared login is kept only for one of them, and never
// when a login is kept already (a kept login is never replaced by a peer's).
// peer is the sender's pseudonym. It returns the account it was kept for,
// unmasked, for the agent's own recreate; never for display.
func (s *Server) AdoptPeerDeezerLogin(sealed SealedDeezerLogin, listed []string, peer string) (string, error) {
	if s.deezerLoginPath == "" {
		return "", ErrDeezerShareOff
	}
	s.deezerShare.adoptMu.Lock()
	defer s.deezerShare.adoptMu.Unlock()
	if _, have := s.keptDeezer(); have {
		return "", ErrDeezerShareAlreadyKept
	}
	l, err := s.openSharedDeezerLogin(sealed)
	if err != nil {
		return "", err
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
		return "", ErrDeezerShareAccount
	}
	k := keptLogin{Account: account, DisplayName: l.DisplayName, SecretType: l.SecretType,
		Secret: l.Secret, From: "peer", KeptAt: time.Now().UTC(), Peer: peer}
	if err := saveKeptLogin(s.deezerLoginPath, k); err != nil {
		s.logger.Warn("deezer share: the shared login could not be kept", slog.String("err", err.Error()))
		return "", ErrDeezerShareSaveFailed
	}
	s.logger.Info("deezer share: kept the Deezer login another speaker of the home shared, the account document hands it back",
		slog.String("account", maskAccount(account)), slog.String("secretType", k.SecretType),
		slog.String("peer", peer))
	return account, nil
}

// NoteDeezerShare records one share attempt, sent or received.
func (s *Server) NoteDeezerShare(sent bool, ev DeezerShareEvent) {
	list := &s.deezerShare.received
	if sent {
		list = &s.deezerShare.sent
	}
	s.noteDeezerShareIn(list, ev)
}

// NoteDeezerShareRequest records one offer request, sent or received.
func (s *Server) NoteDeezerShareRequest(sent bool, ev DeezerShareEvent) {
	list := &s.deezerShare.reqRecv
	if sent {
		list = &s.deezerShare.reqSent
	}
	s.noteDeezerShareIn(list, ev)
}

// noteDeezerShareIn appends ev to list (one of deezerShare's), capped.
func (s *Server) noteDeezerShareIn(list *[]DeezerShareEvent, ev DeezerShareEvent) {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	s.deezerShare.mu.Lock()
	defer s.deezerShare.mu.Unlock()
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
	if len(sh.sent)+len(sh.received)+len(sh.reqSent)+len(sh.reqRecv) == 0 {
		return nil
	}
	return &DeezerShareLog{
		Sent:             append([]DeezerShareEvent(nil), sh.sent...),
		Received:         append([]DeezerShareEvent(nil), sh.received...),
		RequestsSent:     append([]DeezerShareEvent(nil), sh.reqSent...),
		RequestsReceived: append([]DeezerShareEvent(nil), sh.reqRecv...),
	}
}
