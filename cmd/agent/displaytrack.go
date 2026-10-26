package main

// The gabbo side of the track screen (internal/oled/track.go): the speaker
// tells STR when the track has to leave the panel or step aside for a moment.

import (
	"context"

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

// trackOnSourceChanged tells the track screen about every source switch:
// leaving STR's sources takes the track off the panel and keeps titles off
// it until STR's source is back. Coming back (out of standby, from AUX or
// Bluetooth) brings the screen back at once rather than at the next song,
// which for a long Spotify track or a station without titles would never
// come. Switching to Bluetooth or AUX puts the clock there when the display
// setting asks for it (notePanelSource). Runs on the gabbo read loop; the
// call does not block and keeps the order of the switches.
func trackOnSourceChanged(from, to string) {
	ours := trackSourceKeeps(to)
	setTrackSource(to, ours)
	if notePanelSource != nil {
		notePanelSource(to)
	}
	if ours && !trackSourceKeeps(from) && refreshTrackPanel != nil {
		go refreshTrackPanel()
	}
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
}

// OnPlayingChanged is the boxws hook for the speaker's play status: the
// playing symbol on the track screen moves only while music runs.
func (h *presetWsHandler) OnPlayingChanged(_ context.Context, playing bool) {
	setTrackPlaying(playing)
}
