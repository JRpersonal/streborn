package oled

// The live radio track on the speaker's own panel.
//
// Before this, "show the current track on the speaker display" re-sent the
// stream to the speaker with the new title as metadata, which made it
// re-buffer: about a second of silence at every song change. On a speaker
// whose panel STR can draw on, STR now draws the text itself and the audio
// stream is never touched.
//
// The track screen is STR's own full screen (station on top, artist and title
// below), not a band over BoseApp's picture: what BoseApp would show there is
// the stale title from the start of the stream, and mixing the two is exactly
// what the notices avoid too. It is drawn once per change and stands still in
// between, so a held panel costs no CPU, except while something moves: the
// playing symbol, and a line too long for the panel at its normal size,
// which scrolls instead of shrinking (marquee.go). Every few minutes the
// whole layout moves by a pixel so no pixel sits under the same text all
// evening.
//
// The screen can carry the time as well: with a track, in the top line next
// to the station; without one (the user chose the clock alone), large in the
// middle. Then it is redrawn at every full minute and stands still between.
//
// On the speaker's own inputs, Bluetooth and AUX, the same large clock can
// stand in for the firmware's connection picture (ShowSourceClock), with the
// input's name small on top. STR does not know whether music runs there, so
// that screen has no playing symbol and is drawn only at the minute.
//
// The panel is shared. The splash, the update screen, the games and the
// notices all win it: acquirePanel and the notice queue call yieldTrack,
// which ends the hold at once, and the track screen comes back by itself once
// the panel is free again. A volume change hands the panel back to BoseApp for
// a few seconds so its own volume bar shows (PauseTrack). Standby, a new
// stream, a source change away from STR, a stop, the setting switched off and
// the agent exiting all end it (ClearTrack); if the agent dies the kernel
// releases the held thread on its own.
//
// Nothing here writes to NAND.

import (
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// TrackText is what the track screen shows. Station is the small top line;
// Title is the main text, Artist the line above it. Either of Artist and
// Title may be empty (the per-speaker mode shows only one of them). Clock
// adds the time: in the top line when there is a track, large on its own when
// Artist and Title are both empty. Hour12 draws it as "3:42 PM", as the
// speaker's own clock setting says.
type TrackText struct {
	Station, Artist, Title string
	Clock, Hour12          bool

	// still marks the clock on one of the speaker's own inputs
	// (ShowSourceClock): no playing symbol, nothing moves. Set by
	// ShowSourceClock only, so it is part of the cache key and of the
	// comparison with what is shown.
	still bool

	// sleepMin is the sleep timer's countdown in whole minutes (0: none).
	// Not the caller's to set: the screen's draw fills it from the deadline
	// (SetSleepDeadline), so the layout and the cache key both carry it.
	sleepMin int
}

func (t TrackText) empty() bool { return !t.Clock && t.Artist == "" && t.Title == "" }

// sanitized returns t made drawable in the track screen's font
// (sanitizeSmooth; no cut yet: the layout wraps and cuts).
func (t TrackText) sanitized() TrackText {
	return TrackText{
		Station: sanitizeSmooth(t.Station, trackTextMax),
		Artist:  sanitizeSmooth(t.Artist, trackTextMax),
		Title:   sanitizeSmooth(t.Title, trackTextMax),
		Clock:   t.Clock,
		Hour12:  t.Hour12,
		still:   t.still,
	}
}

const (
	// trackTextMax bounds what is kept of one field before wrapping; far more
	// than the panel can show, so any cut is the layout's.
	trackTextMax = 160
	// trackRetry is how often a track screen that wants the panel tries to
	// take it while something else holds it.
	trackRetry = time.Second
	// trackShiftEvery moves the layout by a pixel against panel wear.
	trackShiftEvery = 3 * time.Minute
	// trackYieldPause keeps the track screen off the panel briefly after it
	// gave way, so the one that asked takes it first.
	trackYieldPause = time.Second
	// TrackVolumePause is how long a volume change hands the panel back to
	// BoseApp, long enough for its volume bar.
	TrackVolumePause = 4 * time.Second
	// trackStopWait bounds how long a stop waits for the panel to be let go.
	trackStopWait = 2 * time.Second
	// trackClockLate is how far past the full minute the clock is redrawn.
	trackClockLate = 50 * time.Millisecond
)

// trackScreen owns the track display: what it should show and whether it
// holds the panel. One goroutine (loop) takes and holds the panel; every
// public call only changes the wanted state and wakes or stops it.
type trackScreen struct {
	mu         sync.Mutex
	want       *TrackText // nil: nothing to show
	pauseUntil time.Time
	holding    bool
	drawn      bool // the current hold has put a frame on the panel
	failures   int  // holds in a row that never got the panel
	// stepAside: the running hold ends only to give way for a moment (a
	// volume bar, a notice); returning: the next hold is the comeback. Both
	// log at DEBUG, so a listener working the volume does not write two
	// lines per change into the NAND log.
	stepAside, returning bool
	// paused: the speaker said it stopped playing; the playing symbol lies
	// flat and the screen stands still. Unknown counts as playing, because
	// the screen only shows while one of STR's streams is on.
	paused bool
	// sleepUntil is when the sleep timer switches the speaker off (zero:
	// none); the top line counts down to it (sleep.go).
	sleepUntil time.Time
	// cost counters for the display_cost debug section
	draws     int64
	drawNanos int64
	heldNanos int64 // finished holds
	holdStart time.Time
	// scrollDraws counts the draws of a moving marquee frame
	scrollDraws int64
	// marqueeFrom is when the marquee cycle started (zero: at the next
	// draw); marqueeNext when its next step is due (zero: nothing scrolls).
	marqueeFrom, marqueeNext time.Time
	// cache keeps the standing parts of the screen between marquee frames;
	// used on the holding thread only (draw)
	cache     trackCache
	foreign   bool   // the speaker is on a source that is not STR's
	source    string // the source the speaker last switched to
	quit      chan struct{}
	done      chan struct{}
	stopWhy   string
	redraw    chan struct{}
	wake      chan struct{}
	startOnce sync.Once

	// seams; newTrackScreen wires the real ones
	now        func() time.Time
	supported  func() bool
	ready      func() bool
	gameActive func() bool
	noticeBusy func() bool
	tryLock    func() bool
	unlock     func()
	hold       func(draw func([]byte), redraw, quit <-chan struct{}, next func() time.Duration) error
	retry      time.Duration
	shiftEvery time.Duration
	log        func() *slog.Logger
}

func newTrackScreen() *trackScreen {
	return &trackScreen{
		redraw:     make(chan struct{}, 1),
		wake:       make(chan struct{}, 1),
		now:        time.Now,
		supported:  panelSupported,
		ready:      panelMayHold, // flip thread and display controller up (splash.go)
		gameActive: GameActive,
		tryLock:    busy.TryLock,
		unlock:     busy.Unlock,
		hold:       hold,
		retry:      trackRetry,
		shiftEvery: trackShiftEvery,
		log:        noticeLogger,
	}
}

var tracks = newTrackScreen()

// TrackSupported reports whether STR can draw the track on this speaker's
// panel. Where it cannot, the caller keeps the old metadata path.
func TrackSupported() bool { return tracks.supported() }

// ShowTrack puts t on the panel, or updates what is there. Returns at once.
func ShowTrack(t TrackText) { tracks.show(t) }

// ShowSourceClock puts the large clock on the panel while the speaker plays
// one of its own inputs (SourceClockLabel), with t.Station as the input's
// name on top. source is the firmware's name of that input: the call does
// nothing unless the speaker is still on it (SetTrackSource), so a late call
// from before a switch never puts a stale screen up. Any source change ends
// it. Returns at once.
func ShowSourceClock(source string, t TrackText) { tracks.showSourceClock(source, t) }

// SourceClockLabel reports whether the clock may stand in for the firmware's
// picture on source, and the name shown on top of it. Only the speaker's
// plain inputs qualify, the ones where the firmware shows nothing but a
// connection picture: BLUETOOTH and AUX. Firmware services that draw their own
// titles (TuneIn, native Spotify, Amazon, Pandora...), AirPlay, a TV or HDMI
// input (PRODUCT), LOCAL (a soundbar's TV input on some models, not seen on a
// speaker with a panel) and STANDBY keep the firmware's screen.
func SourceClockLabel(source string) (label string, ok bool) {
	switch source {
	case "BLUETOOTH":
		return "Bluetooth", true
	case "AUX":
		return "AUX", true
	}
	return "", false
}

// ClearTrack ends the track screen and hands the panel back to BoseApp.
// Blocks (bounded) until the panel is let go, so a caller that exits next
// leaves nothing held. reason goes to the log.
func ClearTrack(reason string) { tracks.clear(reason) }

// ClearSourceClock ends the input clock (ShowSourceClock) put up for source,
// and nothing else: when the speaker has moved on since (another input, or
// STR's own source with its track screen already back), the call is a no-op.
// For a caller that decided on the panel from a source read a moment ago.
// Blocks (bounded) like ClearTrack.
func ClearSourceClock(source, reason string) { tracks.clearSourceClock(source, reason) }

// PauseTrack hands the panel back to BoseApp for d (a volume change: its own
// volume bar shows), then the track screen returns by itself.
func PauseTrack(d time.Duration, reason string) { tracks.pause(d, reason) }

// TrackShowing reports whether the track screen holds the panel.
func TrackShowing() bool {
	tracks.mu.Lock()
	defer tracks.mu.Unlock()
	return tracks.holding
}

// yieldTrack ends a held track screen so another panel user can take it; the
// track screen comes back once the panel is free.
func yieldTrack(reason string) { tracks.yield(reason) }

func (s *trackScreen) start() { s.startOnce.Do(func() { go s.loop() }) }

func (s *trackScreen) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// SetTrackPlaying tells the track screen whether the speaker plays, from its
// own play status: the playing symbol moves only while it does.
func SetTrackPlaying(playing bool) { tracks.setPlaying(playing) }

func (s *trackScreen) setPlaying(playing bool) {
	s.mu.Lock()
	changed := s.paused == playing
	s.paused = !playing
	if changed && playing {
		// play again: the marquee starts over with the start of each line
		s.marqueeFrom = time.Time{}
	}
	// the input clock has no playing symbol: nothing to redraw
	holding := s.holding && (s.want == nil || !s.want.still)
	s.mu.Unlock()
	if changed && holding {
		select {
		case s.redraw <- struct{}{}:
		default:
		}
	}
}

// SetTrackSource tells the track screen which source the speaker switched
// to and whether it is one of STR's own. On any other source (standby, AUX,
// Bluetooth, a firmware service such as TuneIn that shows its own titles) the
// track leaves the panel and titles are ignored until STR's source is back.
// Any switch at all ends the input clock (ShowSourceClock); the caller puts it
// up again for the new input.
// Does not block (it is called from the gabbo read loop): the flag and the
// wanted text change at once, in order, and only letting go of the panel
// runs in the background.
func SetTrackSource(name string, ours bool) { tracks.setSource(name, ours) }

func (s *trackScreen) setSource(name string, ours bool) {
	s.mu.Lock()
	s.foreign = !ours
	s.source = name
	had := s.want != nil
	inputClock := had && s.want.still
	if !ours || inputClock {
		s.want = nil
	}
	s.mu.Unlock()
	if !had || (ours && !inputClock) {
		return
	}
	reason := "source " + name
	go func() {
		if !s.stop(reason) {
			s.log().Info("display track overlay off", "reason", reason, "held", false)
		}
	}()
}

func (s *trackScreen) show(t TrackText) {
	t = t.sanitized()
	if t.empty() {
		s.clear("no track text")
		return
	}
	s.mu.Lock()
	if s.foreign {
		s.mu.Unlock()
		s.log().Debug("display track text ignored", "reason", "speaker is on another source", "title", t.Title)
		return
	}
	s.setWant(t)
}

func (s *trackScreen) showSourceClock(source string, t TrackText) {
	t = TrackText{Station: t.Station, Clock: true, Hour12: t.Hour12, still: true}.sanitized()
	s.mu.Lock()
	if !s.foreign || s.source != source {
		now := s.source
		s.mu.Unlock()
		s.log().Debug("display input clock ignored", "reason", "speaker left the input", "input", source, "source", now)
		return
	}
	s.setWant(t)
}

// setWant makes t the wanted screen and takes or redraws the panel. Called
// with s.mu held; unlocks it.
func (s *trackScreen) setWant(t TrackText) {
	if s.want != nil && *s.want == t {
		s.mu.Unlock()
		return
	}
	s.want = &t
	// new text: a scrolling line starts at its beginning again
	s.marqueeFrom = time.Time{}
	holding := s.holding
	s.mu.Unlock()
	s.log().Debug("display track text", "station", t.Station, "artist", t.Artist, "title", t.Title, "clock", t.Clock, "input", t.still)
	if holding {
		select {
		case s.redraw <- struct{}{}:
		default:
		}
		return
	}
	s.start()
	s.poke()
}

func (s *trackScreen) clear(reason string) {
	s.mu.Lock()
	had := s.want != nil
	s.want = nil
	s.returning = false
	s.mu.Unlock()
	if !s.stop(reason) && had {
		s.log().Info("display track overlay off", "reason", reason, "held", false)
	}
}

func (s *trackScreen) clearSourceClock(source, reason string) {
	s.mu.Lock()
	if s.want == nil || !s.want.still || s.source != source {
		now := s.source
		s.mu.Unlock()
		s.log().Debug("display input clock clear ignored", "reason", reason, "input", source, "source", now)
		return
	}
	s.want = nil
	s.returning = false
	s.mu.Unlock()
	if !s.stop(reason) {
		s.log().Info("display track overlay off", "reason", reason, "held", false)
	}
}

func (s *trackScreen) pause(d time.Duration, reason string) {
	s.mu.Lock()
	if s.holding {
		s.stepAside = true
	}
	if until := s.now().Add(d); until.After(s.pauseUntil) {
		s.pauseUntil = until
	}
	s.mu.Unlock()
	s.stop(reason)
}

func (s *trackScreen) yield(reason string) { s.pause(trackYieldPause, reason) }

// stop ends a running hold and waits (bounded) until the panel is let go.
// Reports whether a hold was running.
func (s *trackScreen) stop(reason string) bool {
	s.mu.Lock()
	if !s.holding {
		s.mu.Unlock()
		return false
	}
	quit, done := s.quit, s.done
	if s.stopWhy == "" {
		s.stopWhy = reason
		close(quit)
	}
	s.mu.Unlock()
	select {
	case <-done:
	case <-time.After(trackStopWait):
	}
	return true
}

func (s *trackScreen) loop() {
	for {
		s.tryHold()
		var tc <-chan time.Time
		s.mu.Lock()
		if s.want != nil {
			wait := s.retry
			if d := s.pauseUntil.Sub(s.now()); d > wait {
				wait = d
			}
			tc = time.After(wait)
		}
		s.mu.Unlock()
		select {
		case <-s.wake:
		case <-tc:
		}
	}
}

// canTake is the gate for taking the panel: something to show, no pause, a
// supported panel with BoseApp's display up, no game round and no notice
// showing or waiting. The panel lock itself is taken after it.
func (s *trackScreen) canTake() bool {
	s.mu.Lock()
	ok := s.want != nil && !s.holding && !s.now().Before(s.pauseUntil)
	s.mu.Unlock()
	// cheapest first: ready() walks /proc
	return ok && !s.gameActive() && !s.noticeActive() && s.supported()
}

// noticeActive is the notice queue's state, through the seam when a test set
// one. (Not wired in newTrackScreen: the two package queues would form an
// initialisation cycle.)
func (s *trackScreen) noticeActive() bool {
	if s.noticeBusy != nil {
		return s.noticeBusy()
	}
	return notices.active()
}

// tryHold takes the panel when the gate allows and holds it until stopped.
// Runs on the loop goroutine only.
func (s *trackScreen) tryHold() {
	if !s.canTake() || !s.tryLock() {
		return
	}
	if s.gameActive() || !s.ready() {
		s.unlock()
		return
	}
	s.mu.Lock()
	if s.want == nil {
		s.mu.Unlock()
		s.unlock()
		return
	}
	s.holding, s.drawn = true, false
	s.marqueeFrom, s.marqueeNext = time.Time{}, time.Time{}
	s.quit, s.done, s.stopWhy = make(chan struct{}), make(chan struct{}), ""
	quit, done := s.quit, s.done
	// a redraw left from before the hold would only draw the same text twice
	select {
	case <-s.redraw:
	default:
	}
	s.mu.Unlock()

	start := s.now()
	s.mu.Lock()
	s.holdStart = start
	s.mu.Unlock()
	err := s.hold(s.draw, s.redraw, quit, s.nextDraw)

	s.unlock()
	s.mu.Lock()
	s.holding = false
	s.heldNanos += int64(s.now().Sub(start))
	s.holdStart = time.Time{}
	why, drawn := s.stopWhy, s.drawn
	failures := 0
	if drawn {
		s.failures = 0
	} else if why == "" {
		// The panel could not be taken: back off, doubling up to five
		// minutes, so a speaker where it never works is not asked every
		// second and the log is not filled.
		s.failures++
		failures = s.failures
		back := s.retry * 10 << min(failures-1, 5)
		if back > 5*time.Minute {
			back = 5 * time.Minute
		}
		if until := s.now().Add(back); until.After(s.pauseUntil) {
			s.pauseUntil = until
		}
	}
	if why == "" {
		why = "panel lost"
	}
	aside := s.stepAside && drawn && err == nil
	s.stepAside = false
	if aside {
		s.returning = true
	}
	s.mu.Unlock()
	close(done)
	switch {
	case aside:
		s.log().Debug("display track overlay stepped aside", "reason", why, "heldSec", int(s.now().Sub(start).Seconds()))
	case drawn:
		args := []any{"reason", why, "heldSec", int(s.now().Sub(start).Seconds())}
		if err != nil {
			args = append(args, "err", err)
		}
		s.log().Info("display track overlay off", args...)
	case failures == 1:
		s.log().Info("display track overlay could not take the panel", "err", err)
	case failures > 1:
		s.log().Debug("display track overlay could not take the panel", "err", err, "attempt", failures)
	}
}

// draw renders the wanted text; called by hold on the holding thread, so
// the first call is the moment the panel is ours.
func (s *trackScreen) draw(buf []byte) {
	s.mu.Lock()
	var t TrackText
	// wanted is false when a clear landed between the start of this hold and
	// its first frame; the hold is about to end, so it announces nothing.
	wanted := s.want != nil
	if wanted {
		t = *s.want
	}
	first := !s.drawn
	s.drawn = true
	back := first && s.returning
	if first {
		s.returning = false
	}
	now := s.now()
	step := 0
	if s.shiftEvery > 0 {
		step = int(now.UnixNano() / int64(s.shiftEvery))
	}
	anim := trackAnim{playing: !s.paused && (s.want == nil || !s.want.still), frame: int(now.UnixNano() / int64(barsFrame))}
	if s.marqueeFrom.IsZero() {
		s.marqueeFrom = now
	}
	since := now.Sub(s.marqueeFrom)
	t.sleepMin = sleepMinutes(s.sleepUntil, now)
	s.mu.Unlock()
	switch {
	case back:
		s.log().Debug("display track overlay back", append([]any{"station", t.Station, "title", t.Title}, holdAttrs("track")...)...)
	case first && wanted:
		s.log().Info("display track overlay on", append([]any{"station", t.Station, "artist", t.Artist, "title", t.Title, "clock", t.Clock, "input", t.still}, holdAttrs("track")...)...)
	}
	dx, dy := trackShift(step)
	t0 := time.Now()
	f := s.cache.prepare(t, now, dx, dy)
	// The marquee moves only while the speaker plays: paused, every line
	// shows its start and no scroll frame is asked for.
	mStep, mNext := 0, time.Duration(0)
	if anim.playing {
		mStep, mNext = marqueeAt(since, f.steps())
	}
	s.cache.render(anim, mStep, buf)
	el := time.Since(t0)
	s.mu.Lock()
	s.draws++
	s.drawNanos += int64(el)
	if mStep > 0 {
		s.scrollDraws++
	}
	s.marqueeNext = time.Time{}
	if mNext > 0 {
		s.marqueeNext = now.Add(mNext)
	}
	s.mu.Unlock()
}

// TrackStats is what the track screen has cost since the agent started: how
// long it held the panel, how often it drew and how long the drawing took
// (the copy to the panel not included). For the display_cost debug section.
func TrackStats() map[string]any { return tracks.stats() }

func (s *trackScreen) stats() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := s.heldNanos
	if !s.holdStart.IsZero() {
		held += int64(s.now().Sub(s.holdStart))
	}
	return map[string]any{
		"holding":   s.holding,
		"heldSec":   held / int64(time.Second),
		"draws":     s.draws,
		"drawMsSum": float64(s.drawNanos) / 1e6,
		// draws of a moving marquee frame, a part of draws
		"scrollDraws": s.scrollDraws,
	}
}

// nextDraw is how long the held screen may stand before it is drawn again:
// until the next layout shift, or the next full minute while it shows the
// time. Called by hold after every draw.
func (s *trackScreen) nextDraw() time.Duration {
	s.mu.Lock()
	clock := s.want != nil && s.want.Clock
	moving := s.want != nil && !s.paused && !s.want.still
	marquee := s.marqueeNext
	now := s.now()
	toSleep := time.Duration(0)
	if s.want != nil {
		toSleep = toSleepStep(s.sleepUntil, now)
	}
	s.mu.Unlock()
	var d time.Duration
	if s.shiftEvery > 0 {
		d = s.shiftEvery - time.Duration(now.UnixNano()%int64(s.shiftEvery))
	}
	if moving {
		// the next frame of the playing symbol
		toFrame := barsFrame - time.Duration(now.UnixNano()%int64(barsFrame))
		if d <= 0 || toFrame < d {
			d = toFrame
		}
	}
	if moving && !marquee.IsZero() {
		// the next marquee step, or the end of its pause
		toStep := max(marquee.Sub(now), time.Millisecond)
		if d <= 0 || toStep < d {
			d = toStep
		}
	}
	if clock {
		// a hair past the minute, so the draw reads the new one
		toMinute := time.Minute - time.Duration(now.UnixNano()%int64(time.Minute)) + trackClockLate
		if d <= 0 || toMinute < d {
			d = toMinute
		}
	}
	if toSleep > 0 && (d <= 0 || toSleep < d) {
		// the sleep countdown's next minute, also with the clock off
		d = toSleep
	}
	return d
}

// trackShift is the layout offset for shift step n: a slow walk around a 2x2
// pixel square, so the text never sits on the same pixels for long.
func trackShift(n int) (dx, dy int) {
	switch n % 4 {
	case 1:
		return 1, 0
	case 2:
		return 1, 1
	case 3:
		return 0, 1
	}
	return 0, 0
}

// ---- layout ----

// Track screen layout. Two text sizes and no more: the station and the artist
// in the small face, the title and the time in the large one, so the eye
// does not jump between three or four sizes (discussion #1305). The levels
// rank the lines: the title brightest, the artist below it, the station
// dimmest, as it changes least. Vertical positions are baselines.
const (
	lvlTrackStation = 9
	lvlTrackRule    = 3
	lvlTrackArtist  = 11
	lvlTrackTitle   = 15
	lvlTrackClock   = 12
	// smallLineH and bigLineH are baseline to baseline within a wrapped
	// field: the descender, then room for the accents of the next line.
	smallLineH = 13
	bigLineH   = 19
	trackTopY  = 5 // top of the station's capitals without the time
	clockTopY  = 4 // top of the time's digits in the top line
	// trackRuleGap is how far below the top line's baseline the rule runs,
	// clear of its descenders; trackAreaGap where the text block may start
	// below the rule.
	trackRuleGap = 4
	trackAreaGap = 4
	trackGap     = 5 // between the artist's descenders and the title
	ampmGap      = 2 // between "3:42" and "PM" in the top line
)

// trackLines is the text of the track screen broken into panel lines.
type trackLines struct {
	station string
	artist  []string // small face: up to two lines, or one that scrolls
	title   []string // large face: up to two lines, or one that scrolls
}

// layoutTrack breaks t (already sanitised) into lines. Nothing is drawn
// smaller than its normal size and nothing is cut: the station is one line,
// the artist is wrapped onto two small lines when it fits there, the title
// onto two large lines when it fits there without splitting a word;
// otherwise the field is one line that scrolls (marquee.go). Two standing
// lines read at a glance and cost nothing, so wrapping wins where it works;
// a third line would crowd the panel and a split word reads worse than a
// moving one. Widths are pixels: the font is proportional, so "Männer" and
// "MMMMMM" are not the same length.
func layoutTrack(t TrackText) trackLines {
	l := trackLines{station: t.Station}
	if w, cut := wrapWidth(t.Artist, smoothSmall, blockBoxW, 2); !cut {
		l.artist = w
	} else {
		l.artist = []string{t.Artist}
	}
	if w, cut := wrapWidth(t.Title, smoothLarge, blockBoxW, 2); !cut && !hasWideWord(t.Title, smoothLarge, blockBoxW) {
		l.title = w
	} else if t.Title != "" {
		l.title = []string{t.Title}
	}
	return l
}

// hasWideWord reports whether a word of s is wider than w pixels in face f:
// split across two large lines it reads worse than scrolling on one.
func hasWideWord(s string, f *smoothFont, w int) bool {
	for _, word := range strings.Fields(s) {
		if f.width(word) > w {
			return true
		}
	}
	return false
}

// wrapWidth breaks s into at most maxLines lines no wider than w pixels in
// face f, at spaces, splitting a word only when it alone is wider than a
// line. When s does not fit, the last line ends in an ellipsis and cut is
// true.
func wrapWidth(s string, f *smoothFont, w, maxLines int) (lines []string, cut bool) {
	if s == "" || maxLines <= 0 {
		return nil, false
	}
	cur := ""
	words := strings.Fields(s)
	for i := 0; i < len(words); i++ {
		word := words[i]
		switch {
		case cur == "" && f.width(word) <= w:
			cur = word
		case cur != "" && f.width(cur+" "+word) <= w:
			cur += " " + word
		case cur == "":
			// a word wider than a line: split it at the last rune that fits
			// (always at least one, so a narrow box cannot loop)
			n := fitRunes(word, f, w)
			cur = word[:n]
			words[i] = word[n:]
			i--
		default:
			lines = append(lines, cur)
			cur = ""
			i--
		}
		if len(lines) == maxLines {
			// a word is still waiting: it does not fit any more
			cut = true
			break
		}
	}
	if !cut && cur != "" {
		lines = append(lines, cur)
	}
	if cut {
		const ell = "…"
		last := lines[len(lines)-1]
		for last != "" && f.width(last+ell) > w {
			_, size := utf8.DecodeLastRuneInString(last)
			last = last[:len(last)-size]
		}
		lines[len(lines)-1] = strings.TrimRight(last, " ") + ell
	}
	return lines, cut
}

// fitRunes is how many bytes of s fit into w pixels in face f, at least the
// first rune.
func fitRunes(s string, f *smoothFont, w int) int {
	n, used := 0, 0
	for i, r := range s {
		g, _ := f.glyph(r)
		if i > 0 && used+int(g.adv) > w {
			return n
		}
		used += int(g.adv)
		n = i + utf8.RuneLen(r)
	}
	return n
}

// wrapText breaks s into at most maxLines lines of cols characters at spaces,
// splitting a word only when it alone is longer than a line. When s does not
// fit, the last line ends in "..." and cut is true. s is sanitised for the
// dot-matrix font (SanitizeText), so one byte is one character. The notices
// wrap with it; the track screen and the preset notice's station name wrap
// by pixel width (wrapWidth).
func wrapText(s string, cols, maxLines int) (lines []string, cut bool) {
	if s == "" || maxLines <= 0 {
		return nil, false
	}
	cur := ""
	words := strings.Fields(s)
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case cur == "" && len(w) <= cols:
			cur = w
		case cur != "" && len(cur)+1+len(w) <= cols:
			cur += " " + w
		case cur == "":
			// a word longer than a line: split it
			cur = w[:cols]
			words[i] = w[cols:]
			i--
		default:
			lines = append(lines, cur)
			cur = ""
			i--
		}
		if len(lines) == maxLines {
			// a word is still waiting: it does not fit any more
			cut = true
			break
		}
	}
	if !cut && cur != "" {
		lines = append(lines, cur)
	}
	if cut {
		last := lines[len(lines)-1]
		if len(last)+3 > cols {
			last = strings.TrimRight(last[:cols-3], " ")
		}
		lines[len(lines)-1] = last + "..."
	}
	return lines, cut
}

// clockText is the time as the track screen draws it: "15:42", or "3:42"
// with "PM" apart when the speaker's clock is set to 12 hours.
func clockText(now time.Time, hour12 bool) (hm, ampm string) {
	if hour12 {
		return now.Format("3:04"), now.Format("PM")
	}
	return now.Format("15:04"), ""
}

// trackFrame is the whole track screen laid out, unshifted: every text line
// in its box, where the playing symbol sits and where the rule runs.
type trackFrame struct {
	lines      []marqueeLine
	barsBottom int
	ruleY      int // -1: no rule
	// moon: the sleep countdown's moon at moonX, moonY (top left, unshifted)
	moon         bool
	moonX, moonY int
	// noBars: no playing symbol (the clock on one of the speaker's inputs)
	noBars bool
}

// steps is how many marquee steps the longest scrolling line needs; 0 when
// everything fits and the screen can stand still.
func (f trackFrame) steps() int {
	n := 0
	for _, l := range f.lines {
		n = max(n, l.steps())
	}
	return n
}

// Text boxes. The station box keeps clear of the playing symbol on the left
// and, mirrored, on the right, so a centred station stays centred on the
// panel; the text block below uses the whole width less a pixel each side.
const (
	stationBoxX = 1 + barsW + barsGap // 12
	blockBoxX   = 1
	blockBoxW   = Width - 2
)

// trackLayout lays out the track screen for t (already sanitised) at the time
// now.
func trackLayout(t TrackText, now time.Time) trackFrame {
	f := trackFrame{ruleY: -1, noBars: t.still}
	if t.Artist == "" && t.Title == "" {
		if t.Clock {
			f.plainHeader(t)
			f.bigClock(t, now)
		}
		return f
	}
	var areaY int
	if t.Clock {
		// The time in the large face on the right of the top line, the
		// station small on its left. At the small size the time was too
		// small to read from across a room (Jens, 2026-10-13).
		areaY = f.clockHeader(t, now)
	} else {
		areaY = f.plainHeader(t)
	}
	l := layoutTrack(t)
	// The block runs from the top of the first line's capitals to the
	// bottom of the last line's descenders, and is centred in the room left.
	type ln struct {
		s   string
		f   *smoothFont
		lvl int
	}
	var block []ln
	for _, s := range l.artist {
		block = append(block, ln{s, smoothSmall, lvlTrackArtist})
	}
	for _, s := range l.title {
		block = append(block, ln{s, smoothLarge, lvlTrackTitle})
	}
	if len(block) == 0 {
		return f
	}
	// baselines relative to the first line's capital top
	base := make([]int, len(block))
	for i, b := range block {
		switch {
		case i == 0:
			base[i] = b.f.capH
		case b.f == block[i-1].f && b.f == smoothSmall:
			base[i] = base[i-1] + smallLineH
		case b.f == block[i-1].f:
			base[i] = base[i-1] + bigLineH
		default:
			// from the artist to the title
			base[i] = base[i-1] + block[i-1].f.bottom + trackGap + b.f.capH
		}
	}
	last := block[len(block)-1]
	h := base[len(base)-1] + last.f.bottom
	y := areaY + (Height-areaY-h)/2
	for i, b := range block {
		f.lines = append(f.lines, marqueeLine{s: b.s, f: b.f, lvl: b.lvl, x0: blockBoxX, w: blockBoxW, y: y + base[i]})
	}
	return f
}

// clockHeader lays out the top line with the time: the time in the large
// face flush right ("PM" small after it on a 12-hour clock), the station
// small on the left in the room left, all on one baseline, and the rule
// under them. Returns where the text block below may start.
func (f *trackFrame) clockHeader(t TrackText, now time.Time) int {
	hm, ampm := clockText(now, t.Hour12)
	w := textWidth(hm, smoothLarge)
	if ampm != "" {
		w += ampmGap + textWidth(ampm, smoothSmall)
	}
	x := Width - w - 1
	baseline := clockTopY + smoothLarge.capH
	f.lines = append(f.lines, marqueeLine{s: hm, f: smoothLarge, lvl: lvlTrackClock, x0: x, w: textWidth(hm, smoothLarge), y: baseline, left: true})
	if ampm != "" {
		ax := x + textWidth(hm, smoothLarge) + ampmGap
		f.lines = append(f.lines, marqueeLine{s: ampm, f: smoothSmall, lvl: lvlTrackStation, x0: ax, w: textWidth(ampm, smoothSmall), y: baseline, left: true})
	}
	f.barsBottom = baseline
	// The sleep countdown sits between the station and the time; the
	// station's box ends before it (a long name scrolls in what is left).
	stationEnd := x - 4
	if t.sleepMin > 0 {
		sx := x - sleepClockGap - sleepWidth(t.sleepMin)
		f.sleepIndicator(t.sleepMin, sx, baseline)
		stationEnd = sx - 4
	}
	if t.Station != "" && stationEnd > stationBoxX {
		f.lines = append(f.lines, marqueeLine{s: t.Station, f: smoothSmall, lvl: lvlTrackStation, x0: stationBoxX, w: stationEnd - stationBoxX, y: baseline, left: true})
	}
	f.ruleY = baseline + trackRuleGap
	return f.ruleY + trackAreaGap
}

// plainHeader lays out the top line without the time: the playing symbol on
// the left, the station centred between it and its mirror on the right, the
// rule under the station. Returns where the text block below may start.
func (f *trackFrame) plainHeader(t TrackText) int {
	baseline := trackTopY + smoothSmall.capH
	f.barsBottom = baseline
	// The sleep countdown flush right; the station's box shrinks by the same
	// on both sides, so a centred station stays centred on the panel.
	margin := stationBoxX
	if t.sleepMin > 0 {
		sx := Width - 1 - sleepWidth(t.sleepMin)
		f.sleepIndicator(t.sleepMin, sx, baseline)
		margin = max(margin, Width-sx+5)
	}
	if t.Station != "" {
		f.lines = append(f.lines, marqueeLine{s: t.Station, f: smoothSmall, lvl: lvlTrackStation, x0: margin, w: Width - 2*margin, y: baseline})
		f.ruleY = baseline + trackRuleGap
	}
	return plainAreaY()
}

// plainAreaY is where the text block may start below the top line without
// the time.
func plainAreaY() int { return trackTopY + smoothSmall.capH + trackRuleGap + trackAreaGap }

// bigClock lays out the time large below the header, "AM"/"PM" small under
// it: the screen without a track.
func (f *trackFrame) bigClock(t TrackText, now time.Time) {
	hm, ampm := clockText(now, t.Hour12)
	areaY := plainAreaY()
	h := smoothClock.capH
	if ampm != "" {
		h += trackGap + smoothSmall.capH
	}
	y := areaY + (Height-areaY-h)/2
	f.lines = append(f.lines, marqueeLine{s: hm, f: smoothClock, lvl: lvlTrackTitle, x0: 0, w: Width, y: y + smoothClock.capH})
	if ampm != "" {
		f.lines = append(f.lines, marqueeLine{s: ampm, f: smoothSmall, lvl: lvlTrackArtist, x0: 0, w: Width, y: y + h})
	}
}

// render draws the laid-out screen into buf at marquee step, with the
// playing symbol in state a, offset by dx, dy pixels.
func (f trackFrame) render(a trackAnim, step, dx, dy int, buf []byte) {
	clear(buf)
	c := canvas(buf)
	if len(f.lines) == 0 {
		return
	}
	if !f.noBars {
		c.bars(1+dx, f.barsBottom+dy, a)
	}
	f.drawStanding(c, dx, dy)
	for _, l := range f.lines {
		l.draw(c, step, dx, dy)
	}
}

// drawStanding paints the parts of the screen that are not text: the rule
// and the sleep countdown's moon.
func (f trackFrame) drawStanding(c canvas, dx, dy int) {
	if f.ruleY >= 0 {
		c.fill(24+dx, f.ruleY+dy, Width-48, 1, lvlTrackRule)
	}
	if f.moon {
		c.moonIcon(f.moonX+dx, f.moonY+dy, lvlTrackMoon)
	}
}

// renderTrack draws the track screen for t (already sanitised) at the time
// now and marquee step into buf, offset by dx, dy pixels.
func renderTrack(t TrackText, now time.Time, a trackAnim, step, dx, dy int, buf []byte) {
	trackLayout(t, now).render(a, step, dx, dy, buf)
}

// trackAnim is the moving part of the playback screen: the playing symbol.
type trackAnim struct {
	playing bool
	frame   int // advances every barsFrame while playing
}

// The playing symbol: three bars at the left of the top line that move
// while the speaker plays and lie flat when it does not. They show that
// music runs, not its level: STR does not see the audio the speaker plays.
const (
	barsN     = 3
	barsBarW  = 2
	barsW     = barsN*barsBarW + barsN - 1 // 8 px
	barsGap   = 3
	barsMaxH  = 7
	barsFrame = 400 * time.Millisecond
)

// barHeights is the height of each bar for frame: a fixed pseudo-random walk
// between 2 and barsMaxH pixels, flat at 2 when not playing.
func barHeights(a trackAnim) [barsN]int {
	var h [barsN]int
	for i := range h {
		h[i] = 2
		if a.playing {
			x := uint32(a.frame*barsN+i) * 2654435761
			h[i] = 2 + int(x>>27)%(barsMaxH-1)
		}
	}
	return h
}

// bars paints the playing symbol with its left edge at x and its bottom
// row just above bottom.
func (c canvas) bars(x, bottom int, a trackAnim) {
	lvl := lvlTrackClock
	if !a.playing {
		lvl = lvlTrackRule
	}
	for i, h := range barHeights(a) {
		c.fill(x+i*(barsBarW+1), bottom-h, barsBarW, h, lvl)
	}
}
