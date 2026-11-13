package webui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// #1890: the next track's server is asked once; only no answer at all counts
// as gone. A server that answers, even with a 404 or a 500, is left to the
// dead-track rule.
func TestQueueNextServerGone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		code  int
		err   error
		items int
		start int
		want  bool
	}{
		{"server answers", 206, nil, 3, 0, false},
		{"server answers with an error", 500, nil, 3, 0, false},
		{"no answer at all", 0, context.DeadlineExceeded, 3, 0, true},
		{"connection refused", 0, errors.New("dial tcp: connection refused"), 3, 0, true},
		{"no next track", 0, context.DeadlineExceeded, 3, 2, false},
	}
	for _, tc := range cases {
		s := &Server{queue: newPlayQueue()}
		s.queue.load(badURLTracks("http://192.0.2.10:49200", tc.items), tc.start, false, repeatOff)
		var asked atomic.Int32
		s.trackProbeFn = func(_ context.Context, u string) (int, error) {
			asked.Add(1)
			if !strings.HasPrefix(u, "http://192.0.2.10:49200") {
				t.Errorf("%s: probed %q", tc.name, u)
			}
			return tc.code, tc.err
		}
		if got := s.queueNextServerGone(); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
		if asked.Load() > 1 {
			t.Errorf("%s: asked %d times, want at most once", tc.name, asked.Load())
		}
	}
}

// The reason stays in /api/queue until STR starts something else, and only
// while no queue plays.
func TestQueueLastEndLastsUntilTheNextPlay(t *testing.T) {
	t.Parallel()
	s := &Server{queue: newPlayQueue()}
	s.queue.load(badURLTracks("http://192.0.2.10:49200", 3), 0, false, repeatOff)
	if s.queueSnapshotWithCard().LastEnd != nil {
		t.Fatal("lastEnd before any end")
	}
	s.stopQueueServerGone()
	snap := s.queueSnapshotWithCard()
	if snap.Active || snap.LastEnd == nil || snap.LastEnd.Reason != queueLastEndServerGone || snap.LastEnd.At == "" {
		t.Fatalf("after the end: active=%v lastEnd=%+v", snap.Active, snap.LastEnd)
	}
	raw, _ := json.Marshal(snap)
	if !strings.Contains(string(raw), `"lastEnd":{"reason":"server_gone"`) {
		t.Errorf("JSON does not carry lastEnd: %s", raw)
	}
	s.play.bumpGen() // any play STR starts afterwards
	if le := s.queueSnapshotWithCard().LastEnd; le != nil {
		t.Errorf("lastEnd %+v survived a new play", le)
	}
	raw, _ = json.Marshal(s.queueSnapshotWithCard())
	if strings.Contains(string(raw), "lastEnd") {
		t.Errorf("an empty lastEnd is in the JSON: %s", raw)
	}
}

// An ordinary stop leaves nothing to explain.
func TestQueueLastEndOnlyForAGoneServer(t *testing.T) {
	t.Parallel()
	s := &Server{queue: newPlayQueue()}
	s.queue.load(badURLTracks("http://192.0.2.10:49200", 3), 0, false, repeatOff)
	s.stopQueue("stop pressed")
	if le := s.queueSnapshotWithCard().LastEnd; le != nil {
		t.Errorf("lastEnd %+v after a plain stop", le)
	}
}

// Every way the watcher skips a track it did not hear asks first whether the
// next track's server still answers: the dropped track (the #1890 bundle),
// the track that never started, and the one the server could not deliver.
func TestEverySkipOfAnUnheardTrackAsksForTheServerFirst(t *testing.T) {
	cases := []struct{ file, skip string }{
		{"queueplay.go", `"the speaker dropped the track without playing it"`},
		{"queueplay.go", `"the track never started playing"`},
		{"queuebadurl.go", `"the media server could not deliver the track"`},
	}
	for _, c := range cases {
		src := readRepoFile(t, c.file)
		i := strings.Index(src, c.skip)
		if i < 0 {
			t.Fatalf("%s: the skip %s is gone; if it moved, move this test with it", c.file, c.skip)
		}
		if !strings.Contains(src[max(0, i-2000):i], "s.queueNextServerGone()") {
			t.Errorf("%s: the skip %s no longer asks whether the server answers", c.file, c.skip)
		}
	}
}

// The phone page says it too, in every language it carries, and reads the
// queue for a speaker left with no source as well as for one on UPnP.
func TestPhonePageNamesTheGoneMusicServer(t *testing.T) {
	if n := strings.Count(indexHTML, ",serverGone:\""); n != 12 {
		t.Errorf("serverGone is in %d language tables, want 12", n)
	}
	for _, w := range []string{`serverGone:"Music server not responding"`, `serverGone:"Musikserver antwortet nicht"`} {
		if !strings.Contains(indexHTML, w) {
			t.Errorf("missing %s", w)
		}
	}
	for _, code := range []string{
		"q.lastEnd.reason === 'server_gone'",
		"(upnpPlay || upSrc === 'INVALID_SOURCE') ? await currentQueue()",
		"song || goneLine || human[state] || T.stopped",
	} {
		if !strings.Contains(indexHTML, code) {
			t.Errorf("the phone page lost %q", code)
		}
	}
}
