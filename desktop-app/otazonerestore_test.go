package main

import (
	"strings"
	"testing"
)

// The cases planZoneRestore has to get right. The one that cost a user his
// multiroom group is "dropped": a group of three that comes back empty.
func TestPlanZoneRestore(t *testing.T) {
	three := []ZoneMember{
		{DeviceID: "dev-a", IP: "192.0.2.22"},
		{DeviceID: "dev-b", IP: "192.0.2.27"},
		{DeviceID: "dev-c", IP: "192.0.2.20"},
	}
	cases := []struct {
		name    string
		before  zoneRecord
		now     zoneRecord
		playing bool
		restore bool
		saysAny []string
	}{{
		name:    "a lone speaker is not a group, and says nothing at all",
		before:  zoneRecord{},
		now:     zoneRecord{},
		restore: false,
	}, {
		name:    "a group of three that came back empty is rebuilt",
		before:  zoneRecord{MasterIP: "192.0.2.33", MasterDevice: "dev-m", Members: three},
		now:     zoneRecord{},
		playing: true,
		restore: true,
		saysAny: []string{"dropped the group", "192.0.2.33"},
	}, {
		name:    "a group that survived the reboot is left alone",
		before:  zoneRecord{MasterIP: "192.0.2.33", Members: three},
		now:     zoneRecord{MasterIP: "192.0.2.33", Members: three},
		playing: true,
		restore: false,
		saysAny: []string{"survived the update"},
	}, {
		name:    "a group that came back with MORE members is left alone",
		before:  zoneRecord{MasterIP: "192.0.2.33", Members: three[:1]},
		now:     zoneRecord{MasterIP: "192.0.2.33", Members: three},
		playing: true,
		restore: false,
		saysAny: []string{"survived the update"},
	}, {
		name:    "a partly dropped group is rebuilt",
		before:  zoneRecord{MasterIP: "192.0.2.33", Members: three},
		now:     zoneRecord{MasterIP: "192.0.2.33", Members: three[:1]},
		playing: true,
		restore: true,
		saysAny: []string{"1 of 3"},
	}, {
		name:    "a silent speaker is left alone, because rebuilding would wake the house",
		before:  zoneRecord{MasterIP: "192.0.2.33", Members: three},
		now:     zoneRecord{},
		playing: false,
		restore: false,
		saysAny: []string{"silent", "nobody asked for"},
	}, {
		name:    "a stereo pair is never rebuilt as a plain zone",
		before:  zoneRecord{MasterIP: "192.0.2.33", Members: three[:1], Stereo: true},
		now:     zoneRecord{},
		playing: true,
		restore: false,
		saysAny: []string{"stereo pair"},
	}, {
		name:    "a permanent group re-forms itself, so STR does not",
		before:  zoneRecord{MasterIP: "192.0.2.33", Members: three, Permanent: true},
		now:     zoneRecord{},
		playing: true,
		restore: false,
		saysAny: []string{"permanent"},
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planZoneRestore(c.before, c.now, c.playing)
			if got.Restore != c.restore {
				t.Errorf("restore = %v, want %v (why: %q)", got.Restore, c.restore, got.Why)
			}
			for _, want := range c.saysAny {
				if !strings.Contains(got.Why, want) {
					t.Errorf("the journal line does not mention %q: %q", want, got.Why)
				}
			}
			if len(c.saysAny) == 0 && got.Why != "" {
				t.Errorf("a lone speaker should produce no journal line, got %q", got.Why)
			}
		})
	}
}

// A leader is not listed among its own members; a follower is, and names its
// leader in senderIP. Getting this backwards would have STR ask the wrong box
// to rebuild the group.
func TestZoneRecordFromDocTellsLeaderFromFollower(t *testing.T) {
	members := []any{
		map[string]any{"deviceID": "dev-a", "ip": "192.0.2.22"},
		map[string]any{"deviceID": "dev-b", "ip": "192.0.2.27"},
	}

	leader := zoneRecordFromDoc(map[string]any{
		"master":  "dev-m",
		"members": members,
	}, "192.0.2.33")
	if leader.MasterIP != "192.0.2.33" {
		t.Errorf("a box absent from its own member list is the leader, got master %q", leader.MasterIP)
	}
	if leader.MasterDevice != "dev-m" {
		t.Errorf("master device = %q, want dev-m", leader.MasterDevice)
	}
	if len(leader.slavesFor()) != 2 {
		t.Errorf("slaves = %d, want 2", len(leader.slavesFor()))
	}

	follower := zoneRecordFromDoc(map[string]any{
		"master":   "dev-m",
		"senderIP": "192.0.2.33",
		"members":  members,
	}, "192.0.2.22")
	if follower.MasterIP != "192.0.2.33" {
		t.Errorf("a box listed among the members follows senderIP, got master %q", follower.MasterIP)
	}
}

// The leader must never end up enrolled as its own follower.
func TestSlavesForDropsTheLeader(t *testing.T) {
	rec := zoneRecord{
		MasterIP: "192.0.2.33",
		Members: []ZoneMember{
			{DeviceID: "dev-m", IP: "192.0.2.33"},
			{DeviceID: "dev-a", IP: "192.0.2.22"},
		},
	}
	got := rec.slavesFor()
	if len(got) != 1 || got[0].IP != "192.0.2.22" {
		t.Errorf("slavesFor = %+v, want only 192.0.2.22", got)
	}
}

// An empty zone document must not look like a group. This is the guard that
// keeps the restore silent for the overwhelming majority of speakers, which
// stand alone.
func TestZoneRecordFromDocOnAStandaloneSpeaker(t *testing.T) {
	for _, doc := range []map[string]any{
		nil,
		{},
		{"members": []any{}},
		// A remembered-but-not-live group: members empty, remembered populated.
		// STR must not read that as a live group to restore, or every update of
		// a speaker that was once grouped would re-form a group the user ended.
		{"members": []any{}, "remembered": []any{
			map[string]any{"deviceID": "dev-a", "ip": "192.0.2.22"},
		}},
	} {
		rec := zoneRecordFromDoc(doc, "192.0.2.33")
		if rec.MasterIP != "" || len(rec.Members) != 0 {
			t.Errorf("doc %v read as a live group: %+v", doc, rec)
		}
		if plan := planZoneRestore(rec, zoneRecord{}, true); plan.Restore || plan.Why != "" {
			t.Errorf("doc %v produced a plan: %+v", doc, plan)
		}
	}
}

// The now_playing shapes taken from real diagnostic bundles. Getting this
// backwards is the dangerous direction: a false "playing" authorises the
// rebuild, which wakes every member of the group.
func TestAudiblyPlayingOnRealNowPlayingShapes(t *testing.T) {
	playing := []string{
		`<nowPlaying deviceID="DEV#1" source="LOCAL_INTERNET_RADIO"><playStatus>PLAY_STATE</playStatus></nowPlaying>`,
		`<nowPlaying source="SPOTIFY"><playStatus>BUFFERING_STATE</playStatus></nowPlaying>`,
	}
	silent := []string{
		"",
		`<nowPlaying deviceID="DEV#1" source="STANDBY"><ContentItem source="STANDBY" isPresetable="false" /></nowPlaying>`,
		`<nowPlaying source="LOCAL_INTERNET_RADIO"><playStatus>STOP_STATE</playStatus></nowPlaying>`,
		`<nowPlaying source="LOCAL_INTERNET_RADIO"><playStatus>PAUSE_STATE</playStatus></nowPlaying>`,
		`<nowPlaying source="INVALID_SOURCE" />`,
	}
	for _, x := range playing {
		if !audiblyPlaying(x) {
			t.Errorf("read as silent, so the group would never be rebuilt: %s", x)
		}
	}
	for _, x := range silent {
		if audiblyPlaying(x) {
			t.Errorf("read as playing, so a silent house would be woken: %s", x)
		}
	}
}
