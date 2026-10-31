package webui

import (
	"context"
	"strconv"
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
// it was paused and the position counts up normally.
//
// The freeze is not every resume, though. A field ST10 (FW 27.0.6, 2026-10-31)
// was restarted by STR over a resume the listener heard play on by itself: the
// start of the file for two seconds, then a seek back, so the last stretch
// came twice on every pause. now_playing had stood on the paused second, and
// the RelTime veto below did not stop the restart. Why it did not is NOT
// measured: the bundles' syslog tails start minutes after each restart, and
// where they show a running play, RelTime and the syslog's "Audio path offset
// N" move in lockstep (RelTime = offset - 1 s, the same number as UpnpState's
// "time N", one clock). A frozen RelTime during the self-resume is the working
// guess; an unreadable RelTime is the other candidate. So the offset is used
// as a second clock where it helps either way (it is read even when RelTime is
// not), the real freeze's own "urlId mismatch" / "for stale UrlID" lines
// override it (internal/boxlog, AudioPathOffset and StaleURLIDSince), and
// every verdict logs both clocks' raw readings (clockReading) so the next
// bundle settles the question. STR acts in two places:
//
//   - The app's resume (resumePausedFileLocked) sends the plain Play first and
//     watches for a few seconds: an offset line past the paused one, or
//     RelTime moving, with no stale-URL line, is a resume that worked and
//     nothing else is sent. Only a stale-URL line or a position that does not
//     move falls through to Stop/Play/Seek.
//   - The speaker's own key goes past STR, so the lone-file end watch, which
//     already polls the speaker while a file plays (singletrackend.go), looks
//     for the shape: a pause, then PLAY_STATE with the position not moving.
//     Before it repairs (once per resume) it asks the same evidence
//     (speakerResumedByItself).
//
// Without the syslog (a chassis that logs no offset, the reader not running)
// both fall back to the renderer's own position, which is what they read
// before.
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
	// plainPlayWait is how long the app's resume watches a plain Play for a
	// moving clock before it restarts the file, counted from the speaker's
	// answer to the Play, and plainPlayStep how often it looks. The offset
	// line comes once a second, and one line past the paused offset is enough
	// (plainPlayContinuedLocked); four seconds leave room for a resume that
	// re-buffers before its first line. A stale-URL line ends the wait at once.
	plainPlayWait time.Duration
	plainPlayStep time.Duration
}

// productionFileResumeTiming is the file restart's clock on a real speaker.
var productionFileResumeTiming = fileResumeTiming{
	seekDelay:     2 * time.Second,
	seekRetry:     1500 * time.Millisecond,
	restartQuiet:  4 * time.Second,
	freezeAfter:   7 * time.Second,
	confirmGap:    1500 * time.Millisecond,
	plainPlayWait: 4 * time.Second,
	plainPlayStep: 500 * time.Millisecond,
}

// audioOffsetFresh is how old the syslog's audio-path offset may be and still
// be the speaker's current position: the line comes once a second while the
// file plays, so an older one means it stopped coming (paused, stopped, or a
// chassis that never logs it).
const audioOffsetFresh = 3 * time.Second

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
	s.notePausedAtFrom(pos, false)
}

// notePausedAtFrom is notePausedAt with the position's origin: fromLog is the
// speaker's syslog play clock (see pausePosFromLog).
func (s *Server) notePausedAtFrom(pos time.Duration, fromLog bool) {
	if pos <= 0 {
		return
	}
	s.pausePosMu.Lock()
	s.pausePos = pos
	s.pausePosFromLog = fromLog
	s.pausePosMu.Unlock()
}

// pausePositionFromLog returns the recorded pause position when it came from
// the speaker's syslog play clock.
func (s *Server) pausePositionFromLog() (time.Duration, bool) {
	s.pausePosMu.Lock()
	defer s.pausePosMu.Unlock()
	if !s.pausePosFromLog || s.pausePos <= 0 {
		return 0, false
	}
	return s.pausePos, true
}

// SetBoxLogPlayClockFns wires the speaker syslog's play clock and the frozen
// resume's signature (boxlog.Reader.AudioPathOffset and StaleURLIDSince) for
// the file resume above. Set before serving; nil keeps the RelTime-only
// judgement.
func (s *Server) SetBoxLogPlayClockFns(offset func() (int, time.Time, bool), staleSince func(time.Time) bool) {
	s.audioPathOffsetFn = offset
	s.staleURLIDSinceFn = staleSince
}

// audioPathOffset is the speaker's syslog play clock as a position, and when
// the line was read; ok=false without the reader or before its first line.
func (s *Server) audioPathOffset() (time.Duration, time.Time, bool) {
	if s.audioPathOffsetFn == nil {
		return 0, time.Time{}, false
	}
	sec, at, ok := s.audioPathOffsetFn()
	if !ok || sec < 0 {
		return 0, time.Time{}, false
	}
	return time.Duration(sec) * time.Second, at, true
}

// logPlayPosition is the syslog play clock when its newest line is at most
// maxAge old, and so still the speaker's position.
func (s *Server) logPlayPosition(maxAge time.Duration) (time.Duration, bool) {
	pos, at, ok := s.audioPathOffset()
	if !ok || time.Since(at) > maxAge || pos <= 0 {
		return 0, false
	}
	return pos, true
}

// staleURLIDSince reports whether the speaker logged the frozen resume's
// stale-URL-id discard at or after t. false without the reader.
func (s *Server) staleURLIDSince(t time.Time) bool {
	return s.staleURLIDSinceFn != nil && s.staleURLIDSinceFn(t)
}

// resumePausedFileLocked is the app's resume for a paused file. It sends the
// plain Play first and watches whether the speaker continues; only when it
// freezes (a stale-URL line, or no clock moving) does it restart the file:
// Stop, Play, Seek back to the paused position. It reports whether it handled
// the resume; false leaves the caller to its usual Play. The caller holds
// boxCmdMu.
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
	// The syslog clock beats both: if RelTime stands on an older pause after a
	// resume the speaker did by itself (the working guess for #830, see the
	// top of this file), the offset's own reading at the pause is the newer
	// one. That reading counts only when it is not behind RelTime: a RelTime
	// further on is the newer of the two.
	// The same syslog reading is the plain Play's baseline: one offset line
	// past it already says the speaker plays on.
	at := s.pausePosition()
	pctx, pcancel := context.WithTimeout(ctx, 3*time.Second)
	rel, _, relOK := s.renderer.PositionInfo(pctx)
	pcancel()
	var (
		logBase     time.Duration
		haveLogBase bool
	)
	if lp, fresh := s.logPlayPosition(audioOffsetFresh); fresh {
		at, logBase, haveLogBase = lp, lp, true
	} else if p, ok := s.pausePositionFromLog(); ok && (!relOK || p >= rel) {
		at, logBase, haveLogBase = p, p, true
	} else if relOK && rel > 0 {
		at = rel
	}
	pp := s.plainPlayContinuedLocked(ctx, logBase, haveLogBase)
	attrs := append([]any{"title", tr.title, "pausedAt", at.String(), "evidence", pp.why}, pp.attrs()...)
	switch {
	case pp.moved:
		s.logger.Info("resume of a paused file: the plain play continued it, nothing restarted", attrs...)
		return true
	case pp.froze:
		s.logger.Info("resume of a paused file: the plain play froze, restarting it at the paused position", attrs...)
	default:
		s.logger.Info("resume of a paused file: no clock moved after the plain play, restarting it at the paused position", attrs...)
	}
	return s.restartFileAtLocked(ctx, tr, at, "app resume")
}

// clockReading is one read of the speaker's two play clocks: the renderer's
// RelTime and the syslog's audio-path offset with the age of its line. Every
// resume verdict logs its readings raw, because what the two clocks do through
// a resume the speaker did by itself is not measured yet (see the top of this
// file): the next field bundle has to show the values, not only a verdict.
type clockReading struct {
	rel    time.Duration
	relOK  bool
	off    time.Duration
	offAge time.Duration
	offOK  bool
}

// readClocks reads both clocks, the syslog first (it costs nothing), then
// RelTime.
func (s *Server) readClocks() clockReading {
	var c clockReading
	if off, at, ok := s.audioPathOffset(); ok {
		c.off, c.offAge, c.offOK = off, time.Since(at), true
	}
	c.rel, c.relOK = s.readRelPosition()
	return c
}

// offFresh reports whether the offset was read and is recent enough to be the
// speaker's current position.
func (c clockReading) offFresh() bool { return c.offOK && c.offAge <= audioOffsetFresh }

// relString and offString render a reading for the log: "-" for a clock that
// gave nothing, the offset with its line's age.
func (c clockReading) relString() string {
	if !c.relOK {
		return "-"
	}
	return c.rel.String()
}

func (c clockReading) offString() string {
	if !c.offOK {
		return "-"
	}
	return c.off.String() + " age " + c.offAge.Round(100*time.Millisecond).String()
}

// plainPlayOutcome is what the app's plain Play showed: whether the speaker
// continued (moved), said it froze (froze: a stale-URL line or a refused
// Play), and the raw readings behind it.
type plainPlayOutcome struct {
	moved, froze bool
	why          string
	// relBefore is RelTime while still paused, relAfter the last read after
	// the Play.
	relBefore, relAfter clockReading
	// base is the offset the post-Play lines are compared with: the paused
	// one from the syslog when known, else the first line after the Play.
	base     time.Duration
	haveBase bool
	baseFrom string
	// offFirst and offLast are the first and last fresh offset line read
	// after the Play; lines counts the reads that found one.
	offFirst, offLast clockReading
	lines             int
	waited            time.Duration
}

func (o plainPlayOutcome) attrs() []any {
	base := "-"
	if o.haveBase {
		base = o.base.String() + " (" + o.baseFrom + ")"
	}
	return []any{
		"relTimeBefore", o.relBefore.relString(), "relTimeAfter", o.relAfter.relString(),
		"offsetBase", base, "offsetFirst", o.offFirst.offString(), "offsetLast", o.offLast.offString(),
		"offsetReads", o.lines, "waited", o.waited.Round(100 * time.Millisecond).String(),
	}
}

// plainPlayContinuedLocked sends the plain Play for a paused file and watches,
// for up to plainPlayWait after the speaker answered it, whether the speaker
// continues, with no stale-URL line since the Play:
//
//   - an offset line read after the Play that is past logBase, the paused
//     offset from the syslog (haveLogBase). One line is enough, so a resume
//     that re-buffers and leaves a single line in the window still counts.
//     Without a paused offset the first line after the Play is the baseline
//     and a later, further one is needed.
//   - or RelTime further on than it was while paused.
//
// The caller holds boxCmdMu, so the wait is bounded and runs on a detached
// context: the app gives up on its request after a few seconds, and the
// restart that may follow must still happen.
func (s *Server) plainPlayContinuedLocked(ctx context.Context, logBase time.Duration, haveLogBase bool) plainPlayOutcome {
	times := s.fileResumeTimes()
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second+times.plainPlayWait)
	defer cancel()
	out := plainPlayOutcome{base: logBase, haveBase: haveLogBase}
	if haveLogBase {
		out.baseFrom = "paused"
	}
	// RelTime while still paused, the renderer clock's starting point.
	out.relBefore.rel, out.relBefore.relOK = s.readRelPosition()
	playAt := time.Now()
	if err := s.renderer.Play(pctx); err != nil {
		out.froze, out.why = true, "the speaker refused the plain play: "+err.Error()
		return out
	}
	step := times.plainPlayStep
	if step <= 0 {
		step = productionFileResumeTiming.plainPlayStep
	}
	// The window counts from the speaker's answer, not from the request: a
	// Play that took a second to come back must not eat into it.
	for deadline := time.Now().Add(times.plainPlayWait); ; {
		time.Sleep(step)
		out.waited = time.Since(playAt)
		if s.staleURLIDSince(playAt) {
			out.froze, out.why = true, "the speaker logged a stale URL id after the play"
			return out
		}
		// The syslog clock: a line logged after the Play, past the baseline.
		if off, at, ok := s.audioPathOffset(); ok && !at.Before(playAt) && time.Since(at) <= audioOffsetFresh {
			r := clockReading{off: off, offAge: time.Since(at), offOK: true}
			if !out.offFirst.offOK {
				out.offFirst = r
			}
			out.offLast = r
			out.lines++
			if !out.haveBase {
				out.base, out.haveBase, out.baseFrom = off, true, "first line after the play"
			} else if off > out.base {
				out.moved, out.why = true, "audio path offset "+off.String()+" is past "+out.base.String()+" ("+out.baseFrom+")"
				return out
			}
		}
		// The renderer's clock: further on than it was while paused.
		if out.relBefore.relOK {
			cur, ok := s.readRelPosition()
			out.relAfter = clockReading{rel: cur, relOK: ok}
			if ok && cur > out.relBefore.rel {
				out.moved, out.why = true, "position moved from "+out.relBefore.rel.String()+" to "+cur.String()
				return out
			}
		}
		if !time.Now().Before(deadline) {
			out.why = "no clock moved within " + times.plainPlayWait.String() + " (" + strconv.Itoa(out.lines) + " offset reads after the play)"
			return out
		}
	}
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
// stopped it meanwhile. at is the paused position (the watch prefers the
// syslog clock's reading at the pause over RelTime), since is when the file
// last played on before the pause: a stale-URL line after it belongs to this
// resume.
func (s *Server) repairFrozenFileResume(tr singleTrack, gen uint64, at time.Duration, since time.Time) {
	// now_playing is not the last word: it trails the speaker, and a resume
	// the speaker did by itself must never be restarted over (#830: an ST10
	// jumped back ten seconds after its own resume had already played on).
	// The speaker's own clocks decide. Read before boxCmdMu: it only reads.
	v := s.speakerResumedByItself(at, since)
	attrs := append([]any{"title", tr.title, "pausedAt", at.String(), "evidence", v.why}, v.attrs()...)
	if v.moved {
		s.logger.Info("resume of a file: the speaker continued by itself, no restart", attrs...)
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
	s.logger.Warn("resume of a file froze on the speaker, restarting it at the paused position", attrs...)
	s.restartFileAtLocked(s.queueCtx(), tr, at, "frozen resume")
}

// resumeVerdict is speakerResumedByItself's answer with the readings it rests
// on, for the log (see clockReading).
type resumeVerdict struct {
	moved         bool
	why           string
	first, second clockReading
	// secondRead: the confirmation read happened (the first can decide alone).
	secondRead bool
	stale      bool
}

func (v resumeVerdict) attrs() []any {
	second := clockReading{}
	if v.secondRead {
		second = v.second
	}
	return []any{
		"relTime1", v.first.relString(), "relTime2", second.relString(),
		"offset1", v.first.offString(), "offset2", second.offString(),
		"staleURL", v.stale,
	}
}

// speakerResumedByItself reads the speaker's own clocks and reports whether
// the file is moving. Two clocks, read twice confirmGap apart:
//
//   - the syslog's audio-path offset (audioPathOffsetFn), the speaker's play
//     clock once a second. Trusted only while no stale-URL line came since:
//     whether it also counts during the real freeze is not measured, and that
//     line is the freeze's own signature.
//   - the renderer's position (UPnP GetPositionInfo RelTime, not the
//     now_playing document): already past the paused position, or further on
//     a moment later.
//
// Neither moving is no evidence of a resume and returns false, which keeps the
// repair: the freeze it exists for is real and long. Either clock moving is
// enough, so an unreadable RelTime alone no longer restarts a play whose
// offset counts on. The verdict carries both readings raw.
func (s *Server) speakerResumedByItself(at time.Duration, since time.Time) resumeVerdict {
	v := resumeVerdict{first: s.readClocks()}
	if v.first.relOK && at > 0 && v.first.rel > at+pausedPointSlack {
		v.moved, v.why = true, "position "+v.first.rel.String()+" is past the paused "+at.String()
		return v
	}
	time.Sleep(s.fileResumeTimes().confirmGap)
	v.second, v.secondRead = s.readClocks(), true
	v.stale = !since.IsZero() && s.staleURLIDSince(since)
	if !v.stale && v.first.offFresh() && v.second.offFresh() && v.second.off > v.first.off {
		v.moved, v.why = true, "audio path offset moved "+v.first.off.String()+"->"+v.second.off.String()
		return v
	}
	if v.first.relOK && v.second.relOK && v.second.rel > v.first.rel {
		v.moved, v.why = true, "position moved from "+v.first.rel.String()+" to "+v.second.rel.String()
		return v
	}
	if v.stale {
		v.why = "the speaker logged a stale URL id since the resume"
	} else {
		v.why = "no clock moved"
	}
	return v
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
