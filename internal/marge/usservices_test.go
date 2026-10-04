package marge

import (
	"net/http"
	"net/http/httptest"
	"os"
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

// The iHeartRadio registration in the same shape (provider id 16).
const iheartAddSourceBody = `<?xml version="1.0" encoding="UTF-8" ?><source>` +
	`<credential type="token">iheart-secret</credential>` +
	`<name>iHeartRadio</name><sourceproviderid>16</sourceproviderid>` +
	`<sourcename>IHEART</sourcename><username>listener@example.com</username></source>`

func newPandoraServer(t *testing.T, enabled bool) *Server {
	t.Helper()
	return newNativeServer(t, enabled, "", nil)
}

// newNativeServer wires both stores, the opt-in, a region and optional extra
// options.
func newNativeServer(t *testing.T, optIn bool, region string, extra []Option) *Server {
	t.Helper()
	dir := t.TempDir()
	opts := []Option{
		WithPandora(filepath.Join(dir, "pandora-optin"), filepath.Join(dir, "pandora-source.json")),
		WithIHeart(filepath.Join(dir, "iheart-source.json")),
		WithRegion(func() string { return region }),
	}
	s := New(testStoredMusicLogger(), append(opts, extra...)...)
	if optIn {
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

func nativeStatus(s *Server, typ string) NativeServiceStatus {
	for _, ns := range s.NativeStatusSnapshot().Services {
		if ns.Type == typ {
			return ns
		}
	}
	return NativeServiceStatus{}
}

func serviceOf(svcs []ServiceAvailability, typ string) (ServiceAvailability, bool) {
	for _, sv := range svcs {
		if sv.Type == typ {
			return sv, true
		}
	}
	return ServiceAvailability{}, false
}

func pandoraService(svcs []ServiceAvailability) (ServiceAvailability, bool) {
	return serviceOf(svcs, "PANDORA")
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
	s := New(testStoredMusicLogger(), WithRegion(func() string { return "US" }))
	if s.PandoraEnabled() {
		t.Fatal("a server without WithPandora reports the opt-in on")
	}
	// Without a store a registration could not be kept, so even a US speaker
	// keeps today's answers.
	if sv, _ := pandoraService(s.services()); sv.Available {
		t.Fatal("PANDORA available without a store wired")
	}
	if _, ok := serviceOf(s.services(), "IHEART"); ok {
		t.Fatal("IHEART listed without a store wired")
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
	st := nativeStatus(s, "PANDORA")
	if !st.Registered || !st.HasCredential || st.CredentialType != "token" || st.Mode != "optin" {
		t.Fatalf("status after register: %+v", st)
	}
	if strings.Contains(st.Username, "listener") {
		t.Errorf("status username not masked: %q", st.Username)
	}
	if s.NativeUsername("PANDORA") != "listener@example.com" {
		t.Errorf("NativeUsername = %q", s.NativeUsername("PANDORA"))
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
	if err := s.ClearNative("PANDORA"); err != nil {
		t.Fatal(err)
	}
	if nativeStatus(s, "PANDORA").Registered {
		t.Fatal("ClearNative left the record")
	}
}

func TestPandoraAddSourceIgnoredWithoutOptIn(t *testing.T) {
	s := newPandoraServer(t, false)
	serve(s, http.MethodPost, "/streaming/account/stick@local/source", pandoraAddSourceBody, "127.0.0.1:4000")
	if nativeStatus(s, "PANDORA").Registered {
		t.Fatal("opt-in off, but the Pandora registration was stored")
	}
	// The attempt is still recorded for the bundle, masked and not accepted.
	posts := s.NativeStatusSnapshot().Posts
	if len(posts) != 1 || posts[0].Accepted || posts[0].Service != "PANDORA" || strings.Contains(posts[0].Username, "listener") {
		t.Fatalf("posts = %+v", posts)
	}
}

func TestPandoraSpyLogMasksCredential(t *testing.T) {
	s := newPandoraServer(t, true)
	serve(s, http.MethodPost, "/streaming/account/stick@local/source", pandoraAddSourceBody, "127.0.0.1:4000")
	serve(s, http.MethodPost, "/streaming/account/stick@local/source", iheartAddSourceBody, "127.0.0.1:4000")
	for _, e := range s.RecentRequests() {
		if strings.Contains(e.Body, "secret-token-value") || strings.Contains(e.Body, "iheart-secret") {
			t.Fatalf("spy log kept the credential: %s", e.Body)
		}
	}
	log := serve(s, http.MethodGet, "/__spy/log", "", "").Body.String()
	if strings.Contains(log, "secret-token-value") || strings.Contains(log, "iheart-secret") {
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

func TestNativeRequestType(t *testing.T) {
	if nativeRequestType("/streaming/account/x/source", pandoraAddSourceBody) != "PANDORA" {
		t.Error("Pandora registration not recognised")
	}
	if nativeRequestType("/bmx/pandora/v1/x", "") != "PANDORA" {
		t.Error("pandora path not recognised")
	}
	if nativeRequestType("/streaming/account/x/source", iheartAddSourceBody) != "IHEART" {
		t.Error("iHeart registration not recognised")
	}
	if got := nativeRequestType("/streaming/account/x/source", `<sourceproviderid>11</sourceproviderid><sourcename>TUNEIN</sourcename>`); got != "" {
		t.Errorf("provider 11 mistaken for %s", got)
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

func TestUSSpeakerGetsBothServicesWithoutOptIn(t *testing.T) {
	s := newNativeServer(t, false, "US", nil)
	for _, typ := range []string{"PANDORA", "IHEART"} {
		sv, ok := serviceOf(s.services(), typ)
		if !ok || !sv.Available || sv.Reason != "" {
			t.Errorf("US speaker: %s = %+v, %v", typ, sv, ok)
		}
		if m := nativeStatus(s, typ).Mode; m != "us" {
			t.Errorf("%s mode = %q, want us", typ, m)
		}
	}
	w := serve(s, http.MethodPost, "/streaming/account/stick@local/source", iheartAddSourceBody, "127.0.0.1:4000")
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `<sourceproviderid>16</sourceproviderid>`) {
		t.Fatalf("iHeart addSource: %d %s", w.Code, w.Body.String())
	}
	serve(s, http.MethodPost, "/streaming/account/stick@local/source", pandoraAddSourceBody, "127.0.0.1:4000")
	full := serve(s, http.MethodGet, "/streaming/account/stick@local/full", "", "127.0.0.1:4000").Body.String()
	for _, want := range []string{`<sourcename>IHEART</sourcename>`, `<sourcename>PANDORA</sourcename>`, "iheart-secret", `<source id="201"`} {
		if !strings.Contains(full, want) {
			t.Errorf("/full misses %s", want)
		}
	}
	st := s.NativeStatusSnapshot()
	if !st.US || st.Region != "US" || len(st.Posts) != 2 || !st.Posts[0].Accepted {
		t.Fatalf("status = %+v", st)
	}
	if sv, ok := serviceOf(st.Served, "IHEART"); !ok || !sv.Available {
		t.Fatal("served list in the status lacks IHEART")
	}
}

func TestNonUSSpeakerKeepsTodaysAnswers(t *testing.T) {
	for _, region := range []string{"", "DE", "CA"} {
		s := newNativeServer(t, false, region, nil)
		got := s.services()
		if len(got) != len(DefaultServices) {
			t.Fatalf("region %q: service list changed: %+v", region, got)
		}
		for i := range got {
			if got[i] != DefaultServices[i] {
				t.Fatalf("region %q: entry %d changed: %+v", region, i, got[i])
			}
		}
		serve(s, http.MethodPost, "/streaming/account/stick@local/source", iheartAddSourceBody, "127.0.0.1:4000")
		if nativeStatus(s, "IHEART").Registered {
			t.Fatalf("region %q: iHeart registration stored", region)
		}
		full := serve(s, http.MethodGet, "/streaming/account/stick@local/full", "", "127.0.0.1:4000").Body.String()
		if strings.Contains(full, "IHEART") || strings.Contains(full, "PANDORA") {
			t.Fatalf("region %q: /full names a US service:\n%s", region, full)
		}
	}
}

func TestOptInEnablesIHeartOutsideUS(t *testing.T) {
	s := newNativeServer(t, true, "AU", nil)
	if sv, ok := serviceOf(s.services(), "IHEART"); !ok || !sv.Available {
		t.Fatalf("opt-in: IHEART = %+v, %v", sv, ok)
	}
	if nativeStatus(s, "IHEART").Mode != "optin" {
		t.Fatal("mode should be optin")
	}
}

func TestRegionIsReadOnEveryUse(t *testing.T) {
	cc := ""
	dir := t.TempDir()
	s := New(testStoredMusicLogger(),
		WithPandora(filepath.Join(dir, "m"), filepath.Join(dir, "p.json")),
		WithIHeart(filepath.Join(dir, "i.json")),
		WithRegion(func() string { return cc }))
	if s.NativeServiceEnabled("IHEART") {
		t.Fatal("unknown region enabled iHeart")
	}
	cc = "us"
	if !s.NativeServiceEnabled("IHEART") || !s.NativeServiceEnabled("iheartradio") {
		t.Fatal("US region (lower case) did not enable iHeart")
	}
}

func TestCarriedOverAccountYieldsToRegistration(t *testing.T) {
	dir := t.TempDir()
	reflect := filepath.Join(dir, "reflect-sources.json")
	if err := os.WriteFile(reflect, []byte(`[{"source":"IHEART","account":"olduser@example.com","name":"iHeartRadio"},{"source":"DEEZER","account":"123","name":"Deezer"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newNativeServer(t, false, "US", []Option{WithReflectSourcesPath(reflect)})
	full := serve(s, http.MethodGet, "/streaming/account/stick@local/full", "", "127.0.0.1:4000").Body.String()
	if strings.Count(full, `<sourcename>IHEART</sourcename>`) != 1 || !strings.Contains(full, "olduser@example.com") {
		t.Fatalf("carried-over iHeart missing from /full:\n%s", full)
	}
	if got := nativeStatus(s, "IHEART").CarriedOver; got != "ol***@example.com" {
		t.Errorf("carriedOver = %q", got)
	}
	serve(s, http.MethodPost, "/streaming/account/stick@local/source", iheartAddSourceBody, "127.0.0.1:4000")
	full = serve(s, http.MethodGet, "/streaming/account/stick@local/full", "", "127.0.0.1:4000").Body.String()
	if strings.Count(full, `<sourcename>IHEART</sourcename>`) != 1 || strings.Contains(full, "olduser@example.com") {
		t.Fatalf("the registration should replace the carried-over entry:\n%s", full)
	}
	if !strings.Contains(full, `<sourcename>DEEZER</sourcename>`) {
		t.Fatal("Deezer reflection lost")
	}
}

func TestPostHistoryIsBounded(t *testing.T) {
	s := newNativeServer(t, false, "US", nil)
	for i := 0; i < nativePostCap+5; i++ {
		serve(s, http.MethodPost, "/streaming/account/stick@local/source", iheartAddSourceBody, "127.0.0.1:4000")
	}
	if n := len(s.NativeStatusSnapshot().Posts); n != nativePostCap {
		t.Fatalf("posts = %d, want %d", n, nativePostCap)
	}
}
