package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// The following half of a healthy stereo pair takes a UPnP push and plays
// nothing (mail, 2026-10-26). Every play path must refuse it before the wake,
// name the master, and leave the renderer alone.

func followerOf(id, ip string) func() StereoFollower {
	return func() StereoFollower {
		return StereoFollower{Follower: true, MasterID: id, MasterIP: ip, PairName: "Living room"}
	}
}

func decodeRefusal(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	return resp
}

func assertFollowerRefusal(t *testing.T, resp map[string]any) {
	t.Helper()
	if resp["error"] != "box-grouped" || resp["stereo"] != true {
		t.Errorf("body = %v, want box-grouped with stereo:true", resp)
	}
	if resp["master"] != "AABBCCDDEEFF" || resp["masterIP"] != "192.0.2.10" {
		t.Errorf("body = %v, want the master's id and address", resp)
	}
}

func assertNothingPushed(t *testing.T, rec *soapRecorder) {
	t.Helper()
	if acts := rec.list(); len(acts) != 0 {
		t.Errorf("the follower's renderer was driven anyway: %v", acts)
	}
}

func TestStereoFollowerPlayIsRefusedBeforeAnyPush(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.stereoFollowerFn = followerOf("AABBCCDDEEFF", "192.0.2.10")
	s.peersFn = func(context.Context) []PeerLink {
		t.Error("the refusal started a peer browse under the play lock")
		return nil
	}
	s.cachedPeersFn = func() []PeerLink {
		return []PeerLink{
			{Name: "Kitchen", URL: "http://192.0.2.30:8080/", IP: "192.0.2.30", DeviceID: "998877665544"},
			{Name: "Lounge left", URL: "http://192.0.2.10:8080/", IP: "192.0.2.10", DeviceID: "aabbccddeeff"},
		}
	}

	w := httptest.NewRecorder()
	s.handlePlay(w, httptest.NewRequest(http.MethodPost, "/api/play",
		strings.NewReader(`{"url":"http://192.0.2.20:49000/track.mp3","title":"A track","mime":"audio/mpeg"}`)))

	resp := decodeRefusal(t, w)
	assertFollowerRefusal(t, resp)
	if resp["masterName"] != "Lounge left" || resp["masterURL"] != "http://192.0.2.10:8080/" {
		t.Errorf("body = %v, want the master's name and phone page from the peer list", resp)
	}
	if resp["pairName"] != "Living room" {
		t.Errorf("pairName = %v, want the pair's name", resp["pairName"])
	}
	assertNothingPushed(t, rec)
}

func TestStereoFollowerQueueIsRefusedBeforeAnyPush(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.stereoFollowerFn = followerOf("AABBCCDDEEFF", "192.0.2.10")

	w := httptest.NewRecorder()
	s.handleQueue(w, httptest.NewRequest(http.MethodPost, "/api/queue",
		strings.NewReader(`{"items":[{"url":"http://192.0.2.20:49000/a.mp3","title":"A","mime":"audio/mpeg"}]}`)))

	assertFollowerRefusal(t, decodeRefusal(t, w))
	assertNothingPushed(t, rec)
	if s.queue.isActive() {
		t.Error("a queue was started on the follower")
	}
}

func TestStereoFollowerFolderReplayIsRefusedBeforeBrowsing(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.stereoFollowerFn = followerOf("AABBCCDDEEFF", "192.0.2.10")

	w := httptest.NewRecorder()
	s.handleQueueReplayCard(w, httptest.NewRequest(http.MethodPost, "/api/queue/replay-card",
		strings.NewReader(`{"key":"queue:uuid:0000-1111:42","name":"Album"}`)))

	assertFollowerRefusal(t, decodeRefusal(t, w))
	assertNothingPushed(t, rec)
}

func TestStereoFollowerPresetRecallIsRefusedBeforeTheWake(t *testing.T) {
	s, rec := newPlayTestServer(t)
	seedRadioPreset(t, s, 1)
	s.stereoFollowerFn = followerOf("AABBCCDDEEFF", "192.0.2.10")

	_, ref := s.playSlot(context.Background(), 1)
	if ref == nil || ref.Status != http.StatusConflict || ref.Error != "box-grouped" {
		t.Fatalf("refusal = %+v, want 409 box-grouped", ref)
	}
	assertNothingPushed(t, rec)
}

// The master and a standalone speaker play exactly as before.
func TestStereoMasterAndStandalonePlayAsBefore(t *testing.T) {
	for name, fn := range map[string]func() StereoFollower{
		"unwired":    nil,
		"standalone": func() StereoFollower { return StereoFollower{} },
	} {
		t.Run(name, func(t *testing.T) {
			s, rec := newPlayTestServer(t)
			s.stereoFollowerFn = fn
			w := httptest.NewRecorder()
			s.handlePlay(w, httptest.NewRequest(http.MethodPost, "/api/play",
				strings.NewReader(`{"url":"http://stream.example/relax","title":"Test Station"}`)))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
			}
			if !rec.has("SetAVTransportURI") || !rec.has("Play") {
				t.Errorf("play did not reach the box: %v", rec.list())
			}
		})
	}
}

// A master the peer list does not know still gets its address out; the name
// and link stay empty and the phone page falls back to the address.
func TestStereoFollowerRefusalWithoutPeerList(t *testing.T) {
	s := newStereoTestServer()
	s.stereoFollowerFn = followerOf("AABBCCDDEEFF", "192.0.2.10")
	start := time.Now()
	ref := s.stereoFollowerRefusal()
	if ref == nil {
		t.Fatal("not refused")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("refusal took %s; it must not touch the speaker", d)
	}
	body := ref.JSON.(map[string]any)
	if body["masterIP"] != "192.0.2.10" || body["masterName"] != "" {
		t.Errorf("body = %v, want the address and no invented name", body)
	}
}

// A pair follower that leads a multiroom zone is that zone's master: the
// permanent group only re-forms from a play started there, so refusing it would
// take the group away for good (mail, 2026-10-26: the following ST10 leads the
// household's permanent group).
func TestStereoFollowerLeadingAStoredGroupStillPlays(t *testing.T) {
	s, rec := newPlayTestServer(t)
	store, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(zones.Zone{Master: "112233445566", Permanent: true, Name: "Downstairs",
		Slaves: []zones.Member{{DeviceID: "998877665544", IP: "192.0.2.30"}}}); err != nil {
		t.Fatal(err)
	}
	s.zones = store
	s.fetchZoneFn = func(context.Context, string) (boxapi.Zone, error) {
		t.Error("the stored group already answered; the live zone was read anyway")
		return boxapi.Zone{}, nil
	}
	s.stereoFollowerFn = func() StereoFollower {
		return StereoFollower{Follower: true, MasterID: "AABBCCDDEEFF", MasterIP: "192.0.2.10", SelfID: "112233445566"}
	}
	w := httptest.NewRecorder()
	s.handlePlay(w, httptest.NewRequest(http.MethodPost, "/api/play",
		strings.NewReader(`{"url":"http://stream.example/relax","title":"Test Station"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if !rec.has("SetAVTransportURI") {
		t.Errorf("play did not reach the box: %v", rec.list())
	}
}

func TestStereoFollowerLeadingTheLiveZoneIsNotRefused(t *testing.T) {
	s := newStereoTestServer()
	s.stereoFollowerFn = func() StereoFollower {
		return StereoFollower{Follower: true, MasterID: "AABBCCDDEEFF", SelfID: "112233445566"}
	}
	s.fetchZoneFn = func(context.Context, string) (boxapi.Zone, error) {
		return boxapi.Zone{Master: "112233445566", Members: []boxapi.ZoneMember{{DeviceID: "998877665544"}}}, nil
	}
	if ref := s.stereoFollowerRefusal(); ref != nil {
		t.Fatalf("the live zone's master was refused: %+v", ref)
	}
	// A zone some other speaker leads changes nothing, nor does an unreadable
	// zone.
	s.fetchZoneFn = func(context.Context, string) (boxapi.Zone, error) {
		return boxapi.Zone{Master: "AABBCCDDEEFF"}, nil
	}
	if s.stereoFollowerRefusal() == nil {
		t.Error("a follower of someone else's zone was let through")
	}
	s.fetchZoneFn = func(context.Context, string) (boxapi.Zone, error) {
		return boxapi.Zone{}, errors.New("connection refused")
	}
	if s.stereoFollowerRefusal() == nil {
		t.Error("an unreadable zone let the follower through")
	}
}

// The stereo document is the pair itself, not a zone the follower leads.
func TestStereoFollowerStereoDocumentIsNotAZoneItLeads(t *testing.T) {
	s := newStereoTestServer()
	store, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(zones.Zone{Master: "112233445566", Stereo: true,
		Slaves: []zones.Member{{DeviceID: "AABBCCDDEEFF", IP: "192.0.2.10"}}}); err != nil {
		t.Fatal(err)
	}
	s.zones = store
	s.stereoFollowerFn = func() StereoFollower {
		return StereoFollower{Follower: true, MasterID: "AABBCCDDEEFF", SelfID: "112233445566"}
	}
	if s.stereoFollowerRefusal() == nil {
		t.Error("a stereo document was taken for a zone the follower leads")
	}
}

func TestStereoFollowerResumeIsRefusedBeforeAnyPush(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.stereoFollowerFn = followerOf("AABBCCDDEEFF", "192.0.2.10")
	w := httptest.NewRecorder()
	s.handleResume(w, httptest.NewRequest(http.MethodPost, "/api/resume", nil))
	assertFollowerRefusal(t, decodeRefusal(t, w))
	assertNothingPushed(t, rec)
}

func TestStereoFollowerPandoraIsRefusedBeforeTheSelect(t *testing.T) {
	s, _, box := nativeTestServer(true)
	pandoraLinked(box)
	s.stereoFollowerFn = followerOf("AABBCCDDEEFF", "192.0.2.10")
	w := httptest.NewRecorder()
	s.handlePandoraPlay(w, nativeReq(http.MethodPost, "/api/pandora/play", `{"location":"111"}`, "192.168.1.5:5000", "application/json"))
	assertFollowerRefusal(t, decodeRefusal(t, w))
	if len(box.selected) != 0 {
		t.Errorf("the follower selected a station anyway: %v", box.selected)
	}
}
