package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/region"
)

type fakeNative struct {
	on      bool
	us      bool
	users   map[string]string
	cleared []string
}

func (f *fakeNative) PandoraEnabled() bool               { return f.on }
func (f *fakeNative) SetPandoraEnabled(on bool) error    { f.on = on; return nil }
func (f *fakeNative) NativeServiceEnabled(_ string) bool { return f.on || f.us }
func (f *fakeNative) NativeUsername(typ string) string   { return f.users[typ] }
func (f *fakeNative) ClearNative(typ string) error {
	f.cleared = append(f.cleared, typ)
	delete(f.users, typ)
	return nil
}
func (f *fakeNative) NativeStatusSnapshot() marge.NativeServicesStatus {
	return marge.NativeServicesStatus{OptIn: f.on, US: f.us}
}

type fakeNativeBox struct {
	setSource, setDisplay, setUser, setPass string
	removedSource, removedUser              string
	sources                                 []boxapi.Source
}

func (b *fakeNativeBox) SetMusicServiceAccount(_ context.Context, source, display, user, pass string) error {
	b.setSource, b.setDisplay, b.setUser, b.setPass = source, display, user, pass
	return nil
}

func (b *fakeNativeBox) RemoveMusicServiceAccount(_ context.Context, source, _, user string) error {
	b.removedSource, b.removedUser = source, user
	return nil
}

func (b *fakeNativeBox) GetSources(context.Context) ([]boxapi.Source, error) {
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
	s, _, box := nativeTestServer(false)
	w := httptest.NewRecorder()
	s.handleNativeAccount("IHEART")(w, nativeReq(http.MethodPost, "/api/iheart/account", `{"user":"u@example.com","password":"pw"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusConflict || box.setUser != "" {
		t.Fatalf("service off: status %d, box got user %q", w.Code, box.setUser)
	}
}

func TestNativeAccountOnUSSpeakerNeedsNoOptIn(t *testing.T) {
	s, p, box := nativeTestServer(false)
	p.us = true
	w := httptest.NewRecorder()
	s.handleNativeAccount("IHEART")(w, nativeReq(http.MethodPost, "/api/iheart/account", `{"user":"u@example.com","password":"pw"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusOK || box.setSource != "IHEART" || box.setDisplay != "iHeartRadio" {
		t.Fatalf("US speaker: status %d, source %q display %q", w.Code, box.setSource, box.setDisplay)
	}
}

func TestPandoraAccountAddAndRemove(t *testing.T) {
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

func TestUSServicesDebugSnapshotReadsBoxSources(t *testing.T) {
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

func TestAgentVersionReportsRegion(t *testing.T) {
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
	if got := maskForLog("listener@example.com"); got != "li***@example.com" {
		t.Errorf("maskForLog = %q", got)
	}
}
