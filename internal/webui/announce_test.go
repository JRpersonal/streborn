package webui

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/upnp"
)

// announceBox is a fake speaker for the announcement cycle (#1359). It records
// every command in the order the speaker would hear it, and plays along like the
// real firmware: a UPnP push turns the source to UPNP, the announcement clip
// finishes right after its Play, and the STOP key ends a native station.
type announceBox struct {
	mu      sync.Mutex
	events  []string
	np      nowPlayingSnapshot
	volume  int
	soapErr map[string]string // SOAP action -> fault text answered once
}

var currentURIRe = regexp.MustCompile(`<CurrentURI>([^<]*)</CurrentURI>`)

func (b *announceBox) record(ev string) {
	b.events = append(b.events, ev)
}

func (b *announceBox) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		action := r.Header.Get("SOAPACTION")
		if i := strings.LastIndex(action, "#"); i >= 0 {
			action = strings.Trim(action[i+1:], `"`)
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if fault, ok := b.soapErr[action]; ok {
			delete(b.soapErr, action)
			b.record("fault:" + action)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(w, `<s:Envelope><s:Body><s:Fault><detail>%s</detail></s:Fault></s:Body></s:Envelope>`, fault)
			return
		}
		switch action {
		case "SetAVTransportURI":
			uri := ""
			if m := currentURIRe.FindSubmatch(body); len(m) == 2 {
				uri = strings.ReplaceAll(string(m[1]), "&amp;", "&")
			}
			b.record("seturi:" + uri)
			b.np = nowPlayingSnapshot{Source: "UPNP", Location: uri, PlayStatus: "BUFFERING_STATE"}
		case "Play":
			b.record("play")
			if strings.Contains(b.np.Location, "/announce/audio") {
				// The clip is short: it has played to its end by the next read.
				b.np.PlayStatus = "STOP_STATE"
			} else {
				b.np.PlayStatus = "PLAY_STATE"
			}
		case "Stop":
			b.record("stop")
			if b.np.Source == "UPNP" {
				b.np.PlayStatus = "STOP_STATE"
			}
		default:
			b.record(strings.ToLower(action))
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope><s:Body/></s:Envelope>`))
	}
}

func (b *announceBox) now(context.Context) nowPlayingSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.np
}

func (b *announceBox) source() string { return b.now(context.Background()).Source }

func (b *announceBox) readVolume(context.Context) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.volume
}

func (b *announceBox) setVolume(_ context.Context, v int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record(fmt.Sprintf("setvolume:%d", v))
	b.volume = v
	return nil
}

func (b *announceBox) stopKey(context.Context) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record("stopkey")
	if b.np.Source == nativeRadioSource {
		b.np.PlayStatus = "STOP_STATE"
	}
	return true
}

func (b *announceBox) playKey(context.Context) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record("playkey")
	return true
}

var selectLocationRe = regexp.MustCompile(`location="([^"]*)"`)

func (b *announceBox) selectItem(_ context.Context, body string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	loc := ""
	if m := selectLocationRe.FindStringSubmatch(body); len(m) == 2 {
		loc = strings.ReplaceAll(m[1], "&amp;", "&")
	}
	b.record("select:" + loc)
	b.np = nowPlayingSnapshot{Source: nativeRadioSource, Location: loc, PlayStatus: "PLAY_STATE"}
	return nil
}

func (b *announceBox) seen() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.events, ",")
}

// fastAnnounce shrinks every announcement wait so a test runs in milliseconds.
func fastAnnounce(t *testing.T) {
	t.Helper()
	poll, polls, tail, unknown, wake := announcePoll, announceStartPolls, announceTail, announceUnknownWait, announceWakeTimeout
	announcePoll, announceStartPolls, announceTail, announceUnknownWait, announceWakeTimeout =
		time.Millisecond, 3, 5*time.Millisecond, 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() {
		announcePoll, announceStartPolls, announceTail, announceUnknownWait, announceWakeTimeout = poll, polls, tail, unknown, wake
	})
}

func newAnnounceServer(t *testing.T, b *announceBox) *Server {
	t.Helper()
	fastAnnounce(t)
	fastHandover(t)
	srv := httptest.NewServer(b.handler())
	t.Cleanup(srv.Close)
	return &Server{
		logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		queue:               newPlayQueue(),
		renderer:            &upnp.Renderer{ControlURL: srv.URL, Client: srv.Client()},
		boxSourceFn:         b.source,
		nativeStopFn:        b.stopKey,
		nativeResumeFn:      b.playKey,
		selectFn:            b.selectItem,
		announceNowFn:       b.now,
		announceVolumeFn:    b.readVolume,
		announceSetVolumeFn: b.setVolume,
	}
}

// nativeStationLocation is what now_playing reports for a station the speaker
// plays itself: a relative descriptor, not a URL anything can fetch.
func nativeStationLocation() string {
	payload := `{"streamUrl":"http://stream.example/swr3.mp3","name":"SWR3","imageUrl":""}`
	return "/station?data=" + base64.RawURLEncoding.EncodeToString([]byte(payload))
}

const clipEvents = "seturi:" + announceAudioURL + ",setvolume:20,play"

// #1359, part one: the native station must be stopped BEFORE the announcement
// volume is set, or the running radio blasts at that volume until the clip
// takes over.
func TestAnnounceStopsTheNativeStationBeforeRaisingTheVolume(t *testing.T) {
	loc := nativeStationLocation()
	b := &announceBox{volume: 35, np: nowPlayingSnapshot{Source: nativeRadioSource, Location: loc, ItemName: "SWR3", PlayStatus: "PLAY_STATE"}}
	s := newAnnounceServer(t, b)
	s.runAnnouncement(t.Context(), []byte("clip"), "audio/mpeg", "Hallo", 20)

	got := b.seen()
	if !strings.HasPrefix(got, "stopkey,"+clipEvents+",") {
		t.Fatalf("speaker saw %q, want it to start with stopkey,%s", got, clipEvents)
	}
}

// #1359, part two: the native station comes back through the speaker's own
// /select, with the descriptor it reported, never as a UPnP push of a relative
// location the speaker cannot fetch. The volume goes back first.
func TestAnnounceResumesTheNativeStationThroughSelect(t *testing.T) {
	loc := nativeStationLocation()
	b := &announceBox{volume: 35, np: nowPlayingSnapshot{Source: nativeRadioSource, Location: loc, ItemName: "SWR3", PlayStatus: "PLAY_STATE"}}
	s := newAnnounceServer(t, b)
	s.runAnnouncement(t.Context(), []byte("clip"), "audio/mpeg", "Hallo", 20)

	want := "stopkey," + clipEvents + ",stop,setvolume:35,select:" + loc
	if got := b.seen(); got != want {
		t.Errorf("speaker saw\n  %q\nwant\n  %q", got, want)
	}
	if strings.Contains(b.seen(), "seturi:/station") {
		t.Error("the relative station descriptor was pushed over UPnP")
	}
}

// A station the speaker refuses to /select falls back to the stream the
// descriptor names, pushed over UPnP.
func TestAnnounceFallsBackToTheDescribedStream(t *testing.T) {
	loc := nativeStationLocation()
	b := &announceBox{volume: 35, np: nowPlayingSnapshot{Source: nativeRadioSource, Location: loc, ItemName: "SWR3", PlayStatus: "PLAY_STATE"}}
	s := newAnnounceServer(t, b)
	s.selectFn = func(context.Context, string) error { return fmt.Errorf("select: box refused") }
	s.runAnnouncement(t.Context(), []byte("clip"), "audio/mpeg", "Hallo", 20)

	if got := b.seen(); !strings.HasSuffix(got, ",stop,setvolume:35,seturi:http://stream.example/swr3.mp3,play") {
		t.Errorf("speaker saw %q, want the described stream pushed after the volume restore", got)
	}
}

// A UPnP stream STR was playing is stopped before the volume changes, and comes
// back through the wrong-state repair: a first push the box refuses in the
// wrong transport state is cleared and pushed again.
func TestAnnounceResumesAUPnPStreamWithTheWrongStateRepair(t *testing.T) {
	radio := "http://stream.example/nova.mp3"
	b := &announceBox{volume: 35, np: nowPlayingSnapshot{Source: "UPNP", Location: radio, ItemName: "Nova", PlayStatus: "PLAY_STATE"}}
	s := newAnnounceServer(t, b)
	s.lastPlay = &lastPlayInfo{boxURL: radio, title: "Nova", art: "http://art.example/nova.png", mime: "audio/mpeg"}
	// Arm the fault only for the resume: the clip has played by then.
	s.announceSetVolumeFn = func(ctx context.Context, v int) error {
		if v == 35 {
			b.mu.Lock()
			b.soapErr = map[string]string{"Play": "Action request came in wrong state"}
			b.mu.Unlock()
		}
		return b.setVolume(ctx, v)
	}
	s.runAnnouncement(t.Context(), []byte("clip"), "audio/mpeg", "Hallo", 20)

	want := "stop," + clipEvents + ",stop,setvolume:35," +
		"seturi:" + radio + ",fault:Play,stop,seturi:,seturi:" + radio + ",play"
	if got := b.seen(); got != want {
		t.Errorf("speaker saw\n  %q\nwant\n  %q", got, want)
	}
}

// Only a source that was playing is resumed: a paused or stopped one stays as
// it was, and a source STR cannot bring back (Bluetooth, AUX) is left alone.
// The volume goes back in every case.
func TestAnnounceResumesOnlyWhatWasPlaying(t *testing.T) {
	cases := []nowPlayingSnapshot{
		{Source: "UPNP", Location: "http://stream.example/a.mp3", PlayStatus: "STOP_STATE"},
		{Source: "UPNP", Location: "http://stream.example/a.mp3", PlayStatus: "PAUSE_STATE"},
		{Source: nativeRadioSource, Location: nativeStationLocation(), PlayStatus: "STOP_STATE"},
		{Source: "BLUETOOTH", Location: "", PlayStatus: "PLAY_STATE"},
		{Source: "AUX", Location: "AUX", PlayStatus: "PLAY_STATE"},
	}
	for _, prev := range cases {
		b := &announceBox{volume: 35, np: prev}
		s := newAnnounceServer(t, b)
		s.runAnnouncement(t.Context(), []byte("clip"), "audio/mpeg", "Hallo", 20)
		got := b.seen()
		if !strings.HasSuffix(got, "play,stop,setvolume:35") {
			t.Errorf("%s/%s: speaker saw %q, want it to end with the clip, a stop and the volume restore", prev.Source, prev.PlayStatus, got)
		}
		if strings.Contains(got, "select:") || strings.Count(got, "seturi:") != 1 {
			t.Errorf("%s/%s: speaker saw %q, want no resume", prev.Source, prev.PlayStatus, got)
		}
	}
}

// Without a volume in the request nothing touches the volume at all.
func TestAnnounceWithoutVolumeLeavesTheVolumeAlone(t *testing.T) {
	radio := "http://stream.example/nova.mp3"
	b := &announceBox{volume: 35, np: nowPlayingSnapshot{Source: "UPNP", Location: radio, PlayStatus: "PLAY_STATE"}}
	s := newAnnounceServer(t, b)
	s.runAnnouncement(t.Context(), []byte("clip"), "audio/mpeg", "Hallo", 0)
	if got := b.seen(); strings.Contains(got, "setvolume") {
		t.Errorf("speaker saw %q, want no volume write", got)
	}
}

// A clip the speaker refuses puts the volume back and the previous stream on.
func TestAnnounceUndoesAClipThatDidNotStart(t *testing.T) {
	radio := "http://stream.example/nova.mp3"
	b := &announceBox{volume: 35, np: nowPlayingSnapshot{Source: "UPNP", Location: radio, PlayStatus: "PLAY_STATE"},
		soapErr: map[string]string{"SetAVTransportURI": "Transition not available"}}
	s := newAnnounceServer(t, b)
	s.runAnnouncement(t.Context(), []byte("clip"), "audio/mpeg", "Hallo", 20)
	got := b.seen()
	if strings.Contains(got, "setvolume:20") {
		t.Errorf("speaker saw %q, the announcement volume was set for a clip that never started", got)
	}
	if !strings.HasSuffix(got, "seturi:"+radio+",play") {
		t.Errorf("speaker saw %q, want the previous stream back", got)
	}
}

func TestChunkText(t *testing.T) {
	if got := chunkText("   ", 10); got != nil {
		t.Errorf("blank text = %q, want nil", got)
	}
	if got := chunkText(" kurz ", 10); len(got) != 1 || got[0] != "kurz" {
		t.Errorf("short text = %q, want [kurz]", got)
	}
	got := chunkText("eins zwei drei vier fuenf", 10)
	want := []string{"eins zwei", "drei vier", "fuenf"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("chunks = %q, want %q", got, want)
	}
	for _, c := range chunkText("ab "+strings.Repeat("x", 25)+" cd", 10) {
		if len(c) > 10 {
			t.Errorf("chunk %q is longer than the limit", c)
		}
	}
	if got := chunkText("ab "+strings.Repeat("x", 25)+" cd", 10); strings.ReplaceAll(strings.Join(got, ""), " ", "") != "ab"+strings.Repeat("x", 25)+"cd" {
		t.Errorf("hard-cut chunks %q lost text", got)
	}
}

func TestMP3DurationSec(t *testing.T) {
	// MPEG-1 Layer III, 128 kbit/s: 16000 bytes per second.
	clip := make([]byte, 32000)
	copy(clip[10:], []byte{0xFF, 0xFB, 0x90, 0x64})
	if got := mp3DurationSec(clip); got < 1.99 || got > 2.01 {
		t.Errorf("MPEG-1 128k: %.3f s, want 2", got)
	}
	// MPEG-2 Layer III, 32 kbit/s (Google TTS): 4000 bytes per second.
	clip = make([]byte, 12000)
	copy(clip, []byte{0xFF, 0xF3, 0x40, 0xC4})
	if got := mp3DurationSec(clip); got < 2.99 || got > 3.01 {
		t.Errorf("MPEG-2 32k: %.3f s, want 3", got)
	}
	if got := mp3DurationSec([]byte("not audio at all")); got != 0 {
		t.Errorf("garbage: %.3f s, want 0", got)
	}
	if got := mp3DurationSec(nil); got != 0 {
		t.Errorf("empty: %.3f s, want 0", got)
	}
}
