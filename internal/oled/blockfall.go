package oled

import (
	"math/rand/v2"
	"strconv"
)

// Blockfall is STR's hidden game for the Portable's display: a falling-block
// game of our own (12x16 board of 6 px blocks, a mixed set of 3-, 4- and
// 5-cell pieces, grey LED look), played with the remote's keys that have no
// function on the speaker. Previous/Skip move, thumbs up rotates, thumbs down
// sends the piece down fast; holding Previous/Skip slides (driven by the
// remote's repeat events). Full rows clear.
// There is no autopilot: without input the pieces stack up and the round
// ends by itself.
//
// This file is the pure game: state, rules and rendering into a grey-level
// frame. Display, keys, music and the round's lifecycle live elsewhere.

const (
	bfW, bfH  = 12, 16
	bfCell    = 6
	bfX0      = Width - bfW*bfCell - 3 // board on the right, border at x 125
	bfY0      = Height - bfH*bfCell    // 4
	bfFrameHz = 30
)

// Key codes in BoseApp's KEY_VAL order, as the speaker's own key trace names
// them.
const (
	KeyPlay       = 0
	KeyPause      = 1
	KeyStop       = 2
	KeyPrev       = 3
	KeyNext       = 4
	KeyThumbsUp   = 5
	KeyThumbsDown = 6
	KeyPower      = 8
	KeyPreset1    = 12 // .. KeyPreset1+5 for preset 6
	KeyAux        = 18
)

type bfPt struct{ x, y int }

// bfShapes is our own piece selection, deliberately not the classic seven.
var bfShapes = [][]bfPt{
	{{0, 0}, {1, 0}, {2, 0}},                 // bar 3
	{{0, 0}, {1, 0}, {0, 1}},                 // corner 3
	{{0, 0}, {1, 0}, {0, 1}, {1, 1}},         // square 4
	{{0, 0}, {1, 0}, {2, 0}, {1, 1}},         // tee 4
	{{1, 0}, {0, 1}, {1, 1}, {2, 1}, {1, 2}}, // plus 5
	{{0, 0}, {2, 0}, {0, 1}, {1, 1}, {2, 1}}, // cup 5
	{{0, 0}, {0, 1}, {1, 1}, {1, 2}},         // step 4
	{{0, 0}, {1, 0}, {2, 0}, {3, 0}, {0, 1}}, // long hook 5
}

// bfRotations holds every distinct rotation of every shape, normalised.
var bfRotations = func() [][][]bfPt {
	var all [][][]bfPt
	for _, s := range bfShapes {
		var out [][]bfPt
		cur := s
		for r := 0; r < 4; r++ {
			minX, minY := 99, 99
			for _, c := range cur {
				minX, minY = min(minX, c.x), min(minY, c.y)
			}
			norm := make([]bfPt, len(cur))
			for i, c := range cur {
				norm[i] = bfPt{c.x - minX, c.y - minY}
			}
			dup := false
			for _, o := range out {
				if sameCells(o, norm) {
					dup = true
				}
			}
			if !dup {
				out = append(out, norm)
			}
			next := make([]bfPt, len(cur))
			for i, c := range cur {
				next[i] = bfPt{-c.y, c.x}
			}
			cur = next
		}
		all = append(all, out)
	}
	return all
}()

func sameCells(a, b []bfPt) bool {
	if len(a) != len(b) {
		return false
	}
	for _, p := range a {
		found := false
		for _, q := range b {
			if p == q {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func shapeWidth(s []bfPt) int {
	w := 0
	for _, c := range s {
		w = max(w, c.x+1)
	}
	return w
}

// Blockfall is one round.
type Blockfall struct {
	board      [bfH][bfW]int // 0 empty, else grey level
	kind, rot  int
	next       int
	x, y       int
	fast       bool
	Score      int
	Lines      int
	Over       bool
	r          *rand.Rand
	clearing   []int
	clearTimer int
	frame      int
	slideDir   int  // -1 left, 1 right: the column key last pressed
	slideDown  bool // that key is held
	slideAge   int  // steps since the held key's last event
	slideWait  int  // steps left before the held key starts to slide
	slideCD    int  // steps left to the next column of a slide
	slid       int  // columns moved in this slide, for the speed-up
	best       int  // the record before this round
}

func (g *Blockfall) setBest(n int) { g.best = n }

// NewBlockfall starts a round.
func NewBlockfall(seed uint64) *Blockfall {
	g := &Blockfall{r: rand.New(rand.NewPCG(seed, 0x424c4b46))}
	g.next = g.r.IntN(len(bfShapes))
	g.spawn()
	return g
}

func (g *Blockfall) piece() []bfPt { return bfRotations[g.kind][g.rot] }

// State reports whether the round is over, its score and the cleared rows.
func (g *Blockfall) State() (bool, int, int) { return g.Over, g.Score, g.Lines }

// End finishes the round from outside.
func (g *Blockfall) End() { g.Over = true }

func (g *Blockfall) fits(cells []bfPt, x, y int) bool {
	for _, c := range cells {
		px, py := x+c.x, y+c.y
		if px < 0 || px >= bfW || py >= bfH {
			return false
		}
		if py >= 0 && g.board[py][px] != 0 {
			return false
		}
	}
	return true
}

func (g *Blockfall) spawn() {
	g.kind = g.next
	g.next = g.r.IntN(len(bfShapes))
	g.rot = 0
	g.x = (bfW - shapeWidth(g.piece())) / 2
	g.y = -1
	g.fast = false
	if !g.fits(g.piece(), g.x, g.y) {
		g.Over = true
	}
}

func (g *Blockfall) lock() {
	lvl := 9 + g.kind%4*2
	for _, c := range g.piece() {
		if py := g.y + c.y; py >= 0 {
			g.board[py][g.x+c.x] = lvl
		} else {
			g.Over = true
		}
	}
	g.Score++ // one point per placed piece
	g.clearing = g.clearing[:0]
	for y := 0; y < bfH; y++ {
		full := true
		for x := 0; x < bfW; x++ {
			if g.board[y][x] == 0 {
				full = false
			}
		}
		if full {
			g.clearing = append(g.clearing, y)
		}
	}
	if len(g.clearing) > 0 {
		g.clearTimer = 12
	} else if !g.Over {
		g.spawn()
	}
}

// rowPoints pays more for rows cleared at once.
var rowPoints = []int{0, 10, 30, 60, 100, 150}

func (g *Blockfall) removeCleared() {
	for _, y := range g.clearing {
		for yy := y; yy > 0; yy-- {
			g.board[yy] = g.board[yy-1]
		}
		g.board[0] = [bfW]int{}
	}
	n := len(g.clearing)
	g.Lines += n
	g.Score += rowPoints[min(n, len(rowPoints)-1)]
	g.clearing = g.clearing[:0]
	g.spawn()
}

// Previous/Skip move the piece one column per tap. Held, the piece slides on
// after bfSlideDelay, first at a walking pace and then faster, and stops at
// the release. The remote sends one press and one release per touch with
// nothing in between (key log of a round, 2026-10-06): taps last 150 to
// 470 ms, holds 600 ms and more.
const (
	bfSlideDelay = 15  // steps (0.5 s) before a held key starts to slide
	bfSlideSlow  = 6   // steps between columns at the start of a slide
	bfSlideFast  = 2   // steps between columns once it has picked up speed
	bfKeyStale   = 150 // a held key with no event for 5 s counts as released
)

func (g *Blockfall) Key(key, state int) {
	if g.Over {
		return
	}
	d := 0
	switch key {
	case KeyPrev:
		d = -1
	case KeyNext:
		d = 1
	}
	switch state {
	case KeyPressed:
		switch key {
		case KeyPrev, KeyNext:
			g.move(d)
			g.slideDir, g.slideDown, g.slideAge = d, true, 0
			g.slideWait, g.slideCD, g.slid = bfSlideDelay, 0, 0
		case KeyThumbsUp:
			g.rotate()
		case KeyThumbsDown:
			g.fast = true
		}
	case KeyRepeat:
		if d != 0 && d == g.slideDir {
			g.slideDown, g.slideAge = true, 0
		}
	case KeyReleased:
		if d != 0 && d == g.slideDir {
			g.slideDown = false
		}
	}
}

// stepSlide moves a held piece on, faster the longer the key is held.
func (g *Blockfall) stepSlide() {
	if !g.slideDown {
		return
	}
	if g.slideAge++; g.slideAge > bfKeyStale {
		g.slideDown = false
		return
	}
	switch {
	case g.slideWait > 0:
		g.slideWait--
	case g.slideCD > 0:
		g.slideCD--
	default:
		g.move(g.slideDir)
		g.slid++
		g.slideCD = max(bfSlideFast, bfSlideSlow-g.slid)
	}
}

func (g *Blockfall) move(d int) {
	if g.clearTimer == 0 && g.fits(g.piece(), g.x+d, g.y) {
		g.x += d
	}
}

func (g *Blockfall) rotate() {
	if g.clearTimer > 0 {
		return
	}
	nr := (g.rot + 1) % len(bfRotations[g.kind])
	cells := bfRotations[g.kind][nr]
	for _, kick := range []int{0, -1, 1, -2, 2} { // nudge if it would not fit
		if g.fits(cells, g.x+kick, g.y) {
			g.rot, g.x = nr, g.x+kick
			return
		}
	}
}

// Step advances one frame (bfFrameHz per second).
func (g *Blockfall) Step() {
	g.frame++
	if g.Over {
		return
	}
	g.stepSlide()
	if g.clearTimer > 0 {
		g.clearTimer--
		if g.clearTimer == 0 {
			g.removeCleared()
		}
		return
	}
	fallEvery := max(30-3*(g.Lines/8), 8)
	if g.fast || g.frame%fallEvery == 0 {
		if g.fits(g.piece(), g.x, g.y+1) {
			g.y++
		} else {
			g.lock()
		}
	}
}

func (g *Blockfall) dropY() int {
	y := g.y
	for g.fits(g.piece(), g.x, y+1) {
		y++
	}
	return y
}

// Frame renders the round into buf (grey levels 0..15).
func (g *Blockfall) Frame(buf []byte) {
	clear(buf)
	fillRect(buf, bfX0-2, bfY0-1, 1, bfH*bfCell+1, 4)
	fillRect(buf, bfX0+bfW*bfCell, bfY0-1, 1, bfH*bfCell+1, 4)
	flash := g.clearTimer > 0 && (g.clearTimer/3)%2 == 0
	for y := 0; y < bfH; y++ {
		clearing := false
		for _, cy := range g.clearing {
			if cy == y {
				clearing = true
			}
		}
		for x := 0; x < bfW; x++ {
			v := g.board[y][x]
			if clearing && flash {
				v = 15
			}
			if g.Over && v != 0 {
				v = 3
			}
			if v != 0 {
				fillRect(buf, bfX0+x*bfCell, bfY0+y*bfCell, bfCell-1, bfCell-1, v)
			}
		}
	}
	if !g.Over && g.clearTimer == 0 {
		gy := g.dropY() // landing shadow, then the piece
		for _, c := range g.piece() {
			if py := gy + c.y; py >= 0 {
				fillRect(buf, bfX0+(g.x+c.x)*bfCell+1, bfY0+py*bfCell+1, bfCell-3, bfCell-3, 3)
			}
		}
		for _, c := range g.piece() {
			if py := g.y + c.y; py >= 0 {
				fillRect(buf, bfX0+(g.x+c.x)*bfCell, bfY0+py*bfCell, bfCell-1, bfCell-1, 15)
			}
		}
	}
	// HUD column on the left: the next piece large on top, the score at the
	// bottom right against the board.
	drawSmall(buf, "NEXT", 2, 0, 6)
	nc := bfRotations[g.next][0]
	w := shapeWidth(nc)
	cs := 8
	if w > 3 {
		cs = 6
	}
	px := 2 + (46-w*cs)/2
	for _, c := range nc {
		fillRect(buf, px+c.x*cs, 8+c.y*cs, cs-1, cs-1, 11)
	}
	drawSmall(buf, "SCORE", bfX0-4-19, 78, 6)
	sc := 2
	if g.Score >= 10000 {
		sc = 1
	}
	n := len(strconv.Itoa(g.Score))
	drawDigits(buf, g.Score, bfX0-4-(n*(5*sc+1)-1), Height-1-7*sc, sc, 15)
	if g.Over {
		// GAME OVER plate across the board, NEW BEST under it on a record
		record := g.Score > g.best && g.Score > 0
		h := 24
		if record {
			h = 44
		}
		clearRect(buf, bfX0+2, 38, bfW*bfCell-4, h)
		fillRect(buf, bfX0+2, 38, bfW*bfCell-4, 1, 8)
		fillRect(buf, bfX0+2, 37+h, bfW*bfCell-4, 1, 8)
		drawWord(buf, "GAME", bfX0+(bfW*bfCell-23)/2, 41, 1, 15)
		drawWord(buf, "OVER", bfX0+(bfW*bfCell-23)/2, 51, 1, 15)
		if record {
			drawWord(buf, "NEW", bfX0+(bfW*bfCell-17)/2, 63, 1, 15)
			drawWord(buf, "BEST", bfX0+(bfW*bfCell-23)/2, 72, 1, 15)
		}
	}
	if g.best > 0 && (!g.Over || g.Score <= g.best) {
		// the record to beat, in the left column above the score
		drawSmall(buf, "BEST", bfX0-4-15, 54, 6)
		n := len(strconv.Itoa(g.best))
		drawDigits(buf, g.best, bfX0-4-(n*6-1), 61, 1, 9)
	}
}

var digits5x7 = [10][7]string{
	{"01110", "10001", "10011", "10101", "11001", "10001", "01110"},
	{"00100", "01100", "00100", "00100", "00100", "00100", "01110"},
	{"01110", "10001", "00001", "00010", "00100", "01000", "11111"},
	{"11110", "00001", "00001", "01110", "00001", "00001", "11110"},
	{"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	{"11111", "10000", "11110", "00001", "00001", "10001", "01110"},
	{"00110", "01000", "10000", "11110", "10001", "10001", "01110"},
	{"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	{"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	{"01110", "10001", "10001", "01111", "00001", "00010", "01100"},
}

// drawDigits paints n with the 5x7 digits, each dot sc x sc, 1 px apart.
func drawDigits(buf []byte, n, x, y, sc, lvl int) {
	for _, ch := range strconv.Itoa(n) {
		for gy, row := range digits5x7[ch-'0'] {
			for gx, b := range row {
				if b == '1' {
					fillRect(buf, x+gx*sc, y+gy*sc, sc, sc, lvl)
				}
			}
		}
		x += 5*sc + 1
	}
}

// drawWord paints text with the 5x7 letter font, each dot sc x sc.
func drawWord(buf []byte, s string, x, y, sc, lvl int) {
	for _, ch := range s {
		for gy, row := range font[ch] {
			for gx, b := range row {
				if b == '1' {
					fillRect(buf, x+gx*sc, y+gy*sc, sc, sc, lvl)
				}
			}
		}
		x += 6 * sc
	}
}

var letters3x5 = map[rune][5]string{
	'B': {"110", "101", "110", "101", "110"},
	'S': {"111", "100", "111", "001", "111"},
	'C': {"111", "100", "100", "100", "111"},
	'O': {"111", "101", "101", "101", "111"},
	'R': {"110", "101", "110", "101", "101"},
	'E': {"111", "100", "110", "100", "111"},
	'N': {"110", "101", "101", "101", "101"},
	'T': {"111", "010", "010", "010", "010"},
	'X': {"101", "101", "010", "101", "101"},
}

// drawSmall paints a label in the tiny 3x5 font.
func drawSmall(buf []byte, s string, x, y, lvl int) {
	for _, ch := range s {
		for gy, row := range letters3x5[ch] {
			for gx, b := range row {
				if b == '1' {
					fillRect(buf, x+gx, y+gy, 1, 1, lvl)
				}
			}
		}
		x += 4
	}
}
