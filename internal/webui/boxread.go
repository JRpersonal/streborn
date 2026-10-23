package webui

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// The own-box now_playing read, in one place.
//
// Every "what does this speaker play right now" question in this package
// comes through here: the recall verify, the queue watcher, the standby
// checks, the hold-to-save, the display-message gate, the announcement and
// /api/status itself. One fetch path means one body limit, one feed into the
// /api/status micro-cache, one record in the :8090 outage tracker and one
// test seam (Server.boxNowReadFn) instead of the nine fields and seven
// package variables this replaced.
//
// What is deliberately NOT unified is the policy for an unreadable speaker.
// The reader returns the error; the caller decides whether that means
// "standby" (the resume guards fail closed), "leave it alone" (the silent
// wake), "false" (the stuck-selection probe) or "unknown". And each caller
// keeps its own deadline: the context it passes bounds the read.

// errNoBoxHost is the read error for a Server without a configured speaker.
var errNoBoxHost = errors.New("box host not configured")

// readBoxNow reads the speaker's now_playing fresh, within ctx's deadline
// (callers wrap their own; the boxapi default is 6 s). The outage tracker
// sees the verdict and a 200 body feeds the /api/status micro-cache, so a
// phone poll right after an internal read is served without a second trip
// to the firmware.
func (s *Server) readBoxNow(ctx context.Context) (*boxapi.NowPlaying, error) {
	if s.boxNowReadFn != nil {
		return s.boxNowReadFn(ctx)
	}
	if s.boxHost == "" {
		return nil, errNoBoxHost
	}
	np, err := boxapi.New(s.boxHost).NowPlaying(ctx)
	// A firmware error reply is still a fetch that reached the firmware;
	// only a transport failure counts as the webserver being down.
	var be *boxapi.BoxError
	s.noteBoseAppFetch(err == nil || errors.As(err, &be))
	if err != nil {
		return nil, err
	}
	s.cacheStatusBody(np.Raw)
	return np, nil
}

// readBoxNowWithin is readBoxNow under its own deadline, for the callers
// that have no context of their own.
func (s *Server) readBoxNowWithin(d time.Duration) (*boxapi.NowPlaying, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return s.readBoxNow(ctx)
}

// readBoxNowCached answers from the /api/status micro-cache when it holds a
// body younger than statusCacheTTL and reads fresh otherwise. For callers
// that can live with an answer up to two seconds old: the phone poll and a
// coarse "is anything playing" gate. A decision that acts on the speaker
// the next moment takes readBoxNow.
func (s *Server) readBoxNowCached(ctx context.Context) (*boxapi.NowPlaying, error) {
	if s.boxNowReadFn != nil {
		return s.boxNowReadFn(ctx)
	}
	if body, ok := s.cachedStatusBody(); ok {
		return boxapi.ParseNowPlaying(body), nil
	}
	return s.readBoxNow(ctx)
}

// readBoxSnapshot is readBoxNow projected onto the snapshot shape the
// announcement and the zone code carry around. An unreadable speaker is the
// zero snapshot (Source == ""), which every consumer treats as "unknown, do
// nothing".
func (s *Server) readBoxSnapshot(ctx context.Context) nowPlayingSnapshot {
	np, err := s.readBoxNow(ctx)
	if err != nil {
		return nowPlayingSnapshot{}
	}
	return snapshotOf(np)
}

// snapshotOf projects a parsed now_playing onto nowPlayingSnapshot.
func snapshotOf(np *boxapi.NowPlaying) nowPlayingSnapshot {
	return nowPlayingSnapshot{
		Source:     np.Source,
		Location:   np.ContentItem.Location,
		ItemName:   np.ContentItem.ItemName,
		PlayStatus: np.PlayStatus,
	}
}

// cacheStatusBody stores a 200 now_playing body for /api/status.
func (s *Server) cacheStatusBody(body []byte) {
	s.statusMu.Lock()
	s.statusBody = body
	s.statusCode = http.StatusOK
	s.statusAt = time.Now()
	s.statusStaleWarned = false
	s.statusMu.Unlock()
}

// cachedStatusBody returns the cached now_playing body when it is younger
// than statusCacheTTL.
func (s *Server) cachedStatusBody() ([]byte, bool) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if s.statusBody != nil && s.statusCode == http.StatusOK && time.Since(s.statusAt) < statusCacheTTL {
		return s.statusBody, true
	}
	return nil, false
}
