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
	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/presets"
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

// presetRefusalSource lists the firmware's preset refusals from its own syslog
// since a moment (boxlog.Reader.PresetRefusalsSince). Wired once in main; unset
// (tests, a box without the syslog ring) it reports nothing.
var presetRefusalSource atomic.Pointer[func(time.Time) []boxlog.PresetRefusal]

func setPresetRefusalSource(fn func(time.Time) []boxlog.PresetRefusal) {
	if fn == nil {
		presetRefusalSource.Store(nil)
		return
	}
	presetRefusalSource.Store(&fn)
}

// firmwarePresetRefusalsSince returns the refused preset writes the firmware
// logged at or after since.
func firmwarePresetRefusalsSince(since time.Time) []boxlog.PresetRefusal {
	p := presetRefusalSource.Load()
	if p == nil {
		return nil
	}
	return (*p)(since)
}

// sweepRefusals is what the firmware's refusals during one write sweep mean.
//
// The firmware does not say WHICH slot it refused, so the refusals are matched
// to the sweep by count and reason. One reason is not a refusal of the key at
// all. After every AddPreset the firmware sends the new key to its account
// (STR's marge), and an item whose source has no account SourceID never gets
// there: "AddPreset - failed due to invalid SourceID", then
// "UpdatePresetFailureCB". A UPNP item never has one, so every UPnP-form write
// (a Spotify key, any slot without the native radio form) logs that pair, every
// time, while the key itself is stored and stays listed. Field bundles
// 2026-10-26: one Spotify key, exactly one refusal per sweep; two Spotify keys,
// exactly two, timed between the native writes either side of them; the four
// native slots of the same sweeps all reached marge; and a sweep into an EMPTY
// list found the two UPnP-form slots listed afterwards.
//
// Until this was told apart, that expected line was read as "the account layer
// was not ready" for EVERY rewritten slot, a re-sync was asked for, and the
// next sweep logged the same line again: a forced pass every five minutes, each
// one pulling a playing speaker off its stream.
type sweepRefusals struct {
	// UPnPForm are the sweep's UPnP-form slots, Expected how many of the
	// refusals they explain. Their keys are stored.
	UPnPForm []int
	Expected int
	// Genuine counts the refusals the UPnP-form writes do not explain, Reason
	// is the first of them, and Suspects the rewritten slots one of them may
	// belong to.
	Genuine  int
	Reason   string
	Suspects []int
}

// judgeSweepRefusals matches the refusals logged during a sweep to its writes.
// written are every spec the sweep sent (sweptSpecs), including the ones the
// CLI answered with an error: a rejected UPnP-form write still logs its
// account-copy refusal pair, and leaving it out of the count would blame that
// line on a native slot. kept are the slots the box already listed before the
// sweep (only those can hide a refusal: a slot that was missing and is listed
// now demonstrably landed).
func judgeSweepRefusals(written []boxcli.PresetSpec, kept []int, refusals []boxlog.PresetRefusal) sweepRefusals {
	var j sweepRefusals
	upnp := map[int]bool{}
	for _, spec := range written {
		if spec.NativeLocation == "" {
			upnp[spec.Slot] = true
			j.UPnPForm = append(j.UPnPForm, spec.Slot)
		}
	}
	sort.Ints(j.UPnPForm)
	sourceID, other := 0, 0
	for _, r := range refusals {
		if r.InvalidSourceID {
			sourceID++
			continue
		}
		if other == 0 && j.Reason == "" {
			j.Reason = r.Reason
		}
		other++
	}
	j.Expected = min(sourceID, len(j.UPnPForm))
	if j.Expected == 0 {
		j.UPnPForm = nil
	}
	excess := sourceID - j.Expected
	if excess > 0 && j.Reason == "" {
		for _, r := range refusals {
			if r.InvalidSourceID {
				j.Reason = r.Reason
				break
			}
		}
	}
	j.Genuine = excess + other
	if j.Genuine == 0 {
		return j
	}
	for _, slot := range kept {
		// More "invalid SourceID" lines than UPnP-form writes can only come
		// from a slot written natively (a native write the firmware would not
		// take for that reason). Any other reason can hit any slot.
		if other == 0 && upnp[slot] {
			continue
		}
		j.Suspects = append(j.Suspects, slot)
	}
	return j
}

// sweptSpecs returns the specs boxcli.SyncAllPresets actually sends, in its
// order: it skips a spec without a stream URL or with a slot outside 1 to 6
// (a row-2 slot is never sent, see boxcli.ErrNotBoxSlot).
func sweptSpecs(specs []boxcli.PresetSpec) []boxcli.PresetSpec {
	out := make([]boxcli.PresetSpec, 0, len(specs))
	for _, spec := range specs {
		if spec.StreamURL == "" || !presets.IsBoxSlot(spec.Slot) {
			continue
		}
		out = append(out, spec)
	}
	return out
}

// presetRefusalSettle is how long after its last write a sweep waits before it
// reads the firmware's refusals. The reader stamps a refusal when it sees the
// syslog line, 1 to 2 s after the AddPreset, while the readback is done about
// half a second after the last write (2026-10-26 bundles). Without the wait the
// account-copy refusal of a UPnP-form key written LAST (a webhook placeholder,
// a Spotify key on slot 6) is missed by its own sweep and lands inside the next
// one's window, where it reads as an extra refusal of a native slot. A var so
// tests need not sleep.
var presetRefusalSettle = 2500 * time.Millisecond

// refusalSettleWait is how much longer a sweep that finished writing at
// writesDone must wait at now before its refusals are complete: only a sweep
// whose last write was in the UPnP form waits (a native write logs no
// account-copy refusal to wait for), and only the part of the settle the
// readback has not already spent.
func refusalSettleWait(swept []boxcli.PresetSpec, writesDone, now time.Time) time.Duration {
	if len(swept) == 0 || swept[len(swept)-1].NativeLocation != "" {
		return 0
	}
	return max(presetRefusalSettle-now.Sub(writesDone), 0)
}

// reportRewrittenSlots logs the forced rewrite of slots the box already listed
// and reports whether the firmware refused it, so a re-sync is worth asking for.
//
// The readback cannot tell such a rewrite from a refused one: the slot is
// listed either way. The firmware's own syslog can, and on a just-booted box it
// did: "UpdatePresetFailureCB ... MargeHSM is in the wrong state" for the
// boot-time full re-sync, while STR logged "rewrote slots the box already
// held, it still lists them" twice and stood the initial sync down (SoundTouch
// 20 field bundle 2026-10-22). The hardware keys were dead. So a refusal seen
// during the sweep is logged as one, attributed only to the slots it can
// belong to (judgeSweepRefusals), and without one the line says what it does
// and does not know instead of reading as success.
//
// The caller asks for a routine re-sync on a refusal rather than dropping into
// the 10 s fast cadence: a real refusal means the account layer is not up yet,
// every AddPreset flips the box to UPNP, and the autopair re-assert that brings
// the account layer up waits for an idle box. Hammering writes would hold off
// the very re-assert that cures it; the landed re-assert asks for its own
// re-sync anyway (autopair onPaired).
func reportRewrittenSlots(logger *slog.Logger, kept []int, j sweepRefusals) bool {
	if j.Expected > 0 {
		logger.Info("preset reconcile: the speaker's account copy refused the UPnP-form keys, as it does for every one (invalid SourceID); the keys themselves are stored",
			"slots", j.UPnPForm, "refusals", j.Expected)
	}
	if len(kept) == 0 {
		return false
	}
	if j.Genuine > 0 && len(j.Suspects) > 0 {
		logger.Warn("preset reconcile: the speaker refused the rewrite (its firmware logged a preset update failure); the hardware keys for these slots may be dead until a retry lands",
			"slots", j.Suspects, "refusals", j.Genuine, "reason", j.Reason)
		return true
	}
	logger.Info("preset reconcile: rewrote slots the box already held; the speaker logged no refusal that belongs to them, but its list cannot confirm a rewrite",
		"slots", kept)
	return false
}

// A refused rewrite asks for a re-sync, and a firmware that keeps refusing
// would get one sweep per ask, each of them flipping the source. So the asks
// back off: the first refusal re-asks at once (the routine budget still
// applies), each further CONSECUTIVE refusal waits twice as long since the last
// ask, capped at an hour. A sweep that lands without a refusal ends the streak.
// No timer is involved: a skipped ask is simply not made, and the next sweep
// comes from the triggers that exist anyway (a wake, a press on a dead key, the
// dead-key insurance pass, the autopair re-assert that asks for its own).
const (
	refusedRewriteBackoffBase = 5 * time.Minute
	refusedRewriteBackoffCap  = time.Hour
)

var (
	// refusedRewriteStreak counts the re-asks made since the last sweep that
	// landed without a refusal.
	refusedRewriteStreak atomic.Int32
	refusedRewriteAskAt  atomic.Int64 // unix nanoseconds of the last re-ask
)

// refusedRewriteBackoff is the minimum time since the previous re-ask before
// the n-th re-ask of one refusal streak may be made: none for the first, then
// 5, 10, 20, 40 minutes, then an hour.
func refusedRewriteBackoff(n int) time.Duration {
	if n <= 1 {
		return 0
	}
	d := refusedRewriteBackoffBase
	for i := 2; i < n && d < refusedRewriteBackoffCap; i++ {
		d *= 2
	}
	return min(d, refusedRewriteBackoffCap)
}

// noteRewriteOutcome records one sweep's verdict and reports whether a re-sync
// should be asked for now; streak is the number of re-asks in the current
// refusal streak and wait how long the backoff still holds when ask is false.
// Called from the reconcile loop only, so the load-then-store is not raced.
func noteRewriteOutcome(refused bool, now time.Time) (ask bool, streak int, wait time.Duration) {
	if !refused {
		refusedRewriteStreak.Store(0)
		return false, 0, 0
	}
	streak = int(refusedRewriteStreak.Load())
	gap := refusedRewriteBackoff(streak + 1)
	if last := refusedRewriteAskAt.Load(); streak > 0 && last != 0 && gap > 0 {
		if since := now.Sub(time.Unix(0, last)); since < gap {
			return false, streak, gap - since
		}
	}
	refusedRewriteStreak.Store(int32(streak + 1))
	refusedRewriteAskAt.Store(now.UnixNano())
	return true, streak + 1, 0
}
