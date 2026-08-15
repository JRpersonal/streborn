package webui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
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
	members, isFollower := s.liveGroupView(context.Background())
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

	if _, isFollower := quietServer(livingHost).liveGroupView(context.Background()); isFollower {
		t.Error("a speaker whose own deviceID is the master must not be demoted to a follower")
	}
}

// A speaker in no group at all must not be turned into a follower of nobody.
func TestAStandaloneSpeakerIsNotAFollower(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{livingHost: {}},
		map[string]string{livingHost: livingID})

	if _, isFollower := quietServer(livingHost).liveGroupView(context.Background()); isFollower {
		t.Error("an empty zone is not a group")
	}
}

// A speaker that cannot say who it is must not guess. Comparing against an
// empty deviceID would make every grouped speaker look like a follower.
func TestASpeakerThatCannotIdentifyItselfStaysWithTheStoredDocument(t *testing.T) {
	withSpeakers(t,
		map[string]boxapi.Zone{livingHost: {Master: leaderID, SenderIP: leaderIP}},
		map[string]string{livingHost: ""})

	if _, isFollower := quietServer(livingHost).liveGroupView(context.Background()); isFollower {
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

	members, isFollower := quietServer(livingHost).liveGroupView(context.Background())
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

	if _, isFollower := quietServer(livingHost).liveGroupView(context.Background()); isFollower {
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
