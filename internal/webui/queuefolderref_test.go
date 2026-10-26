package webui

import (
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/mediaservers"
)

// A folder saved on a key from the queue that plays keeps the server and the
// folder's container id its card names, so a later heal can browse the folder
// again by id when the server changed every file path (queuebadurl.go).
func TestLiveQueuePresetRecordsTheFolderID(t *testing.T) {
	s := queueSaveTestServer(t)
	reg, err := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(mediaservers.Server{ID: "abc-123", Name: "Router media server"}); err != nil {
		t.Fatal(err)
	}
	s.mediaServers = reg
	s.queue.load(queueSaveTracks, 0, false, repeatOff)
	s.queueFolder = recentCardCtx{key: "queue:uuid:abc-123:64$1", name: "All tracks"}
	p, ok := s.LiveQueuePreset(1)
	if !ok {
		t.Fatal("no preset from the live queue")
	}
	if p.Folder == nil || p.Folder.ServerUDN != "uuid:abc-123" || p.Folder.ContainerID != "64$1" {
		t.Fatalf("folder = %+v, want uuid:abc-123 / 64$1", p.Folder)
	}

	// A card that names no server folder records none.
	for _, key := range []string{"", "folder:http://192.0.2.10/x.mp3", "queue:slot:3"} {
		if ref := s.queueFolderRef(key); ref != nil {
			t.Errorf("card %q recorded folder %+v", key, ref)
		}
	}
}
