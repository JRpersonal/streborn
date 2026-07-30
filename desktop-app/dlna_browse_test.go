package main

import (
	"os"
	"testing"
)

// TestBrowseMediaLive walks the media server so a queue preset can be built
// from real tracks, using the same binding the Library tab uses.
func TestBrowseMediaLive(t *testing.T) {
	udn := os.Getenv("STR_LIVE_UDN")
	if udn == "" {
		t.Skip("set STR_LIVE_UDN=<udn> [STR_LIVE_OBJ=<id>] to browse")
	}
	obj := os.Getenv("STR_LIVE_OBJ")
	if obj == "" {
		obj = "0"
	}
	a := NewApp()
	// The app keeps discovered servers in memory, so the scan must happen in
	// this same process before anything can be browsed.
	if _, derr := a.ListMediaServers(4); derr != nil {
		t.Fatalf("ListMediaServers: %v", derr)
	}
	page, err := a.BrowseLibrary(udn, obj, 0, 40)
	if err != nil {
		t.Fatalf("BrowseMediaServer(%s,%s): %v", udn, obj, err)
	}
	t.Logf("containers=%d items=%d total=%d", len(page.Containers), len(page.Items), page.TotalMatches)
	for _, c := range page.Containers {
		t.Logf("  DIR  id=%s %q (%d)", c.ID, c.Title, c.ChildCount)
	}
	for i, it := range page.Items {
		if i >= 8 {
			t.Logf("  ... %d more items", len(page.Items)-8)
			break
		}
		t.Logf("  FILE id=%s %q mime=%s url=%s", it.ID, it.Title, it.MimeType, it.StreamURL)
	}
}
