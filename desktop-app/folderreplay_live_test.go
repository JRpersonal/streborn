package main

// A live check of the folder replay against a real speaker and a real media
// server, through the bound App methods the buttons call (see the str-fleet-test
// skill: one layer under the GUI, never a hand-rolled curl).
//
// What it proves, and the reason each step is here:
//
//   - a folder started as a queue is recorded as ONE Recently-played card whose
//     key names the server and the container,
//   - ReplayFolderCard, which the play button on that card now calls, rebuilds
//     the WHOLE folder rather than replaying its first track,
//   - and the queue the speaker ends up with holds more than one track, which is
//     the difference between the bug (#978: one song, then the speaker stops)
//     and the fix.
//
// Env-gated so CI never touches hardware:
//
//	STR_LIVE_BOX=192.168.178.79 STR_LIVE_PORT=17008 go test ./... -run LiveFolderReplay -v

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLiveFolderReplay(t *testing.T) {
	host := os.Getenv("STR_LIVE_BOX")
	if host == "" {
		t.Skip("set STR_LIVE_BOX (and STR_LIVE_PORT) to run this against a speaker")
	}
	port, _ := strconv.Atoi(os.Getenv("STR_LIVE_PORT"))
	if port == 0 {
		port = 17008
	}
	a := NewApp()

	// Volume first, and low: this plays real audio in the room.
	if err := a.SetBoxVolume(host, port, 5); err != nil {
		t.Fatalf("volume: %v", err)
	}

	// 1. Find a folder on a registered server that holds at least two tracks.
	servers, err := a.ListMediaServers(6)
	if err != nil || len(servers) == 0 {
		t.Fatalf("no media servers found: %v", err)
	}
	var udn, container, folderName string
	var items []map[string]any
	for _, srv := range servers {
		root, err := a.BrowseLibrary(srv.UDN, "0", 0, 60)
		if err != nil {
			continue
		}
		// One level down is enough: the root of a music server is folders.
		for _, c := range root.Containers {
			page, err := a.BrowseLibrary(srv.UDN, c.ID, 0, 60)
			if err != nil {
				continue
			}
			for _, sub := range page.Containers {
				deep, err := a.BrowseLibrary(srv.UDN, sub.ID, 0, 60)
				if err != nil || len(deep.Items) < 2 {
					continue
				}
				udn, container, folderName = srv.UDN, sub.ID, sub.Title
				for _, it := range deep.Items {
					if it.StreamURL == "" {
						continue
					}
					items = append(items, map[string]any{
						"url": it.StreamURL, "title": it.Title, "art": it.AlbumArtURL,
						"mime": it.MimeType, "duration_sec": it.DurationSec,
					})
				}
				break
			}
			if udn != "" {
				break
			}
		}
		if udn != "" {
			break
		}
	}
	if udn == "" || len(items) < 2 {
		t.Skip("no folder with two or more playable tracks found on this LAN")
	}
	key := "queue:" + udn + ":" + container
	t.Logf("folder %q on %s, %d tracks, card key %s", folderName, udn, len(items), key)

	// 2. Start it the way the Library tab does, with the same card identity.
	payload, _ := json.Marshal(map[string]any{
		"items": items, "start": 0, "shuffle": false, "repeat": "off",
		"card": map[string]any{"key": key, "name": folderName, "art": items[0]["art"]},
	})
	if err := a.StartQueue(host, port, string(payload)); err != nil {
		t.Fatalf("StartQueue: %v", err)
	}
	time.Sleep(8 * time.Second)
	_ = a.SetBoxVolume(host, port, 5) // re-assert: a fresh play can carry its own level

	// 3. Stop, so the replay starts from a speaker that is not already playing
	// the folder, which is the situation the Recently-played button is used in.
	if err := a.Stop(host, port); err != nil {
		t.Logf("stop: %v (continuing)", err)
	}
	time.Sleep(3 * time.Second)

	// 4. The card is what the button acts on. It must carry the folder key.
	if !liveRecentHasCard(t, a, host, port, key) {
		t.Fatalf("the folder did not land in Recently played under %q", key)
	}

	// 5. The button itself.
	if err := a.ReplayFolderCard(host, port, key, folderName, ""); err != nil {
		t.Fatalf("ReplayFolderCard: %v", err)
	}
	time.Sleep(6 * time.Second)
	_ = a.SetBoxVolume(host, port, 5)

	// 6. The speaker must hold a QUEUE now, not a single track. This is the whole
	// difference: before the fix the replay pushed one URL and cleared the queue.
	n := liveQueueLen(t, a, host, port)
	if n < 2 {
		t.Fatalf("after the replay the speaker holds %d queue tracks, want the whole folder (%d)", n, len(items))
	}
	t.Logf("replayed as a queue of %d tracks", n)

	// Leave the room quiet.
	_ = a.Stop(host, port)
}

func liveRecentHasCard(t *testing.T, a *App, host string, port int, key string) bool {
	t.Helper()
	resp, err := a.boxDoTimeout(host, port, http.MethodGet, "/api/recent", "", "", 8*time.Second)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	defer resp.Body.Close()
	var entries []struct {
		CardKey string `json:"cardKey"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatalf("recent decode: %v", err)
	}
	for _, e := range entries {
		if strings.EqualFold(e.CardKey, key) {
			return true
		}
	}
	return false
}

func liveQueueLen(t *testing.T, a *App, host string, port int) int {
	t.Helper()
	resp, err := a.boxDoTimeout(host, port, http.MethodGet, "/api/queue", "", "", 8*time.Second)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	defer resp.Body.Close()
	var snap struct {
		Active bool `json:"active"`
		Items  []struct {
			Title string `json:"title"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("queue decode: %v", err)
	}
	if !snap.Active {
		return 0
	}
	return len(snap.Items)
}
