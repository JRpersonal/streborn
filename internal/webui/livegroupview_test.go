package webui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// withSpeakers answers the two firmware reads liveGroupView makes from a map,
// so a case reads as "this is what each speaker says" rather than as HTTP
// plumbing. boxapi pins port 8090, which a test cannot serve anyway.
func withSpeakers(t *testing.T, zones map[string]boxapi.Zone, ids map[string]string) {
	t.Helper()
	prevZone, prevID := fetchZone, fetchDeviceID
	fetchZone = func(_ context.Context, host string) (boxapi.Zone, error) {
		z, ok := zones[host]
		if !ok {
			return boxapi.Zone{}, errors.New("speaker not answering")
		}
		return z, nil
	}
	fetchDeviceID = func(_ context.Context, host string) string { return ids[host] }
	t.Cleanup(func() { fetchZone, fetchDeviceID = prevZone, prevID })
}

func quietServer(host string) *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: host}
}

const (
	leaderID   = "08DF1F0C9870"
	livingID   = "000C8A96488D"
	bathID     = "EC24B8B790CC"
	strangerID = "A1B2C3D4E5F6" // a speaker in somebody else's group
	leaderIP   = "192.168.178.79"
	livingHost = "127.0.0.1"
)

// The field case, five speakers in one group led by the Portable: the leader
// reported the group, one speaker holding a stale document claimed to LEAD it,
// and the others said they were not grouped at all while playing the group's
// music. A follower has to report the group it is actually in.
func TestAFollowerReportsTheGroupItIsActuallyIn(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{
			// What a follower's own firmware answers: the real master, itself
			// as the only member, and the master's address to reach it at.
			livingHost: {Master: leaderID, SenderIP: leaderIP, Members: []boxapi.ZoneMember{{DeviceID: livingID, IP: "192.168.178.44"}}},
			leaderIP:   {Master: leaderID, Members: []boxapi.ZoneMember{{DeviceID: bathID, IP: "192.168.178.48"}, {DeviceID: livingID, IP: "192.168.178.44"}}},
		},
		map[string]string{livingHost: livingID, leaderIP: leaderID})

	s := quietServer(livingHost)
	members, isFollower, _ := s.liveGroupView(context.Background())
	if !isFollower {
		t.Fatal("a speaker whose firmware names another master must report as a follower")
	}
	if len(members) != 3 {
		t.Fatalf("want the leader plus its two followers, got %d: %+v", len(members), members)
	}
	if !members[0].IsMaster || members[0].DeviceID != leaderID {
		t.Errorf("the leader must come first and be marked as such: %+v", members[0])
	}
	var selfSeen bool
	for _, m := range members {
		if m.DeviceID == livingID {
			selfSeen = true
			if !m.IsSelf {
				t.Errorf("this speaker's own entry must be marked isSelf: %+v", m)
			}
			if m.IP != livingHost {
				t.Errorf("own entry should be addressed over the local host, got %q", m.IP)
			}
		}
		if m.IsMaster && m.DeviceID != leaderID {
			t.Errorf("only the real leader may be marked master: %+v", m)
		}
	}
	if !selfSeen {
		t.Error("the group must include this speaker, it is playing the group's music")
	}
}

// senderIsMaster reads "true" on a follower on these chassis, which is what
// made a follower claim leadership. The deviceIDs decide instead, and that has
// to hold in the other direction too.
func TestTheLeaderIsDecidedByDeviceIDNotBySenderIsMaster(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{
			livingHost: {Master: leaderID, Members: []boxapi.ZoneMember{{DeviceID: bathID, IP: "192.168.178.48"}}},
		},
		map[string]string{livingHost: leaderID}) // this speaker IS the leader

	if _, isFollower, _ := quietServer(livingHost).liveGroupView(context.Background()); isFollower {
		t.Error("a speaker whose own deviceID is the master must not be demoted to a follower")
	}
}

// A speaker in no group at all must not be turned into a follower of nobody.
func TestAStandaloneSpeakerIsNotAFollower(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{livingHost: {}},
		map[string]string{livingHost: livingID})

	if _, isFollower, _ := quietServer(livingHost).liveGroupView(context.Background()); isFollower {
		t.Error("an empty zone is not a group")
	}
}

// A speaker that cannot say who it is must not guess. Comparing against an
// empty deviceID would make every grouped speaker look like a follower.
func TestASpeakerThatCannotIdentifyItselfStaysWithTheStoredDocument(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{livingHost: {Master: leaderID, SenderIP: leaderIP}},
		map[string]string{livingHost: ""})

	if _, isFollower, _ := quietServer(livingHost).liveGroupView(context.Background()); isFollower {
		t.Error("without its own deviceID the speaker cannot tell, so it must not claim to follow")
	}
}

// The leader not answering must not cost the follower its group: it still
// knows that it follows, and whom.
func TestAnUnreachableLeaderStillLeavesAGroup(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{
			livingHost: {Master: leaderID, SenderIP: leaderIP, Members: []boxapi.ZoneMember{{DeviceID: livingID, IP: "192.168.178.44"}}},
			// the leader is absent from the map, so it does not answer
		},
		map[string]string{livingHost: livingID})

	members, isFollower, _ := quietServer(livingHost).liveGroupView(context.Background())
	if !isFollower {
		t.Fatal("an unreachable leader is not evidence the group is gone")
	}
	if len(members) != 2 {
		t.Fatalf("want the leader and this speaker, got %d: %+v", len(members), members)
	}
	if !strings.EqualFold(members[0].DeviceID, leaderID) || !members[0].IsMaster {
		t.Errorf("the leader must still be named: %+v", members[0])
	}
	if !members[1].IsSelf {
		t.Errorf("this speaker must be in its own group: %+v", members[1])
	}
}

// Without the leader's address there is nothing to ask and nothing to show,
// and inventing a group from a deviceID alone would give the page rows it
// cannot read a volume from.
func TestAFollowerWithoutTheLeadersAddressReportsNothing(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{livingHost: {Master: leaderID}},
		map[string]string{livingHost: livingID})

	if _, isFollower, _ := quietServer(livingHost).liveGroupView(context.Background()); isFollower {
		t.Error("no address for the leader means no group can be reported")
	}
}

// The desktop app reads /api/box/zone, which returned the speaker's own view.
// On a follower that is a group of one, so the app showed a different set of
// speakers depending on which one it happened to ask.
func TestTheZoneReadReportsTheWholeGroupFromAFollower(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{
			livingHost: {Master: leaderID, SenderIP: leaderIP, Members: []boxapi.ZoneMember{{DeviceID: livingID, IP: "192.168.178.44"}}},
			leaderIP:   {Master: leaderID, Members: []boxapi.ZoneMember{{DeviceID: bathID, IP: "192.168.178.48"}, {DeviceID: livingID, IP: "192.168.178.44"}}},
		},
		map[string]string{livingHost: livingID, leaderIP: leaderID})

	s := quietServer(livingHost)
	own := boxapi.Zone{Master: leaderID, SenderIP: leaderIP, Members: []boxapi.ZoneMember{{DeviceID: livingID, IP: "192.168.178.44"}}}
	members, ok := s.leaderZone(context.Background(), own)
	if !ok {
		t.Fatal("a follower must be able to report the whole group")
	}
	if len(members) != 2 {
		t.Fatalf("want both members of the group, got %d: %+v", len(members), members)
	}
}

// The leader's own answer is already complete and must not be replaced by a
// call to itself.
func TestTheLeaderKeepsItsOwnMemberList(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{livingHost: {Master: leaderID, SenderIP: leaderIP}},
		map[string]string{livingHost: leaderID})

	own := boxapi.Zone{Master: leaderID, SenderIP: leaderIP,
		Members: []boxapi.ZoneMember{{DeviceID: bathID, IP: "192.168.178.48"}}}
	if _, ok := quietServer(livingHost).leaderZone(context.Background(), own); ok {
		t.Error("the leader already has the full list, it must not ask anybody")
	}
}

// A standalone speaker has no leader to ask, and must not be turned into one.
func TestAStandaloneZoneReadIsLeftAlone(t *testing.T) {
	withSpeakers(t, map[string]boxapi.Zone{livingHost: {}}, map[string]string{livingHost: livingID})
	if _, ok := quietServer(livingHost).leaderZone(context.Background(), boxapi.Zone{}); ok {
		t.Error("no master means nothing to fetch")
	}
}

// The regression the audit caught the same day it was introduced: the read was
// moved onto the firmware and the write was left on the stored document, so a
// follower drew a full group card whose every press came back 409. Whatever
// the page is allowed to SHOW, it has to be allowed to SET.
func TestWhatTheGroupViewShowsIsWhatCanBeSet(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{
			livingHost: {Master: leaderID, SenderIP: leaderIP, Members: []boxapi.ZoneMember{{DeviceID: livingID, IP: "192.168.178.44"}}},
			leaderIP:   {Master: leaderID, Members: []boxapi.ZoneMember{{DeviceID: bathID, IP: "192.168.178.48"}, {DeviceID: livingID, IP: "192.168.178.44"}}},
		},
		map[string]string{livingHost: livingID, leaderIP: leaderID})

	// A follower has no stored document at all, which is the whole point.
	s := quietServer(livingHost)
	members, grouped, stereo := s.groupView(context.Background())
	if !grouped {
		t.Fatal("a follower must be reported as grouped, it is playing the group's music")
	}
	if stereo {
		t.Error("a multiroom zone is not a stereo pair")
	}
	if len(members) != 3 {
		t.Fatalf("want the leader and both followers, got %d: %+v", len(members), members)
	}
	// Every row the page draws must be addressable by the write path.
	for _, m := range members {
		if m.IP == "" {
			t.Errorf("a member with no address can be shown but never set: %+v", m)
		}
	}
}

// A speaker in no group answers the same way to both, so the page draws
// nothing and the write path refuses.
func TestAStandaloneSpeakerIsNotGroupedForEitherPath(t *testing.T) {
	withSpeakers(t, map[string]boxapi.Zone{livingHost: {}}, map[string]string{livingHost: livingID})
	if _, grouped, _ := quietServer(livingHost).groupView(context.Background()); grouped {
		t.Error("no group means no group, for the reader and the writer alike")
	}
}

// The stored-group cross-check used to read the speaker's own zone a second
// time, a fraction of a second after liveGroupView had just read it, and the
// second answer decided. Two reads are two chances for an empty one, and an
// empty one throws the group off the page.
func TestTheStoredGroupCheckReusesTheAnswerAlreadyRead(t *testing.T) {
	store, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
	if err != nil {
		t.Fatalf("zone store: %v", err)
	}
	if err := store.Set(zones.Zone{Master: leaderID, MasterIP: livingHost,
		Slaves: []zones.Member{{DeviceID: bathID, IP: "192.168.178.48"}}}); err != nil {
		t.Fatalf("store the group: %v", err)
	}
	// This speaker leads the group, so liveGroupView reads its zone and hands
	// the answer on rather than reporting a follower.
	withSpeakers(t,
		map[string]boxapi.Zone{livingHost: {Master: leaderID, Members: []boxapi.ZoneMember{{DeviceID: bathID, IP: "192.168.178.48"}}}},
		map[string]string{livingHost: leaderID})
	inner := fetchZone
	var reads int
	fetchZone = func(ctx context.Context, host string) (boxapi.Zone, error) {
		if host == livingHost {
			reads++
		}
		return inner(ctx, host)
	}

	s := quietServer(livingHost)
	s.zones = store
	members, grouped, _ := s.groupView(context.Background())
	if !grouped {
		t.Fatalf("the firmware confirmed the group, it must be reported: %+v", members)
	}
	if reads != 1 {
		t.Errorf("the speaker's own zone was read %d times for one view, want 1", reads)
	}
}

// The line reads as a group that went missing, so a speaker that never had one
// must not print it. It ran on every poll of the phone's Speakers tab, which is
// the noise that buries the one occurrence that matters.
func TestNoGroupIsReportedLostWhenNoneWasStored(t *testing.T) {
	const lost = "not on the speaker any more"
	var log bytes.Buffer
	s := &Server{logger: slog.New(slog.NewTextHandler(&log, nil)), boxHost: livingHost}

	// What a standalone speaker's firmware answers, already read.
	standalone := ownZoneAnswer{read: true}
	if s.storedGroupIsLive(standalone) {
		t.Error("an empty firmware zone is not a group")
	}
	if strings.Contains(log.String(), lost) {
		t.Errorf("a speaker that never stored a group cannot have lost one: %s", log.String())
	}

	store, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
	if err != nil {
		t.Fatalf("zone store: %v", err)
	}
	if err := store.Set(zones.Zone{Master: leaderID, MasterIP: livingHost,
		Slaves: []zones.Member{{DeviceID: bathID, IP: "192.168.178.48"}}}); err != nil {
		t.Fatalf("store the group: %v", err)
	}
	s.zones = store
	log.Reset()
	if s.storedGroupIsLive(standalone) {
		t.Error("the stored group is not on the speaker, so it must not be reported")
	}
	if !strings.Contains(log.String(), lost) {
		t.Errorf("a stored group the speaker has dropped is worth a line: %s", log.String())
	}
}

// senderIPAddress is whoever sent the last zone message, not proof of who leads
// now. After a group is torn down and rebuilt around a different speaker that
// address answers with somebody else's group, and adopting its members puts
// strangers on the page and takes their volume away from whoever is listening
// to them.
func TestALeaderThatNamesADifferentMasterIsNotAdopted(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{
			livingHost: {Master: leaderID, SenderIP: leaderIP, Members: []boxapi.ZoneMember{{DeviceID: livingID, IP: "192.168.178.44"}}},
			// That address has since joined a group led by a third speaker.
			leaderIP: {Master: bathID, Members: []boxapi.ZoneMember{{DeviceID: leaderID, IP: leaderIP}, {DeviceID: strangerID, IP: "192.168.178.58"}}},
		},
		map[string]string{livingHost: livingID, leaderIP: leaderID})

	members, isFollower, _ := quietServer(livingHost).liveGroupView(context.Background())
	if !isFollower {
		t.Fatal("this speaker still follows, only the other list is unusable")
	}
	if len(members) != 2 {
		t.Fatalf("want only what this speaker knows first hand, got %d: %+v", len(members), members)
	}
	for _, m := range members {
		if strings.EqualFold(m.DeviceID, strangerID) {
			t.Errorf("a speaker from the other group must not appear: %+v", m)
		}
	}
	if !members[1].IsSelf {
		t.Errorf("this speaker must be in its own group: %+v", members[1])
	}
}

// A speaker listed twice gets two rows on the page, and one press of the group
// slider then sends it the same change twice.
func TestASpeakerListedTwiceGetsOneRow(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{
			livingHost: {Master: leaderID, SenderIP: leaderIP, Members: []boxapi.ZoneMember{{DeviceID: livingID, IP: "192.168.178.44"}}},
			leaderIP: {Master: leaderID, Members: []boxapi.ZoneMember{
				{DeviceID: bathID, IP: "192.168.178.48"},
				{DeviceID: livingID, IP: "192.168.178.44"},
				{DeviceID: bathID, IP: "192.168.178.48"},
				{DeviceID: leaderID, IP: leaderIP}, // the leader listing itself
			}},
		},
		map[string]string{livingHost: livingID, leaderIP: leaderID})

	members, isFollower, _ := quietServer(livingHost).liveGroupView(context.Background())
	if !isFollower {
		t.Fatal("this speaker follows the leader")
	}
	if len(members) != 3 {
		t.Fatalf("want the leader and its two followers once each, got %d: %+v", len(members), members)
	}
	rows := map[string]int{}
	for _, m := range members {
		rows[strings.ToUpper(m.DeviceID)]++
	}
	for id, n := range rows {
		if n != 1 {
			t.Errorf("speaker %s got %d rows", id, n)
		}
	}
}

// A leader that does not list this speaker is missing ONE row, not wrong about
// the whole group. This speaker's own firmware named that leader, so the
// membership was reported, by the speaker we asked first. Throwing the list
// away instead turned a five speaker group into two rows on the phone whenever
// a leader answered with an empty deviceID for a member, which the two-chip
// chassis here have done before.
func TestASpeakerMissingFromTheLeadersListIsAddedNotDropped(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{
			livingHost: {Master: leaderID, SenderIP: leaderIP, Members: []boxapi.ZoneMember{{DeviceID: livingID, IP: "192.168.178.44"}}},
			leaderIP:   {Master: leaderID, Members: []boxapi.ZoneMember{{DeviceID: bathID, IP: "192.168.178.48"}}},
		},
		map[string]string{livingHost: livingID, leaderIP: leaderID})

	members, isFollower, _ := quietServer(livingHost).liveGroupView(context.Background())
	if !isFollower {
		t.Fatal("this speaker's own firmware names a master, it follows")
	}
	if len(members) != 3 {
		t.Fatalf("want the leader, the member it did list, and this speaker, got %d: %+v", len(members), members)
	}
	if !strings.EqualFold(members[0].DeviceID, leaderID) || !members[0].IsMaster {
		t.Errorf("the leader must come first: %+v", members[0])
	}
	selfRows := 0
	for _, m := range members {
		if m.IsSelf {
			selfRows++
			if m.IP != livingHost {
				t.Errorf("this speaker's row must be addressable: %+v", m)
			}
		}
	}
	if selfRows != 1 {
		t.Errorf("want exactly one row for this speaker, got %d", selfRows)
	}
}

// leaderZone answers the same question as liveGroupView, for the phone. Until
// today it did so with none of the checks, so one speaker could produce two
// different groups depending on which surface asked.
func TestLeaderZoneRefusesAGroupThatNamesADifferentLeader(t *testing.T) {
	// The leader we were told to follow now leads somebody else's group.
	withSpeakers(t,
		map[string]boxapi.Zone{
			livingHost: {Master: leaderID, SenderIP: leaderIP},
			leaderIP:   {Master: strangerID, Members: []boxapi.ZoneMember{{DeviceID: bathID, IP: "192.168.178.48"}}},
		},
		map[string]string{livingHost: livingID, leaderIP: strangerID})

	s := quietServer(livingHost)
	if _, ok := s.leaderZone(context.Background(), boxapi.Zone{Master: leaderID, SenderIP: leaderIP}); ok {
		t.Error("a member list from a group with a different leader was adopted")
	}
}

// A leader that does not list us must not collapse the group: our own firmware
// is what named that leader. Repeats in the list must not become extra rows.
func TestLeaderZoneAddsThisSpeakerWhenTheLeaderOmitsIt(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{
			livingHost: {Master: leaderID, SenderIP: leaderIP},
			leaderIP: {Master: leaderID, Members: []boxapi.ZoneMember{
				{DeviceID: bathID, IP: "192.168.178.48"},
				{DeviceID: bathID, IP: "192.168.178.48"},
			}},
		},
		map[string]string{livingHost: livingID, leaderIP: leaderID})

	members, ok := quietServer(livingHost).leaderZone(context.Background(),
		boxapi.Zone{Master: leaderID, SenderIP: leaderIP})
	if !ok {
		t.Fatal("the group was dropped although the leader agreed about who leads")
	}
	if len(members) != 2 {
		t.Fatalf("expected the other member plus this speaker, got %d: %+v", len(members), members)
	}
	var self bool
	for _, m := range members {
		if m.DeviceID == livingID {
			self = true
		}
	}
	if !self {
		t.Errorf("this speaker is missing from its own group: %+v", members)
	}
}
