package main

// The first press after a rest, and why it used to produce silence.
//
// Measured on an ST30 on 2026-09-27, from the owner's own speaker. The box was
// in standby. One press of key 2 arrived, and within a second and a half the
// firmware did all of this:
//
//	09:02:05.011  the box announced its PREVIOUS content item, a Spotify key
//	09:02:05.639  1036 UNABLE_TO_PROCESS_NOT_LOGGED_IN, UpnpRcvdContentItemInWrongState
//	09:02:06.226  the speaker left standby
//	09:02:06.590  it opened the stream and dropped it 2 ms later
//	09:02:07.298  Decoder: "Could not obtain first-frame 1"
//	09:02:07.314  APAudioControl: PlaybackFailure ERROR_NO_DECODED_DATA
//	09:02:07.355  the owner pressed the key a second time
//
// Nothing here is STR's doing: the station resolved, the proxy served, the key
// was registered. The box simply woke into the wrong state, asked for the
// stream before its audio path was ready, and threw the first fetch away. What
// IS STR's doing is that it watched this happen and did nothing, because the
// firmware's own failure lines were classified for the diagnostic bundle and
// delivered to no one.
//
// So the second press becomes the agent's job. One retry, only just after a
// native preset activation, only once per activation, and only when the
// speaker is not already playing something by the time the retry would fire.
//
// The press that never started at all.
//
// Reported on an ST10 (#1170, v1.0.7). The speaker was playing Bluetooth and
// the owner pressed key 5:
//
//	09:09:53.698  box key PRESET_5
//	09:09:53.724  source changed BLUETOOTH -> LOCAL_INTERNET_RADIO
//	09:09:58.838  source changed LOCAL_INTERNET_RADIO -> INVALID_SOURCE
//	09:09:59.061  source changed INVALID_SOURCE -> BLUETOOTH
//
// The firmware switched to the radio source and then never asked for the
// station: no descriptor resolve, no stream proxy fetch, and therefore no
// PlaybackFailure either, because nothing was ever played that could fail.
// Five seconds later it gave up and went back to Bluetooth. The owner's second
// press, a minute later, played within 0.3 s. The trigger above never saw
// this one. So a second trigger: a press that LEFT Bluetooth or AUX, whose
// source then falls to INVALID_SOURCE or back to an input inside the same
// window, without the station ever being resolved or fetched, gets the same
// single retry. Only from Bluetooth or AUX: a press from standby or from
// another radio station that ends on INVALID_SOURCE is a different failure,
// and the native-drop watchdog in internal/boxws already owns it.

import (
	"context"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/webui"
)

const (
	// firstPressWindow is how long after a native preset activation a playback
	// failure still counts as that press failing. The measured gap was 2.1 s
	// from activation to ERROR_NO_DECODED_DATA; a cold ST30 coming out of
	// standby is the slowest case in the fleet, so the window is generous
	// enough for it and short enough that a failure minutes later, which is a
	// different story, is not blamed on the press.
	firstPressWindow = 8 * time.Second
	// firstPressSettle is the pause before the retry. The firmware has just
	// told itself the stream failed; pressing again inside that moment is how
	// a user gets two dead presses instead of one.
	firstPressSettle = 1500 * time.Millisecond
	// pressSourceLink is how far apart a press and the source switch it caused
	// may lie and still be read as one event. Measured: the switch to the
	// radio source came 45 ms BEFORE the agent's own activation note (the box
	// switches first, then announces the preset), so the link works in both
	// directions.
	pressSourceLink = 3 * time.Second
)

// Box source names the press-went-nowhere trigger cares about.
const (
	srcRadio     = "LOCAL_INTERNET_RADIO"
	srcInvalid   = "INVALID_SOURCE"
	srcBluetooth = "BLUETOOTH"
	srcAux       = "AUX"
)

// nativeActivation is the last native preset the box activated by itself.
type nativeActivation struct {
	slot      int
	at        time.Time
	rescued   bool
	rescuedAt time.Time
	reported  bool
	// origin is the source the box left for the radio on this press
	// (BLUETOOTH, STANDBY, ...), empty when the box was on the radio already
	// or no switch was seen.
	origin string
}

type firstPressRescue struct {
	mu   sync.Mutex
	last nativeActivation
	// radioFrom/radioFromAt are the last switch INTO the radio source, kept
	// because that switch can arrive just before the activation it belongs to.
	radioFrom   string
	radioFromAt time.Time
}

// noteNativeActivation records a native preset activation as a candidate for
// one rescue.
func (f *firstPressRescue) noteNativeActivation(slot int, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// The rescue's own second press activates the same slot again. That is
	// still the one press the user made, not a new one: keep its rescue spent
	// (so a second failure cannot buy a third press) and keep the window
	// anchored on the rescue, which is what failedAfterRescue measures from.
	if f.last.rescued && f.last.slot == slot && at.Sub(f.last.rescuedAt) < firstPressWindow {
		return
	}
	f.last = nativeActivation{slot: slot, at: at}
	if f.radioFrom != "" && absDuration(at.Sub(f.radioFromAt)) <= pressSourceLink {
		f.last.origin = f.radioFrom
	}
}

// fallBack follows a source transition at t and decides whether it is the
// last activation's press going nowhere (see the #1170 account at the top).
// stationSeen is the latest moment the station was resolved or its stream
// fetched. It takes the activation's single rescue and returns the slot to
// press, or 0.
func (f *firstPressRescue) fallBack(from, to string, t, stationSeen time.Time) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if to == srcRadio {
		f.radioFrom, f.radioFromAt = from, t
		// The switch arrived after the activation it belongs to.
		if f.last.slot != 0 && f.last.origin == "" && !f.last.rescued &&
			absDuration(t.Sub(f.last.at)) <= pressSourceLink {
			f.last.origin = from
		}
		return 0
	}
	if f.last.slot == 0 || f.last.rescued {
		return 0
	}
	if f.last.origin != srcBluetooth && f.last.origin != srcAux {
		return 0
	}
	if to != srcInvalid && to != srcBluetooth && to != srcAux {
		return 0
	}
	if t.Before(f.last.at) || t.Sub(f.last.at) > firstPressWindow {
		return 0
	}
	// The station was resolved or fetched after the press: the firmware did
	// act on it, and whatever ended it is not a press that went nowhere.
	if !stationSeen.IsZero() && !stationSeen.Before(f.last.at) {
		return 0
	}
	f.last.rescued = true
	f.last.rescuedAt = t
	return f.last.slot
}

// originOf returns the source the last activation left, if it is slot's.
func (f *firstPressRescue) originOf(slot int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.last.slot != slot {
		return ""
	}
	return f.last.origin
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// failedAfterRescue reports whether a failure at t means the rescued press
// failed a second time, which is the point where the station is taken to be
// down. It answers true once per activation, and returns the slot.
func (f *firstPressRescue) failedAfterRescue(t time.Time) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.last.slot == 0 || !f.last.rescued || f.last.reported {
		return 0, false
	}
	// The firmware logs two lines for one silence (no first frame, then
	// PlaybackFailure). Only a failure after the rescue's own press, which
	// fires firstPressSettle after the claim, is the second failure.
	pressedAgain := f.last.rescuedAt.Add(firstPressSettle)
	if t.Before(pressedAgain) || t.Sub(pressedAgain) > firstPressWindow {
		return 0, false
	}
	f.last.reported = true
	return f.last.slot, true
}

// claim decides whether a failure at t belongs to the last activation and
// takes the single rescue that activation is allowed. It returns the slot to
// press, or 0.
func (f *firstPressRescue) claim(t time.Time) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.last.slot == 0 || f.last.rescued {
		return 0
	}
	if t.Before(f.last.at) || t.Sub(f.last.at) > firstPressWindow {
		return 0
	}
	f.last.rescued = true
	f.last.rescuedAt = t
	return f.last.slot
}

// OnPlayFailure is the boxlog hook. It runs on the reader's goroutine, so the
// work happens in its own.
func (h *presetWsHandler) OnPlayFailure(ev boxlog.PlayFailure) {
	slot := h.firstPress.claim(ev.At)
	if slot == 0 {
		// The rescue already pressed again and the box failed a second time:
		// the station is not answering (or the speaker is offline). Say so on
		// the display instead of leaving the user in front of silence.
		if again, ok := h.firstPress.failedAfterRescue(ev.At); ok {
			h.logger.Info("native preset failed again after the rescue, telling the user", "slot", again,
				"reason", string(ev.Class))
			h.displayMessage(webui.DisplayMsgStationDown)
		}
		return
	}
	h.logger.Info("first press after a rest produced no audio, pressing the key again",
		"slot", slot, "reason", string(ev.Class), "boxSaid", ev.Message)
	go h.rescueFirstPress(slot, "")
}

// OnSourceChanged is the boxws hook for every source transition. It runs on
// the gabbo read loop, so the press happens in its own goroutine.
func (h *presetWsHandler) OnSourceChanged(_ context.Context, from, to string) {
	var seen time.Time
	if h.stationActivity != nil {
		seen = h.stationActivity()
	}
	slot := h.firstPress.fallBack(from, to, time.Now(), seen)
	if slot == 0 {
		return
	}
	origin := h.firstPress.originOf(slot)
	h.logger.Info("first press went nowhere: the speaker fell back without fetching the station, pressing the key again",
		"slot", slot, "leftSource", origin, "from", from, "to", to)
	go h.rescueFirstPress(slot, origin)
}

// rescueFirstPress presses slot once more after firstPressSettle. origin is
// the input the failed press left (BLUETOOTH, AUX), or empty: audio on that
// input is the very thing the user pressed the key to leave, so it does not
// count as the speaker having found its feet.
func (h *presetWsHandler) rescueFirstPress(slot int, origin string) {
	time.Sleep(firstPressSettle)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// If the speaker found its feet in the meantime, leave it alone. A retry
	// on a speaker that is already playing would cut the music off and start
	// it again, which is a worse bug than the one being fixed.
	if origin != "" && h.sourceAndPlaying != nil {
		if src, playing, ok := h.sourceAndPlaying(ctx); ok && playing && src != origin {
			h.logger.Info("first press rescue: the speaker is playing after all, standing down", "slot", slot, "source", src)
			return
		}
	} else if h.playingNow != nil && h.playingNow(ctx) {
		h.logger.Info("first press rescue: the speaker is playing after all, standing down", "slot", slot)
		return
	}
	if err := boxcli.PresetKey(ctx, h.boxHost, slot, "p"); err != nil {
		h.logger.Warn("first press rescue: the speaker refused the second press", "slot", slot, "err", err)
		return
	}
	h.logger.Info("first press rescue: pressed again", "slot", slot)
}
