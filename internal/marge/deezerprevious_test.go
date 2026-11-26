package marge

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Each test login carries this marker, so the leak checks find every one.
const testLoginMarker = "deezer-login-secret-"

func testLogin(n string) keptLogin {
	return keptLogin{Account: testDeezerAccount, SecretType: "token", Secret: testLoginMarker + n, From: "registration", KeptAt: time.Now().UTC()}
}

// registerDeezer is the firmware's own registration of a Deezer login.
func registerDeezer(t *testing.T, s *Server, secret string) {
	t.Helper()
	body := `<source><credential type="token">` + secret + `</credential><name>Deezer</name>` +
		`<username>` + testDeezerAccount + `</username><sourceproviderid>14</sourceproviderid><sourcename>DEEZER</sourcename></source>`
	if w := serve(s, http.MethodPost, "/streaming/account/stick@local/source", body, "127.0.0.1:4000"); w.Code != http.StatusCreated {
		t.Fatalf("registration: %d", w.Code)
	}
}

func secretsOf(list []retiredLogin) []string {
	var out []string
	for _, r := range list {
		out = append(out, strings.TrimPrefix(r.Secret, testLoginMarker))
	}
	return out
}

func noLoginLeak(t *testing.T, what string, outs ...string) {
	t.Helper()
	for _, out := range outs {
		if strings.Contains(out, testLoginMarker) || strings.Contains(out, testDeezerSecret) || strings.Contains(out, testDeezerAccount) {
			t.Fatalf("%s leaks a login or the account: %s", what, out)
		}
	}
}

// Every replacement backs the replaced login up first: the capture, the
// firmware's registration, newest first, at most three, the same login once.
func TestDeezerPreviousLoginsRotate(t *testing.T) {
	f := newDeezerFixture(t, deezerSourcesXML)
	CaptureKeptDeezerLogin(f.glob(), f.keptPath, f.logger)
	s := f.server(true)
	if s.KeptDeezerSummary().Previous != 0 {
		t.Fatal("a backup before any replacement")
	}
	if _, err := os.Stat(previousLoginPath(f.keptPath)); !os.IsNotExist(err) {
		t.Fatal("a backup file without a replacement")
	}
	registerDeezer(t, s, testLoginMarker+"b")
	got := loadPreviousLogins(f.keptPath)
	if len(got) != 1 || got[0].Secret != testDeezerSecret || got[0].From != "sources-file" || got[0].RetiredAt.IsZero() || got[0].Refused {
		t.Fatalf("after one replacement: %+v", got)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(previousLoginPath(f.keptPath))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("backup file mode: %v %v", fi, err)
		}
	}
	// The same login again changes nothing.
	registerDeezer(t, s, testLoginMarker+"b")
	if n := len(loadPreviousLogins(f.keptPath)); n != 1 {
		t.Fatalf("an unchanged login was backed up: %d", n)
	}
	for _, n := range []string{"c", "d", "e"} {
		registerDeezer(t, s, testLoginMarker+n)
	}
	if got := secretsOf(loadPreviousLogins(f.keptPath)); strings.Join(got, ",") != "d,c,b" {
		t.Fatalf("capped list = %v", got)
	}
	// A login that comes back is listed once, at the head, and leaves the
	// list while it is the kept one.
	registerDeezer(t, s, testLoginMarker+"c")
	if got := secretsOf(loadPreviousLogins(f.keptPath)); strings.Join(got, ",") != "e,d,b" {
		t.Fatalf("after c came back = %v", got)
	}
	registerDeezer(t, s, testLoginMarker+"e")
	if got := secretsOf(loadPreviousLogins(f.keptPath)); strings.Join(got, ",") != "c,d,b" {
		t.Fatalf("after e came back = %v", got)
	}

	// A capture of a different login from Sources.xml backs up too.
	registerDeezer(t, s, testLoginMarker+"f")
	CaptureKeptDeezerLogin(f.glob(), f.keptPath, f.logger)
	if k, _ := loadKeptLogin(f.keptPath); k.Secret != testDeezerSecret || k.From != "sources-file" {
		t.Fatalf("capture = %+v", k)
	}
	if got := secretsOf(loadPreviousLogins(f.keptPath)); got[0] != "f" || len(got) != 3 {
		t.Fatalf("after the capture = %v", got)
	}

	st := s.DeezerLoginSnapshot()
	if st.PreviousCount != 3 || st.PreviousRefused != 0 {
		t.Fatalf("snapshot = %+v", st)
	}
	snap, _ := json.Marshal(st)
	sum, _ := json.Marshal(s.KeptDeezerSummary())
	noLoginLeak(t, "snapshot, summary and log", string(snap), string(sum), f.logs.String())
}

// An empty login never gets in: not as the kept one, not into the backups.
func TestDeezerPreviousNeverHoldsAnEmptyLogin(t *testing.T) {
	f := newDeezerFixture(t, "")
	if _, err := replaceKeptLogin(f.keptPath, keptLogin{Account: testDeezerAccount}, false); err == nil {
		t.Fatal("an empty login was kept")
	}
	if err := saveKeptLogin(f.keptPath, testLogin("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := replaceKeptLogin(f.keptPath, keptLogin{Account: testDeezerAccount, Secret: ""}, false); err == nil {
		t.Fatal("an empty login replaced a kept one")
	}
	if k, _ := loadKeptLogin(f.keptPath); k.Secret != testLoginMarker+"a" {
		t.Fatalf("kept = %+v", k)
	}
	if _, err := os.Stat(previousLoginPath(f.keptPath)); !os.IsNotExist(err) {
		t.Fatal("an empty login was backed up")
	}
	// A hand-made file with an empty entry: it is skipped.
	b, _ := json.Marshal([]retiredLogin{{keptLogin: keptLogin{Account: testDeezerAccount}}, {keptLogin: testLogin("z")}})
	if err := os.WriteFile(previousLoginPath(f.keptPath), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := secretsOf(loadPreviousLogins(f.keptPath)); len(got) != 1 || got[0] != "z" {
		t.Fatalf("loaded = %v", got)
	}
}

// refusedSetup keeps login "new" (Deezer refuses it) with "old" backed up.
func refusedSetup(t *testing.T) (deezerFixture, *Server) {
	t.Helper()
	f := newDeezerFixture(t, "")
	s := f.server(true)
	if err := saveKeptLogin(f.keptPath, testLogin("old")); err != nil {
		t.Fatal(err)
	}
	if _, err := replaceKeptLogin(f.keptPath, testLogin("new"), false); err != nil {
		t.Fatal(err)
	}
	return f, s
}

// Use the older login: the older one is kept again, the refused one is backed
// up marked refused, and it is never offered back, so the two never swap
// back and forth; a capture or a copy of the refused one does not undo it.
func TestDeezerUseOlderLoginSwapsOnce(t *testing.T) {
	f, s := refusedSetup(t)
	if sum := s.KeptDeezerSummary(); !sum.OlderUsable || sum.Previous != 1 {
		t.Fatalf("before = %+v", sum)
	}
	acct, err := s.UseOlderDeezerLogin()
	if err != nil || acct != testDeezerAccount {
		t.Fatalf("swap: %q %v", acct, err)
	}
	k, _ := loadKeptLogin(f.keptPath)
	if k.Secret != testLoginMarker+"old" || k.From != "restored" {
		t.Fatalf("kept = %+v", k)
	}
	got := loadPreviousLogins(f.keptPath)
	if len(got) != 1 || got[0].Secret != testLoginMarker+"new" || !got[0].Refused {
		t.Fatalf("backups = %+v", got)
	}
	sum := s.KeptDeezerSummary()
	if sum.OlderUsable || sum.Previous != 1 || sum.PreviousRefused != 1 || sum.From != "restored" {
		t.Fatalf("after = %+v", sum)
	}
	// No ping-pong: the refused one is never put back.
	if _, err := s.UseOlderDeezerLogin(); !errors.Is(err, ErrDeezerNoOlderLogin) {
		t.Fatalf("second swap: %v", err)
	}
	// The firmware's file may still hold the refused one: a capture keeps
	// the restored login.
	xmlDoc := strings.Replace(deezerSourcesXML, testDeezerSecret, testLoginMarker+"new", 1)
	if err := os.MkdirAll(filepath.Dir(f.sourcesPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.sourcesPath, []byte(xmlDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	CaptureKeptDeezerLogin(f.glob(), f.keptPath, f.logger)
	if k, _ := loadKeptLogin(f.keptPath); k.Secret != testLoginMarker+"old" {
		t.Fatalf("a capture undid the swap: %+v", k)
	}
	// A swap needs a kept login and the feature.
	if _, err := newDeezerFixture(t, "").server(true).UseOlderDeezerLogin(); !errors.Is(err, ErrDeezerShareNoKeptLogin) {
		t.Fatalf("nothing kept: %v", err)
	}
	if _, err := newDeezerFixture(t, "").server(false).UseOlderDeezerLogin(); !errors.Is(err, ErrDeezerShareOff) {
		t.Fatalf("off: %v", err)
	}
	snap, _ := json.Marshal(s.DeezerLoginSnapshot())
	noLoginLeak(t, "the swap", string(snap), f.logs.String())
}

// Only a login for the kept account, not refused, is put back.
func TestDeezerUseOlderLoginSkipsOtherAccountsAndRefused(t *testing.T) {
	f := newDeezerFixture(t, "")
	s := f.server(true)
	other := testLogin("other")
	other.Account = "2222222222"
	refused := testLogin("refused")
	b, _ := json.Marshal([]retiredLogin{{keptLogin: other}, {keptLogin: refused, Refused: true}})
	if err := os.MkdirAll(filepath.Dir(f.keptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previousLoginPath(f.keptPath), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveKeptLogin(f.keptPath, testLogin("cur")); err != nil {
		t.Fatal(err)
	}
	if s.KeptDeezerSummary().OlderUsable {
		t.Fatal("another account's or a refused login is offered")
	}
	if _, err := s.UseOlderDeezerLogin(); !errors.Is(err, ErrDeezerNoOlderLogin) {
		t.Fatalf("swap: %v", err)
	}
	if k, _ := loadKeptLogin(f.keptPath); k.Secret != testLoginMarker+"cur" {
		t.Fatalf("kept = %+v", k)
	}
}

// A process that dies between the swap's writes loses no login: after the
// first write and after the second, every login is still in one of the two
// files, and a swap run again finishes the job.
func TestDeezerUseOlderLoginIsCrashSafe(t *testing.T) {
	all := func(f deezerFixture) map[string]bool {
		out := map[string]bool{}
		if k, ok := loadKeptLogin(f.keptPath); ok {
			out[k.Secret] = true
		}
		for _, r := range loadPreviousLogins(f.keptPath) {
			out[r.Secret] = true
		}
		return out
	}
	for _, stop := range []int{1, 2} {
		f, s := refusedSetup(t)
		// A third, older login for the same account, so the cap is in play.
		list := loadPreviousLogins(f.keptPath)
		list = append(list, retiredLogin{keptLogin: testLogin("oldest"), RetiredAt: time.Now()})
		if err := savePreviousLogins(f.keptPath, list); err != nil {
			t.Fatal(err)
		}
		deezerSwapCrashAfter = stop
		_, err := s.UseOlderDeezerLogin()
		deezerSwapCrashAfter = 0
		if err == nil {
			t.Fatalf("stop %d: the seam did not stop the swap", stop)
		}
		got := all(f)
		for _, n := range []string{"new", "old", "oldest"} {
			if !got[testLoginMarker+n] {
				t.Fatalf("stop %d: login %s is lost: %v", stop, n, got)
			}
		}
		if stop == 1 {
			// The kept login is still the refused one: the swap runs again.
			if _, err := s.UseOlderDeezerLogin(); err != nil {
				t.Fatalf("stop %d: the swap did not finish when run again: %v", stop, err)
			}
		}
		if k, _ := loadKeptLogin(f.keptPath); k.Secret != testLoginMarker+"old" {
			t.Fatalf("stop %d: kept = %+v", stop, k)
		}
		if got := all(f); len(got) != 3 {
			t.Fatalf("stop %d: logins afterwards = %v", stop, got)
		}
	}
}

// A copy the owner pressed for replaces a kept login and backs it up, marked
// refused when Deezer refused it; a login backed up as refused is never
// taken again.
func TestDeezerAdoptReplaceBacksUp(t *testing.T) {
	sender, receiver, recv, logs := deezerSharePair(t)
	if err := saveKeptLogin(recv.keptPath, testLogin("mine")); err != nil {
		t.Fatal(err)
	}
	key := receiver.DeezerShareKey()
	sealed, err := sender.SealKeptDeezerLogin(key, DeezerShareAccountHash(key, testDeezerAccount))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := receiver.AdoptPeerDeezerLogin(sealed, []string{testDeezerAccount}, DeezerAdoptOptions{Peer: "peer#a"}); !errors.Is(err, ErrDeezerShareAlreadyKept) {
		t.Fatalf("without Replace: %v", err)
	}
	_, changed, err := receiver.AdoptPeerDeezerLogin(sealed, []string{testDeezerAccount},
		DeezerAdoptOptions{Peer: "peer#a", PeerName: "Kitchen", Replace: true, CurrentRefused: true})
	if err != nil || !changed {
		t.Fatalf("with Replace: %v %v", changed, err)
	}
	if k, _ := loadKeptLogin(recv.keptPath); k.Secret != testDeezerSecret || k.From != "peer" || k.PeerName != "Kitchen" {
		t.Fatalf("kept = %+v", k)
	}
	got := loadPreviousLogins(recv.keptPath)
	if len(got) != 1 || got[0].Secret != testLoginMarker+"mine" || !got[0].Refused {
		t.Fatalf("backups = %+v", got)
	}
	if sum := receiver.KeptDeezerSummary(); sum.FromName != "Kitchen" || sum.Previous != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	// The same login again: nothing changes.
	sealed2, _ := sender.SealKeptDeezerLogin(key, DeezerShareAccountHash(key, testDeezerAccount))
	if _, changed, err := receiver.AdoptPeerDeezerLogin(sealed2, []string{testDeezerAccount}, DeezerAdoptOptions{Replace: true}); err != nil || changed {
		t.Fatalf("same login: %v %v", changed, err)
	}
	// The receiver backs its "mine" up as refused; a copy of exactly that
	// login is refused.
	other := newDeezerFixture(t, "")
	if err := saveKeptLogin(other.keptPath, testLogin("mine")); err != nil {
		t.Fatal(err)
	}
	sealed3, err := other.server(true).SealKeptDeezerLogin(key, DeezerShareAccountHash(key, testDeezerAccount))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := receiver.AdoptPeerDeezerLogin(sealed3, []string{testDeezerAccount}, DeezerAdoptOptions{Replace: true}); !errors.Is(err, ErrDeezerShareRefusedBefore) {
		t.Fatalf("a refused login came back: %v", err)
	}
	noLoginLeak(t, "the adopt log", logs())
}

// Forgetting Deezer on STR's side forgets the backups too.
func TestClearKeptDeezerForgetsTheBackups(t *testing.T) {
	f, s := refusedSetup(t)
	if _, err := os.Stat(previousLoginPath(f.keptPath)); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearKeptDeezer(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{f.keptPath, previousLoginPath(f.keptPath)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s is still on disk: %v", p, err)
		}
	}
	if st := s.DeezerLoginSnapshot(); st.Kept || st.PreviousCount != 0 {
		t.Fatalf("snapshot = %+v", st)
	}
}
