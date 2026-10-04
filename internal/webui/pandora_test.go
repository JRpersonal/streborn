package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/marge"
)

type fakePandora struct {
	on      bool
	user    string
	cleared bool
}

func (f *fakePandora) PandoraEnabled() bool            { return f.on }
func (f *fakePandora) SetPandoraEnabled(on bool) error { f.on = on; return nil }
func (f *fakePandora) PandoraUsername() string         { return f.user }
func (f *fakePandora) ClearPandora() error             { f.cleared = true; f.user = ""; return nil }
func (f *fakePandora) PandoraStatusSnapshot() marge.PandoraStatus {
	return marge.PandoraStatus{OptIn: f.on, Registered: f.user != ""}
}

type fakePandoraBox struct {
	setUser, setPass, removedUser string
	sources                       []boxapi.Source
}

func (b *fakePandoraBox) SetMusicServiceAccount(_ context.Context, source, _, user, pass string) error {
	if source == "PANDORA" {
		b.setUser, b.setPass = user, pass
	}
	return nil
}

func (b *fakePandoraBox) RemoveMusicServiceAccount(_ context.Context, _, _, user string) error {
	b.removedUser = user
	return nil
}

func (b *fakePandoraBox) GetSources(context.Context) ([]boxapi.Source, error) {
	return b.sources, nil
}

func pandoraTestServer(on bool) (*Server, *fakePandora, *fakePandoraBox) {
	s := quietServer("127.0.0.1")
	p := &fakePandora{on: on}
	box := &fakePandoraBox{}
	s.pandora = p
	s.pandoraBoxOverride = box
	return s, p, box
}

func pandoraReq(method, path, body, remote, ct string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = remote
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	return r
}

func TestPandoraEndpointsLANOnly(t *testing.T) {
	s, _, _ := pandoraTestServer(true)
	for _, h := range []http.HandlerFunc{s.handlePandora, s.handlePandoraAccount} {
		w := httptest.NewRecorder()
		h(w, pandoraReq(http.MethodPost, "/api/pandora", `{"enabled":true}`, "203.0.113.7:5000", "application/json"))
		if w.Code != http.StatusForbidden {
			t.Errorf("non-LAN caller got %d, want 403", w.Code)
		}
	}
}

func TestPandoraToggleNeedsJSON(t *testing.T) {
	s, p, _ := pandoraTestServer(false)
	w := httptest.NewRecorder()
	s.handlePandora(w, pandoraReq(http.MethodPost, "/api/pandora", `{"enabled":true}`, "192.168.1.5:5000", "application/x-www-form-urlencoded"))
	if w.Code != http.StatusUnsupportedMediaType || p.on {
		t.Fatalf("form post: status %d, on=%v", w.Code, p.on)
	}
	w = httptest.NewRecorder()
	s.handlePandora(w, pandoraReq(http.MethodPost, "/api/pandora", `{"enabled":true}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusOK || !p.on {
		t.Fatalf("json post: status %d, on=%v", w.Code, p.on)
	}
}

func TestPandoraAccountRequiresOptIn(t *testing.T) {
	s, _, box := pandoraTestServer(false)
	w := httptest.NewRecorder()
	s.handlePandoraAccount(w, pandoraReq(http.MethodPost, "/api/pandora/account", `{"user":"u@example.com","password":"pw"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusConflict || box.setUser != "" {
		t.Fatalf("opt-in off: status %d, box got user %q", w.Code, box.setUser)
	}
}

func TestPandoraAccountAddAndRemove(t *testing.T) {
	s, p, box := pandoraTestServer(true)
	w := httptest.NewRecorder()
	s.handlePandoraAccount(w, pandoraReq(http.MethodPost, "/api/pandora/account", `{"user":" u@example.com ","password":"pw"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusOK || box.setUser != "u@example.com" || box.setPass != "pw" {
		t.Fatalf("add: status %d, user %q", w.Code, box.setUser)
	}
	if strings.Contains(w.Body.String(), "pw") {
		t.Fatal("response echoes the password")
	}

	p.user = "u@example.com"
	w = httptest.NewRecorder()
	s.handlePandoraAccount(w, pandoraReq(http.MethodDelete, "/api/pandora/account", "", "192.168.1.5:5000", ""))
	if w.Code != http.StatusOK || box.removedUser != "u@example.com" || !p.cleared {
		t.Fatalf("remove: status %d, removed %q, cleared %v", w.Code, box.removedUser, p.cleared)
	}
}

func TestPandoraDebugSnapshotReadsBoxSource(t *testing.T) {
	s, _, box := pandoraTestServer(true)
	box.sources = []boxapi.Source{{Source: "TUNEIN", Status: "READY"}, {Source: "PANDORA", Status: "UNAVAILABLE"}}
	snap, ok := s.PandoraDebugSnapshot().(map[string]any)
	if !ok {
		t.Fatalf("snapshot type %T", s.PandoraDebugSnapshot())
	}
	src, _ := snap["boxSource"].(map[string]string)
	if src["status"] != "UNAVAILABLE" {
		t.Fatalf("boxSource = %v", src)
	}
	box.sources = nil
	snap = s.PandoraDebugSnapshot().(map[string]any)
	if src, _ := snap["boxSource"].(map[string]string); src["status"] != "absent" {
		t.Fatalf("missing PANDORA should read absent, got %v", src)
	}
}

func TestMaskForLog(t *testing.T) {
	if got := maskForLog("listener@example.com"); got != "li***@example.com" {
		t.Errorf("maskForLog = %q", got)
	}
}
