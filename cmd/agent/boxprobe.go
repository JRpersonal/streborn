// Read-only probes of the box state on :8090 (now_playing scraping).

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// probeClient is shared by every now_playing probe. Some of them sit on
// 10-second reconcile cadences, so the client (and its connection pool) is
// allocated once instead of per call. The 4 s bound is the one every probe
// has always had.
var probeClient = &http.Client{Timeout: 4 * time.Second}

// fetchNowPlaying reads the box's :8090/now_playing document once through
// boxapi. Every probe below is a verdict boxapi.NowPlaying answers, so each
// stays testable against canned XML (boxapi.ParseNowPlaying) without hardware
// or a fixed :8090 listener. ok is false on any read error, which every
// verdict maps to its zero value.
func fetchNowPlaying(boxHost string) (np *boxapi.NowPlaying, ok bool) {
	if boxHost == "" {
		boxHost = "127.0.0.1"
	}
	c := boxapi.New(boxHost)
	c.HTTP = probeClient
	np, err := c.NowPlaying(context.Background())
	if err != nil {
		return nil, false
	}
	return np, true
}

// boxInSetupOOB reports whether BoseApp's /setup says the box is still
// in out-of-box setup (SETUP_AP_OOB). Pushing presets in that state
// fails with "MargeHSM is in the wrong state" and only spams the log,
// so the reconciler waits until the box has joined a network. On any
// read error we return false (proceed) so a firmware whose /setup
// differs never silently stops reconciling on a working box.
func boxInSetupOOB(boxHost string) bool {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:8090/setup", boxHost))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return strings.Contains(string(body), "SETUP_AP_OOB")
}

// boxNowPlayingSource returns the source attribute of the box's now_playing
// (e.g. "SETUP", "STANDBY", "UPNP", "INVALID_SOURCE"), or "" on any error.
func boxNowPlayingSource(boxHost string) string {
	np, ok := fetchNowPlaying(boxHost)
	if !ok {
		return ""
	}
	return np.Source
}

// boxSetupState reads the firmware's OWN /setup, which is a different question
// from what now_playing reports. It separates a real out-of-box setup
// (state=SETUP_AP_OOB with a systemstate like SETUP_LANG_NOT_SET, the speaker
// really is raising its own access point) from #367's merely stuck source
// (state=SETUP_INACTIVE while now_playing still says source=SETUP). Those are
// different bugs and no bundle could tell them apart, because STR logged the
// clear without ever logging what it was clearing.
func boxSetupState(boxHost string) (state, systemState string) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	st, err := boxapi.New(boxHost).GetSetupStatus(ctx)
	if err != nil {
		return "unreadable", ""
	}
	return st.State, st.SystemState
}

// setupEpisodeSink records the firmware's setup episodes so /api/agent/version
// and the diagnostic bundle can report them. internal/webui.Server implements
// it; nil is a valid value and disables the recording.
type setupEpisodeSink interface {
	NoteBoxSetupEpisode()
	NoteBoxSetupCleared()
	// NoteSetupWifiPass lets STR's stored Wi-Fi yield to a setup the owner
	// did through the speaker's own setup page (#1573): it snapshots the
	// speaker's Wi-Fi store when a setup starts and retires STR's records
	// when a profile appears that STR did not write.
	NoteSetupWifiPass(inSetup bool)
}

const (
	// leaveSetupFast is the cadence while the box IS in setup, and during the
	// settling window after a fresh install.
	leaveSetupFast = 15 * time.Second
	// leaveSetupSlow is the maintenance cadence on a healthy box.
	leaveSetupSlow = 5 * time.Minute
	// leaveSetupBackoff is the cadence once this episode's clear budget is
	// spent. STR stops arguing with the firmware every fifteen seconds; the
	// state machine on the speaker wins that argument, and hammering it for a
	// quarter of an hour is not free on a box with 120 MB of RAM.
	leaveSetupBackoff = 60 * time.Second
	// leaveSetupFastWindow is how long the fast cadence runs after boot, and
	// how far each observed entry into SETUP extends it.
	leaveSetupFastWindow = 10 * time.Minute
	// leaveSetupClearsPerEpisode caps the POSTs STR spends on ONE episode. On
	// the #873 reporter's box the firmware re-raised setup after every clear
	// for about a quarter of an hour, so past the first few attempts STR is
	// not repairing anything, it is writing to the speaker every minute for no
	// effect - and a POST /setup is a write, on a path whose original comment
	// preferred a read because a write can touch the deep-standby countdown.
	//
	// ATTEMPTS, not successes: a box whose POST /setup keeps failing would
	// otherwise never reach the cap, and would be polled and warned about
	// every fifteen seconds for the rest of its uptime, into an agent log that
	// is mirrored to NAND.
	//
	// The counter is per EPISODE, so a speaker that does this once a month is
	// served at full speed every time. See leaveSetupEpisodeGap for what
	// separates two episodes from one flapping one.
	leaveSetupClearsPerEpisode = 4
	// leaveSetupEpisodeGap is how long the source has to stay OUT of SETUP
	// before the next SETUP counts as a new episode with a fresh clear budget.
	//
	// Without it the budget above is not a budget. The behaviour #873 records
	// is a speaker flipping between SETUP and INVALID_SOURCE for several
	// minutes, and a budget that refilled on every flip back into SETUP would
	// have let STR POST /setup every fifteen seconds for the whole quarter of
	// an hour - exactly the write storm the cap exists to prevent, on exactly
	// the box that provoked it.
	//
	// Five minutes matches the window webui.BoxSetup folds observations into
	// (internal/webui/boxsetup.go), so the agent log and the counter the
	// desktop app reads agree on what one episode is.
	leaveSetupEpisodeGap = 5 * time.Minute
)

// leaveSetupCadence decides how long to wait before the next now_playing read.
// Pure, so the shape of the watcher is testable without a box.
//
// The measured failure it fixes: on the #873 reporter's ST10 an entry into
// SETUP landed 14 s after the fast window closed and then sat 4m46s before the
// next check, and the one after that sat 2m45s. About seven minutes of a
// thirteen-minute episode was this gap, not the firmware. Widening the fast
// window again is not the answer (it was already widened once, for a box that
// entered SETUP forty seconds late, and this one entered ten minutes late):
// the cadence follows the STATE.
//
// What it does NOT promise is 15 s for the whole episode. While STR still has
// clear attempts left for this episode it looks every 15 s; once they are
// spent it keeps watching at 60 s and stops POSTing. That is still twenty
// times closer than the five-minute maintenance ticker this replaced, and it
// bounds what a firmware that re-raises setup for a quarter of an hour can
// cost the speaker.
func leaveSetupCadence(now, fastUntil time.Time, inSetup, backingOff bool) time.Duration {
	switch {
	case backingOff:
		return leaveSetupBackoff
	case inSetup, now.Before(fastUntil):
		return leaveSetupFast
	}
	return leaveSetupSlow
}

// leaveSetupBudgetSpent reports whether this episode's clear attempts are used
// up. attempts counts every POST /setup STR made in the current episode,
// whether it succeeded or not.
func leaveSetupBudgetSpent(attempts int) bool { return attempts >= leaveSetupClearsPerEpisode }

// leaveSetupNewEpisode reports whether a SETUP observation opens a NEW episode
// (fresh clear budget, fresh fast window) or merely continues the one that is
// already running.
//
// wasInSetup is the state before this observation; lastSeen is when SETUP was
// last observed, zero on the first one ever. A source that flips out of SETUP
// and straight back inside leaveSetupEpisodeGap is the SAME episode flapping,
// which is precisely what #873's box does, and treating each flip as a new
// episode would hand it an unlimited clear budget.
func leaveSetupNewEpisode(now, lastSeen time.Time, wasInSetup bool) bool {
	if wasInSetup {
		return false
	}
	return lastSeen.IsZero() || now.Sub(lastSeen) > leaveSetupEpisodeGap
}

// setupAPNudge wakes the leave-setup watcher the moment the gabbo bus reports
// the firmware raising its setup access point, so STR does not wait out the
// current delay after the firmware has already announced what it is doing.
//
// A wake, deliberately, and not a clear of its own. A frame-triggered clear
// would run outside the watcher's clear budget and backoff, which is the one
// thing that stops STR arguing with a firmware that re-raises setup for a
// quarter of an hour. Buffered size 1 with a non-blocking send, so any number
// of frames collapse into a single extra pass.
var setupAPNudge = make(chan struct{}, 1)

// nudgeLeaveSetupWatcher asks the watcher for an early pass. Never blocks and
// never grows: a pass is already pending or one is queued.
func nudgeLeaveSetupWatcher() {
	select {
	case setupAPNudge <- struct{}{}:
	default:
	}
}

// leaveSetupAskedWindow is how far back a key combination on the speaker counts
// as the reason for the setup that follows. The firmware's countdown runs about
// five seconds before the access point comes up.
const leaveSetupAskedWindow = time.Minute

// setupAskedAt is when the speaker last showed a key combination (its countdown
// or its manual access point mode), in Unix nanoseconds; zero for never.
var setupAskedAt atomic.Int64

// noteSetupAsked records a key combination on the speaker. The boxlog reader
// calls it from its syslog goroutine.
func noteSetupAsked(at time.Time) { setupAskedAt.Store(at.UnixNano()) }

// setupWasAsked reports whether a SETUP seen at now follows a key combination
// on the speaker, which makes it a setup somebody asked for and not a stuck one.
//
// A person holding 2 and volume down on a Portable wants the setup network to
// open. Before this the watcher cleared it 0.3 s after it came up, the speaker
// went straight back to its old Wi-Fi and the combination looked broken (#1340).
func setupWasAsked(now time.Time, askedAt int64) bool {
	if askedAt == 0 {
		return false
	}
	d := now.Sub(time.Unix(0, askedAt))
	return d >= -time.Second && d <= leaveSetupAskedWindow
}

// leaveSetupOOBHold is how long the watcher leaves a setup access point open
// that the firmware raised by itself on a speaker with no network. See
// leaveSetupHoldsOOB.
const leaveSetupOOBHold = 10 * time.Minute

// leaveSetupHoldsOOB reports whether the watcher must leave this SETUP open for
// now instead of POSTing SETUP_LEAVE. Pure.
//
// setupState is the firmware's /setup state, gateway the IPv4 default route's
// gateway ("" or "0.0.0.0" when there is none), heldSince when this episode's
// hold began (zero when it has not begun yet).
//
// Field case (#1573, Series-I ST20, BCO chassis, 2026-10-28): the owner set the
// speaker up again through Bose's own setup page, and on a later boot it could
// not join, had no lease and no gateway, and the firmware raised its setup
// access point (SETUP_AP_OOB) by itself. That access point was the speaker's
// own way back onto a network, and the watcher closed it with SETUP_LEAVE.
//
// Bounded, not open-ended. "SETUP_AP_OOB with no gateway" also matches a BCO
// speaker whose association merely failed at a cold boot (#157); leaving its
// access point up for good would strand it, because run.sh's goform rescue
// stays away from a speaker in setup. So the hold gives a person standing at
// the speaker leaveSetupOOBHold to use the setup page, and then the watcher
// clears it as before and the rescue gets its turn (run.sh does not count the
// minutes in setup against the rescue's budget). A key combination still
// opens the setup for as long as the owner wants (setupWasAsked).
//
// Unchanged, and still cleared at once:
//   - #367's stuck source: SETUP_INACTIVE while now_playing says SETUP;
//   - the out-of-box setup the watcher was written for (ST300 + scm-ST30,
//     2026-07-09), a speaker that installed STR over the network and so has a
//     gateway;
//   - a setup page that has done its job: the speaker joined the network and
//     sits in SETUP_AP_OOB + SETUP_LANG_NOT_SET, with a gateway, waiting for
//     the SETUP_LEAVE the Bose app used to send.
func leaveSetupHoldsOOB(setupState, gateway string, heldSince, now time.Time) bool {
	if setupState != setupStateAPOOB {
		return false
	}
	if gateway != "" && gateway != "0.0.0.0" {
		return false
	}
	return heldSince.IsZero() || now.Sub(heldSince) < leaveSetupOOBHold
}

// leaveSetupSourceWatcher clears a stuck out-of-box SETUP source so the box can
// play. A POST /setup SETUP_LEAVE is harmless when the box is not in setup, so
// a stray check costs nothing. See boxapi.LeaveSetup.
//
// It does NOT stop the firmware re-entering setup, and it never could: the
// setup access-point phase is the firmware's own, and it is what takes the
// Wi-Fi down (#873). What this watcher owes the user is to notice quickly, to
// say what state it found, and to stop fighting once the firmware has made
// clear it will keep re-raising it.
func leaveSetupSourceWatcher(ctx context.Context, boxHost string, logger *slog.Logger, sink setupEpisodeSink) {
	if boxHost == "" {
		boxHost = "127.0.0.1"
	}
	client := boxapi.New(boxHost)
	var (
		fastUntil = time.Now().Add(leaveSetupFastWindow)
		// attempts counts THIS episode's POSTs, successful or not; clearsTotal
		// counts the successful ones over the agent's life, for the log.
		attempts    int
		clearsTotal int
		episodes    int
		inSetup     bool
		lastClear   time.Time
		// lastSetupSeen is when SETUP was last observed, which is what tells a
		// NEW episode from the same one flapping. See leaveSetupNewEpisode.
		lastSetupSeen time.Time
		// asked marks an episode a person started with a key combination,
		// which STR leaves alone until it ends. See setupWasAsked.
		asked bool
		// warnedFailure keeps a box whose POST /setup always fails from
		// writing the same Warn line into the NAND-mirrored agent log on every
		// pass. Edge-triggered, like run.sh's fw_sig line: said on the first
		// failure of a run of them and again after the next success.
		warnedFailure bool
		// holdSince is when this episode's hold of a firmware-raised setup
		// access point began, zero when none is running. See
		// leaveSetupHoldsOOB. holding is whether the last pass held it.
		holdSince time.Time
		holding   bool
	)
	for {
		// A held setup access point is watched at the backoff cadence: the only
		// things STR waits for are the hold running out and a network coming
		// back, and neither needs a fifteen-second poll.
		backingOff := inSetup && (leaveSetupBudgetSpent(attempts) || holding)
		delay := leaveSetupCadence(time.Now(), fastUntil, inSetup, backingOff)
		// The gabbo setupAPUpdated frame can cut the wait short, but only while
		// the budget still allows a clear: once STR is backing off, a nil
		// channel makes the timer the only way out, so the firmware's own
		// flapping cannot be turned back into a fifteen-second argument.
		var nudge <-chan struct{}
		if !backingOff {
			nudge = setupAPNudge
		}
		select {
		case <-ctx.Done():
			return
		case <-nudge:
		case <-time.After(delay):
		}
		now := time.Now()
		// Tri-state, deliberately. boxNowPlayingSource returns "" for ANY read
		// failure, and reading that as "not in setup" drops the watcher back to
		// the five-minute cadence in the middle of an episode - the exact gap
		// this was written to close - and invents a spurious extra episode on
		// the next successful read. A read that failed says nothing, so the
		// state and the cadence stay as they were.
		src := boxNowPlayingSource(boxHost)
		if src == "" {
			continue
		}
		if src != "SETUP" {
			if holding {
				holding = false
				logger.Info("leave-setup: the speaker left the setup access point it raised by itself",
					"source", src, "episode", episodes)
			}
			inSetup = false
			if sink != nil {
				sink.NoteSetupWifiPass(false)
			}
			continue
		}
		// Every SETUP observation refreshes the app-facing counter; that side
		// folds observations inside its own window into one episode, so the
		// state it reports stays "active" for as long as this really lasts.
		if sink != nil {
			sink.NoteBoxSetupEpisode()
			sink.NoteSetupWifiPass(true)
		}
		reentered := !inSetup
		// A NEW episode extends the fast window and refills the clear budget:
		// an episode that starts ten minutes after boot deserves the same
		// attention as one that starts at boot. A source that flips out of
		// SETUP and back within leaveSetupEpisodeGap is the SAME episode, and
		// refilling the budget for it would uncap the writes entirely.
		if leaveSetupNewEpisode(now, lastSetupSeen, inSetup) {
			episodes++
			attempts = 0
			asked = false
			warnedFailure = false
			holdSince = time.Time{}
			holding = false
			fastUntil = now.Add(leaveSetupFastWindow)
		}
		lastSetupSeen = now
		if !asked && setupWasAsked(now, setupAskedAt.Load()) {
			asked = true
			logger.Info("leave-setup: the setup was asked for with a key combination on the speaker, leaving it open",
				"episode", episodes)
		}
		if asked {
			inSetup = true
			continue
		}
		// Said on every re-entry, but only while a clear is still on the table:
		// past the cap the watcher is silent by design, and a flapping source
		// would otherwise write this line into the NAND-mirrored log for the
		// rest of the episode.
		if reentered && !lastClear.IsZero() && !leaveSetupBudgetSpent(attempts) {
			logger.Info("leave-setup: the box re-entered SETUP after a clear",
				"sinceClearSec", int(now.Sub(lastClear).Seconds()),
				"episode", episodes, "attempts", attempts, "clears", clearsTotal,
				// The delay the loop will actually pick next, computed rather
				// than assumed.
				"nextCheckSec", int(leaveSetupCadence(now, fastUntil, true, false).Seconds()))
		}
		inSetup = true
		if leaveSetupBudgetSpent(attempts) {
			// Observe, do not POST. The firmware has answered this episode's
			// clears by raising setup again every time; STR watches for the
			// end of it and stops writing to the speaker.
			continue
		}
		setupState, systemState := boxSetupState(boxHost)
		if systemState == systemStateLangNotSet {
			// The language gate is what keeps raising this; let the gate
			// check run now rather than after its first delay.
			nudgeBoxLanguage()
		}
		gateway, gatewayIface := defaultRouteV4()
		if leaveSetupHoldsOOB(setupState, gateway, holdSince, now) {
			// Said once per hold, not per pass: every line lands in the
			// NAND-mirrored agent log.
			if holdSince.IsZero() {
				holdSince = now
				logger.Info("leave-setup: the speaker raised its own setup access point and has no network, leaving it open for a while so it can be set up again",
					"setupState", setupState, "systemState", systemState,
					"holdSec", int(leaveSetupOOBHold.Seconds()), "episode", episodes)
			}
			holding = true
			continue
		}
		if holding {
			holding = false
			reason := "hold-elapsed"
			if gateway != "" && gateway != "0.0.0.0" {
				reason = "network-back"
			}
			logger.Info("leave-setup: ending the hold of the speaker's own setup access point, clearing it",
				"reason", reason, "setupState", setupState, "systemState", systemState,
				"gatewayIface", gatewayIface, "heldSec", int(now.Sub(holdSince).Seconds()),
				"episode", episodes)
		}
		attempts++
		lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := client.LeaveSetup(lctx)
		cancel()
		if err != nil {
			if !warnedFailure {
				warnedFailure = true
				logger.Warn("leave-setup: could not clear the out-of-box SETUP source",
					"err", err, "setupState", setupState, "systemState", systemState,
					"episode", episodes, "attempts", attempts)
			}
		} else {
			warnedFailure = false
			clearsTotal++
			lastClear = now
			if sink != nil {
				sink.NoteBoxSetupCleared()
			}
			// Two messages, because after the first clear of an episode the
			// old one is a claim the evidence contradicts: on the #873 box the
			// firmware raised setup again after every clear, so "so it can
			// play" would have gone into the NAND log three times for a
			// speaker that went on being unusable for another ten minutes.
			// The first clear of an episode keeps the wording that has been
			// right on every #367 box since 2026-07-09.
			if attempts == 1 {
				logger.Info("leave-setup: cleared the box's stuck out-of-box SETUP source so it can play (no power-cycle needed)",
					"setupState", setupState, "systemState", systemState,
					"episode", episodes, "clears", clearsTotal)
			} else {
				logger.Info("leave-setup: cleared the box's SETUP source again; the firmware had re-raised it",
					"setupState", setupState, "systemState", systemState,
					"episode", episodes, "attempts", attempts, "clears", clearsTotal)
			}
		}
		if leaveSetupBudgetSpent(attempts) {
			// Said once per episode, when the cap is reached: the reason
			// belongs in the bundle rather than being inferred from a gap in
			// the timestamps, but it must not become its own log storm.
			logger.Warn("leave-setup: this episode's clear budget is spent, watching without clearing",
				"episode", episodes, "attempts", attempts, "clears", clearsTotal,
				"nextCheckSec", int(leaveSetupBackoff.Seconds()))
		}
	}
}

// boxIsPlaying reports whether the Bose box is actively rendering audio (any
// source), so the memory guard never reboots mid-playback. Best-effort: any
// error or a non-play state counts as not playing.
func boxIsPlaying(boxHost string) bool {
	np, ok := fetchNowPlaying(boxHost)
	return ok && np.IsPlaying()
}

// boxSourceAndPlaying returns the active source AND whether audio is flowing,
// from a single now_playing read. The preset reconcile needs both to decide
// whether it may write, and two separate probes would be two requests at a
// moment when the box is already busy rendering.
func boxSourceAndPlaying(boxHost string) (src string, playing, ok bool) {
	np, got := fetchNowPlaying(boxHost)
	if !got {
		return "", false, false
	}
	return np.Source, np.IsPlaying(), true
}

// boxNowPlayingSummary returns compact now_playing evidence for the recall
// settle logs: the active source, the box's own itemName (what a display model
// like the Wave/ST20 shows) and the playStatus. It answers two open field
// questions at once: which side of the Wave wrong-state race won (audio
// without name vs name without audio - itemName empty on the winning STR push,
// #469/Wave 2026-07-25), and whether a verify success was the box's stale
// same-slot ContentItem rather than a real fetch (#419 Finding 1).
func boxNowPlayingSummary(boxHost string) (source, itemName, playStatus string) {
	np, ok := fetchNowPlaying(boxHost)
	if !ok {
		return "", "", ""
	}
	return np.Summary()
}

// nudgeStuckSource is the sys-power-nudge decision: the box still reports
// INVALID_SOURCE at the third verify attempt and no nudge ran yet. Bounded to
// exactly one nudge per recall; earlier attempts give the normal wake+re-push
// a chance first.
func nudgeStuckSource(attempt int, nudged bool, source string) bool {
	return attempt == 3 && !nudged && source == "INVALID_SOURCE"
}

// boxPlayingURL reports whether the box is in a play/buffering state AND its
// now_playing actually points at wantURL, the URL this recall pushed. It is the
// success signal for the radio recall verify.
//
// The location check is what a bare play-state check misses: a box that rejects
// the recall (1036 UpnpRcvdContentItemInWrongState, chronic on the Wave) keeps
// reporting the PREVIOUS stream's play state while never fetching the new one,
// so the verify passed at its first tick and the user was left with a display
// that shows the station and no audio at all.
//
// Deliberately forgiving in one direction: firmware whose now_playing carries NO
// location at all falls back to the plain play-state verdict, so a model that
// simply does not report a location does not end up in an endless re-push loop.
func boxPlayingURL(boxHost, wantURL string) bool {
	np, ok := fetchNowPlaying(boxHost)
	return ok && np.IsURL(wantURL)
}

// streamPath reduces an STR stream URL to the path+query the box echoes back in
// now_playing ("http://127.0.0.1:8888/stream/5" -> "/stream/5"). See
// boxapi.StreamPath, which IsURL compares on.
func streamPath(u string) string { return boxapi.StreamPath(u) }

// boxPlayingSpotify reports whether the box's now_playing is STR's Spotify
// stream in a play/buffering state. It is the reliable success signal for the
// Spotify recall verify, where a bare play-state check (boxIsPlaying) and a
// bare Streaming() check each fail one way: right after a press the box can
// bounce off STR's preset to the PREVIOUS (radio) preset, which boxIsPlaying
// reads as "playing" and would wrongly skip recovery (the first-press
// double-tap); while Streaming() flaps to false even when the box is happily
// playing Spotify, and re-pointing on that flap re-attaches the box and
// restarts the track. The now_playing location tells the two apart.
func boxPlayingSpotify(boxHost string) bool {
	np, ok := fetchNowPlaying(boxHost)
	return ok && np.IsSpotify()
}

// boxReallyPlayingSpotify is the strict form of boxPlayingSpotify: the box is on
// the Spotify stream AND actually in PLAY_STATE (audio flowing), not merely
// BUFFERING. The verify loop uses it to avoid re-pointing, and thereby disrupting,
// a box that has genuinely started playing after a transient 1036 wrong-state flap
// on a preset->preset switch.
func boxReallyPlayingSpotify(boxHost string) bool {
	np, ok := fetchNowPlaying(boxHost)
	return ok && np.ReallySpotify()
}
