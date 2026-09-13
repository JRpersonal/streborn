package main

import (
	"testing"

	"github.com/JRpersonal/streborn/dlna"
)

// The Library list has to say which of its entries nothing answered for.
//
// #733: the reporter enabled a pinned WD NAS from the list while the NAS was
// switched off. A pinned server that is dark is listed from its stored snapshot
// (refreshedManualServers), so it looked exactly like a server that had just
// replied; all three of his speakers then failed every browse of it and three
// releases were spent looking for the cause in STR (#770).
func TestMergeLibraryEntriesFlagsWhatDidNotAnswer(t *testing.T) {
	fritz := dlna.Server{UDN: "uuid:fritz", FriendlyName: "FRITZ!Box", Location: "http://192.0.2.1:49000/desc.xml"}
	nas := dlna.Server{UDN: "uuid:nas", FriendlyName: "WD My Cloud", Location: "http://192.0.2.20:9000/plugins/UPnP/MediaServer.xml"}
	plex := dlna.Server{UDN: "uuid:plex", FriendlyName: "Plex", Location: "http://192.0.2.30:32469/DeviceDescription.xml"}

	byUDN := func(t *testing.T, got []libraryEntry, udn string) libraryEntry {
		t.Helper()
		for _, e := range got {
			if e.Server.UDN == udn {
				return e
			}
		}
		t.Fatalf("%s is not in the list: %+v", udn, got)
		return libraryEntry{}
	}

	t.Run("a pinned server that is dark is listed and flagged", func(t *testing.T) {
		got := mergeLibraryEntries(
			[]dlna.Server{fritz},
			[]manualServerProbe{{Server: nas}}, // no answer this scan: stored snapshot
		)
		if len(got) != 2 {
			t.Fatalf("got %d entries, want the live one plus the pinned one: %+v", len(got), got)
		}
		if e := byUDN(t, got, "uuid:nas"); !e.NotAnswering || !e.Manual {
			t.Errorf("pinned dark server: NotAnswering=%v Manual=%v, want both true", e.NotAnswering, e.Manual)
		}
		if e := byUDN(t, got, "uuid:fritz"); e.NotAnswering || e.Manual {
			t.Errorf("live server: NotAnswering=%v Manual=%v, want both false", e.NotAnswering, e.Manual)
		}
	})

	t.Run("a pinned server that answered is not flagged", func(t *testing.T) {
		got := mergeLibraryEntries(nil, []manualServerProbe{{Server: nas, Answering: true}})
		if e := byUDN(t, got, "uuid:nas"); e.NotAnswering || !e.Manual {
			t.Errorf("pinned live server: NotAnswering=%v Manual=%v, want false/true", e.NotAnswering, e.Manual)
		}
	})

	t.Run("the sweep answering for a pinned server wins over its own failed probe", func(t *testing.T) {
		// The NAS ignored the direct describe (a wedged HTTP server, a probe that
		// raced its wake) but answered the M-SEARCH sweep. It is alive, so the
		// entry must stay unflagged, listed once, and keep its remove control.
		got := mergeLibraryEntries([]dlna.Server{nas}, []manualServerProbe{{Server: nas}})
		if len(got) != 1 {
			t.Fatalf("got %d entries, want 1 (deduped by UDN): %+v", len(got), got)
		}
		if e := got[0]; e.NotAnswering || !e.Manual {
			t.Errorf("NotAnswering=%v Manual=%v, want false/true", e.NotAnswering, e.Manual)
		}
	})

	t.Run("records without a UDN never reach the list", func(t *testing.T) {
		got := mergeLibraryEntries(
			[]dlna.Server{{FriendlyName: "no udn"}, plex},
			[]manualServerProbe{{Server: dlna.Server{FriendlyName: "empty snapshot"}}},
		)
		if len(got) != 1 || got[0].Server.UDN != "uuid:plex" {
			t.Fatalf("got %+v, want only the Plex entry", got)
		}
	})
}
