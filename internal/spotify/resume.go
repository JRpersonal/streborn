package spotify

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// resumeStore remembers, per Spotify context (a playlist or album URI), the
// last track that played from it, so a later default (non-shuffle) preset
// recall can continue on that track instead of restarting the context at its
// first song. This is the "continue where you left off" behaviour a real
// Spotify Connect device has. It is best-effort: a missing or stale entry just
// falls back to starting the context from the top (go-librespot ignores a
// skip_to_uri that is no longer in the context).
//
// Persistence is a small JSON map on NAND, written atomically and debounced (at
// most once per resumeFlushEvery, and only when something changed) so flash
// wear stays low; in practice a track changes only every few minutes. The map
// is capped to resumeMaxContexts most-recently-touched contexts so a user with
// many playlists cannot grow the file without bound.
type resumeStore struct {
	path   string
	logger *slog.Logger

	mu    sync.Mutex
	track map[string]string // context URI -> last track URI
	order []string          // LRU, most-recently touched last
	dirty bool
}

const (
	resumeMaxContexts = 64
	resumeFlushEvery  = 30 * time.Second
)

// newResumeStore loads any persisted map from path (best-effort) and returns a
// store ready to note/query. A path of "" disables persistence (the in-memory
// map still works, used by tests).
func newResumeStore(path string, logger *slog.Logger) *resumeStore {
	s := &resumeStore{path: path, logger: logger, track: map[string]string{}}
	s.load()
	return s
}

func (s *resumeStore) load() {
	if s.path == "" {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var m map[string]string
	if json.Unmarshal(b, &m) != nil || m == nil {
		return
	}
	s.mu.Lock()
	s.track = m
	for k := range m {
		s.order = append(s.order, k)
	}
	s.mu.Unlock()
}

// note records that trackURI is the current track of contextURI. It is a no-op
// unless both are spotify: URIs, and does not mark the store dirty when the
// track is unchanged, so a repeated poll of the same track never churns NAND.
func (s *resumeStore) note(contextURI, trackURI string) {
	if !strings.HasPrefix(contextURI, "spotify:") || !strings.HasPrefix(trackURI, "spotify:") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.track[contextURI] == trackURI {
		s.touch(contextURI) // keep an active context from being evicted
		return
	}
	s.track[contextURI] = trackURI
	s.touch(contextURI)
	s.evict()
	s.dirty = true
}

// touch moves ctx to the most-recent end of the LRU order. Caller holds mu.
func (s *resumeStore) touch(ctx string) {
	for i, c := range s.order {
		if c == ctx {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.order = append(s.order, ctx)
}

// evict drops the least-recently-touched contexts beyond the cap. Caller holds mu.
func (s *resumeStore) evict() {
	for len(s.order) > resumeMaxContexts {
		old := s.order[0]
		s.order = s.order[1:]
		delete(s.track, old)
	}
}

// trackFor returns the remembered last track URI for a context, or "".
func (s *resumeStore) trackFor(contextURI string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.track[contextURI]
}

// run flushes the store to NAND on a slow tick whenever it changed, plus once
// on shutdown, until ctx ends. A single debounced writer keeps flash wear low.
func (s *resumeStore) run(ctx context.Context) {
	if s.path == "" {
		return
	}
	t := time.NewTicker(resumeFlushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.flush()
			return
		case <-t.C:
			s.flush()
		}
	}
}

// flush writes the map to NAND atomically (temp + rename) when dirty, so a power
// loss mid-write cannot leave a torn JSON file (the box loses power abruptly).
func (s *resumeStore) flush() {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	b, err := json.Marshal(s.track)
	s.dirty = false
	s.mu.Unlock()
	if err != nil || s.path == "" {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		s.logger.Debug("spotify: resume store write failed", "err", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		s.logger.Debug("spotify: resume store rename failed", "err", err)
		_ = os.Remove(tmp) // don't orphan the temp file on NAND
	}
}
