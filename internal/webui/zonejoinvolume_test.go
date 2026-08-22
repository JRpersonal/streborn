package webui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

func member(id, ip string) boxapi.ZoneMember { return boxapi.ZoneMember{DeviceID: id, IP: ip} }

func ids(ms []boxapi.ZoneMember) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.DeviceID)
	}
	return out
}

func TestNewlyJoinedMembers(t *testing.T) {
	cases := []struct {
		name        string
		want        []boxapi.ZoneMember
		live        boxapi.Zone
		liveErr     error
		prev        zones.Zone
		hadPrev     bool
		added       []string
		trustworthy bool
	}{
		{
			name:        "fresh zone: everyone is new",
			want:        []boxapi.ZoneMember{member("AA", "192.0.2.11"), member("BB", "192.0.2.12")},
			hadPrev:     true, // an empty stored document IS a before-picture
			added:       []string{"AA", "BB"},
			trustworthy: true,
		},
		{
			name: "one added to a live group: only that one",
			want: []boxapi.ZoneMember{member("AA", "192.0.2.11"), member("BB", "192.0.2.12")},
			live: boxapi.Zone{Master: "MM", Members: []boxapi.ZoneMember{
				member("MM", "192.0.2.10"), member("AA", "192.0.2.11"),
			}},
			added:       []string{"BB"},
			trustworthy: true,
		},
		{
			// The #401 balance-wipe regression: the live read failed, so without
			// the stored document every existing member would look new and get
			// the master's level.
			name:    "live read failed but the document knows the group",
			want:    []boxapi.ZoneMember{member("AA", "192.0.2.11"), member("BB", "192.0.2.12")},
			liveErr: errors.New("getZone: timeout"),
			prev: zones.Zone{Master: "MM", Slaves: []zones.Member{
				{DeviceID: "AA", IP: "192.0.2.11"},
			}},
			hadPrev:     true,
			added:       []string{"BB"},
			trustworthy: true,
		},
		{
			// Two-chip chassis: the firmware lists the SCM deviceID, discovery
			// supplies the wlan0 MAC. Same speaker, same IP, not new.
			name: "same IP under a different deviceID is not new",
			want: []boxapi.ZoneMember{member("WLAN0MAC", "192.0.2.11")},
			live: boxapi.Zone{Master: "MM", Members: []boxapi.ZoneMember{
				member("SCMDEVICEID", "192.0.2.11"),
			}},
			added:       nil,
			trustworthy: true,
		},
		{
			name: "deviceID match with no IP on record is not new",
			want: []boxapi.ZoneMember{member("aa", "192.0.2.11")},
			live: boxapi.Zone{Master: "MM", Members: []boxapi.ZoneMember{
				member("AA", ""),
			}},
			added:       nil,
			trustworthy: true,
		},
		{
			// A healthy MIRROR group never drives the firmware zone, so an empty
			// live read is normal there. The stored document is what carries the
			// membership, and it must be consulted even when the read SUCCEEDED.
			name: "mirror group: empty live read, document holds the members",
			want: []boxapi.ZoneMember{member("AA", "192.0.2.11"), member("BB", "192.0.2.12")},
			live: boxapi.Zone{},
			prev: zones.Zone{Master: "MM", Slaves: []zones.Member{
				{DeviceID: "AA", IP: "192.0.2.11"},
			}},
			hadPrev:     true,
			added:       []string{"BB"},
			trustworthy: true,
		},
		{
			// No before-picture at all. Every slave would look new; touch nobody.
			name:        "live read failed and nothing on record: not trustworthy",
			want:        []boxapi.ZoneMember{member("AA", "192.0.2.11")},
			liveErr:     errors.New("getZone: no route to host"),
			hadPrev:     false,
			added:       nil,
			trustworthy: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, trust := newlyJoinedMembers(tc.want, tc.live, tc.liveErr, tc.prev, tc.hadPrev)
			if trust != tc.trustworthy {
				t.Fatalf("trustworthy = %v, want %v", trust, tc.trustworthy)
			}
			gotIDs := ids(got)
			if len(gotIDs) != len(tc.added) {
				t.Fatalf("added = %v, want %v", gotIDs, tc.added)
			}
			for i := range gotIDs {
				if gotIDs[i] != tc.added[i] {
					t.Fatalf("added = %v, want %v", gotIDs, tc.added)
				}
			}
		})
	}
}

func TestDropMembersRemovesUnverifiedJoins(t *testing.T) {
	in := []boxapi.ZoneMember{member("AA", "192.0.2.11"), member("BB", "192.0.2.12")}
	got := ids(dropMembers(in, []string{"bb"}))
	if len(got) != 1 || got[0] != "AA" {
		t.Fatalf("dropMembers = %v, want [AA]", got)
	}
	if len(ids(dropMembers(in, nil))) != 2 {
		t.Fatalf("dropMembers with no ids must not drop anything")
	}
}

// fakeVolumes stands in for the two live seams.
type fakeVolumes struct {
	mu     sync.Mutex
	level  map[string]int
	errFor map[string]error
	writes []string
	// after the first write to a host, report this level instead (the wake
	// default revert the single correction exists for)
	revertTo map[string]int
	written  map[string]bool
}

func newFakeVolumes() *fakeVolumes {
	return &fakeVolumes{
		level: map[string]int{}, errFor: map[string]error{},
		revertTo: map[string]int{}, written: map[string]bool{},
	}
}

func (f *fakeVolumes) read(_ context.Context, host string) (boxapi.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errFor[host]; err != nil {
		return boxapi.Volume{}, err
	}
	if f.written[host] {
		if r, ok := f.revertTo[host]; ok {
			return boxapi.Volume{Target: r, Actual: r}, nil
		}
	}
	v := f.level[host]
	return boxapi.Volume{Target: v, Actual: v}, nil
}

func (f *fakeVolumes) set(_ context.Context, addr netip.Addr, pct int) error {
	host := addr.String()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, host)
	f.level[host] = pct
	if f.written[host] {
		// a second write means the correction fired; stop reverting
		delete(f.revertTo, host)
	}
	f.written[host] = true
	return nil
}

func withFakeVolumes(t *testing.T, f *fakeVolumes) {
	t.Helper()
	origRead, origSet := readMemberVolumeFn, setMemberVolumeFn
	readMemberVolumeFn, setMemberVolumeFn = f.read, f.set
	t.Cleanup(func() { readMemberVolumeFn, setMemberVolumeFn = origRead, origSet })
}

func newJoinVolServer(host string) *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: host}
}

func TestMatchNewMembersSkipsWhenMasterReportsNothing(t *testing.T) {
	f := newFakeVolumes()
	f.errFor["192.0.2.10"] = errors.New("box busy")
	withFakeVolumes(t, f)

	s := newJoinVolServer("192.0.2.10")
	s.matchNewMembersToMasterVolume([]boxapi.ZoneMember{member("AA", "192.0.2.11")}, s.zoneFormSeq.Load())

	if len(f.writes) != 0 {
		t.Fatalf("a master that did not answer must not silence the joiner, wrote %v", f.writes)
	}

	// Same again, this time the master answers 0: a muted or mid-wake leader.
	f2 := newFakeVolumes()
	f2.level["192.0.2.10"] = 0
	withFakeVolumes(t, f2)
	s.matchNewMembersToMasterVolume([]boxapi.ZoneMember{member("AA", "192.0.2.11")}, s.zoneFormSeq.Load())
	if len(f2.writes) != 0 {
		t.Fatalf("a leader reporting 0 must not silence the joiner, wrote %v", f2.writes)
	}
}

func TestMatchNewMembersStandsDownForANewerEdit(t *testing.T) {
	f := newFakeVolumes()
	f.level["192.0.2.10"] = 40
	withFakeVolumes(t, f)

	s := newJoinVolServer("192.0.2.10")
	stale := s.zoneFormSeq.Load()
	s.zoneFormSeq.Add(1) // a newer group edit arrived
	s.matchNewMembersToMasterVolume([]boxapi.ZoneMember{member("AA", "192.0.2.11")}, stale)

	if len(f.writes) != 0 {
		t.Fatalf("a superseded applier must not deliver a stale level, wrote %v", f.writes)
	}
}

// The whole point of the feature, plus the one correction it is allowed to
// make. A joining speaker gets the group's level; if the firmware then finishes
// waking it and resets it to its own default, that revert is corrected once.
//
// Private addresses on purpose: isLANPeer refuses anything outside RFC1918 /
// link-local, so the documentation range used by the pure tables above would
// silently write to nobody here.
func TestMatchNewMembersAppliesAndCorrectsTheWakeDefault(t *testing.T) {
	f := newFakeVolumes()
	f.level["192.168.10.10"] = 12 // the group leader, a quiet evening
	f.level["192.168.10.11"] = 30
	f.level["192.168.10.12"] = 30
	// This one snaps back to its wake default right after we set it.
	f.revertTo["192.168.10.11"] = 30
	withFakeVolumes(t, f)

	s := newJoinVolServer("192.168.10.10")
	s.matchNewMembersToMasterVolume([]boxapi.ZoneMember{
		member("AA", "192.168.10.11"),
		member("BB", "192.168.10.12"),
	}, s.zoneFormSeq.Load())

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.level["192.168.10.11"] != 12 || f.level["192.168.10.12"] != 12 {
		t.Fatalf("joining members should be at the group level 12, got %v", f.level)
	}
	// AA written twice (initial + one correction), BB once. Never more.
	n11, n12 := 0, 0
	for _, h := range f.writes {
		switch h {
		case "192.168.10.11":
			n11++
		case "192.168.10.12":
			n12++
		}
	}
	if n11 != 2 {
		t.Fatalf("the reverted member should be corrected exactly once, got %d writes", n11)
	}
	if n12 != 1 {
		t.Fatalf("a member that held its level must not be written again, got %d writes", n12)
	}
}

// A level that moved to something a person would plausibly have chosen is left
// alone: #548's rule that a control which cannot tell whose intent it is
// enforcing must not enforce one.
func TestMatchNewMembersLeavesAHumanChangeAlone(t *testing.T) {
	f := newFakeVolumes()
	f.level["192.168.10.10"] = 12
	f.level["192.168.10.11"] = 30
	f.revertTo["192.168.10.11"] = 45 // somebody turned it up in the meantime
	withFakeVolumes(t, f)

	s := newJoinVolServer("192.168.10.10")
	s.matchNewMembersToMasterVolume([]boxapi.ZoneMember{member("AA", "192.168.10.11")}, s.zoneFormSeq.Load())

	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, h := range f.writes {
		if h == "192.168.10.11" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("a level changed by someone else must not be overruled, got %d writes", n)
	}
}

// The address a joining member is dialled at comes from the zone-form request
// body and ends up in a URL, so it has to be a bare LAN IP and nothing else.
// A boolean "looks fine" check would leave the raw string in the request; this
// returns a re-serialised address or nothing.
func TestLanPeerAddrRejectsEverythingButALANIP(t *testing.T) {
	good := []string{"192.168.178.49", "10.0.0.1", "172.16.5.5", "169.254.1.2", " 192.168.1.7 "}
	for _, in := range good {
		if a, ok := lanPeerAddr(in); !ok || !a.IsValid() {
			t.Errorf("lanPeerAddr(%q) should be accepted", in)
		}
	}
	bad := []string{
		"", "   ",
		"8.8.8.8",                 // public
		"127.0.0.1",               // loopback
		"0.0.0.0",                 // unspecified
		"224.0.0.1",               // multicast
		"192.168.1.1:8080",        // carries a port
		"evil.example.com",        // a name, not an IP
		"192.168.1.1/../../etc",   // path fragment
		"user@192.168.1.1",        // credential
		"192.168.1.1 192.168.1.2", // two of them
		"fe80::1%eth0",            // zoned
		"[192.168.1.1]",           // bracketed
	}
	for _, in := range bad {
		if _, ok := lanPeerAddr(in); ok {
			t.Errorf("lanPeerAddr(%q) should be rejected", in)
		}
	}
}

// A member whose address is not a LAN IP is skipped entirely, not dialled.
func TestMatchNewMembersSkipsANonLANMember(t *testing.T) {
	f := newFakeVolumes()
	f.level["192.168.10.10"] = 20
	withFakeVolumes(t, f)

	s := newJoinVolServer("192.168.10.10")
	s.matchNewMembersToMasterVolume([]boxapi.ZoneMember{
		{DeviceID: "AA", IP: "8.8.8.8"},
		{DeviceID: "BB", IP: "evil.example.com"},
		{DeviceID: "CC", IP: ""},
	}, s.zoneFormSeq.Load())

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 0 {
		t.Fatalf("no non-LAN member may be dialled, wrote %v", f.writes)
	}
}
