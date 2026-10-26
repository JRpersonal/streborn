package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
