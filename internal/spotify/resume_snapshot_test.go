package spotify

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// Discussion #1077 "Issue 11": key 2's playlist resumed on the track key 1 was
// playing, and the bundle could not say what the resume store held. The
// spotify_resume debug section must show each context's resume point, when it
// was written and what the last recall asked for, without naming an account.
func TestResumeSnapshotShape(t *testing.T) {
	s := newResumeStore("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	const (
		key1 = "spotify:playlist:key1"
		key2 = "spotify:playlist:key2"
	)
	before := time.Now().Add(-time.Second)
	s.note(key1, "spotify:track:a")
	s.note(key2, "spotify:track:b")
	s.note("spotify:user:jane.doe:collection", "spotify:track:c")
	s.noteRecall(key2, "spotify:track:b", false)

	snap := s.snapshot()
	if snap.Count != 3 || len(snap.Points) != 3 {
		t.Fatalf("count = %d, points = %d, want 3", snap.Count, len(snap.Points))
	}
	// Most recently touched first.
	if snap.Points[1].ContextURI != key2 || snap.Points[1].TrackURI != "spotify:track:b" {
		t.Errorf("points[1] = %+v, want key2 -> track b", snap.Points[1])
	}
	if snap.Points[2].ContextURI != key1 {
		t.Errorf("points[2] = %+v, want key1 (oldest last)", snap.Points[2])
	}
	if got := snap.Points[0].ContextURI; got != "spotify:user:redacted:collection" {
		t.Errorf("account context = %q, want the account part redacted", got)
	}
	for _, p := range snap.Points {
		at, err := time.Parse(time.RFC3339, p.NotedAt)
		if err != nil || at.Before(before.Truncate(time.Second)) {
			t.Errorf("notedAt = %q, want an RFC 3339 time from this run (err %v)", p.NotedAt, err)
		}
	}
	if snap.LastRecall == nil || snap.LastRecall.ContextURI != key2 || snap.LastRecall.SkipToURI != "spotify:track:b" || snap.LastRecall.Shuffle {
		t.Errorf("lastRecall = %+v, want key2 with skip_to_uri track b", snap.LastRecall)
	}

	// The section is a Go value; the masking path turns it into JSON. Pin the
	// field names a reader of the bundle sees, and that no account leaks.
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"count", "points", "lastRecall"} {
		if _, ok := generic[k]; !ok {
			t.Errorf("section is missing %q: %s", k, b)
		}
	}
	p0 := generic["points"].([]any)[0].(map[string]any)
	for _, k := range []string{"contextUri", "trackUri", "notedAt"} {
		if _, ok := p0[k]; !ok {
			t.Errorf("point is missing %q: %s", k, b)
		}
	}
	if strings.Contains(string(b), "jane.doe") {
		t.Errorf("account name leaked into the section: %s", b)
	}
}

// Entries loaded from NAND have no write time in this run, and an empty store
// still yields an empty list rather than null.
func TestResumeSnapshotLoadedAndEmpty(t *testing.T) {
	var m Manager
	if snap := m.ResumeSnapshot(); snap.Points == nil || snap.Count != 0 || snap.LastRecall != nil {
		t.Fatalf("nil store snapshot = %+v, want empty points", snap)
	}

	s := newResumeStore("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.track["spotify:album:loaded"] = "spotify:track:x"
	s.order = append(s.order, "spotify:album:loaded")
	snap := s.snapshot()
	if snap.Count != 1 || snap.Points[0].NotedAt != "" {
		t.Fatalf("loaded entry = %+v, want one point without notedAt", snap)
	}
	if snap.LastRecall != nil {
		t.Errorf("lastRecall = %+v before any recall, want nil", snap.LastRecall)
	}
}

func TestRedactSpotifyUser(t *testing.T) {
	for in, want := range map[string]string{
		"spotify:playlist:abc":              "spotify:playlist:abc",
		"spotify:user:someone:playlist:abc": "spotify:user:redacted:playlist:abc",
		"Spotify:User:someone":              "spotify:user:redacted",
		"":                                  "",
	} {
		if got := redactSpotifyUser(in); got != want {
			t.Errorf("redactSpotifyUser(%q) = %q, want %q", in, got, want)
		}
	}
}
