package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The main speaker of a permanent group asks its members for a quiet wake
// with the zone join to follow, and only that request carries zonejoin=1.
func TestWakeJoinFollowsOnlyOnTheZoneJoinKey(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"quietifasleep=1&zonejoin=1", true},
		{"quiet=1&zonejoin=1", true},
		{"quietifasleep=1", false},
		{"quiet=1", false},
		{"zonejoin=0", false},
		{"", false},
	}
	for _, tc := range cases {
		q, err := url.ParseQuery(tc.query)
		if err != nil {
			t.Fatal(err)
		}
		if got := wakeJoinFollows(q); got != tc.want {
			t.Errorf("wakeJoinFollows(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
	// What the main speaker sends: quiet only if asleep (an old agent must
	// leave a playing member alone), and the join to follow.
	q, err := url.ParseQuery(strings.SplitN(memberWakePath, "?", 2)[1])
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("quietifasleep") != "1" || !wakeJoinFollows(q) || q.Get("quiet") != "" {
		t.Fatalf("member wake path %q: want quietifasleep=1 and zonejoin=1, and not the ungated quiet=1", memberWakePath)
	}
	// The mirror group sends no /setZone after the wake, so its members keep
	// the full quiet wake that stops their resumed station first.
	q, err = url.ParseQuery(strings.SplitN(memberWakePathNoJoin, "?", 2)[1])
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("quietifasleep") != "1" || wakeJoinFollows(q) {
		t.Fatalf("mirror member wake path %q: want quietifasleep=1 without zonejoin", memberWakePathNoJoin)
	}
}

// bufferingResumeBox is the member measured on 2026-11-26: woken, its firmware
// resumes the last station, which sits in BUFFERING_STATE and ignores every
// STOP it gets.
type bufferingResumeBox struct {
	*wakeLevelBox
	stops       atomic.Int32
	joinFollows atomic.Int32 // 0 = not woken, 1 = without the join, 2 = with it
}

func newBufferingResumeServer(t *testing.T) (*Server, *bufferingResumeBox) {
	t.Helper()
	b := &bufferingResumeBox{wakeLevelBox: &wakeLevelBox{level: 10, fwLevel: 10}}
	s := newLevelServer(t, b.wakeLevelBox)
	s.quietWakeNowPlayingFn = func(context.Context, string) nowPlayingSnapshot {
		b.mu.Lock()
		defer b.mu.Unlock()
		if !b.awake {
			return nowPlayingSnapshot{Source: "STANDBY"}
		}
		np := oldStation
		np.PlayStatus = "BUFFERING_STATE"
		return np
	}
	s.quietWakeStopFn = func(context.Context, string) error {
		b.stops.Add(1)
		return nil
	}
	s.quietWakeWakeFn = func(_ context.Context, joinFollows bool) error {
		if joinFollows {
			b.joinFollows.Store(2)
		} else {
			b.joinFollows.Store(1)
		}
		b.wake()
		return nil
	}
	return s, b
}

// With the join to follow, the member answers once it is awake and muted: the
// standby wake is told to skip its self-wake grace, the resume gets one look
// and one STOP, and nothing waits for a STOP the station ignores.
func TestQuietWakeForAZoneJoinAnswersOnceAwakeAndMuted(t *testing.T) {
	t.Parallel()
	s, b := newBufferingResumeServer(t)

	start := time.Now()
	if err := s.quietWakeFor(context.Background(), true); err != nil {
		t.Fatalf("quiet wake: %v", err)
	}
	took := time.Since(start)

	if got := b.joinFollows.Load(); got != 2 {
		t.Fatalf("the standby wake was not told a zone join follows (state %d), so it kept the self-wake grace", got)
	}
	if got := b.stops.Load(); got != 1 {
		t.Fatalf("sent %d STOPs, want the single one of the one look", got)
	}
	if took >= quietWakeRestopGap {
		t.Fatalf("the wake took %s: it waited on the resume instead of answering for the join", took)
	}
	if lvl := b.current(); lvl != 0 {
		t.Fatalf("level %d after the wake, want the mute held until the join", lvl)
	}
	if !s.quietWakeActive() {
		t.Fatal("no level hold armed: nothing would ever put the level back")
	}
	// No STOP after the answer either: the join comes next, and a STOP on a
	// zone member stops the group.
	time.Sleep(2 * quietWakeRestopGap)
	if got := b.stops.Load(); got != 1 {
		t.Fatalf("a STOP went out after the wake answered (%d in all): it would land after the zone join", got)
	}
}

// The endpoint passes the key through: zonejoin=1 reaches the wake, a quiet
// wake without it does not.
func TestBoxWakeHandlerPassesTheZoneJoinOn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		query string
		want  int32
	}{
		{"quietifasleep=1&zonejoin=1", 2},
		{"quietifasleep=1", 1},
	} {
		s, b := newBufferingResumeServer(t)
		// An awake speaker on no source: the watch has nothing to wait for,
		// so the full quiet wake answers at once too.
		s.quietWakeNowPlayingFn = func(context.Context, string) nowPlayingSnapshot {
			b.mu.Lock()
			defer b.mu.Unlock()
			if !b.awake {
				return nowPlayingSnapshot{Source: "STANDBY"}
			}
			return nowPlayingSnapshot{Source: "INVALID_SOURCE"}
		}
		rec := httptest.NewRecorder()
		s.handleBoxWake(rec, httptest.NewRequest(http.MethodPost, "/api/box/wake?"+tc.query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", tc.query, rec.Code, rec.Body.String())
		}
		if got := b.joinFollows.Load(); got != tc.want {
			t.Errorf("%s: wake ran with join state %d, want %d", tc.query, got, tc.want)
		}
	}
}

// selfResumeRestoreServer is a speaker whose quiet wake's hold has run out
// with no zone join: the firmware's resumed station plays on, muted.
func selfResumeRestoreServer(t *testing.T) (*Server, *[]string, *sync.Mutex) {
	t.Helper()
	b := &wakeLevelBox{level: 0, fwLevel: 10}
	s := newLevelServer(t, b)
	var mu sync.Mutex
	events := []string{}
	s.quietWakeNowPlayingFn = func(context.Context, string) nowPlayingSnapshot { return oldStation }
	s.quietWakeStopFn = func(context.Context, string) error {
		mu.Lock()
		events = append(events, "stop")
		mu.Unlock()
		return nil
	}
	s.ownLevelSetFn = func(_ context.Context, v int) error {
		b.mu.Lock()
		b.level = v
		b.mu.Unlock()
		mu.Lock()
		events = append(events, "level")
		mu.Unlock()
		return nil
	}
	s.rejoinReadZoneMasterFn = func(context.Context, string) string { return "" }
	s.noteQuietWakeEpisode()
	s.groupWake.mu.Lock()
	s.groupWake.in = wakeLevelInputs{remembered: 30}
	s.groupWake.until = time.Now().Add(-time.Second)
	s.groupWake.mu.Unlock()
	return s, &events, &mu
}

// The join never came: the level must not come back over the station the
// firmware resumed, so that is stopped first.
func TestQuietWakeTimeoutStopsTheResumeBeforeTheLevelComesBack(t *testing.T) {
	t.Parallel()
	s, events, mu := selfResumeRestoreServer(t)

	s.restoreQuietWakeVolume("timeout")

	mu.Lock()
	defer mu.Unlock()
	if len(*events) != 2 || (*events)[0] != "stop" || (*events)[1] != "level" {
		t.Fatalf("events %v, want the STOP and then the level", *events)
	}
}

// Everything that is not the firmware's own resume on a standalone speaker is
// left playing: the speaker is in a zone (a STOP stops the group), it joined
// or left one since the wake, or somebody started a play through STR since.
func TestQuietWakeTimeoutLeavesAnythingButTheSelfResumeAlone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(s *Server)
	}{
		{"in a zone", func(s *Server) {
			s.rejoinReadZoneMasterFn = func(context.Context, string) string { return "MASTER-ID" }
		}},
		{"zone change since the wake", func(s *Server) { s.noteZoneChanged() }},
		{"user play since the wake", func(s *Server) {
			s.play.last = &lastPlayInfo{boxURL: "http://192.0.2.1:8888/stream/1", ts: time.Now().Add(time.Second)}
		}},
		{"episode over", func(s *Server) {
			s.groupWake.mu.Lock()
			s.groupWake.episodeUntil = time.Time{}
			s.groupWake.mu.Unlock()
		}},
		{"not playing", func(s *Server) {
			s.quietWakeNowPlayingFn = func(context.Context, string) nowPlayingSnapshot {
				np := oldStation
				np.PlayStatus = "STOP_STATE"
				return np
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, events, mu := selfResumeRestoreServer(t)
			// noteZoneChanged must land after the episode stamp.
			time.Sleep(2 * time.Millisecond)
			tc.setup(s)

			s.restoreQuietWakeVolume("timeout")

			mu.Lock()
			defer mu.Unlock()
			for _, e := range *events {
				if e == "stop" {
					t.Fatalf("events %v: a STOP went out", *events)
				}
			}
		})
	}
}

// A zone join is the normal end of the hold and never sends a STOP: the zone
// already replaced what the speaker resumed.
func TestQuietWakeZoneJoinRestoreSendsNoStop(t *testing.T) {
	t.Parallel()
	s, events, mu := selfResumeRestoreServer(t)
	s.restoreQuietWakeVolume("zone joined")
	mu.Lock()
	defer mu.Unlock()
	for _, e := range *events {
		if e == "stop" {
			t.Fatalf("events %v: the zone join restore sent a STOP", *events)
		}
	}
}
