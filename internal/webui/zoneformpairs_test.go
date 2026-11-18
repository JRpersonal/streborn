package webui

// A stereo pair in a multiroom group (zoneformpairs.go): the pair joins as one
// member by its pair master, never leads, and a stereo flag that came with a
// group extend no longer re-pairs the speakers.
//
// Every firmware read goes through the fetchGroupFn seam, so nothing here
// needs a speaker on :8090.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

const (
	pgLeaderIP = "192.0.2.10" // a SoundTouch 30, the group's leader
	pgLeftIP   = "192.0.2.19" // the pair master (LEFT)
	pgRightIP  = "192.0.2.11" // the pair's other half (RIGHT)
	pgOtherIP  = "192.0.2.30" // an unpaired speaker
	pgLeaderID = "AAAAAAAAAA10"
	pgLeftID   = "AAAAAAAAAA19"
	pgRightID  = "AAAAAAAAAA11"
	pgOtherID  = "AAAAAAAAAA30"
)

// The pair document both halves report, as the field bundle showed it.
func pgPair() boxapi.Group {
	return boxapi.Group{
		ID: "str-grp-" + pgLeftID, Name: "Kitchen", MasterDeviceID: pgLeftID, Status: "GROUP_OK",
		Members: []boxapi.ZoneMember{
			{DeviceID: pgLeftID, IP: pgLeftIP, Role: "LEFT"},
			{DeviceID: pgRightID, IP: pgRightIP, Role: "RIGHT"},
		},
	}
}

// pgServer is a speaker at host whose pair reads answer from pairs (by host);
// a host not in the map answers an empty group, as an unpaired speaker does.
func pgServer(t *testing.T, host string, pairs map[string]boxapi.Group) *Server {
	t.Helper()
	s := &Server{boxHost: host, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.fetchGroupFn = func(_ context.Context, h string) (boxapi.Group, error) {
		return pairs[h], nil
	}
	return s
}

func pgRun(master boxapi.ZoneMember, stereo bool, slaves ...boxapi.ZoneMember) *zoneFormRun {
	return &zoneFormRun{
		req:    zoneFormReq{Stereo: stereo},
		mode:   "native",
		master: master,
		slaves: slaves,
		ctx:    context.Background(),
	}
}

var (
	pgLeader = boxapi.ZoneMember{DeviceID: pgLeaderID, IP: pgLeaderIP}
	pgLeft   = boxapi.ZoneMember{DeviceID: pgLeftID, IP: pgLeftIP}
	pgRight  = boxapi.ZoneMember{DeviceID: pgRightID, IP: pgRightIP}
	pgOther  = boxapi.ZoneMember{DeviceID: pgOtherID, IP: pgOtherIP}
)

func pgIPs(ms []boxapi.ZoneMember) string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.IP)
	}
	return strings.Join(out, ",")
}

// The field shape: the leader's zone names the pair master and nothing else.
func TestAPairJoinsAGroupByItsPairMaster(t *testing.T) {
	t.Parallel()
	s := pgServer(t, pgLeaderIP, map[string]boxapi.Group{pgLeftIP: pgPair(), pgRightIP: pgPair()})
	run := pgRun(pgLeader, false, pgLeft, pgOther)
	if ans := s.zoneFormPairs(run); ans != nil {
		t.Fatalf("a pair as a member was refused: %+v", ans)
	}
	if got := pgIPs(run.slaves); got != pgLeftIP+","+pgOtherIP {
		t.Fatalf("members = %s, want the pair master and the other speaker", got)
	}
}

// Adding the pair's other half on its own made it serve two masters (#792).
// It means the pair, so it is taken in by the pair master.
func TestThePairsOtherHalfMapsToThePair(t *testing.T) {
	t.Parallel()
	s := pgServer(t, pgLeaderIP, map[string]boxapi.Group{pgLeftIP: pgPair(), pgRightIP: pgPair()})

	run := pgRun(pgLeader, false, pgRight)
	if ans := s.zoneFormPairs(run); ans != nil {
		t.Fatalf("refused: %+v", ans)
	}
	if len(run.slaves) != 1 || run.slaves[0] != pgLeft {
		t.Fatalf("members = %+v, want only the pair master %s", run.slaves, pgLeftIP)
	}

	// Both halves ticked: still one member.
	both := pgRun(pgLeader, false, pgLeft, pgRight, pgOther)
	if ans := s.zoneFormPairs(both); ans != nil {
		t.Fatalf("refused: %+v", ans)
	}
	if got := pgIPs(both.slaves); got != pgLeftIP+","+pgOtherIP {
		t.Fatalf("members = %s, want the pair once and the other speaker", got)
	}
}

// A group led by a pair master never starts its audio (#792), and the same
// holds for the other half: refused with a code a client can read, as a 409
// so a client that treats every 2xx as done does not report a group.
func TestAPairHalfCannotLeadAGroup(t *testing.T) {
	t.Parallel()
	for _, self := range []boxapi.ZoneMember{pgLeft, pgRight} {
		s := pgServer(t, self.IP, map[string]boxapi.Group{pgLeftIP: pgPair(), pgRightIP: pgPair()})
		ans := s.zoneFormPairs(pgRun(self, false, pgOther))
		if ans == nil {
			t.Fatalf("%s: a group led by a pair half was accepted", self.IP)
		}
		if ans.Status != http.StatusConflict || ans.JSON["code"] != pairCannotLeadCode || ans.JSON["ok"] != false {
			t.Fatalf("%s: answer = %+v, want 409 with code %q", self.IP, ans, pairCannotLeadCode)
		}
	}
}

// A member's pair that names THIS box: the leader is in that pair even when
// its own pair read did not answer.
func TestAMembersPairNamingTheLeaderIsRefused(t *testing.T) {
	t.Parallel()
	s := pgServer(t, pgLeftIP, map[string]boxapi.Group{pgRightIP: pgPair()})
	s.fetchGroupFn = func(_ context.Context, h string) (boxapi.Group, error) {
		if h == pgLeftIP {
			return boxapi.Group{}, errors.New("speaker busy")
		}
		return pgPair(), nil
	}
	ans := s.zoneFormPairs(pgRun(pgLeft, false, pgRight))
	if ans == nil || ans.JSON["code"] != pairCannotLeadCode {
		t.Fatalf("answer = %+v, want the pair-cannot-lead refusal", ans)
	}
}

// A permanent group defined with the pair's other half stores the pair master,
// which is what every later re-form drives.
func TestAPermanentGroupWithAPairStoresThePairMaster(t *testing.T) {
	t.Parallel()
	zs, err := zones.Load(filepath.Join(t.TempDir(), "zone.json"))
	if err != nil {
		t.Fatalf("zones.Load: %v", err)
	}
	s := pgServer(t, pgLeaderIP, map[string]boxapi.Group{pgLeftIP: pgPair(), pgRightIP: pgPair()})
	s.zones = zs
	run := pgRun(pgLeader, false, pgRight, pgOther)
	run.req.Permanent, run.req.DefineOnly = true, true
	if ans := s.zoneFormPairs(run); ans != nil {
		t.Fatalf("refused: %+v", ans)
	}
	if ans := s.zoneFormDefineOnly(run); ans == nil || ans.JSON["defined"] != true {
		t.Fatalf("define-only answer = %+v", ans)
	}
	z, ok := s.zones.Get()
	if !ok || !z.Permanent || z.Stereo {
		t.Fatalf("stored = %+v, want a permanent group", z)
	}
	if len(z.Slaves) != 2 || z.Slaves[0].IP != pgLeftIP || z.Slaves[1].IP != pgOtherIP {
		t.Fatalf("stored members = %+v, want the pair master and the other speaker", z.Slaves)
	}
}

// A re-form of a stored group never puts one half of a pair in on its own.
func TestAReformTakesAPairInByItsPairMaster(t *testing.T) {
	t.Parallel()
	s := pgServer(t, pgLeaderIP, map[string]boxapi.Group{pgLeftIP: pgPair(), pgRightIP: pgPair()})
	got := s.pairUnitJoiners(context.Background(), pgLeader, []boxapi.ZoneMember{pgRight, pgOther})
	if pgIPs(got) != pgLeftIP+","+pgOtherIP {
		t.Fatalf("joiners = %s, want the pair master and the other speaker", pgIPs(got))
	}
	// A stored pair master stays as it is.
	same := s.pairUnitJoiners(context.Background(), pgLeader, []boxapi.ZoneMember{pgLeft})
	if pgIPs(same) != pgLeftIP {
		t.Fatalf("joiners = %s, want the pair master", pgIPs(same))
	}
}

// Only a SoundTouch 10 is asked for its pair in production: the read hangs on
// the chassis that cannot pair.
func TestOnlyASoundTouch10IsAskedForItsPair(t *testing.T) {
	t.Parallel()
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.rememberMemberType("192.0.2.1", "SoundTouch 10")
	s.rememberMemberType("192.0.2.2", "SoundTouch 300")
	s.rememberMemberType("192.0.2.3", "SoundTouch 30")
	if !s.mayBePairHalf("192.0.2.1") {
		t.Error("a SoundTouch 10 must be asked")
	}
	for _, ip := range []string{"192.0.2.2", "192.0.2.3", "192.0.2.4", ""} {
		if s.mayBePairHalf(ip) {
			t.Errorf("%q must not be asked", ip)
		}
	}
}

// The leader's own /getGroup read is asked only of a SoundTouch 10: on the
// scm/BCO chassis it hangs BoseApp's web server (2026-11-15), and nothing but
// a SoundTouch 10 can be half of a pair. A plain two-speaker form led by
// another model reads no pair from the leader; an ST10 leader still does.
func TestOnlyASoundTouch10LeaderReadsItsOwnPair(t *testing.T) {
	t.Parallel()
	for model, wantLeaderRead := range map[string]bool{
		"SoundTouch 10":       true,
		"SoundTouch 20":       false,
		"SoundTouch 30":       false,
		"SoundTouch Portable": false,
	} {
		s := pgServer(t, pgLeaderIP, nil)
		var mu sync.Mutex
		var asked []string
		s.fetchGroupFn = func(_ context.Context, h string) (boxapi.Group, error) {
			mu.Lock()
			asked = append(asked, h)
			mu.Unlock()
			return boxapi.Group{}, nil
		}
		s.boxNameFn = func() (string, string) { return "Living room", model }
		run := pgRun(pgLeader, false, pgOther)
		if ans := s.zoneFormPairs(run); ans != nil {
			t.Fatalf("%s: a plain two-speaker group was refused: %+v", model, ans)
		}
		if got := pgIPs(run.slaves); got != pgOtherIP {
			t.Fatalf("%s: members = %s, want %s", model, got, pgOtherIP)
		}
		leaderRead := false
		mu.Lock()
		for _, h := range asked {
			if h == pgLeaderIP {
				leaderRead = true
			}
		}
		mu.Unlock()
		if leaderRead != wantLeaderRead {
			t.Errorf("%s leader: /getGroup on the leader = %v, want %v (asked %v)", model, leaderRead, wantLeaderRead, asked)
		}
	}
}

// A group the pair master joined ending must not take this speaker's own pair
// document with it.
func TestAPeerPurgeKeepsThisSpeakersPair(t *testing.T) {
	t.Parallel()
	zs, err := zones.Load(filepath.Join(t.TempDir(), "zone.json"))
	if err != nil {
		t.Fatalf("zones.Load: %v", err)
	}
	pair := zones.Zone{Master: pgLeftID, MasterIP: pgLeftIP, Stereo: true,
		Slaves: []zones.Member{{DeviceID: pgRightID, IP: pgRightIP}}}
	if err := zs.Set(pair); err != nil {
		t.Fatal(err)
	}
	s := &Server{boxHost: pgLeftIP, zones: zs, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	body := `{"deviceID":"` + pgRightID + `","ip":"` + pgRightIP + `"}`
	rec := httptest.NewRecorder()
	s.handleZonePurge(rec, httptest.NewRequest(http.MethodPost, "/api/box/zone/purge", strings.NewReader(body)))
	if z, ok := s.zones.Get(); !ok || !z.Stereo {
		t.Fatalf("the pair document was purged: %+v, %v (%s)", z, ok, rec.Body.String())
	}
}
