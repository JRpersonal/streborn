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
	g := NewStarguard(2)
	x0 := g.shipX16
	g.Key(KeyThumbsDown, KeyPressed)
	for range 12 {
		g.Step()
	}
	tap := x0 - g.shipX16
	if tap <= 0 {
		t.Fatalf("thumbs down must steer left: %d -> %d", x0, g.shipX16)
	}
	// held: the remote repeats the press about every 15 steps; the ship
	// keeps gliding, faster, without stopping in between
	x1 := g.shipX16
	stopped := 0
	for i := 0; i < 60; i++ {
		if i%15 == 0 {
			g.Key(KeyThumbsUp, KeyPressed)
		}
		before := g.shipX16
		g.Step()
		if g.shipX16 == before && g.shipX16 < (Width-1-sgShipHalf)*16 {
			stopped++
		}
	}
	if g.shipX16 <= x1 || stopped > 0 {
		t.Fatalf("holding thumbs up must glide right without stopping: moved %d, stopped %d steps", g.shipX16-x1, stopped)
	}
	// releases and other keys do nothing
	before := g.shipX16
	g.moveSteps = 0
	g.Key(KeyThumbsUp, KeyReleased)
	g.Key(KeyPrev, KeyPressed)
	g.Step()
	if g.shipX16 != before {
		t.Fatal("only thumbs presses steer")
	}
}

func TestStarguardWaveClearUpgrades(t *testing.T) {
	g := NewStarguard(3)
	g.banner = 0
	want := append([]string(nil), sgUpgrades...)
	want = append(want, sgExtraLife)
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
