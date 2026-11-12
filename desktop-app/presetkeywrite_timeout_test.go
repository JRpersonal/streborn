package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The speaker registers its hardware keys before it answers the bulk preset
// write and the key sync: six writes, one after the other, 5.5 to 5.9 s on a
// SoundTouch 20. Both requests went through the shared 6 s client, which gave
// up on them; the copy was then reported as done while keys on the speaker
// were missing. Both now carry their own timeout, so a client timeout far
// below the speaker's answer time no longer cuts them short.
func TestTheKeyWritesOutliveTheSharedClientTimeout(t *testing.T) {
	const speakerTakes = 400 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(speakerTakes)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "written": 1})
	}))
	defer srv.Close()
	a := newTestApp()
	a.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	a.httpClient.Timeout = 50 * time.Millisecond
	port := listenPort(t, srv)

	if _, err := a.SyncBoxPresets("127.0.0.1", port); err != nil {
		t.Errorf("key sync gave up before the speaker answered: %v", err)
	}
	supported, err := a.putPresetsBulk("127.0.0.1", port, []Preset{{Slot: 1, Name: "A"}})
	if !supported || err != nil {
		t.Errorf("bulk preset write: supported=%v err=%v, want it to wait for the speaker", supported, err)
	}
	if presetKeyWriteTimeout < 15*time.Second {
		t.Errorf("presetKeyWriteTimeout = %v, six paced key writes need well over 6 s", presetKeyWriteTimeout)
	}
}
