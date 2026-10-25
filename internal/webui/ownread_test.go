package webui

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// The quiet wake and the upload hush read the speaker's OWN firmware, at
// 127.0.0.1 on the box. In v0.9.89 both were switched to the guarded peer
// read, which refuses loopback by design, so on every speaker they read an
// empty snapshot: the quiet wake's first-life mute never fired and its STOP of
// the firmware's resume never went out, so the station the master resumed when
// two idle speakers were paired ran on until the late check (#1495). The upload
// hush never saw anything playing either.
//
// This one needs nothing but the source, so it always runs.
func TestOwnBoxReadsNeverGoThroughThePeerGuard(t *testing.T) {
	t.Parallel()
	for file, fn := range map[string]string{
		"server.go":        "quietWakeNowPlaying",
		"hushforupload.go": "hushReadNowPlaying",
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		body := funcBody(t, string(src), fn)
		if body == "" {
			t.Fatalf("%s not found in %s", fn, file)
		}
		if strings.Contains(body, "fetchNowPlayingPeer(") {
			t.Errorf("%s reads the speaker's own firmware through the peer guard, which refuses 127.0.0.1", fn)
		}
		if !strings.Contains(body, "ownNowPlaying(ctx, s.boxHost)") {
			t.Errorf("%s must read the speaker this agent runs on (ownNowPlaying with s.boxHost)", fn)
		}
	}
	if isLANPeer("127.0.0.1") {
		t.Fatal("the peer guard now lets loopback through; the reason for ownNowPlaying changed")
	}
}

// The same question asked of the real read: with the firmware answering on
// 127.0.0.1:8090, the production path (no seam installed) has to see what it
// plays. Skipped where that port is taken.
func TestOwnBoxReadsReachTheFirmwareOnLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:8090")
	if err != nil {
		t.Skipf("127.0.0.1:8090 not available here: %v", err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`<nowPlaying deviceID="device-id-here" source="LOCAL_INTERNET_RADIO"><playStatus>PLAY_STATE</playStatus></nowPlaying>`))
		}),
		ReadHeaderTimeout: time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	s := &Server{boxHost: "127.0.0.1"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if np := s.quietWakeNowPlaying(ctx, s.boxHost); np.Source != "LOCAL_INTERNET_RADIO" || np.PlayStatus != "PLAY_STATE" {
		t.Errorf("quiet wake read of the own speaker = %+v, want LOCAL_INTERNET_RADIO / PLAY_STATE", np)
	}
	if np := s.hushReadNowPlaying(ctx, s.boxHost); np.Source != "LOCAL_INTERNET_RADIO" {
		t.Errorf("upload hush read of the own speaker = %+v, want LOCAL_INTERNET_RADIO", np)
	}
}
