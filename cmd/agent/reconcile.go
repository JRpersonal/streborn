// Preset reconciliation between the local store and the box, and the
// resync-ask scheduler that gates writes on box wakefulness.

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/boxwrites"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
	"github.com/JRpersonal/streborn/internal/webui"
)

// presetResyncAsk flags one forced full box-preset re-sync for the periodic
// reconcile (the #342 dead-key self-heal). presetResyncLast rate-limits the
// routine requests so repeated thumbs presses cannot cause a re-sync storm;
// presetResyncUrgentLast is a SEPARATE, shorter budget for the deterministic
// wipe moments (a 1036 source rejection precedes a forced re-login, and the
// re-onboarding is when the firmware drops its key registrations) so a routine
// ask consumed minutes earlier cannot starve the heal that is actually needed
// now (field bundles 2026-07-22: five dead-key presses produced no resync
// because the boot-time ask had eaten the 10-minute budget).
var (
	presetResyncAsk        atomic.Bool
	presetResyncLast       atomic.Int64 // unix seconds of the last accepted routine request
	presetResyncUrgentLast atomic.Int64 // unix seconds of the last accepted urgent request
	// presetResyncAsker names the origin of the pending ask (thumb / 1036 /
	// paired / power-wake / standby-exit / reconnect), so the standby deferral
	// WARN and the forced-pass log say WHICH trigger wanted to write - the one
	// fact overnight field bundles could never answer.
	presetResyncAsker atomic.Value // string
	// standbyThumbLast tracks the previous standby-read thumb frame so a
	// repeat within a minute reads as a human retrying a dead key, not a
	// phantom frame. presetResyncStandbyOK grants that ask ONE execution
	// despite a STANDBY reading (the user is demonstrably at the box).
	standbyThumbLast      atomic.Int64
	presetResyncStandbyOK atomic.Bool
)

func requestPresetKeyResync(logger *slog.Logger, asker string) {
	const minGapSec = 2 * 60
	now := time.Now().Unix()
	last := presetResyncLast.Load()
	if last != 0 && now-last < minGapSec {
		return
	}
	// Plain Store, not CAS: once the budget check passed, the ask MUST arm.
	// A CAS loser used to be safe because it always lost to another asker who
	// armed the flag; the standby deferral's budget reset introduced a
	// non-arming concurrent writer, and losing to IT dropped a wake re-ask.
	// Two concurrent askers both storing is harmless (one pending ask).
	presetResyncLast.Store(now)
	presetResyncAsker.Store(asker)
	presetResyncAsk.Store(true)
	if logger != nil {
		logger.Info("preset self-heal: scheduling a full box preset re-sync (#342)", "asker", asker)
	}
}

// requestPresetKeyResyncUrgent is requestPresetKeyResync for the moments where
// a box-side key wipe is EXPECTED (a 1036 rejection / forced re-login, a fresh
// pairing): it uses its own short budget so it cannot be starved by a routine
// ask, and the reconcile's 10s wake bounds how often the forced pass can run.
func requestPresetKeyResyncUrgent(logger *slog.Logger, asker string) {
	const minGapSec = 60
	now := time.Now().Unix()
	last := presetResyncUrgentLast.Load()
	if last != 0 && now-last < minGapSec {
		return
	}
	presetResyncUrgentLast.Store(now)
	presetResyncAsker.Store(asker)
	presetResyncAsk.Store(true)
	if logger != nil {
		logger.Info("preset self-heal: urgent full box preset re-sync scheduled (re-login/pairing wipes the key registrations)", "asker", asker)
	}
}

// deferPresetResyncForStandby drops a pending re-sync ask because the box is
// (or seems to be) in standby: a full AddPreset sweep into a sleeping box
// resets the firmware's deep-standby countdown and heals nothing a sleeping
// box can use (#119; the v0.9.17 reconnect writes provably kept boxes awake
// for days). The wake moment re-asks via OnStandbyExit/OnPowerWake, so the
// routine budget is reset here or a deferral seconds before a wake would
// swallow the wake's own re-ask.
func deferPresetResyncForStandby(logger *slog.Logger, src string) {
	asker, _ := presetResyncAsker.Load().(string)
	presetResyncLast.Store(0)
	// The urgent budget resets too: a 1036/paired asker fires on a box that is
	// ALREADY awake (no wake hook is coming for it), so when its ask was
	// dropped on a transient probe error the ONLY natural retrigger is the
	// next 1036 - which the still-burned 60s budget would silence. Urgent
	// triggers are external events that never fire autonomously on a sleeping
	// box, so this cannot create a standby write loop.
	presetResyncUrgentLast.Store(0)
	reason := "box idles in STANDBY"
	if src == "" {
		reason = "box state unreadable, holding writes"
	}
	logger.Warn("preset re-sync deferred: "+reason+", holding the key writes for the wake (#119)",
		"asker", asker)
}

// proxyStreamURL returns the stable loopback URL for a preset. The Bose
// UPnP player opens it — the stream proxy in the stick agent resolves the
// real station redirect behind it and reconnects on token expiry without
// Bose noticing.
func proxyStreamURL(slot int) string {
	return boxurl.StreamSlot(slot)
}

// boxPresetURL is the location stored in the box's OWN preset slot. On a
// hardware press the box first tries to activate this stored ContentItem itself
// (before STR's recall takes over). Radio uses the per-slot stream proxy.
// Spotify must use the single live Ogg stream STR actually serves, not
// /stream/<slot> (which has no Spotify source): otherwise the box's own
// activation fails with INVALID_SOURCE and the display flashes "service
// unavailable" / "select a preset" before the recall (#22). Pointing it at
// /spotify/stream.ogg makes the box's own activation attach cleanly (it shows
// the preset name + buffers) until STR loads the right playlist.
func boxPresetURL(p presets.Preset) string {
	return boxurl.Preset(p.Slot, p.Type == "spotify")
}

// initialBoxPresetSync waits for the box to boot and syncs all stick
// presets to the box's internal preset store. With a retry loop: failed
// slots are retried after 10s, up to 12 times. Background: the Bose
// firmware is sometimes not yet ready for AddPreset calls at boot (autopair
// not done, marge state not initialised). Without retries, slots would stay
// permanently without a box entry — hardware buttons 1-6 would then trigger
// nothing. Initial 30 s wait (was 12 s): measured in practice that the Bose
// firmware needs ~60 s after a cold boot before /info on 8090 responds and
// the marge state is ready. 12 s was optimistic.
// 12 retry slots with a 10 s pause each = ~2 minutes of total runway.
func initialBoxPresetSync(store *presets.Store, boxHost string, logger *slog.Logger) {
	time.Sleep(30 * time.Second)
	specs := make([]boxcli.PresetSpec, 0, 6)
	for _, p := range store.All() {
		specs = append(specs, boxcli.PresetSpec{
			Slot: p.Slot, Name: p.Name, StreamURL: boxPresetURL(p),
			NativeLocation: nativePresetLocation(context.Background(), boxHost, p),
		})
	}
	if len(specs) == 0 {
		return
	}
	logger.Info("starting initial box preset sync", "count", len(specs))

	pending := make(map[int]boxcli.PresetSpec, len(specs))
	for _, p := range specs {
		pending[p.Slot] = p
	}

	for attempt := 0; attempt < 12 && len(pending) > 0; attempt++ {
		if attempt > 0 {
			time.Sleep(10 * time.Second)
		}
		retrySpecs := make([]boxcli.PresetSpec, 0, len(pending))
		for _, p := range pending {
			retrySpecs = append(retrySpecs, p)
		}
		// SyncAllPresets returns ONLY the failed slots; absence means the
		// slot landed. The old loop ranged over the error map and looked for
		// nil values, which never occur - so successes were re-pushed on all
		// 12 attempts and "all synced" could never log (#342).
		errs := boxcli.SyncAllPresets(context.Background(), boxHost, retrySpecs)
		for _, spec := range retrySpecs {
			err, failed := errs[spec.Slot]
			if !failed {
				delete(pending, spec.Slot)
				logger.Info("box preset synced", "slot", spec.Slot, "name", spec.Name, "attempt", attempt)
			} else if attempt == 5 {
				logger.Warn("box preset sync failed permanently", "slot", spec.Slot, "err", err)
			} else {
				logger.Debug("box preset sync fail, retry", "slot", spec.Slot, "attempt", attempt, "err", err)
			}
		}
	}
	if len(pending) == 0 {
		logger.Info("all box presets synced successfully")
	}
}

// periodicPresetReconcile checks every 5 minutes whether the box still has
// all stick presets in its own list. Missing slots are restored via
// boxcli.AddPreset. This way the fix applies automatically without user
// action when, e.g., the Bose firmware has lost individual entries after a
// standby cycle.
func periodicPresetReconcile(store *presets.Store, boxHost string, logger *slog.Logger) {
	// fullDone tracks whether we have done a full re-sync since the box
	// last became ready. The boot-time preset sync can run before the
	// box's preset / hardware-button subsystem is fully up; the slots
	// then show in /presets (so the missing-only path skips them) yet
	// the physical buttons do not recognise them until a fresh AddPreset
	// re-registers them once the box is ready. So the FIRST reconcile
	// after the box leaves OOB re-pushes ALL slots, not just missing
	// ones. Resets when the box drops back to OOB so a re-provision
	// re-registers the buttons. Live-confirmed on a taigan Portable
	// 2026-06-01: buttons 1/2 stayed "empty" until a full re-sync even
	// though /presets listed them.
	//
	// Converge FAST after a cold boot, then idle. A blind 90s pre-wait
	// meant the hardware buttons stayed unregistered for ~90s+ after every
	// reboot, so an early press hit "button not assigned" (#4). The box
	// /info / preset subsystem comes up ~20-45s in, so polling every 10s from
	// 15s wins that first full re-sync as soon as the box is ready, then the
	// loop drops to a 5 min maintenance cadence. reconcileOnce is gated on the
	// box being out of OOB and reachable, so the early polls are cheap no-ops
	// until it is ready. The fast interval re-tightens automatically if the
	// box later drops back to OOB (ready=false -> fullDone=false).
	time.Sleep(15 * time.Second)
	fullDone := false
	everFullDone := false
	lastAwakeForce := time.Now()
	for {
		force := !fullDone
		retryDeferred := false
		if force && everFullDone {
			// A steady-state retry force (failed AddPresets, a transient
			// /presets read error) is NOT the boot window: gate it like any
			// other write, or a box whose BoseApp rejects AddPreset overnight
			// gets hammered with write attempts every 10s all night. Skipping
			// the pass entirely also keeps the missing-only heal from doing
			// the same writes through the back door. The BOOT force stays
			// ungated on purpose: a just-booted box legitimately reads
			// STANDBY before the first press, and gating it would regress the
			// first-press-after-reboot registration (#4).
			if src := boxNowPlayingSource(boxHost); src == "STANDBY" || src == "" {
				retryDeferred = true
				logger.Info("preset reconcile: retry pass held, box in standby or unreadable; resuming on wake or the next maintenance tick")
			}
		}
		if !retryDeferred && !force && presetResyncAsk.CompareAndSwap(true, false) {
			// Dead-key self-heal (#342): a hardware press produced no
			// selection frame, so the box's key layer likely lost its
			// registrations even though /presets still lists them.
			//
			// Standby gate at the EXECUTION, for every asker (#119): the
			// OnConnected gate covered only its own ask, while 1036-urgent,
			// paired-urgent, thumb and late standby-exit asks all wrote into a
			// sleeping box, and a failed probe used to count as awake. A
			// deferred ask is DROPPED, not re-armed - the wake itself re-asks
			// via OnStandbyExit/OnPowerWake (the deferral resets both ask
			// budgets so those re-asks are accepted), and re-arming would
			// turn the 10s wake-poll into an all-night probe loop.
			//
			// The routine budget is cleared at CONSUME time, before the
			// up-to-4s probe: a wake re-ask landing DURING the probe must not
			// be swallowed by the consumed ask's stale rate-limit stamp. On
			// the awake path the stamp is restored via CAS - if a re-ask
			// already overwrote the 0, its own fresh stamp wins.
			lastConsumed := presetResyncLast.Swap(0)
			if presetResyncStandbyOK.CompareAndSwap(true, false) {
				// User-present evidence (repeated dead-key presses): this ask
				// runs even when the box still reads STANDBY.
				presetResyncLast.CompareAndSwap(0, lastConsumed)
				force = true
			} else if src := boxNowPlayingSource(boxHost); src == "STANDBY" || src == "" {
				deferPresetResyncForStandby(logger, src)
			} else {
				presetResyncLast.CompareAndSwap(0, lastConsumed)
				force = true
			}
		}
		// Periodic dead-key insurance while the box is AWAKE (#487): the
		// firmware can silently de-register the hardware key layer while
		// /presets still lists every slot, and on some boxes a dead press
		// emits no frame at all - so no event-driven heal ever fires. Before
		// v0.9.21 the accidental every-11-min reconnect re-sync papered over
		// this; the keepalive fix removed it and one ST10's remote went dead
		// within an hour. A forced pass every 20 minutes restores that
		// insurance, gated on the box NOT being in standby: writes to an
		// awake box never touch the deep-standby countdown (#119), and a box
		// in standby gets its re-sync from the standby-exit hook the moment
		// it wakes.
		if !force && time.Since(lastAwakeForce) > 20*time.Minute {
			src := boxNowPlayingSource(boxHost)
			switch {
			case src == "" || src == "STANDBY":
				// asleep or unreadable: the standby-exit hook owns the wake
			case !resyncSafeSource(src):
				// The box is on a source the USER chose. AddPreset names
				// UPNP, and the firmware activates that source on the write:
				// a field bundle caught the insurance pass yanking a running
				// BLUETOOTH session to UPNP 43 ms after the re-sync line
				// (2026-08-02). Dead-key insurance is not worth interrupting
				// what someone is listening to; the next wake, press or
				// maintenance tick on our own source re-registers the keys.
				logger.Info("preset reconcile: periodic awake re-sync skipped, the box is on a user-chosen source", "source", src)
			default:
				force = true
				logger.Info("preset reconcile: periodic awake re-sync (dead-key insurance, #487)")
			}
			// Stamp even when skipped in standby: re-check 20 min later, not
			// every 10 s tick, and the standby-exit hook owns the wake moment.
			lastAwakeForce = time.Now()
		}
		if force {
			lastAwakeForce = time.Now()
		}
		if retryDeferred {
			// Held pass: no reconcile at all this round (the missing-only
			// heal would write the same failed slots right back). Maintenance
			// cadence, but wake early on a fresh ask (= the box woke up).
			for waited := time.Duration(0); waited < 5*time.Minute && !presetResyncAsk.Load(); waited += 10 * time.Second {
				time.Sleep(10 * time.Second)
			}
			continue
		}
		ready := reconcileOnce(store, boxHost, logger, force)
		fullDone = ready
		if ready {
			everFullDone = true
		}
		if fullDone {
			// Maintenance cadence, but wake early when the self-heal asked
			// for a forced re-sync so a dead key recovers in seconds, not
			// after up to five minutes.
			for waited := time.Duration(0); waited < 5*time.Minute && !presetResyncAsk.Load(); waited += 10 * time.Second {
				time.Sleep(10 * time.Second)
			}
		} else {
			time.Sleep(10 * time.Second)
		}
	}
}

// reconcileOnce returns true once the box is out of OOB and reachable.
// When forceFull is set it re-pushes EVERY stick preset rather than only
// the slots missing from the box's /presets list (see fullDone above).
// resyncSafeSource reports whether a preset re-sync may run while the box is
// on this source. AddPreset names UPNP and the firmware activates that source
// on the write, so a re-sync is only safe when the box is idle or already on
// STR's own source. Anything the user picked would be yanked away mid-listen.
//
// Deliberately an ALLOWLIST, never a list of sources to avoid: the input
// sources are named differently per model and we do not know them all. A
// CineMate reports its TV input as LOCAL where an ST10 says AUX, an SA-5
// answers AUX with sourceAccount AUX1..AUX3, and the Wave's tuner sources are
// invisible to STR entirely. With an allowlist an unknown name is treated as
// "the user chose this", which costs at most one deferred key refresh; a
// denylist would silently interrupt every model whose source name we forgot.
func resyncSafeSource(src string) bool {
	switch src {
	case "UPNP", "INVALID_SOURCE":
		return true
	default:
		return false
	}
}

func reconcileOnce(store *presets.Store, boxHost string, logger *slog.Logger, forceFull bool) bool {
	stick := store.All()
	if len(stick) == 0 {
		return false
	}
	// A forced full re-sync is exactly the moment the box's source registration
	// may have changed (it follows a re-association or a box that just became
	// ready), so do not decide the preset form from a stale cached verdict.
	if forceFull {
		invalidateNativeRadioReady()
	}
	// Do not push presets while the box is still in out-of-box setup.
	// In OOB the Marge state machine is NotAssociated, so every
	// AddPreset fails with "MargeHSM is in the wrong state" and just
	// spams BoseApp's log (and ours) once per cycle. Wait until the box
	// has joined a network. Live-observed on a taigan Portable in OOB,
	// 2026-05-31.
	if boxInSetupOOB(boxHost) {
		logger.Debug("preset reconcile: box still in OOB setup (MargeHSM not associated), skipping until it joins a network")
		return false
	}
	boxLocs, err := fetchBoxPresets(boxHost)
	if err != nil {
		logger.Debug("preset reconcile: box presets not readable", "err", err)
		return false
	}
	// A box-side EMPTY list while STR's store has presets is the "all presets
	// suddenly empty" field state (Wave 2026-07-25: keys dead by evening, a
	// plug pull did not restore them). WARN with both counts so the bundle
	// shows the loss moment and whether the re-registration below healed it or
	// the per-slot AddPreset failures explain why the keys stayed dead.
	if len(boxLocs) == 0 {
		logger.Warn("preset forensics: box reports an EMPTY preset list while the STR store has presets (box lost its key registrations; re-registering now)",
			"storeCount", len(stick))
	}
	// Add the STR store presets the box is missing (or all, on a forced full
	// re-sync). strSlots also drives the prune pass below.
	//
	// A slot the box already has is rewritten in one more case: it still holds
	// the old UPnP form while this box can take a native radio station. That is
	// the migration for every speaker installed before native presets existed.
	// Without it nothing would ever change on them, because the slot is present
	// and a present slot is never re-written.
	strSlots := map[int]bool{}
	var missing []boxcli.PresetSpec
	migrated, reverted := 0, 0
	for _, p := range stick {
		strSlots[p.Slot] = true
		native := nativePresetLocation(context.Background(), boxHost, p)
		loc, onBox := boxLocs[p.Slot]
		boxHasNative := onBox && isNativeRadioLocation(loc)
		upgradable := onBox && native != "" && !boxHasNative && isOwnBoxPresetLocation(loc)
		// The reverse case matters just as much: the slot is stored natively but
		// this box can no longer take that form (the radio source did not
		// register on this boot, or the native write was latched off). Leaving it
		// would point a hardware key at a source the box cannot enter, which is a
		// DEAD key - strictly worse than the UPnP form it replaced. Put it back.
		stale := boxHasNative && native == ""
		// A slot the box already holds natively is otherwise left alone, and
		// that is right: every write wakes the speaker and restarts its standby
		// countdown. One case has to be an exception. Until v0.9.36 a station
		// logo was judged by the file extension of its URL, so stations whose
		// logo came from the icon fallback (it answers .ico URLs with PNG bytes)
		// had STR's stand-in written into the slot. The judgement is fixed, but
		// the slot keeps what it was given, and the owner sees one station with
		// a picture and the next without and cannot tell why.
		//
		// So: repair a slot that carries our stand-in when the preset now
		// resolves to a real picture. The condition stops holding as soon as the
		// rewrite lands, so this is one write per affected slot, once, and never
		// a recurring one.
		relogo := boxHasNative && native != "" &&
			webui.StationLocationCarriesStandInLogo(loc) &&
			!webui.StationLocationCarriesStandInLogo(native)
		switch {
		case upgradable:
			migrated++
		case stale:
			reverted++
		}
		if forceFull || !onBox || upgradable || stale || relogo {
			missing = append(missing, boxcli.PresetSpec{
				Slot: p.Slot, Name: p.Name, StreamURL: boxPresetURL(p),
				NativeLocation: native,
			})
		}
	}
	if migrated > 0 {
		logger.Info("preset migration: rewriting UPnP slots as native radio stations, so the box activates its own hardware keys instead of refusing them (1036)",
			"slots", migrated)
	}
	if reverted > 0 {
		logger.Warn("preset migration: the box no longer offers the native radio source, putting those slots back on the UPnP form so the keys keep working",
			"slots", reverted)
	}
	syncFailed := false
	if len(missing) > 0 {
		if forceFull {
			logger.Info("preset reconcile: full re-sync after box became ready (registers hardware buttons)", "slots", len(missing))
		} else {
			logger.Info("preset reconcile: missing slots on box, syncing", "missing", len(missing))
		}
		// SyncAllPresets returns ONLY failed slots; the old nil-check here
		// could never fire, so healed slots never logged and - worse -
		// persistent AddPreset failures were swallowed silently, invisible
		// in every diagnostic bundle (#342).
		// Forensics for the "the speaker turns itself on" reports (#486 and
		// the 2026-08-02 bundle): AddPreset names UPNP as its source, and the
		// firmware appears to ACTIVATE that source on the write, so a re-sync
		// into a sleeping box can wake it. The ledger already records the
		// source before the write; capture it again right after so a bundle
		// shows the flip as cause and effect instead of correlation, and name
		// which slot the batch started with (the flip lands ~23-43 ms in, i.e.
		// during the FIRST AddPreset, not the batch as a whole).
		srcBefore := boxNowPlayingSource(boxHost)
		boxwrites.NoteN("addpreset", srcBefore, len(missing))
		errs := boxcli.SyncAllPresets(context.Background(), boxHost, missing)
		if srcAfter := boxNowPlayingSource(boxHost); srcAfter != srcBefore {
			logger.Warn("preset forensics: the box changed source across a preset write",
				"before", srcBefore, "after", srcAfter, "slots", len(missing),
				"firstSlot", missing[0].Slot, "forced", forceFull)
		}
		for _, spec := range missing {
			if serr, failed := errs[spec.Slot]; failed {
				syncFailed = true
				logger.Warn("preset reconcile: AddPreset failed", "slot", spec.Slot, "err", serr)
			} else {
				logger.Info("preset reconcile healed", "slot", spec.Slot)
			}
		}
		// Read the slots back before believing the sweep, and re-write the ones
		// that did not land. A native AddPreset the firmware does not like is
		// accepted at the CLI and stores NOTHING, which reads as six healed
		// slots in the log while the hardware keys are dead.
		//
		// Retrying matters because the misses are not random: measured on an
		// ST10 across several reboots, it is the FIRST writes of the first
		// sweep after a boot that vanish (the store is written in a fixed
		// order and slots 2 and 3 lead it), while the UPnP form for the very
		// same slots succeeds moments later. So the firmware is briefly
		// willing to take a preset but not yet able to keep a native one, even
		// though it already advertises the radio source as READY. A short
		// backoff turns that into a non-event.
		if wroteNative(missing) {
			if lost := verifyNativeWrites(boxHost, missing, logger); len(lost) > 0 {
				disableNativePresets(fmt.Sprintf("slots %v stayed empty after a native write", lost))
				syncFailed = true // keep the fast cadence so UPnP is restored now
			} else {
				noteNativeWriteLanded()
			}
		}
	}
	// Prune STR-owned box presets the store no longer backs, so a stale preset
	// from an earlier install does not linger as a dead button. The box's Bose
	// firmware keeps its preset list across an STR reinstall, but STR's store is
	// fresh, so those slots show a name yet cannot play (the store stream URL is
	// gone) - the reporter saw old presets reappear after a reinstall and not
	// work. Remove ONLY locations STR itself wrote (strict boxurl shape, not the
	// loose "/stream/" substring match, which could misread a foreign Icecast
	// URL containing /stream/ as STR-owned and delete a working box preset); a
	// foreign preset (e.g. a box-cached Deezer entry) or any slot STR does have
	// is left untouched.
	for slot, loc := range boxLocs {
		if strSlots[slot] || !isOwnBoxPresetLocation(loc) {
			continue
		}
		boxwrites.Note("removepreset", boxNowPlayingSource(boxHost))
		rctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		if rerr := boxcli.RemovePreset(rctx, boxHost, slot); rerr != nil {
			logger.Warn("preset reconcile: could not remove a stale STR preset", "slot", slot, "err", rerr)
		} else {
			logger.Info("preset reconcile: removed a stale STR preset the store no longer backs (dead button after reinstall)", "slot", slot)
		}
		cancel()
	}
	// A pass with failed AddPresets is NOT done: returning true here dropped
	// the loop to the 5-minute maintenance cadence while the box's key layer
	// stayed empty, which is exactly the "hardware keys dead for ~6.5 minutes
	// after every power cycle" window in the field bundles (a just-booted
	// BoseApp accepts GET /presets but rejects AddPreset for a while). Report
	// not-ready so the 10s fast cadence retries until every slot registered.
	if syncFailed {
		logger.Warn("preset reconcile: some AddPresets failed, keeping the fast retry cadence")
		return false
	}
	return true
}

// fetchBoxPresets reads GET /presets from the Bose API and returns each set
// slot's ContentItem location (slot -> location URL). The location is what tells
// STR-owned presets (its own /stream/ or /spotify/ URLs) apart from foreign ones,
// so the reconcile can prune only its own stale entries and never a box-native
// preset.
func fetchBoxPresets(boxHost string) (map[int]string, error) {
	entries, err := fetchBoxPresetsFull(boxHost)
	if err != nil {
		return nil, err
	}
	out := map[int]string{}
	for _, e := range entries {
		out[e.Slot] = e.Location
	}
	return out, nil
}

// boxPresetEntry is one slot of the box's own :8090/presets list, with enough
// ContentItem detail to seed the webui's box-native snapshot and to identify a
// lost STR preset for the store recovery.
type boxPresetEntry struct {
	Slot     int
	Location string
	Name     string
	Source   string
	Type     string
	Account  string
}

// fetchBoxPresetsFull reads GET /presets and returns each set slot's
// ContentItem fields.
func fetchBoxPresetsFull(boxHost string) ([]boxPresetEntry, error) {
	client := http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:8090/presets", boxHost))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out []boxPresetEntry
	// Bose format: <presets><preset id="1" ...><ContentItem location="..."/></preset></presets>
	for _, blk := range presetBlockRegex.FindAllStringSubmatch(string(body), -1) {
		slot := 0
		fmt.Sscanf(blk[1], "%d", &slot)
		if slot < 1 || slot > 6 {
			continue
		}
		e := boxPresetEntry{Slot: slot}
		if m := presetLocationRegex.FindStringSubmatch(blk[0]); m != nil {
			e.Location = m[1]
		}
		if m := presetItemNameRegex.FindStringSubmatch(blk[0]); m != nil {
			e.Name = xmlEntityUnescape(m[1])
		}
		if m := presetSourceRegex.FindStringSubmatch(blk[0]); m != nil {
			e.Source = m[1]
		}
		if m := presetTypeRegex.FindStringSubmatch(blk[0]); m != nil {
			e.Type = m[1]
		}
		if m := presetAccountRegex.FindStringSubmatch(blk[0]); m != nil {
			e.Account = m[1]
		}
		out = append(out, e)
	}
	return out, nil
}

// xmlEntityUnescape reverses the five predefined XML entities in text content
// (Bose escapes station names like "Pop & Rock" in /presets).
var xmlEntityReplacer = strings.NewReplacer(
	"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'")

func xmlEntityUnescape(s string) string { return xmlEntityReplacer.Replace(s) }

// margeXMLEscape escapes user-provided text (station names, art URLs) for the
// marge preset template, which is a text/template and does not escape XML.
var margeXMLEscaper = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

func margeXMLEscape(s string) string { return margeXMLEscaper.Replace(s) }

// firstArtURL returns the first entry of a pipe-separated art fallback chain
// (how preset.Art is persisted); the box only ever gets one URL.
func firstArtURL(art string) string {
	if idx := strings.Index(art, "|"); idx >= 0 {
		return art[:idx]
	}
	return art
}

// seedBoxPresetsAndRecoverStore runs once in the background at agent start.
// Two jobs (#252, the ST20 whose presets showed "unassigned although they are
// assigned"):
//
//  1. Seed the webui's box-native preset snapshot from :8090/presets. The
//     snapshot otherwise stays empty until the box happens to emit a gabbo
//     presetsUpdated frame, so the app showed every slot as unassigned even
//     though the box's own list still held them.
//  2. If the NAND preset store came up EMPTY while the box still lists STR's
//     own /stream/ presets, the store was lost (the pre-v0.9.14 non-durable
//     save left presets.json at 0 bytes after a standby power-cut; the loss
//     surfaced when the OTA restarted the agent). Every hardware press then
//     404s at the stream proxy: the buttons are dead although the box's key
//     layer works. Restore what the recently-played history can identify by
//     exact station/playlist name, so the buttons come back without the user
//     re-saving every slot.
//
// firstAttempt (nil = none) is called exactly once, right after the FIRST
// :8090/presets read attempt returns, success or not. main gates autopair's
// first forced re-assert on it: that re-assert makes the box re-read its cloud
// presets from marge, and with an EMPTY stick store marge answers an empty
// list, which wipe-prone firmwares translate into dropping their own preset
// list within seconds - i.e. the re-onboarding at t=8s destroyed the very box
// list this recovery wanted to read at t=20s, and the presets were lost for
// good on exactly the warm-restart (#252/OTA) case the recovery targets.
// Reading first, then pairing, closes the race.
func seedBoxPresetsAndRecoverStore(store *presets.Store, recentStore *recent.Store, boxHost string, seed func([]webui.BoxPreset), logger *slog.Logger, firstAttempt func()) {
	if boxHost == "" || store == nil {
		if firstAttempt != nil {
			firstAttempt()
		}
		return
	}
	// First read attempt runs IMMEDIATELY: on a warm agent restart (OTA) the
	// box answers right away, and the snapshot must be taken before autopair's
	// first forced re-assert (see firstAttempt above). Only a cold boot - where
	// :8090 needs ~60s to come up - falls back to the gentle 20s polling, which
	// gives up quietly after ~10 minutes (the periodic reconcile keeps running
	// regardless).
	var entries []boxPresetEntry
	for i := 0; i < 30; i++ {
		if i > 0 {
			time.Sleep(20 * time.Second)
		}
		var err error
		entries, err = fetchBoxPresetsFull(boxHost)
		if i == 0 && firstAttempt != nil {
			firstAttempt()
		}
		if err == nil {
			break
		}
		entries = nil
	}
	if len(entries) == 0 {
		return
	}
	if seed != nil {
		bps := make([]webui.BoxPreset, 0, len(entries))
		for _, e := range entries {
			bps = append(bps, webui.BoxPreset{
				Slot: e.Slot, Source: e.Source, Type: e.Type,
				Location: e.Location, SourceAccount: e.Account, Name: e.Name,
			})
		}
		seed(bps)
		logger.Info("box preset snapshot seeded from :8090/presets", "slots", len(bps))
	}
	if recentStore == nil || len(store.All()) > 0 {
		return
	}
	recovered := 0
	recents := recentStore.All()
	for _, e := range entries {
		if !isOwnBoxPresetLocation(e.Location) || e.Name == "" {
			continue
		}
		if _, exists := store.Get(e.Slot); exists {
			continue
		}
		wantSpotify := strings.Contains(e.Location, "/spotify/")
		// Newest matching history entry wins (the ring is oldest-first).
		for i := len(recents) - 1; i >= 0; i-- {
			r := recents[i]
			if r.CardName != e.Name || r.CardURL == "" {
				continue
			}
			if wantSpotify != (r.Source == "spotify") {
				continue
			}
			p := presets.Preset{Slot: e.Slot, Name: e.Name, Art: r.CardArt, Homepage: r.Homepage}
			if wantSpotify {
				p.Type = "spotify"
				p.URI = r.CardURL
				p.Account = r.Account
			} else {
				p.Type = "radio"
				p.StreamURL = r.CardURL
			}
			if err := store.SetSlot(p); err != nil {
				logger.Warn("preset store recovery: could not save recovered slot", "slot", e.Slot, "err", err)
			} else {
				// Warn on purpose: this must be visible in a diagnostic bundle.
				logger.Warn("preset store recovery: restored a lost preset from the recently-played history",
					"slot", e.Slot, "name", e.Name, "type", p.Type)
				recovered++
			}
			break
		}
	}
	if recovered > 0 {
		logger.Warn("preset store recovery: the preset store was empty while the box still lists STR presets (likely wiped by a pre-v0.9.14 standby power-cut); restored what the history could identify",
			"recovered", recovered)
	}
}

// presetBlockRegex captures one <preset id="N" ...> ... </preset> block; (?s)
// lets . span the newlines Bose puts inside the block. presetLocationRegex then
// pulls the ContentItem location out of that block.
var presetBlockRegex = regexp.MustCompile(`(?s)<preset id="(\d+)".*?</preset>`)
var presetLocationRegex = regexp.MustCompile(`location="([^"]*)"`)
var presetItemNameRegex = regexp.MustCompile(`<itemName>([^<]*)</itemName>`)
var presetSourceRegex = regexp.MustCompile(`source="([^"]*)"`)
var presetTypeRegex = regexp.MustCompile(`type="([^"]*)"`)
var presetAccountRegex = regexp.MustCompile(`sourceAccount="([^"]*)"`)
