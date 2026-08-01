// volume.go: the volume and events plane — the go-librespot /events
// WebSocket (volume mirroring, Connect-intent hooks, activation events)
// and the multiroom group volume fan-out.

package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/gorilla/websocket"
)

// SetVolume tells go-librespot the current volume as a percent (0..100) so the
// Spotify app's slider reflects the speaker's real level. With volume_steps
// 100 the API value is the percent directly. This is the box -> Spotify
// direction; the box -> go-librespot caller is the gabbo volumeUpdated hook.
func (m *Manager) SetVolume(ctx context.Context, pct int) error {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	// Mark the change self-caused for the event loop: go-librespot echoes API
	// volume changes as "volume" events, and only genuine Spotify-Connect
	// changes may fan out to the multiroom group (see selfVolActive).
	m.mu.Lock()
	m.selfVolUntil = time.Now().Add(3 * time.Second)
	m.mu.Unlock()
	return m.apiPost(ctx, "/player/volume", fmt.Sprintf(`{"volume":%d}`, pct))
}

// selfVolActive reports whether a recent volume change originated from the
// manager itself (slider seed/nudge) rather than from a Spotify Connect
// client.
func (m *Manager) selfVolActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Now().Before(m.selfVolUntil)
}

// syncVolumeFromBox seeds go-librespot's volume with the box's real level so the
// Spotify app slider starts at the correct value. Without it go-librespot
// defaults to 100% (external_volume ignores initial_volume), so the first slider
// touch jumped the speaker to 100 and then back down to the chosen value.
func (m *Manager) syncVolumeFromBox(ctx context.Context) {
	if m.box == nil {
		return
	}
	st, err := m.box.LoadSettings(ctx)
	if err != nil {
		return
	}
	vol := st.Volume.Actual
	if vol < 0 || vol > 100 {
		return
	}
	set := func(v int) {
		vctx, c := context.WithTimeout(ctx, 4*time.Second)
		_ = m.SetVolume(vctx, v)
		c()
	}
	set(vol)
	// The Spotify app caches go-librespot's default (100) and only updates the
	// slider when it sees a volume CHANGE. Nudge to an adjacent value and back
	// so the app picks up the real level instead of showing 100 until the user
	// first touches the slider (Jens' idea).
	time.Sleep(1500 * time.Millisecond)
	nudge := vol - 1
	if nudge < 0 {
		nudge = vol + 1
	}
	set(nudge)
	time.Sleep(250 * time.Millisecond)
	set(vol)
	m.logger.Info("spotify: seeded + nudged app volume slider from box", "vol", vol)
}

// Connect-intent plumbing: a pause/stop/transfer pressed in the Spotify app
// reaches STR only as a go-librespot event - no gabbo key frame, no STR
// endpoint. These hooks feed it into the box-side stop/play latches so the
// starved box's subsequent source drop reads as deliberate, not as a
// spontaneous firmware off that the auto-revive would undo (#78: "playback
// could not be stopped from the Spotify app; only pulling power helped").

// ownEngineCmdIntentWindow: engine state events this soon after one of STR's
// own /player transport commands are echoes of that command (a recall loads
// the context paused, pauses again belt-and-braces, then resumes), not the
// user acting in the Spotify app. Sized to cover the slowest staged recall:
// play -> waitContextLoaded (5s) -> replay-from-top -> shuffle -> resume.
const ownEngineCmdIntentWindow = 15 * time.Second

// SetConnectIntentHooks wires deliberate playback intent from the Spotify app
// into the box-side latches. onPause fires on paused/stopped/inactive events
// outside the own-command window; onPlay fires on active/playing. nil-safe.
func (m *Manager) SetConnectIntentHooks(onPause func(event string), onPlay func()) {
	m.mu.Lock()
	m.connectPauseFn = onPause
	m.connectPlayFn = onPlay
	m.mu.Unlock()
}

// noteOwnPlayerCmd stamps STR's own engine transport commands so their echoed
// state events are not misread as Spotify-app intent. Volume is excluded: it
// carries no transport intent, and stamping it would mask a real pause the
// user presses right after adjusting the volume.
func (m *Manager) noteOwnPlayerCmd(path string) {
	if !strings.HasPrefix(path, "/player/") || path == "/player/volume" {
		return
	}
	m.mu.Lock()
	m.lastOwnPlayerCmd = time.Now()
	m.mu.Unlock()
}

func (m *Manager) ownPlayerCmdRecent() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.lastOwnPlayerCmd.IsZero() && time.Since(m.lastOwnPlayerCmd) < ownEngineCmdIntentWindow
}

// handleEnginePlaybackEnd forwards a paused/stopped/inactive engine event as
// deliberate user intent, unless it is the echo of STR's own staged command.
func (m *Manager) handleEnginePlaybackEnd(evType string) {
	if m.ownPlayerCmdRecent() {
		m.logger.Debug("spotify: engine state event inside own-command window, not user intent", "event", evType)
		return
	}
	m.mu.Lock()
	fn := m.connectPauseFn
	m.mu.Unlock()
	if fn == nil {
		return
	}
	// INFO on purpose: this line is the bundle-forensics marker that separates
	// "user stopped via the Spotify app" from the firmware self-off (#419
	// family) when a diagnostic arrives.
	m.logger.Info("spotify: Spotify app ended playback on this box, arming the deliberate-stop latch", "event", evType)
	fn(evType)
}

// handleEnginePlaybackStart clears the stop latch when playback starts or the
// device becomes active again, so the recovery paths a deliberate stop parked
// come back to life for the play the user just asked for.
func (m *Manager) handleEnginePlaybackStart() {
	m.mu.Lock()
	fn := m.connectPlayFn
	m.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// watchVolume subscribes to go-librespot's /events WebSocket and mirrors every
// Spotify-app volume change onto the box. go-librespot runs with
// external_volume, so a Connect volume command does not touch its audio; it
// surfaces here as a "volume" event {value, max} which we scale to a percent
// and push to the box over the Bose REST API. Reconnects with a short backoff.
func (m *Manager) watchVolume(ctx context.Context) {
	url := "ws://" + m.apiAddr + "/events"
	for ctx.Err() == nil {
		if err := m.volumeStream(ctx, url); err != nil && ctx.Err() == nil {
			m.logger.Debug("spotify: events stream ended", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (m *Manager) volumeStream(ctx context.Context, url string) error {
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := d.DialContext(ctx, url, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Closer goroutine is bound to a per-call context cancelled on return, so
	// it never outlives this stream. The earlier version waited on the
	// long-lived parent ctx and leaked one goroutine (holding conn) per
	// reconnect; with frequent go-librespot restarts that fed an OOM.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-sctx.Done(); conn.Close() }()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var ev struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "active":
			// The Spotify app selected this device. Switch the box to the Spotify
			// stream if it is on another source (#14), and seed the app's volume
			// slider with the box's real level so the first slider touch does not
			// jump the speaker to 100% first.
			m.maybeActivate()
			go m.syncVolumeFromBox(context.Background())
			m.handleEnginePlaybackStart()
		case "playing":
			m.maybeActivate()
			m.handleEnginePlaybackStart()
		case "paused", "stopped", "inactive":
			// The Spotify app paused/stopped playback on this box, or moved it
			// to another device. Forward as deliberate intent (guarded against
			// echoes of STR's own staged recalls inside).
			m.handleEnginePlaybackEnd(ev.Type)
		case "will_play":
			// A track is about to play; if its context (playlist/album) differs
			// from the last one, the app switched playlists. Re-point the box so
			// it drops the old buffer and plays the new stream promptly.
			var wp struct {
				ContextURI string `json:"context_uri"`
			}
			if json.Unmarshal(ev.Data, &wp) == nil && wp.ContextURI != "" {
				m.mu.Lock()
				changed := m.lastContext != "" && wp.ContextURI != m.lastContext
				m.lastContext = wp.ContextURI
				if changed {
					// New context: drop the previous track so noteResume cannot
					// pair this context with the old track before its own
					// metadata lands (review, 2026-06-25).
					m.curTrackURI = ""
				}
				m.mu.Unlock()
				if changed {
					m.repointBox()
				}
			}
		case "metadata":
			// Current track info for the desktop (and later box) display.
			var md struct {
				URI           string   `json:"uri"`
				Name          string   `json:"name"`
				ArtistNames   []string `json:"artist_names"`
				AlbumCoverURL string   `json:"album_cover_url"`
			}
			if err := json.Unmarshal(ev.Data, &md); err != nil {
				continue
			}
			m.mu.Lock()
			m.curName = md.Name
			m.curArtist = strings.Join(md.ArtistNames, ", ")
			m.curCover = md.AlbumCoverURL
			if md.URI != "" {
				m.curTrackURI = md.URI
			}
			m.mu.Unlock()
			m.notifyTrack()
			// Remember this track as the resume point for its context, so a
			// later default recall continues here instead of restarting the
			// playlist (the events stream covers the no-desktop-app case).
			m.noteResume()
		case "volume":
			if m.box == nil {
				continue // no box client: metadata only, no volume mirror
			}
			var vd struct {
				Value int `json:"value"`
				Max   int `json:"max"`
			}
			if err := json.Unmarshal(ev.Data, &vd); err != nil {
				continue
			}
			pct := 100
			if vd.Max > 0 {
				pct = vd.Value * 100 / vd.Max
			}
			sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			if err := m.box.SetVolume(sctx, pct); err != nil {
				m.logger.Debug("spotify: box SetVolume from Spotify event failed", "err", err, "pct", pct)
			}
			cancel()
			m.logger.Info("spotify: volume mirrored to box", "pct", pct)
			// A group's volume must reach every follower, not just the master
			// this go-librespot runs on. Only for GENUINE Connect changes:
			// the manager's own seed/nudge echoes back as the same event, and
			// fanning that out rewrote every follower's individually-set
			// level on each device activation. Handed to a worker so a slow/
			// offline follower can never stall this event loop.
			if m.selfVolActive() {
				m.logger.Debug("spotify: volume event caused by own seed/nudge, not fanning out to group", "pct", pct)
			} else {
				m.requestGroupVolume(pct)
			}
		}
	}
}

// requestGroupVolume hands a group volume fan-out to the dedicated worker with
// latest-value coalescing: a slider-drag burst collapses to the final level,
// and the go-librespot event loop never blocks on follower HTTP calls.
func (m *Manager) requestGroupVolume(pct int) {
	m.mu.Lock()
	if m.volFanCh == nil {
		m.volFanCh = make(chan int, 1)
		go m.volumeFanWorker(m.volFanCh)
	}
	ch := m.volFanCh
	m.mu.Unlock()
	for {
		select {
		case ch <- pct:
			return
		default:
			// Worker busy and a value queued: drop the superseded one.
			select {
			case <-ch:
			default:
			}
		}
	}
}

// volumeFanWorker serially applies queued group fan-outs. Serial on purpose:
// each fan-out is itself parallel across followers, and running two fan-outs
// concurrently could deliver an old level after a newer one.
func (m *Manager) volumeFanWorker(ch chan int) {
	for pct := range ch {
		m.mirrorVolumeToGroup(context.Background(), pct)
	}
}

// mirrorVolumeToGroup pushes pct onto every multiroom follower this box leads,
// in parallel and best-effort, so a Spotify Connect volume change applies to the
// whole group (go-librespot runs only on the master, so without this only the
// master's volume moves). The follower list provider live-verifies the zone,
// so a speaker that left the group is never touched.
func (m *Manager) mirrorVolumeToGroup(ctx context.Context, pct int) {
	m.mu.Lock()
	fn := m.groupSlaveIPsFn
	set := m.groupVolumeSetFn
	m.mu.Unlock()
	if fn == nil {
		return
	}
	if set == nil {
		set = m.setFollowerVolume
	}
	ips := fn()
	if len(ips) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, ip := range ips {
		if ip == "" {
			continue
		}
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := set(sctx, ip, pct); err != nil {
				m.logger.Debug("spotify: group follower SetVolume failed", "ip", ip, "err", err, "pct", pct)
			}
		}(ip)
	}
	wg.Wait()
	m.logger.Info("spotify: volume mirrored to group followers", "count", len(ips), "pct", pct)
}

// setFollowerVolume sets one follower's volume, preferring the follower's STR
// agent (which serializes box commands behind its own lock - a raw :8090
// volume PUT can land mid play-start and kill it, a documented firmware
// flaw) and falling back to the Bose port when the agent is unreachable.
func (m *Manager) setFollowerVolume(ctx context.Context, ip string, pct int) error {
	body := fmt.Sprintf(`{"value":%d}`, pct)
	var lastErr error
	for _, port := range []string{"17008", "8888"} {
		rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		req, err := http.NewRequestWithContext(rctx, http.MethodPut,
			"http://"+ip+":"+port+"/api/box/volume", strings.NewReader(body))
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("agent volume PUT %s: status %d", port, resp.StatusCode)
	}
	// No reachable STR agent on the follower: drive the Bose port directly.
	if err := boxapi.New(ip).SetVolume(ctx, pct); err != nil {
		return fmt.Errorf("%w (agent fallback: %v)", err, lastErr)
	}
	return nil
}
