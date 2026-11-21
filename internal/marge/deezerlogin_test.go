package marge

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Placeholders only: the account is not a real Deezer id and the secret is a
// marker the leak checks look for.
const (
	testDeezerAccount = "1111111111"
	testDeezerSecret  = "deezer-login-secret-value"
)

// A speaker's own Sources.xml holding a Deezer login, next to entries that
// must be ignored.
var deezerSourcesXML = `<?xml version="1.0" encoding="UTF-8" ?><sources>` +
	`<source secret="" secretType=""><sourceKey type="LOCAL_INTERNET_RADIO" account=""/></source>` +
	`<source displayName="Listener" secret="` + testDeezerSecret + `" secretType="token">` +
	`<sourceKey type="DEEZER" account="` + testDeezerAccount + `"/></source>` +
	`<source displayName="user@example.com" secret="spotify-secret" secretType="token_version_3">` +
	`<sourceKey type="SPOTIFY" account="user@example.com"/></source>` +
	`</sources>`

type deezerFixture struct {
	dir, sourcesPath, keptPath, reflectPath string
	logs                                    *bytes.Buffer
	logger                                  *slog.Logger
}

func newDeezerFixture(t *testing.T, sourcesXML string) deezerFixture {
	t.Helper()
	dir := t.TempDir()
	f := deezerFixture{
		dir:         dir,
		sourcesPath: filepath.Join(dir, "BoseApp-Persistence", "1", "Sources.xml"),
		keptPath:    filepath.Join(dir, "streborn", "deezer-login.json"),
		reflectPath: filepath.Join(dir, "streborn", "reflect-sources.json"),
		logs:        &bytes.Buffer{},
	}
	f.logger = slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if sourcesXML != "" {
		if err := os.MkdirAll(filepath.Dir(f.sourcesPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.sourcesPath, []byte(sourcesXML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f deezerFixture) glob() string {
	return filepath.Join(f.dir, "BoseApp-Persistence", "*", "Sources.xml")
}

func (f deezerFixture) writeReflect(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.reflectPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.reflectPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f deezerFixture) server(withKept bool) *Server {
	opts := []Option{WithDeviceID("DEVICEID_PLACEHOLDER"), WithReflectSourcesPath(f.reflectPath)}
	if withKept {
		opts = append(opts, WithKeptDeezerLogin(f.keptPath))
	}
	return New(f.logger, opts...)
}

func fullFrom(t *testing.T, s *Server, remote string) string {
	t.Helper()
	w := serve(s, http.MethodGet, "/streaming/account/stick@local/full", "", remote)
	if w.Code != http.StatusOK {
		t.Fatalf("/full status %d", w.Code)
	}
	body := w.Body.String()
	xmlWellFormed(t, body)
	return body
}

func TestCaptureKeepsTheSpeakersDeezerLogin(t *testing.T) {
	f := newDeezerFixture(t, deezerSourcesXML)
	if !CaptureKeptDeezerLogin(f.glob(), f.keptPath, f.logger) {
		t.Fatal("capture reported no login")
	}
	k, ok := loadKeptLogin(f.keptPath)
	if !ok || k.Account != testDeezerAccount || k.Secret != testDeezerSecret || k.SecretType != "token" || k.From != "sources-file" {
		t.Fatalf("kept = %+v, %v", k, ok)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(f.keptPath)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("kept file mode %v, want 0600", fi.Mode().Perm())
		}
	}

	// A second start with the same login writes nothing.
	before, _ := os.ReadFile(f.keptPath)
	if !CaptureKeptDeezerLogin(f.glob(), f.keptPath, f.logger) {
		t.Fatal("second capture reported no login")
	}
	after, _ := os.ReadFile(f.keptPath)
	if !bytes.Equal(before, after) {
		t.Fatal("an unchanged login was rewritten")
	}
}

func TestCaptureWithoutDeezerWritesNothing(t *testing.T) {
	for name, xmlDoc := range map[string]string{
		"no file":   "",
		"no deezer": `<sources><source secret="" secretType=""><sourceKey type="LOCAL_INTERNET_RADIO" account=""/></source></sources>`,
		// What a speaker already on an older STR may hold: the entry, no login.
		"deezer without login": `<sources><source secret="" secretType="token"><sourceKey type="DEEZER" account="` + testDeezerAccount + `"/></source></sources>`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newDeezerFixture(t, xmlDoc)
			if CaptureKeptDeezerLogin(f.glob(), f.keptPath, f.logger) {
				t.Fatal("capture reported a login")
			}
			if _, err := os.Stat(f.keptPath); !os.IsNotExist(err) {
				t.Fatalf("a file was written: %v", err)
			}
			if _, err := os.Stat(filepath.Dir(f.keptPath)); !os.IsNotExist(err) {
				t.Fatalf("a directory was created: %v", err)
			}
		})
	}
}

func TestCaptureKeepsAnEarlierLoginWhenTheFileLostIt(t *testing.T) {
	f := newDeezerFixture(t, deezerSourcesXML)
	CaptureKeptDeezerLogin(f.glob(), f.keptPath, f.logger)
	before, _ := os.ReadFile(f.keptPath)
	// The firmware rewrote its file without the token.
	if err := os.WriteFile(f.sourcesPath, []byte(`<sources><source secret=""><sourceKey type="DEEZER" account="`+testDeezerAccount+`"/></source></sources>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !CaptureKeptDeezerLogin(f.glob(), f.keptPath, f.logger) {
		t.Fatal("the earlier login was dropped")
	}
	after, _ := os.ReadFile(f.keptPath)
	if !bytes.Equal(before, after) {
		t.Fatal("the kept login changed")
	}
}

func TestFullHandsTheLoginBackOnlyToTheSpeaker(t *testing.T) {
	f := newDeezerFixture(t, deezerSourcesXML)
	f.writeReflect(t, `[{"source":"DEEZER","account":"`+testDeezerAccount+`","name":"Deezer"}]`)
	CaptureKeptDeezerLogin(f.glob(), f.keptPath, f.logger)
	s := f.server(true)

	own := fullFrom(t, s, "127.0.0.1:4000")
	for _, want := range []string{
		`<credential type="token">` + testDeezerSecret + `</credential>`,
		`<sourceproviderid>14</sourceproviderid>`,
		`<sourcename>DEEZER</sourcename>`,
		`<username>` + testDeezerAccount + `</username>`,
	} {
		if !strings.Contains(own, want) {
			t.Fatalf("speaker's /full lacks %s:\n%s", want, own)
		}
	}
	if strings.Count(own, `<sourcename>DEEZER</sourcename>`) != 1 {
		t.Fatalf("/full names Deezer more than once:\n%s", own)
	}

	lan := fullFrom(t, s, "192.0.2.10:4000")
	if strings.Contains(lan, testDeezerSecret) {
		t.Fatalf("a LAN client got the login:\n%s", lan)
	}
	if !strings.Contains(lan, `<credential type="token">***</credential>`) {
		t.Fatalf("LAN /full lacks the masked credential:\n%s", lan)
	}
}

func TestFullNamesAKeptLoginWithoutAReflectEntry(t *testing.T) {
	// A speaker updated from an older STR: the snapshot that seeds the reflect
	// file read /sources after Deezer had gone, so only the kept login knows.
	f := newDeezerFixture(t, deezerSourcesXML)
	CaptureKeptDeezerLogin(f.glob(), f.keptPath, f.logger)
	s := f.server(true)
	own := fullFrom(t, s, "127.0.0.1:4000")
	if !strings.Contains(own, `<credential type="token">`+testDeezerSecret+`</credential>`) ||
		!strings.Contains(own, `<username>`+testDeezerAccount+`</username>`) ||
		!strings.Contains(own, `<sourceproviderid>14</sourceproviderid>`) {
		t.Fatalf("/full lacks the kept Deezer source:\n%s", own)
	}
	// A preset the firmware stores on that source resolves to DEEZER.
	var id string
	for _, r := range s.reflectedAccountSources() {
		if r.typ == "DEEZER" {
			id = r.id
		}
	}
	if id == "" || s.sourceNameForID(id) != "DEEZER" {
		t.Fatalf("kept source id %q resolves to %q", id, s.sourceNameForID(id))
	}
}

func TestAnotherDeezerAccountGetsNoLogin(t *testing.T) {
	f := newDeezerFixture(t, deezerSourcesXML)
	f.writeReflect(t, `[{"source":"DEEZER","account":"2222222222","name":"Deezer"}]`)
	CaptureKeptDeezerLogin(f.glob(), f.keptPath, f.logger)
	own := fullFrom(t, f.server(true), "127.0.0.1:4000")
	// The reflected account keeps its empty credential; the kept one is its
	// own entry with the login.
	if !strings.Contains(own, `<credential type="token"></credential><name>Deezer</name><sourceproviderid>14</sourceproviderid><sourcename>DEEZER</sourcename><sourceSettings/><updatedOn>2020-01-01T00:00:00.000+00:00</updatedOn><username>2222222222</username>`) {
		t.Fatalf("the other account changed:\n%s", own)
	}
	if strings.Count(own, testDeezerSecret) != 1 {
		t.Fatalf("login not exactly once:\n%s", own)
	}
}

func TestWithoutAKeptLoginEveryDocumentIsUnchanged(t *testing.T) {
	// The invariant for every speaker without a Deezer login: with the
	// feature wired, the documents are byte for byte what they were.
	for name, reflect := range map[string]string{
		"no reflect file":  "",
		"reflected deezer": `[{"source":"DEEZER","account":"` + testDeezerAccount + `","name":"Deezer"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newDeezerFixture(t, "")
			if reflect != "" {
				f.writeReflect(t, reflect)
			}
			CaptureKeptDeezerLogin(f.glob(), f.keptPath, f.logger)
			with, without := f.server(true), f.server(false)
			for _, path := range []string{
				"/streaming/account/stick@local/full",
				"/streaming/account/stick@local/sources",
				"/streaming/sourceproviders",
			} {
				for _, remote := range []string{"127.0.0.1:4000", "192.0.2.10:4000"} {
					a := serve(with, http.MethodGet, path, "", remote).Body.String()
					b := serve(without, http.MethodGet, path, "", remote).Body.String()
					if a != b {
						t.Fatalf("%s from %s differs:\nwith    %s\nwithout %s", path, remote, a, b)
					}
				}
			}
		})
	}
}

const deezerAddSourceWithLogin = `<?xml version="1.0" encoding="UTF-8" ?><source>` +
	`<credential type="token">` + testDeezerSecret + `</credential>` +
	`<name>Deezer</name><sourceproviderid>14</sourceproviderid>` +
	`<sourcename>DEEZER</sourcename><username>` + testDeezerAccount + `</username></source>`

func TestDeezerRegistrationIsAnsweredAsDeezerAndKept(t *testing.T) {
	f := newDeezerFixture(t, "")
	s := f.server(true)
	w := serve(s, http.MethodPost, "/streaming/account/stick@local/source", deezerAddSourceWithLogin, "127.0.0.1:4000")
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d", w.Code)
	}
	echo := w.Body.String()
	xmlWellFormed(t, echo)
	if !strings.Contains(echo, `<sourceproviderid>14</sourceproviderid><sourcename>DEEZER</sourcename>`) || strings.Contains(echo, "STORED_MUSIC") {
		t.Fatalf("registration not answered as Deezer: %s", echo)
	}
	if strings.Contains(echo, testDeezerSecret) {
		t.Fatalf("the echo carries the login: %s", echo)
	}
	k, ok := loadKeptLogin(f.keptPath)
	if !ok || k.Secret != testDeezerSecret || k.From != "registration" {
		t.Fatalf("kept = %+v, %v", k, ok)
	}
	// The list the firmware reads right after a registration names it READY,
	// with the login for the speaker and masked for anyone else.
	own := serve(s, http.MethodGet, "/streaming/account/stick@local/sources", "", "127.0.0.1:4000").Body.String()
	if !strings.Contains(own, `status="READY"><credential type="token">`+testDeezerSecret+`</credential>`) {
		t.Fatalf("sources list lacks the kept Deezer source:\n%s", own)
	}
	lan := serve(s, http.MethodGet, "/streaming/account/stick@local/sources", "", "192.0.2.10:4000").Body.String()
	if strings.Contains(lan, testDeezerSecret) {
		t.Fatalf("a LAN client got the login from the sources list:\n%s", lan)
	}
}

func TestDeezerLoginNeverLeaks(t *testing.T) {
	f := newDeezerFixture(t, deezerSourcesXML)
	CaptureKeptDeezerLogin(f.glob(), f.keptPath, f.logger)
	s := f.server(true)
	serve(s, http.MethodPost, "/streaming/account/stick@local/source", deezerAddSourceWithLogin, "127.0.0.1:4000")
	fullFrom(t, s, "127.0.0.1:4000")

	// Agent log.
	for _, leak := range []string{testDeezerSecret, testDeezerAccount} {
		if strings.Contains(f.logs.String(), leak) {
			t.Fatalf("log carries %q:\n%s", leak, f.logs.String())
		}
	}
	// Request log, in memory and over HTTP.
	spy := serve(s, http.MethodGet, "/__spy/log", "", "192.0.2.10:4000").Body.String()
	if strings.Contains(spy, testDeezerSecret) {
		t.Fatalf("/__spy/log carries the login:\n%s", spy)
	}
	for _, line := range s.RecentRequestLines(0) {
		if strings.Contains(line, testDeezerSecret) {
			t.Fatalf("request trail carries the login: %s", line)
		}
	}
	// Debug section (it reaches the diagnostic bundle).
	js, _ := json.Marshal(s.DeezerLoginSnapshot())
	for _, leak := range []string{testDeezerSecret, testDeezerAccount} {
		if strings.Contains(string(js), leak) {
			t.Fatalf("debug section carries %q: %s", leak, js)
		}
	}
	st := s.DeezerLoginSnapshot()
	if !st.Configured || !st.Kept || st.SecretType != "token" {
		t.Fatalf("snapshot = %+v", st)
	}
}

func TestUnreadableSourcesFileLogsNoContent(t *testing.T) {
	f := newDeezerFixture(t, `<sources><source secret="`+testDeezerSecret+`"><sourceKey type="DEEZER"`)
	CaptureKeptDeezerLogin(f.glob(), f.keptPath, slog.New(slog.NewTextHandler(f.logs, nil)))
	if strings.Contains(f.logs.String(), testDeezerSecret) {
		t.Fatalf("log carries the login:\n%s", f.logs.String())
	}

}
