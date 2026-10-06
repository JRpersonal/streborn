package oled

import (
	"math/rand/v2"
	"strconv"
)

// Starguard: a ship at the bottom of the display holds off waves of aliens
// marching down. Thumbs down and thumbs up steer left and right, the ship
// fires on its own, and every cleared wave brings an upgrade. Our own game,
// our own sprites.

const (
	sgTop      = 9  // the play area starts under the HUD line
	sgShipY    = 92 // top of the ship
	sgShipHalf = 4
	sgCols     = 8
	sgColStep  = 13
	sgRowStep  = 9
	sgAlienW   = 9
	sgAlienH   = 6
	sgBanner   = 75 // steps a wave banner stays up
)

// Sprites, one string per row, '#' lit. Two animation frames per alien kind.
var (
	sgShip = []string{
		"....#....",
		"...###...",
		".#.###.#.",
		"#########",
		"##.###.##",
	}
	sgAliens = [3][2][]string{
		{
			{"...###...", ".#######.", "##.###.##", "#########", ".#.#.#.#.", "#.#...#.#"},
			{"...###...", ".#######.", "##.###.##", "#########", "..#.#.#..", ".#.....#."},
		},
		{
			{".#.....#.", "..#...#..", ".#######.", "##.###.##", "#########", "#.#...#.#"},
			{".#.....#.", "#.#...#.#", "#########", "##.###.##", ".#######.", ".#.....#."},
		},
		{
			{"..#####..", ".#######.", "###.#.###", "#########", "..##.##..", ".##...##."},
			{"..#####..", ".#######.", "###.#.###", "#########", ".##.#.##.", "##.....##"},
		},
	}
	sgSaucer = []string{
		"...#####...",
		".#########.",
		"##.#.#.#.##",
		"..#.....#..",
	}
	// points per alien kind; kind 0 marches in the top row
	sgPoints = [3]int{30, 20, 10}
)

// sgUpgrades are handed out in this order, one per cleared wave; after the
// last one every further wave brings an extra life.
var sgUpgrades = []string{"RAPID FIRE", "DOUBLE SHOT", "SHIELD", "RAPID FIRE", "SPREAD SHOT", "PIERCING"}

const sgExtraLife = "EXTRA LIFE"

type sgShot struct{ x16, y, dx16 int }

type sgBomb struct{ x, y2 int } // y in half pixels

type sgSpark struct{ x16, y16, dx, dy, life int }

type sgStar struct{ x, y16, v, lvl int }

// Starguard is one round.
type Starguard struct {
	r *rand.Rand

	shipX16          int // ship centre, 1/16 px
	moveDir          int
	keyDown          bool // a thumb is held: no release seen since its press
	keyAge           int  // steps since the last event of the held key
	coast            int  // steps the ship keeps gliding after a release
	heldSteps        int  // steps of continuous movement, for the speed-up
	sincePress       int  // steps since the last press, for the double-frame filter
	fireCD           int
	rapid            int
	double, spread   bool
	pierce           bool
	shieldOwned      bool
	shield           bool
	lives, invuln    int
	shots            []sgShot
	bombs            []sgBomb
	sparks           []sgSpark
	stars            []sgStar
	alive            [5][sgCols]bool
	rows             int
	fx, fy, fdir     int
	marchCD, anim    int
	bombCD           int
	saucerX, saucerD int
	saucerCD         int
	wave, banner     int
	bannerText       string
	tick             int

	Score int
	waves int
	over  bool

	audio *LiveAudio // nil when the round has no live music
	best  int        // the record before this round
}

func (g *Starguard) setBest(n int) { g.best = n }

func (g *Starguard) setAudio(la *LiveAudio) { g.audio = la }

func (g *Starguard) cue(notes []int) {
	if g.audio != nil {
		g.audio.Cue(notes)
	}
}

// NewStarguard starts a round.
func NewStarguard(seed uint64) *Starguard {
	g := &Starguard{r: rand.New(rand.NewPCG(seed, 0x53544152)), lives: 3, shipX16: Width / 2 * 16}
	for range 26 {
		g.stars = append(g.stars, sgStar{x: g.r.IntN(Width), y16: (sgTop + g.r.IntN(Height-sgTop)) * 16, v: 2 + g.r.IntN(6), lvl: 1 + g.r.IntN(3)})
	}
	g.saucerCD = 500 + g.r.IntN(400)
	g.startWave(1, "")
	return g
}

// State reports whether the round is over, its score and the waves cleared.
func (g *Starguard) State() (bool, int, int) { return g.over, g.Score, g.waves }

// End finishes the round from outside.
func (g *Starguard) End() { g.over = true }

func (g *Starguard) startWave(n int, upgrade string) {
	g.wave = n
	g.rows = min(3+(n-1)/2, 5)
	for row := range g.alive {
		for col := range g.alive[row] {
			g.alive[row][col] = row < g.rows
		}
	}
	g.fx = (Width - (sgCols-1)*sgColStep - sgAlienW) / 2
	g.fy = sgTop + 4 + min(n-1, 4)*2
	g.fdir = 1
	g.marchCD = 0
	g.bombCD = 60
	g.shots, g.bombs = nil, nil
	if g.shieldOwned {
		g.shield = true
	}
	g.banner = sgBanner
	g.bannerText = upgrade
}

// Key: thumbs down steers left, thumbs up right, for as long as the thumb is
// held. Remotes report a held key in different ways (one press until the
// release, repeat events, or press/release pairs about twice a second), so a
// press or a repeat steers until the release, and after a release the ship
// coasts a moment, which bridges the gap to the next press of a pair. Held a
// while, it speeds up.
func (g *Starguard) Key(key, state int) {
	if g.over {
		return
	}
	d := 0
	switch key {
	case KeyThumbsDown:
		d = -1
	case KeyThumbsUp:
		d = 1
	default:
		return
	}
	switch state {
	case KeyPressed, KeyRepeat:
		// the remote sends two frames for one tap, a quarter second apart:
		// a second press the same way that soon is the same tap
		if state == KeyPressed && d == g.moveDir && !g.keyDown && g.sincePress < sgDoubleFrame {
			return
		}
		if d != g.moveDir {
			g.heldSteps = 0
		}
		if state == KeyPressed {
			g.sincePress = 0
		}
		g.moveDir, g.keyDown, g.keyAge = d, true, 0
	case KeyReleased:
		if d == g.moveDir && g.keyDown {
			g.keyDown = false
			g.coast = sgCoast
		}
	}
}

const (
	sgDoubleFrame = 10  // steps (a third of a second)
	sgCoast       = 16  // steps the ship glides on after a release; bridges a press/release pair
	sgKeyStale    = 150 // a held key with no event for 5 s counts as released
	sgFastFrom    = 15  // steps of holding before the ship speeds up
)

func kindOfRow(row int) int {
	switch {
	case row == 0:
		return 0
	case row <= 2:
		return 1
	}
	return 2
}

func (g *Starguard) alienAt(row, col int) (x, y int) {
	return g.fx + col*sgColStep, g.fy + row*sgRowStep
}

// Step advances one step (30 per second).
func (g *Starguard) Step() {
	g.tick++
	for i := range g.stars {
		s := &g.stars[i]
		s.y16 += s.v
		if s.y16 >= Height*16 {
			s.y16, s.x = sgTop*16, g.r.IntN(Width)
		}
	}
	g.stepSparks()
	if g.over {
		return
	}
	// ship
	g.sincePress++
	if g.keyDown {
		// a lost release must not send the ship into the wall for good
		if g.keyAge++; g.keyAge > sgKeyStale {
			g.keyDown = false
		}
	}
	if g.keyDown || g.coast > 0 {
		speed := 20
		if g.heldSteps >= sgFastFrom {
			speed = 34
		}
		g.shipX16 = max(sgShipHalf*16, min((Width-1-sgShipHalf)*16, g.shipX16+g.moveDir*speed))
		g.heldSteps++
		if !g.keyDown {
			g.coast--
		}
	} else {
		g.heldSteps = 0
	}
	if g.invuln > 0 {
		g.invuln--
	}
	if g.banner > 0 {
		g.banner--
		return
	}
	g.stepShots()
	g.stepAliens()
	g.stepBombs()
	g.stepSaucer()
	if g.aliveCount() == 0 && !g.over {
		g.waves++
		g.Score += 50 * g.wave
		g.cue(CueWaveClear)
		up := sgExtraLife
		if g.waves <= len(sgUpgrades) {
			up = sgUpgrades[g.waves-1]
		}
		switch up {
		case "RAPID FIRE":
			g.rapid++
		case "DOUBLE SHOT":
			g.double = true
		case "SHIELD":
			g.shieldOwned = true
		case "SPREAD SHOT":
			g.spread = true
		case "PIERCING":
			g.pierce = true
		case sgExtraLife:
			g.lives = min(g.lives+1, 5)
		}
		g.startWave(g.wave+1, up)
	}
}

func (g *Starguard) fireInterval() int {
	return [...]int{14, 10, 7}[min(g.rapid, 2)]
}

func (g *Starguard) stepShots() {
	if g.fireCD > 0 {
		g.fireCD--
	} else if len(g.shots) < 9 {
		x := g.shipX16
		if g.double {
			g.shots = append(g.shots, sgShot{x - 48, sgShipY - 1, 0}, sgShot{x + 48, sgShipY - 1, 0})
		} else {
			g.shots = append(g.shots, sgShot{x, sgShipY - 2, 0})
		}
		if g.spread {
			g.shots = append(g.shots, sgShot{x, sgShipY, -7}, sgShot{x, sgShipY, 7})
		}
		g.fireCD = g.fireInterval()
	}
	kept := g.shots[:0]
	for _, s := range g.shots {
		s.y -= 3
		s.x16 += s.dx16
		x := s.x16 / 16
		if s.y < sgTop || x < 0 || x >= Width {
			continue
		}
		if g.hitAlien(x, s.y) && !g.pierce {
			continue
		}
		if g.hitSaucer(x, s.y) && !g.pierce {
			continue
		}
		kept = append(kept, s)
	}
	g.shots = kept
}

// hitAlien removes the alien at (x, y..y+2) and scores it.
func (g *Starguard) hitAlien(x, y int) bool {
	for row := 0; row < g.rows; row++ {
		for col := 0; col < sgCols; col++ {
			if !g.alive[row][col] {
				continue
			}
			ax, ay := g.alienAt(row, col)
			if x >= ax && x < ax+sgAlienW && y+2 >= ay && y < ay+sgAlienH {
				g.alive[row][col] = false
				g.Score += sgPoints[kindOfRow(row)]
				g.burst(ax+sgAlienW/2, ay+sgAlienH/2, 7)
				return true
			}
		}
	}
	return false
}

func (g *Starguard) hitSaucer(x, y int) bool {
	if g.saucerD == 0 || y > sgTop+4 || x < g.saucerX || x >= g.saucerX+len(sgSaucer[0]) {
		return false
	}
	g.Score += 50 * (1 + g.r.IntN(4))
	g.burst(g.saucerX+5, sgTop+2, 12)
	g.saucerD = 0
	g.saucerCD = 600 + g.r.IntN(500)
	return true
}

func (g *Starguard) aliveCount() int {
	n := 0
	for row := 0; row < g.rows; row++ {
		for col := 0; col < sgCols; col++ {
			if g.alive[row][col] {
				n++
			}
		}
	}
	return n
}

func (g *Starguard) stepAliens() {
	alive := g.aliveCount()
	if alive == 0 {
		return
	}
	if g.marchCD > 0 {
		g.marchCD--
		return
	}
	// fewer aliens and later waves march faster, and so does the music
	g.marchCD = max(0, 1+alive*14/(g.rows*sgCols)-min(g.wave-1, 6)/2)
	if g.audio != nil {
		g.audio.SetTempo(100 + 110*(g.rows*sgCols-alive)/(g.rows*sgCols) + 6*min(g.wave-1, 5))
	}
	g.anim ^= 1
	minX, maxX, maxY := Width, 0, 0
	for row := 0; row < g.rows; row++ {
		for col := 0; col < sgCols; col++ {
			if g.alive[row][col] {
				x, y := g.alienAt(row, col)
				minX, maxX, maxY = min(minX, x), max(maxX, x+sgAlienW), max(maxY, y+sgAlienH)
			}
		}
	}
	if (g.fdir > 0 && maxX+1 >= Width) || (g.fdir < 0 && minX-1 <= 0) {
		g.fdir = -g.fdir
		g.fy += 3
		maxY += 3
	} else {
		g.fx += g.fdir
	}
	if maxY >= sgShipY {
		g.burst(g.shipX16/16, sgShipY+2, 16)
		g.lives = 0
		g.over = true
	}
}

func (g *Starguard) stepBombs() {
	if g.bombCD > 0 {
		g.bombCD--
	} else {
		// the lowest alien of a random occupied column drops a bomb
		cols := g.r.Perm(sgCols)
		for _, col := range cols {
			for row := g.rows - 1; row >= 0; row-- {
				if g.alive[row][col] {
					x, y := g.alienAt(row, col)
					g.bombs = append(g.bombs, sgBomb{x + sgAlienW/2, (y + sgAlienH) * 2})
					goto dropped
				}
			}
		}
	dropped:
		g.bombCD = max(12, 50-g.wave*4) + g.r.IntN(30)
	}
	speed := min(3+g.wave/2, 6) // half pixels per step
	kept := g.bombs[:0]
	for _, b := range g.bombs {
		b.y2 += speed
		y := b.y2 / 2
		if y >= Height {
			continue
		}
		sx := g.shipX16 / 16
		if g.invuln == 0 && y >= sgShipY && y < sgShipY+5 && b.x >= sx-sgShipHalf && b.x <= sx+sgShipHalf {
			g.shipHit()
			continue
		}
		kept = append(kept, b)
	}
	g.bombs = kept
}

func (g *Starguard) shipHit() {
	sx := g.shipX16 / 16
	if g.shield {
		g.shield = false
		g.invuln = 45
		g.burst(sx, sgShipY-3, 6)
		g.cue(CueShieldHit)
		return
	}
	g.cue(CueShipLost)
	g.lives--
	g.burst(sx, sgShipY+2, 16)
	g.invuln = 90
	if g.lives <= 0 {
		g.over = true
	}
}

func (g *Starguard) stepSaucer() {
	if g.audio != nil {
		g.audio.SetSaucer(g.saucerD != 0)
	}
	if g.saucerD == 0 {
		if g.saucerCD--; g.saucerCD <= 0 {
			if g.r.IntN(2) == 0 {
				g.saucerX, g.saucerD = -len(sgSaucer[0]), 1
			} else {
				g.saucerX, g.saucerD = Width, -1
			}
		}
		return
	}
	if g.tick%2 == 0 {
		g.saucerX += g.saucerD
	}
	if g.saucerX < -len(sgSaucer[0])-1 || g.saucerX > Width+1 {
		g.saucerD = 0
		g.saucerCD = 600 + g.r.IntN(500)
	}
}

func (g *Starguard) burst(x, y, n int) {
	for i := 0; i < n; i++ {
		g.sparks = append(g.sparks, sgSpark{
			x16: x * 16, y16: y * 16,
			dx: g.r.IntN(41) - 20, dy: g.r.IntN(41) - 24,
			life: 10 + g.r.IntN(8),
		})
	}
}

func (g *Starguard) stepSparks() {
	kept := g.sparks[:0]
	for _, s := range g.sparks {
		s.x16 += s.dx
		s.y16 += s.dy
		s.dy += 2
		if s.life--; s.life > 0 {
			kept = append(kept, s)
		}
	}
	g.sparks = kept
}

func drawSprite(buf []byte, rows []string, x, y, lvl int) {
	for dy, row := range rows {
		for dx, c := range row {
			if c == '#' {
				fillRect(buf, x+dx, y+dy, 1, 1, lvl)
			}
		}
	}
}

// Frame renders the current state into buf (grey levels 0..15).
func (g *Starguard) Frame(buf []byte) {
	clear(buf)
	for _, s := range g.stars {
		fillRect(buf, s.x, s.y16/16, 1, 1, s.lvl)
	}
	// HUD: score on the left, lives as small ships on the right
	drawDigits(buf, g.Score, 1, 0, 1, 15)
	for i := 0; i < g.lives && i < 5; i++ {
		drawSprite(buf, []string{".#.", "###", "#.#"}, Width-4-i*5, 2, 9)
	}
	if g.shieldOwned {
		lvl := 4
		if g.shield {
			lvl = 12
		}
		fillRect(buf, Width-4-5*5-3, 2, 1, 3, lvl)
		fillRect(buf, Width-4-5*5-2, 1, 3, 1, lvl)
		fillRect(buf, Width-4-5*5, 2, 1, 3, lvl)
	}
	fillRect(buf, 0, sgTop-2, Width, 1, 2)
	if g.saucerD != 0 {
		drawSprite(buf, sgSaucer, g.saucerX, sgTop, 13)
	}
	for row := 0; row < g.rows && g.banner == 0; row++ {
		k := kindOfRow(row)
		for col := 0; col < sgCols; col++ {
			if g.alive[row][col] {
				x, y := g.alienAt(row, col)
				drawSprite(buf, sgAliens[k][g.anim], x, y, 15-k*2)
			}
		}
	}
	for _, s := range g.shots {
		fillRect(buf, s.x16/16, s.y, 1, 3, 15)
	}
	for _, b := range g.bombs {
		y := b.y2 / 2
		fillRect(buf, b.x, y, 1, 3, 9)
		fillRect(buf, b.x-1+(y/2)%3, y+1, 1, 1, 6)
	}
	for _, s := range g.sparks {
		fillRect(buf, s.x16/16, s.y16/16, 1, 1, min(15, 4+s.life))
	}
	if !g.over && (g.invuln == 0 || (g.invuln/3)%2 == 0) {
		sx := g.shipX16/16 - sgShipHalf
		drawSprite(buf, sgShip, sx, sgShipY, 15)
		if g.shield {
			fillRect(buf, sx-1, sgShipY-3, sgShipHalf*2+3, 1, 7)
		}
	}
	if g.banner > 0 && !g.over {
		drawWordNum(buf, "WAVE", g.wave, 38, 15)
		// the upgrade blinks in, then stays
		if g.bannerText != "" && (g.banner < sgBanner-30 || (g.banner/5)%2 == 0) {
			drawWord(buf, g.bannerText, (Width-len(g.bannerText)*6+1)/2, 54, 1, 11)
		}
	}
	if g.over {
		// end screen: the score large, then the record or NEW HIGHSCORE
		clearRect(buf, 10, 22, Width-20, 62)
		fillRect(buf, 10, 22, Width-20, 1, 8)
		fillRect(buf, 10, 83, Width-20, 1, 8)
		drawWord(buf, "GAME OVER", (Width-9*6+1)/2, 26, 1, 15)
		n := len(strconv.Itoa(g.Score))
		drawDigits(buf, g.Score, (Width-(n*11-1))/2, 37, 2, 15)
		if g.Score > g.best && g.Score > 0 {
			drawWord(buf, "NEW HIGHSCORE", (Width-13*6+1)/2, 58, 1, 15)
		} else {
			drawWordNum(buf, "BEST", g.best, 58, 9)
		}
		drawWordNum(buf, "WAVE", g.wave, 70, 6)
	}
}

// drawWordNum centres "WORD n" on the panel at row y: the letters from the
// 5x7 font, the number from the 5x7 digits.
func drawWordNum(buf []byte, word string, n, y, lvl int) {
	w := len(word)*6 + 6 + len(strconv.Itoa(n))*6 - 1
	x := (Width - w) / 2
	drawWord(buf, word, x, y, 1, lvl)
	drawDigits(buf, n, x+len(word)*6+6, y, 1, lvl)
}
