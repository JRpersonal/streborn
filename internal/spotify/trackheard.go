// trackheard.go: how much of the current song the room has heard, from a
// clock that starts when the box receives the song's first audio.
//
// Why not the engine's position less a lag (the first way, heardLag): the
// position is what go-librespot has written to its pipe, and after a skip the
// box fills its buffer at several times real time. Ten seconds into a song a
// live Portable had 22 s of it delivered and the engine stood at 25 s, while
// the lag STR knew was the 10 s lead cap, because the speaker drops the stream
// on the Back key before STR decides and the attachment's own lag is gone by
// then. Every Back read as "15 s heard" and restarted the song (#1678, live
// 2026-11-28).
//
// The clock needs none of that. The drain marks the first audio page of each
// song that it hands to the box. For a song that begins an attachment (a
// recall, a skip) the room hears it from about that moment. For a song that
// follows another without a gap, the box still holds the end of the song
// before; that audio in flight is the delivered timeline of this attachment
// less the wall clock since its first page, taken at the same instant.
//
// Known error: the box's own start-up delay (a second or two) is not in it,
// so the clock runs slightly ahead of the room, and a pause keeps it running.
// Both err towards "restart the song", never towards an unwanted jump back;
// HeardPrev also never lets it exceed the engine's position.
package spotify

import (
	"context"
	"time"
)

// startTrackHeardLocked starts the heard clock for the song whose first audio
// is being handed to the box now. Caller holds m.mu, and calls it BEFORE the
// page is added to the delivered timeline.
func (m *Manager) startTrackHeardLocked(now time.Time) {
	m.trackHeardAt = now
	m.trackHeardLag = 0
	// Learn now, with a session playing and the engine answering, whether
	// Back can go to the song before in one call (engineHasPrevNoSeek).
	if !m.enginePrevNoSeek && m.client != nil && m.logger != nil {
		go m.engineHasPrevNoSeek(context.Background())
	}
	// lagRebase: this page is the attachment's first audio, nothing is in
	// flight ahead of it.
	if m.lagRebase || m.lagBaseAt.IsZero() {
		return
	}
	delivered := time.Duration(float64(m.deliveredGran-m.lagBaseGran) / vorbisRate * float64(time.Second))
	m.trackHeardLag = min(max(0, delivered-now.Sub(m.lagBaseAt)), heardPrevMaxLag)
}

// trackHeard is how much of the current song the room has heard by the
// clock. ok is false when no song has reached a box in this agent run.
func (m *Manager) trackHeard(now time.Time) (heard time.Duration, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.trackHeardAt.IsZero() {
		return 0, false
	}
	return max(0, now.Sub(m.trackHeardAt)-m.trackHeardLag), true
}
