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
// between (no animation, no scrolling: long text is wrapped and cut), so a
// held panel costs no CPU. Every few minutes the whole layout moves by a
// pixel so no pixel sits under the same text all evening.
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
)

// TrackText is what the track screen shows. Station is the small top line;
// Title is the main text, Artist the line above it. Either of Artist and
// Title may be empty (the per-speaker mode shows only one of them).
type TrackText struct {
	Station, Artist, Title string
}

func (t TrackText) empty() bool { return t.Artist == "" && t.Title == "" }

// sanitized returns t made drawable (SanitizeText rules, no cut yet: the
// layout wraps and cuts).
func (t TrackText) sanitized() TrackText {
	return TrackText{
		Station: SanitizeText(t.Station, trackTextMax),
		Artist:  SanitizeText(t.Artist, trackTextMax),
		Title:   SanitizeText(t.Title, trackTextMax),
	}
}

const (
	// trackTextMax bounds what is kept of one field before wrapping; far more
	// than the panel can show, so the cut with "..." is the layout's.
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
	foreign    bool // the speaker is on a source that is not STR's
	quit       chan struct{}
	done       chan struct{}
	stopWhy    string
	redraw     chan struct{}
	wake       chan struct{}
	startOnce  sync.Once

	// seams; newTrackScreen wires the real ones
	now        func() time.Time
	supported  func() bool
	ready      func() bool
	gameActive func() bool
	noticeBusy func() bool
	tryLock    func() bool
	unlock     func()
	hold       func(draw func([]byte), redraw, quit <-chan struct{}, tick time.Duration) error
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
		ready:      boseAppDisplayReady,
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

// ClearTrack ends the track screen and hands the panel back to BoseApp.
// Blocks (bounded) until the panel is let go, so a caller that exits next
// leaves nothing held. reason goes to the log.
func ClearTrack(reason string) { tracks.clear(reason) }

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

// SetTrackSource tells the track screen which source the speaker switched
// to and whether it is one of STR's own. On any other source (standby, AUX,
// Bluetooth, a firmware service such as TuneIn that shows its own titles) the
// track leaves the panel and titles are ignored until STR's source is back.
// Does not block (it is called from the gabbo read loop): the flag and the
// wanted text change at once, in order, and only letting go of the panel
// runs in the background.
func SetTrackSource(name string, ours bool) { tracks.setSource(name, ours) }

func (s *trackScreen) setSource(name string, ours bool) {
	s.mu.Lock()
	s.foreign = !ours
	had := s.want != nil
	if !ours {
		s.want = nil
	}
	s.mu.Unlock()
	if ours || !had {
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
	if s.want != nil && *s.want == t {
		s.mu.Unlock()
		return
	}
	s.want = &t
	holding := s.holding
	s.mu.Unlock()
	s.log().Debug("display track text", "station", t.Station, "artist", t.Artist, "title", t.Title)
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
	s.mu.Unlock()
	if !s.stop(reason) && had {
		s.log().Info("display track overlay off", "reason", reason, "held", false)
	}
}

func (s *trackScreen) pause(d time.Duration, reason string) {
	s.mu.Lock()
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
	s.quit, s.done, s.stopWhy = make(chan struct{}), make(chan struct{}), ""
	quit, done := s.quit, s.done
	// a redraw left from before the hold would only draw the same text twice
	select {
	case <-s.redraw:
	default:
	}
	s.mu.Unlock()

	start := s.now()
	err := s.hold(s.draw, s.redraw, quit, s.shiftEvery)

	s.unlock()
	s.mu.Lock()
	s.holding = false
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
	s.mu.Unlock()
	close(done)
	switch {
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
	if s.want != nil {
		t = *s.want
	}
	first := !s.drawn
	s.drawn = true
	step := 0
	if s.shiftEvery > 0 {
		step = int(s.now().UnixNano() / int64(s.shiftEvery))
	}
	s.mu.Unlock()
	if first {
		s.log().Info("display track overlay on", "station", t.Station, "artist", t.Artist, "title", t.Title)
	}
	dx, dy := trackShift(step)
	renderTrack(t, dx, dy, buf)
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

// Track screen layout: grey levels and character widths per font scale.
const (
	lvlTrackStation = 6
	lvlTrackRule    = 3
	lvlTrackArtist  = 9
	lvlTrackTitle   = 15
	smallCols       = 21 // 6 px a character at scale 1
	bigCols         = 10 // 12 px a character at scale 2
	smallLineH      = 10
	bigLineH        = 18
	trackTopY       = 5
	trackRuleY      = 16
	trackAreaY      = 20 // the text block is centred between here and the bottom
	trackGap        = 6  // between the artist and the title block
)

// trackLines is the text of the track screen broken into panel lines.
type trackLines struct {
	station string
	artist  []string // scale 1
	title   []string
	big     bool // title at scale 2
}

// layoutTrack breaks t (already sanitised) into lines: the station cut to one
// line, the artist wrapped onto at most two, the title at double size when it
// fits on two lines of ten, otherwise small on up to three lines (two when an
// artist is shown). Text that does not fit ends in "...".
func layoutTrack(t TrackText) trackLines {
	l := trackLines{station: cutLine(t.Station, smallCols)}
	l.artist, _ = wrapText(t.Artist, smallCols, 2)
	if w, cut := wrapText(t.Title, bigCols, 2); len(w) > 0 && !cut && !hasLongWord(t.Title, bigCols) {
		l.title, l.big = w, true
		return l
	}
	maxTitle := 4
	if len(l.artist) > 0 {
		maxTitle = 3
	}
	l.title, _ = wrapText(t.Title, smallCols, maxTitle)
	return l
}

// hasLongWord reports whether a word of s is longer than cols: split across
// two big lines it reads worse than small on one.
func hasLongWord(s string, cols int) bool {
	for _, w := range strings.Fields(s) {
		if len(w) > cols {
			return true
		}
	}
	return false
}

// cutLine returns s cut to cols characters, ending in "..." when it was longer.
func cutLine(s string, cols int) string {
	if len(s) <= cols {
		return s
	}
	return strings.TrimRight(s[:cols-3], " ") + "..."
}

// wrapText breaks s into at most maxLines lines of cols characters at spaces,
// splitting a word only when it alone is longer than a line. When s does not
// fit, the last line ends in "..." and cut is true. s is sanitised, so one
// byte is one character.
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

// renderTrack draws the track screen for t (already sanitised) into buf,
// offset by dx, dy pixels.
func renderTrack(t TrackText, dx, dy int, buf []byte) {
	clear(buf)
	c := canvas(buf)
	l := layoutTrack(t)
	if l.station != "" {
		c.textAt(l.station, trackTopY+dy, 1, lvlTrackStation, dx)
		c.fill(24+dx, trackRuleY+dy, Width-48, 1, lvlTrackRule)
	}
	titleH := len(l.title) * smallLineH
	if l.big {
		titleH = len(l.title) * bigLineH
	}
	h := len(l.artist)*smallLineH + titleH
	if len(l.artist) > 0 && len(l.title) > 0 {
		h += trackGap
	}
	y := trackAreaY + (Height-trackAreaY-h)/2 + dy
	for _, s := range l.artist {
		c.textAt(s, y, 1, lvlTrackArtist, dx)
		y += smallLineH
	}
	if len(l.artist) > 0 && len(l.title) > 0 {
		y += trackGap
	}
	for _, s := range l.title {
		if l.big {
			c.textAt(s, y, 2, lvlTrackTitle, dx)
			y += bigLineH
		} else {
			c.textAt(s, y, 1, lvlTrackTitle, dx)
			y += smallLineH
		}
	}
}

// textAt paints s centred horizontally, moved dx pixels, with its top at y.
func (c canvas) textAt(s string, y, sc, lvl, dx int) {
	if s == "" {
		return
	}
	w := len(s)*6*sc - sc
	x := (Width-w)/2 + dx
	for _, ch := range s {
		c.glyph(ch, x, y, sc, lvl)
		x += 6 * sc
	}
}
