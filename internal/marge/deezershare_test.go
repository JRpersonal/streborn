package marge

import (
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
)

// deezerSharePair is a sending stand-in that keeps a login (from its own
// Sources.xml) and a receiving one that keeps none.
func deezerSharePair(t *testing.T) (sender, receiver *Server, recv deezerFixture, logs func() string) {
	t.Helper()
	send := newDeezerFixture(t, deezerSourcesXML)
	CaptureKeptDeezerLogin(send.glob(), send.keptPath, send.logger)
	sender = send.server(true)
	recv = newDeezerFixture(t, "")
	receiver = recv.server(true)
	return sender, receiver, recv, func() string { return send.logs.String() + recv.logs.String() }
}

func TestDeezerShareSealsOnlyForTheKeptAccount(t *testing.T) {
	sender, receiver, _, _ := deezerSharePair(t)
	key := receiver.DeezerShareKey()
	if key == "" || key != receiver.DeezerShareKey() {
		t.Fatalf("share key %q is not stable within a run", key)
	}
	if _, err := sender.SealKeptDeezerLogin(key, DeezerShareAccountHash(key, "2222222222")); !errors.Is(err, ErrDeezerShareHashMismatch) {
		t.Fatalf("another account: %v", err)
	}
	if _, err := sender.SealKeptDeezerLogin(key, ""); !errors.Is(err, ErrDeezerShareHashMismatch) {
		t.Fatalf("no hash: %v", err)
	}
	// The hash is salted with the key: the same account under another key
	// does not match.
	other := newDeezerFixture(t, "").server(true).DeezerShareKey()
	if _, err := sender.SealKeptDeezerLogin(key, DeezerShareAccountHash(other, testDeezerAccount)); !errors.Is(err, ErrDeezerShareHashMismatch) {
		t.Fatalf("hash under another key: %v", err)
	}
	// Case and spaces of the account do not matter.
	sealed, err := sender.SealKeptDeezerLogin(key, DeezerShareAccountHash(key, " "+strings.ToUpper(testDeezerAccount)+" "))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(sealed)
	if strings.Contains(string(b), testDeezerSecret) || strings.Contains(string(b), testDeezerAccount) {
		t.Fatalf("the sealed login carries the login in clear: %s", b)
	}
	if _, err := receiver.SealKeptDeezerLogin(key, DeezerShareAccountHash(key, testDeezerAccount)); !errors.Is(err, ErrDeezerShareNoKeptLogin) {
		t.Fatalf("a speaker without a kept login sealed: %v", err)
	}
}

func TestDeezerShareAdoptKeepsTheLoginOnce(t *testing.T) {
	sender, receiver, recv, logs := deezerSharePair(t)
	key := receiver.DeezerShareKey()
	sealed, err := sender.SealKeptDeezerLogin(key, DeezerShareAccountHash(key, testDeezerAccount))
	if err != nil {
		t.Fatal(err)
	}
	// Not for an account the receiver does not list.
	peerA := DeezerAdoptOptions{Peer: "peer#a", PeerName: "Kitchen"}
	if _, _, err := receiver.AdoptPeerDeezerLogin(sealed, []string{"2222222222"}, peerA); !errors.Is(err, ErrDeezerShareAccount) {
		t.Fatalf("account mismatch: %v", err)
	}
	if _, err := os.Stat(recv.keptPath); !os.IsNotExist(err) {
		t.Fatal("a refused login was written")
	}
	// Not when sealed to another key (another run of the receiver).
	stranger := newDeezerFixture(t, "").server(true)
	if _, _, err := stranger.AdoptPeerDeezerLogin(sealed, []string{testDeezerAccount}, peerA); !errors.Is(err, ErrDeezerShareUndecryptable) {
		t.Fatalf("another key opened it: %v", err)
	}
	tampered := sealed
	tampered.Box = sealed.Box[:len(sealed.Box)-4] + "AAAA"
	if _, _, err := receiver.AdoptPeerDeezerLogin(tampered, []string{testDeezerAccount}, peerA); err == nil {
		t.Fatal("a tampered box was taken")
	}

	acct, changed, err := receiver.AdoptPeerDeezerLogin(sealed, []string{" " + testDeezerAccount}, peerA)
	if err != nil || acct != testDeezerAccount || !changed {
		t.Fatalf("adopt: %q %v %v", acct, changed, err)
	}
	k, ok := loadKeptLogin(recv.keptPath)
	if !ok || k.Secret != testDeezerSecret || k.Account != testDeezerAccount || k.From != "peer" || k.Peer != "peer#a" ||
		k.PeerName != "Kitchen" || k.SecretType != "token" {
		t.Fatalf("kept = %+v", k)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(recv.keptPath)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("kept file mode: %v %v", fi, err)
		}
	}
	// Without the owner's copy a kept login is never replaced by a peer's.
	before, _ := os.ReadFile(recv.keptPath)
	sealed2, _ := sender.SealKeptDeezerLogin(key, DeezerShareAccountHash(key, testDeezerAccount))
	if _, _, err := receiver.AdoptPeerDeezerLogin(sealed2, []string{testDeezerAccount}, DeezerAdoptOptions{Peer: "peer#b"}); !errors.Is(err, ErrDeezerShareAlreadyKept) {
		t.Fatalf("second adopt: %v", err)
	}
	if after, _ := os.ReadFile(recv.keptPath); string(after) != string(before) {
		t.Fatal("the kept login was overwritten")
	}

	receiver.NoteDeezerShare(false, DeezerShareEvent{Peer: "peer#a", Outcome: "accepted"})
	sender.NoteDeezerShare(true, DeezerShareEvent{Peer: "peer#r", Trigger: "copy", Outcome: "accepted"})
	st := receiver.DeezerLoginSnapshot()
	if !st.Kept || st.From != "peer" || st.FromPeer != "peer#a" || st.Shares == nil || len(st.Shares.Received) != 1 || st.Shares.Received[0].Outcome != "accepted" {
		t.Fatalf("snapshot = %+v", st)
	}
	if ss := sender.DeezerLoginSnapshot(); ss.Shares == nil || len(ss.Shares.Sent) != 1 || ss.Shares.Sent[0].Trigger != "copy" {
		t.Fatalf("sender snapshot = %+v", ss)
	}
	for _, v := range []any{st, sender.DeezerLoginSnapshot()} {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), testDeezerSecret) || strings.Contains(string(b), testDeezerAccount) {
			t.Fatalf("the snapshot leaks: %s", b)
		}
	}
	if out := logs(); strings.Contains(out, testDeezerSecret) || strings.Contains(out, testDeezerAccount) {
		t.Fatalf("the log leaks: %s", out)
	}
}

func TestDeezerShareLogIsCapped(t *testing.T) {
	s := newDeezerFixture(t, "").server(true)
	if s.DeezerLoginSnapshot().Shares != nil {
		t.Fatal("a share record before any attempt")
	}
	for i := 0; i < deezerShareLogCap+5; i++ {
		s.NoteDeezerShare(true, DeezerShareEvent{Peer: "peer#" + string(rune('a'+i)), Outcome: "unreachable"})
	}
	sh := s.DeezerLoginSnapshot().Shares
	if len(sh.Sent) != deezerShareLogCap || sh.Sent[len(sh.Sent)-1].Peer != "peer#"+string(rune('a'+deezerShareLogCap+4)) {
		t.Fatalf("sent = %+v", sh.Sent)
	}
}

func TestDeezerShareOffWithoutAKeptFile(t *testing.T) {
	sender, _, _, _ := deezerSharePair(t)
	off := newDeezerFixture(t, "").server(false)
	key := off.DeezerShareKey()
	sealed, err := sender.SealKeptDeezerLogin(key, DeezerShareAccountHash(key, testDeezerAccount))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := off.AdoptPeerDeezerLogin(sealed, []string{testDeezerAccount}, DeezerAdoptOptions{Peer: "peer#a", Replace: true}); !errors.Is(err, ErrDeezerShareOff) {
		t.Fatalf("feature off: %v", err)
	}
}
