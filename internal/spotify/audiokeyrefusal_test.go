package spotify

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func refusalManager() *Manager {
	return &Manager{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

const refusalLine = `level=warning msg="skipping track: Spotify refused the audio key (code 1) for this track"`

// One refused track is one unavailable track, not a broken account. Saying
// anything then would put a scary message on a playlist that plays fine.
func TestASingleRefusedTrackSaysNothing(t *testing.T) {
	m := refusalManager()
	m.noteLibrespotLine(refusalLine)
	if m.AudioKeyRefused() {
		t.Error("a single refused track was reported as a refused account")
	}
}

// The reported case: a whole playlist raced past in silence. In the 2026-08-16
// bundle it was 51 tracks in under three seconds.
func TestAPlaylistRacingPastIsReported(t *testing.T) {
	m := refusalManager()
	for i := 0; i < 51; i++ {
		m.noteLibrespotLine(refusalLine)
	}
	m.noteLibrespotLine(`level=warning msg="stopping after 51 consecutive unplayable tracks"`)
	if !m.AudioKeyRefused() {
		t.Fatal("a playlist that skipped 51 tracks in a row was not reported")
	}
}

// Refusals spread over hours are separate episodes, not a run: a track that is
// unavailable today and another next week must not add up to an accusation.
func TestRefusalsFarApartAreNotARun(t *testing.T) {
	m := refusalManager()
	for i := 0; i < 10; i++ {
		m.noteLibrespotLine(refusalLine)
		m.mu.Lock()
		m.lastKeyRefusalAt = time.Now().Add(-time.Hour)
		m.mu.Unlock()
	}
	if m.AudioKeyRefused() {
		t.Error("refusals an hour apart were counted as one run")
	}
}

// The message goes away by itself. Nothing on the speaker would clear it, so it
// must not sit there for the rest of the day.
func TestTheMessageExpires(t *testing.T) {
	m := refusalManager()
	for i := 0; i < 6; i++ {
		m.noteLibrespotLine(refusalLine)
	}
	if !m.AudioKeyRefused() {
		t.Fatal("a fresh run was not reported")
	}
	m.mu.Lock()
	m.lastKeyRefusalAt = time.Now().Add(-11 * time.Minute)
	m.keyRefusalGaveUpAt = time.Time{}
	m.mu.Unlock()
	if m.AudioKeyRefused() {
		t.Error("the message was still showing eleven minutes later")
	}
}
