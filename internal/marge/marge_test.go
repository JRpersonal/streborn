package marge

import (
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// xmlWellFormed checks whether the string is parseable as XML.
func xmlWellFormed(t *testing.T, in string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(in))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("xml not well-formed: %v\n--\n%s\n--", err, in)
		}
	}
}

func newTestServer() *Server {
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithDeviceID("DEVICEID_PLACEHOLDER"),
		WithSpyLogSize(50),
	)
}

func TestEmptyPresetsXMLWellFormed(t *testing.T) {
	xmlWellFormed(t, EmptyPresetsXML)
}

func TestEmptyRecentsXMLWellFormed(t *testing.T) {
	xmlWellFormed(t, EmptyRecentsXML)
}

func TestSoundTouchStatusXMLWellFormed(t *testing.T) {
	xmlWellFormed(t, SoundTouchConfiguredXML)
	xmlWellFormed(t, SoundTouchNotConfiguredXML)
}

func TestPresetsHandlerEmpty(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/preset/list", nil)
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "<presets") {
		t.Fatalf("expected <presets in body, got: %s", body)
	}
}

func TestPresetsHandlerMitInhalt(t *testing.T) {
	s := newTestServer()
	s.SetPresets([]Preset{
		{ID: 1, Source: "SPOTIFY", Type: "uri", Location: "spotify:playlist:xyz",
			SourceAccount: "user@example.com", ItemName: "Morning",
			ContainerArt: "https://example.com/art.png",
			CreatedOn:    1700000000, UpdatedOn: 1700000000},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/preset/list", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "Morning") {
		t.Fatalf("preset name not found: %s", body)
	}
	if !strings.Contains(body, "spotify:playlist:xyz") {
		t.Fatalf("preset location not found: %s", body)
	}
}

func TestServiceAvailabilityHandler(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/services/availability", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	// Make sure the most important providers are present.
	for _, want := range []string{"SPOTIFY", "DEEZER", "AMAZON", "AIRPLAY"} {
		if !strings.Contains(body, want) {
			t.Fatalf("provider %s missing: %s", want, body)
		}
	}
}

func TestReflectDeezerSource(t *testing.T) {
	// Path A: a reflect-sources file with a Deezer entry must make the marge stub
	// re-advertise Deezer both as a source provider and as an account-linked
	// source, so the box keeps it and plays via its cached ARL.
	dir := t.TempDir()
	rp := dir + "/reflect-sources.json"
	if err := os.WriteFile(rp, []byte(`[{"source":"DEEZER","account":"1456373802","name":"Deezer"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithDeviceID("DEVICEID_PLACEHOLDER"), WithReflectSourcesPath(rp))

	get := func(path string) string {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d", path, rec.Code)
		}
		body := rec.Body.String()
		xmlWellFormed(t, body)
		return body
	}

	sp := get("/streaming/sourceproviders")
	if !strings.Contains(sp, `id="DEEZER"`) {
		t.Fatalf("sourceproviders missing DEEZER: %s", sp)
	}
	full := get("/streaming/account/1456373802/full")
	if !strings.Contains(full, `type="DEEZER"`) || !strings.Contains(full, "1456373802") {
		t.Fatalf("account/full missing reflected Deezer source: %s", full)
	}

	// Without a reflect file: no Deezer is advertised (safe default).
	s2 := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithDeviceID("DEVICEID_PLACEHOLDER"))
	rec := httptest.NewRecorder()
	s2.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/streaming/sourceproviders", nil))
	if strings.Contains(rec.Body.String(), `id="DEEZER"`) {
		t.Fatalf("sourceproviders should not list DEEZER without a reflect file: %s", rec.Body.String())
	}
}

func TestSourcesHandlerWithoutItems(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sources", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "DEVICEID_PLACEHOLDER") {
		t.Fatalf("deviceID not in body: %s", body)
	}
}

func TestAccountHandlerUnkonfiguriert(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/account/info", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "UNCONFIGURED") {
		t.Fatalf("expected UNCONFIGURED status: %s", body)
	}
}

func TestAccountHandlerConfigured(t *testing.T) {
	s := newTestServer()
	s.SetAccount(&AccountInfo{
		AccountUUID:  "uuid-123",
		AccountEmail: "test@example.com",
		AuthToken:    "token-xyz",
		CreatedAt:    "2026-05-14T12:00:00Z",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/account/info", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "uuid-123") {
		t.Fatalf("AccountUUID missing: %s", body)
	}
}

func TestSpyLogfaengtRequestsAuf(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/some/unknown/path",
		strings.NewReader("<?xml version=\"1.0\"?><test/>"))
	req.Header.Set("Content-Type", "application/xml")
	s.Handler().ServeHTTP(rec, req)

	entries := s.RecentRequests()
	if len(entries) != 1 {
		t.Fatalf("expected 1 spy entry, got %d", len(entries))
	}
	if entries[0].Method != "POST" {
		t.Fatalf("method wrong: %s", entries[0].Method)
	}
	if entries[0].Path != "/some/unknown/path" {
		t.Fatalf("path wrong: %s", entries[0].Path)
	}
	if !strings.Contains(entries[0].Body, "<test") {
		t.Fatalf("body missing: %s", entries[0].Body)
	}
}

func TestSpyLogEndpoint(t *testing.T) {
	s := newTestServer()
	// First a request to log something
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/preset/foo", nil)
	s.Handler().ServeHTTP(rec, req)

	// Then fetch the spy log
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/__spy/log", nil)
	s.Handler().ServeHTTP(rec2, req2)
	body := rec2.Body.String()
	if !strings.Contains(body, "GET /preset/foo") {
		t.Fatalf("spy log does not contain the request: %s", body)
	}
}

func TestCatchallGenericAck(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/completely/unknown", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "<ack") {
		t.Fatalf("generic ack missing: %s", body)
	}
}

// TestMargeGroupCreateReadDelete exercises the stereo-pair group CRUD the ST10
// firmware runs against marge as the cloud half of /addGroup and /removeGroup
// (#166). The captured create call is
// POST /streaming/account/stick@local/group/ with a <group> descriptor; the box
// must get a group record back or it fails with GROUP_CREATE_GROUP_ON_MARGE_ERROR.
func TestMargeGroupCreateReadDelete(t *testing.T) {
	s := newTestServer()
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		s.Handler().ServeHTTP(rec, req)
		return rec
	}

	const acct = "stick@local"
	const master = "EC24B8B790CC"
	const slave = "94E36DF9CE40"
	createBody := `<?xml version="1.0" encoding="UTF-8" ?><group><masterDeviceId>` + master +
		`</masterDeviceId><name>Stereo TEST</name><roles>` +
		`<groupRole><deviceId>` + master + `</deviceId><role>LEFT</role></groupRole>` +
		`<groupRole><deviceId>` + slave + `</deviceId><role>RIGHT</role></groupRole>` +
		`</roles></group>`

	// Create: must not fall through to the account handler, must echo the group.
	rec := serve(http.MethodPost, "/streaming/account/"+acct+"/group/", createBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d, want 201\nbody=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	xmlWellFormed(t, body)
	for _, want := range []string{"<group ", "id=", master, slave, "LEFT", "RIGHT"} {
		if !strings.Contains(body, want) {
			t.Fatalf("create response missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "<account") {
		t.Fatalf("create leaked into the account handler: %s", body)
	}

	// Poll: with a stored pair the box's group poll must read the pair back.
	rec = serve(http.MethodGet, "/streaming/account/"+acct+"/device/"+master+"/group/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("poll status=%d, want 200", rec.Code)
	}
	poll := rec.Body.String()
	xmlWellFormed(t, poll)
	if !strings.Contains(poll, master) || !strings.Contains(poll, slave) {
		t.Fatalf("poll did not return the stored pair: %s", poll)
	}

	// Delete: dissolve clears the store.
	rec = serve(http.MethodDelete, "/streaming/account/"+acct+"/group/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d, want 200", rec.Code)
	}
	xmlWellFormed(t, rec.Body.String())

	// After delete the poll falls back to the standalone (account) behaviour.
	rec = serve(http.MethodGet, "/streaming/account/"+acct+"/device/"+master+"/group/", "")
	if strings.Contains(rec.Body.String(), "<group ") {
		t.Fatalf("group still returned after delete: %s", rec.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

// TestLoginFlowPathsNeverError pins the fake-login contract on the stub side:
// every endpoint the box touches while establishing or re-validating its marge
// login must answer with a success status. The firmware treats cloud errors
// during onboarding as "not signed in" (MargeHSM falls back and every UPnP
// source activation is then refused with 1036 NOT_LOGGED_IN), so a single
// regression to a 4xx/5xx here silently kills the hardware preset buttons on
// exactly the boxes that have no cached pre-shutdown Bose account left.
func TestLoginFlowPathsNeverError(t *testing.T) {
	s := newTestServer()
	h := s.Handler()

	paths := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/streaming/support/power_on"},
		{http.MethodGet, "/streaming/support/anything"},
		{http.MethodGet, "/streaming/sourceproviders"},
		{http.MethodPost, "/streaming/account/stick@local/device/"},
		{http.MethodGet, "/streaming/account/stick@local"},
		{http.MethodGet, "/streaming/auth/token"},
		{http.MethodGet, "/bmx/registry/v1/services"},
		{http.MethodGet, "/bmx/anything/else"},
		// Paths STR has not mapped explicitly must still succeed generically:
		// an unknown firmware variant probing a new endpoint must never be
		// told "error" mid-login.
		{http.MethodGet, "/streaming/some/new/endpoint"},
		{http.MethodPost, "/totally/unknown"},
	}
	for _, p := range paths {
		req := httptest.NewRequest(p.method, p.path, strings.NewReader("<x/>"))
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, req)
		if rw.Code >= 400 {
			t.Errorf("%s %s: status %d - a login-flow path must never error", p.method, p.path, rw.Code)
		}
	}
}

// TestAddDeviceIdempotentAcrossForcedRelogins: the login maintenance re-asserts
// setMargeAccount repeatedly on a login-suspect box, so the box re-runs its
// addDevice call again and again. Every round must keep answering the full
// adddeviceresponse (with a margetoken), or a later round would leave the
// MargeHSM in a worse state than the first.
func TestAddDeviceIdempotentAcrossForcedRelogins(t *testing.T) {
	s := newTestServer()
	h := s.Handler()
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/streaming/account/stick@local/device/",
			strings.NewReader(`<adddevicerequest/>`))
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, req)
		// wrap201 answers 201 Created, the format the firmware accepts.
		if rw.Code < 200 || rw.Code > 299 {
			t.Fatalf("addDevice round %d: status %d", i+1, rw.Code)
		}
		body := rw.Body.String()
		xmlWellFormed(t, body)
		if !strings.Contains(body, "margetoken") {
			t.Fatalf("addDevice round %d: response carries no margetoken:\n%s", i+1, body)
		}
	}
}
