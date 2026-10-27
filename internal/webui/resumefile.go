package webui

import (
	"context"
	"time"
)

// A paused music file that never came back (#830).
//
// Measured on an ST10 (FW 27.0.6, 2026-10-22) with a 240 s MP3 handed to the
// speaker directly, the way library tracks and file presets are played: Pause
// held the position, but Play after it did not continue. The firmware re-opens
// the URL (a new GET with "Range: bytes=<its buffer end>-"), then throws the
// answer away as belonging to a stale URL id ("urlId mismatch", "PLAYING error
// #0 for stale UrlID") and sits in PLAYING with the position frozen at the pause
// point, silent, for at least a minute. The same happens on the speaker's own
// PLAY_PAUSE key, which STR never sees as a request.
//
// It is firmware behaviour, not STR's metadata: three different HTTP servers
// and raw UPnP pushes that bypassed STR entirely showed the same freeze, with
// and without a duration and DLNA.ORG_OP=01 in the DIDL. It needs a file larger
// than the speaker's buffer (about 1 MB encoded), which is every song.
//
// What does work, measured on the same speaker: Stop, Play, and about two
// seconds later Seek(REL_TIME, paused position). The track continues from where
// it was paused and the position counts up normally. So STR does exactly that,
// in two places:
//
//   - The app's resume (resumePausedFileLocked) knows it is resuming, so it
//     does not wait for the freeze: a paused file goes straight to
//     Stop/Play/Seek. Waiting for a symptom STR can predict would only add
//     seconds of silence, and a plain Play on a paused file never worked.
//   - The speaker's own key goes past STR, so the lone-file end watch, which
//     already polls the speaker while a file plays (singletrackend.go), looks
//     for the shape: a pause, then PLAY_STATE with the position not moving.
//     It repairs once per resume.
//
// Neither touches radio, Spotify, a library folder (the queue has its own
// watcher) or anything without a known length: the restart seeks, and a seek
// is only meaningful on a finite file.

// fileResumeTiming is the file restart's clock: the pauses a test shrinks so
// a restart runs in milliseconds. Production reads productionFileResumeTiming;
// a test installs its own copy on the server (Server.fileResumeTiming), so no
// test ever writes a package-level knob that a restart or an end watch of
// another test still reads.
type fileResumeTiming struct {
	// seekDelay is how long after the Play the Seek goes out. The
	// speaker needs to have opened the file again before it takes a seek;
	// about two seconds is what worked on the ST10.
	seekDelay time.Duration
	// seekRetry is the pause before the one retry of a refused seek,
	// for a speaker that is slower to open the file than the ST10 was.
	seekRetry time.Duration
	// restartQuiet is how long the end watch ignores the speaker while STR
	// restarts the file: the Stop shows up as STOP_STATE and the Play as a
	// position near zero, and neither is the track ending.
	restartQuiet time.Duration
	// freezeAfter is how long the position may stand still in
	// PLAY_STATE after a pause before the watch calls it the freeze. The watch
	// polls every four seconds, so this is three reads in a row on the paused
	// second. Two reads (the 3 s it was) were not enough: on an ST10 the
	// speaker's own resume played on audibly while STR still read the paused
	// second, and STR restarted the file over it, a jump back of about ten
	// seconds (#830). The real freeze lasts a minute and more, so waiting a
	// little longer costs only seconds when it is real.
	freezeAfter time.Duration
	// confirmGap is the pause between the two position reads that confirm a
	// freeze on the renderer's own clock before the watch acts on it.
	confirmGap time.Duration
}

// productionFileResumeTiming is the file restart's clock on a real speaker.
var productionFileResumeTiming = fileResumeTiming{
	seekDelay:    2 * time.Second,
	seekRetry:    1500 * time.Millisecond,
	restartQuiet: 4 * time.Second,
	freezeAfter:  7 * time.Second,
	confirmGap:   1500 * time.Millisecond,
}

// pausedPointSlack is how far from the paused position a still position may
// be and still be the freeze. The freeze holds the position on the paused
// second; a position that went on past it and only then stood still is the
// speaker having resumed, never this freeze.
const pausedPointSlack = 2 * time.Second

// fileResumeTimes is the file restart's clock: the production values unless
// a test installed a faster one.
func (s *Server) fileResumeTimes() *fileResumeTiming {
	if s.fileResumeTiming != nil {
		return s.fileResumeTiming
	}
	return &productionFileResumeTiming
}

// activeDirectFile is the lone file the end watch is following, under the
// recall generation of its play.
type activeDirectFile struct {
	tr  singleTrack
	gen uint64
}

// noteActiveFile records the file a lone-file end watch follows.
func (s *Server) noteActiveFile(tr singleTrack, gen uint64) {
	s.activeFileMu.Lock()
	s.activeFile = &activeDirectFile{tr: tr, gen: gen}
	s.activeFileMu.Unlock()
}

// fileSeekable reports whether a directly played file can be restarted at a
// position: it needs a known length, which is also what made STR tell the
// speaker it is seekable.
func fileSeekable(tr singleTrack) bool {
	return isPlainHTTPURL(tr.boxURL) && (tr.meta.Seekable || tr.dur > 0)
}

// currentDirectFile returns the lone, seekable file that is the speaker's
// current play, if there is one. Anything newer (a station, a preset, a
// folder) makes it stale.
func (s *Server) currentDirectFile() (singleTrack, bool) {
	s.activeFileMu.Lock()
	af := s.activeFile
	s.activeFileMu.Unlock()
	if af == nil || af.gen != s.RecallGeneration() {
		return singleTrack{}, false
	}
	if s.queue != nil && s.queue.isActive() {
		return singleTrack{}, false
	}
	if !fileSeekable(af.tr) {
		return singleTrack{}, false
	}
	return af.tr, true
}

// fileRestarting reports whether STR is restarting a file right now, so the end
// watch must not read the transient stop as the track ending.
func (s *Server) fileRestarting() bool {
	s.activeFileMu.Lock()
	defer s.activeFileMu.Unlock()
	return !s.fileRestartUntil.IsZero() && time.Now().Before(s.fileRestartUntil)
}

func (s *Server) setFileRestartQuiet(d time.Duration) {
	s.activeFileMu.Lock()
	s.fileRestartUntil = time.Now().Add(d)
	s.activeFileMu.Unlock()
}

// pausedNotStopped reports whether the app's last stop-like request was a
// pause. A pause records a user stop too (it must hold off the auto re-push),
// but the file is still loaded and the end watch has to stay with it so it can
// see the resume.
func (s *Server) pausedNotStopped() bool {
	s.pausePosMu.Lock()
	pauseAt := s.pauseReqAt
	s.pausePosMu.Unlock()
	if pauseAt.IsZero() {
		return false
	}
	s.lastUserStopMu.Lock()
	stopAt := s.lastUserStop
	s.lastUserStopMu.Unlock()
	return !pauseAt.Before(stopAt)
}

// notePausedAt remembers a pause position the end watch read off the speaker,
// which is how a pause on the speaker's own key gets one.
func (s *Server) notePausedAt(pos time.Duration) {
	if pos <= 0 {
		return
	}
	s.pausePosMu.Lock()
	s.pausePos = pos
	s.pausePosMu.Unlock()
}

// resumePausedFileLocked is the app's resume for a paused file: Stop, Play,
// Seek back to the paused position, instead of the plain Play the firmware
// freezes on. It reports whether it handled the resume; false leaves the caller
// to its usual Play. The caller holds boxCmdMu.
func (s *Server) resumePausedFileLocked(ctx context.Context) bool {
	if s.renderer == nil {
		return false
	}
	tr, ok := s.currentDirectFile()
	if !ok {
		return false
	}
	tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	state, err := s.renderer.TransportState(tctx)
	cancel()
	if err != nil || state != "PAUSED_PLAYBACK" {
		// Not paused (left PAUSED after a timeout, or unreadable): the caller's
		// Play and its wrong-state fallback handle that, and the end watch still
		// catches a freeze.
		s.logger.Debug("resume: the file is not paused on the speaker, using the plain play",
			"title", tr.title, "transportState", state, "err", err)
		return false
	}
	// While paused the speaker still holds the exact position, so it is asked
	// first. The one recorded at the pause is the fallback: it can be older than
	// the pause if the pause came from the speaker's key between two polls.
	at := s.pausePosition()
	pctx, pcancel := context.WithTimeout(ctx, 3*time.Second)
	if rel, _, ok := s.renderer.PositionInfo(pctx); ok && rel > 0 {
		at = rel
	}
	pcancel()
	s.logger.Info("resume of a paused file: restarting it at the paused position, a plain play freezes on the speaker",
		"title", tr.title, "at", at.String())
	return s.restartFileAtLocked(ctx, tr, at, "app resume")
}

// restartFileAtLocked stops the file, plays it again and seeks to at. It
// reports whether the speaker took the play; a refused seek is not a failure,
// the track then plays from its start. The caller holds boxCmdMu.
func (s *Server) restartFileAtLocked(ctx context.Context, tr singleTrack, at time.Duration, why string) bool {
	// Detached: the app gives up on its request after a few seconds, and a
	// restart cut off between the Stop and the Play leaves silence behind.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	times := s.fileResumeTimes()
	s.setFileRestartQuiet(times.seekDelay + times.seekRetry + times.restartQuiet)
	defer s.setFileRestartQuiet(times.restartQuiet)
	// Where the track continues, for the end watch's clock and for a later
	// repair that has to put the file back.
	s.notePausedAt(at)

	if err := s.renderer.Stop(rctx); err != nil && !isWrongTransportState(err) {
		s.logger.Warn("file restart: the speaker did not take the Stop, trying the Play anyway",
			"title", tr.title, "why", why, "err", err)
	}
	if err := s.renderer.Play(rctx); err != nil {
		// The URL is gone from the transport: hand the file over again.
		s.logger.Warn("file restart: the Play after the Stop failed, handing the file over again",
			"title", tr.title, "why", why, "err", err)
		if err := s.renderer.PlayURLTrack(rctx, tr.boxURL, tr.title, tr.art, tr.mime, tr.meta); err != nil {
			s.logger.Warn("file restart: the speaker refused the file", "title", tr.title, "why", why, "err", err)
			return false
		}
	}
	if at <= 0 {
		s.logger.Info("file restart: no paused position known, playing from the start", "title", tr.title, "why", why)
		return true
	}
	time.Sleep(times.seekDelay)
	err := s.renderer.Seek(rctx, at)
	if err != nil {
		s.logger.Info("file restart: the speaker refused the first seek, trying once more",
			"title", tr.title, "why", why, "at", at.String(), "err", err)
		time.Sleep(times.seekRetry)
		err = s.renderer.Seek(rctx, at)
	}
	if err != nil {
		s.logger.Warn("file restart: the speaker refused the seek, the track plays from its start",
			"title", tr.title, "why", why, "at", at.String(), "err", err)
		return true
	}
	s.logger.Info("file restart: continued where it was paused", "title", tr.title, "why", why, "at", at.String())
	return true
}

// repairFrozenFileResume is the end watch's repair for a resume that froze. It
// re-checks under boxCmdMu that the file is still what plays and that nobody
// stopped it meanwhile.
func (s *Server) repairFrozenFileResume(tr singleTrack, gen uint64, at time.Duration) {
	// now_playing is not the last word: it trails the speaker, and a resume
	// the speaker did by itself must never be restarted over (#830: an ST10
	// jumped back ten seconds after its own resume had already played on).
	// The renderer's own clock decides. Read before boxCmdMu: it only reads.
	if moved, why := s.speakerResumedByItself(at); moved {
		s.logger.Info("resume of a file: the speaker continued by itself, no restart",
			"title", tr.title, "pausedAt", at.String(), "evidence", why)
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	if s.RecallGeneration() != gen || (s.queue != nil && s.queue.isActive()) {
		return
	}
	if s.userStoppedRecently() && !s.pausedNotStopped() {
		return
	}
	s.logger.Warn("resume of a file froze on the speaker, restarting it at the paused position",
		"title", tr.title, "at", at.String())
	s.restartFileAtLocked(s.queueCtx(), tr, at, "frozen resume")
}

// speakerResumedByItself reads the renderer's position (UPnP GetPositionInfo,
// the speaker's own clock, not the now_playing document) and reports whether
// the file is moving: already past the paused position, or further on a
// moment later. An unreadable position is no evidence either way and returns
// false, which keeps the repair: the freeze it exists for is real and long.
func (s *Server) speakerResumedByItself(at time.Duration) (bool, string) {
	first, ok := s.readRelPosition()
	if !ok {
		return false, ""
	}
	if at > 0 && first > at+pausedPointSlack {
		return true, "position " + first.String() + " is past the paused " + at.String()
	}
	time.Sleep(s.fileResumeTimes().confirmGap)
	second, ok := s.readRelPosition()
	if ok && second > first {
		return true, "position moved from " + first.String() + " to " + second.String()
	}
	return false, ""
}

// readRelPosition is the renderer's current track position; ok=false when it
// cannot be read (no renderer, no answer, a field the speaker leaves empty).
func (s *Server) readRelPosition() (time.Duration, bool) {
	if s.relPositionFn != nil {
		return s.relPositionFn()
	}
	if s.renderer == nil {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(s.queueCtx(), 3*time.Second)
	defer cancel()
	return s.renderer.RelPosition(ctx)
}

// pauseHintSince reports whether something since t says the listener paused:
// an app pause request, or a key pressed on the speaker. A pause shorter than
// the watch's poll never shows up as PAUSE_STATE, and this is what still ties
// the freeze after it to a resume.
func (s *Server) pauseHintSince(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	if s.explicitStopAfter(t) {
		return true
	}
	return s.userActivityFn != nil && s.userActivityFn().After(t)
}

// fileResumeWatch recognises a resume that froze, from what the end watch
// reads off the speaker each poll. Pure state, no clock of its own, so it can
// be driven step by step in a test.
type fileResumeWatch struct {
	pausedSeen  bool
	pausedAt    time.Duration
	prevPos     time.Duration
	havePrev    bool
	lastMoveAt  time.Time
	frozenSince time.Time
	// repaired: this resume had its one repair. Cleared once the position
	// moves again, so the next pause gets its own.
	repaired bool
	// endEpsilon is the end watch's "frozen at the end" slack (queueTiming);
	// zero means the production value, so a bare literal needs none.
	endEpsilon time.Duration
	// freezeAfter is how long a still position counts as the freeze
	// (fileResumeTiming.freezeAfter); zero means the production value, as
	// for endEpsilon.
	freezeAfter time.Duration
}

// fileResumeObs is one poll.
type fileResumeObs struct {
	status    string
	pos       time.Duration
	end       time.Duration // the track's length, 0 when unknown
	now       time.Time
	pauseHint bool // see pauseHintSince
}

// fileResumeStep is the verdict for one poll. hold tells the end watch to skip
// its end nets on this poll: a frozen position after a pause is the resume
// failing, not the track ending, and a long pause has already run the wall
// clock past the track's length.
type fileResumeStep struct {
	repair bool
	hold   bool
	at     time.Duration
}

func (w *fileResumeWatch) observe(o fileResumeObs) fileResumeStep {
	switch o.status {
	case "PAUSE_STATE":
		w.pausedSeen = true
		if o.pos > 0 {
			w.pausedAt = o.pos
			w.prevPos, w.havePrev = o.pos, true
		}
		w.frozenSince = time.Time{}
		w.repaired = false
		return fileResumeStep{}
	case "PLAY_STATE":
	default:
		// Buffering, stopped or unreadable: no verdict on this poll.
		w.frozenSince = time.Time{}
		return fileResumeStep{}
	}
	moved := !w.havePrev || o.pos != w.prevPos
	w.prevPos, w.havePrev = o.pos, true
	if moved {
		w.lastMoveAt = o.now
		w.frozenSince = time.Time{}
		w.pausedSeen, w.pausedAt = false, 0
		w.repaired = false
		return fileResumeStep{}
	}
	if (!w.pausedSeen && !o.pauseHint) || w.repaired || o.pos <= 0 {
		return fileResumeStep{}
	}
	// The freeze holds the paused second. A position standing still anywhere
	// else is not it.
	if w.pausedAt > 0 && (o.pos > w.pausedAt+pausedPointSlack || o.pos < w.pausedAt-pausedPointSlack) {
		w.frozenSince = time.Time{}
		return fileResumeStep{}
	}
	// Frozen at the end is the track having ended: the end nets own that.
	eps := w.endEpsilon
	if eps <= 0 {
		eps = productionQueueTiming.endEpsilon
	}
	if o.end <= 0 || o.pos >= o.end-eps {
		return fileResumeStep{}
	}
	if w.frozenSince.IsZero() {
		w.frozenSince = o.now
		return fileResumeStep{hold: true}
	}
	freezeAfter := w.freezeAfter
	if freezeAfter <= 0 {
		freezeAfter = productionFileResumeTiming.freezeAfter
	}
	if o.now.Sub(w.frozenSince) < freezeAfter {
		return fileResumeStep{hold: true}
	}
	at := w.pausedAt
	if at <= 0 {
		at = o.pos
	}
	w.repaired = true
	w.frozenSince = time.Time{}
	return fileResumeStep{repair: true, at: at}
}

// restarted resets the watch after STR restarted the file: the next reading is
// a fresh start, and this resume has had its repair.
func (w *fileResumeWatch) restarted() {
	w.havePrev = false
	w.frozenSince = time.Time{}
	w.pausedSeen, w.pausedAt = false, 0
	w.repaired = true
}
