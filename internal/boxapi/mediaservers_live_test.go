package boxapi

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestBrowseMediaServerLive drives BrowseMediaServer against a REAL speaker with
// a registered media server. Opt-in, because it needs hardware:
//
//	STR_LIVE_BOX=192.168.178.79 STR_LIVE_ACCOUNT=<uuid>/0 go test ./internal/boxapi -run Live -v
//
// It exists because the /navigate request shape is the part of this feature that
// no unit test can protect: the firmware rejects every arrangement but one, and
// the response nests the item's own ContentItem next to its PARENT's, which is
// exactly the kind of decoding that looks right and returns the wrong location.
func TestBrowseMediaServerLive(t *testing.T) {
	host := os.Getenv("STR_LIVE_BOX")
	account := os.Getenv("STR_LIVE_ACCOUNT")
	if host == "" || account == "" {
		t.Skip("set STR_LIVE_BOX and STR_LIVE_ACCOUNT to run this against hardware")
	}
	c := New(host)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	root, total, err := c.BrowseMediaServer(ctx, account, "", "", 1, 20)
	if err != nil {
		t.Fatalf("browse root: %v", err)
	}
	t.Logf("root: total=%d", total)
	for _, it := range root {
		t.Logf("  %-22s type=%-6s loc=%q", it.Name, it.Type, it.Location)
		if it.Location == "" {
			t.Errorf("item %q has no location: the ContentItem decoding is wrong", it.Name)
		}
	}
	if len(root) == 0 {
		t.Fatal("root came back empty; is the media server registered on this speaker?")
	}

	// Descend into the first container. This is the call that fails outright if
	// the <item>/<name>/<type> wrapper is ever "simplified" away.
	var dir *MediaItem
	for i := range root {
		if root[i].Type == "dir" {
			dir = &root[i]
			break
		}
	}
	if dir == nil {
		t.Skip("no folder in the root to descend into")
	}
	kids, ktotal, err := c.BrowseMediaServer(ctx, account, dir.Location, dir.Name, 1, 20)
	if err != nil {
		t.Fatalf("browse %q: %v", dir.Name, err)
	}
	t.Logf("%s: total=%d", dir.Name, ktotal)
	for _, it := range kids {
		t.Logf("  %-22s type=%-6s loc=%q", it.Name, it.Type, it.Location)
	}
	if len(kids) == 0 {
		t.Errorf("descending into %q returned nothing", dir.Name)
	}
}
