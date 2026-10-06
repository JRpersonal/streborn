package oled

import (
	"sync"
	"sync/atomic"
)

// LiveAudio is the music of a round that changes with the game: one stream
// from the intro to the game-over jingle, switched inside the stream so the
// speaker never has to start a new one. The game sets the tempo and queues
// short cues; the synth reads them while it renders.
//
// The speaker buffers a second or two of the stream, so everything here is
// chosen to sound right when it arrives late: a march whose tempo follows the
// aliens left, a warble while the saucer crosses, and fanfares for a cleared
// wave or a lost ship. Shot and explosion sounds would always lag the picture
// and are left out on purpose.
type LiveAudio struct {
	phase  atomic.Int32 // livePhaseIntro, livePhaseGame, livePhaseOver
	tempo  atomic.Int32 // percent of the base march tempo
	saucer atomic.Bool
	mu     sync.Mutex
	cues   [][]int
}

const (
	livePhaseIntro int32 = iota
	livePhaseGame
	livePhaseOver
)

// Cues, one MIDI note per sixteenth, 0 = rest.
var (
	CueWaveClear = []int{72, 76, 79, 84, 0, 84, 88, 91}
	CueShieldHit = []int{84, 79, 84, 79}
	CueShipLost  = []int{67, 66, 64, 0, 60, 59, 55, 0, 48}
)

// NewLiveAudio starts in the intro.
func NewLiveAudio() *LiveAudio {
	la := &LiveAudio{}
	la.tempo.Store(100)
	return la
}

// StartGame switches the stream from the intro melody to the in-game music.
func (la *LiveAudio) StartGame() { la.phase.Store(livePhaseGame) }

// GameOver switches to the game-over jingle, played once.
func (la *LiveAudio) GameOver() { la.phase.Store(livePhaseOver) }

// SetTempo sets the march tempo in percent of the base (clamped 100..230).
func (la *LiveAudio) SetTempo(pct int) { la.tempo.Store(int32(max(100, min(230, pct)))) }

// SetSaucer turns the saucer warble on or off.
func (la *LiveAudio) SetSaucer(on bool) { la.saucer.Store(on) }

// Cue queues a short phrase over the music. At most a few are kept; a game
// that queues faster than they play loses the oldest.
func (la *LiveAudio) Cue(notes []int) {
	la.mu.Lock()
	la.cues = append(la.cues, notes)
	if len(la.cues) > 3 {
		la.cues = la.cues[len(la.cues)-3:]
	}
	la.mu.Unlock()
}

func (la *LiveAudio) nextCue() []int {
	la.mu.Lock()
	defer la.mu.Unlock()
	if len(la.cues) == 0 {
		return nil
	}
	c := la.cues[0]
	la.cues = la.cues[1:]
	return c
}

// march: the bass steps down four notes, one per beat.
var liveMarch = []int{40, 38, 36, 35}

const (
	liveBaseBPM = 84
	cueStepLen  = synthRate / 11 // one cue note, about 90 ms
)

// liveSynth renders a LiveAudio sample by sample.
type liveSynth struct {
	la          *LiveAudio
	phase       int32
	sub         Synth // intro and game-over parts reuse the plain synth
	beat, inPos int   // current march beat and position inside it
	bassPh      uint32
	cue         []int
	cueIdx      int
	cuePos      int
	leadPh      uint32
	saucerPh    uint32
	saucerT     int
}

func (s *liveSynth) render(buf []int16) {
	if p := s.la.phase.Load(); p != s.phase {
		s.phase = p
		switch p {
		case livePhaseGame:
			s.beat, s.inPos = 0, 0
		case livePhaseOver:
			s.sub = Synth{Track: sgOverTrack}
		}
	}
	switch s.phase {
	case livePhaseIntro, livePhaseOver:
		if s.sub.Track.Lead == nil {
			s.sub = Synth{Track: sgIntroTrack}
		}
		s.sub.Render(buf)
		return
	}
	beatLen := synthRate * 60 * 100 / (liveBaseBPM * int(s.la.tempo.Load()))
	saucer := s.la.saucer.Load()
	for i := range buf {
		var v int32
		// march bass, triangle with a falling envelope over the beat
		if s.inPos >= beatLen {
			s.inPos = 0
			s.beat++
		}
		s.bassPh += noteInc[liveMarch[s.beat%len(liveMarch)]]
		env := bassEnv[min(s.inPos*len(bassEnv)/max(beatLen, 1), len(bassEnv)-1)]
		if s.inPos < beatLen*3/4 {
			v += (triangle(s.bassPh) * 15000 >> 15) * env >> 8
		}
		s.inPos++
		// cue on top, square with a short envelope per note
		if s.cue == nil {
			if s.cue = s.la.nextCue(); s.cue != nil {
				s.cueIdx, s.cuePos = 0, 0
			}
		}
		if s.cue != nil {
			if n := s.cue[s.cueIdx]; n != 0 {
				s.leadPh += noteInc[n]
				sq := int32(-11000)
				if s.leadPh < 1<<30 {
					sq = 11000
				}
				v += sq * leadEnv[min(s.cuePos*len(leadEnv)/cueStepLen, len(leadEnv)-1)] >> 8
			}
			if s.cuePos++; s.cuePos >= cueStepLen {
				s.cuePos = 0
				if s.cueIdx++; s.cueIdx >= len(s.cue) {
					s.cue = nil
				}
			}
		}
		// saucer warble: a high square swapping between two notes
		if saucer {
			n := 88
			if (s.saucerT/2205)%2 == 1 {
				n = 91
			}
			s.saucerPh += noteInc[n]
			if s.saucerPh < 1<<31 {
				v += 3500
			} else {
				v -= 3500
			}
			s.saucerT++
		}
		buf[i] = int16(max(-32768, min(32767, v)))
	}
}
