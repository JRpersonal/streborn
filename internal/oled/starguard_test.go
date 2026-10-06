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
	wall := (Width - 1 - sgShipHalf) * 16
	moved := func(g *Starguard, steps int, every int, key int, states ...int) (dist, stopped int) {
		x0 := g.shipX16
		for i := 0; i < steps; i++ {
			if every > 0 && i%every == 0 {
				for _, st := range states {
					g.Key(key, st)
				}
			}
			before := g.shipX16
			g.Step()
			if g.shipX16 == before && g.shipX16 != wall && g.shipX16 != sgShipHalf*16 {
				stopped++
			}
		}
		return g.shipX16 - x0, stopped
	}

	// a tap glides a short way left and stops
	g := NewStarguard(2)
	g.Key(KeyThumbsDown, KeyPressed)
	g.Step()
	g.Key(KeyThumbsDown, KeyReleased)
	if d, _ := moved(g, 40, 0, 0); d >= 0 || d < -40*16 {
		t.Fatalf("a tap must glide a short way left: %d", d)
	}

	// one press held until the release: moves the whole time, then stops
	g = NewStarguard(2)
	g.shipX16 = 10 * 16
	g.Key(KeyThumbsUp, KeyPressed)
	if d, stops := moved(g, 60, 0, 0); d < 60*20 || stops > 0 {
		t.Fatalf("holding (one press) must move right the whole time: %d px/16, %d stops", d, stops)
	}
	g.Key(KeyThumbsUp, KeyReleased)
	moved(g, sgCoast+1, 0, 0)
	if d, _ := moved(g, 20, 0, 0); d != 0 {
		t.Fatalf("after the release and the coast it must stop: %d", d)
	}

	// press/release pairs twice a second and repeat events both count as held
	for _, states := range [][]int{{KeyPressed, KeyReleased}, {KeyRepeat}} {
		g = NewStarguard(2)
		g.shipX16 = 10 * 16
		g.Key(KeyThumbsUp, KeyPressed)
		if d, stops := moved(g, 60, 15, KeyThumbsUp, states...); d <= 0 || stops > 0 {
			t.Fatalf("held as %v must move without stopping: %d, %d stops", states, d, stops)
		}
	}

	// held longer, it speeds up
	g = NewStarguard(2)
	g.shipX16 = 10 * 16
	g.Key(KeyThumbsUp, KeyPressed)
	slow, _ := moved(g, 10, 0, 0)
	moved(g, sgFastFrom, 0, 0)
	fast, _ := moved(g, 10, 0, 0)
	if fast <= slow {
		t.Fatalf("holding must speed up: %d then %d", slow, fast)
	}

	// a lost release does not drive the ship for good
	g = NewStarguard(2)
	g.Key(KeyThumbsDown, KeyPressed)
	moved(g, sgKeyStale+sgCoast+2, 0, 0)
	if g.keyDown {
		t.Fatal("a key with no events for 5 s must count as released")
	}

	// other keys do nothing
	g = NewStarguard(2)
	g.Key(KeyPrev, KeyPressed)
	if d, _ := moved(g, 10, 0, 0); d != 0 {
		t.Fatal("only the thumbs steer")
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
