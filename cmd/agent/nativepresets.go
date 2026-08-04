package main

import (
	"context"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/webui"
)

// Native presets: storing a slot as a LOCAL_INTERNET_RADIO station rather than
// a UPnP stream, so the speaker can activate its own hardware key.
//
// Measured on an ST10 (FW 27.0.6, 2026-08-02): a UPNP preset makes the box
// answer its OWN key press with 1036 UNABLE_TO_PROCESS_NOT_LOGGED_IN /
// UpnpRcvdContentItemInWrongState, then flap UPNP -> INVALID_SOURCE -> UPNP,
// and only STR's recovery (clear the transport, push again, verify) gets audio
// out, about eight seconds after the press. The same station stored as a
// native radio item plays in about two seconds and logs nothing, because the
// firmware accepts it.
//
// The cause is not a login. UPNP is the box's local MediaRenderer and reports
// status="UNAVAILABLE" in GET /sources even WHILE it is the actively playing
// source, so the firmware's availability check can never pass. Registering
// UPNP in the emulated account does not change that (tried, no effect) and is
// actively wrong: it is a device-local slot the firmware answers with
// INVALID_SOURCE.
//
// Every preset STR stores is already an HTTP URL on our own stream proxy -
// radio, playlist queues and Spotify alike - so the conversion is uniform and
// needs no per-type handling: the same URL simply travels inside an orion
// station descriptor instead of a UPnP ContentItem.
//
// This migrates the installed base without asking anything of anyone: the next
// preset sync after an agent update rewrites the slots in place. It is gated on
// the box actually reporting the radio source as registered, and falls back to
// the UPnP form otherwise, so a speaker where the emulated account did not take
// keeps exactly the behaviour it has today.

const nativeReadyTTL = 2 * time.Minute

var nativeReady struct {
	sync.Mutex
	checked time.Time
	ok      bool
	// disabled latches native presets off for the rest of this agent run once
	// the box has proven it will not store them. The failure mode is silent
	// (the CLI reports success and the slot stays empty), so something has to
	// stop the agent retrying a write that never lands.
	disabled bool
	why      string
	// failures counts CONSECUTIVE sweeps whose native writes did not land.
	// Latching on the first one was wrong: measured on an ST10 (2026-08-03),
	// six seconds after a reboot three of six slots silently failed to store,
	// which permanently disabled native presets for that whole agent run even
	// though the box was fine moments later. The firmware is documented as
	// accepting GET /presets while still rejecting writes for a while after
	// boot, so a single miss is a timing artefact, not a verdict. Nothing is
	// at risk while we retry: every sweep that finds a native slot the box
	// cannot take puts it straight back on the UPnP form, so the hardware keys
	// keep working throughout.
	failures int
}

// nativeFailureBudget is how many consecutive sweeps may fail to store a native
// preset before the agent stops trying for this run.
const nativeFailureBudget = 3

// disableNativePresets latches the native preset form off after the box was
// measured to ignore it, so the next sweep restores the UPnP form and the
// hardware keys work again.
func disableNativePresets(reason string) {
	nativeReady.Lock()
	already := nativeReady.disabled
	nativeReady.failures++
	n := nativeReady.failures
	if n >= nativeFailureBudget {
		nativeReady.disabled = true
		nativeReady.why = reason
	}
	nativeReady.Unlock()
	l := nativeReadyLogger
	if l == nil || already {
		return
	}
	if n >= nativeFailureBudget {
		l.Warn("native presets: the box keeps accepting the command without storing it, falling back to UPnP presets for this run so the hardware keys keep working",
			"reason", reason, "consecutiveFailures", n)
		return
	}
	// Not a verdict yet: right after a boot the firmware accepts preset writes
	// it does not actually keep. Say so, and retry on the next sweep.
	l.Warn("native presets: a native write did not land, retrying on the next sweep (the slots are on the UPnP form meanwhile, so the keys work)",
		"reason", reason, "attempt", n, "of", nativeFailureBudget)
}

// nativeDropBudget is how many times the box may abandon a native station it
// just accepted before the agent stops storing presets that way on this
// speaker.
const nativeDropBudget = 4

var nativeDrops struct {
	sync.Mutex
	n int
}

// noteNativeStreamDropped records the box leaving a native station on its own.
//
// This is the playback-side counterpart to the write-side latch. A speaker can
// accept the native preset perfectly and then refuse to KEEP it: measured on a
// SoundTouch 20 (2nd generation, v0.9.30), all twelve native presses ended in
// the box abandoning the station within seconds, rescued only by STR's re-push,
// while the same build on the owner's ST30 dropped once in eight presses. The
// write-side latch saw nothing, because the writes were fine.
//
// After a few drops the speaker is put back on the UPnP form, which works there.
// A slower path is a far better outcome than a station that keeps falling over.
func noteNativeStreamDropped() {
	nativeDrops.Lock()
	nativeDrops.n++
	n := nativeDrops.n
	nativeDrops.Unlock()
	if n < nativeDropBudget {
		return
	}
	if disabled, _ := nativePresetsDisabled(); disabled {
		return
	}
	if l := nativeReadyLogger; l != nil {
		l.Warn("native presets: this speaker keeps dropping the station it just started, switching its presets back to the slower form that works here",
			"drops", n)
	}
	// Latch immediately: unlike a lost write, this is not a boot-window
	// artefact that a retry fixes. The next reconcile sweep then sees native
	// unavailable and rewrites every slot in the UPnP form.
	forceDisableNativePresets("box repeatedly abandoned a native station it had accepted")
}

// forceDisableNativePresets latches native presets off at once, for evidence
// that does not improve on a retry.
func forceDisableNativePresets(reason string) {
	nativeReady.Lock()
	nativeReady.disabled = true
	nativeReady.why = reason
	nativeReady.Unlock()
}

// noteNativeWriteLanded records a sweep whose native writes stuck, clearing the
// consecutive-failure count so an early-boot miss cannot accumulate across an
// otherwise healthy run.
func noteNativeWriteLanded() {
	nativeReady.Lock()
	nativeReady.failures = 0
	nativeReady.Unlock()
}

// nativePresetsDisabled reports the latch state, for diagnostics.
func nativePresetsDisabled() (bool, string) {
	nativeReady.Lock()
	defer nativeReady.Unlock()
	return nativeReady.disabled, nativeReady.why
}

// nativePresetStatus is the /api/debug/state section that says, per speaker,
// whether the hardware keys run on the native path or still need the recovery
// machinery.
//
// This exists to answer one question with evidence rather than opinion: when
// can the 1036 recovery code be deleted? That is only safe once field bundles
// show the fallback never firing on any chassis, and nothing in a bundle says
// that today. Read-only, cheap, and it makes every incoming bundle a data point.
func nativePresetStatus(boxHost string) any {
	nativeReady.Lock()
	disabled, why, fails, ok, checked := nativeReady.disabled, nativeReady.why,
		nativeReady.failures, nativeReady.ok, nativeReady.checked
	nativeReady.Unlock()

	st := map[string]any{
		"sourceRegistered":    ok,
		"latchedOff":          disabled,
		"consecutiveFailures": fails,
	}
	if why != "" {
		st["latchReason"] = why
	}
	if !checked.IsZero() {
		st["lastCheck"] = checked.Format(time.RFC3339)
	}
	// The form each slot is ACTUALLY stored in on the box, which is the number
	// that decides whether the recovery paths are still load-bearing.
	if locs, err := fetchBoxPresets(boxHost); err == nil {
		native, upnp := 0, 0
		for _, loc := range locs {
			if isNativeRadioLocation(loc) {
				native++
			} else {
				upnp++
			}
		}
		st["slotsNative"] = native
		st["slotsUPnP"] = upnp
	}
	return st
}

// nativeRadioReady reports whether the box has LOCAL_INTERNET_RADIO registered
// and READY, which is the precondition for storing native presets. The answer
// is cached: this is consulted once per preset slot in a sync sweep, and the
// box only changes it at boot or re-association.
func nativeRadioReady(ctx context.Context, boxHost string) bool {
	nativeReady.Lock()
	defer nativeReady.Unlock()
	if nativeReady.disabled {
		return false
	}
	if !nativeReady.checked.IsZero() && time.Since(nativeReady.checked) < nativeReadyTTL {
		return nativeReady.ok
	}
	was, first := nativeReady.ok, nativeReady.checked.IsZero()
	nativeReady.checked = time.Now()
	nativeReady.ok = probeNativeRadioReady(ctx, boxHost)
	// A silent verdict here is unauditable from a diagnostic bundle: it decides
	// whether every hardware key on this box costs a recovery round or not, and
	// a bundle that only shows UPnP presets cannot say whether the box refused
	// the radio source or the agent never asked. Log every change, and the
	// first answer either way.
	if l := nativeReadyLogger; l != nil && (first || was != nativeReady.ok) {
		if nativeReady.ok {
			l.Info("native presets: the box has LOCAL_INTERNET_RADIO registered, storing hardware presets natively")
		} else {
			l.Warn("native presets: the box does NOT report LOCAL_INTERNET_RADIO as READY, keeping the UPnP preset form (hardware keys stay on the recovery path)")
		}
	}
	return nativeReady.ok
}

// nativeReadyLogger is set once at agent start. A package-level logger keeps
// nativePresetLocation callable from the three preset-sync sites without
// threading a logger through each of them.
var nativeReadyLogger *slog.Logger

// SetNativeReadyLogger wires the logger used for the native-preset verdict.
func setNativeReadyLogger(l *slog.Logger) { nativeReadyLogger = l }

// invalidateNativeRadioReady drops the cached verdict so the next preset sync
// re-probes. Called when the box re-associates, since that is when the source
// registration changes.
func invalidateNativeRadioReady() {
	nativeReady.Lock()
	nativeReady.checked = time.Time{}
	nativeReady.Unlock()
}

func probeNativeRadioReady(ctx context.Context, boxHost string) bool {
	if boxHost == "" {
		return false
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, "http://"+boxHost+":8090/sources", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return false
	}
	var doc struct {
		Items []struct {
			Source string `xml:"source,attr"`
			Status string `xml:"status,attr"`
		} `xml:"sourceItem"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return false
	}
	for _, it := range doc.Items {
		if strings.EqualFold(it.Source, "LOCAL_INTERNET_RADIO") &&
			strings.EqualFold(it.Status, "READY") {
			return true
		}
	}
	return false
}

// nativeRetryWait is the pause before the single re-write of a slot that did not
// land. Measured on an ST10: the first native writes of the first sweep after a
// boot vanish while the same command succeeds a second or two later, so the
// wait is deliberately short - the goal is to ride out a settling
// firmware, not to hammer it.
var nativeRetryWait = 1 * time.Second

// verifyNativeWrites reads the slots back and re-writes the native ones that
// did not land, returning the slots that never made it.
//
// One readback covers the whole batch, so a healthy sweep costs a single extra
// HTTP call; only a sweep that actually lost something pays for retries. That
// matters because the Bose firmware app cannot sustain a high request rate on
// some chassis, which is why this does not verify slot by slot.
func verifyNativeWrites(boxHost string, specs []boxcli.PresetSpec, logger *slog.Logger) []int {
	pending := map[int]boxcli.PresetSpec{}
	for _, s := range specs {
		if s.NativeLocation != "" {
			pending[s.Slot] = s
		}
	}
	if len(pending) == 0 {
		return nil
	}

	missingNow := func() []int {
		after, err := fetchBoxPresets(boxHost)
		if err != nil {
			return nil // cannot tell; treat as "no evidence of loss"
		}
		var out []int
		for slot := range pending {
			if loc, ok := after[slot]; !ok || !isNativeRadioLocation(loc) {
				out = append(out, slot)
			}
		}
		sort.Ints(out)
		return out
	}

	lost := missingNow()
	if len(lost) == 0 {
		return nil
	}
	// A native write that did not land leaves the slot EMPTY, not on its old
	// value, so every one of these slots is a dead hardware key right now. That
	// is why this retries once, quickly, and then stops: measured across four
	// reboots on an ST10, a single re-write after one second recovered every
	// loss (always slots 2 and 3, the first two in the write order). Waiting out
	// a longer backoff would only extend the window in which a key does nothing.
	logger.Info("preset write: some slots did not keep the native form, re-writing them once",
		"slots", lost)
	time.Sleep(nativeRetryWait)
	for _, slot := range lost {
		spec := pending[slot]
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := boxcli.AddPresetNative(ctx, boxHost, slot, spec.Name, spec.NativeLocation); err != nil {
			logger.Warn("preset write: the box refused a native re-write", "slot", slot, "err", err)
		}
		cancel()
		// Space the commands out: each one is its own TAP connection, and six
		// back-to-back connects is what the losing sweeps looked like.
		time.Sleep(boxcli.WriteGap)
	}
	if lost = missingNow(); len(lost) == 0 {
		logger.Info("preset write: every slot kept the native form after one re-write")
		return nil
	}

	// Still empty. Put the UPnP form in NOW rather than leaving dead keys while
	// the next sweep comes around: a key that costs a recovery round is far
	// better than one that does nothing. The next sweep sees a UPnP slot on a
	// box that can take native and tries the upgrade again, and the
	// consecutive-failure latch stops that repeating forever.
	logger.Warn("preset write: slots still not stored, restoring the UPnP form so the keys are not dead",
		"slots", lost)
	for _, slot := range lost {
		spec := pending[slot]
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := boxcli.AddPreset(ctx, boxHost, slot, spec.Name, spec.StreamURL); err != nil {
			logger.Warn("preset write: could not restore the UPnP form either", "slot", slot, "err", err)
		}
		cancel()
		time.Sleep(boxcli.WriteGap)
	}
	return lost
}

// wroteNative reports whether a sync batch contained at least one native
// slot, i.e. whether the readback below is worth an HTTP call.
func wroteNative(specs []boxcli.PresetSpec) bool {
	for _, s := range specs {
		if s.NativeLocation != "" {
			return true
		}
	}
	return false
}

// nativePresetLocation returns the orion station location for a preset, or ""
// when the slot must keep the UPnP form. The location is relative to the BMX
// service baseUrl on purpose: a full URL makes the firmware concatenate the
// two and resolve nothing.
func nativePresetLocation(ctx context.Context, boxHost string, p presets.Preset) string {
	if !nativeStorable(p) {
		return ""
	}
	if !nativeRadioReady(ctx, boxHost) {
		return ""
	}
	stream := boxPresetURL(p)
	if stream == "" {
		return ""
	}
	return webui.OrionStationLocation(stream, p.Name, p.Art)
}

// nativeStorable reports whether a preset may be stored on the native radio
// source.
//
// Spotify presets may NOT, and the reason is the very property that makes the
// native form good for radio: the box activates it entirely by itself, so STR
// stands back and does not run its recall path. A radio stream needs nothing
// more than that. A Spotify preset does: something has to tell the local
// Spotify engine WHICH playlist to load, and that only happens on STR's recall
// path. Stored natively, the box would faithfully play the Spotify proxy URL
// and get whatever the engine happened to be playing before - or silence.
//
// Everything else STR stores (radio stations and play queues) is a plain
// stream from the box's point of view and converts cleanly.
func nativeStorable(p presets.Preset) bool {
	return !strings.EqualFold(strings.TrimSpace(p.Type), "spotify")
}
