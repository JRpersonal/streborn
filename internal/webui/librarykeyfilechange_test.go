package webui

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// A library key whose file is replaced under the same URL every night (a
// recording on MiniDLNA). The key kept the length the first night's file had
// when it was saved, the speaker was handed that length, echoed it back as its
// total, and the end watch stopped the speaker at 7:09 every night while the
// recording still played (posSec=431 trackSec=429, the speaker's buffer full).

// swappableFile serves one file the way a media server does (size, byte
// ranges, HEAD) and lets the test replace it in place. It counts the ranged
// GETs, which is what a length read costs.
type swappableFile struct {
	mu     sync.Mutex
	body   []byte
	ranged atomic.Int32
}

func (f *swappableFile) set(b []byte) {
	f.mu.Lock()
	f.body = b
	f.mu.Unlock()
}

func (f *swappableFile) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	body := f.body
	f.mu.Unlock()
	if r.Method == http.MethodGet && r.Header.Get("Range") != "" {
		f.ranged.Add(1)
	}
	w.Header().Set("Content-Type", "audio/mp4")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

// loopbackKeyLengthClient lets a server's key length check reach a test media
// server (the production client refuses loopback).
func loopbackKeyLengthClient(s *Server) {
	s.keyLengthClientFn = func() *http.Client { return &http.Client{Timeout: customFileProbeStep} }
}

// activeFileTrack is the lone file the end watch was armed with.
func activeFileTrack(t *testing.T, s *Server) singleTrack {
	t.Helper()
	s.activeFileMu.Lock()
	defer s.activeFileMu.Unlock()
	if s.activeFile == nil {
		t.Fatal("no end watch was armed")
	}
	return s.activeFile.tr
}

func TestALibraryKeyPlaysAFileReplacedUnderItsURLWithItsCurrentLength(t *testing.T) {
	s, rec := newDIDLTestServer(t)
	loopbackKeyLengthClient(s)
	var logs lockedBuffer
	s.logger = slog.New(slog.NewTextHandler(&logs, nil))
	file := &swappableFile{body: m4aFile(25*time.Minute, true, 64*1024)}
	media := httptest.NewServer(file)
	defer media.Close()

	const slot = 3
	if err := s.presets.SetSlot(presets.Preset{
		Slot: slot, Name: "latest", Type: "radio", StreamURL: media.URL + "/MediaItems/1.m4a",
		Source: "MiniDLNA", DurationSec: 429, // the first night's recording
	}); err != nil {
		t.Fatal(err)
	}
	if !s.RecallSlot(context.Background(), slot) {
		t.Fatal("RecallSlot did not claim the library track key")
	}
	if body := rec.first(); !strings.Contains(body, "0:25:00") || strings.Contains(body, "0:07:09") {
		t.Fatalf("the speaker was not told the file's current 25 min: %s", body)
	}
	tr := activeFileTrack(t, s)
	if tr.dur != 25*time.Minute || tr.claimedLength {
		t.Fatalf("end watch armed with dur=%v claimed=%v, want 25m0s read from the file", tr.dur, tr.claimedLength)
	}
	if n := strings.Count(logs.String(), "the file changed since the key was saved"); n != 1 {
		t.Fatalf("changed-file line logged %d times, want once:\n%s", n, logs.String())
	}
	reads := file.ranged.Load()
	if reads == 0 {
		t.Fatal("the length was not read from the file")
	}

	// The same file again: one HEAD, no length read, no second log line.
	if !s.RecallSlot(context.Background(), slot) {
		t.Fatal("second RecallSlot did not claim the key")
	}
	if got := file.ranged.Load(); got != reads {
		t.Fatalf("an unchanged file was read again: %d ranged GETs, want %d", got, reads)
	}
	if tr := activeFileTrack(t, s); tr.dur != 25*time.Minute {
		t.Fatalf("cached length = %v, want 25m0s", tr.dur)
	}
	if n := strings.Count(logs.String(), "the file changed since the key was saved"); n != 1 {
		t.Fatalf("changed-file line logged %d times after a cache hit, want once", n)
	}

	// The next night's recording is shorter: a new size, a new read, and the
	// key ends on time instead of waiting out the old length.
	file.set(m4aFile(3*time.Minute, true, 32*1024))
	if !s.RecallSlot(context.Background(), slot) {
		t.Fatal("third RecallSlot did not claim the key")
	}
	if got := file.ranged.Load(); got == reads {
		t.Fatal("a replaced file was not read again")
	}
	if tr := activeFileTrack(t, s); tr.dur != 3*time.Minute || tr.claimedLength {
		t.Fatalf("after the swap dur=%v claimed=%v, want 3m0s from the file", tr.dur, tr.claimedLength)
	}
}

func TestALibraryKeyKeepsItsStoredLengthWhenTheFileCannotBeAsked(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"server down", nil},
		{"HEAD 405 and no ranges", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "audio/mp4")
			w.WriteHeader(http.StatusOK) // ignores the range
			_, _ = w.Write(make([]byte, 1024))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, rec := newDIDLTestServer(t)
			loopbackKeyLengthClient(s)
			var url string
			if tc.handler == nil {
				dead := httptest.NewServer(http.NotFoundHandler())
				url = dead.URL + "/MediaItems/1.m4a"
				dead.Close()
			} else {
				srv := httptest.NewServer(tc.handler)
				defer srv.Close()
				url = srv.URL + "/MediaItems/1.m4a"
			}
			if err := s.presets.SetSlot(presets.Preset{
				Slot: 3, Name: "latest", Type: "radio", StreamURL: url,
				Source: "MiniDLNA", DurationSec: 429,
			}); err != nil {
				t.Fatal(err)
			}
			if !s.RecallSlot(context.Background(), 3) {
				t.Fatal("RecallSlot did not claim the library track key")
			}
			if body := rec.first(); !strings.Contains(body, "0:07:09") {
				t.Fatalf("the stored length was not the fallback: %s", body)
			}
			// A length nobody read from the file is only a claim: the end
			// watch must not take the speaker's echo of it as the end.
			if tr := activeFileTrack(t, s); tr.dur != 429*time.Second || !tr.claimedLength {
				t.Fatalf("end watch armed with dur=%v claimed=%v, want the stored 429s as a claim", tr.dur, tr.claimedLength)
			}
		})
	}
}

// The watch side of the same bug, for a key whose file could not be asked:
// the speaker echoes the stored length as its total and keeps playing past it.
// No Stop while the position climbs; the end comes when it freezes.
func TestAKeyWithAStaleStoredLengthIsNotStoppedWhileTheSpeakerStillPlays(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	// A stored length is whole seconds, so the track runs in real seconds on
	// the fast poll.
	const storedLen = 1 * time.Second
	const realEnd = 2 * time.Second
	if err := s.presets.SetSlot(presets.Preset{
		Slot: 3, Name: "latest", Type: "radio", StreamURL: libraryTrackURL,
		Source: "MiniDLNA", DurationSec: int(storedLen / time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	np.set("PLAY_STATE", 0, storedLen) // the speaker echoes STR's length
	start := time.Now()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		// The speaker plays in real time past the stored length to the
		// file's real end, and freezes there.
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			pos := time.Since(start) - 50*time.Millisecond
			if pos > realEnd {
				pos = realEnd
			}
			if pos > 0 {
				np.set("PLAY_STATE", pos, storedLen)
			}
		}
	}()
	if !s.RecallSlot(context.Background(), 3) {
		t.Fatal("RecallSlot did not claim the library track key")
	}
	waitForTrackEnd(t, "the box to be stopped at the file's real end", func() bool { return rec.has("Stop") })
	if took := time.Since(start); took < realEnd {
		t.Fatalf("the speaker was stopped after %v, while it still played past the stored %v", took, storedLen)
	}
}
