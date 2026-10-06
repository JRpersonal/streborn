package oled

import "testing"

func TestStarguardEndsWithoutInput(t *testing.T) {
	g := NewStarguard(1)
	steps := 0
	for !g.over && steps < 30*60*10 {
		g.Step()
		steps++
	}
	if !g.over {
		t.Fatal("a round without input must end by itself")
	}
	if steps > 30*60*4 {
		t.Fatalf("idle round took %d steps (%.0f s)", steps, float64(steps)/30)
	}
}

// press runs a press of ms milliseconds and then idles for gapMs, at 30
// steps a second, the way the remote delivered it in the measured key log.
func press(g *Starguard, key, ms, gapMs int) {
	g.Key(key, KeyPressed)
	for range ms * 30 / 1000 {
		g.Step()
	}
	g.Key(key, KeyReleased)
	for range gapMs * 30 / 1000 {
		g.Step()
	}
}

func TestStarguardSteering(t *testing.T) {
	// every tap from the measured log (150 to 470 ms) is exactly one step,
	// however quickly the next one follows
	for _, ms := range []int{150, 200, 300, 400, 470} {
		g := NewStarguard(2)
		x0 := g.shipX16
		press(g, KeyThumbsDown, ms, 350)
		press(g, KeyThumbsDown, ms, 350)
		press(g, KeyThumbsDown, ms, 1000)
		if d := x0 - g.shipX16; d != 3*sgTapStep {
			t.Fatalf("three %d ms taps moved %d/16 px, want %d", ms, d, 3*sgTapStep)
		}
	}

	// a hold moves the whole time after the hold delay, then stops at once
	g := NewStarguard(2)
	g.shipX16 = 10 * 16
	g.Key(KeyThumbsUp, KeyPressed)
	for range sgHoldDelay {
		g.Step()
	}
	x1 := g.shipX16
	stops := 0
	for range 30 {
		before := g.shipX16
		g.Step()
		if g.shipX16 == before {
			stops++
		}
	}
	if g.shipX16-x1 < 30*20 || stops > 0 {
		t.Fatalf("holding must move right the whole time: %d, %d stops", g.shipX16-x1, stops)
	}
	g.Key(KeyThumbsUp, KeyReleased)
	x2 := g.shipX16
	for range 20 {
		g.Step()
	}
	if g.shipX16 != x2 {
		t.Fatal("after the release it must stop at once")
	}

	// held longer, it speeds up
	g = NewStarguard(2)
	g.shipX16 = 10 * 16
	g.Key(KeyThumbsUp, KeyPressed)
	for range sgHoldDelay {
		g.Step()
	}
	a0 := g.shipX16
	for range 5 {
		g.Step()
	}
	slow := g.shipX16 - a0
	for range sgFastFrom {
		g.Step()
	}
	a1 := g.shipX16
	for range 5 {
		g.Step()
	}
	if fast := g.shipX16 - a1; fast <= slow {
		t.Fatalf("holding must speed up: %d then %d", slow, fast)
	}

	// a lost release does not drive the ship for good
	g = NewStarguard(2)
	g.Key(KeyThumbsDown, KeyPressed)
	for range sgKeyStale + 2 {
		g.Step()
	}
	if g.keyDown {
		t.Fatal("a key with no events for 5 s must count as released")
	}

	// other keys do nothing
	g = NewStarguard(2)
	x0 := g.shipX16
	g.Key(KeyPrev, KeyPressed)
	for range 10 {
		g.Step()
	}
	if g.shipX16 != x0 {
		t.Fatal("only the thumbs steer")
	}
}

func TestStarguardWaveClearUpgrades(t *testing.T) {
	g := NewStarguard(3)
	g.banner = 0
	// an upgrade for every second cleared wave, then extra lives
	var want []string
	for _, up := range append(append([]string(nil), sgUpgrades...), sgExtraLife) {
		want = append(want, "", up)
	}
	for i, up := range want {
		for row := range g.alive {
			for col := range g.alive[row] {
				g.alive[row][col] = false
			}
		}
		g.bombs = nil
		g.Step()
		if g.waves != i+1 || g.bannerText != up {
			t.Fatalf("wave %d cleared: waves %d, upgrade %q, want %q", i+1, g.waves, g.bannerText, up)
		}
		g.banner = 0
	}
	if g.rapid != 2 || !g.double || !g.shieldOwned || !g.shield || !g.spread || !g.pierce || g.lives != 4 {
		t.Fatalf("upgrades not applied: %+v", g)
	}
	if g.rows != 5 {
		t.Fatalf("later waves bring more rows: %d", g.rows)
	}
}

func TestStarguardShotsScore(t *testing.T) {
	g := NewStarguard(4)
	g.banner = 0
	x, y := g.alienAt(g.rows-1, 3)
	g.shipX16 = (x + sgAlienW/2) * 16
	g.bombCD = 1 << 20
	for i := 0; i < 120 && g.Score == 0; i++ {
		g.Step()
	}
	if g.Score == 0 || g.alive[g.rows-1][3] && g.alive[g.rows-1][2] && g.alive[g.rows-1][4] {
		t.Fatalf("auto fire must hit the alien above the ship (score %d, alien at %d,%d)", g.Score, x, y)
	}
}

func TestStarguardShieldThenLives(t *testing.T) {
	g := NewStarguard(5)
	g.shieldOwned, g.shield = true, true
	g.shipHit()
	if g.shield || g.lives != 3 {
		t.Fatal("the shield takes the first hit")
	}
	g.shipHit()
	g.shipHit()
	g.shipHit()
	if !g.over || g.lives != 0 {
		t.Fatalf("three hits without a shield end the round: lives %d", g.lives)
	}
}

func TestStarguardFrameFits(t *testing.T) {
	g := NewStarguard(6)
	buf := make([]byte, FrameSize)
	for i := 0; i < 600; i++ {
		g.Step()
		g.Frame(buf)
	}
	g.End()
	g.Frame(buf)
	for _, v := range buf {
		if v > 15 {
			t.Fatalf("grey level %d", v)
		}
	}
}

// The end screen carries the score large and says NEW HIGHSCORE on a record,
// so the shared screenshot speaks for itself.
func TestStarguardEndScreen(t *testing.T) {
	lit := func(g *Starguard) int {
		buf := make([]byte, FrameSize)
		g.Frame(buf)
		n := 0
		for _, v := range buf[58*Width : 65*Width] {
			if v == 15 {
				n++
			}
		}
		return n
	}
	g := NewStarguard(8)
	g.setBest(100)
	g.Score = 500
	g.End()
	record := lit(g)
	g.Score = 50
	plain := lit(g)
	if record == 0 || record == plain {
		t.Fatalf("NEW HIGHSCORE line must differ from the BEST line: %d vs %d lit", record, plain)
	}
}
