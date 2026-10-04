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
)

// nativeActivation is the last native preset the box activated by itself.
type nativeActivation struct {
	slot      int
	at        time.Time
	rescued   bool
	rescuedAt time.Time
	reported  bool
}

type firstPressRescue struct {
	mu   sync.Mutex
	last nativeActivation
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
	go h.rescueFirstPress(slot)
}

func (h *presetWsHandler) rescueFirstPress(slot int) {
	time.Sleep(firstPressSettle)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// If the speaker found its feet in the meantime, leave it alone. A retry
	// on a speaker that is already playing would cut the music off and start
	// it again, which is a worse bug than the one being fixed.
	if h.playingNow != nil && h.playingNow(ctx) {
		h.logger.Info("first press rescue: the speaker is playing after all, standing down", "slot", slot)
		return
	}
	if err := boxcli.PresetKey(ctx, h.boxHost, slot, "p"); err != nil {
		h.logger.Warn("first press rescue: the speaker refused the second press", "slot", slot, "err", err)
		return
	}
	h.logger.Info("first press rescue: pressed again", "slot", slot)
}
