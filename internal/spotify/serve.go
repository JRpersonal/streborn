// serve.go: the HTTP surface — ServeOgg (the box's audio fetch) and
// ServeInfo, plus the now-playing metadata helpers behind them.

package spotify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// PlaylistMeta returns a stable cover image URL and the human title for a
// Spotify context URI (playlist, album, ...) via Spotify's public oEmbed
// endpoint, which needs no token. A saved preset uses the cover as its tile logo
// (#24) and the title as its name, so the box display and the tile show e.g.
// "Jens Chill" instead of a bare "Spotify". Returns "","" on any failure.
// Best-effort, called off the play path (on preset save).
func (m *Manager) PlaylistMeta(ctx context.Context, uri string) (cover, title string) {
	page := spotifyURItoURL(uri)
	if page == "" {
		return "", ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://open.spotify.com/oembed?url="+url.QueryEscape(page), nil)
	if err != nil {
		return "", ""
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}
	var od struct {
		ThumbnailURL string `json:"thumbnail_url"`
		Title        string `json:"title"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&od); err != nil {
		return "", ""
	}
	return od.ThumbnailURL, od.Title
}

// spotifyURItoURL converts spotify:playlist:ID (or album/track/artist) to its
// open.spotify.com page URL, or "" for an unrecognised URI.
func spotifyURItoURL(uri string) string {
	parts := strings.Split(uri, ":")
	if len(parts) != 3 || parts[0] != "spotify" {
		return ""
	}
	switch parts[1] {
	case "playlist", "album", "track", "artist":
		return "https://open.spotify.com/" + parts[1] + "/" + parts[2]
	}
	return ""
}

// Bitrate returns the bitrate measured from the live stream (kbit/s), or the
// configured nominal when nothing has streamed yet.
func (m *Manager) Bitrate() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.actualKbps > 0 {
		return m.actualKbps
	}
	return m.bitr
}

// Streaming reports whether a box is currently attached to the Ogg stream
// (i.e. Spotify is actively playing to the speaker). The memory guard uses
// this to avoid rebooting the box mid-playback.
func (m *Manager) Streaming() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sink != nil
}

// liveNowPlaying pulls the current track straight from go-librespot's /status,
// the authoritative source, and refreshes the cache with it. The cached values
// come from pushed "metadata" events, which lag (and can be missed entirely):
// a live capture showed /spotify/info still reporting an earlier track while
// go-librespot had advanced several (#136). Pulling /status on demand keeps the
// desktop now-playing line in step with what is actually playing. Best-effort:
// returns false and leaves the cache untouched if /status is unreachable or
// carries no track, so the caller falls back to the cached values.
func (m *Manager) liveNowPlaying(ctx context.Context) (track, artist, cover string, ok bool) {
	data, err := m.apiGet(ctx, "/status")
	if err != nil {
		return "", "", "", false
	}
	var st struct {
		Track *struct {
			URI           string   `json:"uri"`
			Name          string   `json:"name"`
			ArtistNames   []string `json:"artist_names"`
			AlbumCoverURL string   `json:"album_cover_url"`
		} `json:"track"`
	}
	if json.Unmarshal(data, &st) != nil || st.Track == nil || st.Track.Name == "" {
		return "", "", "", false
	}
	track = st.Track.Name
	artist = strings.Join(st.Track.ArtistNames, ", ")
	cover = st.Track.AlbumCoverURL
	m.mu.Lock()
	m.curName, m.curArtist, m.curCover = track, artist, cover
	if st.Track.URI != "" {
		m.curTrackURI = st.Track.URI
	}
	m.mu.Unlock()
	m.notifyTrack()
	m.noteResume()
	return track, artist, cover, true
}

// SetOnTrack registers the recently-played hook (webui.NoteRecentSpotifyTrack).
func (m *Manager) SetOnTrack(fn func(track, artist string)) {
	m.mu.Lock()
	m.onTrack = fn
	m.mu.Unlock()
}

// notifyTrack fires onTrack when the current Spotify track changed since the
// last notification, so each song is recorded once. Called after every
// metadata/status update; the dedup on the track name keeps a repeated /status
// poll from re-recording. The callback runs outside the lock. Cheap (#135).
func (m *Manager) notifyTrack() {
	m.mu.Lock()
	cb, track, artist := m.onTrack, m.curName, m.curArtist
	if cb == nil || track == "" || track == m.lastNotifiedTrack {
		m.mu.Unlock()
		return
	}
	m.lastNotifiedTrack = track
	m.mu.Unlock()
	cb(track, artist)
}

// ServeInfo answers GET /spotify/info with the live state the UI needs: whether
// Spotify is available, the measured bitrate, and the advertised device name.
func (m *Manager) ServeInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	m.mu.Lock()
	track, artist, cover, context := m.curName, m.curArtist, m.curCover, m.lastContext
	lowDisk, lowDiskFreeKB := m.lowDisk, m.lowDiskFreeKB
	m.mu.Unlock()
	// Prefer the live track from /status over the laggy cached metadata events.
	if lt, la, lc, ok := m.liveNowPlaying(r.Context()); ok {
		track, artist, cover = lt, la, lc
	}
	resp := struct {
		Ready   bool   `json:"ready"`
		Bitrate int    `json:"bitrate"`
		Name    string `json:"name"`
		Track   string `json:"track"`
		Artist  string `json:"artist"`
		Cover   string `json:"cover"`
		Context string `json:"context"` // current playlist/album URI (for saving a Spotify preset)
		Account string `json:"account"` // current go-librespot login (for the preset)
		// PremiumRequired is true when the logged-in Spotify account is free/open,
		// which cannot do the autonomous on-demand playback a preset recall needs
		// (#45). The UI shows a "recall needs Premium" note when set.
		PremiumRequired bool `json:"premiumRequired"`
		// LowDisk is true when the box NAND is too full to start go-librespot
		// (#ST30). The UI shows "box storage full" instead of Spotify silently
		// appearing unavailable; LowDiskFreeKB is the free space at the last check.
		LowDisk       bool  `json:"lowDisk"`
		LowDiskFreeKB int64 `json:"lowDiskFreeKB"`
	}{
		Ready:           m.Ready(),
		Bitrate:         m.Bitrate(),
		Name:            m.DeviceName(),
		Track:           track,
		Artist:          artist,
		Cover:           cover,
		Context:         context,
		Account:         m.currentUsername(r.Context()),
		PremiumRequired: m.PremiumRequired(),
		LowDisk:         lowDisk,
		LowDiskFreeKB:   lowDiskFreeKB,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// ServeOgg streams go-librespot's live Ogg/Vorbis passthrough output to the
// HTTP client (the box's UPnP fetch) until it disconnects. It registers as
// the single consumer; a new request replaces any previous one. No header is
// prepended: the box decodes the raw Ogg directly.
// Re-attach storm damping (#136, #113). A re-attach closer together than the
// window counts toward a storm and grows the box-re-point backoff from the base
// up to the cap; anything more spaced out is treated as a normal switch and
// clears the backoff.
const (
	spotifyStormWindow         = 20 * time.Second
	spotifyActivateBackoffBase = 5 * time.Second
	spotifyActivateBackoffMax  = 60 * time.Second
)

func (m *Manager) ServeOgg(w http.ResponseWriter, r *http.Request) {
	if !m.Ready() {
		http.Error(w, "spotify not configured", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "audio/ogg")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)

	// Replay the current track's cached Ogg header pages first so a box that
	// joins mid-track has the identification/comment/setup headers it needs
	// to initialise the decoder; the live pages (forwarded by the drain)
	// then follow and are decodable even though they start mid-track.
	m.mu.Lock()
	hdr := append([]byte(nil), m.headerPages...)
	m.mu.Unlock()
	if len(hdr) > 0 {
		if _, err := w.Write(hdr); err != nil {
			return
		}
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	done := make(chan struct{})
	cw := &closeNotifyWriter{w: w, done: done}
	m.mu.Lock()
	oldSink, _ := m.sink.(*closeNotifyWriter) // previous consumer, if any
	reattach := m.sink != nil                 // a consumer was already attached = box re-fetched
	m.sink = cw
	sinceLast := time.Duration(0)
	if !m.lastAttachAt.IsZero() {
		sinceLast = time.Since(m.lastAttachAt)
	}
	m.lastAttachAt = time.Now()
	m.mu.Unlock()
	// Single-connection invariant: tear down the previous box connection now.
	// A box stuck in INVALID_SOURCE re-fetches the stream repeatedly; if the old
	// connections are left open they pile up and the box leaks decode/socket
	// buffers per connection until it OOMs (garbled audio then reboot, live
	// 2026-06-10). Closing the old sink makes its ServeOgg return and drop the
	// stale connection, so the box only ever holds one Ogg stream at a time.
	if oldSink != nil && oldSink != cw {
		oldSink.closeConn()
	}
	// Surface and damp a re-attach storm (the box re-fetching every few seconds,
	// the INVALID_SOURCE re-point loop heard as the song restarting). The
	// single-connection invariant above already prevents the per-connection
	// buffer pile-up that used to OOM the box; here we also back off STR's own
	// re-pointing so it stops shoving the box back into the same failing state.
	// A rapid re-attach grows the backoff (capped); a healthy, spaced-out attach
	// resets it so normal playlist switches stay responsive.
	if reattach && sinceLast > 0 && sinceLast < spotifyStormWindow {
		m.mu.Lock()
		if m.activateBackoff < spotifyActivateBackoffBase {
			m.activateBackoff = spotifyActivateBackoffBase
		} else {
			m.activateBackoff *= 2
			if m.activateBackoff > spotifyActivateBackoffMax {
				m.activateBackoff = spotifyActivateBackoffMax
			}
		}
		backoff := m.activateBackoff
		if t := time.Now().Add(backoff); t.After(m.suppressActivateUntil) {
			m.suppressActivateUntil = t
		}
		m.mu.Unlock()
		m.logger.Warn("spotify: rapid Ogg re-attach (INVALID_SOURCE re-point storm); backing off box re-point",
			"sinceLastMs", sinceLast.Milliseconds(), "backoff", backoff.String())
	} else if reattach && sinceLast >= spotifyStormWindow {
		// A spaced-out re-attach is normal (a deliberate playlist switch): the
		// storm has cleared, so drop the accumulated backoff.
		m.mu.Lock()
		m.activateBackoff = 0
		m.mu.Unlock()
	}
	// reattach=true means the box dropped and re-fetched the stream (the prime
	// suspect for a track appearing to restart): it then gets the cached
	// granule-0 headers again. Logged so the restart can be correlated.
	m.mu.Lock()
	m.sinkAttachedAt, m.sinkBytes, m.sinkPages = time.Now(), 0, 0
	m.sinkFirstAudioAt, m.sinkLastPageAt = time.Time{}, time.Time{}
	m.mu.Unlock()
	m.logger.Info("spotify: box attached to Ogg stream", "remote", r.RemoteAddr, "headerBytes", len(hdr), "reattach", reattach)

	// A fresh (non-reattach) attach is a clean recall start, not a storm: clear
	// any accumulated re-point backoff so the next genuine playlist switch is
	// handled promptly.
	if !reattach {
		m.mu.Lock()
		m.activateBackoff = 0
		m.mu.Unlock()
	}

	// On a FRESH attach (not a re-fetch), the box's own preset self-activation
	// can reach ServeOgg a beat BEFORE the gabbo press event flags the recall
	// (race, seen when switching from radio to a Spotify preset). Wait briefly
	// for the flag so we don't resume the old (mid) track before Play loads the
	// new shuffled one.
	if !reattach {
		for i := 0; i < 10 && !m.recalling(); i++ {
			time.Sleep(50 * time.Millisecond)
		}
	}
	// The drain pauses go-librespot while no box is attached; resume it so the
	// live stream flows to this box. Do NOT resume while a recall is driving
	// playback: Play() loads and resumes the NEW track itself, so a resume here
	// would only replay the OLD one. This covers both a fresh recall attach and a
	// same-account preset switch (reattach): the latter used to resume the previous
	// playlist's track for a few seconds until Play caught up, which the box played
	// out as audible overlap (ST30 5->4 switch, 2026-07-14). The one exception is a
	// cross-account recall: SwitchAccount restarted go-librespot and left it paused
	// in the restart gap, so on the box's re-fetch resume it or it hangs buffering
	// on a paused stream (observed: preset stuck after playing another account).
	if !m.recalling() || (reattach && m.recallRestartedRecently()) {
		rctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		_ = m.Resume(rctx)
		cancel()
	}

	select {
	case <-r.Context().Done():
	case <-done:
	}
	m.mu.Lock()
	if m.sink == cw {
		m.sink = nil
	}
	m.mu.Unlock()
	m.mu.Lock()
	attachedMs := int64(0)
	if !m.sinkAttachedAt.IsZero() {
		attachedMs = time.Since(m.sinkAttachedAt).Milliseconds()
	}
	firstAudioMs := int64(-1)
	if !m.sinkFirstAudioAt.IsZero() && !m.sinkAttachedAt.IsZero() {
		firstAudioMs = m.sinkFirstAudioAt.Sub(m.sinkAttachedAt).Milliseconds()
	}
	bytes, pages := m.sinkBytes, m.sinkPages
	m.mu.Unlock()
	kbps := int64(0)
	if attachedMs > 0 {
		kbps = bytes * 8 / attachedMs
	}
	// firstAudioMs = -1 means the box was attached but never received a single
	// audio page: the silent-stream failure that used to look like success.
	m.logger.Info("spotify: box detached from Ogg stream",
		"attachedMs", attachedMs, "forwardedKB", bytes/1024, "pages", pages,
		"firstAudioAfterMs", firstAudioMs, "kbps", kbps)
}

// closeNotifyWriter signals done on the first failed write so ServeOgg
// returns when the box drops the connection.
type closeNotifyWriter struct {
	w    io.Writer
	done chan struct{}
	once sync.Once
}

func (c *closeNotifyWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if err != nil {
		c.once.Do(func() { close(c.done) })
	}
	return n, err
}

// closeConn tears the connection down from the manager side, used to enforce
// the single-connection invariant when a new box attaches. Idempotent.
func (c *closeNotifyWriter) closeConn() {
	c.once.Do(func() { close(c.done) })
}

func (c *closeNotifyWriter) Flush() {
	if f, ok := c.w.(http.Flusher); ok {
		f.Flush()
	}
}
