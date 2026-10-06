package oled

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"os"
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
	run := func(g *Blockfall, steps int) {
		for range steps {
			g.Step()
		}
	}
	tap := func(g *Blockfall, key, ms, gapMs int) {
		g.Key(key, KeyPressed)
		run(g, ms*30/1000)
		g.Key(key, KeyReleased)
		run(g, gapMs*30/1000)
	}
	// every tap from the measured log is one column, quick ones too
	for _, ms := range []int{150, 300, 470} {
		g := NewBlockfall(3)
		g.kind, g.rot, g.x = 0, 0, 5
		g.fast = false
		x0 := g.x
		tap(g, KeyPrev, ms, 350)
		tap(g, KeyPrev, ms, 350)
		if g.x != x0-2 {
			t.Fatalf("two %d ms taps moved %d columns, want 2", ms, x0-g.x)
		}
	}
	// a hold slides on, faster over time, and stops at the release
	g := NewBlockfall(3)
	g.kind, g.rot, g.x = 0, 0, 0
	g.Key(KeyNext, KeyPressed)
	run(g, bfSlideDelay)
	stepsFor := func(cols int) int {
		x0, n := g.x, 0
		for g.x < x0+cols && n < 200 {
			g.Step()
			n++
		}
		return n
	}
	first, next := stepsFor(3), stepsFor(3)
	if first >= 200 || next >= first {
		t.Fatalf("a hold must slide and pick up speed: %d steps for the first 3 columns, %d for the next 3", first, next)
	}
	g.Key(KeyNext, KeyReleased)
	x2 := g.x
	run(g, 20)
	if g.x != x2 {
		t.Fatal("the slide must stop at the release")
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

func TestEggCodes(t *testing.T) {
	now := time.Now()
	for _, g := range Games {
		var p []eggPress
		// noise before the code must not stop it from matching
		p = append(p, eggPress{"PRESET_1", now.Add(-time.Second)})
		for i, k := range g.Code {
			p = append(p, eggPress{k, now.Add(time.Duration(i) * 500 * time.Millisecond)})
		}
		end := p[len(p)-1].at
		if got := matchCode(p, end); got != g.ID {
			t.Fatalf("%s: matched %q", g.ID, got)
		}
		if codeMatches(p, g.Code, p[1].at.Add(eggWindow+time.Second)) {
			t.Fatalf("%s: a code typed too slowly must not match", g.ID)
		}
		p[len(p)-2].name = "PRESET_2"
		if got := matchCode(p, end); got != "" {
			t.Fatalf("%s: a wrong key matched %q", g.ID, got)
		}
	}
	// no code may be the tail of another, or the shorter one would win
	for _, a := range Games {
		for _, b := range Games {
			if a.ID == b.ID || len(a.Code) > len(b.Code) {
				continue
			}
			tail := b.Code[len(b.Code)-len(a.Code):]
			same := true
			for i := range tail {
				same = same && tail[i] == a.Code[i]
			}
			if same {
				t.Fatalf("%s ends with the code of %s", b.ID, a.ID)
			}
		}
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
	old := ScoreDir
	ScoreDir = t.TempDir()
	defer func() { ScoreDir = old }()
	if filepath.Base(ScoresPath("blockfall")) != "blockfall.json" || filepath.Base(ScreenshotPath("blockfall")) != "blockfall-last.png" {
		t.Fatal("Blockfall's files must keep the names v1.0.5 wrote")
	}
	screen := make([]byte, FrameSize)
	screen[0] = 15
	if s, _ := SaveRound("blockfall", Result{}, time.Now()); s.Rounds != 0 {
		t.Fatal("an empty round must not be written")
	}
	SaveRound("blockfall", Result{Score: 120, Lines: 3, Screen: screen}, time.Now())
	s, _ := SaveRound("blockfall", Result{Score: 80, Lines: 1, Screen: screen}, time.Now())
	if s.Best != 120 || s.Last != 80 || s.Rounds != 2 {
		t.Fatalf("scores %+v", s)
	}
	if got := LoadScores("blockfall"); got.Best != 120 {
		t.Fatalf("reload %+v", got)
	}
	if got := LoadScores("starguard"); got.Rounds != 0 {
		t.Fatalf("each game keeps its own scores: %+v", got)
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
	for _, tr := range []Track{IntroTrack, OverTrack, sgIntroTrack, sgOverTrack} {
		s := Synth{Track: tr}
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
}

// The game-over jingle plays once and then stays silent, so the speaker keeps
// the stream without repeating it.
func TestOverTrackPlaysOnce(t *testing.T) {
	for _, tr := range []Track{OverTrack, sgOverTrack} {
		s := Synth{Track: tr}
		n := int(tr.Length().Seconds()*synthRate) + synthRate/10
		s.Render(make([]int16, n))
		tail := make([]int16, synthRate)
		s.Render(tail)
		for i, v := range tail {
			if v != 0 {
				t.Fatalf("sample %d after the jingle is %d, want silence", i, v)
			}
		}
		if l := tr.Length(); l < 3*time.Second || l > 4*time.Second {
			t.Fatalf("jingle length %v", l)
		}
	}
}

// Each game keeps exactly two small files on NAND, however many rounds are
// played, and a failed write leaves nothing behind.
func TestScoreFilesStayBounded(t *testing.T) {
	old := ScoreDir
	ScoreDir = t.TempDir()
	defer func() { ScoreDir = old }()
	screen := make([]byte, FrameSize)
	for i := 1; i <= 50; i++ {
		for _, g := range Games {
			if _, err := SaveRound(g.ID, Result{Score: i, Screen: screen}, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	}
	entries, _ := os.ReadDir(ScoreDir)
	var total int64
	for _, e := range entries {
		info, _ := e.Info()
		total += info.Size()
	}
	if len(entries) != 2*len(Games) || total > int64(len(Games))*8<<10 {
		t.Fatalf("%d files, %d bytes after 50 rounds per game", len(entries), total)
	}
	// a write into a directory that is gone fails without leftovers
	ScoreDir = filepath.Join(ScoreDir, "missing")
	if _, err := SaveRound("blockfall", Result{Score: 1, Screen: screen}, time.Now()); err == nil {
		t.Fatal("writing into a missing directory must fail")
	}
}

func TestKeysThatEndARound(t *testing.T) {
	for _, k := range []int{KeyPower, KeyPlay, KeyPause, KeyStop, KeyAux, KeyPreset1, KeyPreset1 + 5} {
		if !takesSpeakerBack(k) {
			t.Fatalf("key %d must end the round", k)
		}
	}
	for _, k := range []int{KeyPrev, KeyNext, KeyThumbsUp, KeyThumbsDown, 9, 10, 11} {
		if takesSpeakerBack(k) {
			t.Fatalf("key %d (game, volume or mute) must not end the round", k)
		}
	}
}
