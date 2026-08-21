package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// liveZoneXML is what the fake firmware's /getZone answers: a zone led by
// master with one member. Requesting exactly that member makes driveZone take
// the "requested group already live" path, so a drive completes quickly and
// deterministically without a /setZone round-trip.
func liveZoneXML(master, memberID, memberIP string) string {
	return `<?xml version="1.0" encoding="UTF-8" ?><zone master="` + master + `">` +
		`<member ipaddress="` + memberIP + `" role="NORMAL">` + memberID + `</member></zone>`
}

func driveTestMembers() (boxapi.ZoneMember, []boxapi.ZoneMember) {
	return boxapi.ZoneMember{DeviceID: "MASTER00DEVICE", IP: "127.0.0.1"},
		[]boxapi.ZoneMember{{DeviceID: "SLAVE0000001", IP: "127.0.0.1"}}
}

// A background drive must never take a sequence number: the coalescer reads
// "my number is no longer the latest" as "a newer user request supersedes
// me", so a background bump would make a settled user request stand down
// against a drive that never meant to compete (see zoneDriveOpts.coalesce).
// The user path taking a number afterwards is the contrast that keeps the
// zero assertion honest.
func TestDriveZoneWithoutCoalesceNeverBumpsTheFormSequence(t *testing.T) {
	fw := startFakeFirmware(t)
	master, slaves := driveTestMembers()
	fw.setZoneXML(liveZoneXML(master.DeviceID, slaves[0].DeviceID, slaves[0].IP))
	s := &Server{logger: discardLogger(), boxHost: "127.0.0.1"}

	res := s.driveZone(context.Background(), master, slaves, "Whole home", "native",
		zoneDriveOpts{coalesce: false, reason: "boot"})
	if res.status != http.StatusOK {
		t.Fatalf("background drive: status = %d (errText %q), want 200 so the assertion covers a full drive", res.status, res.errText)
	}
	if got := s.zoneFormSeq.Load(); got != 0 {
		t.Errorf("zoneFormSeq = %d after a background drive, want 0", got)
	}

	res = s.driveZone(context.Background(), master, slaves, "Whole home", "native",
		zoneDriveOpts{coalesce: true, reason: "form"})
	if res.status != http.StatusOK {
		t.Fatalf("user drive: status = %d (errText %q), want 200", res.status, res.errText)
	}
	if got := s.zoneFormSeq.Load(); got != 1 {
		t.Errorf("zoneFormSeq = %d after a user form, want 1", got)
	}
}

// persist decides whether the drive writes the zone document. Background
// rejoins (wake-rejoin, preplay onto a live zone) run with persist:false so a
// transient constellation never overwrites the group the user asked for;
// forming drives run with persist:true so the reconcile has a record to
// retry from.
func TestDriveZonePersistOptGatesTheStoredDocument(t *testing.T) {
	fw := startFakeFirmware(t)
	master, slaves := driveTestMembers()
	fw.setZoneXML(liveZoneXML(master.DeviceID, slaves[0].DeviceID, slaves[0].IP))

	t.Run("persist false leaves the store untouched", func(t *testing.T) {
		store, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
		if err != nil {
			t.Fatal(err)
		}
		s := &Server{logger: discardLogger(), boxHost: "127.0.0.1", zones: store}
		res := s.driveZone(context.Background(), master, slaves, "Whole home", "native",
			zoneDriveOpts{persist: false, reason: "wake-rejoin"})
		if res.status != http.StatusOK {
			t.Fatalf("status = %d (errText %q), want 200", res.status, res.errText)
		}
		if z, ok := store.Get(); ok {
			t.Errorf("a persist:false drive wrote the zone document: %+v", z)
		}
	})

	t.Run("persist true writes the requested group", func(t *testing.T) {
		store, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
		if err != nil {
			t.Fatal(err)
		}
		s := &Server{logger: discardLogger(), boxHost: "127.0.0.1", zones: store}
		res := s.driveZone(context.Background(), master, slaves, "Whole home", "native",
			zoneDriveOpts{persist: true, reason: "boot"})
		if res.status != http.StatusOK {
			t.Fatalf("status = %d (errText %q), want 200", res.status, res.errText)
		}
		z, ok := store.Get()
		if !ok {
			t.Fatal("a persist:true drive wrote nothing")
		}
		if z.Master != master.DeviceID || z.MasterIP != master.IP || z.Mode != "native" || z.Name != "Whole home" {
			t.Errorf("stored document = %+v, want the requested group", z)
		}
		if len(z.Slaves) != 1 || z.Slaves[0].DeviceID != slaves[0].DeviceID || z.Slaves[0].IP != slaves[0].IP {
			t.Errorf("stored slaves = %+v, want exactly %+v", z.Slaves, slaves)
		}
	})
}

// zoneDriveResult.write must keep handleZoneForm's HTTP answers
// byte-compatible with the pre-split code: errors via http.Error as
// text/plain with the status, success as JSON.
func TestZoneDriveResultWrite(t *testing.T) {
	t.Run("error text answers as plain text with the status", func(t *testing.T) {
		w := httptest.NewRecorder()
		zoneDriveResult{status: http.StatusBadGateway, errText: "setZone: no answer"}.write(w)
		if w.Code != http.StatusBadGateway {
			t.Errorf("status = %d, want 502", w.Code)
		}
		if got := w.Body.String(); got != "setZone: no answer\n" {
			t.Errorf("body = %q, want the http.Error line", got)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("Content-Type = %q, want text/plain", ct)
		}
	})

	t.Run("body answers as JSON with the status", func(t *testing.T) {
		w := httptest.NewRecorder()
		zoneDriveResult{status: http.StatusOK, body: map[string]any{"ok": true, "mode": "native"}}.write(w)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("body is not JSON: %v (%s)", err, w.Body.String())
		}
		if resp["ok"] != true || resp["mode"] != "native" {
			t.Errorf("body = %v, want ok=true mode=native", resp)
		}
	})
}
