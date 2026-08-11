// Standby handling and resume-after-power-on / reconnect logic.

package webui

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/boxwrites"
)

// ResumeLastPlay re-pushes the last stream STR played: the power-on resume. On
// the SoundTouch firmware a power press emits NO powerStateUpdated frame; the box
// only reports a source change out of STANDBY and, because it can no longer play
// its UPNP selection itself, restores it as INVALID_SOURCE + DO_NOT_RESUME. boxws
// surfaces that as OnPowerWake (verified live on a Portable/taigan 2026-06-13:
// powerStateUpdated never appears, a real power press = STANDBY -> INVALID_SOURCE
// + DO_NOT_RESUME). Power-on is an explicit "play it again", so this overrides
// the user-stop the power-off STOP_STATE set and clears the failed/attempt state
// so even a previously dead stream gets one fresh try.
//
// The same DO_NOT_RESUME wake is also what a SELF-wake produces (a box pulled out
// of standby by its stereo pair / zone, which made Klaus' box start playing on
// its own, 2026-06-12). The two are indistinguishable on the wire, so the guard
// is zone membership: a standalone box can only leave standby by a user press, so
// it resumes; a box that is part of a zone does NOT (see boxInZone). Plus the
// per-box opt-out below.
func (s *Server) ResumeLastPlay() {
	if s.renderer == nil {
		return
	}
	// Per-box opt-out (default on). The few users who want silence on power-on
	// turn this off; everyone else gets the last station back, like Bose did.
	if !s.resumeOnPowerOnEnabled() {
		s.logger.Info("wake resume: power-on resume disabled for this box, not resuming")
		return
	}
	// Crash-loop guard (#381): when the last attempts all ended in a reboot
	// before playback stabilised, the persisted stream itself is the prime
	// suspect for crashing the box. Stand down BEFORE the wake call below so a
	// guarded box is not even woken. A manual play re-arms via setLastPlay.
	if s.autoResumeBlocked() {
		s.logger.Warn("wake resume: standing down, the last automatic resumes each ended in a reboot before playback stabilised (crash-loop guard, #381); press a preset key or start playback from the app to re-enable")
		return
	}
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	// Split the two no-resume reasons so a diagnostic shows which one hit, and
	// allow a much longer age than the old 12h: a power press after an overnight
	// (or weekend) standby still expects the last station back, like Bose did
	// (#119 Klaus). The persisted lastPlay (NAND) survives the agent restart that
	// a long standby often triggers, so this age is what actually gates now.
	if lp == nil {
		s.lastPlayMu.Unlock()
		s.logger.Info("wake resume: no last station remembered (no resume target on NAND yet)")
		return
	}
	if age := time.Since(lp.ts); age >= resumeMaxAge {
		s.lastPlayMu.Unlock()
		s.logger.Info("wake resume: last station too old to resume", "ageHours", int(age.Hours()), "maxHours", int(resumeMaxAge.Hours()))
		return
	}
	lp.failed = false
	lp.rePushes = 0
	boxURL, title, art, mime, capturedTS := lp.boxURL, lp.title, lp.art, lp.mime, lp.ts
	s.lastPlayMu.Unlock()

	go func() {
		// Let the power transition settle so the box's reported state is
		// unambiguous before we decide. The DO_NOT_RESUME wake that triggers this
		// fires on a power-on, but the box can also reach standby again right
		// after, so settle then read the real state.
		time.Sleep(2 * time.Second)

		// scm power-off bounce guard (#197). Some ST20 (scm) firmware oscillates
		// UPNP->STANDBY->UPNP on a power-off, and the STANDBY->UPNP restore arrives
		// as the SAME DO_NOT_RESUME frame a genuine power-ON does, so this
		// OnPowerWake can fire from a power-OFF. If HandleEnterStandby just cleared
		// the transport for this box (a UPNP->STANDBY we saw moments ago), this
		// "wake" is that bounce, not a user switching the box on: stand down and,
		// crucially, leave the user-stop HandleEnterStandby set intact (do NOT fall
		// through to the clear below) so neither this resume nor the parallel auto
		// re-push pulls the box back up. Without this, a power-off taken while the
		// stream was still buffering left the box briefly reporting BUFFERING/PLAY
		// at this settle point, so the standby && !busy guard below missed and STR
		// woke the box back on (deqw, ST20 #197: "turns back on and continues
		// playing music").
		if s.standbyStoppedRecently() {
			s.logger.Info("wake resume: standby bounce detected (box just powered off STR's source), not resuming (#197)")
			return
		}

		// Klaus guard: a box pulled out of standby by its stereo pair / zone emits
		// the SAME DO_NOT_RESUME wake as a user power press, so the frame cannot
		// tell them apart. But a STANDALONE box can only leave standby by a user
		// pressing power, so it is safe to resume; a box that is part of a zone is
		// not (the pair may have woken it), so stand down. This is what lets the
		// resume default to ON without bringing back Klaus' spontaneous playback.
		// Checked live (authoritative) rather than from cached zone events.
		if s.boxInZone() {
			s.logger.Info("wake resume: box is in a zone / stereo pair, not auto-resuming (self-wake guard)")
			return
		}

		// Power-off bounce guard for boxes where the UPnP-source standby did not
		// arm standbyStoppedRecently above. A rhino ST10 reports a power-off as a
		// gabbo STOP_STATE (-> NoteUserStop) rather than the UPNP->STANDBY that
		// HandleEnterStandby keys on, so the #197 guard missed it and this wake
		// resumed a box the user had just switched off (Svagerka, ST10: "off does
		// not stick, playback resumes within seconds"). Discriminator: a power-OFF
		// is preceded by a deliberate stop within the last few seconds (the box was
		// playing when off was pressed), whereas a genuine power-ON follows a box
		// that was already stopped/off, so no fresh stop precedes it. If a user stop
		// landed within userStopWindow, treat this wake as the power-off bounce and
		// stand down, keeping the user-stop intact so the parallel auto re-push does
		// not pull the box back up either. A real power-on hours later has no recent
		// stop, so it still resumes.
		if s.userStoppedRecently() {
			s.logger.Info("wake resume: a deliberate stop immediately preceded this wake (power-off bounce), not resuming")
			return
		}

		// Discriminate a power-OFF from a power-ON wake (#105): after the user
		// presses power OFF the box settles in standby; after a wake it has
		// already left standby (the DO_NOT_RESUME we are reacting to is the box
		// restoring its selection while awake). Never wake a box the user just
		// turned off, and leave the user-stop suppression intact so the parallel
		// auto-re-push does not pull it back up either.
		if standby, busy := s.boxPlayState(); standby && !busy {
			s.logger.Info("wake resume: box is in standby (deliberate power-off), not resuming")
			return
		}

		// Genuine power-on: an explicit "play it again", so drop the recent
		// user-stop (the power-off emitted STOP_STATE) that would otherwise
		// suppress this resume and the auto-re-push.
		s.lastUserStopMu.Lock()
		s.lastUserStop = time.Time{}
		s.lastUserStopMu.Unlock()

		if s.boxHost != "" {
			wctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			_ = boxcli.WakeAndWait(wctx, s.boxHost, 6*time.Second, s.logger)
			cancel()
		}
		// Box already playing? Then this DO_NOT_RESUME came from STR waking the
		// box for a preset press (not a bare power-on), and that press already
		// started playback. Skip so we do not re-push the same stream and cause a
		// double-start hiccup.
		if _, busy := s.boxPlayState(); busy {
			s.logger.Info("wake resume: box already playing, no resume needed")
			return
		}
		s.boxCmdMu.Lock()
		defer s.boxCmdMu.Unlock()
		// A play/recall that raced this resume may have been holding boxCmdMu
		// while the checks above ran: it is the very request whose ensureBoxReady
		// wake fired this DO_NOT_RESUME, and its stream had not started when the
		// busy check above looked (so the check missed it). By the time we get
		// the lock that recall has updated lastPlay, and pushing the target we
		// captured at entry would replace the user's NEW station with the
		// PREVIOUS one (#252: recall slot 5 right after a wake ended up back on
		// slot 4). Re-read lastPlay under the lock and stand down when it moved.
		s.lastPlayMu.Lock()
		cur := s.lastPlay
		s.lastPlayMu.Unlock()
		if resumeIsStale(boxURL, capturedTS, cur) {
			s.logger.Info("wake resume: a newer play started while this resume waited, standing down",
				"captured", boxURL, "current", lastPlayURL(cur))
			return
		}
		// Count the attempt and persist BEFORE the push: if this stream crashes
		// the box, the incremented count is what the next boot loads (#381).
		s.noteAutoResumeAttempt()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var err error
		if mime != "" {
			err = s.renderer.PlayURLMime(ctx, boxURL, title, art, mime)
		} else {
			err = s.renderer.PlayURL(ctx, boxURL, title, art)
		}
		if err != nil {
			s.logger.Warn("wake resume: play failed", "err", err, "url", boxURL)
			return
		}
		s.logger.Info("wake resume: resumed last stream after power-on", "url", boxURL, "title", title)
	}()
}

// resumeIsStale reports whether a resume target captured at the START of a
// wake-resume / reconnect-recovery has been overtaken by a newer play by the
// time the box command lock was finally acquired. Those paths capture lastPlay,
// then wait (settle sleep, state probes, boxCmdMu); a user play/recall that
// held the lock meanwhile has called setLastPlay, so the capture is the
// PREVIOUS station and pushing it would clobber the one the user just started
// (#252). A same-URL entry with a newer timestamp is stale too: the newer play
// already (re)started that stream, and a second push only causes a
// double-start hiccup.
func resumeIsStale(capturedURL string, capturedTS time.Time, current *lastPlayInfo) bool {
	if current == nil {
		return true // nothing to resume anymore
	}
	return current.boxURL != capturedURL || !current.ts.Equal(capturedTS)
}

// lastPlayURL is a nil-safe accessor for the stand-down log line.
func lastPlayURL(lp *lastPlayInfo) string {
	if lp == nil {
		return ""
	}
	return lp.boxURL
}

// RecoverAfterReconnect re-pushes the last stream when the gabbo WebSocket
// reconnects and finds the box awake but stuck on an STR selection it is not
// playing. This recovers the lost first press after a deep/overnight standby
// (#183): the box wakes and emits the preset/now-selection frame before STR has
// reconnected (the reconnect backoff had grown while the box was unreachable),
// so OnPresetSelected never runs and the display shows "service unavailable"
// until a second press.
//
// It reuses the power-on resume's safeguards so it can only ever resume, never
// surprise: it honours the per-box opt-out, stands down inside a zone (a
// stereo-pair self-wake looks identical on the wire), suppresses a deliberate
// user stop, and acts only when the box is awake-and-idle on an STR source
// (boxSelectionStuck). A routine idle reconnect (box in standby), a box already
// playing, or a box on a native source (AUX/Bluetooth) is a no-op. Unlike
// ResumeLastPlay it does NOT clear the user-stop: a reconnect is not the explicit
// "play it again" a real power press is.
func (s *Server) RecoverAfterReconnect() {
	if s.renderer == nil {
		return
	}
	if !s.resumeOnPowerOnEnabled() {
		return
	}
	// Crash-loop guard (#381). This path is the one a crash-caused REBOOT
	// takes: the agent starts, the gabbo WS connects, the box sits awake with
	// the stuck STR selection the crash left behind, and without this guard
	// the recovery re-pushes the very stream that crashed the box - forever.
	if s.autoResumeBlocked() {
		s.logger.Warn("reconnect recovery: standing down, the last automatic resumes each ended in a reboot before playback stabilised (crash-loop guard, #381); press a preset key or start playback from the app to re-enable")
		return
	}
	// A deliberate user stop must survive a WS reconnect: without this guard a
	// reconnect resumed the last stream the user had stopped.
	if s.userStoppedRecently() {
		s.logger.Info("reconnect recovery: user stopped recently, not resuming")
		return
	}
	// A gabbo reconnect can land mid power-off bounce (a flapping scm box keeps
	// reconnecting). If STR saw this box drop UPNP->STANDBY moments ago, stand down
	// so the reconnect recovery does not re-push a URI the firmware bounces on (#197).
	if s.standbyStoppedRecently() {
		s.logger.Info("reconnect recovery: box just dropped to standby, not resuming (#197)")
		return
	}
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	// The strict age gate depends on WHAT the box is stuck on and is applied
	// below once the selection is known (reconnectResumeWindow); here only the
	// generous outer bound shared with the power-on resume applies.
	if lp == nil || time.Since(lp.ts) >= resumeMaxAge {
		s.lastPlayMu.Unlock()
		return
	}
	boxURL, title, art, mime, capturedTS := lp.boxURL, lp.title, lp.art, lp.mime, lp.ts
	s.lastPlayMu.Unlock()

	go func() {
		// Let the wake settle so the box's reported state is unambiguous before
		// we decide (the box can flip through transient states right after a
		// reconnect/wake).
		time.Sleep(2 * time.Second)
		if s.boxInZone() {
			s.logger.Info("reconnect recovery: box in a zone / stereo pair, standing down (self-wake guard)")
			return
		}
		stuck, selLoc := s.boxStuckSelection()
		if !stuck {
			return // asleep, already playing, or on a native source: nothing to recover
		}
		// Only resume when the box is stuck on OUR last stream. A non-empty
		// selection location that does NOT reference lastPlay means the box
		// moved on (e.g. a failed Spotify preset recall left it on a different
		// selection); resurrecting the old stream then surprised the user by
		// starting an unrelated preset (#ST30 preset 1 self-started, 2026-07-10).
		// An empty INVALID_SOURCE (the box restored STR's UPNP source but could
		// not self-activate it, #183) carries no location and is the genuine
		// recovery target, gated by the freshness + user-stop guards above.
		if selLoc != "" && !sameStream(selLoc, boxURL) {
			s.logger.Info("reconnect recovery: box is stuck on a different selection than our last stream, not resuming",
				"selection", selLoc, "lastPlay", boxURL)
			return
		}
		if age := time.Since(capturedTS); age >= reconnectResumeWindow(selLoc) {
			s.logger.Info("reconnect recovery: last stream too old for this recovery, not resuming",
				"age", age, "window", reconnectResumeWindow(selLoc))
			return
		}
		s.boxCmdMu.Lock()
		defer s.boxCmdMu.Unlock()
		// Same anti-clobber guard as the power-on resume (#252): a play/recall
		// that held boxCmdMu while this recovery waited has updated lastPlay, and
		// pushing the entry capture now would replace the user's new stream.
		s.lastPlayMu.Lock()
		cur := s.lastPlay
		s.lastPlayMu.Unlock()
		if resumeIsStale(boxURL, capturedTS, cur) {
			s.logger.Info("reconnect recovery: a newer play started while this recovery waited, standing down",
				"captured", boxURL, "current", lastPlayURL(cur))
			return
		}
		// Count the attempt and persist BEFORE the push: if this stream crashes
		// the box, the incremented count is what the next boot loads (#381).
		s.noteAutoResumeAttempt()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var err error
		if mime != "" {
			err = s.renderer.PlayURLMime(ctx, boxURL, title, art, mime)
		} else {
			err = s.renderer.PlayURL(ctx, boxURL, title, art)
		}
		if err != nil {
			s.logger.Warn("reconnect recovery: play failed", "err", err, "url", boxURL)
			return
		}
		s.logger.Info("reconnect recovery: resumed last stream after WS reconnect", "url", boxURL, "title", title)
	}()
}

// boxSelectionStuck reports whether the box is awake with an STR selection it is
// not playing: the state a lost preset-press / power-on wake leaves behind (the
// box restored STR's UPNP source as INVALID_SOURCE and shows "service
// unavailable"). It is the trigger for RecoverAfterReconnect (#183) and is
// deliberately narrow: a box in standby, a box already playing/paused, or a box
// on a native source (AUX, Bluetooth) all return false so the recovery never
// fights them.
func (s *Server) boxSelectionStuck() bool {
	stuck, _ := s.boxStuckSelection()
	return stuck
}

// nowPlayingLocationRe pulls the ContentItem location out of a now_playing body.
var nowPlayingLocationRe = regexp.MustCompile(`location="([^"]*)"`)

// boxStuckSelection reports whether the box is awake with a stuck STR selection
// (see boxSelectionStuck's contract) AND the location of that selection, so the
// caller can tell whether the box is stuck on OUR last stream (resume it) or on
// something else (leave it). An empty location means the box carries no
// ContentItem (a bare INVALID_SOURCE), the #183 recovery target.
func (s *Server) boxStuckSelection() (stuck bool, location string) {
	if s.boxHost == "" {
		return false, ""
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get("http://" + s.boxHost + ":8090/now_playing")
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	body := string(b)
	if strings.Contains(body, "STANDBY") {
		return false, "" // box asleep: a routine idle reconnect, nothing to recover
	}
	if strings.Contains(body, "PLAY_STATE") || strings.Contains(body, "BUFFERING_STATE") || strings.Contains(body, "PAUSE_STATE") {
		return false, "" // already playing/paused
	}
	// Only recover an STR-owned selection (UPNP) or the box's failed
	// self-activation of it (INVALID_SOURCE). A native source the user picked
	// (AUX, BLUETOOTH, ...) is left alone.
	if !strings.Contains(body, `source="UPNP"`) && !strings.Contains(body, "INVALID_SOURCE") {
		return false, ""
	}
	if m := nowPlayingLocationRe.FindStringSubmatch(body); m != nil {
		location = m[1]
	}
	return true, location
}

// sameStream reports whether two box-facing stream URLs point at the same STR
// stream, comparing by path (the host differs between the master's own loopback
// form http://127.0.0.1:8888/stream/2 and the box-visible :17008 form). Used to
// tell whether the box's stuck selection is our last stream.
func sameStream(a, b string) bool {
	pa, pb := streamPath(a), streamPath(b)
	return pa != "" && pa == pb
}

func streamPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Path
}

// reconnectResumeMaxAge bounds how old the last stream may be when the box is
// stuck on OUR OWN stream location after a WS blip: that shape means the box
// dropped the stream mid-playback, so it only counts as "just playing" for a
// few minutes. Deliberately short - resurrecting an hours-old stopped stream
// surprised users (#ST30 self-start, 2026-07-10).
const reconnectResumeMaxAge = 10 * time.Minute

// reconnectResumeWindow returns how old the last stream may be for the
// reconnect recovery to resume it, depending on the box's stuck selection. A
// bare INVALID_SOURCE with no location is the wake signature (#183): after a
// deep standby the box often reboots, the wake frame beats the rebooted
// agent's first gabbo connect, and this recovery is then the ONLY path that
// brings the last station back - so it keeps the power-on resume's generous
// window ("abends aus, morgens an", #119). A selection stuck on our own stream
// location is a mid-playback drop and only resumes when it happened minutes
// ago (see reconnectResumeMaxAge).
func reconnectResumeWindow(stuckSelectionLocation string) time.Duration {
	if stuckSelectionLocation == "" {
		return resumeMaxAge
	}
	return reconnectResumeMaxAge
}

// defaultResumeOnPowerOnPath is the NAND flag file for the per-box power-on
// resume opt-out. Absent or "1" means on (the default), "0" means off.
const defaultResumeOnPowerOnPath = "/mnt/nv/streborn/resume-on-power-on"

// defaultDisplayTrackPath is the NAND flag file for the per-box "show the live
// radio track on the speaker display" opt-in. Absent or "0" means off (the
// default); "1" means on.
const defaultDisplayTrackPath = "/mnt/nv/streborn/display-track-on-box"

// minDisplayPushInterval is the shortest gap between two ICY display pushes. Each
// push re-buffers the box (a brief audio gap), and some stations flip the
// StreamTitle between the song and promo/talk lines every few seconds, so the
// push is rate-limited to keep those gaps occasional rather than constant.
const minDisplayPushInterval = 12 * time.Second

// displayTrackEnabled reports whether "show the live radio track on the speaker
// display" is enabled for this box. Default OFF: the flag file is absent on a
// fresh install and only an explicit "1"/"true"/"on"/"yes" turns it on. The env
// override STR_ICY_DISPLAY=1 still forces it on for dev/testing.
func (s *Server) displayTrackEnabled() bool {
	if icyDisplayPushEnabled() {
		return true
	}
	path := s.displayTrackPath
	if path == "" {
		path = defaultDisplayTrackPath
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(string(b))) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// defaultDisplayTrackModePath is the NAND file for WHAT the display push shows:
// "both" (Artist - Title, the default), "title", or "artist".
const defaultDisplayTrackModePath = "/mnt/nv/streborn/display-track-mode"

// displayTrackMode returns the configured display content: "both" | "title" |
// "artist". Default "both" (absent/unrecognized file).
func (s *Server) displayTrackMode() string {
	path := s.displayTrackPath
	if path == "" {
		path = defaultDisplayTrackPath
	}
	b, err := os.ReadFile(modePathFor(path))
	if err != nil {
		return "both"
	}
	switch m := strings.ToLower(strings.TrimSpace(string(b))); m {
	case "title", "artist", "both":
		return m
	default:
		return "both"
	}
}

// modePathFor derives the mode file path next to the enabled-flag file, so a test
// override of displayTrackPath keeps both files together.
func modePathFor(enabledPath string) string {
	if enabledPath == "" || enabledPath == defaultDisplayTrackPath {
		return defaultDisplayTrackModePath
	}
	return enabledPath + ".mode"
}

// splitStreamTitle splits an ICY StreamTitle into (artist, title). The separator
// is the de-facto tell across stations, matching the app's Recently-played view:
// " - " is "Artist - Title", " / " is "Title / Artist" (flipped). No separator:
// the whole string is the title, artist empty.
func splitStreamTitle(s string) (artist, title string) {
	s = strings.TrimSpace(s)
	for _, sep := range []string{" / ", " - ", " – ", " — "} {
		if i := strings.Index(s, sep); i > 0 && i+len(sep) < len(s) {
			left, right := strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(sep):])
			if left == "" || right == "" {
				continue
			}
			if sep == " / " {
				return right, left // "Title / Artist"
			}
			return left, right // "Artist - Title"
		}
	}
	return "", s
}

// displayTrackText applies the configured display mode to a raw ICY StreamTitle,
// returning what should appear on the speaker display. "both" keeps the full
// string; "title"/"artist" use the split, falling back to the full string when
// the split has no artist (no separator), so the display is never blank.
func (s *Server) displayTrackText(streamTitle string) string {
	switch s.displayTrackMode() {
	case "title":
		if _, title := splitStreamTitle(streamTitle); title != "" {
			return title
		}
	case "artist":
		if artist, _ := splitStreamTitle(streamTitle); artist != "" {
			return artist
		}
	}
	return strings.TrimSpace(streamTitle)
}

// boxInZone reports whether the speaker is currently part of a multiroom zone or
// stereo pair, read live from the box (/getZone). It is the power-on resume's
// self-wake guard: a standalone box can only leave standby by a user power press
// (safe to resume), but a zone member may have been woken by its pair (Klaus'
// spontaneous playback), so it must not auto-resume. On a read error it returns
// false (treat as standalone): a missing zone read should not silently disable
// the feature for the standalone majority, and the per-box opt-out is the
// backstop for the rare paired box that also fails the read.
func (s *Server) boxInZone() bool {
	if s.boxHost == "" {
		return false
	}
	// Persisted membership first: a box we recorded as part of a zone or stereo
	// pair must stand down from power-on resume even if the live /getZone races a
	// zone that is still forming (it legitimately reads empty mid-handshake) or
	// the read errors. This closes the self-wake gap where a member woken by its
	// pair resumed because the live read came back empty. Fail-safe direction:
	// silence beats spontaneous playback.
	if s.zones != nil {
		if _, ok := s.zones.Get(); ok {
			return true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	z, err := boxapi.New(s.boxHost).GetZone(ctx)
	if err != nil {
		s.logger.Info("wake resume: zone read failed, treating box as standalone", "err", err)
		return false
	}
	return z.Master != ""
}

// resumeOnPowerOnEnabled reports whether "resume the last station on power-on"
// is enabled for this box. Default ON: the flag file is absent on a fresh
// install, and only an explicit opt-out ("0" / "false" / "off" / "no") disables
// it. An unreadable file also defaults to on (fallback-first).
func (s *Server) resumeOnPowerOnEnabled() bool {
	path := s.resumeOnPowerOnPath
	if path == "" {
		path = defaultResumeOnPowerOnPath
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(string(b))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// userStopWindow is how long after a deliberate user stop the auto-re-push
// stays suppressed. A stop causes a proxy disconnect, and maybeRePush waits 2s
// before deciding; this window comfortably covers that gap plus the lag of a
// gabbo STOP_STATE frame, while being short enough that a genuine box-side drop
// later in the session is still resumed.
const userStopWindow = 6 * time.Second

// NoteUserStop records that the user deliberately stopped (or paused) playback.
// The auto-re-push checks this so a wanted stop is not immediately undone.
// Called from the STR Stop/Pause endpoints and from the gabbo STOP_STATE hook.
func (s *Server) NoteUserStop() {
	s.lastUserStopMu.Lock()
	s.lastUserStop = time.Now()
	s.lastUserStopMu.Unlock()
}

// NoteExplicitStop records a stop or pause that arrived as a REQUEST. Callers
// that infer a stop from the box's own state must not call it; see the
// lastExplicitStop field for why the distinction exists.
func (s *Server) NoteExplicitStop() {
	s.lastExplicitStopMu.Lock()
	s.lastExplicitStop = time.Now()
	s.lastExplicitStopMu.Unlock()
}

// explicitStopAfter reports whether the user asked for a pause or a stop after
// t. Absolute rather than a rolling window, so a caller that waits does not see
// its own answer expire.
func (s *Server) explicitStopAfter(t time.Time) bool {
	s.lastExplicitStopMu.Lock()
	defer s.lastExplicitStopMu.Unlock()
	return !s.lastExplicitStop.IsZero() && s.lastExplicitStop.After(t)
}

// ClearUserStop cancels a recorded user-stop so a manual resume re-enables the
// guarded auto-re-push immediately instead of staying suppressed for the
// userStopWindow.
func (s *Server) ClearUserStop() {
	s.lastUserStopMu.Lock()
	s.lastUserStop = time.Time{}
	s.lastUserStopMu.Unlock()
}

// userStoppedRecently reports whether a deliberate stop happened within
// userStopWindow.
func (s *Server) userStoppedRecently() bool {
	s.lastUserStopMu.Lock()
	defer s.lastUserStopMu.Unlock()
	return !s.lastUserStop.IsZero() && time.Since(s.lastUserStop) < userStopWindow
}

// userStoppedAfter reports whether a deliberate stop happened strictly after t
// (absolute, so a long wait cannot expire it out of the rolling window).
func (s *Server) userStoppedAfter(t time.Time) bool {
	s.lastUserStopMu.Lock()
	defer s.lastUserStopMu.Unlock()
	return !s.lastUserStop.IsZero() && s.lastUserStop.After(t)
}

// standbyBounceWakeWindow is how long after HandleEnterStandby cleared the
// transport (a power-off STR saw as UPNP->STANDBY) a following DO_NOT_RESUME
// "wake" is treated as the scm power-off BOUNCE rather than a genuine power-on.
// On the ST20 (scm) the STANDBY->UPNP restore arrives within ~200 ms of the
// power-off and ResumeLastPlay then settles for 2 s before it decides, so the
// window must comfortably exceed that settle. See standbyStoppedRecently.
const standbyBounceWakeWindow = 6 * time.Second

// standbyStoppedRecently reports whether STR saw this box's UPnP source drop to
// STANDBY (a power-off) within standbyBounceWakeWindow. ResumeLastPlay, maybeRePush
// and RecoverAfterReconnect use it to stand down on the scm power-off bounce (a
// DO_NOT_RESUME / reconnect / stream-drop that follows a power-OFF) instead of
// re-waking a box the user just switched off.
func (s *Server) standbyStoppedRecently() bool {
	s.standbyStopMu.Lock()
	defer s.standbyStopMu.Unlock()
	return !s.lastStandbyStop.IsZero() && time.Since(s.lastStandbyStop) < standbyBounceWakeWindow
}

// RecentlyPoweredOff reports whether STR saw this box's UPnP source drop to
// STANDBY within the bounce window. Exported for the agent's hardware-preset
// recall verify (cmd/agent), which must abort its re-push retries when the user
// powered the box off mid-recall instead of treating standby as "not playing yet"
// and re-pushing the stream (#197).
func (s *Server) RecentlyPoweredOff() bool { return s.standbyStoppedRecently() }

// noteStandbyStop arms the power-off suppression seen on a UPNP->STANDBY drop: it
// refreshes lastStandbyStop (the #197 standbyStoppedRecently window) and records a
// user-stop, independent of whether the caller then clears the transport. This
// decoupling is deliberate: the zone / disabled guards in HandleEnterStandby
// govern only the transport-clear, but the suppression must stay armed for all
// three wake paths (ResumeLastPlay, maybeRePush, RecoverAfterReconnect) regardless.
func (s *Server) noteStandbyStop() {
	s.standbyStopMu.Lock()
	s.lastStandbyStop = time.Now()
	s.standbyStopMu.Unlock()
	s.NoteUserStop()
}

// standbyStopDebounce bounds the resume-suppression burst detection for the rapid
// UPNP<->STANDBY oscillation a power-off produces on some ST20 (scm) firmware.
const standbyStopDebounce = 4 * time.Second

// standbyClearMinGap rate-limits the transport clear during a power-off bounce so
// the ~170 ms UPNP<->STANDBY oscillation re-clears the transport a few times
// (covering a clear that lost the race) without flooding the box with SOAP calls.
const standbyClearMinGap = 500 * time.Millisecond

// standbyBounceFixEnabled gates the #197 mitigation. Default on; set
// STR_STANDBY_STOP=0 on the box to disable it if it ever regresses, without an
// OTA (run.sh exports the agent's environment).
func standbyBounceFixEnabled() bool {
	return os.Getenv("STR_STANDBY_STOP") != "0"
}

// spontaneousResumeEnabled gates the #419 spontaneous-power-off recovery.
// Default on; set STR_SPONT_RESUME=0 on the box to disable it without an OTA.
func spontaneousResumeEnabled() bool {
	return os.Getenv("STR_SPONT_RESUME") != "0"
}

// Discriminator windows for HandleEnterStandby (#419).
const (
	// userPlayGuardWindow is how long after a user-initiated play a
	// UPNP->STANDBY flip is still considered part of that recall settling
	// (the hardware verify retries for ~30s; the box can flip mid-verify).
	userPlayGuardWindow = 35 * time.Second
	// userPlayActivityEpsilon separates the userActivityUpdate that belongs to
	// the preset press that STARTED the recall (it can trail the
	// nowSelectionUpdated by a moment) from a genuinely NEW key press during
	// the recall (e.g. the user pressing power to stop it, the original #197).
	userPlayActivityEpsilon = 2 * time.Second
	// ownPushFlipWindow: a UPNP->STANDBY flip this soon after one of STR's OWN
	// transport commands (SetURI/Play of a wake-resume or recall) is the
	// firmware answering that push, not a user power-off - provided no key
	// press arrived after the push.
	ownPushFlipWindow = 3 * time.Second
	// powerKeyAdjacencyWindow: for a key press during a recall to count as the
	// user powering the box off, the UPNP->STANDBY flip must follow the key
	// within this window (a power press flips the source near-instantly). A key
	// press that is merely SOMEWHERE in the recall window - a volume tweak, a
	// thumbs key - used to reclassify a later firmware flap as a user power-off,
	// which cleared the transport and latched every recovery off, so "press
	// preset, touch volume, box dies" (#252: the ST20 switching itself off).
	powerKeyAdjacencyWindow = 3 * time.Second
	// spontaneousOffWindow: a UPNP->STANDBY drop with no physical key press
	// this recent is the firmware powering the source off on its own (#419:
	// observed correlating with Wi-Fi instability, 40 min into stable playback).
	spontaneousOffWindow = 12 * time.Second
	// loginGiveupStandbyWindow: a UPNP->STANDBY drop this soon after the box
	// refused a source as NOT_LOGGED_IN (1036) is the box giving up on its own
	// failed self-activation (it flaps INVALID_SOURCE -> STANDBY ~4s after the
	// press), NOT a user power-off. That drop lands in a dead zone - a hardware
	// press never stamps lastUserPlayStart so recallActive is false, and 4s is
	// inside spontaneousOffWindow - so it fell through to the #197 power-off
	// clear, which stood the recall-verify down before the forced re-login could
	// land: the first hardware press after a standby-cleared login died silently
	// (field: Portable 2026-07-23). Kept tight so a real power press that happens
	// to follow a login error is still handled conservatively.
	loginGiveupStandbyWindow = 6 * time.Second
	// spontResumeMinGap single-flights the spontaneous recovery.
	spontResumeMinGap = 30 * time.Second
	// spontResumeStreamWindow: the recovery only fires when the box was
	// actively pulling a stream this recently, so STR only ever resumes music
	// the drop actually interrupted, never long-idle boxes.
	spontResumeStreamWindow = 60 * time.Second
	// clockJumpAgeFloor: an "age" beyond this cannot be a real gap between a
	// stream fetch and a source drop within one agent run; it means the wall
	// clock moved under us (no battery RTC: these boxes boot in 2015 until
	// NTP lands). Used only to log the honest reason.
	clockJumpAgeFloor = 24 * time.Hour
)

// recallOwnsRetryWindow is how long after a user-started play the recall's own
// verify owns the retry, so maybeRePush does not push on top of it. It spans
// the press, the wrong-state re-push and the first verify ticks, which is where
// the two used to collide; after it the verify's own pacing is 5 s apart, far
// from a storm.
const recallOwnsRetryWindow = 12 * time.Second

// userPlayedRecently reports whether the user started a play (hardware preset
// press or app play) within recallOwnsRetryWindow. A stream drop inside that
// window is almost always the PREVIOUS stream being torn down by the new
// selection, not a box-side dropout worth resuming.
func (s *Server) userPlayedRecently() bool {
	s.standbyStopMu.Lock()
	defer s.standbyStopMu.Unlock()
	return !s.lastUserPlayStart.IsZero() && time.Since(s.lastUserPlayStart) < recallOwnsRetryWindow
}

// SetUserActivityFn wires boxws.LastUserActivity so HandleEnterStandby can tell
// a physical power-off from a spontaneous firmware source power-off (#419).
func (s *Server) SetUserActivityFn(fn func() time.Time) {
	s.userActivityFn = fn
}

// SetStorm1036Fn wires boxws.Storm1036 so the version envelope can report an
// ongoing "the box refuses every recall" state to the app.
func (s *Server) SetStorm1036Fn(fn func() (bool, int, time.Time)) {
	s.storm1036Fn = fn
}

// SetOwnTransportCmdFn wires boxws.LastOwnTransportCommand so HandleEnterStandby
// can recognise a source flip that answers STR's OWN transport push (the
// firmware rejecting a wake-resume or recall SetURI) instead of classifying it
// as a user power-off. Field signature: power-on key press, STR's resume push
// ~2s later, flip 200ms after the push - the key press satisfied the adjacency
// check and the flip latched "user stopped deliberately", so the resume the
// user asked for was suppressed.
func (s *Server) SetOwnTransportCmdFn(fn func() time.Time) {
	s.ownTransportCmdFn = fn
}

// NoteUserPlay records that the user explicitly asked for playback (hardware
// preset press or app play). It clears the deliberate-stop latches: the press
// is newer intent than any earlier stop, so a stand-down armed by a previous
// power-off must not suppress the recall the user just asked for (#419: after
// a source bounce every preset press died against the stale latch until a
// power pull). It also stamps lastUserPlayStart for HandleEnterStandby's
// mid-recall discriminator.
func (s *Server) NoteUserPlay() {
	s.standbyStopMu.Lock()
	s.lastUserPlayStart = time.Now()
	s.lastStandbyStop = time.Time{}
	s.standbyStopMu.Unlock()
	s.ClearUserStop()
}

// HandleEnterStandby reacts to the box's own UPnP source dropping to STANDBY
// (a power-off), seen over gabbo. On some ST20 (scm) firmware the box then
// oscillates STANDBY->UPNP and switches itself back on because STR's UPnP
// transport still has a URI loaded and is treated as the active source, so the
// speaker "cannot be switched off" until several presses (#197, confirmed in a
// diagnostic: UPNP->STANDBY->UPNP within ~170 ms, repeated). STR clears the
// transport so the firmware has nothing to bounce back to.
//
// Conservative by design: it only runs when STR's own source (UPNP) was active,
// never when the box is in a zone (a mirror/slave legitimately re-selects UPNP),
// and is debounced so the flip does not issue a burst of Stops. Stopping a box
// the user just powered off matches their intent, so the blast radius is small.
func (s *Server) HandleEnterStandby() {
	if !standbyBounceFixEnabled() || s.renderer == nil {
		return
	}

	// Classify the drop before treating it as a user power-off (#419). Two
	// non-user cases must NOT arm the deliberate-stop latches, which suppress
	// every recovery path (wake resume, auto-re-push, recall verify) and left
	// both of the reporter's boxes silent until a power pull:
	//
	//  1. The flip lands moments after the user explicitly asked for playback
	//     and no NEW key was pressed since: the box is settling/oscillating
	//     during STR's own recall. Latching here killed the very recall the
	//     user just requested, and clearing the transport yanked the URI out
	//     from under the in-flight verify retries (1036 wrong-state loop).
	//  2. The flip arrives with no physical key press anywhere near it: the
	//     firmware powered STR's source off on its own (observed correlating
	//     with Wi-Fi instability). That is a box-side drop, not a user stop,
	//     so STR may recover the stream instead of standing down.
	//
	// A firmware that never emits userActivityUpdate yields a zero lastKey and
	// falls through to the conservative power-off handling, so #197 behaviour
	// is unchanged wherever the discriminator has no signal.
	now := time.Now()
	s.standbyStopMu.Lock()
	playStart := s.lastUserPlayStart
	s.standbyStopMu.Unlock()
	var lastKey time.Time
	if s.userActivityFn != nil {
		lastKey = s.userActivityFn()
	}
	// A deliberate stop STR itself initiated (app/phone-remote Stop, Pause or
	// Standby call NoteUserStop) produces no gabbo key frame, so the activity
	// discriminator alone would misread the resulting source drop as
	// spontaneous and wake the box right back up. A fresh user-stop always
	// means deliberate: keep the conservative handling.
	deliberateStop := s.userStoppedRecently()
	// A standby right after a NOT_LOGGED_IN rejection is the box giving up on its
	// own failed source self-activation, not a user power-off (see
	// loginGiveupStandbyWindow). Latching the deliberate-stop signals and clearing
	// the transport here stood the recall-verify loop down before the forced
	// re-login landed, so the first hardware press after a standby-cleared login
	// played nothing (Portable 2026-07-23). Leave the transport and latches alone
	// so the verify loop can wake the box and re-push once the re-login completes.
	// A real user power-off carries no recent 1036, so it is unaffected.
	if !deliberateStop && s.loginErrorRecentWithin(loginGiveupStandbyWindow) {
		s.logger.Info("standby bounce: box dropped to standby right after a NOT_LOGGED_IN rejection, treating as a login give-up, not a user power-off (recovery stays armed)")
		return
	}
	recallActive := !playStart.IsZero() && now.Sub(playStart) < userPlayGuardWindow
	// A key press only reads as "the user powered the box off mid-recall" when
	// it is BOTH newer than the press that started the recall (outside the
	// trailing-frame epsilon) AND immediately adjacent to the flip - a power
	// press flips the source within a moment. Requiring only "any key since
	// recall start" made a volume tweak seconds earlier reclassify a routine
	// firmware flap as a power-off, clearing the transport and latching every
	// recovery off mid-recall (#252).
	newKeySinceRecall := lastKey.After(playStart.Add(userPlayActivityEpsilon))
	keyAdjacentToFlip := !lastKey.IsZero() && now.Sub(lastKey) <= powerKeyAdjacencyWindow
	// A flip right after STR's OWN transport push, with no key press SINCE that
	// push, is the firmware rejecting/settling OUR command - not a user
	// power-off, even when a key press sits just before the push (field case:
	// power-ON key, STR's wake-resume push 2s later, flip 200ms after the
	// push; the key satisfied the adjacency check and the flip latched "user
	// stopped deliberately", suppressing the very resume the key asked for).
	// A real power-off is unaffected: its key press comes AFTER our last push.
	//
	// The excusal requires an actual key signal (a nonzero lastKey): on
	// firmware that never emits userActivityUpdate the "push after key"
	// comparison is vacuously true for EVERY push, and STR pushes on a ~5s
	// cadence while a recall struggles - so a genuine power press within 3s of
	// any push would be excused, nothing would latch, and the verify's wake
	// would power the box back on (#197). With no key signal we keep the
	// documented conservative fall-through above; the field case that
	// motivated the excusal had a real key press and still passes.
	var lastOwnCmd time.Time
	if s.ownTransportCmdFn != nil {
		lastOwnCmd = s.ownTransportCmdFn()
	}
	ownPushAnswered := !lastOwnCmd.IsZero() && now.Sub(lastOwnCmd) <= ownPushFlipWindow &&
		!lastKey.IsZero() && lastOwnCmd.After(lastKey)
	if ownPushAnswered && !deliberateStop {
		s.logger.Info("standby bounce: source flipped right after STR's own transport push, treating as the firmware answering the push, not a user power-off")
		return
	}
	if recallActive && (!newKeySinceRecall || !keyAdjacentToFlip) && !deliberateStop {
		// Feed the refusal accounting: this drop is NOT a user power-off, so
		// an exhausted verify seeing STANDBY must not excuse itself with "the
		// user switched it off" (silent-refusal family, see wedge.go).
		s.NoteNonUserStandbyDrop()
		s.logger.Info("standby bounce: source dropped during the user's own recall with no adjacent new key press, leaving transport and latches alone (#419)")
		return
	}
	if !recallActive && !deliberateStop && !lastKey.IsZero() && now.Sub(lastKey) > spontaneousOffWindow {
		s.handleSpontaneousSourceOff(now.Sub(lastKey))
		return
	}

	// Arm the suppression signal on EVERY observed power-off, BEFORE the zone guard
	// that only governs the transport-clear. The stamp (read by ResumeLastPlay's
	// standbyStoppedRecently #197 guard) and the user-stop (read by maybeRePush /
	// RecoverAfterReconnect) must stay armed even when we go on to skip the clear
	// (a zoned box), or all three wake paths would be free to re-wake a box the
	// user just powered off. Refreshed on every flip so a rapid second press keeps
	// the window alive (the reporter's "needs multiple presses").
	s.noteStandbyStop()

	if s.boxInZone() {
		return // a zone slave/master mirror re-selects UPNP on purpose; leave its transport
	}

	// Re-issue the clear on each flip of the oscillation, not just the first: a
	// single Stop loses the ~170 ms STANDBY->UPNP race and the firmware re-selects
	// STR's still-loaded URI. A short min-gap keeps a fast flap from flooding the
	// box with SOAP calls.
	s.standbyStopMu.Lock()
	doClear := s.lastStandbyClear.IsZero() || time.Since(s.lastStandbyClear) >= standbyClearMinGap
	if doClear {
		s.lastStandbyClear = time.Now()
	}
	s.standbyStopMu.Unlock()
	if !doClear {
		return
	}

	// A power-off is a deliberate stop: drop any queue so it does not fight the
	// standby, then Stop and EMPTY the transport URI so the firmware has nothing to
	// bounce back to (Stop alone leaves the URI loaded and the box re-selects it).
	// The SOAP calls run in a goroutine: HandleEnterStandby is invoked
	// synchronously from the gabbo read loop, and up to ~5s of SOAP blocked
	// every queued frame - which is exactly what skewed the processing-time
	// stamps the teardown classification depends on (#252). The min-gap above
	// already bounds how many of these can be in flight.
	//
	// The goroutine re-checks user intent before EACH SOAP call: moving the
	// clear off the read loop also un-serialized it from the next queued frame,
	// so a preset press right after the power-off (the routine power-on-via-
	// preset flow) could have its fresh SetURI overtaken by this straggler's
	// ClearURI - a Stop can block for seconds against a box that is shutting
	// down, and the ClearURI then wiped the transport the new recall had just
	// armed. A user play newer than this clear means the clear no longer
	// represents current intent, so it stands down.
	s.stopQueue()
	s.logger.Info("standby bounce: box powered off STR's UPnP source, stopping + clearing the transport URI so it stays off (#197)")
	clearArmedAt := time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(s.queueCtx(), 5*time.Second)
		defer cancel()
		staleClear := func() bool {
			s.standbyStopMu.Lock()
			defer s.standbyStopMu.Unlock()
			return s.lastUserPlayStart.After(clearArmedAt)
		}
		if staleClear() {
			s.logger.Info("standby bounce: a newer user play superseded the transport clear, leaving the fresh recall alone")
			return
		}
		if err := s.renderer.Stop(ctx); err != nil {
			s.logger.Debug("standby bounce: transport stop returned (expected if already off)", "err", err)
		}
		if staleClear() {
			s.logger.Info("standby bounce: a newer user play superseded the transport clear, leaving the fresh recall alone")
			return
		}
		// Empty the transport URI so the firmware has nothing to bounce back to
		// after a deliberate power-off (#197: an ST20-scm re-selected its
		// still-loaded URI and woke itself back on). NOTE: this clear is NOT the
		// cause of the dead preset-after-standby 1036 - live-tested 2026-07-23 on
		// the Portable, leaving the URI loaded did not stop the hardware press
		// from provoking the box's own login-gated UPNP activation, which the
		// firmware still refused 1036 UpnpRcvdContentItemInWrongState. The 1036 is
		// inherent to the box's cloud-gated source activation and independent of
		// STR's transport state; the recovery band-aid (login give-up standby
		// discriminator) is what carries the press back to audio.
		if err := s.renderer.ClearURI(ctx); err != nil {
			s.logger.Debug("standby bounce: transport clear returned (expected if already off)", "err", err)
		}
	}()
}

// handleSpontaneousSourceOff handles a UPNP->STANDBY drop with no physical key
// press anywhere near it: the firmware powered STR's source off on its own
// (#419, observed on ST10+ST30 correlating with Wi-Fi/router instability, e.g.
// 40 min into stable playback). It deliberately does NOT arm the user-stop
// latches (nobody stopped anything), and when the drop interrupted an actively
// playing stream it wakes the box and re-pushes it, so the music survives the
// firmware hiccup instead of staying dead until a power pull.
func (s *Server) handleSpontaneousSourceOff(sinceKey time.Duration) {
	s.logger.Info("standby bounce: firmware powered off STR's source with no user input, not treating as a user stop (#419)",
		"lastKeyAgoS", int(sinceKey.Seconds()))
	if !spontaneousResumeEnabled() {
		s.logger.Info("spontaneous-off recovery: disabled by setting, standing down")
		return
	}
	// Only recover music the drop actually interrupted: the stream proxy must
	// have served the box this recently. A long-idle box that drifts to standby
	// on its own is left alone.
	if s.streamActivityFn == nil {
		s.logger.Info("spontaneous-off recovery: no stream-activity signal wired, standing down")
		return
	}
	fetch, _ := s.streamActivityFn()
	if age := time.Since(fetch); fetch.IsZero() || age > spontResumeStreamWindow {
		// An age far beyond any plausible uptime means the stamp was taken
		// before the box corrected its clock, not that the stream is old:
		// these speakers have no battery RTC and boot in 2015 until NTP
		// lands, which produced a "did not serve recently" stand-down with
		// lastFetchAgoS of about twenty years (field: ST10, 2026-08-02).
		// The verdict stays the same - a stand-down never wakes a box, and
		// deep standby is sacred - but the log must not blame the stream for
		// what is a clock jump, or the next bundle gets read wrong.
		if age > clockJumpAgeFloor {
			s.logger.Info("spontaneous-off recovery: stream-activity stamp predates a clock correction, standing down (not a stale stream)",
				"lastFetchAgoS", int(age.Seconds()))
			return
		}
		s.logger.Info("spontaneous-off recovery: the stream proxy did not serve this box recently, standing down",
			"lastFetchAgoS", int(age.Seconds()), "windowS", int(spontResumeStreamWindow.Seconds()))
		return
	}
	// Single-flight + crash-loop guard. The #381 attempt counter caps a
	// firmware that re-drops the source on every recovery at
	// maxAutoResumeAttempts per window instead of a wake/drop tug-of-war.
	s.standbyStopMu.Lock()
	tooSoon := !s.lastSpontResume.IsZero() && time.Since(s.lastSpontResume) < spontResumeMinGap
	if !tooSoon {
		s.lastSpontResume = time.Now()
	}
	s.standbyStopMu.Unlock()
	if tooSoon || s.autoResumeBlocked() {
		s.logger.Info("spontaneous-off recovery: standing down",
			"reason", map[bool]string{true: "another recovery ran just now", false: "auto-resume blocked (crash-loop guard or opt-out)"}[tooSoon])
		return
	}
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	var boxURL, title, art, mime string
	var capturedTS time.Time
	if lp != nil {
		boxURL, title, art, mime, capturedTS = lp.boxURL, lp.title, lp.art, lp.mime, lp.ts
	}
	s.lastPlayMu.Unlock()
	if boxURL == "" {
		s.logger.Info("spontaneous-off recovery: no last-play to restore, standing down")
		return
	}
	go func() {
		// Let the flip settle; a genuine user power-off that the activity
		// discriminator somehow missed reaches stable STANDBY here and the
		// user-stop latch check below (armed by a STOP_STATE on some models)
		// still gets a chance to hold it.
		time.Sleep(3 * time.Second)
		if s.userStoppedRecently() || s.standbyStoppedRecently() {
			s.logger.Info("spontaneous-off recovery: a deliberate stop arrived while settling, standing down")
			return
		}
		if s.boxInZone() {
			s.logger.Info("spontaneous-off recovery: box is in a zone, standing down")
			return
		}
		s.noteAutoResumeAttempt()
		// NEVER power a box on from an automatic path (#487 field capture,
		// 2026-07-27): the Wave emits no userActivityUpdate for its power key,
		// so a real user power-off is indistinguishable here from a firmware
		// drop, and the wake that used to sit at this line switched the
		// speaker back on 6 s after its owner had switched it off. A box that
		// is genuinely still in STANDBY is therefore left alone, and the
		// resume is DEFERRED: armDeferredResume hands it to the standby-exit
		// path, so the stream comes back the moment the user turns the box on
		// themselves. The #419 case this function exists for is unaffected -
		// there the firmware drops the SOURCE while the box stays awake, and
		// the re-push below still runs immediately.
		// An unknown source (probe failed) counts as "possibly off" and defers
		// too: pushing a transport into a box that is actually off can switch
		// scm firmware back on, which is the same harm by another route.
		if src := s.boxSourceNow(); src == "STANDBY" || src == "" {
			s.armDeferredResume(boxURL, title, art, mime, capturedTS)
			s.logger.Info("spontaneous-off recovery: box is in standby, NOT powering it on; resume deferred to the next user wake",
				"source", src, "url", boxURL)
			return
		}
		s.boxCmdMu.Lock()
		defer s.boxCmdMu.Unlock()
		// A newer user play may have raced this recovery; never clobber it (#252).
		s.lastPlayMu.Lock()
		cur := s.lastPlay
		s.lastPlayMu.Unlock()
		if resumeIsStale(boxURL, capturedTS, cur) {
			s.logger.Info("spontaneous-off recovery: a newer play started while settling, standing down")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var err error
		if mime != "" {
			err = s.renderer.PlayURLMime(ctx, boxURL, title, art, mime)
		} else {
			err = s.renderer.PlayURL(ctx, boxURL, title, art)
		}
		if err != nil {
			s.logger.Warn("spontaneous-off recovery: re-push failed", "err", err, "url", boxURL)
			return
		}
		s.logger.Info("spontaneous-off recovery: resumed the stream the firmware dropped (#419)", "url", boxURL, "title", title)
	}()
}

// recallBoxInert reports the one state a power nudge is right for: the box says
// INVALID_SOURCE, i.e. it has no source selected at all, and the stream proxy
// has served nothing since this recall began. Anything else, including a box
// that is merely slow or genuinely in STANDBY, must be left alone.
func (s *Server) recallBoxInert(started time.Time) bool {
	if s.boxSourceNow() != "INVALID_SOURCE" {
		return false
	}
	if s.streamActivityFn == nil {
		return false // no evidence available: never nudge on a guess
	}
	fetch, _ := s.streamActivityFn()
	return fetch.Before(started)
}

// nudgeDeadSource sends a single `sys power` toggle to shake a box out of the
// dead-source state. Bounded and fire-and-forget: the caller re-pushes right
// after, and a nudge that does nothing costs one CLI call.
func (s *Server) nudgeDeadSource() {
	if s.boxHost == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := boxcli.PowerOn(ctx, s.boxHost); err != nil {
		s.logger.Warn("recall verify: power nudge failed", "err", err)
	}
}

// boxSourceNow reads the box's current source attribute ("" on any error).
// Used by the spontaneous-off recovery to tell "the firmware dropped the
// source while the box stayed awake" (recoverable right now) from "the box is
// off" (must never be powered on automatically, #487).
func (s *Server) boxSourceNow() string {
	if s.boxSourceFn != nil {
		return s.boxSourceFn()
	}
	if s.boxHost == "" {
		return ""
	}
	cl := &http.Client{Timeout: 4 * time.Second}
	resp, err := cl.Get("http://" + s.boxHost + ":8090/now_playing")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if m := regexp.MustCompile(`source="([^"]*)"`).FindSubmatch(b); len(m) == 2 {
		return string(m[1])
	}
	return ""
}

// deferredResume is a stream the firmware dropped while the box was (or went)
// off. STR refuses to power a speaker on by itself, so the recovery waits here
// until the user switches the box on and the gabbo standby-exit arrives.
type deferredResume struct {
	boxURL, title, art, mime string
	capturedTS               time.Time
	armedAt                  time.Time
}

// deferredResumeTTL bounds how long a deferred resume stays armed. Long enough
// to cover "switched off in the evening, on again next morning" without
// resurrecting a stream from days ago.
const deferredResumeTTL = 18 * time.Hour

// armDeferredResume stores the resume for the next user wake. A newer arm
// replaces an older one: the most recent interrupted stream is what the user
// expects back.
func (s *Server) armDeferredResume(boxURL, title, art, mime string, capturedTS time.Time) {
	s.deferredMu.Lock()
	s.deferred = &deferredResume{boxURL: boxURL, title: title, art: art, mime: mime, capturedTS: capturedTS, armedAt: time.Now()}
	s.deferredMu.Unlock()
}

// RunDeferredResume replays a stream armed by armDeferredResume, if any. Wired
// to the gabbo standby-exit (the box just left STANDBY, i.e. the user turned it
// on), so the music the firmware dropped comes back on the user's own action
// instead of STR waking the speaker. All the usual guards still apply: a
// deliberate stop, a zone, a newer play, an opt-out or an expired arm cancel it.
func (s *Server) RunDeferredResume() {
	s.deferredMu.Lock()
	d := s.deferred
	s.deferred = nil
	s.deferredMu.Unlock()
	if d == nil {
		return
	}
	if time.Since(d.armedAt) > deferredResumeTTL {
		s.logger.Info("deferred resume: too old, dropping", "ageMin", int(time.Since(d.armedAt).Minutes()))
		return
	}
	if !spontaneousResumeEnabled() || s.autoResumeBlocked() {
		s.logger.Info("deferred resume: auto-resume disabled or blocked, standing down")
		return
	}
	if s.userStoppedRecently() || s.standbyStoppedRecently() || s.boxInZone() {
		s.logger.Info("deferred resume: a deliberate stop or a zone is in play, standing down")
		return
	}
	s.lastPlayMu.Lock()
	cur := s.lastPlay
	s.lastPlayMu.Unlock()
	if resumeIsStale(d.boxURL, d.capturedTS, cur) {
		s.logger.Info("deferred resume: a newer play took over, standing down")
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Ledger: the one autonomous UPnP push the agent performs (all other
	// pushes are user actions). Source is by definition just-out-of-standby.
	boxwrites.Note("upnp-resume", "standby-exit")
	var err error
	if d.mime != "" {
		err = s.renderer.PlayURLMime(ctx, d.boxURL, d.title, d.art, d.mime)
	} else {
		err = s.renderer.PlayURL(ctx, d.boxURL, d.title, d.art)
	}
	if err != nil {
		s.logger.Warn("deferred resume: re-push failed", "err", err, "url", d.boxURL)
		return
	}
	s.logger.Info("deferred resume: restored the stream the firmware had dropped, on the user's own wake (#487)",
		"url", d.boxURL, "title", d.title)
}

// HandleStreamDisconnect is called by the stream proxy when the Bose renderer
// closes a stream. When the upstream was healthy (so the box dropped it, not
// the source) it conservatively tries to resume, which fixes the renderer
// dropping a long stream on its own (radio stops after ~11 min, no STR error).
func (s *Server) HandleStreamDisconnect(upstreamErr error) {
	if upstreamErr != nil {
		return // upstream failed; not a box-side drop, leave it alone
	}
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	// Coalesce. Skip when there is no recent stream, the stream was already
	// declared dead, or a resume is already in flight. A dead/moved URL fires a
	// disconnect on EVERY failed resume; without this latch each one spawned a
	// fresh maybeRePush goroutine, producing the dozens-per-second runaway that
	// starved :8888 (v0.7.5). The latch is Server-level so it survives
	// setLastPlay swapping the lastPlayInfo struct mid-wait.
	if lp == nil || time.Since(lp.ts) >= 6*time.Hour || lp.failed || s.rePushInFlight {
		s.lastPlayMu.Unlock()
		return
	}
	s.rePushInFlight = true
	s.lastPlayMu.Unlock()
	go s.maybeRePush()
}

// maybeRePush resumes the last stream, but only when it is safe: it waits a
// moment (a user power-off reaches STANDBY within ~1-2 s, so this tells "user
// turned it off" from "renderer dropped the stream while the box stays on"),
// then re-pushes only if the box is on and idle (not standby, not playing, not
// paused). A windowed counter caps retries so a genuinely failing stream is not
// looped forever.
func (s *Server) maybeRePush() {
	// Release the in-flight latch on exit so a later genuine drop can re-arm
	// (HandleStreamDisconnect refuses to spawn a second goroutine until then).
	// The latch is a Server field, so this release cannot land on the wrong
	// struct after setLastPlay swapped lastPlay mid-run.
	defer func() {
		s.lastPlayMu.Lock()
		s.rePushInFlight = false
		s.lastPlayMu.Unlock()
	}()

	// Exponential backoff keyed on how many attempts this stream already took:
	// 2s, 2s, 4s, 8s, 16s (capped at 30s). A dead/moved URL drops the moment it
	// is re-pushed, so without this the resume loop spun dozens of times per
	// second; the backoff spaces the (few) attempts out instead. The first wait
	// also serves the original purpose of telling a user power-off (box reaches
	// STANDBY in ~1-2s) from a renderer drop while the box stays on.
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	if lp == nil {
		s.lastPlayMu.Unlock()
		return
	}
	attempt := lp.rePushes
	s.lastPlayMu.Unlock()
	backoff := 2 * time.Second
	if attempt > 1 {
		backoff = time.Duration(1<<uint(attempt)) * time.Second
	}
	if backoff > 30*time.Second {
		backoff = 30 * time.Second
	}
	time.Sleep(backoff)

	// A deliberate user stop (STR Stop/Pause, or the box/remote stop button seen
	// over gabbo) must hold. Genuine box-side drops carry no such stop and resume.
	if s.userStoppedRecently() {
		s.logger.Info("re-push: user stopped deliberately, not resuming")
		return
	}
	// A power-off STR saw as UPNP->STANDBY (HandleEnterStandby) must also hold the
	// re-push: on the scm bounce the box is flipping STANDBY<->UPNP and a re-push
	// here would hand the firmware a fresh URI to switch back on with (#197).
	if s.standbyStoppedRecently() {
		s.logger.Info("re-push: box just powered off (standby bounce), not resuming (#197)")
		return
	}
	if s.userPlayedRecently() {
		// A recall the user just started owns its own retry: its verify
		// re-pushes every 5 s until the box really plays that stream.
		// Re-pushing here as well makes the two fight, and each push tears the
		// other's stream down, which arms yet another disconnect. A Wave
		// diagnostic caught the resulting storm: four stream starts and two
		// re-pushes inside 1.7 s, because the drop being "recovered" was just
		// the PREVIOUS preset being torn down by the new press.
		//
		// But the verify is only alive for so long. A stream drop 5-12s after
		// a user play used to be orphaned: the verify had already exited on
		// its first success, this path returned permanently, and the music
		// stayed off with a clean log (field bundle: slot-6 drop at +5.5s,
		// box off, zero recovery). Wait out the remainder of the window and
		// re-evaluate. A NEWER play started while waiting hands ownership to
		// that recall's verify; a stop or power-off that arrived while
		// waiting is compared against absolute stamps so it cannot expire out
		// of a rolling window during the wait.
		s.logger.Info("re-push: a preset/play the user just started owns the retry; re-checking once the ownership window ends")
		waitStart := time.Now()
		s.standbyStopMu.Lock()
		playStart := s.lastUserPlayStart
		s.standbyStopMu.Unlock()
		if remain := recallOwnsRetryWindow - time.Since(playStart) + time.Second; remain > 0 {
			time.Sleep(remain)
		}
		if s.userPlayedRecently() {
			s.logger.Info("re-push: a newer play started while waiting, standing down")
			return
		}
		if s.userStoppedAfter(waitStart) {
			s.logger.Info("re-push: user stopped while waiting out the ownership window, not resuming")
			return
		}
		if s.StandbyStoppedAfter(waitStart) {
			s.logger.Info("re-push: box powered off while waiting out the ownership window, not resuming (#197)")
			return
		}
	}
	standby, busy := s.boxPlayState()
	if standby {
		s.logger.Info("re-push: box went to standby, not resuming (treated as user power-off)")
		return
	}
	if busy {
		// Recovered (playing/paused again, or the user switched). Reset the
		// attempt counter so a later genuine drop starts a fresh backoff window.
		s.lastPlayMu.Lock()
		if s.lastPlay != nil {
			s.lastPlay.rePushes = 0
		}
		s.lastPlayMu.Unlock()
		return
	}

	s.lastPlayMu.Lock()
	lp = s.lastPlay
	if lp == nil {
		s.lastPlayMu.Unlock()
		return
	}
	if lp.rePushes >= maxRePushes {
		// Hard stop. The stream keeps dropping (a dead/moved radio-browser URL,
		// 503, etc.). Mark it dead so no further disconnect re-arms it; only a
		// fresh play (setLastPlay) clears this. This is the fix for the runaway
		// that re-armed forever and starved the control port.
		lp.failed = true
		url := lp.boxURL
		s.lastPlayMu.Unlock()
		s.logger.Warn("re-push: stream keeps dropping, giving up for good (likely a dead/moved URL); not retrying until a new play",
			"url", url, "attempts", maxRePushes)
		return
	}
	lp.rePushes++
	boxURL, title, art, mime, n := lp.boxURL, lp.title, lp.art, lp.mime, lp.rePushes
	s.lastPlayMu.Unlock()

	s.logger.Info("re-push: box dropped the stream while idle, resuming", "url", boxURL, "attempt", n, "max", maxRePushes)
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	// Re-check under the command mutex, mirroring the spontaneous-off path: a
	// verify push or app play that held boxCmdMu while we waited may have
	// started the box, and re-pushing over it would re-buffer a stream that is
	// already playing.
	if _, busy := s.boxPlayState(); busy {
		s.logger.Info("re-push: box started playing while waiting for the command slot, standing down")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var err error
	if mime != "" {
		err = s.renderer.PlayURLMime(ctx, boxURL, title, art, mime)
	} else {
		err = s.renderer.PlayURL(ctx, boxURL, title, art)
	}
	if err != nil {
		s.logger.Warn("re-push failed", "err", err, "url", boxURL)
	}
}

// icyDisplayPushEnabled reports whether STR should push live ICY StreamTitle
// updates to the box's now-playing/display by re-issuing SetAVTransportURI
// mid-stream. Off by default: re-setting the URI can make some renderers
// re-buffer (an audible gap on every track change), so this stays behind an
// env flag until verified on the target hardware. Set STR_ICY_DISPLAY=1.
func icyDisplayPushEnabled() bool {
	return os.Getenv("STR_ICY_DISPLAY") == "1"
}

// HandleStreamTitle pushes a freshly parsed radio StreamTitle to the box so it
// appears in now-playing / on the display, by re-issuing the stream STR last
// told the box to play with the new title as DIDL metadata. The URL stays the
// stable proxy URL, only the title changes.
//
// Gated behind STR_ICY_DISPLAY: a URI re-set may cost an audio gap on some
// renderers (see icyDisplayPushEnabled), so we keep the safe default of
// surfacing the title only in the app (via /api/stream/title) until the
// mid-stream re-set is verified on real hardware. Wired from the stream proxy.
func (s *Server) HandleStreamTitle(title string) {
	// Recently-played (#135): record the live radio track under the current
	// source card, regardless of whether the on-box ICY display push is enabled
	// (this callback fires on every ICY title change either way).
	s.recentNoteRadioTrack(title)
	// Remember the live title so enabling the push / changing its mode can show
	// the CURRENT track immediately (see pushDisplayNow), not only the next one.
	if title != "" {
		s.lastPlayMu.Lock()
		s.lastICYTitle = title
		s.lastPlayMu.Unlock()
	}
	if !s.displayTrackEnabled() || s.renderer == nil || title == "" {
		return
	}
	// Rate-limit: re-buffering the box on every StreamTitle flip (song <-> promo)
	// would gap the audio constantly. Skip if we pushed within the last window.
	s.lastPlayMu.Lock()
	throttled := !s.lastDisplayPush.IsZero() && time.Since(s.lastDisplayPush) < minDisplayPushInterval
	s.lastPlayMu.Unlock()
	if throttled {
		return
	}
	s.pushDisplayTitle(title)
}

// setDisplayText re-issues the current stream's now-playing metadata with shown
// as the on-display title, keeping the stream URL / art / mime. It is the shared
// box write behind both the ICY title push and the revert-to-default path. This
// re-buffers the box (a brief audio gap), so callers gate it. Updates the
// debounce stamp. No-op if nothing is playing.
func (s *Server) setDisplayText(shown string) {
	if s.renderer == nil || shown == "" {
		return
	}
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	if lp == nil {
		s.lastPlayMu.Unlock()
		return
	}
	boxURL, art, mime := lp.boxURL, lp.art, lp.mime
	s.lastDisplayPush = time.Now()
	s.lastPlayMu.Unlock()
	if boxURL == "" {
		return
	}
	// Serialise against other box commands (re-push, play) so a title update
	// cannot interleave with a stream switch mid-SOAP.
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var err error
	if mime != "" {
		err = s.renderer.SetURIMime(ctx, boxURL, shown, art, mime)
	} else {
		err = s.renderer.SetURI(ctx, boxURL, shown, art)
	}
	if err != nil {
		s.logger.Warn("display push failed", "err", err, "shown", shown)
		return
	}
	s.logger.Info("display push", "shown", shown)
}

// pushDisplayTitle re-issues the now-playing metadata so the configured display
// text (artist / title / both, applied to rawTitle) appears on the speaker.
// Callers gate it: HandleStreamTitle debounces, the enable / mode-change path
// pushes once immediately.
func (s *Server) pushDisplayTitle(rawTitle string) {
	shown := s.displayTrackText(rawTitle)
	if shown == "" {
		return
	}
	s.setDisplayText(shown)
}

// pushDisplayNow immediately shows the current track on the speaker display,
// bypassing the debounce. Used right after the user enables the feature or
// switches the artist/title/both mode, so the display updates at once instead of
// waiting for the next song change. No-op when disabled or nothing is playing.
func (s *Server) pushDisplayNow() {
	if !s.displayTrackEnabled() {
		return
	}
	s.lastPlayMu.Lock()
	cur := s.lastICYTitle
	s.lastPlayMu.Unlock()
	if cur != "" {
		s.pushDisplayTitle(cur)
	}
}

// pushDisplayDefault reverts the speaker display to its normal text (the station
// name STR set when it started playing) right after the user turns the artist/
// title push OFF, instead of leaving the last custom text on screen until the
// next song change. Gated on the box actually playing so a SetURI never wakes an
// idle speaker; the display only carries our custom text during radio playback.
func (s *Server) pushDisplayDefault() {
	if standby, busy := s.boxPlayState(); standby || !busy {
		return
	}
	s.lastPlayMu.Lock()
	title := ""
	if s.lastPlay != nil {
		title = s.lastPlay.title
	}
	s.lastPlayMu.Unlock()
	if title != "" {
		s.setDisplayText(title)
	}
}

// boxPlayState reads now_playing and reports whether the box is in standby and
// whether it is busy (playing, buffering or paused). It FAILS CLOSED: when the
// box host is unknown or the query keeps erroring it reports standby=true, so a
// caller that would otherwise wake or re-push the box stands down when it cannot
// confirm the box is awake. A SoundTouch 10 sends no power event on a standby
// press, so the only guard against STR resuming over a deliberate standby is
// this state check; a transient /now_playing error must not be read as "awake
// and idle" and trigger a resume. One quick retry first so a single hiccup does
// not abort a legitimate stream recovery (where the box really is awake+idle).
func (s *Server) boxPlayState() (standby, busy bool) {
	if s.playStateFn != nil {
		return s.playStateFn()
	}
	if s.boxHost == "" {
		return true, false
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(400 * time.Millisecond)
		}
		resp, err := cl.Get("http://" + s.boxHost + ":8090/now_playing")
		if err != nil {
			lastErr = err
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		resp.Body.Close()
		body := string(b)
		standby = strings.Contains(body, "STANDBY")
		busy = strings.Contains(body, "PLAY_STATE") || strings.Contains(body, "BUFFERING_STATE") || strings.Contains(body, "PAUSE_STATE")
		return standby, busy
	}
	// Could not read the box state: assume standby so we never resume/wake on an
	// uncertain state (silence beats spontaneous playback).
	s.logger.Warn("box play-state query failed, assuming standby (will not resume/re-push)", "err", lastErr)
	return true, false
}
