// presetWsHandler reacts to events on the Bose WebSocket (gabbo) bus:
// hardware preset recall, playback verification, and standby bookkeeping.

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/internal/autopair"
	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/boxws"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/spotify"
	"github.com/JRpersonal/streborn/internal/upnp"
	"github.com/JRpersonal/streborn/internal/webhooks"
)

// presetWsHandler implements boxws.Handler and, on a hardware preset
// button press, calls the UPnP renderer with the stream URL from the preset store.
type presetWsHandler struct {
	logger   *slog.Logger
	store    *presets.Store
	renderer *upnp.Renderer
	autoPair *autopair.Manager
	boxHost  string
	spotify  *spotify.Manager
	// onUserStop is invoked when the box reports a deliberate playback stop
	// over gabbo (STOP_STATE). Wired to webui.NoteUserStop so the auto-re-push
	// does not fight a wanted stop. nil-safe.
	onUserStop func()
	// lastUserActivity reports when the box last emitted a userActivityUpdate
	// (any physical key on the box or IR remote). Wired to
	// boxws.Client.LastUserActivity; nil-safe. The volume restore consults it
	// so a level the user chose BY HAND during a recall recovery is never
	// overridden.
	lastUserActivity func() time.Time
	// lastUserStop is when OnUserStop last fired (guarded by lastUserStopMu).
	// The hardware recall verifies (verifyPlayURL / verifySpotifyPlaying)
	// compare it against their recall start so a deliberate stop DURING the
	// verify window stands the re-push down instead of being overridden --
	// stop-after-recall-start, mirroring the webui side's stand-down, not a
	// rolling window (an older stop must not suppress a recall the user just
	// asked for).
	lastUserStopMu sync.Mutex
	lastUserStop   time.Time
	// lastSourceReject is when the box last rejected STR's UPnP source with a 1036
	// UpnpRcvdContentItemInWrongState (a preset->preset switch racing the previous
	// source's teardown). verifySpotifyPlaying consults it so it re-points instead
	// of standing down on the box's "attached + buffering" appearance while the box
	// is actually stuck on that rejection and never plays (ST30 4->5, 2026-07-14).
	sourceRejectMu   sync.Mutex
	lastSourceReject time.Time
	// onRemoteSkip advances playback on a hardware remote Next/Prev key, source-
	// aware (Spotify or the STR play queue). Wired to webui.TransportSkip so the
	// hardware keys use the same skip logic as the phone remote; without it a
	// folder skip stalled until the current track ended naturally (#300). nil-safe.
	onRemoteSkip func(ctx context.Context, forward bool) (string, error)
	// webhooks fires the user-configured HTTP request on a "thumb" trigger (a
	// lone userActivityUpdate, see OnThumbActivity). nil-safe.
	webhooks *webhooks.Store
	// margeGroupClear drops STR's stereo-pair record for this speaker. Called
	// when the BOX itself reports its pair torn down, which is how a teardown
	// done in the Bose app (or one that reached only the other member) reaches
	// STR at all. nil-safe.
	margeGroupClear func(reason string)
	// noteLastPlay records a hardware-preset recall as the webui's lastPlay so
	// the auto-re-push and the wake-resume know what to resume (the hardware path
	// plays straight through the renderer, bypassing the webui's own lastPlay).
	// Returns the new recall generation for the supersession check below.
	// Wired to webui.NoteLastPlay. nil-safe.
	noteLastPlay func(boxURL, title, art, mime string) uint64
	// recallGenFn reads the webui's current recall generation. A hardware
	// verify captures the generation its own noteLastPlay returned and stands
	// down as soon as the live value moves on: a newer play (second hardware
	// press or an app recall) supersedes the old loop, which otherwise kept
	// re-pushing its stale URL over the user's newest choice for up to ~26s
	// ("pressed 2, got 1"). The soft path has had exactly this guard
	// (verifyRecall's recallGen) all along. Wired to webui.RecallGeneration.
	// nil-safe.
	recallGenFn func() uint64
	// pressSeq orders the hardware presses themselves. It is bumped on the
	// gabbo read loop (strictly in press order) before the slow recall work is
	// handed to a goroutine, so a stale recall goroutine can recognise that a
	// newer press exists even before either has reached noteLastPlay.
	pressSeq atomic.Uint64
	// Wedge detection hooks (webui.NoteRecallExhausted / NoteBoxHealthy): the
	// hardware recall path reports exhausted/successful verifies so the
	// power-cycle hint also fires when the user only uses the preset keys.
	noteRecallExhausted func()
	noteBoxHealthy      func()
	// noteRecentPreset records a hardware-preset press into the recently-played
	// ring (#135). Wired to webui.NoteRecentPreset. nil-safe.
	noteRecentPreset func(presets.Preset)
	// onPowerWake is invoked when the box leaves standby on a power press: a
	// powerStateUpdated on firmware that sends it, or the DO_NOT_RESUME selection
	// restore on firmware that does not (Portable/taigan). Resumes the last
	// station, the Bose-style power-on preset. Wired to webui.ResumeLastPlay, which
	// is gated by the per-box opt-out and a zone-membership self-wake guard.
	// nil-safe.
	onPowerWake func()
	// onBoxReconnect fires after the gabbo WS (re)connects. After a deep/overnight
	// standby the box wakes and emits its first preset/now-selection frame before
	// STR has reconnected, so the press is lost and nothing plays until a second
	// press (#183). This recovers that stuck wake. Wired to
	// webui.RecoverAfterReconnect, which reuses the power-on resume guards. nil-safe.
	onBoxReconnect func()
	// onStandbyExit fires when the box leaves STANDBY (the user switched it on).
	// Wired to webui.Server.RunDeferredResume so a stream the firmware dropped
	// while the box was off comes back on the user's own wake. nil-safe.
	onStandbyExit func()
	// onEnterStandby fires when STR's UPnP source drops to STANDBY (a power-off
	// seen over gabbo). It clears the box transport so ST20 (scm) firmware that
	// oscillates UPNP<->STANDBY does not switch the speaker back on (#197). Wired to
	// webui.HandleEnterStandby, which is zone-guarded and debounced. nil-safe.
	onEnterStandby func()
	// recentlyPoweredOff reports whether STR saw this box drop UPNP->STANDBY within
	// the bounce window. The hardware-preset recall verify (verifyPlayURL) checks it
	// so it does NOT re-push the stream when the user powered the box off mid-recall
	// (the box reads "not playing" because it is in standby), which on scm ST20
	// firmware switched the speaker back on (#197). Wired to webui.RecentlyPoweredOff.
	// nil-safe.
	recentlyPoweredOff func() bool
	// standbyStopAfter is the absolute variant of recentlyPoweredOff: it reports
	// a power-off strictly AFTER the given time (the press). The rolling 6s
	// window could expire between two verify ticks and let a re-push wake the
	// powered-off box after all; the absolute stamp cannot. Preferred when
	// wired; recentlyPoweredOff stays as the fallback. Wired to
	// webui.StandbyStoppedAfter. nil-safe.
	standbyStopAfter func(t time.Time) bool
	// noteBoxPresets records the box's OWN preset list (gabbo presetsUpdated),
	// including foreign sources like Deezer that STR did not set, into the webui
	// so the app can show and preserve them (Option C). Wired to
	// webui.NoteBoxPresets. nil-safe.
	noteBoxPresets func([]boxws.BoxPreset)
	// recallSlot lets the webui claim a hardware preset press for a queue preset
	// (a saved DLNA folder): it returns true and starts the play-queue when the
	// slot is a queue preset, false otherwise so the single-track recall below
	// still runs. The queue lives in webui, so the queue start happens there.
	// Wired to webui.Server.RecallSlot. nil-safe.
	recallSlot func(ctx context.Context, slot int) bool
	// noteUserPlay records a hardware preset press as an explicit user play in
	// the webui: it clears the deliberate-stop latches (a press outranks any
	// earlier stop, #419) and anchors the standby-flip discriminator. Wired to
	// webui.Server.NoteUserPlay. nil-safe.
	noteUserPlay func()
	// Test seams over the box CLI / now_playing probes. nil = the real
	// implementations (boxcli.WakeAndWait, boxcli.PowerOn, boxPlayingURL,
	// boxNowPlayingSource); the verify tests stub them so wake ordering and
	// the stuck-source nudge are assertable without hardware.
	wakeBox      func(ctx context.Context, host string) error
	sysPowerFn   func(ctx context.Context, host string) error
	boxPlayingFn func(url string) bool
	boxSourceFn  func() string
	// boxSummaryFn seams the now_playing settle probe (boxNowPlayingSummary)
	// the verify logs on success/exhaustion. nil = real :8090 fetch.
	boxSummaryFn func() (source, itemName, playStatus string)

	// slotPulled reports whether the box is credibly playing THIS slot's
	// proxied stream for a recall anchored at the given time. It is the recall
	// verify's ground truth: the box pulling THIS slot's audio through the
	// proxy proves it plays what this recall pushed, whereas now_playing lags
	// and can still name the PREVIOUS preset seconds after the box switched.
	// Slot-scoped AND liveness-aware on purpose: the old global stamp let any
	// proxied fetch certify a failed recall, and even the slot stamp alone let
	// a 36ms fetch that died in the box's re-login bounce count as success
	// (#252 field bundles). Wired to streamproxy.Server.SlotPulledSince.
	// nil-safe.
	slotPulled func(slot int, since time.Time) bool
	// slotFetchLive reports whether the box currently holds an OPEN connection to
	// the slot's proxied stream (streamproxy.Server.SlotFetchLive). Unlike
	// slotPulled it makes no sustained-duration call on a closed fetch, so the
	// recall verify can tell "still pulling audio" from a login error's brief
	// source bounce that opened the stream and closed. nil-safe.
	slotFetchLive func(slot int) bool
	// loginErrorSinceFn reports whether the box rejected a source as
	// NOT_LOGGED_IN (1036) after t (webui.Server.LoginErrorSince). Wired so the
	// recall verify does not trust a now-closed short slot pull as success while
	// a login error is in flight for this recall. nil-safe.
	loginErrorSinceFn func(t time.Time) bool
}

// slotPulledSince reports whether the box is credibly playing THIS slot's
// proxied stream for a recall anchored at t. A preset that plays a direct
// (non-proxied) URL never stamps; the now_playing check decides for it.
func (h *presetWsHandler) slotPulledSince(slot int, t time.Time) bool {
	if h.slotPulled == nil {
		return false
	}
	return h.slotPulled(slot, t)
}

// slotFetchLiveNow reports whether the box currently holds an open pull of the
// slot's proxied stream. nil-safe (false when unwired).
func (h *presetWsHandler) slotFetchLiveNow(slot int) bool {
	if h.slotFetchLive == nil {
		return false
	}
	return h.slotFetchLive(slot)
}

// loginErrorSince reports whether a NOT_LOGGED_IN rejection landed after t.
// nil-safe (false when unwired).
func (h *presetWsHandler) loginErrorSince(t time.Time) bool {
	if h.loginErrorSinceFn == nil {
		return false
	}
	return h.loginErrorSinceFn(t)
}

// recallReachedAudio reports whether a recall anchored at pressAt has credibly
// reached audio: the box is playing this URL, or it pulled the slot's proxied
// stream in a way that counts. The login-error carve-out is the fix's core: a
// NOT_LOGGED_IN rejection for THIS recall makes a now-CLOSED slot pull an
// unreliable success signal, because the box's re-login source bounce opens the
// proxied stream and serves it right around minSustainedFetch (~3s) before
// dropping it WITHOUT audio and powering off (Portable 2026-07-23). In that
// window only a still-live pull or an actual playing state proves audio;
// otherwise the verify loop keeps retrying until the forced re-login lands.
func (h *presetWsHandler) recallReachedAudio(slot int, url string, pressAt time.Time) bool {
	ok, _ := h.recallReachedAudioSignal(slot, url, pressAt)
	return ok
}

// recallReachedAudioSignal is recallReachedAudio plus WHICH signal decided.
// The verify logs the signal on success because the two signals have very
// different reliability: "slot_fetch" is the proxy serving audio since the
// press (ground truth), while "now_playing" is the box's own report, which can
// resurrect a stale same-slot ContentItem after a failed self-activation
// (#419 Finding 1). A success line reading signal=now_playing next to an
// INVALID_SOURCE source is that false positive, made visible in bundles.
func (h *presetWsHandler) recallReachedAudioSignal(slot int, url string, pressAt time.Time) (bool, string) {
	if h.playingURL(url) && !h.staleSameSlotSuspect(slot, url, pressAt) {
		return true, "now_playing"
	}
	if !h.slotPulledSince(slot, pressAt) {
		return false, ""
	}
	// slot pulled since the press. Trust it unless a login error is in flight for
	// this recall AND the pull is no longer live (a closed short login-bounce).
	if h.loginErrorSince(pressAt) && !h.slotFetchLiveNow(slot) {
		return false, ""
	}
	return true, "slot_fetch"
}

// staleSameSlotSuspect flags the #419 Finding-1 false success: on a same-slot
// re-press the box's own FAILED self-activation (1036) resurrects the previous
// play's ContentItem - identical /stream/<slot> path, briefly play-ish state -
// so a bare now_playing match passed at the first tick and the verify exited
// silently while the box sat at INVALID_SOURCE in silence (captured on-site,
// ST30 2026-07-25: press slot 5 after a standby teardown of slot 5 -> 1036 ->
// no fetch, no retry, no sound). Tightly scoped so it cannot regress healthy
// recalls: it only fires for PROXIED streams (where an open proxy fetch is a
// physical precondition for audio, so demanding fetch evidence is exact, not
// heuristic) and only when the box actually refused THIS recall. A same-slot
// re-press over a still-playing stream keeps its open pull (slotFetchLiveNow)
// and stays a success; direct-URL plays and unrefused recalls keep the old
// now_playing trust unchanged.
func (h *presetWsHandler) staleSameSlotSuspect(slot int, url string, pressAt time.Time) bool {
	if streamPath(url) == "" {
		return false // direct URL: no proxy evidence exists, keep trusting now_playing
	}
	if !h.lastSourceRejectTime().After(pressAt) && !h.loginErrorSince(pressAt) {
		return false // the box never refused this recall; its report is trustworthy
	}
	return !h.slotPulledSince(slot, pressAt) && !h.slotFetchLiveNow(slot)
}

// superseded reports whether a newer hardware press (pressSeq moved past seq)
// or a newer play of any kind (the webui recall generation moved past gen)
// exists. Verify/re-push loops of an older recall stand down on it instead of
// fighting the newest recall for the transport. gen==0 means the generation
// was never captured (no webui wired); only the press sequence decides then.
func (h *presetWsHandler) superseded(seq, gen uint64) bool {
	if h.pressSeq.Load() != seq {
		return true
	}
	if gen != 0 && h.recallGenFn != nil && h.recallGenFn() != gen {
		return true
	}
	return false
}

// wake brings the box out of standby (no-op without a configured box host or
// when the box is already awake). Seam-aware for the verify tests.
//
// abort (nil = never) is consulted by the wake helper immediately before it
// would send the `sys power` toggle: the verify paths pass their stand-down
// predicate so a user power press that lands AFTER the caller's loop-top check
// but BEFORE the toggle still keeps the box off - the standby classifier
// stamps the latch within milliseconds of the flip, while the wake's own
// self-wake grace is 2.5s, so by toggle time the stamp is reliably visible.
func (h *presetWsHandler) wake(ctx context.Context, abort func() bool) error {
	if h.boxHost == "" {
		return nil
	}
	if h.wakeBox != nil {
		if abort != nil && abort() {
			return nil
		}
		return h.wakeBox(ctx, h.boxHost)
	}
	return boxcli.WakeAndWaitAbort(ctx, h.boxHost, 6*time.Second, h.logger, abort)
}

// triggerPairAsync fires one fire-and-forget pair cycle (9a9b0c7): the :8090
// pair POST hangs for seconds on several firmwares, so it must never sit
// between a button press and playback (#270). timeout bounds the cycle (press
// paths use a short budget; wake/reconnect paths can afford a longer one).
func (h *presetWsHandler) triggerPairAsync(timeout time.Duration) {
	if h.autoPair == nil {
		return
	}
	go func() {
		pairCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		h.autoPair.TriggerNow(pairCtx)
	}()
}

// recallStandDown reports whether an in-flight hardware recall must leave the
// box alone: a newer press/play owns the transport, the user powered the box
// off, or the user deliberately stopped playback since the press. Re-checked
// not only at each verify tick but immediately before every escalation (wake,
// sys-power nudge, re-push): those run seconds after the loop-top check, and a
// power press inside that gap used to be reversed by the very wake that was
// meant to recover the box's own give-up (#197).
func (h *presetWsHandler) recallStandDown(seq, gen uint64, pressAt time.Time) bool {
	return h.superseded(seq, gen) || h.poweredOffSince(pressAt) || h.userStoppedSince(pressAt)
}

// sysPowerToggle sends one `sys power` toggle over the TAP CLI (the stuck-
// INVALID_SOURCE nudge). Seam-aware for the verify tests.
func (h *presetWsHandler) sysPowerToggle(ctx context.Context) error {
	if h.boxHost == "" {
		return nil
	}
	if h.sysPowerFn != nil {
		return h.sysPowerFn(ctx, h.boxHost)
	}
	return boxcli.PowerOn(ctx, h.boxHost)
}

// playingURL reports whether the box's now_playing points at url in a play
// state. Seam-aware for the verify tests.
func (h *presetWsHandler) playingURL(url string) bool {
	if h.boxPlayingFn != nil {
		return h.boxPlayingFn(url)
	}
	return boxPlayingURL(h.boxHost, url)
}

// currentBoxSource returns the box's now_playing source attribute ("" on any
// error). Seam-aware for the verify tests.
func (h *presetWsHandler) currentBoxSource() string {
	if h.boxSourceFn != nil {
		return h.boxSourceFn()
	}
	return boxNowPlayingSource(h.boxHost)
}

// poweredOffSince reports whether the user powered the box off after t,
// preferring the absolute stamp over the rolling window (see standbyStopAfter).
// The recentlyPoweredOff fallback is unreachable in production (main wires both
// funcs from the same webui server); it exists so a partially-wired test - or a
// future caller that only has the rolling window - still gets the conservative
// answer instead of nil-func false.
func (h *presetWsHandler) poweredOffSince(t time.Time) bool {
	if h.standbyStopAfter != nil {
		return h.standbyStopAfter(t)
	}
	if h.recentlyPoweredOff != nil {
		return h.recentlyPoweredOff()
	}
	return false
}

// OnPresetsChanged forwards the box's own preset list to the webui (Option C).
func (h *presetWsHandler) OnPresetsChanged(_ context.Context, presets []boxws.BoxPreset) {
	if h.noteBoxPresets != nil {
		h.noteBoxPresets(presets)
	}
}

func (h *presetWsHandler) OnPresetSelected(ctx context.Context, slot int, location, title string) {
	// Press time is taken while still on the gabbo read loop, so every
	// stand-down check anchors to when the user actually pressed, not to when
	// STR got around to the recall.
	pressAt := time.Now()
	// Per-key webhook (beta): fire the configured "preset<slot>" webhook on a
	// hardware preset press (front panel or remote; app recalls take a different
	// path and never reach here). In replace mode, withhold the preset playback
	// so only the webhook runs (the user has cleared the STR preset for this
	// slot); in additional mode, the preset plays AND the webhook fires. The
	// replace decision is a config read and stays synchronous; the HTTP fire
	// runs in a goroutine because a slow webhook target stalled the read loop
	// for up to 8s per press, delaying every queued frame.
	if h.webhooks != nil && slot >= 1 && slot <= 6 {
		id := fmt.Sprintf("preset%d", slot)
		replace := h.webhooks.ButtonReplaceEnabled(id)
		go func() {
			if h.webhooks.FireButton(ctx, id) {
				h.logger.Info("preset webhook fired", "slot", slot, "replace", replace)
			}
		}()
		if replace {
			h.logger.Info("preset webhook replace mode: withholding preset playback", "slot", slot)
			return
		}
	}
	// The press sequence orders rapid presses exactly as the user made them.
	// Bumped only for presses that actually recall (i.e. after the replace-mode
	// return above): a webhook-only press starts no recall of its own, so it
	// must not read as "a newer press" to an unrelated in-flight verify.
	seq := h.pressSeq.Add(1)
	// The press is the user explicitly asking for playback: clear any stale
	// deliberate-stop latch BEFORE the recall so a preceding (or spontaneous,
	// #419) power-off cannot suppress it, and anchor the webui's standby-flip
	// discriminator so a source flip during this recall is not read as a
	// user power-off.
	if h.noteUserPlay != nil {
		h.noteUserPlay()
	}
	// Everything below talks to the box (SOAP, wake, pairing) and takes seconds
	// on a cold or wedged box - exactly the boxes whose presses were failing.
	// It used to run synchronously right here on the gabbo read loop, holding
	// up every queued frame (the teardown STOP_STATE, the press's own trailing
	// userActivityUpdate, 1036 errorUpdates, the user's next press) for up to
	// ~18s. All the teardown windows in boxws and the #419 power-off
	// discriminator in the webui measure at frame-PROCESSING time, so that
	// delay made them read the box's routine teardown as user intent: a
	// phantom user stop killed the verify, and the trailing key frame turned a
	// mid-recall standby flap into a "user power-off" that cleared the
	// transport - the ST20 that "switches itself off on every preset press"
	// and the ST30 whose remote presses never play (#252). The read loop must
	// keep draining; the recall runs beside it.
	go h.recallPreset(ctx, seq, pressAt, slot, location, title)
}

// recallPreset is the slow half of a hardware preset press: the queue-preset
// claim, the Spotify branch, URL selection, the UPnP push, wake, pairing and
// the background verify. Runs OFF the gabbo read loop; seq/pressAt come from
// the press event and drive supersession and the stand-down anchors.
func (h *presetWsHandler) recallPreset(ctx context.Context, seq uint64, pressAt time.Time, slot int, location, title string) {
	// A queue preset (a saved DLNA folder) is recalled by the webui's play-queue,
	// not the single-track UPnP play below. Let it claim the press; it returns
	// true (and starts the queue) only for a queue preset, so every other preset
	// type falls through to the existing behaviour unchanged.
	if h.recallSlot != nil && h.recallSlot(ctx, slot) {
		if h.noteRecentPreset != nil {
			if p, ok := h.store.Get(slot); ok {
				h.noteRecentPreset(p)
			}
		}
		return
	}
	// The URL stays the proxy URL (location = http://127.0.0.1:8888/stream/N)
	// so the stream proxy handles the reconnect on token expiry. Name + icon
	// come from the stick preset store — the Bose ContentItem metadata has no
	// art entry, so we must actively pack the album art URL into the DIDL-Lite
	// metadata via our PlayURL call, otherwise the display (ST20/30) shows no
	// logo.
	// A NATIVE radio preset needs no help: the box resolves the station through
	// the agent's own BMX adapter and fetches the stream itself, which is the
	// whole point of storing presets on the native source (no 1036, no verify,
	// no re-push). Measured 2026-08-02: 200 ms after the press the box had
	// resolved the station and the stream proxy was serving - and STR then
	// overrode it 0.5 s later with its legacy UPnP recall, because the location
	// is not one of STR's own stream URLs. Stand back instead.
	if isNativeRadioLocation(location) {
		h.logger.Info("hardware preset: native radio preset, the box activates it itself",
			"slot", slot, "location", location)
		if p, ok := h.store.Get(slot); ok {
			if h.noteRecentPreset != nil {
				h.noteRecentPreset(p)
			}
			// Record it as the last play even though STR does not drive this
			// one. Standing back must not mean forgetting what is playing: the
			// power-on resume and the box-side-drop recovery both read this
			// record, and leaving it on the PREVIOUS station makes them bring
			// that older station back. A user reported exactly that, a rare
			// jump back to the station played before (Portable, v0.9.30).
			if h.noteLastPlay != nil {
				h.noteLastPlay(boxPresetURL(p), p.Name, p.Art,
					upnp.MimeForCodecOrURL(p.Codec, p.StreamURL))
			}
		}
		return
	}
	url := location
	name := title
	icon := ""
	// mime is the DIDL protocolInfo label for the recall. Presets saved from an
	// AAC/HE-AAC station carry their codec; labelling them with the audio/mpeg
	// default made the box decode them as MPEG and play silence (#252).
	mime := ""
	if p, ok := h.store.Get(slot); ok {
		// Recently-played (#135): record the pressed preset (radio or Spotify)
		// from the authoritative store entry, before the source-specific recall.
		if h.noteRecentPreset != nil {
			h.noteRecentPreset(p)
		}
		// Spotify presets do not have a playable HTTP StreamURL. They are
		// recalled by telling go-librespot to play the saved URI and then
		// pointing the box's UPnP renderer at our live /spotify/stream.
		if p.Type == "spotify" && p.URI != "" {
			h.playSpotifyPreset(ctx, seq, pressAt, slot, p)
			return
		}
		if p.Name != "" {
			name = p.Name
		}
		icon = p.Art
		// A preset stored before the codec was recorded has none, and an AAC
		// station then got the audio/mpeg default and played silence (#252). Read
		// the codec off the station URL in that case.
		mime = upnp.MimeForCodecOrURL(p.Codec, p.StreamURL)
		// Fallback: NetManager occasionally fires nowSelectionUpdated
		// with an empty location — observed when Bose's preset cache
		// was populated while BoseApp had not yet fully loaded the
		// NetManager DB at boot. Our own store always has the
		// authoritative URL, so use it whenever the event field is
		// empty. Symmetric with the software-preset code path.
		if url == "" && p.StreamURL != "" {
			url = p.StreamURL
			h.logger.Info("hardware preset location empty, falling back to store URL", "slot", slot)
		}
		// The box's ContentItem location can be a STALE Bose-cloud reference on a
		// preset that predates STR or was never re-synced: e.g. a pre-shutdown
		// TuneIn entry with location="/v1/playback/station/..." source=TUNEIN.
		// Playing that fails with UPnP 402 "No URI supplied" and verifyPlayURL then
		// retries it in a storm, so the button looks dead and the box churns
		// (#45/#105, Brecht 2026-06-20). Our store holds the authoritative STR
		// proxy URL, so prefer it whenever the box handed us something that is not
		// one of STR's own stream URLs.
		if p.StreamURL != "" && url != p.StreamURL && !isSTRStreamURL(url) {
			h.logger.Info("hardware preset: box location is not an STR stream, using store URL",
				"slot", slot, "boxLocation", url, "storeURL", p.StreamURL)
			url = p.StreamURL
		}
	}
	if url == "" {
		h.logger.Info("hardware preset pressed, no mapping", "slot", slot)
		return
	}
	// A stale cloud preset with no STR replacement (a TuneIn/relative location and
	// no store StreamURL) is not playable: the box answers SetAVTransportURI with
	// UPnP 402, and verifyPlayURL below would then hammer it in a retry storm.
	// Stand down with a clear, actionable log instead (re-save fixes it).
	if !isPlayableURL(url) {
		h.logger.Warn("hardware preset is a stale cloud entry (e.g. old TuneIn), not playable; re-save it in the app",
			"slot", slot, "location", url)
		return
	}

	// This is a radio (non-Spotify) recall. Tell the Spotify manager the user
	// switched away so its #14 auto-attach does not yank the box back to a
	// still-advancing go-librespot a second later (reported: Spotify->radio
	// played radio ~1s then jumped back to the Spotify preset).
	if h.spotify != nil {
		h.spotify.SwitchedAway(ctx)
	}

	// Record this hardware recall as the last play BEFORE the push: the returned
	// recall generation is what stands every OLDER verify loop down, and bumping
	// it first means a stale loop cannot clobber this recall while the SetURI is
	// still in flight. The auto-re-push and the power-button wake-resume read
	// the same record to know what to bring back (the webui only tracks its own
	// soft plays otherwise); the mime rides along so re-pushes keep the AAC
	// label too (#252).
	var gen uint64
	if h.noteLastPlay != nil {
		if h.pressSeq.Load() != seq {
			// A newer press exists before this recall even pushed: never let the
			// older goroutine overwrite the newer press's lastPlay or URL.
			h.logger.Info("hardware recall superseded before push, standing down", "slot", slot)
			return
		}
		gen = h.noteLastPlay(url, name, icon, mime)
	}
	// Push the stream FIRST, before the wake and pairing, mirroring the Spotify
	// path. On a hardware/remote press the box is already awake (it just emitted
	// the gabbo frame) and briefly shows its own "Service Unavailable" flash from
	// failing to natively self-activate the UPNP preset; getting STR's SetURI in
	// as early as possible shortens that flash (#383). Pairing is not a
	// precondition for the SetURI, and a cold-standby race (UPnP 1036) is caught
	// by the background verifyPlayURL retry below.
	playCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var playErr error
	if mime != "" {
		playErr = h.renderer.PlayURLMime(playCtx, url, name, icon, mime)
	} else {
		playErr = h.renderer.PlayURL(playCtx, url, name, icon)
	}
	if playErr != nil {
		h.logger.Warn("upnp play (initial) failed, will verify+retry", "slot", slot, "err", playErr)
	}

	// Wake from standby (fast on a hardware press, the box is already awake) AFTER
	// the display push, so the SetURI is not delayed behind it. No abort: the
	// press itself is the user's most recent intent.
	wakeCtx, wcancel := context.WithTimeout(ctx, 8*time.Second)
	if err := h.wake(wakeCtx, nil); err != nil {
		h.logger.Warn("could not bring box out of STANDBY", "err", err)
	}
	wcancel()
	// Fire-and-forget, mirroring the app-recall path: pairing is not a
	// precondition for the UPnP push above.
	h.triggerPairAsync(6 * time.Second)
	// Verify+retry in the background: the first hardware press after a cold
	// boot can race the box/agent bringup so nothing plays until a second
	// press. This re-issues until the box actually plays. Affects radio too.
	go h.verifyPlayURL(seq, gen, pressAt, slot, url, name, icon, mime)
	h.logger.Info("hardware preset mapped to upnp", "slot", slot, "name", name, "mime", mime)
}

// OnRemoteSkip handles the SoundTouch remote's next/prev track keys. The box
// cannot skip a UPnP source itself (it emits QPLAY_SKIP_*_FAILED), so STR skips
// on its behalf: go-librespot during Spotify, or the STR play queue during
// folder/library playback. It routes through the same source-aware skip as the
// phone remote (webui.TransportSkip). Before this the hardware keys only skipped
// Spotify, so a folder skip stalled until the current track ended naturally,
// which the box surfaced as "Action Unavailable" for the remaining track time
// (#300). A no-op on a non-skippable source (radio, aux) just does nothing.
func (h *presetWsHandler) OnRemoteSkip(ctx context.Context, forward bool) {
	if h.onRemoteSkip == nil {
		return
	}
	// Off the gabbo read loop: a skip against a slow/wedged transport held the
	// loop for up to 8s per press, delaying every queued frame and skewing the
	// processing-time classification windows (#252). The webui skip serializes
	// box commands internally, so concurrent presses do not interleave SOAP.
	go func() {
		sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		src, err := h.onRemoteSkip(sctx, forward)
		if err != nil {
			h.logger.Warn("remote skip failed", "forward", forward, "source", src, "err", err)
			return
		}
		h.logger.Info("remote skip", "forward", forward, "source", src)
	}()
}

// OnUserStop is fired when the box reports a deliberate playback stop over
// gabbo. It tells the webui's auto-re-push to stand down so a wanted stop holds
// (v0.7.0: a single stop did not stick because the resume restarted it), and
// records the stop time so the hardware recall verifies can stand down too.
func (h *presetWsHandler) OnUserStop(_ context.Context) {
	h.lastUserStopMu.Lock()
	h.lastUserStop = time.Now()
	h.lastUserStopMu.Unlock()
	if h.onUserStop != nil {
		h.onUserStop()
	}
}

// userStoppedSince reports whether the box reported a deliberate stop (gabbo
// STOP_STATE -> OnUserStop) after start.
func (h *presetWsHandler) userStoppedSince(start time.Time) bool {
	h.lastUserStopMu.Lock()
	defer h.lastUserStopMu.Unlock()
	return userStopAbortsVerify(start, h.lastUserStop)
}

// OnSourceRejected records that the box rejected STR's UPnP source with a 1036
// UpnpRcvdContentItemInWrongState (boxws optional hook). The routine preset->
// preset switch race: the box is still tearing down the previous source when
// STR's SetURI lands, refuses it, and can hang attached-but-buffering on the
// Spotify stream without ever playing. verifySpotifyPlaying reads the timestamp
// to re-point in that case instead of trusting the stuck state.
func (h *presetWsHandler) OnSourceRejected(_ context.Context) {
	h.sourceRejectMu.Lock()
	h.lastSourceReject = time.Now()
	h.sourceRejectMu.Unlock()
	// A 1036 rejection precedes the forced re-login, and the re-onboarding
	// that follows is when the firmware wipes its hardware-key preset
	// registrations (field bundles: "missing=5/6" healed right after every
	// "forced re-login sent"). Schedule the heal proactively; own short
	// budget, so a routine ask cannot starve it.
	requestPresetKeyResyncUrgent(h.logger, "1036")
}

// lastSourceRejectTime returns when the box last rejected STR's source (zero if
// never), for the verify loop's re-point-on-wrong-state decision.
func (h *presetWsHandler) lastSourceRejectTime() time.Time {
	h.sourceRejectMu.Lock()
	defer h.sourceRejectMu.Unlock()
	return h.lastSourceReject
}

// OnThumbActivity fires the user-configured webhook when the box reports a lone
// userActivityUpdate (the best available signal for a remote thumbs key on this
// firmware; up and down are indistinguishable, so it is a single toggle-style
// trigger). The detection + debounce live in boxws; here we just fire.
func (h *presetWsHandler) OnThumbActivity(ctx context.Context) {
	// A lone userActivityUpdate is ALSO the only trace a DEAD hardware preset
	// key leaves: the box's key layer can lose its preset registrations while
	// /presets still lists every slot (#342, display shows "Action
	// unavailable"), and then a press emits no selection frame at all - which
	// is exactly this trigger. The missing-only reconcile never heals that
	// state, so ask it for one forced full re-sync (rate-limited). If the
	// press really was a thumbs key, the extra AddPreset round is harmless.
	//
	// Standby gate: phantom userActivityUpdate frames are field-confirmed
	// (2026-07-25) and this was the one asker with no gate at all, so a 3am
	// phantom frame could schedule a full key sweep. But a REAL press on a
	// DEAD key layer can look identical when the de-registered keys fail to
	// wake the box (#342: lone frame, no selection, source still STANDBY), so
	// repetition is the discriminator: a phantom is a one-off, a human
	// retries. The second frame within a minute schedules the heal and is
	// allowed one gate-exempt write - the user is standing at the box. Off
	// the WS hot path.
	go func() {
		src, _, status := h.nowPlayingSummary()
		playing := status == "PLAY_STATE" || status == "BUFFERING_STATE"
		if src == "STANDBY" && !playing {
			now := time.Now().Unix()
			last := standbyThumbLast.Swap(now)
			if last != 0 && now-last <= 60 {
				h.logger.Info("repeated thumb frames from a box reading STANDBY: treating as real presses on a dead key layer, scheduling the re-sync (#342)")
				presetResyncStandbyOK.Store(true)
				requestPresetKeyResync(h.logger, "thumb-repeat")
				return
			}
			h.logger.Info("thumb frame while the box idles in STANDBY (phantom), not scheduling a key re-sync")
			return
		}
		requestPresetKeyResync(h.logger, "thumb")
	}()
	if h.webhooks == nil {
		return
	}
	h.webhooks.FireThumb(ctx)
}

// OnPowerKey fires the configured "power" webhook on a power-off (standby)
// event. Additional-only: STR cannot suppress the firmware power toggle. boxws
// only calls this on the standby transition, which STR never causes itself, so
// the webhook does not false-fire on STR's own wake-for-recall.
func (h *presetWsHandler) OnPowerKey(ctx context.Context) {
	if h.webhooks != nil {
		// Async: the webhook HTTP request must not stall the gabbo read loop.
		go func() {
			if h.webhooks.FireButton(ctx, "power") {
				h.logger.Info("power webhook fired")
			}
		}()
	}
}

// OnPowerWake resumes the last station when the speaker is switched on, the
// Bose-style power-on preset (default on, opt-out per box). boxws fires this on a
// power-on wake: a powerStateUpdated on firmware that sends one, or the
// DO_NOT_RESUME selection restore on firmware that does not. The actual resume
// (webui.ResumeLastPlay) is gated by the per-box setting and a zone-membership
// guard, so a stereo-pair self-wake (which looks identical on the wire) never
// makes the box start playing on its own.
func (h *presetWsHandler) OnPowerWake(_ context.Context) {
	// The firmware loses hardware-key preset registrations across power
	// cycles (field bundles 2026-07-22: the reconcile heals "missing=5/6"
	// slots again and again; users see "preset not assigned" until the next
	// 5-minute reconcile tick, and "after power-on only the first program
	// plays"). Ask for a full re-sync right at the wake so the keys work
	// within seconds instead of minutes. Rate-limited internally.
	requestPresetKeyResync(h.logger, "power-wake")
	// The fake marge login decays across the same power cycles: re-check the
	// pairing right at the wake. On a healthy box this is one /info read; on
	// a login-suspect box (a recent 1036 NOT_LOGGED_IN) it re-asserts the
	// account before the user's first press instead of after its failure.
	h.triggerPairAsync(10 * time.Second)
	if h.onPowerWake != nil {
		h.logger.Info("power-on detected, attempting last-station resume")
		h.onPowerWake()
	}
}

// OnConnected fires after the gabbo WebSocket (re)connects (boxws optional
// hook). It recovers the lost-first-press case (#183): when the box wakes from a
// deep/overnight standby it emits the preset/now-selection frame before STR has
// reconnected, so OnPresetSelected never runs and the display shows "service
// unavailable" until a second press. On reconnect STR checks the box and, if it
// is awake but its restored STR selection is not playing, re-pushes the last
// stream through the guarded resume (opt-out, zone, user-stop). A routine idle
// reconnect (box in standby) or a box already playing is a no-op.
func (h *presetWsHandler) OnConnected(_ context.Context) {
	// A gabbo reconnect usually means the box just came back from a standby
	// or reboot - exactly when the firmware tends to have dropped its
	// hardware-key preset registrations (see OnPowerWake). Ask for a full
	// re-sync so the keys are registered again within seconds.
	//
	// UNLESS the box demonstrably idles in STANDBY: the firmware recycles the
	// idle gabbo socket every ~12 minutes, and the unconditional ask here wrote
	// two AddPresets into BoseApp on every recycle, all night - which reset the
	// firmware's deep-standby countdown forever (#119 bundles 2026-07-26:
	// /proc/uptime spanning days; boxes never low-powered since v0.9.17 shipped
	// this path, while v0.9.16 with the same 5-minute READ-ONLY heartbeats deep
	// slept fine - reads are safe, the blind writes are the difference). Every
	// real dead-key moment keeps its own forced trigger: OnPowerWake (power
	// press), OnThumbActivity (dead-press evidence), the urgent 1036/re-pair
	// asks, and the boot-time force-all. Probe errors fall back to the old
	// unconditional ask, so unknown states never lose the heal.
	go h.resyncUnlessIdleStandby()
	// Re-check the fake login too (see OnPowerWake): the reconnect moments
	// are when the MargeHSM state decays on fresh-install boxes.
	h.triggerPairAsync(10 * time.Second)
	// Log-only probe for the deep-standby missed-first-press race (#435): a
	// gabbo reconnect follows a wake/reboot, and if the box activated a preset
	// while our WS was still down, it now sits on that preset's ContentItem but
	// never got the SetURI, so it is not playing. Confirm the signature in field
	// logs before wiring a post-wake reconciliation push. Best-effort, off the
	// hot path.
	go h.logStandbyRaceSignature()
	if h.onBoxReconnect != nil {
		h.onBoxReconnect()
	}
}

// OnStandbyExit fires when the box's source leaves STANDBY for anything else
// (boxws optional hook). It re-registers the hardware keys the moment the
// user is back at the box: the wake write cannot touch the deep-standby
// countdown (the box is awake), and it is the guaranteed heal for firmware
// that silently de-registered the key layer during standby (#487, where dead
// presses emit no frame and no other trigger ever fires).
func (h *presetWsHandler) OnStandbyExit(_ context.Context) {
	requestPresetKeyResync(h.logger, "standby-exit")
	// The user just switched the box on. If the firmware had dropped a stream
	// while the box was off, STR replays it NOW - on the user's own action -
	// instead of powering the speaker on by itself (#487).
	if h.onStandbyExit != nil {
		go h.onStandbyExit()
	}
}

// resyncUnlessIdleStandby is OnConnected's re-sync decision, off the WS hot
// path. One now_playing read: a box sitting in STANDBY and not playing gets no
// forced AddPreset sweep (the deep-standby fix, #119); anything else - another
// source, an active play state, or a failed probe - still asks. Since the
// execution-site standby gate exists, the ask is re-checked (and possibly
// deferred to the wake) right before the write, so a failed probe here no
// longer guarantees a write - it only forwards the decision.
func (h *presetWsHandler) resyncUnlessIdleStandby() {
	if h.boxHost != "" {
		// Let a mid-transition source settle so a wake in progress is not
		// misread as idle standby (the wake also fires OnPowerWake's own ask).
		time.Sleep(1500 * time.Millisecond)
		src, _, status := h.nowPlayingSummary()
		playing := status == "PLAY_STATE" || status == "BUFFERING_STATE"
		if src == "STANDBY" && !playing {
			h.logger.Info("preset self-heal: reconnect while the box idles in STANDBY, skipping the forced key re-sync so deep standby can engage (#119)")
			return
		}
	}
	requestPresetKeyResync(h.logger, "reconnect")
}

// logStandbyRaceSignature records (log-only) whether, shortly after a gabbo
// reconnect, the box is parked on one of STR's own preset streams without
// playing it - the tell-tale of a deep-standby first press that arrived while
// our WebSocket was still reconnecting (#435). It changes no behaviour; it
// exists so a field diagnostic proves the race before a reconciliation push is
// added. One now_playing read after a short settle.
func (h *presetWsHandler) logStandbyRaceSignature() {
	if h.boxHost == "" {
		return
	}
	// Let the reconnect and the box's own (failing) self-activation settle.
	time.Sleep(4 * time.Second)
	cl := &http.Client{Timeout: 4 * time.Second}
	resp, err := cl.Get("http://" + h.boxHost + ":8090/now_playing")
	if err != nil {
		return
	}
	doc, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	_ = resp.Body.Close()
	s := string(doc)
	source := firstAttr(s, "source")
	location := firstAttr(s, "location")
	playing := strings.Contains(s, "PLAY_STATE") || strings.Contains(s, "BUFFERING_STATE")
	if isSTRStreamURL(location) && !playing {
		h.logger.Warn("standby-race probe: box parked on an STR preset stream but NOT playing after a gabbo reconnect; candidate missed first-press (#435)",
			"location", location, "source", source)
		return
	}
	h.logger.Info("standby-race probe: box state after gabbo reconnect",
		"source", source, "playing", playing, "onSTRPreset", isSTRStreamURL(location))
}

// OnEnterStandby fires when the box's UPnP (STR) source drops to STANDBY on a
// power-off. It clears the transport so ST20 (scm) firmware that oscillates
// UPNP<->STANDBY does not switch the speaker back on (#197). boxws calls this via
// an optional interface, so only handlers that wire it (this one) react.
func (h *presetWsHandler) OnEnterStandby(_ context.Context) {
	if h.onEnterStandby != nil {
		h.onEnterStandby()
	}
}

// OnSourceAux fires the configured "aux" webhook when the box switches to the
// AUX input. Additional-only.
func (h *presetWsHandler) OnSourceAux(ctx context.Context) {
	if h.webhooks != nil {
		// Async: the webhook HTTP request must not stall the gabbo read loop.
		go func() {
			if h.webhooks.FireButton(ctx, "aux") {
				h.logger.Info("aux webhook fired")
			}
		}()
	}
}

// OnZoneChanged records the box's live multiroom/stereo-pair membership. Log
// only on purpose: the box may have formed this zone itself (AfterTouch / Bose
// app), and STR must NOT feed a box-native group into the reconcile store, or
// PeriodicZoneReconcile would try to re-form it via /setZone and fight the
// firmware's own pairing. The desktop multiroom tab already reads the live zone
// via /getZone polling and can dissolve it; this typed log makes box-formed
// groups visible in a diagnostic bundle instead of an "unrecognized frame".
func (h *presetWsHandler) OnZoneChanged(_ context.Context, z boxws.ZoneState) {
	if z.Master == "" {
		h.logger.Info("zone changed: dissolved")
		return
	}
	h.logger.Info("zone changed", "master", z.Master, "senderIsMaster", z.SenderIsMaster, "members", len(z.Members))
}

// OnGroupChanged reacts to the box's stereo pair changing. The one action taken
// is on a TEARDOWN: drop STR's own pair record for this speaker.
//
// Every speaker in a pair keeps a copy of the pair document in STR's cloud
// stand-in, and until now only the speaker the teardown was issued THROUGH ever
// cleared it. Undo the pair in the Bose app and the other speaker kept its copy
// indefinitely; the same happened when a dissolve reached only one member. A
// speaker that still believes it is half of a pair is not offered for pairing
// again, which is how a user with three SoundTouch 10s ended up unable to pair
// at all (field, 2026-08-04).
//
// The frame is emitted by each member's own firmware about itself, so acting on
// it needs no reachability between the speakers - which is exactly what is
// missing between series-I boxes.
//
// Only the teardown is acted on. A pair being FORMED must not write a record
// here: the master installs one canonical document on both speakers, and
// letting the right-hand speaker write its own view is the divergence that
// desynced pairs in the first place.
func (h *presetWsHandler) OnGroupChanged(_ context.Context, g boxws.GroupState) {
	if g.Paired() {
		h.logger.Info("stereo pair changed on the box", "id", g.ID, "master", g.Master, "members", len(g.Members))
		return
	}
	if h.margeGroupClear == nil {
		h.logger.Info("stereo pair dissolved on the box (no marge record to clear)")
		return
	}
	h.margeGroupClear("the box reported its stereo pair dissolved")
}

// spotifyStreamURL is the agent-local URL the box's UPnP renderer fetches for
// ad-hoc Spotify audio (see boxurl.SpotifyDefault for the .ogg-suffix rationale).
var spotifyStreamURL = boxurl.SpotifyDefault()

// playSpotifyPreset recalls a Spotify preset: wake + pair the box, tell
// go-librespot to play the saved URI (autonomous, no app), then point the
// box at the live /spotify/stream so it plays the audio over UPnP.
func (h *presetWsHandler) playSpotifyPreset(ctx context.Context, seq uint64, pressAt time.Time, slot int, p presets.Preset) {
	// Log the inputs up front so a remote "recall does nothing" report (e.g.
	// ST20 #45) shows immediately which precondition failed: no Spotify
	// manager, no stored account/URI on the preset, or go-librespot not ready.
	h.logger.Info("spotify preset recall start", "slot", slot,
		"hasURI", p.URI != "", "account", p.Account, "type", p.Type, "spotifyMgr", h.spotify != nil)
	if h.spotify == nil {
		h.logger.Warn("spotify preset recall: no Spotify manager on this box", "slot", slot)
		return
	}
	if p.URI == "" {
		h.logger.Warn("spotify preset recall: preset has no Spotify URI, cannot autoplay", "slot", slot, "name", p.Name)
		return
	}
	// No live session AND no persisted Spotify login means go-librespot has no way
	// to start playback on its own, so a hardware press would do nothing but thrash
	// the box. Skip with a clear log instead of the silent retry loop (#45 Pierre).
	// Gate on CanRecall (live session OR persisted credential), NOT a persisted
	// credential alone: a box with a live-but-never-persisted zeroconf session
	// plays Spotify fine yet LoggedIn() is false, and gating on the credential
	// alone wrongly skipped its recall (Patrick, ST10, 2026-06-24). Mirror of the
	// soft/app path in internal/webui (the two recall paths must stay in sync).
	if !h.spotify.CanRecall(ctx) {
		h.logger.Warn("spotify preset recall: speaker not logged into Spotify and no live session; log it into Spotify once first", "slot", slot, "name", p.Name)
		return
	}
	if !h.spotify.Ready() {
		// Cold start: pressed right after boot, before go-librespot finished
		// authenticating. Wait briefly instead of doing nothing (which left the
		// box on the idle "select a preset" screen and forced a second press
		// once go-librespot was ready). Bounded so a genuinely unconfigured
		// manager does not hang the handler forever.
		h.logger.Info("spotify preset pressed before manager ready, waiting", "slot", slot)
		for i := 0; i < 24 && !h.spotify.Ready(); i++ {
			time.Sleep(500 * time.Millisecond)
		}
		if !h.spotify.Ready() {
			h.logger.Warn("spotify preset pressed but manager not ready after wait", "slot", slot)
			return
		}
	}
	// A free/open Spotify account cannot autonomously play a saved context, so a
	// hardware-button recall would silently produce no audio. Skip it and log the
	// reason rather than thrashing the box (#45). The desktop app surfaces the
	// "needs Premium" note; the hardware press has no UI to show it.
	if h.spotify.PremiumRequired() {
		h.logger.Warn("spotify preset recall: account is free/open, recall needs Premium; skipping", "slot", slot, "name", p.Name)
		return
	}

	// Mark a recall BEFORE the box attaches (PlayURLMime below / the box's own
	// self-activation) so ServeOgg does not resume the old mid-position track;
	// Play drives the new shuffled track from its start.
	h.spotify.SetRecalling()
	playCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	// Show the preset on the box display IMMEDIATELY: point the box at this
	// slot's stream first so now_playing shows the name and a buffering state
	// right away. Without this the box flashes its own "service unavailable /
	// select a preset" while we load, and the user, seeing no feedback, presses
	// another preset and causes chaos. The box buffers until go-librespot
	// produces audio just below. Uses the per-slot URL the box already
	// self-activated, so this re-confirms it rather than switching URLs.
	slotURL := boxurl.SpotifySlot(slot)
	if err := h.renderer.PlayURLMime(playCtx, slotURL, p.Name, p.Art, "audio/ogg"); err != nil {
		h.logger.Warn("spotify upnp play (display) failed, will verify+retry", "slot", slot, "err", err)
	}
	// Record this hardware recall as the last play so the power-button
	// wake-resume can bring the Spotify stream back. The returned recall
	// generation feeds the verify's supersession check below.
	var gen uint64
	if h.noteLastPlay != nil {
		if h.pressSeq.Load() != seq {
			h.logger.Info("spotify recall superseded before push, standing down", "slot", slot)
			return
		}
		gen = h.noteLastPlay(slotURL, p.Name, p.Art, "audio/ogg")
	}
	// Wake from standby + ensure pairing (the box is awake on a hardware press,
	// so these return fast); kept AFTER the display push so the buffering state
	// shows without waiting on them.
	if h.boxHost != "" {
		wakeCtx, c := context.WithTimeout(ctx, 8*time.Second)
		if err := boxcli.WakeAndWait(wakeCtx, h.boxHost, 6*time.Second, h.logger); err != nil {
			h.logger.Warn("could not bring box out of STANDBY", "err", err)
		}
		c()
	}
	h.triggerPairAsync(6 * time.Second)
	// Load the playlist (audio): a default preset resumes where the user left off
	// (shuffle off, in-order); a shuffle preset starts on a fresh random track.
	if err := h.spotify.PlayAccount(playCtx, p.URI, p.Account, spotify.PlayOptions{Shuffle: p.Shuffle}); err != nil {
		h.logger.Warn("spotify play (initial) failed, will verify+retry", "slot", slot, "err", err)
	}
	// Verify+retry in the background: the first press after a cold boot races
	// go-librespot's auth, so the box gets no audio and the user had to press
	// twice. This retries until the box actually plays, with no latency on the
	// happy path (the initial attempt above already played).
	go h.verifySpotifyPlaying(seq, gen, pressAt, slot, p)
	h.logger.Info("spotify preset recalled", "slot", slot, "name", p.Name, "account", p.Account)
}

// userAdjustedSince reports whether the box saw a physical key press (any
// userActivityUpdate) after anchor plus a small epsilon. The epsilon exists
// because the anchoring press ITSELF emits a userActivityUpdate; anything
// later means the user interacted with the box again. nil-safe: without the
// boxws wiring (tests, exotic firmware) it reads as "no interaction".
func (h *presetWsHandler) userAdjustedSince(anchor time.Time) bool {
	if h.lastUserActivity == nil {
		return false
	}
	return h.lastUserActivity().After(anchor.Add(2 * time.Second))
}

// readBoxVolume returns the box's current target volume (0 on any error / no box
// host). Best-effort, used to remember the user's level across a recall.
func (h *presetWsHandler) readBoxVolume() int {
	if h.boxHost == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if v, err := boxapi.New(h.boxHost).GetVolume(ctx); err == nil {
		return v.Target
	}
	return 0
}

// restorePreRecallVolume no longer changes the volume. It observes and records
// what the box did across a recall recovery, and nothing more.
//
// It used to re-apply the level the box had before the recall, because a
// 1036-standby recovery brings some speakers back at their own default (~30)
// and a recovered preset could blast the room (v0.9.18, reported on a
// Portable). To avoid fighting a level the user had just chosen, it stood down
// whenever a userActivityUpdate arrived after the press.
//
// That guard cannot be trusted. On 2026-08-05 a field bundle showed the box
// emitting userActivityUpdate as a consequence of STR's OWN write into it, and
// phantom frames with nobody near the speaker were already on file from
// 2026-07-25 (see project_autoresume_stop_discriminator). The signal that was
// supposed to distinguish "the user chose this level" from "the box defaulted
// to it" does neither reliably, and the same bundle class already caught the
// restore pushing a level DOWN over a user's correction ("from=34 to=10",
// twice).
//
// A control that cannot tell whose intent it is enforcing should not enforce
// one. The speaker's own volume behaviour now stands, which is also what a
// Bose speaker did before STR existed. The observation stays because it is the
// only way to see from a diagnostic bundle whether loud wake-ups return.
func (h *presetWsHandler) restorePreRecallVolume(pressAt time.Time, preVol int) {
	if preVol <= 0 || h.boxHost == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cur, err := boxapi.New(h.boxHost).GetVolume(ctx)
	if err != nil || cur.Target == preVol {
		return
	}
	h.logger.Info("volume changed across a recall recovery; leaving the speaker's own level alone",
		"before", preVol, "after", cur.Target, "userActivitySincePress", h.userAdjustedSince(pressAt))
}

// verifyPlayURL confirms the box started playing a UPnP (radio) recall and
// re-issues it a few times if not, fixing the "first hardware press after
// reboot does nothing" race for radio presets too. mime is the DIDL label of
// the initial play ("" = audio/mpeg default); the retries must re-issue with
// the SAME label or an AAC station recovered here would fall back to silence
// (#252).
func (h *presetWsHandler) verifyPlayURL(seq, gen uint64, pressAt time.Time, slot int, url, name, icon, mime string) {
	// All stand-down checks anchor to pressAt, the moment the user pressed the
	// key (stamped on the gabbo read loop). The old anchor - verify-start,
	// i.e. after the initial SOAP push and wake - meant a user stop or a 1036
	// rejection that landed DURING a slow push was invisible to the loop.
	// Fast recovery for the wrong-state race, before the 5 s loop below: on a
	// Wave the box answers every hardware press with 1036
	// UpnpRcvdContentItemInWrongState about 0.8 s in, because it first tries to
	// activate its OWN stored ContentItem (which no longer works without the
	// Bose cloud) and STR's push lands while that teardown is still running.
	// Waiting a full verify tick to react leaves the user in silence for five
	// seconds; re-pushing as soon as the rejection is visible is the automatic
	// version of the second press users learned to do by hand.
	// Capture the user's volume before any recovery runs: the box FORGETS its
	// volume across a 1036-standby and comes back at its own default (~30) after
	// STR wakes it, so a recovered preset would otherwise blast the room (field:
	// scm/taigan remote-preset -> reattach + volume reset). Restored at the
	// success below, only if it actually changed.
	preVol := h.readBoxVolume()
	h.rePushAfterSourceReject(seq, gen, pressAt, slot, url, name, icon, mime)
	// Up to 5 attempts (~25s): a box waking from a deep/overnight standby can
	// take longer than the old 3-attempt (~15s) window to finish bringing its
	// network and playback subsystem back up before it accepts the stream (#183).
	nudged := false
	for attempt := 1; attempt <= 5; attempt++ {
		time.Sleep(5 * time.Second)
		// A newer press or app recall owns the transport now: this loop's URL is
		// stale, and re-pushing it would flip the box back to the previous
		// station ("pressed 2, got 1"). The soft path has had this guard all
		// along (verifyRecall's recallGen); the hardware loop lacked it.
		if h.superseded(seq, gen) {
			h.logger.Info("hardware recall superseded by a newer play, standing down", "slot", slot)
			return
		}
		// Success means the box is playing THIS recall, not merely "some play
		// state". A bare play-state check silently passed the exact failure the
		// verify exists for: on a Wave every hardware press is rejected with 1036
		// UpnpRcvdContentItemInWrongState, the box then flips UPNP -> INVALID_SOURCE
		// -> UPNP while still reporting a stale PLAY/BUFFERING state from the
		// previous stream, and it never fetches the new URL. boxIsPlaying read that
		// as success at the first tick, so the recall returned silently: the display
		// showed the station, no audio ever came, no retry ran and no wedge strike
		// was recorded. The Spotify verify already keys off the now_playing location
		// for this very reason (see boxPlayingSpotify); radio now does too.
		// The box pulling THIS SLOT's proxied stream since the press is proof it
		// is playing what we pushed, and it is checked FIRST because now_playing
		// lags: a Portable kept naming the PREVIOUS preset for seconds after it
		// had already opened the new stream, so a location check alone declared a
		// healthy recall dead and the "repair" tore the working stream down.
		if ok, signal := h.recallReachedAudioSignal(slot, url, pressAt); ok {
			// The verify used to exit success SILENTLY, which hid the #419
			// Finding-1 false positive (a stale same-slot now_playing passing
			// the check) from every bundle. Log the deciding signal plus the
			// box's own view: signal=now_playing with source=INVALID_SOURCE or
			// an empty itemName IS that false success, now visible.
			src, item, status := h.nowPlayingSummary()
			h.logger.Info("hardware recall: verify success", "slot", slot,
				"attempt", attempt, "signal", signal,
				"source", src, "itemName", item, "playStatus", status)
			h.restorePreRecallVolume(pressAt, preVol)
			if h.noteBoxHealthy != nil {
				h.noteBoxHealthy()
			}
			return
		}
		// The user powered the box off during the recall: the box reads "not
		// playing" only because it is in standby. Re-pushing here re-arms the
		// transport the power-off cleared, which on scm ST20 firmware bounces the
		// speaker back on (#197, the "start via preset then power off" trigger). A
		// genuine deep-standby wake (#183) carries no recent power-off, so the
		// legitimate retry still runs.
		if h.poweredOffSince(pressAt) {
			h.logger.Info("hardware recall: box powered off mid-recall, not re-pushing (#197)", "slot", slot)
			return
		}
		// The user deliberately stopped playback (gabbo STOP_STATE, e.g. the Bose
		// remote's stop key) after this press: the stop must hold, like the webui
		// side's stand-down, instead of being overridden by a re-push.
		if h.userStoppedSince(pressAt) {
			h.logger.Info("hardware recall: user stopped playback mid-recall, not re-pushing", "slot", slot)
			return
		}
		h.logger.Warn("hardware recall not playing yet, retrying", "slot", slot, "attempt", attempt)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		// standDown re-checks the loop-top conditions right before every
		// escalation below: the currentBoxSource probe, the nudge settle and
		// the wake together stretch several seconds past the loop-top check,
		// and a user power press inside that gap was correctly classified and
		// stamped - but the already-running iteration then toggled the box
		// straight back on (#197 check-then-act).
		standDown := func() bool { return h.recallStandDown(seq, gen, pressAt) }
		// A box stuck in INVALID_SOURCE ignores wake (WakeAndWait only toggles
		// out of STANDBY) and inertly ACKs every SetURI+Play without ever
		// fetching audio; in the mojo/ST30 field bundle the ONLY successful
		// source activation of the day followed a real sys-power toggle. If the
		// box still reports INVALID_SOURCE by the third attempt, send one
		// bounded nudge before pushing again. The source probe (an up-to-4s
		// :8090 read) only runs on the attempt that can consume it.
		if attempt == 3 && !nudged {
			if src := h.currentBoxSource(); nudgeStuckSource(attempt, nudged, src) || h.inertAckNudge(src, slot, url, pressAt) {
				nudged = true
				if standDown() {
					h.logger.Info("hardware recall: stand-down while preparing the sys-power nudge, leaving the box alone", "slot", slot)
					cancel()
					return
				}
				h.logger.Warn("hardware recall: box inert (stuck source or ACKing without fetching), sending one sys-power nudge", "slot", slot, "source", src)
				if err := h.sysPowerToggle(ctx); err != nil {
					h.logger.Warn("hardware recall: sys-power nudge failed", "slot", slot, "err", err)
				}
				time.Sleep(1500 * time.Millisecond)
			}
		}
		// Wake the box first: after the 1036 wrong-state rejection the firmware
		// often gives up on its failed self-activation and powers the source off
		// through INVALID_SOURCE -> STANDBY (field bundles 2026-07-22, all
		// models: the box "switches itself off" after the press). Every retry
		// then pushed SetURI+Play into a sleeping box and the recall never
		// converged, even though the forced re-login had already landed. A
		// user power-off stands the loop down above AND aborts the wake's
		// toggle via the predicate, so waking here only ever reverses the
		// box's own give-up. No-op when the box is awake.
		if err := h.wake(ctx, standDown); err != nil {
			h.logger.Warn("hardware recall retry: could not wake box", "slot", slot, "err", err)
		}
		// The box reset its volume to the box default when it dropped to standby;
		// restore the user's level right after the wake so the recovered preset
		// does not play the room loud for the seconds until the loop-top success
		// check would otherwise restore it. Idempotent (no-op when unchanged).
		h.restorePreRecallVolume(pressAt, preVol)
		// Final gate before the push: the wake can take seconds, and a stop or
		// power press during it must hold.
		if standDown() {
			h.logger.Info("hardware recall: stand-down after wake, not re-pushing", "slot", slot)
			cancel()
			return
		}
		if mime != "" {
			_ = h.renderer.PlayURLMime(ctx, url, name, icon, mime)
		} else {
			_ = h.renderer.PlayURL(ctx, url, name, icon)
		}
		cancel()
	}
	src, item, status := h.nowPlayingSummary()
	h.logger.Warn("hardware recall still not playing after retries", "slot", slot,
		"source", src, "itemName", item, "playStatus", status)
	if h.noteRecallExhausted != nil {
		h.noteRecallExhausted()
	}
}

// sourceRejectProbeDelay is how long verifyPlayURL waits before looking for a
// wrong-state rejection of the recall it just pushed. The box reports the 1036
// about 0.8 s after a hardware press and its source flap settles within ~50 ms
// of that, so this both catches the rejection and lets the teardown finish; a
// recall that simply started normally is already playing by now and is left
// alone.
const sourceRejectProbeDelay = 1500 * time.Millisecond

// rePushAfterSourceReject re-issues a recall the box refused with 1036
// UpnpRcvdContentItemInWrongState, without waiting for the first 5 s verify
// tick. The rejection means the box positively declined this stream, so
// pushing it again is a repair rather than a guess; a recall that is already
// playing, a box the user powered off, a deliberate stop, and a recall a newer
// press superseded are all left alone. Fires at most once per recall: the
// verify loop owns everything after it.
func (h *presetWsHandler) rePushAfterSourceReject(seq, gen uint64, pressAt time.Time, slot int, url, name, icon, mime string) {
	time.Sleep(sourceRejectProbeDelay)
	if !h.lastSourceRejectTime().After(pressAt) {
		return // the box did not refuse this recall; the normal verify governs
	}
	if h.superseded(seq, gen) {
		// A newer press/recall owns the transport: clearing it and re-pushing
		// THIS press's URL would actively tear the newer recall down.
		return
	}
	if h.recallReachedAudio(slot, url, pressAt) {
		return // refused once, then started anyway
	}
	if h.poweredOffSince(pressAt) {
		return // #197: never push into a box the user just switched off
	}
	if h.userStoppedSince(pressAt) {
		return
	}
	h.logger.Warn("hardware recall: box refused the stream (wrong state), clearing the transport and pushing again", "slot", slot)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// The rejection's INVALID_SOURCE flap can end in STANDBY within ~1s of the
	// press (the box gives up on its failed self-activation and powers off);
	// re-pushing into that sleeping box did nothing. Wake it first - a no-op
	// when it is awake. The stand-down predicate covers the check-then-act
	// gap: a user power press between the checks above and the wake's toggle
	// must keep the box off (#197).
	standDown := func() bool { return h.recallStandDown(seq, gen, pressAt) }
	if err := h.wake(ctx, standDown); err != nil {
		h.logger.Warn("wrong-state repair: could not wake box", "slot", slot, "err", err)
	}
	if standDown() {
		h.logger.Info("wrong-state repair: stand-down after wake, not re-pushing", "slot", slot)
		return
	}
	// The box refused the stream because its transport is stuck in the wrong
	// state: it is still holding its own dead-cloud ContentItem active (the box
	// answers with name=UNABLE_TO_PROCESS_NOT_LOGGED_IN detail=WrongState). Pushing
	// the IDENTICAL SetURI onto that stuck state is what the box just rejected, so a
	// blind re-push is rejected again and again until a power pull (bundle 55/56
	// ST30, 59/60 Wave, all v0.9.15). Force the transport out of the wrong state
	// first - Stop + ClearURI - then set the URI and Play from a clean slate. This
	// mirrors the proven clean-slot recall that fixed the analogous hardware-skip
	// INVALID_SOURCE wedge on Spotify (HardwareSkip, 59da772). The re-login
	// self-heal fired in parallel from the boxws NOT_LOGGED_IN routing.
	h.clearTransportForRePush(ctx, slot)
	if mime != "" {
		_ = h.renderer.PlayURLMime(ctx, url, name, icon, mime)
	} else {
		_ = h.renderer.PlayURL(ctx, url, name, icon)
	}
}

// clearTransportForRePush forces the box's UPnP transport out of a stuck
// wrong-state before a re-push: a Stop followed by an empty SetAVTransportURI
// (ClearURI) so the firmware releases the dead ContentItem it keeps trying to
// self-activate. Best-effort - both calls are advisory and a wedged renderer may
// ACK them without acting - but starting the re-push from an emptied transport is
// what turns the persistent 1036 loop into a recoverable one. Kept tiny and
// side-effect-free on the happy path: it only runs on the wrong-state repair.
func (h *presetWsHandler) clearTransportForRePush(ctx context.Context, slot int) {
	if h.renderer == nil {
		return
	}
	if err := h.renderer.Stop(ctx); err != nil {
		h.logger.Debug("wrong-state repair: transport stop returned (expected if already stopped)", "slot", slot, "err", err)
	}
	if err := h.renderer.ClearURI(ctx); err != nil {
		h.logger.Debug("wrong-state repair: clear transport URI returned", "slot", slot, "err", err)
	}
}

// verifySpotifyPlaying confirms the box reached a playing state after a Spotify
// recall and re-issues the recall a few times if not, fixing the "first press
// after reboot does nothing" race without needing a second press.
func (h *presetWsHandler) verifySpotifyPlaying(seq, gen uint64, pressAt time.Time, slot int, p presets.Preset) {
	// Anchors mirror verifyPlayURL: pressAt (the moment of the key press) for
	// the user-stop / power-off stand-downs, seq/gen for supersession.
	// Track the box's 1036 wrong-state rejections so a fresh one within a verify
	// tick forces a re-point instead of trusting the box's playing-looking state.
	lastRejectSeen := pressAt
	for attempt := 1; attempt <= 3; attempt++ {
		time.Sleep(5 * time.Second)
		// A newer press or app recall owns the transport: re-pointing at this
		// Spotify slot now would yank the user's newest choice (e.g. a radio
		// press made during this loop's 15s window used to bounce back to
		// Spotify). Stand down.
		if h.superseded(seq, gen) {
			h.logger.Info("spotify recall superseded by a newer play, standing down", "slot", slot)
			return
		}
		// A box that just rejected STR's source (1036 UpnpRcvdContentItemInWrongState,
		// the preset->preset switch race) can report attached + BUFFERING on the
		// Spotify location without ever reaching audio, then detach ~30s later with no
		// music (ST30 4->5, 2026-07-14: "loaded 30s, never played, second press worked").
		// So if a NEW rejection landed since the last tick, do not stand down on the
		// playing-check; fall through to re-point, the automatic version of the user's
		// second press.
		rej := h.lastSourceRejectTime()
		freshReject := rej.After(lastRejectSeen)
		if rej.After(lastRejectSeen) {
			lastRejectSeen = rej
		}
		// Success = the box is actually on the Spotify stream. Use the
		// location-aware check, not a bare play-state: a bounce-to-radio reads
		// as playing (would skip recovery -> double-tap) and a bare Streaming()
		// flaps to false even while Spotify plays (re-pointing on that flap
		// re-attaches and restarts the track). boxPlayingSpotify keys off the
		// now_playing location, so it is true only when Spotify really plays.
		//
		// A fresh 1036 wrong-state normally forces a re-point (the box can hang
		// attached+buffering after it). But if the box has ALREADY reached real
		// PLAY_STATE despite that transient flap, re-pointing would only knock the
		// already-playing box back into buffering (ST30 4->5: "right song plays a
		// few seconds, then stops to the buffering logo"). So on a fresh reject,
		// still stand down when the box is GENUINELY playing (PLAY_STATE, not merely
		// BUFFERING); only re-point a box that is stuck.
		if (h.spotify.Streaming() || boxPlayingSpotify(h.boxHost)) &&
			(!freshReject || boxReallyPlayingSpotify(h.boxHost)) {
			return
		}
		// Stand down if the user powered the box off mid-recall, so the re-point
		// below does not re-wake a box the user just switched off (#197).
		if h.poweredOffSince(pressAt) {
			h.logger.Info("spotify recall: box powered off mid-recall, not re-pointing (#197)", "slot", slot)
			return
		}
		// A deliberate stop (gabbo STOP_STATE) after this press must hold; do
		// not re-point the box over the user's stop.
		if h.userStoppedSince(pressAt) {
			h.logger.Info("spotify recall: user stopped playback mid-recall, not re-pointing", "slot", slot)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		// Re-point the box at the stream WITHOUT re-Play: ServeOgg resumes
		// go-librespot on attach, so this re-attaches the box without
		// reshuffling/restarting the track. A re-Play here was the cause of the
		// "same song restarts a few seconds in" the user saw. Only the final
		// attempt does a full re-Play, to recover a genuine cold-boot auth race
		// where the playlist never loaded at all.
		if attempt == 3 {
			h.logger.Warn("spotify recall not playing, full re-Play (last resort)", "slot", slot)
			_ = h.spotify.PlayAccount(ctx, p.URI, p.Account, spotify.PlayOptions{Shuffle: p.Shuffle})
		} else {
			h.logger.Warn("spotify recall not playing yet, re-pointing box", "slot", slot, "attempt", attempt)
		}
		// Re-point at the PER-SLOT Ogg URL (not the default), matching the initial
		// recall and the soft path: each Spotify preset gets a unique box-side
		// location so two Spotify presets do not collide on one URL (#22).
		_ = h.renderer.PlayURLMime(ctx, boxurl.SpotifySlot(slot), p.Name, p.Art, "audio/ogg")
		cancel()
	}
	h.logger.Warn("spotify recall still not playing after retries", "slot", slot)
}

// nowPlayingSummary is the seam-aware wrapper over boxNowPlayingSummary.
func (h *presetWsHandler) nowPlayingSummary() (string, string, string) {
	if h.boxSummaryFn != nil {
		return h.boxSummaryFn()
	}
	return boxNowPlayingSummary(h.boxHost)
}

// inertAckNudge is the second sys-power-nudge trigger (#419 Finding 2): the
// box reports source=UPNP and ACKs every SetURI+Play, yet has not fetched a
// single byte of THIS recall's proxied stream by the third attempt - the
// "ACKs everything, fetches nothing" freeze the INVALID_SOURCE-only condition
// missed (on-site ST30 capture 2026-07-25: five honest attempts, source=UPNP
// throughout, zero fetches, nudge never ran; the same box's field bundle shows
// the ONLY successful source activation of the day followed a real sys-power
// toggle). Tightly scoped so it can never interrupt real audio: proxied
// streams only (open proxy fetch = physical precondition for audio, so zero
// fetch evidence at attempt 3 proves silence), and any other source string
// (AUX, BLUETOOTH, a user-started session) never nudges.
func (h *presetWsHandler) inertAckNudge(source string, slot int, url string, pressAt time.Time) bool {
	return source == "UPNP" && streamPath(url) != "" &&
		!h.slotPulledSince(slot, pressAt) && !h.slotFetchLiveNow(slot)
}

// isNativeRadioLocation reports whether a box ContentItem location points at
// the agent's own BMX radio adapter (see internal/webui/lir.go). Such a preset
// is activated and fetched by the BOX; STR must not push a competing UPnP
// recall on top of it.
func isNativeRadioLocation(loc string) bool {
	return strings.HasPrefix(loc, "/station?data=") ||
		strings.Contains(loc, "/svc-bmx-adapter-orion/prod/orion/station")
}
