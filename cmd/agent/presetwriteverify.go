package main

// Telling a preset write that landed from one the box merely accepted.
//
// The Bose CLI answers an AddPreset it will not honour exactly as it answers one
// it will, and then stores nothing. The reconcile used to read that answer as
// success and log "preset reconcile healed" per slot, so a speaker with four
// dead hardware keys produced four lines saying the keys had just been repaired.
//
// Measured on a SoundTouch 20 on 2026-09-27: its write ledger reads
// addpreset@INVALID_SOURCE eight times over, the firmware refused every write,
// and the log carried nothing but healed slots. A diagnostic that asserts the
// opposite of the truth is worse than one that says nothing, because it sends
// whoever reads it somewhere else entirely, and that is what it did.
//
// The native form already had a readback of its own (verifyNativeWrites, which
// also re-writes what it finds missing). The UPnP form had none at all, which is
// the form a speaker falls back to precisely when it is having trouble. So this
// covers both and claims nothing before it has looked.

import (
	"log/slog"
	"sort"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/internal/boxcli"
)

// splitWritesByWhatTheBoxKept reads the box's preset list back and reports which
// of the written slots are actually on it.
//
// When the box cannot be read at all, every slot is reported as landed. That is
// deliberate: the alternative is to call a write failed on the strength of a
// failed GET, which would put the reconcile into its fast retry cadence over a
// momentary blip and rewrite presets nobody asked it to touch. No evidence of
// loss is not evidence of loss.
func splitWritesByWhatTheBoxKept(boxHost string, written []boxcli.PresetSpec) (landed, lost []int) {
	if len(written) == 0 {
		return nil, nil
	}
	after, err := fetchBoxPresets(boxHost)
	if err != nil {
		for _, spec := range written {
			landed = append(landed, spec.Slot)
		}
		sort.Ints(landed)
		return landed, nil
	}
	for _, spec := range written {
		if _, ok := after[spec.Slot]; ok {
			landed = append(landed, spec.Slot)
			continue
		}
		lost = append(lost, spec.Slot)
	}
	sort.Ints(landed)
	sort.Ints(lost)
	return landed, lost
}

// splitHealedFromRewritten separates the landed slots that were missing before
// the write (genuinely healed) from those the box already listed (a forced
// rewrite). The readback cannot tell a refused rewrite of a slot that was
// already there from one that landed, so only the first group may be logged
// as healed (#1077: a stereo follower refused all six writes of a forced pass
// and the log claimed six heals).
func splitHealedFromRewritten(landed []int, before map[int]string) (healed, rewritten []int) {
	for _, slot := range landed {
		if _, was := before[slot]; was {
			rewritten = append(rewritten, slot)
			continue
		}
		healed = append(healed, slot)
	}
	return healed, rewritten
}

// presetRefusalSource reports when the firmware last refused a preset write in
// its own syslog (boxlog.Reader.LastPresetRefusal). Wired once in main; unset
// (tests, a box without the syslog ring) it reports nothing.
var presetRefusalSource atomic.Pointer[func() time.Time]

func setPresetRefusalSource(fn func() time.Time) {
	if fn == nil {
		presetRefusalSource.Store(nil)
		return
	}
	presetRefusalSource.Store(&fn)
}

// firmwareRefusedPresetWriteSince reports whether the firmware logged a
// refused preset write at or after since.
func firmwareRefusedPresetWriteSince(since time.Time) bool {
	p := presetRefusalSource.Load()
	if p == nil {
		return false
	}
	at := (*p)()
	return !at.IsZero() && !at.Before(since)
}

// reportRewrittenSlots logs the forced rewrite of slots the box already listed
// and reports whether the firmware refused it.
//
// The readback cannot tell such a rewrite from a refused one: the slot is
// listed either way. The firmware's own syslog can, and on a just-booted box it
// did: "UpdatePresetFailureCB ... MargeHSM is in the wrong state" for the
// boot-time full re-sync, while STR logged "rewrote slots the box already
// held, it still lists them" twice and stood the initial sync down (SoundTouch
// 20 field bundle 2026-10-22). The hardware keys were dead. So a refusal seen
// during the sweep is logged as one, and without one the line says what it
// does and does not know instead of reading as success.
//
// The caller asks for a routine re-sync on a refusal rather than dropping into
// the 10 s fast cadence: the refusal means the account layer is not up yet,
// every AddPreset flips the box to UPNP, and the autopair re-assert that brings
// the account layer up waits for an idle box. Hammering writes would hold off
// the very re-assert that cures it; the landed re-assert asks for its own
// re-sync anyway (autopair onPaired).
func reportRewrittenSlots(logger *slog.Logger, kept []int, refused bool) bool {
	if len(kept) == 0 {
		return false
	}
	if refused {
		logger.Warn("preset reconcile: the speaker refused the rewrite (its firmware logged a preset update failure, the account layer was not ready); the hardware keys for these slots may be dead until a retry lands",
			"slots", kept)
		return true
	}
	logger.Info("preset reconcile: rewrote slots the box already held; the speaker logged no refusal, but its list cannot confirm a rewrite",
		"slots", kept)
	return false
}
