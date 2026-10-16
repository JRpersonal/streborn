package oled

// Display notices: short messages STR draws on the speaker's own panel for
// things the Bose firmware never shows, because STR does them behind its back
// (a key saved by holding it, a key that is empty, keys written back after a
// reboot, "find this speaker").
//
// A notice is a 32x32 symbol and up to three lines of text. It fades in for
// 150 ms, holds, and hands the panel back; BoseApp repaints its own screen
// within a frame, exactly as after the splash. The panel is taken the same way
// as for the splash and the games (panel_linux.go): only BoseApp's flip thread
// is held, never anything else.
//
// One worker shows one notice at a time, at most one every noticeGap. While a
// notice shows, or the gap after its start runs, only the newest request is
// kept (a lower-priority one never replaces a pending higher-priority one),
// and a request identical to the one showing or pending is dropped. The splash
// and the games own the panel first: a game or an update takes it from a
// running notice, the splash at start-up interrupts one and lets it show
// again afterwards, and a notice never starts while a game round runs.
//
// The held-key screen (keyhold.go) is the one exception to the gap: it starts
// at once, ends a running notice (identify excepted), and the outcome of that
// held key replaces it within the same panel hold.
//
// The per-speaker switch is NoticesFlagPath, absent = on. Only the identify
// notice ignores it: it is the answer to a button the user just pressed.

import (
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// NoticesFlagPath is the per-speaker switch for the notices. Absent means on.
// A var so tests can redirect it.
var NoticesFlagPath = "/mnt/nv/streborn/display-notices"

// NoticesEnabled reads the notices switch, with the same parsing as Enabled.
func NoticesEnabled() bool { return flagOn(NoticesFlagPath) }

// NoticeKind names a notice in the log and in tests.
type NoticeKind string

// The notices STR shows.
const (
	NoticeSavedByKey   NoticeKind = "saved-key"
	NoticeSavedFromApp NoticeKind = "saved-app"
	NoticeAlreadyOnKey NoticeKind = "already-on-key"
	NoticeSwapped      NoticeKind = "swapped"
	NoticeNotSaved     NoticeKind = "not-saved"
	NoticeEmptyKey     NoticeKind = "empty-key"
	NoticeRestored     NoticeKind = "keys-restored"
	NoticeIdentify     NoticeKind = "identify"
)

type noticeIcon int

const (
	iconKeyCheck noticeIcon = iota
	iconKeyEqual
	iconKeyX
	iconKeyEmpty
	iconRestored
	iconIdentify
	iconKeyHold
)

// Notice is one message for the panel. Build it with the constructors below.
type Notice struct {
	Kind NoticeKind
	// Key is the preset key the notice is about (the count of keys for
	// NoticeRestored).
	Key int
	// Line1 is the big line, Line2 and Line3 the small ones; all already
	// sanitised to what the font can draw.
	Line1, Line2, Line3 string
	// Hold is how long the notice stays, fade-in included.
	Hold time.Duration
	// Bar, when above 0, draws a bar filled to that fraction (0..1).
	Bar float64
	// Bypass shows the notice even when notices are switched off.
	Bypass bool

	prio int
	icon noticeIcon
}

func (n Notice) same(o Notice) bool {
	return n.Kind == o.Kind && n.Key == o.Key && n.Line1 == o.Line1 && n.Line2 == o.Line2 && n.Line3 == o.Line3
}

func keyNotice(kind NoticeKind, icon noticeIcon, key int, l1, l2 string, hold time.Duration, prio int) Notice {
	return Notice{
		Kind: kind, Key: key, icon: icon, prio: prio, Hold: hold,
		Line1: SanitizeText(l1, line1Max),
		Line2: SanitizeText(l2, line2Max),
	}
}

// SavedByKey: a held preset key stored name.
func SavedByKey(key int, name string) Notice {
	return keyNotice(NoticeSavedByKey, iconKeyCheck, key, "SAVED "+strconv.Itoa(key), name, 2500*time.Millisecond, 2)
}

// SavedFromApp: the app or the phone page stored something on key.
func SavedFromApp(key int) Notice {
	return keyNotice(NoticeSavedFromApp, iconKeyCheck, key, "SAVED "+strconv.Itoa(key), "FROM STR APP", 2*time.Second, 2)
}

// AlreadyOnKey: a save was refused because the item already sits on other.
func AlreadyOnKey(other int) Notice {
	return keyNotice(NoticeAlreadyOnKey, iconKeyEqual, other, "ON KEY "+strconv.Itoa(other), "ALREADY SAVED", 3*time.Second, 2)
}

// Swapped: a held key took over the item that sat on other, and other got
// what the held key had (otherName), or became empty.
func Swapped(key, other int, otherName string) Notice {
	l2 := "OLD ONE NOW ON " + strconv.Itoa(other)
	if otherName == "" {
		l2 = "KEY " + strconv.Itoa(other) + " NOW EMPTY"
	}
	return keyNotice(NoticeSwapped, iconKeyCheck, key, "SAVED "+strconv.Itoa(key), l2, 3*time.Second, 2)
}

// NotSaved: what plays cannot be kept on key.
func NotSaved(key int) Notice {
	return keyNotice(NoticeNotSaved, iconKeyX, key, "NOT SAVED", "SOURCE NOT SUPPORTED", 3*time.Second, 2)
}

// EmptyKey: a hardware press on a key that holds nothing.
func EmptyKey(key int) Notice {
	return keyNotice(NoticeEmptyKey, iconKeyEmpty, key, "KEY "+strconv.Itoa(key), "EMPTY. SET IN STR APP", 2500*time.Millisecond, 1)
}

// KeysRestored: the reconcile wrote n keys back after a reboot.
func KeysRestored(n int) Notice {
	l1 := strconv.Itoa(n) + " KEYS"
	if n == 1 {
		l1 = "1 KEY"
	}
	return keyNotice(NoticeRestored, iconRestored, n, l1, "RESTORED AFTER REBOOT", 2*time.Second, 0)
}

// Identify: "find this speaker", HELLO and its name with pulsing rings.
func Identify(name string) Notice {
	n := keyNotice(NoticeIdentify, iconIdentify, 0, "HELLO", name, 5*time.Second, 3)
	n.Bypass = true
	return n
}

// Text limits per line: the big line at font scale 2 (12 px a character),
// the small ones at scale 1 (6 px).
const (
	line1Max = 10
	line2Max = 21
)

// translit spells the letters the font lacks the way a German or French
// reader would on a 5x7 display.
var translit = map[rune]string{
	'Ä': "AE", 'Ö': "OE", 'Ü': "UE", 'ẞ': "SS", 'ß': "SS",
	'Æ': "AE", 'Œ': "OE", 'Ø': "O", 'Å': "A", 'Ð': "D", 'Þ': "TH", 'Ł': "L", 'Đ': "D", 'Ħ': "H", 'Ŧ': "T", 'Ŋ': "N", 'ı': "I",
	'&': "+", '_': "-", '–': "-", '—': "-", '·': ".", '…': "...", '\'': "", '’': "", '‘': "",
}

// accentBase maps an accented Latin letter to its base letter, for the
// letters NFD would split (stdlib has no normaliser).
var accentBase = func() map[rune]rune {
	groups := map[rune]string{
		'A': "ÀÁÂÃĀĂĄǍȦẠẢẤẦẨẪẬẮẰẲẴẶ",
		'C': "ÇĆĈĊČ",
		'D': "Ď",
		'E': "ÈÉÊËĒĔĖĘĚẸẺẼẾỀỂỄỆ",
		'G': "ĜĞĠĢǦ",
		'H': "Ĥ",
		'I': "ÌÍÎÏĨĪĬĮİỈỊ",
		'J': "Ĵ",
		'K': "ĶǨ",
		'L': "ĹĻĽĿ",
		'N': "ÑŃŅŇ",
		'O': "ÒÓÔÕŌŎŐƠỌỎỐỒỔỖỘỚỜỞỠỢ",
		'R': "ŔŖŘ",
		'S': "ŚŜŞŠȘ",
		'T': "ŢŤȚ",
		'U': "ÙÚÛŨŪŬŮŰŲƯỤỦỨỪỬỮỰ",
		'W': "Ŵ",
		'Y': "ÝŶŸỲỴỶỸ",
		'Z': "ŹŻŽ",
	}
	m := map[rune]rune{}
	for base, s := range groups {
		for _, r := range s {
			m[r] = base
		}
	}
	return m
}()

// SanitizeText makes s drawable: upper case, umlauts and ß spelled out,
// other accents dropped to the base letter, whitespace runs folded to one
// space, everything the font cannot draw removed, and the result cut to
// limit characters.
func SanitizeText(s string, limit int) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToUpper(s) {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		var out string
		if t, ok := translit[r]; ok {
			out = t
		} else if base, ok := accentBase[r]; ok {
			out = string(base)
		} else if _, ok := font[r]; ok {
			out = string(r)
		}
		if out == "" {
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteString(out)
	}
	res := b.String()
	if len(res) > limit {
		res = strings.TrimRight(res[:limit], " ")
	}
	return res
}

// ---- queue ----

const (
	// noticeGap is the least time between the starts of two notices.
	noticeGap = 3 * time.Second
	// noticeBusyWait is how long a notice waits for the panel while the
	// splash holds it (or BoseApp's display is not up yet) before it is
	// dropped.
	noticeBusyWait = 20 * time.Second
	noticeFadeIn   = 0.15 // seconds
)

type noticeQueue struct {
	mu        sync.Mutex
	pending   *Notice
	showing   bool
	cur       Notice
	lastStart time.Time
	curQuit   chan struct{}
	curDone   chan struct{}
	quitOnce  *sync.Once
	requeue   bool
	wake      chan struct{}
	startOnce sync.Once
	// held is the newest held-key session (keyhold.go) until it is over,
	// heldReq the one the worker has still to start, curSession the one on
	// the panel.
	held       *heldSession
	heldReq    *heldSession
	curSession *heldSession

	now        func() time.Time
	sleep      func(time.Duration)
	enabled    func() bool
	supported  func() bool
	gameActive func() bool
	ready      func() bool
	acquire    func() bool
	release    func()
	show       func(n Notice, quit <-chan struct{}) error
	// showSession keeps a held-key session on the panel until it is over.
	showSession func(s *heldSession, quit <-chan struct{}) error
	busyWait    time.Duration
}

var noticeLog atomic.Pointer[slog.Logger]

// SetNoticeLogger sets where the notice lines go (slog.Default otherwise).
func SetNoticeLogger(l *slog.Logger) { noticeLog.Store(l) }

func noticeLogger() *slog.Logger {
	if l := noticeLog.Load(); l != nil {
		return l
	}
	return slog.Default()
}

func newNoticeQueue() *noticeQueue {
	q := &noticeQueue{
		wake:       make(chan struct{}, 1),
		now:        time.Now,
		sleep:      time.Sleep,
		enabled:    NoticesEnabled,
		supported:  panelSupported,
		gameActive: GameActive,
		ready:      boseAppDisplayReady,
		// The track screen holds the panel for long stretches; a notice
		// ends it first and it comes back afterwards (track.go).
		acquire: func() bool {
			yieldTrack("notice")
			return busy.TryLock()
		},
		release:  busy.Unlock,
		show:     playNotice,
		busyWait: noticeBusyWait,
	}
	q.showSession = func(s *heldSession, quit <-chan struct{}) error { return playSession(q, s, quit) }
	return q
}

var notices = newNoticeQueue()

// Show queues n for the panel and returns at once. Safe from any goroutine.
func Show(n Notice) { notices.post(n) }

func (q *noticeQueue) post(n Notice) {
	q.startOnce.Do(func() { go q.loop() })
	q.mu.Lock()
	if q.attach(n) {
		// the outcome of a held key replaces its held-key screen in place
		q.mu.Unlock()
		return
	}
	switch {
	case q.pending != nil && q.pending.same(n), q.showing && q.cur.same(n):
		q.mu.Unlock()
		noticeLogger().Debug("display notice skipped", "kind", string(n.Kind), "key", n.Key, "reason", "same notice already queued")
		return
	case q.pending != nil && q.pending.prio > n.prio:
		q.mu.Unlock()
		noticeLogger().Debug("display notice skipped", "kind", string(n.Kind), "key", n.Key, "reason", "a more important notice is waiting")
		return
	}
	if q.pending != nil {
		noticeLogger().Debug("display notice skipped", "kind", string(q.pending.Kind), "key", q.pending.Key, "reason", "replaced by a newer notice")
	}
	q.pending = &n
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *noticeQueue) loop() {
	for range q.wake {
		q.drain()
	}
}

// drain shows pending notices until none is left.
func (q *noticeQueue) drain() {
	for {
		q.mu.Lock()
		if s := q.heldReq; s != nil {
			// A held key goes first and never waits for the start gap: it
			// must be on the panel before the firmware draws its message.
			q.heldReq = nil
			q.mu.Unlock()
			q.runSession(s)
			continue
		}
		if q.pending == nil {
			q.mu.Unlock()
			return
		}
		if wait := q.lastStart.Add(noticeGap).Sub(q.now()); !q.lastStart.IsZero() && wait > 0 {
			q.mu.Unlock()
			q.sleepGap(wait)
			continue // the newest pending notice wins after the gap
		}
		n := *q.pending
		q.pending = nil
		q.mu.Unlock()
		q.run(n)
	}
}

// sleepGap waits out the start gap, but wakes early for a held key, which
// does not wait for the gap.
func (q *noticeQueue) sleepGap(d time.Duration) {
	for d > 0 {
		q.mu.Lock()
		held := q.heldReq != nil
		q.mu.Unlock()
		if held {
			return
		}
		step := min(d, heldPanelPoll)
		q.sleep(step)
		d -= step
	}
}

func (q *noticeQueue) skip(n Notice, reason string) {
	noticeLogger().Debug("display notice skipped", "kind", string(n.Kind), "key", n.Key, "reason", reason)
}

// run shows one notice, or drops it with a reason.
func (q *noticeQueue) run(n Notice) {
	if !n.Bypass && !q.enabled() {
		q.skip(n, "off")
		return
	}
	if !q.supported() {
		q.skip(n, "unsupported panel")
		return
	}
	deadline := q.now().Add(q.busyWait)
	for {
		if q.gameActive() {
			q.skip(n, "busy")
			return
		}
		if q.ready() && q.acquire() {
			if !q.gameActive() {
				break
			}
			q.release()
			q.skip(n, "busy")
			return
		}
		q.mu.Lock()
		newer := q.pending != nil
		if !newer && q.heldReq != nil {
			// A held key goes first; this notice waits behind it.
			q.pending = &n
			q.mu.Unlock()
			return
		}
		q.mu.Unlock()
		if newer {
			q.skip(n, "replaced by a newer notice")
			return
		}
		if q.now().After(deadline) {
			q.skip(n, "busy")
			return
		}
		q.sleep(500 * time.Millisecond)
	}
	quit := make(chan struct{})
	done := make(chan struct{})
	q.mu.Lock()
	q.showing, q.cur = true, n
	q.curQuit, q.curDone, q.quitOnce, q.requeue = quit, done, &sync.Once{}, false
	q.lastStart = q.now()
	q.mu.Unlock()
	noticeLogger().Info("display notice shown", "kind", string(n.Kind), "key", n.Key,
		"line1", n.Line1, "line2", n.Line2, "durationMs", n.Hold.Milliseconds())
	err := q.show(n, quit)
	q.release()
	q.mu.Lock()
	q.showing = false
	again := q.requeue && q.pending == nil
	if again {
		q.pending = &n
		q.lastStart = time.Time{}
	}
	q.mu.Unlock()
	close(done)
	if err != nil {
		noticeLogger().Warn("display notice ended early", "kind", string(n.Kind), "key", n.Key, "err", err)
	}
}

// active reports whether a notice is showing or waiting; the track screen
// stays off the panel meanwhile.
func (q *noticeQueue) active() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.showing || q.pending != nil
}

// stop takes the panel back from a running notice and waits (bounded) until
// it is released. With requeue the notice shows again afterwards, unless a
// newer one is waiting by then.
func (q *noticeQueue) stop(requeue bool) {
	q.mu.Lock()
	if !q.showing {
		q.mu.Unlock()
		return
	}
	q.requeue = requeue
	quit, done, once := q.curQuit, q.curDone, q.quitOnce
	q.mu.Unlock()
	once.Do(func() { close(quit) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	if requeue {
		select {
		case q.wake <- struct{}{}:
		default:
		}
	}
}

// acquirePanel is how the splash, the update screen and the games take the
// panel: a running notice is ended first (requeue: shown again afterwards),
// then the panel lock is taken, retried briefly while the notice lets go.
func acquirePanel(requeue bool) bool {
	yieldTrack("splash or game")
	notices.stop(requeue)
	for i := 0; i < 20; i++ {
		if busy.TryLock() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// playNotice draws n on the panel until its hold time is up or quit closes.
func playNotice(n Notice, quit <-chan struct{}) error {
	r := newNoticeRenderer(n)
	hold := n.Hold.Seconds()
	return play(func(t float64, buf []byte) bool {
		r.frame(t, buf)
		return t < hold
	}, nil, n.Hold+time.Second, quit)
}

// ---- drawing ----

// Layout on the 128x100 panel.
const (
	iconX, iconY = 48, 4
	line1Y       = 44
	line2Y       = 66
	line3Y       = 76
	barY         = 89
	lvlLine1     = 15
	lvlLine2     = 9
	lvlLine3     = 6
	badgeCX      = 78
	badgeCY      = 34
)

// noticeRenderer draws one notice. Static notices are drawn once at full
// level and only scaled during the fade-in; the identify notice is animated
// and drawn every frame.
type noticeRenderer struct {
	n    Notice
	full []byte
}

func newNoticeRenderer(n Notice) *noticeRenderer {
	r := &noticeRenderer{n: n, full: make([]byte, FrameSize)}
	r.draw(0, r.full)
	return r
}

func (r *noticeRenderer) animated() bool { return r.n.icon == iconIdentify || r.n.icon == iconKeyHold }

// frame renders time t (seconds since the notice started) into buf.
func (r *noticeRenderer) frame(t float64, buf []byte) {
	src := r.full
	if r.animated() {
		r.draw(t, buf)
		src = buf
	}
	fade := 256
	if t < noticeFadeIn {
		fade = int(math.Max(t, 0) / noticeFadeIn * 256)
	}
	if fade >= 256 {
		if !r.animated() {
			copy(buf, src)
		}
		return
	}
	for i, v := range src {
		buf[i] = byte((int(v)*fade + 128) >> 8)
	}
}

// draw paints the notice at full level, animation state at time t.
func (r *noticeRenderer) draw(t float64, buf []byte) {
	clear(buf)
	c := canvas(buf)
	n := r.n
	switch n.icon {
	case iconKeyCheck, iconKeyEqual, iconKeyX:
		c.presetKey(n.Key, false)
		c.badge(n.icon)
	case iconKeyHold:
		c.presetKey(n.Key, false)
		c.badge(n.icon)
		c.busyDots(int(t * 3))
	case iconKeyEmpty:
		c.presetKey(n.Key, true)
	case iconRestored:
		for row := 0; row < 2; row++ {
			for col := 0; col < 3; col++ {
				c.roundRect(iconX+2+col*10, iconY+4+row*10, 8, 8, 2, 10, false)
			}
		}
		c.badge(iconRestored)
	case iconIdentify:
		const cx, cy = iconX + 16, iconY + 16
		c.disc(cx, cy, 3, 15)
		for i, rad := range []int{8, 13} {
			p := 0.5 + 0.5*math.Sin(2*math.Pi*(t*1.2-float64(i)*0.3))
			c.ring(cx, cy, rad, 4+int(11*p))
		}
		if int(t*2)%2 == 0 {
			c.outline(0, 0, Width, Height, 9)
		}
	}
	c.text(n.Line1, line1Y, 2, lvlLine1)
	c.text(n.Line2, line2Y, 1, lvlLine2)
	c.text(n.Line3, line3Y, 1, lvlLine3)
	if n.Bar > 0 {
		c.outline(14, barY, 100, 5, 6)
		w := int(math.Min(n.Bar, 1) * 98)
		c.fill(15, barY+1, w, 3, 11)
	}
}

// canvas draws grey levels 0..15 into a frame; level 0 clears.
type canvas []byte

func (c canvas) px(x, y, lvl int) {
	if x < 0 || y < 0 || x >= Width || y >= Height {
		return
	}
	c[y*Width+x] = byte(max(0, min(lvl, 15)))
}

func (c canvas) fill(x0, y0, w, h, lvl int) {
	for y := y0; y < y0+h; y++ {
		for x := x0; x < x0+w; x++ {
			c.px(x, y, lvl)
		}
	}
}

func (c canvas) outline(x0, y0, w, h, lvl int) {
	c.fill(x0, y0, w, 1, lvl)
	c.fill(x0, y0+h-1, w, 1, lvl)
	c.fill(x0, y0, 1, h, lvl)
	c.fill(x0+w-1, y0, 1, h, lvl)
}

// roundRect draws the 1 px outline of a w x h rectangle with corner radius
// rad; dotted leaves gaps along it.
func (c canvas) roundRect(x0, y0, w, h, rad, lvl int, dotted bool) {
	lo, hi := (rad-1)*(rad-1)+(rad-1), rad*rad+rad
	for y := y0; y < y0+h; y++ {
		for x := x0; x < x0+w; x++ {
			dx := max(x0+rad-x, 0, x-(x0+w-1-rad))
			dy := max(y0+rad-y, 0, y-(y0+h-1-rad))
			d2 := dx*dx + dy*dy
			if d2 <= lo || d2 > hi {
				continue
			}
			if dotted && (x+y)%3 != 0 {
				continue
			}
			c.px(x, y, lvl)
		}
	}
}

func (c canvas) disc(cx, cy, rad, lvl int) {
	for y := cy - rad; y <= cy+rad; y++ {
		for x := cx - rad; x <= cx+rad; x++ {
			if dx, dy := x-cx, y-cy; dx*dx+dy*dy <= rad*rad+rad {
				c.px(x, y, lvl)
			}
		}
	}
}

// ring is a 1 px circle.
func (c canvas) ring(cx, cy, rad, lvl int) { c.arc(cx, cy, rad, 0, 360, lvl) }

// arc is the part of a 1 px circle between the angles a0 and a1 (degrees,
// counter-clockwise from 3 o'clock, screen y pointing down).
func (c canvas) arc(cx, cy, rad int, a0, a1 float64, lvl int) {
	for y := cy - rad - 1; y <= cy+rad+1; y++ {
		for x := cx - rad - 1; x <= cx+rad+1; x++ {
			dx, dy := x-cx, y-cy
			d2 := dx*dx + dy*dy
			if d2 <= rad*rad-rad || d2 > rad*rad+rad {
				continue
			}
			if a1-a0 < 360 {
				a := math.Atan2(float64(-dy), float64(dx)) * 180 / math.Pi
				if a < 0 {
					a += 360
				}
				if a < a0 || a > a1 {
					continue
				}
			}
			c.px(x, y, lvl)
		}
	}
}

// line draws a 1 px line (Bresenham).
func (c canvas) line(x0, y0, x1, y1, lvl int) {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	e := dx + dy
	for {
		c.px(x0, y0, lvl)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * e
		if e2 >= dy {
			e += dy
			x0 += sx
		}
		if e2 <= dx {
			e += dx
			y0 += sy
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// glyph paints one font character with its top left at x, y.
func (c canvas) glyph(ch rune, x, y, sc, lvl int) {
	for gy, row := range font[ch] {
		for gx, b := range row {
			if b == '1' {
				c.fill(x+gx*sc, y+gy*sc, sc, sc, lvl)
			}
		}
	}
}

// text paints s centred horizontally with its top at y.
func (c canvas) text(s string, y, sc, lvl int) {
	if s == "" {
		return
	}
	w := len(s)*6*sc - sc
	x := (Width - w) / 2
	for _, ch := range s {
		c.glyph(ch, x, y, sc, lvl)
		x += 6 * sc
	}
}

// presetKey is the 26x26 rounded key with its number at scale 2.
func (c canvas) presetKey(key int, dotted bool) {
	lvl := 15
	if dotted {
		lvl = 11
	}
	c.roundRect(iconX+3, iconY+3, 26, 26, 4, lvl, dotted)
	if key >= 0 && key <= 9 {
		c.glyph(rune('0'+key), iconX+11, iconY+9, 2, lvl)
	}
}

// badge is the round mark at the key's bottom right corner: a filled disc
// with the symbol cut out of it, set apart from the key by a dark ring.
func (c canvas) badge(icon noticeIcon) {
	const bx, by = badgeCX, badgeCY
	c.disc(bx, by, 8, 0)
	c.disc(bx, by, 7, 15)
	switch icon {
	case iconKeyCheck:
		for o := 0; o < 2; o++ {
			c.line(bx-4, by+o, bx-1, by+3+o, 0)
			c.line(bx-1, by+3+o, bx+4, by-2+o, 0)
		}
	case iconKeyEqual:
		c.fill(bx-4, by-3, 9, 2, 0)
		c.fill(bx-4, by+2, 9, 2, 0)
	case iconKeyX:
		for o := 0; o < 2; o++ {
			c.line(bx-3+o, by-3, bx+3+o, by+3, 0)
			c.line(bx+3+o, by-3, bx-3+o, by+3, 0)
		}
	case iconRestored:
		// a circular arrow: three quarters of a ring and a head at its end
		c.arc(bx, by, 4, 60, 360, 0)
		c.arc(bx, by, 3, 60, 360, 0)
		c.fill(bx+1, by-5, 4, 1, 0)
		c.fill(bx+3, by-5, 1, 4, 0)
	case iconKeyHold:
		// three dots, drawn by busyDots
	}
}

// busyDots are the held-key badge's three dots in a row, cut dark out of the
// disc; the one at step%3 is cut deepest, so the dots run left to right.
func (c canvas) busyDots(step int) {
	for i := 0; i < 3; i++ {
		lvl := 6
		if i == step%3 {
			lvl = 0
		}
		c.fill(badgeCX-5+i*4, badgeCY-1, 3, 3, lvl)
	}
}
