package webui

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/region"
)

type fakeNative struct {
	on      bool
	us      bool
	users   map[string]string
	carried map[string]string
	cleared []string
}

func (f *fakeNative) PandoraEnabled() bool                 { return f.on }
func (f *fakeNative) SetPandoraEnabled(on bool) error      { f.on = on; return nil }
func (f *fakeNative) NativeServiceEnabled(_ string) bool   { return f.on || f.us }
func (f *fakeNative) NativeUsername(typ string) string     { return f.users[typ] }
func (f *fakeNative) CarriedOverAccount(typ string) string { return f.carried[typ] }
func (f *fakeNative) ClearNative(typ string) error {
	f.cleared = append(f.cleared, typ)
	delete(f.users, typ)
	delete(f.carried, typ)
	return nil
}
func (f *fakeNative) NativeStatusSnapshot() marge.NativeServicesStatus {
	return marge.NativeServicesStatus{OptIn: f.on, US: f.us}
}

type fakeNativeBox struct {
	setSource, setDisplay, setUser, setPass string
	removedSource, removedUser              string
	removeErr                               error
	removeCalls                             int
	setErr                                  error
	// sourcesAfterRemove, when set, is what /sources lists once the
	// removal call was made (nil: the removal empties the service).
	sourcesAfterRemove          []boxapi.Source
	keepAfterRemove             bool
	sources                     []boxapi.Source
	stations                    []boxapi.ServiceStation
	stationsErr, selectErr      error
	listedSource, listedAccount string
	selected                    []string
	sourcesErr                  error
	// removeStarted, when set, is closed as the removal call arrives, and
	// removeGate then holds it until the test closes the gate.
	removeStarted, removeGate chan struct{}
}

func (b *fakeNativeBox) ListServiceStations(_ context.Context, source, account string) ([]boxapi.ServiceStation, error) {
	b.listedSource, b.listedAccount = source, account
	return b.stations, b.stationsErr
}

func (b *fakeNativeBox) SelectContentItem(_ context.Context, item string) error {
	b.selected = append(b.selected, item)
	return b.selectErr
}

func (b *fakeNativeBox) SetMusicServiceAccount(_ context.Context, source, display, user, pass string) error {
	b.setSource, b.setDisplay, b.setUser, b.setPass = source, display, user, pass
	return b.setErr
}

func (b *fakeNativeBox) RemoveMusicServiceAccount(_ context.Context, source, _, user string) error {
	b.removedSource, b.removedUser = source, user
	b.removeCalls++
	if b.removeStarted != nil {
		close(b.removeStarted)
		<-b.removeGate
	}
	if b.removeErr != nil {
		return b.removeErr
	}
	if !b.keepAfterRemove {
		b.sources = b.sourcesAfterRemove
	}
	return nil
}

func (b *fakeNativeBox) GetSources(context.Context) ([]boxapi.Source, error) {
	if b.sourcesErr != nil {
		return nil, b.sourcesErr
	}
	return b.sources, nil
}

func nativeTestServer(on bool) (*Server, *fakeNative, *fakeNativeBox) {
	s := quietServer("127.0.0.1")
	p := &fakeNative{on: on, users: map[string]string{}}
	box := &fakeNativeBox{}
	s.native = p
	s.nativeBoxOverride = box
	return s, p, box
}

func nativeReq(method, path, body, remote, ct string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = remote
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	return r
}

func TestUSServiceEndpointsLANOnly(t *testing.T) {
	t.Parallel()
	s, _, _ := nativeTestServer(true)
	for _, h := range []http.HandlerFunc{s.handleUSServices, s.handleNativeAccount("PANDORA"), s.handleNativeAccount("IHEART")} {
		w := httptest.NewRecorder()
		h(w, nativeReq(http.MethodPost, "/api/us-services", `{"enabled":true}`, "203.0.113.7:5000", "application/json"))
		if w.Code != http.StatusForbidden {
			t.Errorf("non-LAN caller got %d, want 403", w.Code)
		}
	}
}

func TestUSServicesToggleNeedsJSON(t *testing.T) {
	t.Parallel()
	s, p, _ := nativeTestServer(false)
	w := httptest.NewRecorder()
	s.handleUSServices(w, nativeReq(http.MethodPost, "/api/us-services", `{"enabled":true}`, "192.168.1.5:5000", "application/x-www-form-urlencoded"))
	if w.Code != http.StatusUnsupportedMediaType || p.on {
		t.Fatalf("form post: status %d, on=%v", w.Code, p.on)
	}
	w = httptest.NewRecorder()
	s.handleUSServices(w, nativeReq(http.MethodPost, "/api/us-services", `{"enabled":true}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusOK || !p.on {
		t.Fatalf("json post: status %d, on=%v", w.Code, p.on)
	}
}

func TestNativeAccountRequiresEnabledService(t *testing.T) {
	t.Parallel()
	s, _, box := nativeTestServer(false)
	w := httptest.NewRecorder()
	s.handleNativeAccount("IHEART")(w, nativeReq(http.MethodPost, "/api/iheart/account", `{"user":"u@example.com","password":"pw"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusConflict || box.setUser != "" {
		t.Fatalf("service off: status %d, box got user %q", w.Code, box.setUser)
	}
}

func TestNativeAccountOnUSSpeakerNeedsNoOptIn(t *testing.T) {
	t.Parallel()
	s, p, box := nativeTestServer(false)
	p.us = true
	w := httptest.NewRecorder()
	s.handleNativeAccount("IHEART")(w, nativeReq(http.MethodPost, "/api/iheart/account", `{"user":"u@example.com","password":"pw"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusOK || box.setSource != "IHEART" || box.setDisplay != "iHeartRadio" {
		t.Fatalf("US speaker: status %d, source %q display %q", w.Code, box.setSource, box.setDisplay)
	}
}

// iheartProfileIDError is the shape of the speaker's answer when its iHeart
// client cannot parse a newer account's profile id.
const iheartProfileIDError = `box /setMusicServiceAccount: status 500: <?xml version="1.0" encoding="UTF-8" ?><errors deviceID="device-id-here">` +
	`<error value="1005" name="MUSIC_SERVICE_PARSE_ERROR" severity="Unknown">Out-of-range error parsing number from json string ` +
	`[{&quot;profileId&quot;:12345678901}] Field= [profileId]</error></errors>`

func TestNativeLoginErrorCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want string
	}{
		{errors.New(iheartProfileIDError), codeIHeartProfileIDTooLarge},
		{errors.New("OUT-OF-RANGE ERROR ... FIELD= [PROFILEID]"), codeIHeartProfileIDTooLarge},
		{errors.New("box /setMusicServiceAccount: status 500: MUSIC_SERVICE_PARSE_ERROR invalid credentials"), ""},
		{errors.New("Out-of-range error parsing number Field= [stationId]"), ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := nativeLoginErrorCode(tc.err); got != tc.want {
			t.Errorf("nativeLoginErrorCode(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// The firmware's profileId failure reaches the app as a coded JSON error with
// a plain sentence, never as the raw speaker XML.
func TestIHeartProfileIDTooLargeIsACodedError(t *testing.T) {
	t.Parallel()
	s, _, box := nativeTestServer(true)
	box.setErr = errors.New(iheartProfileIDError)
	w := httptest.NewRecorder()
	s.handleNativeAccount("IHEART")(w, nativeReq(http.MethodPost, "/api/iheart/account", `{"user":"u@example.com","password":"pw"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, w.Body.String())
	}
	if got["code"] != codeIHeartProfileIDTooLarge || !strings.Contains(got["error"], "iHeartRadio") {
		t.Fatalf("body = %+v", got)
	}
	if strings.Contains(w.Body.String(), "MUSIC_SERVICE_PARSE_ERROR") || strings.Contains(w.Body.String(), "device-id-here") {
		t.Fatalf("raw firmware answer leaked to the client: %s", w.Body.String())
	}

	// Any other refusal is coded too and names the firmware's error, but
	// never carries its raw answer.
	box.setErr = firmwareError("/setMusicServiceAccount", "1003", "MUSIC_SERVICE_LOGIN_FAILED", "invalid credentials")
	w = httptest.NewRecorder()
	s.handleNativeAccount("IHEART")(w, nativeReq(http.MethodPost, "/api/iheart/account", `{"user":"u@example.com","password":"pw"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), `"account-refused"`) || !strings.Contains(w.Body.String(), "MUSIC_SERVICE_LOGIN_FAILED") {
		t.Fatalf("other refusal: %d %s", w.Code, w.Body.String())
	}
	assertNoFirmwareLeak(t, w.Body.String())
}

// testDeviceID stands in for the speaker's MAC in a firmware error envelope.
// It contains 1047 on purpose: only the typed error value may count.
const testDeviceID = "AABB1047EEFF"

// firmwareError is the typed error boxapi returns for the speaker's error
// envelope, with the deviceID attribute the real one carries.
func firmwareError(path, value, name, detail string) error {
	return &boxapi.BoxError{
		Path: path, Status: 500, Value: value, Name: name, Severity: "Unknown", Detail: detail,
		Body: `<?xml version="1.0" encoding="UTF-8" ?><errors deviceID="` + testDeviceID + `"><error value="` + value +
			`" name="` + name + `" severity="Unknown">` + detail + `</error></errors>`,
	}
}

// assertNoFirmwareLeak fails when an answer to a client carries the
// firmware's raw error envelope or the speaker's deviceID.
func assertNoFirmwareLeak(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, testDeviceID) || strings.Contains(body, "deviceID") || strings.Contains(body, "<errors") {
		t.Fatalf("raw firmware answer leaked to the client: %s", body)
	}
}

func TestSpeakerErrorReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want string
	}{
		{firmwareError("/x", "1047", "SOURCE_ALREADY_REMOVED", "gone"), "speaker error 1047 SOURCE_ALREADY_REMOVED"},
		{firmwareError("/x", "", "SOME_ERROR", ""), "speaker error SOME_ERROR"},
		{firmwareError("/x", "1005", `bad name="x"`, ""), "speaker error 1005"},
		{&boxapi.BoxError{Path: "/x", Status: 503, Body: "<html>busy</html>"}, "speaker answered HTTP 503"},
		{context.DeadlineExceeded, "the speaker did not answer in time"},
		{errors.New("dial tcp 192.0.2.1:8090: connection refused"), "the speaker could not be reached"},
	}
	for _, tc := range cases {
		if got := speakerErrorReason(tc.err); got != tc.want {
			t.Errorf("speakerErrorReason(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestIsSourceAlreadyRemoved(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want bool
	}{
		{firmwareError("/removeMusicServiceAccount", "1047", "SOURCE_ALREADY_REMOVED", ""), true},
		{firmwareError("/removeMusicServiceAccount", "1047", "", ""), true},
		{firmwareError("/removeMusicServiceAccount", "", "source_already_removed", ""), true},
		{errors.New("box /removeMusicServiceAccount: 500: ... 1047 SOURCE_ALREADY_REMOVED"), true},
		// The deviceID contains 1047; another error must not count.
		{firmwareError("/removeMusicServiceAccount", "1005", "UNKNOWN_SOURCE_ERROR", ""), false},
		{errors.New("status 500"), false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := isSourceAlreadyRemoved(tc.err); got != tc.want {
			t.Errorf("isSourceAlreadyRemoved(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestPandoraAccountAddAndRemove(t *testing.T) {
	t.Parallel()
	s, p, box := nativeTestServer(true)
	w := httptest.NewRecorder()
	s.handleNativeAccount("PANDORA")(w, nativeReq(http.MethodPost, "/api/pandora/account", `{"user":" u@example.com ","password":"pw"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusOK || box.setUser != "u@example.com" || box.setPass != "pw" || box.setSource != "PANDORA" || box.setDisplay != "Pandora Music Service" {
		t.Fatalf("add: status %d, box %+v", w.Code, box)
	}
	if strings.Contains(w.Body.String(), "pw") {
		t.Fatal("response echoes the password")
	}

	p.users["PANDORA"] = "u@example.com"
	w = httptest.NewRecorder()
	s.handleNativeAccount("PANDORA")(w, nativeReq(http.MethodDelete, "/api/pandora/account", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusOK || box.removedUser != "u@example.com" || box.removedSource != "PANDORA" || len(p.cleared) != 1 {
		t.Fatalf("remove: status %d, removed %q, cleared %v", w.Code, box.removedUser, p.cleared)
	}
}

// #1101: an account linked with the Bose app has no STR registration. The
// removal must still reach the speaker with the account it lists, and the
// carried-over entry must be dropped.
func TestCarriedOverAccountIsRemovedFromSpeaker(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"PANDORA", "IHEART"} {
		s, p, box := nativeTestServer(true)
		s.nativeRemoveTiming = &nativeRemoveTiming{settle: 0}
		p.carried = map[string]string{typ: "old@example.com"}
		box.sources = []boxapi.Source{{Source: "TUNEIN", Status: "READY"}, {Source: typ, Status: "READY", SourceAccount: "listed@example.com"}}
		box.sourcesAfterRemove = []boxapi.Source{{Source: "TUNEIN", Status: "READY"}, {Source: typ, Status: "UNAVAILABLE"}}
		w := httptest.NewRecorder()
		s.handleNativeAccount(typ)(w, nativeReq(http.MethodDelete, "/api/x/account", "", "192.168.1.5:5000", ""))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d %s", typ, w.Code, w.Body.String())
		}
		if box.removedSource != typ || box.removedUser != "listed@example.com" {
			t.Fatalf("%s: removal sent for %q/%q, want the listed account", typ, box.removedSource, box.removedUser)
		}
		if len(p.cleared) != 1 || p.carried[typ] != "" {
			t.Fatalf("%s: carried-over entry not cleared: %v %v", typ, p.cleared, p.carried)
		}
	}
}

func TestCarriedOverAccountFallsBackToReflectEntry(t *testing.T) {
	t.Parallel()
	s, p, box := nativeTestServer(true)
	s.nativeRemoveTiming = &nativeRemoveTiming{settle: 0}
	p.carried = map[string]string{"PANDORA": "old@example.com"}
	box.sources = []boxapi.Source{{Source: "PANDORA", Status: "UNAVAILABLE"}}
	box.sourcesAfterRemove = box.sources
	w := httptest.NewRecorder()
	s.handleNativeAccount("PANDORA")(w, nativeReq(http.MethodDelete, "/api/pandora/account", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusOK || box.removedUser != "old@example.com" || len(p.cleared) != 1 {
		t.Fatalf("status %d, removed %q, cleared %v", w.Code, box.removedUser, p.cleared)
	}
}

func TestRemovalStillListedIsAnError(t *testing.T) {
	t.Parallel()
	s, _, box := nativeTestServer(true)
	s.nativeRemoveTiming = &nativeRemoveTiming{settle: 0}
	box.sources = []boxapi.Source{{Source: "PANDORA", Status: "READY", SourceAccount: "listed@example.com"}}
	box.keepAfterRemove = true
	w := httptest.NewRecorder()
	s.handleNativeAccount("PANDORA")(w, nativeReq(http.MethodDelete, "/api/pandora/account", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), `"still-linked"`) {
		t.Fatalf("still listed: status %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "listed@example.com") {
		t.Fatal("error answer leaks the account name")
	}
}

func TestRemovalRefusedIsAnError(t *testing.T) {
	t.Parallel()
	s, p, box := nativeTestServer(true)
	p.users["IHEART"] = "u@example.com"
	box.removeErr = firmwareError("/removeMusicServiceAccount", "1005", "UNKNOWN_SOURCE_ERROR", "refused")
	w := httptest.NewRecorder()
	s.handleNativeAccount("IHEART")(w, nativeReq(http.MethodDelete, "/api/iheart/account", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), `"remove-refused"`) || len(p.cleared) != 0 {
		t.Fatalf("refused: status %d %s, cleared %v", w.Code, w.Body.String(), p.cleared)
	}
	if !strings.Contains(w.Body.String(), "UNKNOWN_SOURCE_ERROR") {
		t.Fatalf("refusal does not name the firmware's error: %s", w.Body.String())
	}
	assertNoFirmwareLeak(t, w.Body.String())
}

// #1937: a second DELETE reached the speaker after the first had removed the
// account, and the speaker's SOURCE_ALREADY_REMOVED went back to the app as
// a 502 with its raw XML. That answer is the removal the caller asked for.
func TestRemovalAlreadyRemovedIsSuccess(t *testing.T) {
	t.Parallel()
	s, p, box := nativeTestServer(true)
	s.nativeRemoveTiming = &nativeRemoveTiming{settle: 0}
	var logBuf syncBuffer
	s.logger = slog.New(slog.NewTextHandler(&logBuf, nil))
	p.users["PANDORA"] = "u@example.com"
	box.sources = []boxapi.Source{{Source: "PANDORA", Status: "UNAVAILABLE"}}
	box.removeErr = firmwareError("/removeMusicServiceAccount", "1047", "SOURCE_ALREADY_REMOVED", "SOURCE_ALREADY_REMOVED")
	w := httptest.NewRecorder()
	s.handleNativeAccount("PANDORA")(w, nativeReq(http.MethodDelete, "/api/pandora/account", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"ok":true}` {
		t.Fatalf("already removed: status %d %s", w.Code, w.Body.String())
	}
	if box.removeCalls != 1 || len(p.cleared) != 1 || p.users["PANDORA"] != "" {
		t.Fatalf("cleanup skipped: calls %d, cleared %v, users %v", box.removeCalls, p.cleared, p.users)
	}
	if log := logBuf.String(); !strings.Contains(log, "level=INFO") || !strings.Contains(log, "already removed") {
		t.Fatalf("no info line for the already removed account: %s", log)
	}

	// The /sources re-check still runs: listed READY afterwards is still-linked.
	s, p, box = nativeTestServer(true)
	s.nativeRemoveTiming = &nativeRemoveTiming{settle: 0}
	p.users["PANDORA"] = "u@example.com"
	box.sources = []boxapi.Source{{Source: "PANDORA", Status: "READY", SourceAccount: "u@example.com"}}
	box.removeErr = firmwareError("/removeMusicServiceAccount", "1047", "SOURCE_ALREADY_REMOVED", "")
	w = httptest.NewRecorder()
	s.handleNativeAccount("PANDORA")(w, nativeReq(http.MethodDelete, "/api/pandora/account", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), `"still-linked"`) {
		t.Fatalf("already removed but listed: status %d %s", w.Code, w.Body.String())
	}
}

// #1937: three DELETEs at once. Only one removal reaches the speaker, and
// every caller gets its answer.
func TestConcurrentRemovalsSendOneRemoval(t *testing.T) {
	t.Parallel()
	s, p, box := nativeTestServer(true)
	s.nativeRemoveTiming = &nativeRemoveTiming{settle: 0}
	var logBuf syncBuffer
	s.logger = slog.New(slog.NewTextHandler(&logBuf, nil))
	p.users["PANDORA"] = "u@example.com"
	box.sources = []boxapi.Source{{Source: "PANDORA", Status: "READY", SourceAccount: "u@example.com"}}
	box.sourcesAfterRemove = []boxapi.Source{{Source: "PANDORA", Status: "UNAVAILABLE"}}
	box.removeStarted, box.removeGate = make(chan struct{}), make(chan struct{})

	h := s.handleNativeAccount("PANDORA")
	const callers = 3
	codes := make([]int, callers)
	bodies := make([]string, callers)
	var wg sync.WaitGroup
	call := func(i int) {
		defer wg.Done()
		w := httptest.NewRecorder()
		h(w, nativeReq(http.MethodDelete, "/api/pandora/account", "", "192.168.1.5:5000", ""))
		codes[i], bodies[i] = w.Code, w.Body.String()
	}
	wg.Add(1)
	go call(0)
	<-box.removeStarted
	for i := 1; i < callers; i++ {
		wg.Add(1)
		go call(i)
	}
	// Hold the first removal until both later callers wait for it.
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(logBuf.String(), "a removal is already running") < callers-1 {
		if time.Now().After(deadline) {
			close(box.removeGate)
			t.Fatalf("later callers never joined the running removal: %s", logBuf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(box.removeGate)
	wg.Wait()

	for i := range codes {
		if codes[i] != http.StatusOK || strings.TrimSpace(bodies[i]) != `{"ok":true}` {
			t.Errorf("caller %d: status %d %s", i, codes[i], bodies[i])
		}
	}
	if box.removeCalls != 1 || len(p.cleared) != 1 {
		t.Fatalf("removals sent %d, cleared %v; want one each", box.removeCalls, p.cleared)
	}

	// A DELETE after it finished is safe too: nothing left, 200, no call.
	w := httptest.NewRecorder()
	h(w, nativeReq(http.MethodDelete, "/api/pandora/account", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusOK || box.removeCalls != 1 {
		t.Fatalf("repeat: status %d %s, calls %d", w.Code, w.Body.String(), box.removeCalls)
	}
}

// A caller that gives up while another removal runs gets a coded answer, and
// the running one is not disturbed.
func TestRemovalWaiterTimesOut(t *testing.T) {
	t.Parallel()
	s, p, box := nativeTestServer(true)
	s.nativeRemoveTiming = &nativeRemoveTiming{settle: 0}
	p.users["IHEART"] = "u@example.com"
	box.removeStarted, box.removeGate = make(chan struct{}), make(chan struct{})
	done := make(chan nativeRemoveResult)
	go func() { done <- s.removeNativeAccountShared(context.Background(), "IHEART", "iHeartRadio") }()
	<-box.removeStarted
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := s.removeNativeAccountShared(ctx, "IHEART", "iHeartRadio"); res.status != http.StatusServiceUnavailable || res.body["code"] != "remove-busy" {
		t.Fatalf("waiter: %+v", res)
	}
	close(box.removeGate)
	if res := <-done; res.status != http.StatusOK {
		t.Fatalf("first removal: %+v", res)
	}
}

// The status snapshot (GET /api/us-services, diagnostic bundle) names a
// failed /sources read without the firmware's raw answer.
func TestNativeBoxSourcesErrorHidesFirmwareAnswer(t *testing.T) {
	t.Parallel()
	s, _, box := nativeTestServer(true)
	box.sourcesErr = firmwareError("/sources", "1005", "SOME_ERROR", "")
	got := s.nativeBoxSources(context.Background())
	if !strings.Contains(got["error"]["error"], "SOME_ERROR") {
		t.Fatalf("error = %v", got["error"])
	}
	assertNoFirmwareLeak(t, got["error"]["error"])
}

func TestRemovalWithNothingLinkedStaysOK(t *testing.T) {
	t.Parallel()
	s, p, box := nativeTestServer(true)
	w := httptest.NewRecorder()
	s.handleNativeAccount("PANDORA")(w, nativeReq(http.MethodDelete, "/api/pandora/account", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusOK || box.removeCalls != 0 || len(p.cleared) != 1 {
		t.Fatalf("nothing linked: status %d, calls %d, cleared %v", w.Code, box.removeCalls, p.cleared)
	}
}

func TestUSServicesDebugSnapshotReadsBoxSources(t *testing.T) {
	t.Parallel()
	s, _, box := nativeTestServer(true)
	s.regionRes = region.New("", nil)
	s.regionRes.SetBox("US")
	box.sources = []boxapi.Source{{Source: "TUNEIN", Status: "READY"}, {Source: "PANDORA", Status: "UNAVAILABLE"}, {Source: "IHEART", Status: "READY", SourceAccount: "x"}}
	snap, ok := s.USServicesDebugSnapshot().(map[string]any)
	if !ok {
		t.Fatalf("snapshot type %T", s.USServicesDebugSnapshot())
	}
	srcs, _ := snap["boxSources"].(map[string]map[string]string)
	if srcs["PANDORA"]["status"] != "UNAVAILABLE" || srcs["IHEART"]["status"] != "READY" || srcs["IHEART"]["hasAccount"] != "true" {
		t.Fatalf("boxSources = %v", srcs)
	}
	if ri, _ := snap["region"].(region.Info); ri.Country != "US" || ri.Source != "box" {
		t.Fatalf("region = %+v", snap["region"])
	}
	box.sources = nil
	snap = s.USServicesDebugSnapshot().(map[string]any)
	if srcs, _ := snap["boxSources"].(map[string]map[string]string); srcs["IHEART"]["status"] != "absent" {
		t.Fatalf("missing IHEART should read absent, got %v", srcs)
	}
}

func TestNativeBoxSourcesPrefersReadyEntry(t *testing.T) {
	t.Parallel()
	s, _, box := nativeTestServer(true)
	// Two stale Bose-era accounts listed before the working one (#1630).
	box.sources = []boxapi.Source{
		{Source: "PANDORA", Status: "UNAVAILABLE", SourceAccount: "old1", DisplayName: "old one"},
		{Source: "PANDORA", Status: "UNAVAILABLE", SourceAccount: "old2", DisplayName: "old two"},
		{Source: "PANDORA", Status: "READY", SourceAccount: "new", DisplayName: "current"},
	}
	got := s.nativeBoxSources(context.Background())
	if got["PANDORA"]["status"] != "READY" || got["PANDORA"]["displayName"] != "current" || got["PANDORA"]["hasAccount"] != "true" {
		t.Fatalf("PANDORA = %v, want the READY entry", got["PANDORA"])
	}
	// Without a READY one, the first listed entry is reported.
	box.sources = box.sources[:2]
	got = s.nativeBoxSources(context.Background())
	if got["PANDORA"]["status"] != "UNAVAILABLE" || got["PANDORA"]["displayName"] != "old one" {
		t.Fatalf("PANDORA = %v, want the first entry", got["PANDORA"])
	}
	// A READY entry without an account does not win over the first one.
	box.sources = []boxapi.Source{
		{Source: "PANDORA", Status: "UNAVAILABLE", SourceAccount: "old1", DisplayName: "old one"},
		{Source: "PANDORA", Status: "READY", DisplayName: "no account"},
	}
	got = s.nativeBoxSources(context.Background())
	if got["PANDORA"]["displayName"] != "old one" {
		t.Fatalf("PANDORA = %v, want the first entry", got["PANDORA"])
	}
}

func TestAgentVersionReportsRegion(t *testing.T) {
	t.Parallel()
	s := quietServer("127.0.0.1")
	s.regionRes = region.New("", nil)
	get := func() map[string]string {
		w := httptest.NewRecorder()
		s.handleAgentVersion(w, httptest.NewRequest(http.MethodGet, "/api/agent/version", nil))
		var out map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if _, ok := get()["region"]; ok {
		t.Fatal("unknown region should be omitted")
	}
	s.regionRes.SetBox("US")
	if out := get(); out["region"] != "US" || out["regionSource"] != "box" {
		t.Fatalf("box region: %v / %v", out["region"], out["regionSource"])
	}
	s.regionRes.SetSTR("de")
	if out := get(); out["region"] != "DE" || out["regionSource"] != "str" {
		t.Fatalf("str region: %v / %v", out["region"], out["regionSource"])
	}
}

func TestRegionPutFeedsResolver(t *testing.T) {
	t.Parallel()
	s := quietServer("127.0.0.1")
	s.regionRes = region.New("", nil)
	s.regionRes.SetBox("DE")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/region", strings.NewReader(`{"country":"us"}`))
	r.Header.Set("Content-Type", "application/json")
	s.handleRegion(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /api/region: %d %s", w.Code, w.Body.String())
	}
	if ri := s.regionInfo(); ri.Country != "US" || ri.Source != "str" {
		t.Fatalf("resolver not fed: %+v", ri)
	}
}

func TestMaskForLog(t *testing.T) {
	t.Parallel()
	if got := maskForLog("listener@example.com"); got != "li***@example.com" {
		t.Errorf("maskForLog = %q", got)
	}
}
