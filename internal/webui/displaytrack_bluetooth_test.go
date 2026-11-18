package webui

import (
	"fmt"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/oled"
)

// btNowPlaying is a <nowPlaying> document as the speaker's bus carries it on
// the Bluetooth input (shape of a SoundTouch 20, 27.0.6).
func btNowPlaying(artist, track string) []byte {
	return []byte(fmt.Sprintf(`<nowPlaying deviceID="device-id-here" source="BLUETOOTH" sourceAccount="">`+
		`<ContentItem source="BLUETOOTH" location="" sourceAccount="" isPresetable="false"><itemName>Phone</itemName></ContentItem>`+
		`<track>%s</track><artist>%s</artist><album>Hot Space</album><stationName>Phone</stationName>`+
		`<art artImageStatus="SHOW_DEFAULT_IMAGE" /><playStatus>PLAY_STATE</playStatus>`+
		`<skipEnabled /><skipPreviousEnabled /><connectionStatusInfo status="CONNECTED" deviceName="Phone" /></nowPlaying>`, track, artist))
}

// The decision on the speaker's inputs: which screen for which source, the
// two settings and whether the input reports a song.
func TestInputScreenFor(t *testing.T) {
	cases := []struct {
		src                string
		clock, track, song bool
		want               inputScreen
		wantName           string
	}{
		// Bluetooth with a song from the phone
		{src: "BLUETOOTH", clock: true, track: true, song: true, want: onInputTrack, wantName: "song with the clock in the corner"},
		{src: "BLUETOOTH", clock: false, track: true, song: true, want: onInputTrack, wantName: "song without the clock"},
		{src: "BLUETOOTH", clock: true, track: false, song: true, want: onInputClock, wantName: "track off: the clock alone"},
		{src: "BLUETOOTH", clock: false, track: false, song: true, want: onInputFirmware, wantName: "both off: the firmware"},
		// Bluetooth without one (nothing connected, an app without titles)
		{src: "BLUETOOTH", clock: true, track: true, song: false, want: onInputClock, wantName: "no song: the clock alone"},
		{src: "BLUETOOTH", clock: false, track: true, song: false, want: onInputFirmware, wantName: "no song, clock off: the firmware"},
		{src: "BLUETOOTH", clock: true, track: false, song: false, want: onInputClock, wantName: "track off, no song: the clock alone"},
		{src: "BLUETOOTH", clock: false, track: false, song: false, want: onInputFirmware, wantName: "nothing: the firmware"},
		// AUX reports no song; one left in the model must not reach it
		{src: "AUX", clock: true, track: true, song: true, want: onInputClock, wantName: "AUX never shows a song"},
		{src: "AUX", clock: false, track: true, song: true, want: onInputFirmware, wantName: "AUX, clock off: the firmware"},
		{src: "AUX", clock: true, track: true, song: false, want: onInputClock, wantName: "AUX: the clock"},
		{src: "AUX", clock: true, track: false, song: false, want: onInputClock, wantName: "AUX, track off: the clock"},
		{src: "AUX", clock: false, track: true, song: false, want: onInputFirmware, wantName: "AUX, clock off, no song: the firmware"},
		{src: "AUX", clock: false, track: false, song: false, want: onInputFirmware, wantName: "AUX, nothing: the firmware"},
	}
	for _, c := range cases {
		if got := inputScreenFor(c.src, c.clock, c.track, c.song); got != c.want {
			t.Errorf("%s clock=%v track=%v song=%v (%s): got %d, want %d", c.src, c.clock, c.track, c.song, c.wantName, got, c.want)
		}
	}
}

// The same decision through the server: the settings from their flag files,
// the song from the speaker's now-playing document, the screen on the panel.
func TestPanelBluetoothScreens(t *testing.T) {
	type call struct {
		kind string // "track", "clock" or "" for no screen of STR's
		t    oled.TrackText
	}
	cases := []struct {
		name          string
		src           string
		clock, track  bool
		artist, title string
		want          call
	}{
		{"song and clock", "BLUETOOTH", true, true, "Queen", "Under Pressure",
			call{"track", oled.TrackText{Station: "Bluetooth", Artist: "Queen", Title: "Under Pressure", Clock: true, Hour12: true}}},
		{"song, clock off", "BLUETOOTH", false, true, "Queen", "Under Pressure",
			call{"track", oled.TrackText{Station: "Bluetooth", Artist: "Queen", Title: "Under Pressure"}}},
		{"title only", "BLUETOOTH", true, true, "", "Podcast 12",
			call{"track", oled.TrackText{Station: "Bluetooth", Title: "Podcast 12", Clock: true, Hour12: true}}},
		{"artist only", "BLUETOOTH", true, true, "Queen", "",
			call{"track", oled.TrackText{Station: "Bluetooth", Title: "Queen", Clock: true, Hour12: true}}},
		{"song, track off", "BLUETOOTH", true, false, "Queen", "Under Pressure",
			call{"clock", oled.TrackText{Station: "Bluetooth", Clock: true, Hour12: true}}},
		{"no song", "BLUETOOTH", true, true, "", "",
			call{"clock", oled.TrackText{Station: "Bluetooth", Clock: true, Hour12: true}}},
		{"no song, clock off", "BLUETOOTH", false, true, "", "", call{}},
		{"song, both off", "BLUETOOTH", false, false, "Queen", "Under Pressure", call{}},
		{"AUX", "AUX", true, true, "", "",
			call{"clock", oled.TrackText{Station: "AUX", Clock: true, Hour12: true}}},
		{"AUX, clock off", "AUX", false, true, "", "", call{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, fp, _ := newTrackServer(t, true)
			s.hour12Read = func() (bool, bool) { return true, true }
			setFlag(t, s.displayTrackPath, c.track)
			setFlag(t, clockPathFor(s.displayTrackPath), c.clock)
			// the bus carries the document before the source change (boxws)
			if c.src == "BLUETOOTH" {
				s.NoteBoxNowPlaying(btNowPlaying(c.artist, c.title))
			}
			s.PanelSourceChanged(c.src)
			if c.want.kind == "" {
				time.Sleep(50 * time.Millisecond)
			} else {
				waitUntil(t, func() bool { return len(fp.inputSnapshot())+len(fp.inputTrackSnapshot()) == 1 })
			}
			var got call
			if in := fp.inputTrackSnapshot(); len(in) == 1 {
				got = call{"track", in[0].t}
			} else if in := fp.inputSnapshot(); len(in) == 1 {
				got = call{"clock", in[0].t}
			}
			if got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			if shown, cleared := fp.snapshot(); len(shown)+len(cleared)+len(fp.inputClearSnapshot()) != 0 {
				t.Fatalf("the switch touched the panel beyond its screen: shown %+v cleared %v input clears %v",
					shown, cleared, fp.inputClearSnapshot())
			}
		})
	}
}

// The track mode picks the parts on Bluetooth as it does on STR's sources.
func TestPanelBluetoothTrackModes(t *testing.T) {
	for mode, want := range map[string]oled.TrackText{
		"both":   {Station: "Bluetooth", Artist: "Queen", Title: "Under Pressure"},
		"title":  {Station: "Bluetooth", Title: "Under Pressure"},
		"artist": {Station: "Bluetooth", Title: "Queen"},
	} {
		s, _, _ := newTrackServer(t, true)
		if err := writeFlagFile(modePathFor(s.displayTrackPath), mode); err != nil {
			t.Fatal(err)
		}
		s.NoteBoxNowPlaying(btNowPlaying("Queen", "Under Pressure"))
		if kind, got := s.inputPanelText("BLUETOOTH", "Bluetooth"); kind != onInputTrack || got != want {
			t.Errorf("mode %s: %d %+v, want %+v", mode, kind, got, want)
		}
	}
}

// A song change on Bluetooth redraws from the speaker's own bus frame, the
// same document again does nothing, and the song going away (the phone
// disconnected) falls back to the clock, or to the firmware with the clock
// off. Leaving Bluetooth forgets the song.
func TestPanelBluetoothFollowsTheSong(t *testing.T) {
	s, fp, _ := newTrackServer(t, true)
	setFlag(t, clockPathFor(s.displayTrackPath), true)
	s.PanelSourceChanged("BLUETOOTH")
	waitUntil(t, func() bool { return len(fp.inputSnapshot()) == 1 }) // nothing known yet: the clock

	s.NoteBoxNowPlaying(btNowPlaying("Queen", "Under Pressure"))
	waitUntil(t, func() bool { return len(fp.inputTrackSnapshot()) == 1 })
	s.NoteBoxNowPlaying(btNowPlaying("Queen", "Under Pressure")) // a pause, a position
	s.NoteBoxNowPlaying(btNowPlaying("David Bowie", "Heroes"))
	waitUntil(t, func() bool { return len(fp.inputTrackSnapshot()) == 2 })
	time.Sleep(30 * time.Millisecond)
	in := fp.inputTrackSnapshot()
	if len(in) != 2 || in[1].source != "BLUETOOTH" || in[1].t.Artist != "David Bowie" || in[1].t.Title != "Heroes" || !in[1].t.Clock {
		t.Fatalf("song changes on the panel: %+v", in)
	}

	// a document of another source says nothing about Bluetooth
	s.NoteBoxNowPlaying([]byte(`<nowPlaying source="UPNP"><track>BLUETOOTH</track><artist>"BLUETOOTH"</artist></nowPlaying>`))
	time.Sleep(30 * time.Millisecond)
	if got := fp.inputTrackSnapshot(); len(got) != 2 {
		t.Fatalf("a document of another source redrew the Bluetooth screen: %+v", got)
	}

	// the song goes away: the clock
	s.NoteBoxNowPlaying(btNowPlaying("", ""))
	waitUntil(t, func() bool { return len(fp.inputSnapshot()) == 2 })
	// ... and with the clock off the panel goes back to the firmware
	s.NoteBoxNowPlaying(btNowPlaying("Queen", "Under Pressure"))
	waitUntil(t, func() bool { return len(fp.inputTrackSnapshot()) == 3 })
	setFlag(t, clockPathFor(s.displayTrackPath), false)
	s.NoteBoxNowPlaying(btNowPlaying("", ""))
	waitUntil(t, func() bool { return len(fp.inputClearSnapshot()) == 1 })

	// leaving Bluetooth forgets the song: the next session starts without one
	s.NoteBoxNowPlaying(btNowPlaying("Queen", "Under Pressure"))
	waitUntil(t, func() bool { return len(fp.inputTrackSnapshot()) == 4 })
	s.PanelSourceChanged("UPNP")
	setFlag(t, clockPathFor(s.displayTrackPath), true)
	s.PanelSourceChanged("BLUETOOTH")
	waitUntil(t, func() bool { return len(fp.inputSnapshot()) == 3 })
	time.Sleep(30 * time.Millisecond)
	if got := fp.inputTrackSnapshot(); len(got) != 4 {
		t.Fatalf("the song of the last Bluetooth session came back: %+v", got[4:])
	}
}

// Off Bluetooth, and on a speaker without a panel, a Bluetooth document
// draws nothing.
func TestPanelBluetoothSongOffTheInput(t *testing.T) {
	s, fp, _ := newTrackServer(t, true)
	setFlag(t, clockPathFor(s.displayTrackPath), true)
	s.PanelSourceChanged("UPNP")
	s.NoteBoxNowPlaying(btNowPlaying("Queen", "Under Pressure"))
	time.Sleep(30 * time.Millisecond)
	if n := len(fp.inputTrackSnapshot()) + len(fp.inputSnapshot()); n != 0 {
		t.Fatalf("a Bluetooth document drew %d screens while the speaker is on UPNP", n)
	}

	s, fp, _ = newTrackServer(t, false)
	s.PanelSourceChanged("BLUETOOTH")
	s.NoteBoxNowPlaying(btNowPlaying("Queen", "Under Pressure"))
	time.Sleep(30 * time.Millisecond)
	if shown, cleared := fp.snapshot(); len(shown)+len(cleared)+len(fp.inputTrackSnapshot())+len(fp.inputSnapshot()) != 0 {
		t.Fatal("panel touched without a panel")
	}
}
