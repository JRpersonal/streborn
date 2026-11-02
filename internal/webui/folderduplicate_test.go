package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// One music-library folder, one key (#1695). An album played from key 3, and a
// press on key 6 in the app was taken as a hold: the live-queue save stored the
// same album on key 6 as well. A station has refused that since #836; a folder
// now gets the same refusal, with the same 409 body, so the app says "already
// on key 3" and offers to move it.

const (
	dupServerUDN  = "uuid:00000000-0000-0000-0000-00000000000a"
	dupOtherUDN   = "uuid:00000000-0000-0000-0000-00000000000b"
	dupContainer  = "0$1$7"
	dupFolderName = "Power Windows"
)

// dupFolderOnKey3 is the album as key 3 holds it: the tracks the live queue
// plays (queueSaveTracks) on the MinimServer the card names.
func dupFolderOnKey3(t *testing.T, s *Server) presets.Preset {
	t.Helper()
	p := presets.Preset{
		Slot: 3, Name: dupFolderName, Type: "queue", Source: "MinimServer",
		Folder: &presets.FolderRef{ServerUDN: dupServerUDN, ContainerID: dupContainer},
		Items: []presets.PresetItem{
			{URL: queueSaveTracks[0].URL, Title: queueSaveTracks[0].Title},
			{URL: queueSaveTracks[2].URL, Title: queueSaveTracks[2].Title},
		},
	}
	if err := s.presets.SetSlot(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func postQueueSaveSlot(s *Server, slot int) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.handleQueueSaveSlot(rec, httptest.NewRequest(http.MethodPost, "/api/queue/save-slot",
		strings.NewReader(fmt.Sprintf(`{"slot":%d}`, slot))))
	return rec
}

// wantAlreadyOnSlot pins the exact body of the station guard's 409, which the
// desktop app (presetmove.js) and the phone remote (presetConflict) parse.
func wantAlreadyOnSlot(t *testing.T, rec *httptest.ResponseRecorder, slot int, name string) {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	want := map[string]any{"code": "already-on-slot", "slot": float64(slot), "name": name}
	if len(body) != len(want) {
		t.Fatalf("body = %v, want exactly %v", body, want)
	}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("body[%q] = %v, want %v (body %v)", k, body[k], v, body)
		}
	}
}

// The reported case: the album was started from key 3 (its card is
// "queue:slot:3"), and a hold on key 6 must not put it there as well.
func TestQueueSaveSlotRefusesTheFolderOfAnotherKey(t *testing.T) {
	s := queueSaveTestServer(t)
	dupFolderOnKey3(t, s)
	radio := presets.Preset{Slot: 6, Name: "Rock FM", Type: "radio", StreamURL: "http://radio.example.com/rock"}
	if err := s.presets.SetSlot(radio); err != nil {
		t.Fatal(err)
	}
	s.queue.load(queueSaveTracks, 0, false, repeatOff)
	s.queueFolder = recentCardCtx{key: "queue:slot:3", name: dupFolderName}

	wantAlreadyOnSlot(t, postQueueSaveSlot(s, 6), 3, dupFolderName)
	if p, _ := s.presets.Get(6); p.Name != "Rock FM" || p.Type != "radio" {
		t.Fatalf("key 6 = %+v, want it untouched", p)
	}

	// Saving it onto its own key again is fine.
	if rec := postQueueSaveSlot(s, 3); rec.Code != http.StatusOK {
		t.Fatalf("re-save onto key 3: %d %s", rec.Code, rec.Body.String())
	}
}

// The same folder started from the Library (not from the key): its card names
// the server and the container, and the key's folder identity matches.
func TestQueueSaveSlotRefusesTheSameFolderPlayedFromTheLibrary(t *testing.T) {
	s := queueSaveTestServer(t)
	dupFolderOnKey3(t, s)
	s.queue.load(queueSaveTracks, 0, true, repeatOff)
	s.queueFolder = recentCardCtx{key: "queue:" + dupServerUDN + ":" + dupContainer, name: dupFolderName, source: "MinimServer"}
	wantAlreadyOnSlot(t, postQueueSaveSlot(s, 6), 3, dupFolderName)
	if _, ok := s.presets.Get(6); ok {
		t.Fatal("key 6 was written")
	}
}

// The reporter's wish: the same album from a DIFFERENT media server is a second
// copy on purpose and stays allowed.
func TestQueueSaveSlotAllowsTheSameAlbumFromAnotherServer(t *testing.T) {
	s := queueSaveTestServer(t)
	dupFolderOnKey3(t, s)
	other := []queueItem{
		{URL: "http://192.0.2.20:8200/MediaItems/01.flac", Title: "The Big Money"},
		{URL: "http://192.0.2.20:8200/MediaItems/02.flac", Title: "Grand Designs"},
	}
	s.queue.load(other, 0, false, repeatOff)
	s.queueFolder = recentCardCtx{key: "queue:" + dupOtherUDN + ":" + dupContainer, name: dupFolderName, source: "Other NAS"}
	if rec := postQueueSaveSlot(s, 6); rec.Code != http.StatusOK {
		t.Fatalf("other server: %d %s", rec.Code, rec.Body.String())
	}
	if p, ok := s.presets.Get(6); !ok || p.Type != "queue" || p.Source != "Other NAS" {
		t.Fatalf("key 6 = %+v", p)
	}
}

// A folder an older agent already left on two keys can still be saved onto
// either of them: re-saving onto its own key never collides.
func TestQueueSaveSlotResavesAFolderAlreadyOnTwoKeys(t *testing.T) {
	s := queueSaveTestServer(t)
	p := dupFolderOnKey3(t, s)
	p.Slot = 6
	if err := s.presets.SetSlot(p); err != nil {
		t.Fatal(err)
	}
	s.queue.load(queueSaveTracks, 0, false, repeatOff)
	s.queueFolder = recentCardCtx{key: "queue:slot:6", name: dupFolderName}
	if rec := postQueueSaveSlot(s, 6); rec.Code != http.StatusOK {
		t.Fatalf("re-save onto key 6: %d %s", rec.Code, rec.Body.String())
	}
}

// The phone remote's hold and the desktop app's save-what-plays answer with the
// PUT's own 409 body, which both turn into the move offer.
func TestSavePlayingRefusesTheFolderOfAnotherKey(t *testing.T) {
	s := newSavePlayingServer(t, nowPlayingDoc("UPNP", queueSaveTracks[0].URL, "Come Together", ""))
	dupFolderOnKey3(t, s)
	s.queue.load(queueSaveTracks, 0, false, repeatOff)
	s.queueFolder = recentCardCtx{key: "queue:slot:3", name: dupFolderName}
	rec, _ := postSavePlaying(t, s, 6)
	wantAlreadyOnSlot(t, rec, 3, dupFolderName)
	if _, ok := s.presets.Get(6); ok {
		t.Fatal("key 6 was written")
	}
	if rec, out := postSavePlaying(t, s, 3); rec.Code != http.StatusOK || out["unchanged"] != true {
		t.Fatalf("re-save onto key 3: %d %s", rec.Code, rec.Body.String())
	}
}

// The Library's own "save this folder to a key" is a save gesture too
// (?save=live). The PUTs that re-write what the user already had (a backup
// restore, the keys kept over a reinstall) are not judged by the rule, so a
// folder an older agent let onto two keys comes back on both.
func TestFolderPutRefusesADuplicateOnlyForASaveGesture(t *testing.T) {
	s := newSaveGateServer(t)
	dupFolderOnKey3(t, s)
	body := fmt.Sprintf(`{"name":%q,"type":"queue","source":"MinimServer","folder":{"server_udn":%q,"container_id":%q},"items":[{"url":%q}]}`,
		dupFolderName, dupServerUDN, dupContainer, queueSaveTracks[0].URL)

	rec := httptest.NewRecorder()
	s.handlePresetSlot(rec, httptest.NewRequest(http.MethodPut, "/api/presets/6?save=live", strings.NewReader(body)))
	wantAlreadyOnSlot(t, rec, 3, dupFolderName)
	if _, ok := s.presets.Get(6); ok {
		t.Fatal("key 6 was written by a refused save")
	}

	if rec := putPreset(t, s, 6, body); rec.Code != http.StatusOK {
		t.Fatalf("restore PUT: %d %s", rec.Code, rec.Body.String())
	}
	if p, ok := s.presets.Get(6); !ok || p.Type != "queue" {
		t.Fatalf("key 6 = %+v after the restore", p)
	}
}

// A held hardware key is "put it HERE": as for a station, the folder comes
// over from the key it sat on and that key gets what the held one had.
func TestHoldSaveLiveFolderOnAnotherKeySwaps(t *testing.T) {
	s := newHoldTestServer(t, "UPNP", queueSaveTracks[0].URL)
	dupFolderOnKey3(t, s)
	if err := s.presets.SetSlot(presets.Preset{Slot: 6, Name: "Rock FM", Type: "radio", StreamURL: "http://radio.example.com/rock"}); err != nil {
		t.Fatal(err)
	}
	s.queue.load(queueSaveTracks, 0, false, repeatOff)
	s.queueFolder = recentCardCtx{key: "queue:slot:3", name: dupFolderName}
	saved, err := s.HoldSaveLive(context.Background(), 6)
	if err != nil || saved.SwappedWith != 3 || saved.SwappedName != "Rock FM" || saved.Name != dupFolderName {
		t.Fatalf("saved = %+v err = %v, want a swap with key 3", saved, err)
	}
	if p, _ := s.presets.Get(6); p.Type != "queue" || p.Name != dupFolderName {
		t.Fatalf("key 6 = %+v, want the folder", p)
	}
	if p, _ := s.presets.Get(3); p.Name != "Rock FM" {
		t.Fatalf("key 3 = %+v, want the station key 6 had", p)
	}
	n := 0
	for _, p := range s.presets.All() {
		if p.Type == "queue" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the folder sits on %d keys, want 1", n)
	}
}

// The tap+hold save onto row 2 follows the same rule.
func TestSaveSnapshotRefusesTheFolderOfAnotherKey(t *testing.T) {
	s := newSavePlayingServer(t, nil)
	dupFolderOnKey3(t, s)
	s.queue.load(queueSaveTracks, 0, true, repeatOff)
	s.queueFolder = recentCardCtx{key: "queue:slot:3", name: dupFolderName}
	snap := s.SnapshotPlaying(nowPlayingDoc("UPNP", queueSaveTracks[0].URL, "Come Together", ""))
	var refusal *HoldSaveError
	if _, err := s.SaveSnapshot(context.Background(), snap, 9); !errors.As(err, &refusal) ||
		refusal.Code != "already-on-slot" || refusal.OtherSlot != 3 || refusal.OtherName != dupFolderName {
		t.Fatalf("err = %v, want already-on-slot 3", err)
	}
}

// The app's answer to the refusal is "Move it to key 6": the move endpoint
// carries the whole folder over, tracks and identity included, and frees key 3.
func TestPresetMoveCarriesAFolderAcross(t *testing.T) {
	s := newMoveServer(t)
	orig := dupFolderOnKey3(t, s)
	rec := movePreset(t, s, `{"from":3,"to":6}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move 3 -> 6: %d %s", rec.Code, rec.Body.String())
	}
	if _, still := s.presets.Get(3); still {
		t.Fatal("key 3 still holds the folder")
	}
	p, ok := s.presets.Get(6)
	if !ok || p.Type != "queue" || p.Name != dupFolderName || p.Source != "MinimServer" ||
		len(p.Items) != len(orig.Items) || p.Folder == nil || *p.Folder != *orig.Folder {
		t.Fatalf("key 6 = %+v, want the whole folder", p)
	}
}
