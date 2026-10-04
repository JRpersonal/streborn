package marge

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// A Pandora registration as the firmware posts it after a local
// setMusicServiceAccount. The exact credential shape is unknown, so the tests
// cover both the attribute and the element-text form.
const pandoraAddSourceBody = `<?xml version="1.0" encoding="UTF-8" ?><source>` +
	`<credential type="token">secret-token-value</credential>` +
	`<name>Pandora</name><sourceproviderid>1</sourceproviderid>` +
	`<sourcename>PANDORA</sourcename><username>listener@example.com</username></source>`

func newPandoraServer(t *testing.T, enabled bool) *Server {
	t.Helper()
	dir := t.TempDir()
	s := New(testStoredMusicLogger(), WithPandora(filepath.Join(dir, "pandora-optin"), filepath.Join(dir, "pandora-source.json")))
	if enabled {
		if err := s.SetPandoraEnabled(true); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func serve(s *Server, method, path, body, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if remote != "" {
		req.RemoteAddr = remote
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func pandoraService(svcs []ServiceAvailability) (ServiceAvailability, bool) {
	for _, sv := range svcs {
		if sv.Type == "PANDORA" {
			return sv, true
		}
	}
	return ServiceAvailability{}, false
}

func TestPandoraServiceAvailabilityFollowsOptIn(t *testing.T) {
	s := newPandoraServer(t, false)
	sv, ok := pandoraService(s.services())
	if !ok || sv.Available {
		t.Fatalf("opt-in off: PANDORA must stay unavailable, got %+v", sv)
	}
	if err := s.SetPandoraEnabled(true); err != nil {
		t.Fatal(err)
	}
	sv, _ = pandoraService(s.services())
	if !sv.Available || sv.Reason != "" {
		t.Fatalf("opt-in on: want PANDORA available without reason, got %+v", sv)
	}
	// The shared default table must not be mutated by the opt-in.
	def, _ := pandoraService(DefaultServices)
	if def.Available {
		t.Fatal("DefaultServices was mutated")
	}
	if err := s.SetPandoraEnabled(false); err != nil {
		t.Fatal(err)
	}
	if sv, _ = pandoraService(s.services()); sv.Available {
		t.Fatal("opt-in switched off but PANDORA still available")
	}
}

func TestPandoraNotWiredStaysOff(t *testing.T) {
	s := New(testStoredMusicLogger())
	if s.PandoraEnabled() {
		t.Fatal("a server without WithPandora reports the opt-in on")
	}
	if sv, _ := pandoraService(s.services()); sv.Available {
		t.Fatal("PANDORA available without the opt-in wired")
	}
}

func TestPandoraAddSourcePersistsAndEchoes(t *testing.T) {
	s := newPandoraServer(t, true)
	w := serve(s, http.MethodPost, "/streaming/account/stick@local/source", pandoraAddSourceBody, "127.0.0.1:4000")
	if w.Code != http.StatusCreated || w.Header().Get("METHOD_NAME") != "addSource" {
		t.Fatalf("status %d, METHOD_NAME %q", w.Code, w.Header().Get("METHOD_NAME"))
	}
	for _, want := range []string{`<sourceproviderid>1</sourceproviderid>`, `<sourcename>PANDORA</sourcename>`, `secret-token-value`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("addSource answer misses %s:\n%s", want, w.Body.String())
		}
	}
	st := s.PandoraStatusSnapshot()
	if !st.Registered || !st.HasCredential || st.CredentialType != "token" {
		t.Fatalf("status after register: %+v", st)
	}
	if strings.Contains(st.Username, "listener") {
		t.Errorf("status username not masked: %q", st.Username)
	}
	if s.PandoraUsername() != "listener@example.com" {
		t.Errorf("PandoraUsername = %q", s.PandoraUsername())
	}

	// The firmware (loopback) gets the source with its credential in /full.
	full := serve(s, http.MethodGet, "/streaming/account/stick@local/full", "", "127.0.0.1:4000").Body.String()
	if !strings.Contains(full, `<sourcename>PANDORA</sourcename>`) || !strings.Contains(full, "secret-token-value") {
		t.Fatalf("/full for the box lacks the Pandora source:\n%s", full)
	}
	list := serve(s, http.MethodGet, "/streaming/account/stick@local/sources", "", "127.0.0.1:4000").Body.String()
	if !strings.Contains(list, `<sourcename>PANDORA</sourcename>`) {
		t.Fatalf("source list lacks Pandora:\n%s", list)
	}
	// Anyone else on the LAN sees the source but never the credential.
	lan := serve(s, http.MethodGet, "/streaming/account/stick@local/full", "", "192.0.2.10:4000").Body.String()
	if strings.Contains(lan, "secret-token-value") || !strings.Contains(lan, `<sourcename>PANDORA</sourcename>`) {
		t.Fatalf("/full for a LAN client leaks or drops the credential:\n%s", lan)
	}

	// Switching the opt-in off removes it from both documents.
	if err := s.SetPandoraEnabled(false); err != nil {
		t.Fatal(err)
	}
	if full := serve(s, http.MethodGet, "/streaming/account/stick@local/full", "", "127.0.0.1:4000").Body.String(); strings.Contains(full, "PANDORA") {
		t.Fatalf("/full still carries Pandora with the opt-in off:\n%s", full)
	}
	if err := s.ClearPandora(); err != nil {
		t.Fatal(err)
	}
	if s.PandoraStatusSnapshot().Registered {
		t.Fatal("ClearPandora left the record")
	}
}

func TestPandoraAddSourceIgnoredWithoutOptIn(t *testing.T) {
	s := newPandoraServer(t, false)
	serve(s, http.MethodPost, "/streaming/account/stick@local/source", pandoraAddSourceBody, "127.0.0.1:4000")
	if s.PandoraStatusSnapshot().Registered {
		t.Fatal("opt-in off, but the Pandora registration was stored")
	}
}

func TestPandoraSpyLogMasksCredential(t *testing.T) {
	s := newPandoraServer(t, true)
	serve(s, http.MethodPost, "/streaming/account/stick@local/source", pandoraAddSourceBody, "127.0.0.1:4000")
	for _, e := range s.RecentRequests() {
		if strings.Contains(e.Body, "secret-token-value") {
			t.Fatalf("spy log kept the credential: %s", e.Body)
		}
	}
	log := serve(s, http.MethodGet, "/__spy/log", "", "").Body.String()
	if strings.Contains(log, "secret-token-value") {
		t.Fatal("/__spy/log shows the credential")
	}
}

func TestPandoraCredentialForms(t *testing.T) {
	cases := []struct{ body, typ, val string }{
		{`<credential type="token">abc</credential>`, "token", "abc"},
		{`<credential type="password" text="xyz"/>`, "password", "xyz"},
		{`<credential type="" text=""/>`, "", ""},
		{`<name>x</name>`, "", ""},
	}
	for _, c := range cases {
		typ, val := credentialOf(c.body)
		if typ != c.typ || val != c.val {
			t.Errorf("credentialOf(%q) = %q,%q want %q,%q", c.body, typ, val, c.typ, c.val)
		}
	}
}

func TestMaskCredentials(t *testing.T) {
	in := `<a><credential type="token">abc</credential><credential type="p" text="xyz"/><credential type=""></credential></a>`
	got := maskCredentials(in)
	if strings.Contains(got, "abc") || strings.Contains(got, "xyz") {
		t.Fatalf("not masked: %s", got)
	}
	want := `<a><credential type="token">***</credential><credential type="p" text="***"/><credential type=""></credential></a>`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestIsPandoraRequest(t *testing.T) {
	if !isPandoraRequest("/streaming/account/x/source", pandoraAddSourceBody) {
		t.Error("Pandora registration not recognised")
	}
	if !isPandoraRequest("/bmx/pandora/v1/x", "") {
		t.Error("pandora path not recognised")
	}
	if isPandoraRequest("/streaming/account/x/source", `<sourceproviderid>11</sourceproviderid><sourcename>TUNEIN</sourcename>`) {
		t.Error("provider 11 mistaken for Pandora")
	}
}

func TestMaskAccount(t *testing.T) {
	if got := maskAccount("listener@example.com"); got != "li***@example.com" {
		t.Errorf("maskAccount = %q", got)
	}
	if got := maskAccount(""); strings.Contains(got, "@") {
		t.Errorf("maskAccount(empty) = %q", got)
	}
}
