package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/dlna"
)

func TestMergeKnownServers(t *testing.T) {
	now := time.Now()
	old := now.Add(-24 * time.Hour)

	t.Run("upsert by UDN, skip incomplete records", func(t *testing.T) {
		list := []knownMediaServer{
			{UDN: "uuid:a", Location: "http://192.0.2.9:8200/rootDesc.xml", FriendlyName: "Old Name", LastSeen: old},
		}
		got := mergeKnownServers(list, []dlna.Server{
			// Same server, moved description URL and renamed: must replace in place.
			{UDN: "uuid:a", Location: "http://192.0.2.9:8201/rootDesc.xml", FriendlyName: "New Name"},
			// Brand new server: appended.
			{UDN: "uuid:b", Location: "http://192.0.2.10:32469/DeviceDescription.xml", FriendlyName: "Plex"},
			// No location (e.g. a stale manual snapshot): cannot be re-probed, skip.
			{UDN: "uuid:c", FriendlyName: "No location"},
			// No UDN: not a described server, skip.
			{Location: "http://192.0.2.11:5000/DeviceDescription.xml"},
		}, now)
		if len(got) != 2 {
			t.Fatalf("got %d entries, want 2: %+v", len(got), got)
		}
		if got[0].UDN != "uuid:a" || got[0].Location != "http://192.0.2.9:8201/rootDesc.xml" ||
			got[0].FriendlyName != "New Name" || !got[0].LastSeen.Equal(now) {
			t.Errorf("upsert did not replace in place: %+v", got[0])
		}
		if got[1].UDN != "uuid:b" {
			t.Errorf("new server not appended: %+v", got[1])
		}
	})

	t.Run("cap evicts the longest-unseen entries", func(t *testing.T) {
		var list []knownMediaServer
		for i := 0; i < knownServersCap; i++ {
			list = append(list, knownMediaServer{
				UDN:      fmt.Sprintf("uuid:old-%d", i),
				Location: fmt.Sprintf("http://192.0.2.9:%d/rootDesc.xml", 10000+i),
				LastSeen: old.Add(time.Duration(i) * time.Minute),
			})
		}
		got := mergeKnownServers(list, []dlna.Server{
			{UDN: "uuid:fresh", Location: "http://192.0.2.10:8200/rootDesc.xml"},
		}, now)
		if len(got) != knownServersCap {
			t.Fatalf("got %d entries, want the cap %d", len(got), knownServersCap)
		}
		seen := map[string]bool{}
		for _, k := range got {
			seen[k.UDN] = true
		}
		if !seen["uuid:fresh"] {
			t.Errorf("the just-seen server must survive the cap")
		}
		if seen["uuid:old-0"] {
			t.Errorf("the longest-unseen entry must be the eviction victim")
		}
	})
}
