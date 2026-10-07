package webui

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
)

// A key saved by holding the speaker's button gets the logo chain STR knows
// for the stream, the one an app save stores (#968).
func TestStreamArtChain(t *testing.T) {
	const u = "https://stream.example.com/jazz/"
	const chain = "https://example.com/a.svg|https://example.com/a.png"
	s := queueSaveTestServer(t)
	if got := s.StreamArtChain(u); got != "" {
		t.Fatalf("nothing known: %q", got)
	}

	// The Recently-played ring, newest entry first.
	s.recent = recent.New()
	s.recent.Add(recent.Entry{Source: "radio", CardKey: u, CardURL: u, CardArt: "https://example.com/old.png"})
	s.recent.Add(recent.Entry{Source: "spotify", CardKey: "spotify:playlist:x", CardURL: "spotify:playlist:x"})
	s.recent.Add(recent.Entry{Source: "radio", CardKey: u, CardURL: u, CardArt: chain})
	s.recent.Add(recent.Entry{Source: "radio", CardKey: "other", CardURL: "https://other.example.com/", CardArt: "x"})
	if got := s.StreamArtChain(u); got != chain {
		t.Fatalf("ring: %q", got)
	}

	// The station that plays now beats the ring.
	s.recentRadioCard = recentCardCtx{key: u, url: u, art: "https://example.com/now.png|https://example.com/now.ico"}
	if got := s.StreamArtChain(u); got != "https://example.com/now.png|https://example.com/now.ico" {
		t.Fatalf("current card: %q", got)
	}

	// A preset holding the stream beats both.
	if err := s.presets.SetSlot(presets.Preset{Slot: 1, Name: "Jazz", Type: "radio", StreamURL: u, Art: "https://example.com/p.png|https://example.com/p.svg"}); err != nil {
		t.Fatal(err)
	}
	if got := s.StreamArtChain(" " + u + " "); got != "https://example.com/p.png|https://example.com/p.svg" {
		t.Fatalf("preset: %q", got)
	}
}
