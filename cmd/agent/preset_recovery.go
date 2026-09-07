// The preset store recovery's verdict, the empty-store prune and the seams
// they share with the reconcile.
//
// Field case (#882, three SoundTouch 10s): STR was removed from one speaker
// and reinstalled. The removal deleted the STR folder on the speaker, and
// with it the preset store; the reinstall started empty. The firmware kept
// the six native keys the old install had registered, so the desktop drew
// six playable stations on a speaker whose store had nothing, the phone
// remote (which reads the store) showed six empty keys, and the reporter
// concluded the remote had failed to sync. Nothing in the agent noticed:
// the store recovery and the prune matched only the absolute proxy form of
// a key, not the relative native form the speaker reports, and an empty
// store made the reconcile return before reading the box at all.

package main

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/boxwrites"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/webhooks"
	"github.com/JRpersonal/streborn/internal/webui"
)

// The box calls the recovery and the empty-store prune make, as seams: the
// ports are fixed by the firmware, so a test cannot hand the agent an httptest
// listener for them.
var (
	// fetchBoxPresetsFullFn is the :8090/presets read the recovery and the
	// reconcile go through.
	fetchBoxPresetsFullFn = fetchBoxPresetsFull
	// boxInSetupOOBFn is the /setup probe the prune makes before it writes.
	boxInSetupOOBFn = boxInSetupOOB
	// removeBoxPresetFn is the RemovePreset write of the prune.
	removeBoxPresetFn = boxcli.RemovePreset
	// boxNowPlayingSourceFn is the now_playing read the write ledger records
	// the source from.
	boxNowPlayingSourceFn = boxNowPlayingSource
)

// presetRecoverySettled is set once seedBoxPresetsAndRecoverStore has had its
// one chance to read the box list and refill an empty store from the
// recently-played history. The empty-store prune waits for it: a prune that
// ran first would delete the very slots the recovery identifies the lost
// presets by.
var presetRecoverySettled atomic.Bool

// presetRecoveryVerdict is what the recovery found at agent start, served
// under preset_recovery in /api/debug/state so a bundle from a reinstalled
// speaker can say whether the dead keys were ever seen and why the store
// stayed empty.
type presetRecoveryVerdict struct {
	At             string `json:"at"`
	BoxSlots       int    `json:"boxSlots"`
	StrOriginSlots int    `json:"strOriginSlots"`
	HistoryEntries int    `json:"historyEntries"`
	Recovered      int    `json:"recovered"`
	// Reason: "ok" (something was recovered), "no-history" (the history was
	// empty, the reinstall case), "no-name-match" (history present, no entry
	// named like a lost key), "no-str-origin-slots", "store-not-empty",
	// "no-box-list" (the box never answered), "" (not run yet).
	Reason string `json:"reason"`
}

var (
	presetRecoveryMu   sync.Mutex
	presetRecoveryLast presetRecoveryVerdict
)

func setPresetRecoveryVerdict(v presetRecoveryVerdict) {
	presetRecoveryMu.Lock()
	presetRecoveryLast = v
	presetRecoveryMu.Unlock()
}

// presetRecoveryStatus is the debug-section body.
func presetRecoveryStatus() any {
	presetRecoveryMu.Lock()
	defer presetRecoveryMu.Unlock()
	return presetRecoveryLast
}

// boxPresetListSink receives every box preset list the recovery or the
// reconcile reads, so the reads the passes make anyway keep the webui's
// box-preset cache current (no extra :8090 read, no timer). Set once by main;
// unset in tests. Atomic because the reconcile goroutines start before the
// webui server that the sink writes to exists.
var boxPresetListSink atomic.Pointer[func([]webui.BoxPreset)]

func setBoxPresetListSink(fn func([]webui.BoxPreset)) { boxPresetListSink.Store(&fn) }

func publishBoxPresetList(entries []boxPresetEntry) {
	if fn := boxPresetListSink.Load(); fn != nil {
		(*fn)(boxPresetsFromEntries(entries))
	}
}

// boxPresetsFromEntries maps a :8090/presets read into the webui's shape, with
// the StrOrigin flag: the raw fact that STR wrote the slot. The Lost verdict
// is stamped by the sink (stampLostBoxPresets), which is where the store and
// the webhook slots are known.
func boxPresetsFromEntries(entries []boxPresetEntry) []webui.BoxPreset {
	bps := make([]webui.BoxPreset, 0, len(entries))
	for _, e := range entries {
		bps = append(bps, webui.BoxPreset{
			Slot: e.Slot, Source: e.Source, Type: e.Type,
			Location: e.Location, SourceAccount: e.Account, Name: e.Name,
			StrOrigin: isOwnBoxPresetLocation(e.Location),
		})
	}
	return bps
}

// stampLostBoxPresets sets Lost on every StrOrigin slot the store has nothing
// on and no webhook-only key claims, in place, and returns the same slice.
//
// The webhook exclusion is the point of doing this on the agent. A key
// configured as "webhook only" (#536) holds a placeholder in STR's own
// /stream/N form so the firmware reports its press, and is kept OUT of the
// store on purpose (webhookOnlySlots). Judged by the location alone, as the
// desktop would have to, it is indistinguishable from a dead key: the tile
// would say the key was lost in a reinstall, a tap would explain instead of
// pressing the key, and the webhook the user set up would never fire from the
// app. Only the agent sees the store and the webhook config, so only the
// agent can tell the two apart.
//
// Every producer hands over a fresh slice (a gabbo frame mapping, a
// boxPresetsFromEntries result), so the in-place write never touches a list
// the webui cache already holds.
func stampLostBoxPresets(bps []webui.BoxPreset, store *presets.Store, wh *webhooks.Store) []webui.BoxPreset {
	stored := map[int]bool{}
	if store != nil {
		for _, p := range store.All() {
			stored[p.Slot] = true
		}
	}
	hooked := map[int]bool{}
	for _, slot := range wh.ReplacePresetSlots() {
		hooked[slot] = true
	}
	for i := range bps {
		bps[i].Lost = bps[i].StrOrigin && !stored[bps[i].Slot] && !hooked[bps[i].Slot]
	}
	return bps
}

// staleStrSlots picks the box slots the reconcile may prune: slots STR itself
// wrote (isOwnBoxPresetLocation, strict) that the store no longer backs. A
// foreign slot (a speaker-cached Deezer or TuneIn entry) is never chosen, nor
// is any slot the store or a webhook key claims (strSlots).
//
// One native shape is left alone on purpose: a descriptor that points at
// ANOTHER key's proxy is the speaker's own hold-to-store gesture (it keeps the
// playing item as is), and the store learns that slot from marge a moment
// later. Pruning it in that moment would delete the key the user just made;
// the reconcile's re-own pass rewrites it onto its own form instead.
func staleStrSlots(boxLocs map[int]string, strSlots map[int]bool) []int {
	var out []int
	for slot, loc := range boxLocs {
		if strSlots[slot] || !isOwnBoxPresetLocation(loc) {
			continue
		}
		if own, ok := webui.StationLocationOwnSlot(loc); ok && own != slot {
			continue
		}
		out = append(out, slot)
	}
	sort.Ints(out)
	return out
}

// pruneBoxSlots removes the given slots from the speaker's own preset list:
// stale keys of an earlier install that show a name yet cannot play, because
// the store stream URL behind them is gone. Each removal is ledgered like
// every other box write.
func pruneBoxSlots(boxHost string, slots []int, logger *slog.Logger) {
	for _, slot := range slots {
		boxwrites.Note("removepreset", boxNowPlayingSourceFn(boxHost))
		rctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		if rerr := removeBoxPresetFn(rctx, boxHost, slot); rerr != nil {
			logger.Warn("preset reconcile: could not remove a stale STR preset", "slot", slot, "err", rerr)
		} else {
			logger.Info("preset reconcile: removed a stale STR preset the store no longer backs (dead button after reinstall)", "slot", slot)
		}
		cancel()
	}
}

// pruneStaleKeysOnEmptyStore is the reconcile pass for an EMPTY store with no
// webhook keys. Until #882 the pass returned at once and the box was never
// read: the six native keys a removed install had left on the speaker stayed
// registered, the firmware kept reporting them, and every press fetched a
// /stream/N the fresh agent answered with 404. The pass now reads the list,
// prunes the STR-origin slots and reports the box as done, so the loop drops
// to its maintenance cadence: one /presets read per five minutes, exactly
// what the non-empty pass costs, and no new timer.
//
// Gated behind the store recovery (presetRecoverySettled): the seed read at
// agent start is the one chance the recovery has to identify those slots
// from the recently-played history, and a prune that ran first would delete
// the evidence. Until the recovery has settled the pass stays the cheap
// no-op it always was.
//
// "Empty" is decided AGAIN after the box read, not only on entry. The
// desktop's post-install restore PUTs the stashed slots and syncs them onto
// the box within seconds of the agent binding its port, and on a warm chassis
// this pass runs at t=15 s with the recovery already settled. A pass that
// entered on an empty store and then read the box would see the six keys the
// restore had just registered, all in STR's own form, and against the empty
// store it entered on every one would be stale: six removals, six dead keys
// until the maintenance tick re-adds them, on the very reinstall flow the
// stash exists for, the first time the owner presses a key. So once the list
// is in, a store or webhook config that filled meanwhile makes the pass give
// way; the next pass takes the normal path and registers what it finds.
func pruneStaleKeysOnEmptyStore(store *presets.Store, wh *webhooks.Store, boxHost string, logger *slog.Logger) bool {
	if !presetRecoverySettled.Load() {
		return false
	}
	if boxInSetupOOBFn(boxHost) {
		return false
	}
	entries, err := fetchBoxPresetsFullFn(boxHost)
	if err != nil {
		logger.Debug("preset reconcile: box presets not readable", "err", err)
		return false
	}
	publishBoxPresetList(entries)
	if stick := store.All(); len(stick) > 0 || len(webhookOnlySlots(wh, stick)) > 0 {
		logger.Info("preset reconcile: the store filled while the box list was read (a restore or a save landed), leaving the keys to the next pass",
			"storeCount", len(stick))
		return false
	}
	boxLocs := make(map[int]string, len(entries))
	for _, e := range entries {
		boxLocs[e.Slot] = e.Location
	}
	stale := staleStrSlots(boxLocs, nil)
	if len(stale) == 0 {
		return true
	}
	logger.Warn("preset reconcile: store empty, box lists STR-origin slots (dead keys after reinstall), pruning",
		"boxSlots", len(boxLocs), "strOriginSlots", len(stale), "slots", stale)
	pruneBoxSlots(boxHost, stale, logger)
	return true
}
