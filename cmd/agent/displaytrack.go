package main

// The gabbo side of the track screen (internal/oled/track.go): the speaker
// tells STR when the track has to leave the panel or step aside for a moment.

import (
	"context"
	"sync"

	"github.com/JRpersonal/streborn/internal/oled"
)

// Seams for the tests.
var (
	setTrackSource  = oled.SetTrackSource
	setTrackPlaying = oled.SetTrackPlaying
	pauseTrack      = oled.PauseTrack
	// refreshTrackPanel redraws the playback screen from what STR knows plays
	// (webui.RefreshTrackPanel); wired in main, nil until then.
	refreshTrackPanel func()
	// notePanelSource tells the panel model which source the speaker is on,
	// so the clock can stand in for the firmware's picture on Bluetooth and
	// AUX (webui.PanelSourceChanged); wired in main, nil until then.
	notePanelSource func(source string)
)

// trackSourceKeeps reports whether a source switch to to can still be STR's
// radio: its own UPnP push, or a native radio preset streamed through STR.
// Anything else (standby, AUX, Bluetooth, a firmware service such as TuneIn
// that shows its own titles) ends the track.
func trackSourceKeeps(to string) bool {
	return to == "UPNP" || to == "LOCAL_INTERNET_RADIO"
}

// The order of the track screen's source changes. Every change takes the
// next generation; a wake check that finishes after a newer change has
// nothing more to say. trackMu also keeps a check's late verdict from
// overtaking a frame that arrived in the same moment.
var (
	trackMu  sync.Mutex
	trackGen uint64
	// trackDownByBox is set when the speaker's own word (a wake it denied,
	// its syslog standby) took STR's source off the screen, independently
	// of the gabbo stream. Cleared by the next source frame.
	trackDownByBox bool
)

// trackSettle runs f when no source change came since gen. Reports whether
// it ran.
func trackSettle(gen uint64, f func()) bool {
	trackMu.Lock()
	defer trackMu.Unlock()
	if gen != trackGen {
		return false
	}
	f()
	return true
}

// trackHooks are the seams one decision works with, read once when it
// starts so a goroutine that finishes later uses the same ones.
type trackHooks struct {
	set     func(name string, ours bool)
	refresh func()
	note    func(source string)
}

func currentTrackHooks() trackHooks {
	return trackHooks{set: setTrackSource, refresh: refreshTrackPanel, note: notePanelSource}
}

// source tells the track screen and the panel model about src.
func (k trackHooks) source(src string, ours bool) {
	k.set(src, ours)
	if k.note != nil {
		k.note(src)
	}
}

func (k trackHooks) redraw() {
	if k.refresh != nil {
		k.refresh()
	}
}

// trackOnSourceChanged tells the track screen about every source switch:
// leaving STR's sources takes the track off the panel and keeps titles off
// it until STR's source is back. Coming back (out of standby, from AUX or
// Bluetooth) brings the screen back at once rather than at the next song,
// which for a long Spotify track or a station without titles would never
// come. Switching to Bluetooth or AUX puts the clock there when the display
// setting asks for it (notePanelSource). Runs on the gabbo read loop; the
// call does not block and keeps the order of the switches.
//
// wake is the check of a switch out of standby onto STR's source
// (phantomwake.go), nil when there is none. With one, titles stay off until
// the speaker itself says it is on STR's source: a speaker in standby
// answers STR's own stop and clear with exactly such a frame without ever
// waking, and the screen then stayed on the panel of a speaker in standby.
// A real wake answers at once, so its screen still comes back at once.
func trackOnSourceChanged(from, to string, wake *wakeCheck) {
	k := currentTrackHooks()
	ours := trackSourceKeeps(to)
	verify := ours && wake != nil
	trackMu.Lock()
	trackGen++
	gen := trackGen
	trackDownByBox = false
	k.source(to, ours && !verify)
	trackMu.Unlock()
	if verify {
		go trackAfterWakeCheck(k, gen, to, wake)
		return
	}
	if ours && !trackSourceKeeps(from) && k.refresh != nil {
		go k.refresh()
	}
}

// trackAfterWakeCheck puts STR's screen up as soon as the speaker says it is
// on STR's source, and takes it off for good when the confirming read finds
// the speaker still in standby. A speaker that does not answer keeps the
// panel handed back.
func trackAfterWakeCheck(k trackHooks, gen uint64, to string, w *wakeCheck) {
	ourSource := func() { k.set(to, true) }
	<-w.firstDone
	shown := false
	if w.first == wakeAwake && trackSourceKeeps(w.firstSrc) {
		if shown = trackSettle(gen, ourSource); shown {
			k.redraw()
		}
	}
	<-w.finalDone
	switch {
	case w.final == wakeStandby:
		trackSettle(gen, func() {
			trackDownByBox = true
			k.source("STANDBY", false)
		})
	case !shown && w.final == wakeAwake && trackSourceKeeps(w.finalSrc):
		if trackSettle(gen, ourSource) {
			k.redraw()
		}
	}
}

// trackOnBoxStandby is the speaker's syslog saying it went to standby: the
// track leaves the panel whatever the gabbo stream last said. Non-blocking.
func trackOnBoxStandby() {
	k := currentTrackHooks()
	trackMu.Lock()
	defer trackMu.Unlock()
	trackGen++
	trackDownByBox = true
	k.source("STANDBY", false)
}

// trackOnBoxWake is the speaker's syslog saying it left standby. When the
// speaker's own word took the track off before, the gabbo stream may not
// report this wake as a source change, so the speaker is asked which source
// it woke to and STR's screen comes back if it is STR's. newCheck starts
// that read; it runs only when needed. Non-blocking.
func trackOnBoxWake(newCheck func() *wakeCheck) {
	k := currentTrackHooks()
	trackMu.Lock()
	down, gen := trackDownByBox, trackGen
	trackMu.Unlock()
	if !down || newCheck == nil {
		return
	}
	w := newCheck()
	if w == nil {
		return
	}
	go func() {
		<-w.firstDone
		src := w.firstSrc
		if w.first != wakeAwake {
			<-w.finalDone
			if w.final != wakeAwake {
				return
			}
			src = w.finalSrc
		}
		if !trackSourceKeeps(src) {
			return
		}
		if trackSettle(gen, func() {
			trackDownByBox = false
			k.source(src, true)
		}) {
			k.redraw()
		}
	}()
}

// OnVolumeChanged is the boxws hook for a volumeUpdated frame: the track
// screen hands the panel back for a few seconds so BoseApp's own volume bar
// shows, then returns by itself.
func (h *presetWsHandler) OnVolumeChanged(context.Context) {
	go pauseTrack(oled.TrackVolumePause, "volume")
}

// OnVolumeLevel is the boxws hook carrying the speaker's new volume (-1 when
// the frame had none): it is passed to the Spotify engine so the Spotify
// app's volume slider follows hardware keys, the remote and every other
// volume source. Non-blocking (NoteBoxVolume hands it to a worker).
func (h *presetWsHandler) OnVolumeLevel(_ context.Context, level int) {
	if h.spotify != nil {
		h.spotify.NoteBoxVolume(level)
	}
	if h.onBoxVolume != nil {
		h.onBoxVolume(level)
	}
}

// OnPlayingChanged is the boxws hook for the speaker's play status: the
// playing symbol on the track screen moves only while music runs.
func (h *presetWsHandler) OnPlayingChanged(_ context.Context, playing bool) {
	setTrackPlaying(playing)
}
