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

func TestStarguardSteering(t *testing.T) {
	run := func(g *Starguard, steps int) {
		for range steps {
			g.Step()
		}
	}

	// a tap is one small step and nothing more, even with the remote's
	// second frame a quarter second later
	for _, extraFrame := range []bool{false, true} {
		g := NewStarguard(2)
		x0 := g.shipX16
		g.Key(KeyThumbsDown, KeyPressed)
		run(g, 3)
		g.Key(KeyThumbsDown, KeyReleased)
		run(g, 4)
		if extraFrame {
			g.Key(KeyThumbsDown, KeyPressed)
			run(g, 3)
			g.Key(KeyThumbsDown, KeyReleased)
		}
		run(g, 40)
		if d := g.shipX16 - x0; d != -sgTapStep {
			t.Fatalf("a tap (extra frame %v) moved %d/16 px, want %d", extraFrame, d, -sgTapStep)
		}
	}

	// held: one press until the release moves the whole time, then stops at once
	g := NewStarguard(2)
	g.shipX16 = 10 * 16
	g.Key(KeyThumbsUp, KeyPressed)
	run(g, sgHoldDelay)
	x1 := g.shipX16
	stops := 0
	for range 50 {
		before := g.shipX16
		g.Step()
		if g.shipX16 == before {
			stops++
		}
	}
	if g.shipX16-x1 < 50*20 || stops > 0 {
		t.Fatalf("holding must move right the whole time: %d, %d stops", g.shipX16-x1, stops)
	}
	g.Key(KeyThumbsUp, KeyReleased)
	run(g, sgCoast)
	x2 := g.shipX16
	run(g, 20)
	if g.shipX16 != x2 {
		t.Fatal("after the release it must stop")
	}

	// a remote that repeats press/release twice a second still moves smoothly
	g = NewStarguard(2)
	g.shipX16 = 10 * 16
	stops = 0
	for i := 0; i < 90; i++ {
		switch i % 15 {
		case 0:
			g.Key(KeyThumbsUp, KeyPressed)
		case 9:
			g.Key(KeyThumbsUp, KeyReleased)
		}
		before := g.shipX16
		g.Step()
		if i > 15 && g.shipX16 == before && g.shipX16 < (Width-1-sgShipHalf)*16 {
			stops++
		}
	}
	if stops > 0 {
		t.Fatalf("press/release pairs while held must not stop the ship: %d stops", stops)
	}

	// held longer, it speeds up
	g = NewStarguard(2)
	g.shipX16 = 10 * 16
	g.Key(KeyThumbsUp, KeyPressed)
	run(g, sgHoldDelay)
	a0 := g.shipX16
	run(g, 5)
	slow := g.shipX16 - a0
	run(g, sgFastFrom)
	a1 := g.shipX16
	run(g, 5)
	if fast := g.shipX16 - a1; fast <= slow {
		t.Fatalf("holding must speed up: %d then %d", slow, fast)
	}

	// a lost release does not drive the ship for good
	g = NewStarguard(2)
	g.Key(KeyThumbsDown, KeyPressed)
	run(g, sgKeyStale+sgCoast+2)
	if g.keyDown {
		t.Fatal("a key with no events for 5 s must count as released")
	}

	// other keys do nothing
	g = NewStarguard(2)
	x0 := g.shipX16
	g.Key(KeyPrev, KeyPressed)
	run(g, 10)
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
