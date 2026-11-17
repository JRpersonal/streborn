package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/zones"
)

// A temporary group on top of a permanent one sets the permanent one aside,
// and GET /api/box/zone now names it, so a client can say which group a
// permanent:true form would replace instead of asking blindly
// (SoundRebornRemote, 2026-11-17).
func TestZoneGetReportsTheGroupSetAside(t *testing.T) {
	st, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), zones: st}
	if s.setAsideGroup() != nil {
		t.Fatal("an empty store reported a group set aside")
	}
	perm := zones.Zone{Master: "AAA", Name: "Downstairs", Permanent: true,
		Slaves: []zones.Member{{DeviceID: "BBB", IP: "192.0.2.11"}, {DeviceID: "CCC", IP: "192.0.2.12"}}}
	if err := st.Set(perm); err != nil {
		t.Fatal(err)
	}
	if s.setAsideGroup() != nil {
		t.Fatal("the current permanent group was reported as set aside")
	}
	if err := st.Set(zones.Zone{Master: "AAA", Slaves: []zones.Member{{DeviceID: "DDD", IP: "192.0.2.13"}}}); err != nil {
		t.Fatal(err)
	}
	got := s.setAsideGroup()
	if got == nil || got.Master != "AAA" || got.Name != "Downstairs" || len(got.Members) != 2 ||
		got.Members[0].IP != "192.0.2.11" || got.Members[1].DeviceID != "CCC" {
		t.Fatalf("set-aside group = %+v, want Downstairs with its two members", got)
	}
	b, _ := json.Marshal(zoneAnswer{SetAside: got})
	if !strings.Contains(string(b), `"setAside":{"master":"AAA","name":"Downstairs","members":[`) {
		t.Fatalf("JSON shape: %s", b)
	}
	b, _ = json.Marshal(zoneAnswer{})
	if strings.Contains(string(b), "setAside") {
		t.Fatalf("an answer without a set-aside group carries the field: %s", b)
	}
	// The temporary group ends: the permanent one is current again, nothing aside.
	if err := st.Clear(); err != nil {
		t.Fatal(err)
	}
	if s.setAsideGroup() != nil {
		t.Fatal("still reported as set aside after it came back")
	}
}
