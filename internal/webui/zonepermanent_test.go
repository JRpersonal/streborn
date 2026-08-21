package webui

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zonetemplates"
)

// fakeFirmware stands in for the Bose firmware on :8090. boxapi and
// fetchNowPlaying both hardcode that port (an httptest-picked port would
// yield the invalid http://host:port:8090 URL, see status_stale_test.go), so
// the fake binds the real port on loopback and every speaker in a test
// carries the IP 127.0.0.1. A host where :8090 is already taken skips the
// test rather than failing it.
type fakeFirmware struct {
	mu         sync.Mutex
	deviceID   string // answered by GET /info
	nowPlaying string // body for GET /now_playing
	zone       string // body for GET /getZone
	hits       map[string]int
	srv        *httptest.Server
}

func startFakeFirmware(t *testing.T) *fakeFirmware {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:8090")
	if err != nil {
		t.Skipf("cannot bind the firmware port 127.0.0.1:8090 on this host: %v", err)
	}
	f := &fakeFirmware{deviceID: "MASTER00DEVICE", hits: make(map[string]int)}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(f.handle))
	f.srv.Listener.Close()
	f.srv.Listener = ln
	f.srv.Start()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeFirmware) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits[r.URL.Path]++
	switch r.URL.Path {
	case "/info":
		fmt.Fprintf(w, `<?xml version="1.0"?><info deviceID="%s"><name>Test Box</name><type>SoundTouch 10</type></info>`, f.deviceID)
	case "/now_playing":
		_, _ = w.Write([]byte(f.nowPlaying))
	case "/getZone":
		_, _ = w.Write([]byte(f.zone))
	case "/setZone", "/addZoneSlave", "/removeZoneSlave":
		_, _ = w.Write([]byte(`<status>OK</status>`))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeFirmware) setDeviceID(id string) {
	f.mu.Lock()
	f.deviceID = id
	f.mu.Unlock()
}

func (f *fakeFirmware) setNowPlaying(xml string) {
	f.mu.Lock()
	f.nowPlaying = xml
	f.mu.Unlock()
}

func (f *fakeFirmware) setZoneXML(xml string) {
	f.mu.Lock()
	f.zone = xml
	f.mu.Unlock()
}

func (f *fakeFirmware) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func nowPlayingXML(source, status string) string {
	return `<nowPlaying source="` + source + `">` +
		`<ContentItem location="http://127.0.0.1:8888/stream/1"/>` +
		`<playStatus>` + status + `</playStatus></nowPlaying>`
}

// permStore builds a store holding one permanent template named "Whole home".
func permStore(t *testing.T, master zonetemplates.Member, members ...zonetemplates.Member) *zonetemplates.Store {
	t.Helper()
	store, err := zonetemplates.Load(filepath.Join(t.TempDir(), "zone-templates.json"))
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := store.Upsert(zonetemplates.Template{Name: "Whole home", Master: master, Members: members})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPermanent(tpl.ID, true); err != nil {
		t.Fatal(err)
	}
	return store
}

// classifyPermMember is the permanent engine's whole safety doctrine in one
// switch: an unreadable box is deep sleep (skip, no retry), STANDBY must not
// be enrolled (that would wake it), a box playing its own source stays out,
// and only an awake idle box may be joined.
func TestClassifyPermMemberDecisionTable(t *testing.T) {
	cases := []struct {
		name string
		np   nowPlayingSnapshot
		want permMemberState
	}{
		{"unreadable box (deep sleep or off network)", nowPlayingSnapshot{}, permUnreachable},
		{"standby box answers STANDBY without waking", nowPlayingSnapshot{Source: "STANDBY"}, permStandby},
		{"box playing its own source", nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "PLAY_STATE"}, permSelfPlaying},
		{"buffering counts as playing", nowPlayingSnapshot{Source: "SPOTIFY", PlayStatus: "BUFFERING_STATE"}, permSelfPlaying},
		{"awake and stopped is joinable", nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "STOP_STATE"}, permJoinable},
		{"awake with no playStatus is joinable", nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO"}, permJoinable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fetch := func(context.Context, string) nowPlayingSnapshot { return tc.np }
			m := zonetemplates.Member{DeviceID: "DEV-A", IP: "192.0.2.21"}
			if got := classifyPermMember(context.Background(), m, fetch); got != tc.want {
				t.Errorf("classifyPermMember(%+v) = %v, want %v", tc.np, got, tc.want)
			}
		})
	}
}

// A member with no IP has nothing to probe; guessing an address could reach
// the wrong speaker, so it is unreachable WITHOUT a fetch.
func TestClassifyPermMemberWithoutIPNeverFetches(t *testing.T) {
	calls := 0
	fetch := func(context.Context, string) nowPlayingSnapshot {
		calls++
		return nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO"}
	}
	got := classifyPermMember(context.Background(), zonetemplates.Member{DeviceID: "DEV-A"}, fetch)
	if got != permUnreachable {
		t.Errorf("state = %v, want permUnreachable", got)
	}
	if calls != 0 {
		t.Errorf("fetch called %d time(s) for a member with no IP, want 0", calls)
	}
}

// permEligibleMembers probes via the real fetchNowPlaying (no seam), so the
// fake firmware serves the actual :8090. The property under test: a member on
// the out list is skipped WITHOUT a probe. The control case runs first and
// proves the probe does reach the firmware when the member is not out, so a
// zero request count afterwards means "not asked", not "could not ask".
func TestPermMemberEligibilityHonorsTheOutList(t *testing.T) {
	fw := startFakeFirmware(t)
	fw.setNowPlaying(nowPlayingXML("LOCAL_INTERNET_RADIO", "STOP_STATE"))
	master := zonetemplates.Member{DeviceID: "MASTER00DEVICE", IP: "192.0.2.10"}
	member := zonetemplates.Member{DeviceID: "DEV-A", IP: "127.0.0.1"}

	t.Run("a member not on the out list is probed and joinable", func(t *testing.T) {
		s := &Server{logger: discardLogger(), tpls: permStore(t, master, member)}
		eligible, skipped := s.permEligibleMembers(context.Background(), []zonetemplates.Member{member})
		if len(eligible) != 1 || eligible[0].DeviceID != "DEV-A" || skipped != 0 {
			t.Fatalf("eligible = %+v, skipped = %d, want DEV-A eligible and nobody skipped", eligible, skipped)
		}
		if fw.count("/now_playing") == 0 {
			t.Fatal("the fake firmware saw no probe; the control this test depends on is broken")
		}
	})

	t.Run("a member on the out list is skipped without a probe", func(t *testing.T) {
		s := &Server{logger: discardLogger(), tpls: permStore(t, master, member)}
		if err := s.tpls.AddOut(member.DeviceID, member.IP, "user-removed"); err != nil {
			t.Fatal(err)
		}
		before := fw.count("/now_playing")
		eligible, skipped := s.permEligibleMembers(context.Background(), []zonetemplates.Member{member})
		if len(eligible) != 0 || skipped != 1 {
			t.Fatalf("eligible = %+v, skipped = %d, want nobody eligible and one skipped", eligible, skipped)
		}
		if got := fw.count("/now_playing"); got != before {
			t.Errorf("an out member was probed anyway (%d extra request(s))", got-before)
		}
	})

	t.Run("a member playing its own source is recorded as out", func(t *testing.T) {
		fw.setNowPlaying(nowPlayingXML("SPOTIFY", "PLAY_STATE"))
		s := &Server{logger: discardLogger(), tpls: permStore(t, master, member)}
		eligible, skipped := s.permEligibleMembers(context.Background(), []zonetemplates.Member{member})
		if len(eligible) != 0 || skipped != 1 {
			t.Fatalf("eligible = %+v, skipped = %d, want the self-playing member skipped", eligible, skipped)
		}
		out := s.tpls.OutList()
		if len(out) != 1 || out[0].DeviceID != "DEV-A" || out[0].Reason != "self-play" {
			t.Errorf("out = %+v, want exactly DEV-A with reason self-play", out)
		}
	})
}

// The out list follows the user's explicit group edits: a smaller form marks
// the dropped template members out, a form that re-adds one clears its entry,
// and edits on other groups (no permanent template, different master) must
// not touch it.
func TestNotePermanentMembershipTracksTheUsersGroupEdits(t *testing.T) {
	master := zonetemplates.Member{DeviceID: "MASTER00DEVICE", IP: "192.0.2.10"}
	a := zonetemplates.Member{DeviceID: "DEV-A", IP: "192.0.2.21"}
	b := zonetemplates.Member{DeviceID: "DEV-B", IP: "192.0.2.22"}
	masterZM := boxapi.ZoneMember{DeviceID: master.DeviceID, IP: master.IP}
	slaveA := boxapi.ZoneMember{DeviceID: a.DeviceID, IP: a.IP}
	slaveB := boxapi.ZoneMember{DeviceID: b.DeviceID, IP: b.IP}

	t.Run("a form without a template member marks it out, re-adding clears it", func(t *testing.T) {
		s := &Server{logger: discardLogger(), tpls: permStore(t, master, a, b)}
		s.notePermanentMembership(masterZM, []boxapi.ZoneMember{slaveA})
		if s.tpls.IsOut(a.DeviceID, a.IP) {
			t.Error("the member the form kept was marked out")
		}
		out := s.tpls.OutList()
		if len(out) != 1 || out[0].DeviceID != "DEV-B" || out[0].Reason != "user-removed" {
			t.Fatalf("out = %+v, want exactly DEV-B with reason user-removed", out)
		}

		s.notePermanentMembership(masterZM, []boxapi.ZoneMember{slaveA, slaveB})
		if got := s.tpls.OutList(); len(got) != 0 {
			t.Errorf("out = %+v after the form re-added DEV-B, want empty", got)
		}
	})

	t.Run("without a permanent template no edit is an exclusion", func(t *testing.T) {
		store, err := zonetemplates.Load(filepath.Join(t.TempDir(), "zone-templates.json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Upsert(zonetemplates.Template{Name: "Kitchen pair", Master: master, Members: []zonetemplates.Member{a, b}}); err != nil {
			t.Fatal(err)
		}
		s := &Server{logger: discardLogger(), tpls: store}
		s.notePermanentMembership(masterZM, []boxapi.ZoneMember{slaveA})
		if got := store.OutList(); len(got) != 0 {
			t.Errorf("out = %+v, want untouched (no permanent template)", got)
		}
	})

	t.Run("a form led by a different master is not this group's edit", func(t *testing.T) {
		s := &Server{logger: discardLogger(), tpls: permStore(t, master, a, b)}
		s.notePermanentMembership(boxapi.ZoneMember{DeviceID: "SOMEOTHERBOX"}, []boxapi.ZoneMember{slaveA})
		if got := s.tpls.OutList(); len(got) != 0 {
			t.Errorf("out = %+v, want untouched (different master)", got)
		}
	})
}

// ZoneTemplatesDebug feeds the diagnostic bundle; a field bundle that lies
// about the engine state costs a support round-trip.
func TestZoneTemplatesDebug(t *testing.T) {
	t.Run("no store means disabled", func(t *testing.T) {
		s := &Server{logger: discardLogger()}
		dbg, ok := s.ZoneTemplatesDebug().(map[string]any)
		if !ok {
			t.Fatalf("debug payload is %T, want map[string]any", s.ZoneTemplatesDebug())
		}
		if dbg["enabled"] != false {
			t.Errorf("enabled = %v with no store, want false", dbg["enabled"])
		}
	})

	t.Run("with a store the engine state is visible", func(t *testing.T) {
		master := zonetemplates.Member{DeviceID: "MASTER00DEVICE", IP: "192.0.2.10"}
		a := zonetemplates.Member{DeviceID: "DEV-A", IP: "192.0.2.21"}
		store := permStore(t, master, a) // "Whole home", permanent
		if _, err := store.Upsert(zonetemplates.Template{Name: "Kitchen pair", Master: master, Members: []zonetemplates.Member{a}}); err != nil {
			t.Fatal(err)
		}
		if err := store.AddOut("DEV-A", "192.0.2.21", "self-play"); err != nil {
			t.Fatal(err)
		}
		s := &Server{logger: discardLogger(), tpls: store}
		s.permBootDone.Store(true)

		dbg, ok := s.ZoneTemplatesDebug().(map[string]any)
		if !ok {
			t.Fatalf("debug payload is %T, want map[string]any", s.ZoneTemplatesDebug())
		}
		if dbg["enabled"] != true {
			t.Errorf("enabled = %v, want true", dbg["enabled"])
		}
		names, ok := dbg["templates"].([]string)
		if !ok || len(names) != 2 || names[0] != "Kitchen pair" || names[1] != "Whole home" {
			t.Errorf("templates = %v, want [Kitchen pair Whole home]", dbg["templates"])
		}
		if dbg["permanent"] != "Whole home" {
			t.Errorf("permanent = %v, want Whole home", dbg["permanent"])
		}
		out, ok := dbg["out"].([]zonetemplates.OutEntry)
		if !ok || len(out) != 1 || out[0].DeviceID != "DEV-A" || out[0].Reason != "self-play" {
			t.Errorf("out = %v, want the one self-play entry", dbg["out"])
		}
		if dbg["bootReformDone"] != true {
			t.Errorf("bootReformDone = %v, want true", dbg["bootReformDone"])
		}
	})
}

// permTarget guards every drive the engine can start: only the box the
// permanent template names as MASTER may act. The deviceID comparison runs
// against the firmware's own /info answer, case-insensitively, because
// deviceIDs arrive mixed-case from different chassis.
func TestPermTarget(t *testing.T) {
	master := zonetemplates.Member{DeviceID: "MASTER00DEVICE", IP: "127.0.0.1"}
	a := zonetemplates.Member{DeviceID: "DEV-A", IP: "192.0.2.21"}

	t.Run("no template store", func(t *testing.T) {
		s := &Server{logger: discardLogger(), boxHost: "127.0.0.1"}
		if _, ok := s.permTarget(context.Background()); ok {
			t.Error("permTarget said true with no template store")
		}
	})

	t.Run("no permanent template", func(t *testing.T) {
		store, err := zonetemplates.Load(filepath.Join(t.TempDir(), "zone-templates.json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Upsert(zonetemplates.Template{Name: "Kitchen pair", Master: master, Members: []zonetemplates.Member{a}}); err != nil {
			t.Fatal(err)
		}
		s := &Server{logger: discardLogger(), boxHost: "127.0.0.1", tpls: store}
		if _, ok := s.permTarget(context.Background()); ok {
			t.Error("permTarget said true although no template is permanent")
		}
	})

	t.Run("a box that is not the template's master never drives", func(t *testing.T) {
		fw := startFakeFirmware(t)
		fw.setDeviceID("FOLLOWER0DEVICE") // this box's own firmware identity
		s := &Server{logger: discardLogger(), boxHost: "127.0.0.1", tpls: permStore(t, master, a)}
		if _, ok := s.permTarget(context.Background()); ok {
			t.Error("a box that is not the template's master claimed the permanent group")
		}
	})

	t.Run("the template's master gets its template, case-insensitively", func(t *testing.T) {
		fw := startFakeFirmware(t)
		fw.setDeviceID("master00device") // firmware case differs from the stored ID
		s := &Server{logger: discardLogger(), boxHost: "127.0.0.1", tpls: permStore(t, master, a)}
		tpl, ok := s.permTarget(context.Background())
		if !ok {
			t.Fatal("the template's master did not get its permanent template")
		}
		if tpl.Name != "Whole home" {
			t.Errorf("template = %q, want Whole home", tpl.Name)
		}
	})
}
