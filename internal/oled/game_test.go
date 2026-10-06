package oled

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"path/filepath"
	"testing"
	"time"
)

func TestBlockfallEndsWithoutInput(t *testing.T) {
	g := NewBlockfall(1)
	steps := 0
	for !g.Over && steps < 30*60*10 {
		g.Step()
		steps++
	}
	if !g.Over {
		t.Fatal("a round without input must end by itself")
	}
	if steps > 30*60*5 {
		t.Fatalf("idle round took %d steps (%.0f s)", steps, float64(steps)/30)
	}
	if g.Score == 0 {
		t.Fatal("placed pieces must score")
	}
}

func TestBlockfallRowClearScores(t *testing.T) {
	g := NewBlockfall(2)
	// fill the bottom row except where the bar piece will land
	for x := 0; x < bfW; x++ {
		g.board[bfH-1][x] = 9
	}
	g.board[bfH-1][0], g.board[bfH-1][1], g.board[bfH-1][2] = 0, 0, 0
	g.kind, g.rot, g.x, g.y = 0, 0, 0, 0 // bar 3, flat
	g.Key(KeyThumbsDown, KeyPressed)
	for i := 0; i < 200 && g.Lines == 0; i++ {
		g.Step()
	}
	if g.Lines != 1 {
		t.Fatalf("lines %d, want 1", g.Lines)
	}
	if g.Score != 1+rowPoints[1] {
		t.Fatalf("score %d, want %d", g.Score, 1+rowPoints[1])
	}
}

func TestBlockfallTapAndHold(t *testing.T) {
	g := NewBlockfall(3)
	x0 := g.x
	g.Key(KeyPrev, KeyPressed)
	if g.x != x0-1 {
		t.Fatalf("a tap moves one column: %d -> %d", x0, g.x)
	}
	// an early repeat and a late release (the remote sends both on a short
	// tap) must not move it again
	g.Step()
	g.Key(KeyPrev, KeyRepeat)
	for i := 0; i < 15; i++ {
		g.Step()
	}
	g.Key(KeyPrev, KeyReleased)
	if g.x != x0-1 {
		t.Fatalf("a tap moved %d columns", x0-g.x)
	}
	// holding: repeats after the delay slide one column each
	g.Key(KeyNext, KeyPressed)
	x := g.x
	for i := 0; i < repeatDelay; i++ {
		g.Step()
	}
	g.Key(KeyNext, KeyRepeat)
	g.Key(KeyNext, KeyRepeat)
	if g.x != x+2 {
		t.Fatalf("repeats after the delay must slide: %d -> %d", x, g.x)
	}
}

func TestBlockfallFrameFits(t *testing.T) {
	g := NewBlockfall(4)
	g.Score = 9999
	buf := make([]byte, FrameSize)
	g.Frame(buf)
	g.Over = true
	g.Frame(buf)
	for _, v := range buf {
		if v > 15 {
			t.Fatalf("grey level %d", v)
		}
	}
	if bfX0+bfW*bfCell >= Width || bfY0 < 0 {
		t.Fatal("board does not fit the panel")
	}
}

func TestEggCode(t *testing.T) {
	now := time.Now()
	var p []eggPress
	for i, k := range eggCode {
		p = append(p, eggPress{k, now.Add(time.Duration(i) * 500 * time.Millisecond)})
	}
	if !codeMatches(p, p[len(p)-1].at) {
		t.Fatal("the code must match")
	}
	if codeMatches(p, p[0].at.Add(eggWindow+time.Second)) {
		t.Fatal("a code typed too slowly must not match")
	}
	p[3].name = "THUMBS_UP"
	if codeMatches(p, p[len(p)-1].at) {
		t.Fatal("a wrong key must not match")
	}
}

func TestParseKeyLine(t *testing.T) {
	line := []byte("<135>BoseApp[1982]: [(002894):IrDevice:DEBUG]IR Key event: Key()=5, State()=1, Producer()=2\n")
	k, ok := parseKeyLine(line)
	if !ok || k.Key != 5 || k.State != KeyReleased {
		t.Fatalf("got %+v %v", k, ok)
	}
	if _, ok := parseKeyLine([]byte("something else")); ok {
		t.Fatal("unrelated line parsed")
	}
}

func TestSaveRound(t *testing.T) {
	dir := t.TempDir()
	ScoresPath, ScreenshotPath = filepath.Join(dir, "s.json"), filepath.Join(dir, "s.png")
	defer func() {
		ScoresPath, ScreenshotPath = "/mnt/nv/streborn/blockfall.json", "/mnt/nv/streborn/blockfall-last.png"
	}()
	screen := make([]byte, FrameSize)
	screen[0] = 15
	if s, _ := SaveRound(Result{}, time.Now()); s.Rounds != 0 {
		t.Fatal("an empty round must not be written")
	}
	SaveRound(Result{Score: 120, Lines: 3, Screen: screen}, time.Now())
	s, _ := SaveRound(Result{Score: 80, Lines: 1, Screen: screen}, time.Now())
	if s.Best != 120 || s.Last != 80 || s.Rounds != 2 {
		t.Fatalf("scores %+v", s)
	}
	if got := LoadScores(); got.Best != 120 {
		t.Fatalf("reload %+v", got)
	}
	img, err := png.Decode(bytes.NewReader(ScreenPNG(screen)))
	if err != nil || img.Bounds().Dx() != Width*3 {
		t.Fatalf("png: %v", err)
	}
}

func TestWavHeaderAndSynth(t *testing.T) {
	h := wavHeader()
	if string(h[0:4]) != "RIFF" || binary.LittleEndian.Uint16(h[22:]) != 2 || binary.LittleEndian.Uint32(h[24:]) != 44100 {
		t.Fatal("the speaker plays 44.1 kHz stereo only")
	}
	var s Synth
	buf := make([]int16, synthRate)
	s.Render(buf)
	peak := 0
	for _, v := range buf {
		peak = max(peak, int(v), -int(v))
	}
	if peak < 16000 {
		t.Fatalf("mix too quiet for a low speaker volume: peak %d", peak)
	}
}
