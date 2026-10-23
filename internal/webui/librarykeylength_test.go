package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// #978: a key holding one library song kept showing "playing" for ~24 s after
// the song ended. The recall passed no length, the speaker then reported none
// either (trackSec=0), and only the frozen-position net (15 s) could end it.

// didlRecorder keeps the SetAVTransportURI bodies, so a test can read the
// metadata the speaker was handed.
type didlRecorder struct {
	mu     sync.Mutex
	bodies []string
}

func (d *didlRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("SOAPACTION"), "SetAVTransportURI") {
		b, _ := io.ReadAll(r.Body)
		d.mu.Lock()
		d.bodies = append(d.bodies, string(b))
		d.mu.Unlock()
	}
	w.WriteHeader(http.StatusOK)
}

func (d *didlRecorder) first() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.bodies) == 0 {
		return ""
	}
	return d.bodies[0]
}

func newDIDLTestServer(t *testing.T) (*Server, *didlRecorder) {
	t.Helper()
	rec := &didlRecorder{}
	box := httptest.NewServer(rec)
	t.Cleanup(box.Close)
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatalf("presets.Load: %v", err)
	}
	s := &Server{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		presets:  store,
		queue:    newPlayQueue(),
		renderer: &upnp.Renderer{ControlURL: box.URL, Client: box.Client()},
	}
	t.Cleanup(func() { s.stopQueue("test cleanup") })
	return s, rec
}

func TestLibraryKeyRecallHandsTheStoredLengthToTheSpeaker(t *testing.T) {
	s, rec := newDIDLTestServer(t)
	if err := s.presets.SetSlot(presets.Preset{
		Slot: 6, Name: "Song", Type: "radio", StreamURL: libraryTrackURL,
		Source: "MinimServer", DurationSec: 175,
	}); err != nil {
		t.Fatal(err)
	}
	if !s.RecallSlot(context.Background(), 6) {
		t.Fatal("RecallSlot did not claim the library track key")
	}
	body := rec.first()
	if !strings.Contains(body, "0:02:55") {
		t.Fatalf("the speaker was not told the track length: %s", body)
	}
}

func TestLibraryPresetLengthFallsBackToALearnedLength(t *testing.T) {
	s, _ := newPlayTestServer(t)
	p := presets.Preset{Name: "Song", Type: "radio", StreamURL: libraryTrackURL, Source: "MinimServer"}
	if got := s.libraryPresetLength(p); got != 0 {
		t.Fatalf("unknown length = %v, want 0", got)
	}
	s.trackLens.put(libraryTrackURL, 175*time.Second)
	if got := s.libraryPresetLength(p); got != 175*time.Second {
		t.Fatalf("learned length = %v, want 175s", got)
	}
	p.DurationSec = 200
	if got := s.libraryPresetLength(p); got != 200*time.Second {
		t.Fatalf("stored length = %v, want the stored 200s to win", got)
	}
}

func TestPresetSaveFillsALibraryTrackLengthOnlyForLibraryTracks(t *testing.T) {
	s, _ := newPlayTestServer(t)
	s.trackLens.put(libraryTrackURL, 175*time.Second)
	s.trackLens.put("http://stream.example/relax.mp3", 60*time.Second)

	lib := presets.Preset{Type: "radio", StreamURL: libraryTrackURL, Source: "MinimServer"}
	if !s.fillLibraryPresetLength(&lib) || lib.DurationSec != 175 {
		t.Fatalf("library track not filled: %+v", lib)
	}
	sent := presets.Preset{Type: "radio", StreamURL: libraryTrackURL, Source: "MinimServer", DurationSec: 170}
	if s.fillLibraryPresetLength(&sent) || sent.DurationSec != 170 {
		t.Fatalf("a length the app sent was overwritten: %+v", sent)
	}
	radio := presets.Preset{Type: "radio", StreamURL: "http://stream.example/relax.mp3"}
	if s.fillLibraryPresetLength(&radio) || radio.DurationSec != 0 {
		t.Fatalf("a station got a length: %+v", radio)
	}
}

func TestTrackLengthsDropTheOldestPastTheCap(t *testing.T) {
	var tl trackLengths
	for i := 0; i <= trackLengthsCap; i++ {
		tl.put("http://192.0.2.5/"+strconv.Itoa(i)+".mp3", time.Duration(i+1)*time.Second)
	}
	if got := tl.get("http://192.0.2.5/0.mp3"); got != 0 {
		t.Errorf("the oldest entry survived the cap: %v", got)
	}
	if len(tl.byURL) != trackLengthsCap {
		t.Errorf("entries = %d, want %d", len(tl.byURL), trackLengthsCap)
	}
}

// The evidence shape: a 175 s song frozen at its end, the speaker reporting no
// total. With the length known the wall-clock net calls the end on the next
// poll; without it only the frozen net could, 15 s after the freeze.
func TestALibraryTrackWithAKnownLengthEndsOnItsLength(t *testing.T) {
	in := trackEndInput{
		sawPlay:      true,
		playing:      true,
		elapsed:      177 * time.Second,
		dur:          175 * time.Second,
		lastPos:      175 * time.Second,
		sinceLastPos: queuePollInterval,
	}
	if got := trackEndNet(in); got != endNetWallClock {
		t.Fatalf("with a known length: net = %v, want wall-clock", got)
	}
	in.dur = 0
	if got := trackEndNet(in); got != endNetNone {
		t.Fatalf("without a length the end must wait for the frozen net: net = %v", got)
	}
}

// End to end through the key: the stored length ends the song well before the
// frozen-position net could have (which needs the position still for 15 s).
func TestALibraryKeyWithAStoredLengthStopsWithoutWaitingForTheFreeze(t *testing.T) {
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	// A stored length is whole seconds, so this scenario runs in real seconds
	// on the fast poll. The frozen net keeps its production 15 s: that is the
	// time the test must NOT see, and at the fast clock it would fire together
	// with the stored length and prove nothing.
	queueFrozenTimeout *= queueTestScale
	const storedLen = 2 * time.Second
	if err := s.presets.SetSlot(presets.Preset{
		Slot: 6, Name: "Song", Type: "radio", StreamURL: libraryTrackURL,
		Source: "MinimServer", DurationSec: int(storedLen / time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	np.set("PLAY_STATE", storedLen/2, 0) // the speaker reports no total
	start := time.Now()
	if !s.RecallSlot(context.Background(), 6) {
		t.Fatal("RecallSlot did not claim the library track key")
	}
	time.Sleep(betweenPolls(1))
	np.set("PLAY_STATE", storedLen, 0) // frozen at the end

	waitForTrackEnd(t, "the box to be stopped at the end of the key's song", func() bool { return rec.has("Stop") })
	if took := time.Since(start); took >= storedLen+queueFrozenTimeout {
		t.Fatalf("the stop took %v, the frozen net's time: the stored length was not used", took)
	}
}

// newLearnTestServer is newPlayTestServer with the preset store at a path the
// test knows, so it can reload the file or check whether it was written.
func newLearnTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, _ := newPlayTestServer(t)
	path := filepath.Join(t.TempDir(), "presets.json")
	store, err := presets.Load(path)
	if err != nil {
		t.Fatalf("presets.Load: %v", err)
	}
	s.presets = store
	return s, path
}

// presetsFileMtime reads the store file's modification stamp, so a test can
// prove a call wrote nothing.
func presetsFileMtime(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat presets: %v", err)
	}
	return fi.ModTime()
}

// #978 follow-up: a key saved before v1.0.11 has no length. The frozen-position
// net that ends it teaches the length, which is stored on every library key of
// that file without one, and on nothing else.
func TestAFrozenEndTeachesOldLibraryKeysTheirLength(t *testing.T) {
	s, path := newLearnTestServer(t)
	other := "http://192.0.2.32:9790/minimserver/*/Music/Album/05*20Track.m4a"
	if err := s.presets.SetSlots([]presets.Preset{
		{Slot: 1, Name: "Song", Type: "radio", StreamURL: libraryTrackURL, Source: "MinimServer"},
		{Slot: 2, Name: "Song again", Type: "radio", StreamURL: libraryTrackURL, Source: "MinimServer"},
		{Slot: 3, Name: "Song stored", Type: "radio", StreamURL: libraryTrackURL, Source: "MinimServer", DurationSec: 170},
		{Slot: 4, Name: "Other song", Type: "radio", StreamURL: other, Source: "MinimServer"},
		{Slot: 5, Name: "Station", Type: "radio", StreamURL: libraryTrackURL},
	}); err != nil {
		t.Fatal(err)
	}
	s.learnSingleTrackLength(singleTrack{boxURL: libraryTrackURL, title: "Song"}, endNetFrozen, 175*time.Second)

	if got := s.trackLens.get(libraryTrackURL); got != 175*time.Second {
		t.Fatalf("learned length = %v, want 175s", got)
	}
	want := map[int]int{1: 175, 2: 175, 3: 170, 4: 0, 5: 0}
	for slot, sec := range want {
		p, _ := s.presets.Get(slot)
		if p.DurationSec != sec {
			t.Errorf("slot %d DurationSec = %d, want %d", slot, p.DurationSec, sec)
		}
	}
	// Survives a restart: the length is on the NAND copy, not only in memory.
	reloaded, err := presets.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := reloaded.Get(1); p.DurationSec != 175 {
		t.Fatalf("reloaded slot 1 DurationSec = %d, want 175", p.DurationSec)
	}
	// The next press plays with the stored length.
	p, _ := s.presets.Get(1)
	if got := s.libraryPresetLength(p); got != 175*time.Second {
		t.Fatalf("libraryPresetLength = %v, want 175s", got)
	}
}

// Nothing is learned or written from a wall-clock end, a play that already
// knew its length, an implausible position, or when no key needs it.
func TestALengthIsLearnedOnlyFromAPlausibleFrozenEnd(t *testing.T) {
	s, path := newLearnTestServer(t)
	if err := s.presets.SetSlot(presets.Preset{Slot: 1, Name: "Song", Type: "radio",
		StreamURL: libraryTrackURL, Source: "MinimServer"}); err != nil {
		t.Fatal(err)
	}
	before := presetsFileMtime(t, path)
	time.Sleep(20 * time.Millisecond) // so a write would move the stamp
	cases := []struct {
		name string
		tr   singleTrack
		net  endNet
		pos  time.Duration
	}{
		{"wall-clock end", singleTrack{boxURL: libraryTrackURL}, endNetWallClock, 175 * time.Second},
		{"length already known", singleTrack{boxURL: libraryTrackURL, dur: 175 * time.Second}, endNetFrozen, 175 * time.Second},
		{"too short", singleTrack{boxURL: libraryTrackURL}, endNetFrozen, 4 * time.Second},
		{"too long", singleTrack{boxURL: libraryTrackURL}, endNetFrozen, 5 * time.Hour},
	}
	for _, c := range cases {
		s.learnSingleTrackLength(c.tr, c.net, c.pos)
		if got := s.trackLens.get(libraryTrackURL); got != 0 {
			t.Fatalf("%s: learned %v", c.name, got)
		}
		if p, _ := s.presets.Get(1); p.DurationSec != 0 {
			t.Fatalf("%s: stored %d", c.name, p.DurationSec)
		}
	}
	// A file no key holds: learned for this run, the store untouched.
	s.learnSingleTrackLength(singleTrack{boxURL: "http://192.0.2.32:9790/x.flac"}, endNetFrozen, 60*time.Second)
	if got := s.trackLens.get("http://192.0.2.32:9790/x.flac"); got != 60*time.Second {
		t.Fatalf("unsaved file: learned %v, want 60s", got)
	}
	if after := presetsFileMtime(t, path); !after.Equal(before) {
		t.Fatal("the preset store was written although no key needed a length")
	}
}

// End to end: a key without a length, played through to the freeze, comes out
// of the frozen-position net with its length stored.
func TestAnOldLibraryKeyLearnsItsLengthAfterOnePlay(t *testing.T) {
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	if err := s.presets.SetSlot(presets.Preset{
		Slot: 6, Name: "Song", Type: "radio", StreamURL: libraryTrackURL, Source: "MinimServer",
	}); err != nil {
		t.Fatal(err)
	}
	// Real seconds for the positions: a learned length is whole seconds and
	// must clear learnedLengthMin. The watch itself runs on the fast poll.
	np.set("PLAY_STATE", 2*time.Second, 0) // the speaker reports no total
	if !s.RecallSlot(context.Background(), 6) {
		t.Fatal("RecallSlot did not claim the library track key")
	}
	time.Sleep(betweenPolls(1))
	np.set("PLAY_STATE", 6*time.Second, 0) // frozen at the end

	waitForTrackEnd(t, "the frozen net to end the key's song", func() bool { return rec.has("Stop") })
	if p, _ := s.presets.Get(6); p.DurationSec != 6 {
		t.Fatalf("slot 6 DurationSec = %d after the play, want 6", p.DurationSec)
	}
}
