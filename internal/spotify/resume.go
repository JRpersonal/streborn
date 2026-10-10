package spotify

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/atomicfile"
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

	// Diagnostics only, never persisted: when each entry last changed in this
	// agent run, and what the most recent recall asked the engine for.
	// Together they let a diagnostic file say which context wrote a resume
	// point and when, and which skip_to_uri a recall actually used.
	notedAt    map[string]time.Time
	lastRecall *resumeRecall
}

type resumeRecall struct {
	contextURI string
	skipToURI  string
	shuffle    bool
	at         time.Time
}

const (
	resumeMaxContexts = 64
	resumeFlushEvery  = 30 * time.Second
)

// newResumeStore loads any persisted map from path (best-effort) and returns a
// store ready to note/query. A path of "" disables persistence (the in-memory
// map still works, used by tests).
func newResumeStore(path string, logger *slog.Logger) *resumeStore {
	s := &resumeStore{path: path, logger: logger, track: map[string]string{}, notedAt: map[string]time.Time{}}
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
	s.notedAt[contextURI] = time.Now()
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
		delete(s.notedAt, old)
	}
}

// trackFor returns the remembered last track URI for a context, or "".
func (s *resumeStore) trackFor(contextURI string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.track[contextURI]
}

// forget drops the resume point of contextURI, but only while it still names
// trackURI: a recall found that track gone from the context, and a newer
// point noted in the meantime is not stale. No-op on a nil store.
func (s *resumeStore) forget(contextURI, trackURI string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tr, ok := s.track[contextURI]; !ok || tr != trackURI {
		return
	}
	delete(s.track, contextURI)
	delete(s.notedAt, contextURI)
	for i, c := range s.order {
		if c == contextURI {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.dirty = true
}

// noteRecall records what a preset recall asked the engine for: the context it
// loaded and the resume track it passed as skip_to_uri ("" when it started the
// context from the top or shuffled). Diagnostics only.
func (s *resumeStore) noteRecall(contextURI, skipToURI string, shuffle bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastRecall = &resumeRecall{contextURI: contextURI, skipToURI: skipToURI, shuffle: shuffle, at: time.Now()}
	s.mu.Unlock()
}

// ResumePoint is one entry of the spotify_resume debug section.
type ResumePoint struct {
	ContextURI string `json:"contextUri"`
	TrackURI   string `json:"trackUri"`
	// NotedAt is when this agent run last changed the entry (RFC 3339). Empty
	// for an entry loaded from NAND and not changed since the agent started.
	NotedAt string `json:"notedAt,omitempty"`
}

// ResumeRecall is the last preset recall's use of the store.
type ResumeRecall struct {
	ContextURI string `json:"contextUri"`
	SkipToURI  string `json:"skipToUri"`
	Shuffle    bool   `json:"shuffle"`
	At         string `json:"at"`
}

// ResumeSnapshot is the spotify_resume section of /api/debug/state. It holds
// URIs and times only, no account names: a legacy spotify:user:<id>:... URI
// has its account part replaced before it leaves the agent. The store keeps no
// playback position; a recall resumes on the remembered track from its start.
type ResumeSnapshot struct {
	Count int `json:"count"`
	// Points is ordered most-recently touched first.
	Points     []ResumePoint `json:"points"`
	LastRecall *ResumeRecall `json:"lastRecall,omitempty"`
}

// snapshot returns the store's contents for the diagnostic file.
func (s *resumeStore) snapshot() ResumeSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := ResumeSnapshot{Points: make([]ResumePoint, 0, len(s.track))}
	seen := make(map[string]bool, len(s.track))
	add := func(ctx string) {
		tr, ok := s.track[ctx]
		if !ok || seen[ctx] {
			return
		}
		seen[ctx] = true
		p := ResumePoint{ContextURI: redactSpotifyUser(ctx), TrackURI: redactSpotifyUser(tr)}
		if at, ok := s.notedAt[ctx]; ok {
			p.NotedAt = at.UTC().Format(time.RFC3339)
		}
		out.Points = append(out.Points, p)
	}
	for i := len(s.order) - 1; i >= 0; i-- {
		add(s.order[i])
	}
	for ctx := range s.track { // defensive: an entry missing from the LRU order
		add(ctx)
	}
	out.Count = len(out.Points)
	if r := s.lastRecall; r != nil {
		out.LastRecall = &ResumeRecall{
			ContextURI: redactSpotifyUser(r.contextURI),
			SkipToURI:  redactSpotifyUser(r.skipToURI),
			Shuffle:    r.shuffle,
			At:         r.at.UTC().Format(time.RFC3339),
		}
	}
	return out
}

// redactSpotifyUser replaces the account in a legacy spotify:user:<id>:...
// URI, the only Spotify URI shape that names an account.
func redactSpotifyUser(uri string) string {
	const prefix = "spotify:user:"
	if len(uri) < len(prefix) || !strings.EqualFold(uri[:len(prefix)], prefix) {
		return uri
	}
	rest := uri[len(prefix):]
	tail := ""
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		tail = rest[i:]
	}
	return prefix + "redacted" + tail
}

// ResumeSnapshot returns the per-context resume points for the diagnostic file
// (debug section spotify_resume).
func (m *Manager) ResumeSnapshot() ResumeSnapshot {
	if m.resume == nil {
		return ResumeSnapshot{Points: []ResumePoint{}}
	}
	return m.resume.snapshot()
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
	// Durable write (fsync + rename): a plain write+rename can leave the file at
	// 0 bytes after a speaker's standby power-cut.
	if err := atomicfile.WriteFile(s.path, b, 0o644); err != nil {
		s.logger.Debug("spotify: resume store write failed", "err", err)
	}
}
