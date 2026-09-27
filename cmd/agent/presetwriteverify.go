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
	"sort"

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
