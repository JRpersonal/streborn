package webui

import (
	"sort"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxsnapshot"
)

// TestPartitionRestorable verifies the cloud-preset restore honesty split: a
// service the box already reports UNAVAILABLE (its saved login expired with the
// Bose cloud) is never written back (the firmware would drop the button within
// seconds), an account suffix still matches the base service, and a source the
// box does not list at all is treated as restorable rather than silently dropped.
func TestPartitionRestorable(t *testing.T) {
	cloud := []boxsnapshot.Preset{
		{Slot: 3, Source: "DEEZER", SourceAccount: "1456373802"},
		{Slot: 4, Source: "DEEZER_HIFI"},
		{Slot: 5, Source: "AMAZON"},
	}
	statuses := map[string]string{"DEEZER": "UNAVAILABLE", "AMAZON": "READY"}

	writable, expired := partitionRestorable(cloud, statuses)

	if len(writable) != 1 || writable[0].Slot != 5 {
		t.Fatalf("writable = %+v, want only slot 5 (AMAZON, READY)", writable)
	}
	// Both DEEZER and DEEZER_HIFI normalise to the DEEZER status entry, so the
	// expired list dedupes to a single DEEZER.
	if len(expired) != 1 || expired[0] != "DEEZER" {
		t.Fatalf("expired = %v, want [DEEZER]", expired)
	}

	// A service the box does not list at all is unknown, not proven dead: restore.
	w2, e2 := partitionRestorable([]boxsnapshot.Preset{{Slot: 2, Source: "TIDAL"}}, map[string]string{})
	if len(w2) != 1 || w2[0].Slot != 2 || len(e2) != 0 {
		t.Fatalf("unknown source: writable=%+v expired=%v, want writable=[slot 2] expired=[]", w2, e2)
	}

	// All cloud presets bound to expired sources => nothing writable, everything
	// reported expired (the Max case: a Deezer button that vanishes after restore).
	w3, e3 := partitionRestorable(
		[]boxsnapshot.Preset{{Slot: 3, Source: "DEEZER"}, {Slot: 4, Source: "DEEZER"}},
		map[string]string{"DEEZER": "UNAVAILABLE"},
	)
	sort.Strings(e3)
	if len(w3) != 0 || len(e3) != 1 || e3[0] != "DEEZER" {
		t.Fatalf("all-expired: writable=%+v expired=%v, want writable=[] expired=[DEEZER]", w3, e3)
	}
}
