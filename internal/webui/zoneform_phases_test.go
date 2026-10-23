package webui

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// The phases of POST /api/box/zone (handleZoneForm), the pure ones, and the
// wire of every exit reachable without a speaker.
//
// Three clients read these bodies verbatim: the desktop app's FormZone
// (ok, missing, unverifiable, the plain-text errors), the phone remote, and
// the groupkeys client, which requires JSON with ok, error and deferred and
// never a 404. A change to any line of TestHandleZoneFormWire is a change to
// what they see.

func TestZoneFormStartNormalisesTheRequest(t *testing.T) {
	t.Parallel()
	s := &Server{logger: slog.New(slog.DiscardHandler)}

	_, ans := s.zoneFormStart(zoneFormReq{Master: zoneMemberReq{DeviceID: "M", IP: "192.0.2.1"}})
	if ans == nil || ans.Status != http.StatusBadRequest || ans.Text != "at least one slave is required" {
		t.Fatalf("no members: %+v, want a plain-text 400", ans)
	}

	run, ans := s.zoneFormStart(zoneFormReq{
		Master: zoneMemberReq{DeviceID: "M", IP: "192.0.2.1"},
		Slaves: []zoneMemberReq{{DeviceID: "A", IP: "192.0.2.2"}, {DeviceID: "B", IP: ""}},
		Mode:   "something-else",
	})
	if ans != nil {
		t.Fatalf("refused: %+v", ans)
	}
	if run.mode != "native" {
		t.Errorf("mode = %q, want native for anything but mirror", run.mode)
	}
	if run.master != (boxapi.ZoneMember{DeviceID: "M", IP: "192.0.2.1"}) {
		t.Errorf("master = %+v", run.master)
	}
	if len(run.slaves) != 2 || run.slaves[0].DeviceID != "A" || run.slaves[1].IP != "" {
		t.Errorf("slaves = %+v", run.slaves)
	}
	run, _ = s.zoneFormStart(zoneFormReq{Slaves: []zoneMemberReq{{DeviceID: "A"}}, Mode: "mirror"})
	if run.mode != "mirror" {
		t.Errorf("mode = %q, want mirror", run.mode)
	}
}

func TestZoneVerdictCountsWhatTheMasterAndTheFollowersSaid(t *testing.T) {
	t.Parallel()
	slaves := []boxapi.ZoneMember{{DeviceID: "AAA", IP: "192.0.2.2"}, {DeviceID: "BBB", IP: "192.0.2.3"}, {DeviceID: "CCC", IP: "192.0.2.4"}}
	live := boxapi.Zone{Master: "MASTER", SenderIP: "192.0.2.1", Members: []boxapi.ZoneMember{
		{DeviceID: "aaa", IP: "192.0.2.2"}, {DeviceID: "BBB", IP: "192.0.2.3"},
	}}
	f := zoneVerdict(live, slaves, []string{"CCC"}, []string{"BBB"}, false)
	if !f.MasterFormed || !f.ok() || f.Verified != 2 || f.Requested != 3 {
		t.Errorf("verdict = %+v, want formed, ok, 2 of 3 verified", f)
	}
	// The master's list is matched case-insensitively, so only CCC is missing
	// from it.
	if len(f.MasterMissing) != 1 || f.MasterMissing[0] != "CCC" {
		t.Errorf("masterMissing = %v, want [CCC]", f.MasterMissing)
	}

	// An empty read-back is the 0.8.x regression shape: not formed, not ok.
	empty := zoneVerdict(boxapi.Zone{}, slaves, []string{"AAA", "BBB", "CCC"}, nil, false)
	if empty.MasterFormed || empty.ok() || empty.Verified != 0 || len(empty.MasterMissing) != 3 {
		t.Errorf("empty read-back verdict = %+v", empty)
	}
}

// The final body, key for key. ok:false carries no error field; the readBack
// marker appears only when the speaker never confirmed its own zone.
func TestZoneFormedAnswerIsTheOldBody(t *testing.T) {
	t.Parallel()
	slaves := []boxapi.ZoneMember{{DeviceID: "AAA", IP: "192.0.2.2"}}
	f := zoneVerdict(boxapi.Zone{Master: "MASTER", SenderIP: "192.0.2.1", Members: slaves}, slaves, []string{}, []string{}, false)
	w := httptest.NewRecorder()
	writeZoneFormAnswer(w, f.answer())
	want := `{"master":"MASTER","masterMissing":[],"members":[{"deviceID":"AAA","ip":"192.0.2.2"}],"missing":[],"mode":"native","ok":true,"requested":1,"senderIP":"192.0.2.1","unverifiable":[],"verified":1}` + "\n"
	if w.Code != 200 || w.Body.String() != want {
		t.Errorf("body = %d %s\nwant %s", w.Code, w.Body.String(), want)
	}

	failed := zoneVerdict(boxapi.Zone{Master: "MASTER", Members: slaves}, slaves, []string{"AAA"}, []string{}, true)
	var body map[string]any
	if err := json.Unmarshal([]byte(jsonBody(t, failed.answer())), &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != false || body["readBack"] != "failed" {
		t.Errorf("failed read-back body = %v", body)
	}
	if _, has := body["error"]; has {
		t.Error("ok:false grew an error field; the clients do not expect one")
	}
}

func jsonBody(t *testing.T, a *zoneFormAnswer) string {
	t.Helper()
	w := httptest.NewRecorder()
	writeZoneFormAnswer(w, a)
	return w.Body.String()
}

func TestWriteZoneFormAnswerRendersTextAndJSON(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	writeZoneFormAnswer(w, zoneFormRefusal(http.StatusBadGateway, "setZone: boom"))
	if w.Code != 502 || w.Body.String() != "setZone: boom\n" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("text refusal rendered as %d %q (%s)", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	w = httptest.NewRecorder()
	writeZoneFormAnswer(w, zoneFormOK(map[string]any{"ok": true, "mode": "mirror"}))
	if w.Code != 200 || w.Body.String() != `{"mode":"mirror","ok":true}`+"\n" {
		t.Errorf("json answer rendered as %d %q", w.Code, w.Body.String())
	}
}

// The define-only phase on its own: a template is stored and nothing else
// happens; a live request hands over.
func TestZoneFormDefineOnlyStoresTheTemplateOrHandsOver(t *testing.T) {
	t.Parallel()
	zs, err := zones.Load(filepath.Join(t.TempDir(), "zone.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{logger: slog.New(slog.DiscardHandler), zones: zs}
	run := &zoneFormRun{
		req:    zoneFormReq{Permanent: true, DefineOnly: true, Name: "Haus"},
		mode:   "native",
		master: boxapi.ZoneMember{DeviceID: "MASTER", IP: "192.0.2.1"},
		slaves: []boxapi.ZoneMember{{DeviceID: "AAA", IP: "192.0.2.2"}},
	}
	ans := s.zoneFormDefineOnly(run)
	if ans == nil || jsonBody(t, ans) != `{"defined":true,"mode":"native","ok":true,"permanent":true}`+"\n" {
		t.Fatalf("define-only answer = %+v", ans)
	}
	if z, ok := zs.Get(); !ok || !z.Permanent || z.Name != "Haus" || len(z.Slaves) != 1 {
		t.Errorf("stored template = %+v, %v", z, ok)
	}
	run.req.DefineOnly = false
	if ans := s.zoneFormDefineOnly(run); ans != nil {
		t.Errorf("a live form was answered by the define-only phase: %+v", ans)
	}
	run.req.DefineOnly, run.req.Stereo = true, true
	if ans := s.zoneFormDefineOnly(run); ans != nil {
		t.Errorf("a stereo request was answered by the define-only phase: %+v", ans)
	}
}

// The wire of POST /api/box/zone for every exit reachable with no speaker
// behind boxHost (a dead port, so every firmware read fails fast).
func TestHandleZoneFormWire(t *testing.T) {
	t.Parallel()
	newServer := func(t *testing.T) *Server {
		zs, err := zones.Load(filepath.Join(t.TempDir(), "zone.json"))
		if err != nil {
			t.Fatal(err)
		}
		return &Server{boxHost: "127.0.0.1:1", zones: zs, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	}
	const members = `"master":{"deviceID":"MASTER","ip":"127.0.0.1"},"slaves":[{"deviceID":"AAA","ip":""}]`
	cases := []struct {
		name     string
		body     string
		canceled bool
		status   int
		want     string // exact body, or a prefix when wantPrefix is set
		prefix   bool
	}{
		{"bad json", `{"master":`, false, http.StatusBadRequest, "bad json: unexpected EOF\n", false},
		{"no members", `{"master":{"deviceID":"MASTER"},"slaves":[]}`, false, http.StatusBadRequest, "at least one slave is required\n", false},
		{"define only", `{` + members + `,"permanent":true,"defineOnly":true}`, false, http.StatusOK,
			`{"defined":true,"mode":"native","ok":true,"permanent":true}` + "\n", false},
		{"caller gave up during the settle", `{` + members + `}`, true, http.StatusRequestTimeout, "canceled\n", false},
		{"leader not answering", `{` + members + `}`, false, http.StatusBadGateway, "the speaker leading the group is not answering: ", true},
		{"permanent mirror group deferred", `{` + members + `,"permanent":true,"mode":"mirror"}`, false, http.StatusOK,
			`{"deferred":true,"mode":"mirror","ok":true}` + "\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newServer(t)
			r := httptest.NewRequest(http.MethodPost, "/api/box/zone", strings.NewReader(c.body))
			if c.canceled {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			w := httptest.NewRecorder()
			s.handleZoneForm(w, r)
			if w.Code != c.status {
				t.Errorf("status = %d, want %d (body %s)", w.Code, c.status, w.Body.String())
			}
			got := w.Body.String()
			if c.prefix && !strings.HasPrefix(got, c.want) {
				t.Errorf("body = %q, want prefix %q", got, c.want)
			}
			if !c.prefix && got != c.want {
				t.Errorf("body = %q, want %q", got, c.want)
			}
		})
	}
}
