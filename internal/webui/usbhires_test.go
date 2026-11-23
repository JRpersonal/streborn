package webui

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// stickFLAC is the start of a FLAC file: "fLaC" and its STREAMINFO block.
func stickFLAC(rate, bits int) []byte {
	si := make([]byte, 34)
	v := uint64(rate)<<44 | uint64(1)<<41 | uint64(bits-1)<<36 | 1000
	binary.BigEndian.PutUint64(si[10:], v)
	return append(append([]byte("fLaC"), 0x80, 0, 0, 34), si...)
}

// stickWAV is a RIFF/WAVE header with its fmt chunk.
func stickWAV(rate, bits int) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00")
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*bits/4))
	_ = binary.Write(&b, binary.LittleEndian, uint16(bits/4))
	_ = binary.Write(&b, binary.LittleEndian, uint16(bits))
	b.WriteString("data\x00\x00\x00\x00")
	return b.Bytes()
}

const stickLoop = "http://127.0.0.1:8888/usb/file/Album%201/"

// hiResStickServer is a play test server whose speaker holds a music stick
// (firmware mount) with a 16/44.1 FLAC, a 24/96 FLAC, a 44.1 WAV and a 96 kHz
// WAV in "Album 1".
func hiResStickServer(t *testing.T) (*Server, *soapRecorder) {
	t.Helper()
	f := newFakeStick(t, "taigan")
	media := t.TempDir()
	album := filepath.Join(media, "Album 1")
	if err := os.MkdirAll(album, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"02 Test.flac":    stickFLAC(44100, 16),
		"hires-2496.flac": stickFLAC(96000, 24),
		"05.wav":          stickWAV(44100, 16),
		"wav-96k.wav":     stickWAV(96000, 24),
	} {
		if err := os.WriteFile(filepath.Join(album, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.plug(fatSector("fat32", 64))
	f.firmwareMount(media, "vfat")
	s, rec := newPlayTestServer(t)
	// A folder start saves the sticky play mode; keep it out of /mnt/nv.
	s.playModePath = filepath.Join(t.TempDir(), "play-mode")
	t.Cleanup(func() {
		s.usb.mu.Lock()
		s.usbDropLocked("test end")
		s.usb.mu.Unlock()
	})
	return s, rec
}

func TestAHiResStickFileIsRefusedBeforeTheSpeakerIsTouched(t *testing.T) {
	s, rec := hiResStickServer(t)
	for _, name := range []string{"hires-2496.flac", "wav-96k.wav"} {
		w := postPlayBody(t, s, `{"url":"`+stickLoop+name+`","title":"`+name+`"}`)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status %d %s", name, w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["code"] != libraryFileHiResCode || body["sampleRate"] != float64(96000) ||
			!strings.Contains(body["error"].(string), "above 48 kHz") {
			t.Fatalf("%s: body %v", name, body)
		}
	}
	if n := rec.count(); n != 0 {
		t.Fatalf("the speaker must not be handed a hi-res file: %v", rec.list())
	}
	// 44.1 kHz plays as before.
	if w := postPlayBody(t, s, `{"url":"`+stickLoop+`02%20Test.flac","title":"02 Test"}`); w.Code != http.StatusOK {
		t.Fatalf("a 44.1 kHz FLAC must play: %d %s", w.Code, w.Body.String())
	}
	if rec.count() == 0 {
		t.Fatal("the 44.1 kHz file must reach the speaker")
	}
}

func postQueue(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleQueue(w, httptest.NewRequest(http.MethodPost, "/api/queue", strings.NewReader(body)))
	return w
}

// A folder steps over the hi-res tracks instead of stalling on them, keeps
// them in its list (so the key that holds the folder still matches it), and
// says how many it stepped over.
func TestAFolderStepsOverHiResStickFiles(t *testing.T) {
	s, _ := hiResStickServer(t)
	w := postQueue(t, s, `{"items":[`+
		`{"url":"`+stickLoop+`hires-2496.flac","title":"hires"},`+
		`{"url":"`+stickLoop+`02%20Test.flac","title":"02 Test"},`+
		`{"url":"`+stickLoop+`wav-96k.wav","title":"wav96"},`+
		`{"url":"`+stickLoop+`05.wav","title":"05"}],"start":0,"shuffle":false,"repeat":"off"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("queue: %d %s", w.Code, w.Body.String())
	}
	var snap queueSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.SkippedHiRes != 2 || len(snap.Items) != 4 || !snap.Items[0].Skipped || snap.Items[1].Skipped ||
		!snap.Items[2].Skipped || snap.Pos != 1 {
		t.Fatalf("snapshot: %+v", snap)
	}
	if it, _ := s.queue.current(); it.Title != "02 Test" {
		t.Fatalf("the folder must start on the first track the speaker can play, got %q", it.Title)
	}
	if it, ok := s.queue.next(); !ok || it.Title != "05" {
		t.Fatalf("next must step over the 96 kHz WAV, got %q %v", it.Title, ok)
	}
	if it, _ := s.queue.prev(); it.Title != "02 Test" {
		t.Fatalf("prev must step over it too, got %q", it.Title)
	}

	// A folder of nothing else is refused like a single file.
	w = postQueue(t, s, `{"items":[{"url":"`+stickLoop+`hires-2496.flac","title":"hires"},`+
		`{"url":"`+stickLoop+`wav-96k.wav","title":"wav96"}]}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), libraryFileHiResCode) {
		t.Fatalf("all hi-res: %d %s", w.Code, w.Body.String())
	}
}

// A song key on a hi-res stick file is refused before the wake.
func TestASongKeyOnAHiResStickFileIsRefused(t *testing.T) {
	s, _ := hiResStickServer(t)
	p := presets.Preset{Slot: 2, Name: "hires", Type: "radio", StreamURL: stickLoop + "hires-2496.flac"}
	ref := s.recallRefusal(2, &p)
	if ref == nil || ref.Code != libraryFileHiResCode || ref.Status != http.StatusUnprocessableEntity {
		t.Fatalf("refusal: %+v", ref)
	}
	p = presets.Preset{Slot: 2, Name: "ok", Type: "radio", StreamURL: stickLoop + "02%20Test.flac"}
	if ref := s.recallRefusal(2, &p); ref != nil {
		t.Fatalf("a 44.1 kHz key must pass: %+v", ref)
	}
}

// The speaker refusing to decode a stick file is shown by GET /api/queue
// while that file is still the play on record, and not after another play.
func TestADecoderErrorOnAStickFileIsShown(t *testing.T) {
	s, _ := hiResStickServer(t)
	const loop = stickLoop + "alac.m4a"
	s.setLastPlay(loop, "alac", "", "audio/mp4")
	s.NoteBoxError("3108", "AUDIO_ERROR_DECODER", "")
	got := queueAnswer(t, s)
	ref, ok := got["usbRefused"].(map[string]any)
	if !ok || ref["title"] != "alac" || ref["reason"] != "decoder" {
		t.Fatalf("usbRefused: %v", got)
	}
	if snap := s.USBMusicDebug().(usbMusicDebugView).usbMusicSnapshot; !strings.Contains(snap.LastDecoderError, "AUDIO_ERROR_DECODER") {
		t.Fatalf("debug: %+v", snap)
	}
	// Another play takes it away; a decoder error on a NAS file is not the stick's.
	s.setLastPlay("http://192.0.2.50:9000/a.flac", "nas", "", "audio/flac")
	if _, ok := queueAnswer(t, s)["usbRefused"]; ok {
		t.Fatal("the note must go once something else plays")
	}
	s.NoteBoxError("3108", "AUDIO_ERROR_DECODER", "")
	if _, ok := queueAnswer(t, s)["usbRefused"]; ok {
		t.Fatal("a NAS file's decoder error is not the stick's")
	}
}

// The queue's own stepping, without a speaker.
func TestThePlayQueueStepsOverSkippedTracks(t *testing.T) {
	mk := func(skip ...bool) []queueItem {
		out := make([]queueItem, len(skip))
		for i, sk := range skip {
			out[i] = queueItem{URL: string(rune('a' + i)), Title: string(rune('a' + i)), Skip: sk}
		}
		return out
	}
	q := newPlayQueue()
	// a(skip) b c(skip) d, repeat all.
	q.load(mk(true, false, true, false), 0, false, repeatAll)
	if it, _ := q.current(); it.Title != "b" {
		t.Fatalf("start: %q", it.Title)
	}
	if it, _ := q.peekNatural(); it.Title != "d" {
		t.Fatalf("peek: %q", it.Title)
	}
	if it, _ := q.next(); it.Title != "d" {
		t.Fatalf("next: %q", it.Title)
	}
	if it, _ := q.next(); it.Title != "b" {
		t.Fatalf("wrap: %q", it.Title)
	}
	if it, _ := q.prev(); it.Title != "d" {
		t.Fatalf("prev wrap: %q", it.Title)
	}
	// Repeat off: the end comes after the last playable track.
	q.load(mk(false, false, true), 1, false, repeatOff)
	if _, ok := q.advanceNatural(); ok || q.isActive() {
		t.Fatal("past the last playable track the queue ends")
	}
	// Prev at the front stays on the first playable track.
	q.load(mk(true, false, false), 2, false, repeatOff)
	q.prev()
	if it, _ := q.prev(); it.Title != "b" {
		t.Fatalf("front: %q", it.Title)
	}
	// Shuffle never lands on a skipped track.
	for i := 0; i < 20; i++ {
		q.load(mk(true, false, true, false, true), -1, true, repeatAll)
		for j := 0; j < 6; j++ {
			it, ok := q.next()
			if !ok || it.Skip {
				t.Fatalf("shuffle landed on %+v %v", it, ok)
			}
		}
	}
	// Nothing playable: not active.
	q.load(mk(true, true), 0, false, repeatAll)
	if q.isActive() {
		t.Fatal("a queue of skipped tracks must not be active")
	}
}

// waitDisplayAttempt waits for the display message decision of kind, which
// ShowKeyMessage takes beside the caller.
func waitDisplayAttempt(t *testing.T, s *Server, kind DisplayMsgKind) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.displayMsg.mu.Lock()
		got := s.displayMsg.attempt.Kind
		s.displayMsg.mu.Unlock()
		if got == kind {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no display message %q was asked for", kind)
}

// A hardware press on a key whose stick file (or whole folder) is above
// 48 kHz asks the display to say so. The switch is off here, so the guard
// records the decision without touching the speaker.
func TestAHardwareKeyOnAHiResStickFileAsksTheDisplay(t *testing.T) {
	s, _ := hiResStickServer(t)
	off := filepath.Join(t.TempDir(), "display-messages")
	if err := os.WriteFile(off, []byte("off"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.displayMsg.path = off

	p := presets.Preset{Slot: 2, Name: "hires", Type: "radio", StreamURL: stickLoop + "hires-2496.flac"}
	if !s.recallLibraryFileSlot(context.Background(), 2, p) {
		t.Fatal("the press must be claimed")
	}
	waitDisplayAttempt(t, s, DisplayMsgUSBHiRes)

	s.displayMsg.mu.Lock()
	s.displayMsg.attempt = displayMsgRecord{}
	s.displayMsg.mu.Unlock()
	folder := presets.Preset{Slot: 3, Name: "hires folder", Type: "queue", Items: []presets.PresetItem{
		{URL: stickLoop + "hires-2496.flac", Title: "hires"},
		{URL: stickLoop + "wav-96k.wav", Title: "wav96"},
	}}
	if err := s.presets.SetSlot(folder); err != nil {
		t.Fatal(err)
	}
	if !s.RecallSlot(context.Background(), 3) {
		t.Fatal("the folder press must be claimed")
	}
	waitDisplayAttempt(t, s, DisplayMsgUSBHiRes)
}
