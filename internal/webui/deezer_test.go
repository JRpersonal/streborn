package webui

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/marge"
)

// Placeholders only; the password is a marker the leak checks look for.
const (
	deezerTestUser = "user@example.com"
	deezerTestPass = "deezer-test-password"
)

// deezerTestServer wires a real stand-in (in a temp dir) and a fake speaker,
// and logs into a buffer so the leak checks can read it.
func deezerTestServer(t *testing.T) (*Server, *marge.Server, *fakeNativeBox, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dir := t.TempDir()
	m := marge.New(logger, marge.WithKeptDeezerLogin(filepath.Join(dir, "deezer-login.json")),
		marge.WithReflectSourcesPath(filepath.Join(dir, "reflect-sources.json")))
	s := &Server{logger: logger, boxHost: "127.0.0.1"}
	box := &fakeNativeBox{}
	s.deezer = m
	s.nativeBoxOverride = box
	return s, m, box, logs
}

func deezerCall(s *Server, method, body string) *httptest.ResponseRecorder {
	ct := ""
	if body != "" {
		ct = "application/json"
	}
	w := httptest.NewRecorder()
	s.handleDeezerAccount(w, nativeReq(method, "/api/deezer/account", body, "192.168.1.5:5000", ct))
	return w
}

func deezerViewOf(t *testing.T, w *httptest.ResponseRecorder, key string) DeezerAccountView {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("answer %q: %v", w.Body.String(), err)
	}
	b := w.Body.Bytes()
	if key != "" {
		b = raw[key]
	}
	var v DeezerAccountView
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("view %q: %v", b, err)
	}
	return v
}

// A new login is refused with 410 and its code, the body is never handed to
// the speaker, and the other methods keep their rules.
func TestDeezerAccountNewLoginIsGone(t *testing.T) {
	s, _, box, logs := deezerTestServer(t)
	w := httptest.NewRecorder()
	s.handleDeezerAccount(w, nativeReq(http.MethodPost, "/api/deezer/account", `{"user":"a","password":"b"}`, "203.0.113.7:5000", "application/json"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-LAN caller got %d", w.Code)
	}
	w = deezerCall(s, http.MethodPost, `{"user":"`+deezerTestUser+`","password":"`+deezerTestPass+`"}`)
	if w.Code != http.StatusGone || !strings.Contains(w.Body.String(), `"code":"new-login-unsupported"`) {
		t.Fatalf("new login: %d %s", w.Code, w.Body.String())
	}
	if box.setUser != "" || box.setPass != "" {
		t.Fatalf("the login reached the speaker: %q", box.setUser)
	}
	if strings.Contains(logs.String(), deezerTestPass) || strings.Contains(logs.String(), deezerTestUser) {
		t.Fatal("the refused login was logged")
	}
	if w := deezerCall(s, http.MethodPut, ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT: %d", w.Code)
	}
	bare := &Server{logger: s.logger}
	w = httptest.NewRecorder()
	bare.handleDeezerAccount(w, nativeReq(http.MethodGet, "/api/deezer/account", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("unwired: %d", w.Code)
	}
}

func TestDeezerStates(t *testing.T) {
	now := time.Now()
	recent := &marge.DeezerAttempt{Kind: "recreate", At: now.Add(-10 * time.Second), FirmwareStatus: 200}
	old := &marge.DeezerAttempt{Kind: "recreate", At: now.Add(-5 * time.Minute), FirmwareStatus: 200}
	refused := &marge.DeezerAttempt{Kind: "recreate", At: now, FirmwareStatus: 500, FirmwareError: "1005 X"}
	removal := &marge.DeezerAttempt{Kind: "remove", At: now, FirmwareStatus: 200}
	ready := deezerSourceLine{Status: "READY", HasAccount: true}
	unavail := deezerSourceLine{Status: "UNAVAILABLE", HasAccount: true}
	absent := deezerSourceLine{Status: "absent"}
	for _, c := range []struct {
		src     deezerSourceLine
		readErr error
		a       *marge.DeezerAttempt
		state   string
	}{
		{ready, nil, nil, "connected"},
		{ready, nil, recent, "connected"},
		{absent, nil, nil, "not-connected"},
		{unavail, nil, nil, "unavailable"},
		{unavail, nil, recent, "connecting"},
		{absent, nil, recent, "connecting"},
		{unavail, nil, old, "unavailable"},
		{unavail, nil, refused, "unavailable"},
		{absent, nil, removal, "not-connected"},
		{absent, io.ErrUnexpectedEOF, nil, "unknown"},
	} {
		st, _, _ := deezerState(c.src, c.readErr, c.a, now)
		if st != c.state {
			t.Errorf("%+v %+v: got %s, want %s", c.src, c.a, st, c.state)
		}
	}
	for _, c := range []struct {
		a       *marge.DeezerAttempt
		running bool
		stuck   bool
	}{
		{nil, false, false},
		{&marge.DeezerAttempt{Kind: "recreate", FirmwareStatus: 0}, false, true},
		{&marge.DeezerAttempt{Kind: "recreate", FirmwareStatus: 0}, true, false},
		{&marge.DeezerAttempt{Kind: "login", FirmwareStatus: 500, FirmwareError: "1024 SOURCE_ALREADY_EXISTS"}, false, true},
		{&marge.DeezerAttempt{Kind: "remove", FirmwareStatus: 0}, false, false},
		{&marge.DeezerAttempt{Kind: "recreate", FirmwareStatus: 200}, false, false},
	} {
		if got := deezerStuck(c.a, c.running); got != c.stuck {
			t.Errorf("stuck(%+v, %v) = %v, want %v", c.a, c.running, got, c.stuck)
		}
	}
}

func deezerRecheck(s *Server, ct string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.handleDeezerRecheck(w, nativeReq(http.MethodPost, "/api/deezer/recheck", "{}", "192.168.1.5:5000", ct))
	return w
}

// keepDeezerLogin makes the stand-in keep a Deezer login, the way the
// firmware's own registration does.
func keepDeezerLogin(t *testing.T, m *marge.Server) {
	t.Helper()
	body := `<source><credential type="token">fake-token-for-tests</credential><name>Deezer</name>` +
		`<username>` + deezerTestUser + `</username><sourceproviderid>14</sourceproviderid><sourcename>DEEZER</sourcename></source>`
	req := httptest.NewRequest(http.MethodPost, "/streaming/account/stick@local/source", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:4000"
	m.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if m.KeptDeezerAccount() == "" {
		t.Fatal("the stand-in kept no login")
	}
}

// Check again on a speaker that lists Deezer UNAVAILABLE while STR keeps a
// login: the speaker's account is removed, the notification is sent, the
// state reads connecting, and a second press inside the gap does nothing.
func TestDeezerRecheckRecreatesTheKeptLogin(t *testing.T) {
	s, m, box, logs := deezerTestServer(t)
	keepDeezerLogin(t, m)
	box.sources = []boxapi.Source{{Source: "DEEZER", SourceAccount: deezerTestUser, Status: "UNAVAILABLE"}}
	box.keepAfterRemove = true
	if w := deezerRecheck(s, "text/plain"); w.Code != http.StatusUnsupportedMediaType || box.removeCalls != 0 {
		t.Fatalf("form post: %d, %d removals", w.Code, box.removeCalls)
	}
	w := deezerRecheck(s, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("recheck: %d %s", w.Code, w.Body.String())
	}
	v := deezerViewOf(t, w, "")
	if v.Recreate == nil || !v.Recreate.Ran || v.Recreate.Error != "" {
		t.Fatalf("recreate = %+v", v.Recreate)
	}
	if box.removeCalls != 1 || box.removedSource != "DEEZER" || box.removedUser != deezerTestUser || box.notifyCalls != 1 {
		t.Fatalf("removals %d (%q %q), notifications %d", box.removeCalls, box.removedSource, box.removedUser, box.notifyCalls)
	}
	if v.State != "connecting" || v.Stuck {
		t.Fatalf("view = %+v", v)
	}
	a := m.LastDeezerAttempt()
	if a == nil || a.Kind != "recreate" || a.Trigger != "check-again" || a.FirmwareStatus != http.StatusOK || a.NotifyStatus != http.StatusOK {
		t.Fatalf("attempt = %+v", a)
	}
	if m.KeptDeezerAccount() == "" {
		t.Fatal("the recreate forgot the kept login")
	}
	w = deezerRecheck(s, "application/json")
	if v := deezerViewOf(t, w, ""); v.Recreate == nil || v.Recreate.Ran || v.Recreate.Skipped != "ran-recently" || box.removeCalls != 1 {
		t.Fatalf("second press: %+v, %d removals", v.Recreate, box.removeCalls)
	}
	for _, out := range []string{w.Body.String(), logs.String()} {
		if strings.Contains(out, deezerTestUser) || strings.Contains(out, "fake-token-for-tests") {
			t.Fatalf("the login leaked: %s", out)
		}
	}
}

// The recreate makes no call at all without a kept login, and none to a
// speaker whose Deezer works or that lists no Deezer account.
func TestDeezerRecreateOnlyWhenItApplies(t *testing.T) {
	s, m, box, _ := deezerTestServer(t)
	box.sources = []boxapi.Source{{Source: "DEEZER", SourceAccount: deezerTestUser, Status: "UNAVAILABLE"}}
	if res := s.RecreateDeezerFromKeptLogin(t.Context(), "start"); res.Ran || res.Skipped != "no-kept-login" {
		t.Fatalf("without a kept login: %+v", res)
	}
	keepDeezerLogin(t, m)
	box.sources = []boxapi.Source{{Source: "DEEZER", SourceAccount: deezerTestUser, Status: "READY"}}
	if res := s.RecreateDeezerFromKeptLogin(t.Context(), "start"); res.Ran || res.Skipped != "ready" {
		t.Fatalf("READY: %+v", res)
	}
	box.sources = nil
	if res := s.RecreateDeezerFromKeptLogin(t.Context(), "start"); res.Ran || res.Skipped != "not-listed" {
		t.Fatalf("absent: %+v", res)
	}
	if box.removeCalls != 0 || box.notifyCalls != 0 {
		t.Fatalf("calls made: %d removals, %d notifications", box.removeCalls, box.notifyCalls)
	}
	// A refused removal sends no notification and is reported.
	box.sources = []boxapi.Source{{Source: "DEEZER", SourceAccount: deezerTestUser, Status: "UNAVAILABLE"}}
	box.removeErr = &boxapi.BoxError{Path: "/removeMusicServiceAccount", Status: 500, Value: "1024", Name: "SOURCE_ALREADY_EXISTS"}
	res := s.RecreateDeezerFromKeptLogin(t.Context(), "start")
	if !res.Ran || !strings.Contains(res.Error, "1024") || box.notifyCalls != 0 {
		t.Fatalf("refused removal: %+v, %d notifications", res, box.notifyCalls)
	}
	if v := s.deezerView(t.Context()); !v.Stuck || v.State != "unavailable" {
		t.Fatalf("after a 1024: %+v", v)
	}
}

// The firmware's relogin retry 3330 (Deezer refused the token) marks a
// speaker that is not connected as rejected.
func TestDeezerRejectedByRetryCode(t *testing.T) {
	s, m, box, _ := deezerTestServer(t)
	code := "3330"
	m.SetDeezerRetrySource(func() (string, time.Time, bool) { return code, time.Now(), true })
	box.sources = []boxapi.Source{{Source: "DEEZER", SourceAccount: deezerTestUser, Status: "UNAVAILABLE"}}
	v := deezerViewOf(t, deezerCall(s, http.MethodGet, ""), "")
	if !v.Rejected || v.Login.LastRetry == nil || v.Login.LastRetry.Code != "3330" {
		t.Fatalf("3330: %+v", v)
	}
	code = "3351"
	if v := deezerViewOf(t, deezerCall(s, http.MethodGet, ""), ""); v.Rejected {
		t.Fatalf("3351 read as rejected: %+v", v)
	}
	// While a recreate runs the code is the one from before it.
	code = "3330"
	m.BeginDeezerAttempt("recreate")
	m.NoteDeezerFirmwareAnswer(http.StatusOK, "", "")
	if v := deezerViewOf(t, deezerCall(s, http.MethodGet, ""), ""); v.Rejected || v.State != "connecting" {
		t.Fatalf("connecting: %+v", v)
	}
	box.sources[0].Status = "READY"
	if v := deezerViewOf(t, deezerCall(s, http.MethodGet, ""), ""); v.Rejected || v.State != "connected" {
		t.Fatalf("READY: %+v", v)
	}
}

// Disconnect removes the account the speaker lists, forgets the kept login,
// and reports the speaker without Deezer.
func TestDeezerDisconnect(t *testing.T) {
	s, m, box, _ := deezerTestServer(t)
	box.sources = []boxapi.Source{{Source: "DEEZER", SourceAccount: deezerTestUser, Status: "READY"}}
	w := deezerCall(s, http.MethodDelete, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if box.removedSource != "DEEZER" || box.removedUser != deezerTestUser {
		t.Fatalf("removed %q %q", box.removedSource, box.removedUser)
	}
	if v := deezerViewOf(t, w, "status"); v.State != "not-connected" {
		t.Fatalf("after removal: %+v", v)
	}
	if a := m.LastDeezerAttempt(); a == nil || a.Kind != "remove" || a.FirmwareStatus != http.StatusOK {
		t.Fatalf("attempt = %+v", a)
	}
	// Again, with nothing left: no call, still 200.
	box.removeCalls = 0
	if w := deezerCall(s, http.MethodDelete, ""); w.Code != http.StatusOK || box.removeCalls != 0 {
		t.Fatalf("second removal: %d, %d calls", w.Code, box.removeCalls)
	}
}

func TestParseDeezerLink(t *testing.T) {
	for raw, want := range map[string]DeezerLink{
		"https://www.deezer.com/en/album/376222197":               {"album", "376222197"},
		"https://www.deezer.com/album/376222197?utm_source=share": {"album", "376222197"},
		"https://deezer.com/de/playlist/1234567890/":              {"playlist", "1234567890"},
		"http://www.deezer.com/pt-br/track/3135556":               {"track", "3135556"},
		"  https://www.deezer.com/US/Track/3135556  ":             {"track", "3135556"},
	} {
		got, ok := ParseDeezerLink(raw)
		if !ok || got != want {
			t.Errorf("%q: %+v %v, want %+v", raw, got, ok, want)
		}
	}
	for _, raw := range []string{
		"", "deezer.com/album/1", "https://www.deezer.com/en/artist/27", "https://www.deezer.com/en/album/12ab",
		"https://evil.example/www.deezer.com/album/1", "https://www.deezer.com.evil.example/album/1",
		"https://link.deezer.com/s/30abcdef", "ftp://www.deezer.com/album/1",
	} {
		if l, ok := ParseDeezerLink(raw); ok {
			t.Errorf("%q accepted as %+v", raw, l)
		}
	}
}

// A share link is followed only through Deezer's own hosts.
func TestResolveDeezerShortLink(t *testing.T) {
	hops := map[string]string{
		"link.deezer.com/s/abc": "https://www.deezer.com/album/42?x=1",
		"link.deezer.com/s/off": "https://evil.example/album/42",
		"link.deezer.com/s/two": "https://deezer.page.link/xyz",
		"deezer.page.link/xyz":  "https://www.deezer.com/fr/playlist/77",
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var asked []string
	s.deezerHTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		key := r.URL.Host + r.URL.Path
		asked = append(asked, key)
		loc, ok := hops[key]
		if !ok {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("<html/>")), Header: http.Header{}, Request: r}, nil
		}
		h := http.Header{}
		h.Set("Location", loc)
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: h, Request: r}, nil
	})}
	ctx := t.Context()
	if l, err := s.resolveDeezerLink(ctx, "https://link.deezer.com/s/abc"); err != nil || l != (DeezerLink{"album", "42"}) {
		t.Fatalf("abc: %+v %v", l, err)
	}
	if l, err := s.resolveDeezerLink(ctx, "https://link.deezer.com/s/two"); err != nil || l != (DeezerLink{"playlist", "77"}) {
		t.Fatalf("two hops: %+v %v", l, err)
	}
	asked = nil
	if _, err := s.resolveDeezerLink(ctx, "https://link.deezer.com/s/off"); err == nil {
		t.Fatal("a redirect off Deezer was accepted")
	}
	for _, a := range asked {
		if strings.HasPrefix(a, "evil.example") {
			t.Fatal("the agent fetched a host outside Deezer")
		}
	}
	asked = nil
	if _, err := s.resolveDeezerLink(ctx, "https://example.com/album/1"); err == nil || len(asked) != 0 {
		t.Fatalf("a foreign link was fetched: %v %v", err, asked)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A Deezer link plays as the speaker's own Deezer item, with the account the
// speaker lists and the title from Deezer's public API.
func TestDeezerPlay(t *testing.T) {
	s, _, box, _ := deezerTestServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/album/376222197" {
			_, _ = io.WriteString(w, `{"id":376222197,"title":"Paschtaschutta","artist":{"name":"X"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()
	prev := deezerAPIBase
	deezerAPIBase = api.URL
	defer func() { deezerAPIBase = prev }()

	play := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.handleDeezerPlay(w, nativeReq(http.MethodPost, "/api/deezer/play", body, "192.168.1.5:5000", "application/json"))
		return w
	}
	// No Deezer account on the speaker.
	if w := play(`{"url":"https://www.deezer.com/en/album/376222197"}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "deezer-not-linked") {
		t.Fatalf("not linked: %d %s", w.Code, w.Body.String())
	}
	box.sources = []boxapi.Source{{Source: "DEEZER", SourceAccount: deezerTestUser, Status: "UNAVAILABLE"}}
	if w := play(`{"url":"https://www.deezer.com/en/album/376222197"}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), nativeServiceUnavailableCode) {
		t.Fatalf("unavailable: %d %s", w.Code, w.Body.String())
	}
	if len(box.selected) != 0 {
		t.Fatal("selected while not READY")
	}
	box.sources[0].Status = "READY"
	w := play(`{"url":"https://www.deezer.com/en/album/376222197"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("play: %d %s", w.Code, w.Body.String())
	}
	want := `<ContentItem source="DEEZER" type="album" location="376222197" sourceAccount="` + deezerTestUser + `" isPresetable="true"><itemName>Paschtaschutta</itemName></ContentItem>`
	if len(box.selected) != 1 || box.selected[0] != want {
		t.Fatalf("selected %v\nwant %s", box.selected, want)
	}
	if w := play(`{"url":"https://www.example.com/album/1"}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("foreign link: %d", w.Code)
	}
	// The name the app sends wins, and no API call is needed.
	api.Close()
	if w := play(`{"url":"https://www.deezer.com/track/9","name":"Mine"}`); w.Code != http.StatusOK || !strings.Contains(box.selected[len(box.selected)-1], "<itemName>Mine</itemName>") {
		t.Fatalf("named: %d %v", w.Code, box.selected)
	}
	// No API answer: named after the kind.
	if w := play(`{"url":"https://www.deezer.com/playlist/9"}`); w.Code != http.StatusOK || !strings.Contains(box.selected[len(box.selected)-1], "<itemName>Deezer playlist</itemName>") {
		t.Fatalf("fallback name: %d %v", w.Code, box.selected)
	}
}
