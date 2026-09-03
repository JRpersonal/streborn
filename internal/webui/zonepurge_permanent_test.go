package webui

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/zones"
)

// A peer's purge must never clear a PERMANENT group's persisted zone: during a
// fleet update every member reboots at nearly the same time, and a peer that is
// merely between boots can POST a purge that references this master while
// nothing was actually torn down. The persisted document is the only thing the
// play-reform rebuilds from, so clearing it here lost users' groups for good.
// Only the ad-hoc (non-permanent) zone may be cleared by a peer's dissolve.
func TestHandleZonePurgeKeepsPermanentGroup(t *testing.T) {
	newServer := func(t *testing.T, permanent bool) (*Server, *zones.Store) {
		t.Helper()
		store, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Set(zones.Zone{
			Master: "AABBCCDDEEFF", MasterIP: "192.168.1.50", Mode: "mirror",
			Permanent: permanent,
			Slaves:    []zones.Member{{DeviceID: "112233445566", IP: "192.168.1.60"}},
		}); err != nil {
			t.Fatal(err)
		}
		return &Server{
			zones:  store,
			logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}, store
	}
	purge := func(t *testing.T, s *Server) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/box/zone/purge",
			strings.NewReader(`{"deviceID":"112233445566","ip":"192.168.1.60"}`))
		w := httptest.NewRecorder()
		s.handleZonePurge(w, req)
		if w.Code != 200 {
			t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
		}
		return w
	}

	t.Run("permanent zone survives a referenced peer's purge", func(t *testing.T) {
		s, store := newServer(t, true)
		w := purge(t, s)
		if !strings.Contains(w.Body.String(), `"cleared":false`) {
			t.Errorf("body = %s, want cleared:false", w.Body.String())
		}
		z, ok := store.Get()
		if !ok {
			t.Fatal("permanent zone was wiped by a peer's purge; a fleet update would lose the user's group")
		}
		if !z.Permanent {
			t.Errorf("surviving zone lost its permanent flag: %+v", z)
		}
	})

	t.Run("equivalent ad-hoc zone is still cleared", func(t *testing.T) {
		s, store := newServer(t, false)
		w := purge(t, s)
		if !strings.Contains(w.Body.String(), `"cleared":true`) {
			t.Errorf("body = %s, want cleared:true", w.Body.String())
		}
		if _, ok := store.Get(); ok {
			t.Error("ad-hoc zone still persisted after a referenced peer's purge")
		}
	})
}
