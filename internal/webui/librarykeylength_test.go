package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	if err := s.presets.SetSlot(presets.Preset{
		Slot: 6, Name: "Song", Type: "radio", StreamURL: libraryTrackURL,
		Source: "MinimServer", DurationSec: 5,
	}); err != nil {
		t.Fatal(err)
	}
	np.set("PLAY_STATE", 2*time.Second, 0) // the speaker reports no total
	start := time.Now()
	if !s.RecallSlot(context.Background(), 6) {
		t.Fatal("RecallSlot did not claim the library track key")
	}
	time.Sleep(5 * time.Second)
	np.set("PLAY_STATE", 5*time.Second, 0) // frozen at the end

	waitForTrackEnd(t, "the box to be stopped at the end of the key's song", func() bool { return rec.has("Stop") })
	if took := time.Since(start); took >= 5*time.Second+queueFrozenTimeout {
		t.Fatalf("the stop took %v, the frozen net's time: the stored length was not used", took)
	}
}
