package oled

import (
	"encoding/binary"
	"io"
	"math"
	"time"
)

// Game music: our own 4-bar loop in A minor (square lead, triangle bass) for
// the intro and a short falling jingle for the game-over screen, streamed as
// an endless WAV that the speaker itself plays over UPnP. Nothing plays while
// the game itself runs: Previous/Skip on the remote are the speaker's own skip
// keys, and BoseApp tears the UPnP stream down on every press.
//
// The speaker's renderer wants 44.1 kHz stereo with 0xFFFFFFFF size fields
// (22.05 kHz mono was fetched and dropped). The synthesis is integer only:
// these builds are softfloat (GOARM=5), and a float synth at 44.1 kHz pinned
// the CPU (the game fell under 1 fps); this one needs about 3 % of the core.
// Levels sit near full scale because a -18 dB mix was inaudible at a low
// speaker volume. No sound effects: the speaker buffers the stream for a
// second or more, so they would always arrive late.

const (
	synthRate = 44100
	synthBPM  = 140
	eighthLen = synthRate * 60 / synthBPM / 2 // samples per eighth note
)

// gameLead: one MIDI note per eighth, 0 = rest. Written for this game.
var gameLead = []int{
	69, 72, 76, 72, 74, 72, 69, 67,
	69, 72, 76, 79, 77, 76, 74, 72,
	71, 74, 77, 74, 76, 74, 71, 69,
	72, 71, 69, 67, 69, 0, 69, 0,
}

// gameBass: one root per bar, played root/octave on quarters.
var gameBass = []int{45, 41, 43, 40}

// overLead / overBass: the game-over jingle, two bars, played once.
var overLead = []int{
	76, 0, 72, 0, 69, 0, 68, 0,
	67, 66, 65, 64, 57, 0, 0, 0,
}

var overBass = []int{45, 40}

// sgLead / sgBass: Starguard's intro, a driving E minor loop. sgOverLead /
// sgOverBass: its game-over jingle, played once.
var sgLead = []int{
	64, 0, 67, 64, 71, 0, 69, 67,
	64, 0, 67, 64, 72, 71, 69, 67,
	62, 0, 66, 62, 69, 0, 67, 66,
	64, 67, 71, 76, 74, 71, 67, 0,
}

var sgBass = []int{40, 40, 38, 43}

var sgOverLead = []int{
	71, 70, 69, 68, 67, 0, 64, 0,
	59, 0, 0, 0, 52, 0, 0, 0,
}

var sgOverBass = []int{40, 40}

// Track is one piece of music: a lead and a bass line in the formats above.
// A track that does not loop is followed by silence until the stream stops,
// so the speaker keeps the same stream and shows no state change.
type Track struct {
	Lead, Bass []int
	Loop       bool
}

var (
	// IntroTrack plays under the intro logo.
	IntroTrack = Track{Lead: gameLead, Bass: gameBass, Loop: true}
	// OverTrack plays under the game-over screen.
	OverTrack = Track{Lead: overLead, Bass: overBass}

	sgIntroTrack = Track{Lead: sgLead, Bass: sgBass, Loop: true}
	sgOverTrack  = Track{Lead: sgOverLead, Bass: sgOverBass}
)

// Length is how long one pass of the track lasts.
func (tr Track) Length() time.Duration {
	return time.Duration(len(tr.Lead)) * time.Second * eighthLen / synthRate
}

var (
	noteInc [128]uint32 // phase increment per MIDI note
	leadEnv [eighthLen]int32
	bassEnv [2 * eighthLen]int32
)

func init() {
	for m := range noteInc {
		f := 440 * math.Pow(2, float64(m-69)/12)
		noteInc[m] = uint32(f * 4294967296 / synthRate)
	}
	for i := range leadEnv {
		leadEnv[i] = int32(256 * math.Exp(-3*float64(i)/eighthLen))
	}
	for i := range bassEnv {
		bassEnv[i] = int32(154 + 102*math.Exp(-4*float64(i)/(2*eighthLen)))
	}
}

// Synth renders a track sample by sample.
type Synth struct {
	Track          Track
	t              int
	leadPh, bassPh uint32
}

func triangle(ph uint32) int32 {
	v := int32(ph >> 16) // 0..65535
	if v < 32768 {
		return v*2 - 32768
	}
	return (65535-v)*2 - 32768
}

// Render fills buf with the next mono samples.
func (s *Synth) Render(buf []int16) {
	lead, bass := s.Track.Lead, s.Track.Bass
	for i := range buf {
		t := s.t + i
		step := t / eighthLen
		if !s.Track.Loop && step >= len(lead) {
			buf[i] = 0
			continue
		}
		var v int32
		if n := lead[step%len(lead)]; n != 0 {
			s.leadPh += noteInc[n]
			sq := int32(-12000)
			if s.leadPh < 1<<30 { // 25 % duty
				sq = 12000
			}
			v += sq * leadEnv[t%eighthLen] >> 8
		}
		bn := bass[(step/8)%len(bass)]
		if (step/2)%2 == 1 {
			bn += 12
		}
		s.bassPh += noteInc[bn]
		v += (triangle(s.bassPh) * 14500 >> 15) * bassEnv[t%(2*eighthLen)] >> 8
		buf[i] = int16(max(-32768, min(32767, v)))
	}
	s.t += len(buf)
}

// wavHeader is a 44.1 kHz 16-bit stereo header with "endless" sizes.
func wavHeader() []byte {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], 0xFFFFFFFF)
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], 2) // stereo
	binary.LittleEndian.PutUint32(h[24:], synthRate)
	binary.LittleEndian.PutUint32(h[28:], synthRate*4)
	binary.LittleEndian.PutUint16(h[32:], 4)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], 0xFFFFFFFF)
	return h
}

// StreamMusic writes track as an endless WAV to w, paced to real time: a 2 s head
// start fills the renderer's buffer, then it stays about 1 s ahead. It returns
// when done closes or a write fails (the speaker hung up).
func StreamMusic(w io.Writer, flush func(), done <-chan struct{}, track Track) error {
	if _, err := w.Write(wavHeader()); err != nil {
		return err
	}
	s := Synth{Track: track}
	buf := make([]int16, 1024)
	out := make([]byte, 4*len(buf))
	start := time.Now()
	sent := 0
	for {
		select {
		case <-done:
			return nil
		default:
		}
		ahead := float64(sent)/synthRate - time.Since(start).Seconds()
		if sent > 2*synthRate && ahead > 1 {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		s.Render(buf)
		for i, v := range buf {
			binary.LittleEndian.PutUint16(out[4*i:], uint16(v))
			binary.LittleEndian.PutUint16(out[4*i+2:], uint16(v))
		}
		if _, err := w.Write(out); err != nil {
			return err
		}
		if flush != nil {
			flush()
		}
		sent += len(buf)
	}
}
