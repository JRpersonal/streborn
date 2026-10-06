package oled

import (
	"encoding/binary"
	"io"
	"math"
	"time"
)

// Game music: our own 4-bar loop in A minor (square lead, triangle bass),
// streamed as an endless WAV that the speaker itself plays over UPnP.
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

// Synth renders the music loop sample by sample.
type Synth struct {
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
	for i := range buf {
		t := s.t + i
		step := t / eighthLen
		var v int32
		if n := gameLead[step%len(gameLead)]; n != 0 {
			s.leadPh += noteInc[n]
			sq := int32(-12000)
			if s.leadPh < 1<<30 { // 25 % duty
				sq = 12000
			}
			v += sq * leadEnv[t%eighthLen] >> 8
		}
		bn := gameBass[(step/8)%len(gameBass)]
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

// StreamMusic writes the endless WAV to w, paced to real time: a 2 s head
// start fills the renderer's buffer, then it stays about 1 s ahead. It returns
// when done closes or a write fails (the speaker hung up).
func StreamMusic(w io.Writer, flush func(), done <-chan struct{}) error {
	if _, err := w.Write(wavHeader()); err != nil {
		return err
	}
	var s Synth
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
