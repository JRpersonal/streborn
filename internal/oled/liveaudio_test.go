package oled

import "testing"

func peak(buf []int16) int {
	p := 0
	for _, v := range buf {
		p = max(p, int(v), -int(v))
	}
	return p
}

// One stream carries the whole round: intro melody, in-game march, then the
// jingle once and silence.
func TestLiveAudioPhases(t *testing.T) {
	la := NewLiveAudio()
	s := Synth{Track: Track{Live: la}}
	buf := make([]int16, synthRate)
	s.Render(buf)
	if peak(buf) < 16000 {
		t.Fatalf("intro too quiet: %d", peak(buf))
	}
	la.StartGame()
	s.Render(buf)
	march := peak(buf)
	if march < 8000 {
		t.Fatalf("march too quiet: %d", march)
	}
	la.Cue(CueWaveClear)
	s.Render(buf)
	if peak(buf) <= march {
		t.Fatalf("a cue must sound over the march: %d vs %d", peak(buf), march)
	}
	la.SetSaucer(true)
	la.SetTempo(230)
	s.Render(buf)
	if peak(buf) == 0 {
		t.Fatal("fast march with saucer is silent")
	}
	la.GameOver()
	s.Render(make([]int16, int(sgOverTrack.Length().Seconds()*synthRate)+synthRate/10))
	tail := make([]int16, synthRate)
	s.Render(tail)
	if peak(tail) != 0 {
		t.Fatalf("after the jingle the stream must stay silent: %d", peak(tail))
	}
}

func TestLiveAudioCueQueueIsBounded(t *testing.T) {
	la := NewLiveAudio()
	for range 10 {
		la.Cue(CueShieldHit)
	}
	n := 0
	for la.nextCue() != nil {
		n++
	}
	if n != 3 {
		t.Fatalf("queued %d cues, want the last 3", n)
	}
}

// Starguard drives the music: fewer aliens, faster march; a cleared wave and a
// lost ship queue their cues; the saucer warbles while it crosses.
func TestStarguardDrivesLiveAudio(t *testing.T) {
	la := NewLiveAudio()
	g := NewStarguard(7)
	g.setAudio(la)
	g.banner = 0
	g.bombCD = 1 << 20
	for row := 0; row < g.rows; row++ {
		for col := 0; col < sgCols-1; col++ {
			g.alive[row][col] = false
		}
	}
	for range 40 {
		g.Step()
	}
	if la.tempo.Load() < 180 {
		t.Fatalf("a nearly empty wave must march fast: tempo %d", la.tempo.Load())
	}
	g.saucerCD = 1
	g.Step()
	g.Step()
	if !la.saucer.Load() {
		t.Fatal("the saucer must warble while it crosses")
	}
	g.shipHit()
	if c := la.nextCue(); len(c) == 0 || c[0] != CueShipLost[0] {
		t.Fatalf("a lost ship queues its cue, got %v", c)
	}
}
