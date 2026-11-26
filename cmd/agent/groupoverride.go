// A member of a permanent group that caught the main speaker's remote press.
//
// The SoundTouch remote is infrared, and the speakers of a permanent group
// often share a room, so one press can reach the main speaker and a member at
// once. Field case 2026-11-26: PRESET_2 aimed at the Wohnzimmer ST30 also
// reached the Küche ST10, whose hold-to-save read its half of the press as a
// hold (no tap verdict, no activation: the key was empty and the speaker was
// on Bluetooth). Only that source kept it from saving whatever STR played
// over key 2.
//
// The rule (Jens, 2026-11-26): a member press within GroupPressWindow of the
// main speaker's own press is the same press of the same remote, and the main
// speaker wins. It re-forms the group on its start and takes the member along
// (internal/webui zones_default_group.go). The member does nothing of its own
// with the press: no hold-to-save, no store of the firmware's own hold kept,
// no dead-key play, no STR push for a preset key, no radio-key step. What the
// firmware does by itself (a native station on the key activating) cannot be
// stopped; the main speaker's re-form takes the member over from it.
//
// The main speaker tells its members of each remote press (webui
// NotifyMembersOfRemoteKey); the member only compares (MasterRemoteKeyNear).
// A member press with no such notice near it is the member's own and behaves
// exactly as before.

package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/webui"
)

// masterKeyNearFunc is webui.Server.MasterRemoteKeyNear.
type masterKeyNearFunc func(at time.Time, window time.Duration) (delta time.Duration, key, from string, ok bool)

// errMainSpeakerOverrides refuses the firmware's own store of a key whose hold
// was the main speaker's remote press. It wraps marge.ErrHeldStoreHandled: the
// firmware keeps the key as it was, and it is no refused long press of the
// owner's (no "could not be kept" banner).
var errMainSpeakerOverrides = fmt.Errorf("the press was the group's main speaker's remote press: %w", marge.ErrHeldStoreHandled)

// mainSpeakerOverrides reports whether a remote press at `at` on this speaker
// was the same press as one its permanent group's main speaker reported, and
// logs the deciding signal when it was. what names the action left out.
func mainSpeakerOverrides(near masterKeyNearFunc, logger interface {
	Info(msg string, args ...any)
}, producer boxlog.Producer, key string, at time.Time, what string) bool {
	if near == nil || producer != boxlog.ProducerIRRemote || at.IsZero() {
		return false
	}
	delta, masterKey, from, ok := near(at, webui.GroupPressWindow)
	if !ok {
		return false
	}
	ms := delta.Milliseconds()
	if ms < 0 {
		ms = -ms
	}
	logger.Info("group: member press within "+strconv.FormatInt(ms, 10)+" ms of the main speaker's press, the main speaker overrides it",
		"left", what, "key", key, "masterKey", masterKey, "master", from,
		"noticeAfterPressMs", delta.Milliseconds(), "windowMs", webui.GroupPressWindow.Milliseconds())
	return true
}

// masterOverrides is mainSpeakerOverrides for a preset press the hold saver
// tracks.
func (h *holdSaver) masterOverrides(p holdPress, what string) bool {
	return mainSpeakerOverrides(h.masterNear, h.logger, p.producer, "PRESET_"+strconv.Itoa(p.slot), p.at, what)
}

// newestRemotePress returns the newest preset press when it came from the
// remote within window of at.
func (h *holdSaver) newestRemotePress(at time.Time, window time.Duration) (holdPress, bool) {
	h.mu.Lock()
	p := h.press
	h.mu.Unlock()
	if p.slot == 0 || p.producer != boxlog.ProducerIRRemote || p.at.IsZero() {
		return holdPress{}, false
	}
	if d := at.Sub(p.at); d > window || d < -window {
		return holdPress{}, false
	}
	return p, true
}

// masterOverridesSelection reports whether the preset activation the speaker
// announced at `at` follows a remote press that was the main speaker's.
func (h *holdSaver) masterOverridesSelection(at time.Time) bool {
	if h == nil || h.masterNear == nil {
		return false
	}
	p, ok := h.newestRemotePress(at, webui.GroupPressWindow)
	if !ok {
		return false
	}
	return h.masterOverrides(p, "the preset recall")
}

// masterOverridesStore reports whether the firmware's own store on slot comes
// from a hold of a remote press that was the main speaker's.
func (h *holdSaver) masterOverridesStore(slot int) bool {
	if h == nil || h.masterNear == nil {
		return false
	}
	p, ok := h.newestRemotePress(h.now(), heldNoticeWindow)
	if !ok || p.slot != slot {
		return false
	}
	return h.masterOverrides(p, "the speaker's own store of the key")
}
