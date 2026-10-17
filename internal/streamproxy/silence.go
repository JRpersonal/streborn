package streamproxy

import (
	"context"
	"net/http"
	"time"
)

// A speaker that STR wakes for a group resumes its own last station on the way
// out of standby. When that station's key is gone, the proxy used to answer the
// fetch with 404, and the firmware raised the error a second or two LATER, after
// the speaker had already joined the zone: it dropped to INVALID_SOURCE and the
// zone fell apart with it (Küche, 2026-10-17 09:51, a permanent group lost on
// every play). While such a wake is in progress the proxy now serves silence
// instead. The wake stops the resumed station a moment later and the zone join
// takes the speaker over, so the silence is never heard and no error is raised.

// silenceFrame is one MPEG-1 Layer III frame, 128 kbit/s, 44.1 kHz, mono, with
// all side information zero: a decoder reads it as 1152 samples of silence.
var silenceFrame = func() []byte {
	f := make([]byte, 417) // 144 * 128000 / 44100, no padding
	f[0], f[1], f[2], f[3] = 0xFF, 0xFB, 0x90, 0xC4
	return f
}()

// silenceFrameDur is how long one frame plays.
const silenceFrameDur = 1152 * time.Second / 44100

// silenceCap bounds how long a single fetch is fed silence. A group wake stops
// the station within seconds; the cap only matters when nothing does, and then
// the speaker sees the stream end like any finished one.
const silenceCap = 30 * time.Second

// SetHoldMissingSlotFn wires the check that says a group wake is in progress
// (see holdMissingSlotFn).
func (s *Server) SetHoldMissingSlotFn(fn func() bool) { s.holdMissingSlotFn = fn }

// serveSilence writes silent MP3 frames at playback pace until the speaker
// closes the fetch or silenceCap runs out.
func serveSilence(ctx context.Context, w http.ResponseWriter) {
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	ctx, cancel := context.WithTimeout(ctx, silenceCap)
	defer cancel()
	// A short lead so the decoder has frames to start on at once.
	for i := 0; i < 8; i++ {
		if _, err := w.Write(silenceFrame); err != nil {
			return
		}
	}
	if fl != nil {
		fl.Flush()
	}
	t := time.NewTicker(silenceFrameDur)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := w.Write(silenceFrame); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
	}
}
