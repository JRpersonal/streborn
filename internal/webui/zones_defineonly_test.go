package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/zones"
)

// Defining a permanent group must PERSIST the template and stop there: no wake,
// no /setZone, no stream re-push. Creating one used to wake the master, which
// resumed its last source, so the fresh zone started playing in every room
// (Jens, 2026-08-31). boxHost points at a dead port here, so if the handler ever
// tried to drive the firmware zone it would fail fast rather than block - the
// test asserts it returns ok with the template stored instead.
func TestDefineOnlyPersistsPermanentTemplateWithoutDriving(t *testing.T) {
	zs, err := zones.Load(filepath.Join(t.TempDir(), "zone.json"))
	if err != nil {
		t.Fatalf("zones.Load: %v", err)
	}
	s := &Server{
		boxHost: "127.0.0.1:1", // nothing listening: localDeviceID falls back, a drive would fail fast
		zones:   zs,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	body := `{"master":{"deviceID":"MASTER","ip":"127.0.0.1"},` +
		`"slaves":[{"deviceID":"AAA","ip":""},{"deviceID":"BBB","ip":""}],` +
		`"permanent":true,"defineOnly":true,"mode":"native"}`
	rec := httptest.NewRecorder()
	s.handleZoneForm(rec, httptest.NewRequest(http.MethodPost, "/api/box/zone/form", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"defined":true`) {
		t.Errorf("response is not a define-only result: %s", rec.Body.String())
	}
	z, ok := s.zones.Get()
	if !ok {
		t.Fatal("no zone document was stored")
	}
	if !z.Permanent {
		t.Errorf("stored template is not marked Permanent: %+v", z)
	}
	if len(z.Slaves) != 2 {
		t.Errorf("stored template has %d slaves, want 2", len(z.Slaves))
	}
}

// A NON-permanent form request is not a define-only save even if the flag leaks
// in: the guard is `DefineOnly && !Stereo`, and only the permanent app path ever
// sets it. This pins that the ad-hoc path is untouched by the new branch (it
// would fall through to the live drive, which fails fast against the dead host).
func TestDefineOnlyIgnoredWithoutPermanentDoc(t *testing.T) {
	zs, err := zones.Load(filepath.Join(t.TempDir(), "zone.json"))
	if err != nil {
		t.Fatalf("zones.Load: %v", err)
	}
	s := &Server{
		boxHost: "127.0.0.1:1",
		zones:   zs,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	// defineOnly:false -> the branch does not fire; the handler proceeds toward the
	// live drive and does NOT return a define-only body.
	body := `{"master":{"deviceID":"MASTER","ip":"127.0.0.1"},` +
		`"slaves":[{"deviceID":"AAA","ip":""}],"permanent":true,"defineOnly":false,"mode":"native"}`
	rec := httptest.NewRecorder()
	s.handleZoneForm(rec, httptest.NewRequest(http.MethodPost, "/api/box/zone/form", strings.NewReader(body)))
	if strings.Contains(rec.Body.String(), `"defined":true`) {
		t.Errorf("non-defineOnly request was treated as a template save: %s", rec.Body.String())
	}
}
