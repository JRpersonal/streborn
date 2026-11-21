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

func TestDeezerAccountLANOnlyAndJSON(t *testing.T) {
	s, _, box, _ := deezerTestServer(t)
	w := httptest.NewRecorder()
	s.handleDeezerAccount(w, nativeReq(http.MethodPost, "/api/deezer/account", `{"user":"a","password":"b"}`, "203.0.113.7:5000", "application/json"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-LAN caller got %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.handleDeezerAccount(w, nativeReq(http.MethodPost, "/api/deezer/account", `{"user":"a","password":"b"}`, "192.168.1.5:5000", "application/x-www-form-urlencoded"))
	if w.Code != http.StatusUnsupportedMediaType || box.setUser != "" {
		t.Fatalf("form post: %d, sent %q", w.Code, box.setUser)
	}
	if w := deezerCall(s, http.MethodPost, `{"user":"","password":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty user: %d", w.Code)
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

// Connect hands the login to the speaker in AfterTouch's shape, records the
// attempt and reports the state; nothing carries the password.
func TestDeezerConnectRecordsTheAttempt(t *testing.T) {
	s, m, box, logs := deezerTestServer(t)
	box.sources = []boxapi.Source{{Source: "DEEZER", SourceAccount: deezerTestUser, Status: "UNAVAILABLE"}}
	w := deezerCall(s, http.MethodPost, `{"user":" `+deezerTestUser+` ","password":"`+deezerTestPass+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if box.setSource != "DEEZER" || box.setDisplay != deezerDisplayName || box.setUser != deezerTestUser || box.setPass != deezerTestPass {
		t.Fatalf("sent %q %q %q", box.setSource, box.setDisplay, box.setUser)
	}
	v := deezerViewOf(t, w, "status")
	if v.State != "connecting" || v.Source.Status != "UNAVAILABLE" || v.Account != "us***@example.com" {
		t.Fatalf("view = %+v", v)
	}
	a := m.LastDeezerAttempt()
	if a == nil || a.Kind != "login" || a.FirmwareStatus != http.StatusOK || a.SourceStatus != "UNAVAILABLE" {
		t.Fatalf("attempt = %+v", a)
	}
	// The speaker finishes the login: the next read says connected.
	box.sources = []boxapi.Source{{Source: "DEEZER", SourceAccount: deezerTestUser, Status: "READY"}}
	if v := deezerViewOf(t, deezerCall(s, http.MethodGet, ""), ""); v.State != "connected" {
		t.Fatalf("after READY: %+v", v)
	}
	if m.LastDeezerAttempt().SourceStatus != "READY" {
		t.Fatal("the status read is not recorded with the attempt")
	}
	js, _ := json.Marshal(m.DeezerLoginSnapshot())
	for _, out := range []string{w.Body.String(), logs.String(), string(js)} {
		if strings.Contains(out, deezerTestPass) || strings.Contains(out, deezerTestUser) {
			t.Fatalf("the login leaked: %s", out)
		}
	}
}

// A refusal answers with a code, records the firmware's status and code,
// and keeps the account and the password out of the record and the log.
func TestDeezerConnectRefusedIsRecordedWithoutTheLogin(t *testing.T) {
	s, m, box, logs := deezerTestServer(t)
	box.setErr = &boxapi.BoxError{Path: "/setMusicServiceAccount", Status: 500, Value: "1005", Name: "MUSIC_SERVICE_LOGIN_FAILED",
		Detail: "login for " + deezerTestUser + " with " + deezerTestPass + " failed",
		Body:   `<errors deviceID="AABBCCDDEEFF"><error>login for ` + deezerTestUser + ` with ` + deezerTestPass + `</error></errors>`}
	w := deezerCall(s, http.MethodPost, `{"user":"`+deezerTestUser+`","password":"`+deezerTestPass+`"}`)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), `"code":"account-refused"`) {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	v := deezerViewOf(t, w, "status")
	if v.State != "failed" || v.Reason != "refused" || v.Detail != "1005 MUSIC_SERVICE_LOGIN_FAILED" {
		t.Fatalf("view = %+v", v)
	}
	a := m.LastDeezerAttempt()
	if a.FirmwareStatus != 500 || a.FirmwareError != "1005 MUSIC_SERVICE_LOGIN_FAILED" || a.FirmwareDetail != "login for *** with *** failed" {
		t.Fatalf("attempt = %+v", a)
	}
	js, _ := json.Marshal(m.DeezerLoginSnapshot())
	for _, out := range []string{w.Body.String(), logs.String(), string(js)} {
		if strings.Contains(out, deezerTestPass) || strings.Contains(out, deezerTestUser) || strings.Contains(out, "AABBCCDDEEFF") {
			t.Fatalf("leaked: %s", out)
		}
	}
}

func TestDeezerConnectIsOneAtATime(t *testing.T) {
	s, _, box, _ := deezerTestServer(t)
	s.deezerLoginMu.Lock()
	w := deezerCall(s, http.MethodPost, `{"user":"a@example.com","password":"pw1"}`)
	s.deezerLoginMu.Unlock()
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "login-busy") || box.setUser != "" {
		t.Fatalf("second login: %d %s, sent %q", w.Code, w.Body.String(), box.setUser)
	}
}

func TestDeezerStates(t *testing.T) {
	now := time.Now()
	recent := &marge.DeezerAttempt{Kind: "login", At: now.Add(-10 * time.Second), FirmwareStatus: 200}
	old := &marge.DeezerAttempt{Kind: "login", At: now.Add(-5 * time.Minute), FirmwareStatus: 200}
	noAnswer := &marge.DeezerAttempt{Kind: "login", At: now, FirmwareStatus: 0}
	removal := &marge.DeezerAttempt{Kind: "remove", At: now, FirmwareStatus: 200}
	ready := deezerSourceLine{Status: "READY", HasAccount: true}
	unavail := deezerSourceLine{Status: "UNAVAILABLE", HasAccount: true}
	absent := deezerSourceLine{Status: "absent"}
	for _, c := range []struct {
		src        deezerSourceLine
		readErr    error
		a          *marge.DeezerAttempt
		state, rsn string
	}{
		{ready, nil, nil, "connected", ""},
		{ready, nil, noAnswer, "connected", ""},
		{absent, nil, nil, "not-connected", ""},
		{unavail, nil, nil, "unavailable", ""},
		{unavail, nil, recent, "connecting", ""},
		{unavail, nil, old, "failed", "not-ready"},
		{absent, nil, old, "failed", "not-ready"},
		{absent, nil, noAnswer, "failed", "no-answer"},
		{absent, nil, removal, "not-connected", ""},
		{absent, io.ErrUnexpectedEOF, nil, "unknown", ""},
	} {
		st, rsn, _ := deezerState(c.src, c.readErr, c.a, now)
		if st != c.state || rsn != c.rsn {
			t.Errorf("%+v %+v: got %s/%s, want %s/%s", c.src, c.a, st, rsn, c.state, c.rsn)
		}
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
