// volumesync.go: keeps the Spotify app's volume slider and the speaker's
// volume in step, in both directions, with echo protection.
//
// The speaker owns the real volume: go-librespot runs with external_volume
// (passthrough Ogg cannot be scaled by the engine), so a Connect volume command
// only surfaces as a "volume" event that STR writes to the box, and the volume
// the Spotify app shows is whatever the engine last put into its Connect
// state. Two things went wrong before this file existed:
//
//   - Nothing told the engine when the speaker's volume changed (hardware keys,
//     IR remote, the STR app, the Bose API), so the slider never followed.
//   - go-librespot announces a new device at volume 65535 (100 %): under
//     external_volume it never applies initial_volume (daemon/player.go, the
//     initialVolumeOnce block is skipped; daemon/player_state.go initState
//     hard-codes player.MaxStateVolume). The app showed the slider at max, and
//     the first touch sent that max to the speaker before the chosen level.
//
// Both are now fed from events, never from a poll: the box's gabbo
// volumeUpdated frame (NoteBoxVolume), and go-librespot's own events stream
// ("playback_ready" when a Connect session has registered the device,
// "active" when the app selects it).

package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// volEchoWindow: how long a value STR sent somewhere is remembered so the
	// echo it causes (an engine "volume" event, a box volumeUpdated frame) is
	// recognised as ours. Value-matched, so a real change to a DIFFERENT level
	// inside the window still goes through.
	volEchoWindow = 3 * time.Second
	// boxVolSettle: a held volume key emits a burst of volumeUpdated frames.
	// The push to the engine waits this long after the first one and then
	// sends the latest value, so a ramp becomes a handful of pushes, not one
	// per step (each engine push is a PutState to Spotify, which rate-limits).
	boxVolSettle = 250 * time.Millisecond
	// maxJumpHold / maxJumpMinRise: the narrow safety net for an engine whose
	// Connect state still says 100 % (a fresh zeroconf session, before the
	// seed lands). A Spotify volume of 100 that would raise the speaker by at
	// least maxJumpMinRise is held this long; if the app sends another value
	// meanwhile (the user dragging DOWN from the bogus max), the 100 is
	// dropped. A real "turn it all the way up" is applied after the hold.
	maxJumpHold    = 1200 * time.Millisecond
	maxJumpMinRise = 25
	// volPushInfoEvery rate-limits the INFO line for box -> Spotify pushes, so
	// a bundle shows that the direction works without one line per key press.
	volPushInfoEvery = 10 * time.Second
)

// errEngineNoSession: go-librespot answered 204, it has no Spotify session
// yet (no saved login, or still logging in). Nothing to sync then; the
// "playback_ready" event seeds the volume once a session exists.
var errEngineNoSession = errors.New("go-librespot has no Spotify session")

type volMark struct {
	pct int
	at  time.Time
}

// volSync is the state of the box <-> Spotify volume bridge. Own mutex, so the
// bridge never contends with the manager's playback state.
type volSync struct {
	mu sync.Mutex
	// toEngine: values STR pushed into the engine (box level, seed, nudge).
	// The engine echoes each as a "volume" event, which must not be written
	// back to the box or fanned out to a group.
	toEngine []volMark
	// toBox: values STR wrote to the box because the Spotify app asked. The
	// box reports each as a volumeUpdated frame, which must not be pushed back
	// into the engine.
	toBox []volMark
	// enginePct is the Connect volume the engine last reported (engineKnown
	// false until the first event of the current session).
	enginePct   int
	engineKnown bool
	// boxPct is the speaker's last known volume (boxKnown false until read).
	boxPct   int
	boxKnown bool
	// eventsLive: STR's events socket to go-librespot is connected, i.e. an
	// engine is up to receive pushes.
	eventsLive bool
	ch         chan int
	// spotifySeq numbers Spotify-originated volume events, so a held max can
	// tell whether a newer value superseded it.
	spotifySeq uint64

	lastPushInfoAt time.Time
	pushes         int
	guardFired     int
	lastSeedReason string
	lastSeedPct    int
	lastSeedAt     time.Time

	// capFn is the speaker's volume ceiling (ok=false: none). A Spotify app
	// slider pushed past it stops at it. nil = no ceiling.
	capFn func() (int, bool)

	// Test seams; nil in production.
	engineSet func(ctx context.Context, pct int) error
	boxSet    func(ctx context.Context, pct int) error
	boxGet    func(ctx context.Context) (int, error)
}

func pruneMarks(list []volMark, now time.Time) []volMark {
	keep := list[:0]
	for _, mk := range list {
		if now.Sub(mk.at) < volEchoWindow {
			keep = append(keep, mk)
		}
	}
	return keep
}

func hasMark(list []volMark, pct int) bool {
	for _, mk := range list {
		if mk.pct == pct {
			return true
		}
	}
	return false
}

func clampPct(pct int) int {
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// SetVolume tells go-librespot the volume as a percent (0..100) so the Spotify
// app's slider shows the speaker's real level. With volume_steps 100 the API
// value is the percent directly. The engine echoes it as a "volume" event,
// which the event loop recognises as ours and does not write back.
func (m *Manager) SetVolume(ctx context.Context, pct int) error {
	return m.engineSetVolume(ctx, clampPct(pct))
}

func (m *Manager) engineSetVolume(ctx context.Context, pct int) error {
	now := time.Now()
	m.vs.mu.Lock()
	m.vs.toEngine = append(pruneMarks(m.vs.toEngine, now), volMark{pct, now})
	set := m.vs.engineSet
	m.vs.mu.Unlock()
	if set != nil {
		return set(ctx, pct)
	}
	if m.client == nil {
		return fmt.Errorf("go-librespot /player/volume: no engine client")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+m.apiAddr+"/player/volume",
		strings.NewReader(fmt.Sprintf(`{"volume":%d}`, pct)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("go-librespot /player/volume: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return errEngineNoSession
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("go-librespot /player/volume: status %d", resp.StatusCode)
	}
	return nil
}

func (m *Manager) readBoxVolume(ctx context.Context) (int, error) {
	m.vs.mu.Lock()
	get := m.vs.boxGet
	m.vs.mu.Unlock()
	if get != nil {
		return get(ctx)
	}
	if m.box == nil {
		return 0, errors.New("no box client")
	}
	v, err := m.box.GetVolume(ctx)
	if err != nil {
		return 0, err
	}
	return v.Target, nil
}

func (m *Manager) writeBoxVolume(ctx context.Context, pct int) error {
	m.vs.mu.Lock()
	set := m.vs.boxSet
	m.vs.mu.Unlock()
	if set != nil {
		return set(ctx, pct)
	}
	if m.box == nil {
		return errors.New("no box client")
	}
	return m.box.SetVolume(ctx, pct)
}

// canMirrorToBox reports whether a Spotify volume event has a speaker to go to.
func (m *Manager) canMirrorToBox() bool {
	m.vs.mu.Lock()
	hooked := m.vs.boxSet != nil
	m.vs.mu.Unlock()
	return hooked || m.box != nil
}

// NoteBoxVolume is the box -> Spotify direction: the agent calls it for every
// gabbo volumeUpdated frame with the speaker's new level (negative when the
// frame carried none; the level is then read from the box). It never blocks:
// the value is handed to a worker that waits out the key burst and pushes the
// latest level to the engine. A level that is the echo of STR mirroring the
// Spotify app onto the box is dropped here, so it never bounces back.
func (m *Manager) NoteBoxVolume(pct int) {
	now := time.Now()
	m.vs.mu.Lock()
	if pct >= 0 {
		pct = clampPct(pct)
		m.vs.boxPct, m.vs.boxKnown = pct, true
		m.vs.toBox = pruneMarks(m.vs.toBox, now)
		if hasMark(m.vs.toBox, pct) {
			m.vs.mu.Unlock()
			m.logger.Debug("spotify: speaker volume change is the echo of a Spotify app change, not pushing it back", "pct", pct)
			return
		}
	}
	if !m.vs.eventsLive {
		m.vs.mu.Unlock()
		return // no engine up to tell
	}
	if m.vs.ch == nil {
		m.vs.ch = make(chan int, 1)
		go m.boxVolumeWorker(m.vs.ch)
	}
	ch := m.vs.ch
	m.vs.mu.Unlock()
	for {
		select {
		case ch <- pct:
			return
		default:
			select { // a value is queued: the newer one replaces it
			case <-ch:
			default:
			}
		}
	}
}

func (m *Manager) boxVolumeWorker(ch chan int) {
	for v := range ch {
		time.Sleep(boxVolSettle)
		burst := 1
	drain:
		for {
			select {
			case nv := <-ch:
				v = nv
				burst++
			default:
				break drain
			}
		}
		m.pushBoxVolumeToEngine(v, burst)
	}
}

func (m *Manager) pushBoxVolumeToEngine(v, burst int) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if v < 0 {
		bv, err := m.readBoxVolume(ctx)
		if err != nil {
			m.logger.Debug("spotify: could not read the speaker volume to pass on", "err", err)
			return
		}
		v = clampPct(bv)
		m.vs.mu.Lock()
		m.vs.boxPct, m.vs.boxKnown = v, true
		m.vs.mu.Unlock()
	}
	m.vs.mu.Lock()
	same := m.vs.engineKnown && m.vs.enginePct == v
	live := m.vs.eventsLive
	m.vs.mu.Unlock()
	if same || !live {
		return
	}
	err := m.engineSetVolume(ctx, v)
	if errors.Is(err, errEngineNoSession) {
		return
	}
	if err != nil {
		m.logger.Debug("spotify: passing the speaker volume to the Spotify app failed", "err", err, "pct", v)
		return
	}
	m.vs.mu.Lock()
	m.vs.pushes++
	info := time.Since(m.vs.lastPushInfoAt) >= volPushInfoEvery
	if info {
		m.vs.lastPushInfoAt = time.Now()
	}
	pushes := m.vs.pushes
	m.vs.mu.Unlock()
	if info {
		m.logger.Info("spotify: speaker volume passed to the Spotify app", "pct", v, "framesInBurst", burst, "pushesSinceStart", pushes)
	} else {
		m.logger.Debug("spotify: speaker volume passed to the Spotify app", "pct", v, "framesInBurst", burst)
	}
}

// seedEngineVolume puts the speaker's real level into the engine's Connect
// state. reason says which event asked:
//
//   - "session-ready": go-librespot's playback_ready, the moment a Spotify
//     session has registered the device (announced at 100 %). This is the
//     seed that matters: it corrects the device volume Spotify shows before
//     the app has opened the picker, so the slider never starts at max.
//   - "engine-connected": STR's events socket (re)connected; covers an engine
//     whose session came up before STR was listening. A 204 (no session) is
//     silent.
//   - "active": the app selected the device. Skipped when the engine already
//     holds the box level; otherwise sets it and nudges once (the app only
//     redraws its slider on a CHANGE).
func (m *Manager) seedEngineVolume(ctx context.Context, reason string) {
	rctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	vol, err := m.readBoxVolume(rctx)
	cancel()
	if err != nil || vol < 0 || vol > 100 {
		return
	}
	m.vs.mu.Lock()
	m.vs.boxPct, m.vs.boxKnown = vol, true
	already := m.vs.engineKnown && m.vs.enginePct == vol
	m.vs.mu.Unlock()
	if already && reason == "active" {
		m.logger.Debug("spotify: Spotify app already shows the speaker volume", "vol", vol)
		return
	}
	set := func(v int) error {
		sctx, c := context.WithTimeout(ctx, 4*time.Second)
		defer c()
		return m.engineSetVolume(sctx, v)
	}
	if err := set(vol); err != nil {
		if !errors.Is(err, errEngineNoSession) {
			m.logger.Debug("spotify: seeding the Spotify app volume failed", "err", err, "reason", reason)
		}
		return
	}
	nudged := false
	if reason == "active" {
		// The seed did not land before the app selected the device (a
		// fresh zeroconf tap, or an engine that predates the session-ready
		// seed). The app may hold the old value and redraw only on a change,
		// so step once to a neighbour and back. Both values are marked as
		// ours, so neither reaches the speaker.
		time.Sleep(1500 * time.Millisecond)
		nudge := vol - 1
		if nudge < 0 {
			nudge = vol + 1
		}
		_ = set(nudge)
		time.Sleep(250 * time.Millisecond)
		_ = set(vol)
		nudged = true
	}
	m.vs.mu.Lock()
	m.vs.lastSeedReason, m.vs.lastSeedPct, m.vs.lastSeedAt = reason, vol, time.Now()
	m.vs.mu.Unlock()
	m.logger.Info("spotify: Spotify app volume set to the speaker's level", "vol", vol, "reason", reason, "nudged", nudged)
}

// noteEngineSessionReset forgets the engine's Connect volume: a new engine
// process or a new Spotify session starts again at go-librespot's 100 %.
func (m *Manager) noteEngineSessionReset() {
	m.vs.mu.Lock()
	m.vs.engineKnown = false
	m.vs.mu.Unlock()
}

func (m *Manager) setEventsLive(live bool) {
	m.vs.mu.Lock()
	m.vs.eventsLive = live
	if live {
		m.vs.engineKnown = false
	}
	m.vs.mu.Unlock()
}

// handleEngineVolumeEvent is the Spotify -> box direction: a go-librespot
// "volume" event {value, max}. STR's own pushes come back as echoes and are
// dropped; a genuine Spotify-app change is written to the speaker and fanned
// out to a multiroom group.
func (m *Manager) handleEngineVolumeEvent(value, maxSteps int) {
	pct := 100
	if maxSteps > 0 {
		pct = clampPct(value * 100 / maxSteps)
	}
	now := time.Now()
	m.vs.mu.Lock()
	m.vs.spotifySeq++
	seq := m.vs.spotifySeq
	m.vs.enginePct, m.vs.engineKnown = pct, true
	m.vs.toEngine = pruneMarks(m.vs.toEngine, now)
	echo := hasMark(m.vs.toEngine, pct)
	boxPct, boxKnown := m.vs.boxPct, m.vs.boxKnown
	m.vs.mu.Unlock()
	// Our OWN pushes (speaker level, seed, nudge) echo back as ordinary volume
	// events and must not be written anywhere. The nudge's middle value once
	// reached the speaker and audibly dropped it a step on every activation
	// ("lauter/leiser springt hoch-runter", 2026-09-25).
	if echo {
		m.logger.Debug("spotify: volume event is our own push, not writing it back", "pct", pct)
		return
	}
	if !m.canMirrorToBox() {
		return // no box client: metadata only, no volume mirror
	}
	if pct >= 100 && boxKnown && pct-boxPct >= maxJumpMinRise {
		// Possibly the engine's placeholder 100 % coming back from an app
		// slider that never saw the real level. Hold it; the drag that follows
		// supersedes it.
		go func() {
			time.Sleep(maxJumpHold)
			m.vs.mu.Lock()
			superseded := m.vs.spotifySeq != seq
			if superseded {
				m.vs.guardFired++
			}
			m.vs.mu.Unlock()
			if superseded {
				m.logger.Info("spotify: ignored a jump to full volume from the Spotify app that was replaced at once by another level",
					"heldPct", pct, "speakerPct", boxPct, "holdMs", maxJumpHold.Milliseconds())
				return
			}
			m.applySpotifyVolumeToBox(pct)
		}()
		return
	}
	m.applySpotifyVolumeToBox(pct)
}

// SetVolumeCapFn installs the speaker's volume ceiling (the agent's
// max-volume setting). The Spotify app can then not turn the speaker past it,
// and its slider is moved back to the ceiling so it shows the real level.
func (m *Manager) SetVolumeCapFn(fn func() (int, bool)) {
	m.vs.mu.Lock()
	m.vs.capFn = fn
	m.vs.mu.Unlock()
}

func (m *Manager) applySpotifyVolumeToBox(pct int) {
	asked := pct
	now := time.Now()
	m.vs.mu.Lock()
	capFn := m.vs.capFn
	m.vs.mu.Unlock()
	if capFn != nil {
		if limit, ok := capFn(); ok && pct > limit {
			pct = limit
		}
	}
	m.vs.mu.Lock()
	m.vs.toBox = append(pruneMarks(m.vs.toBox, now), volMark{pct, now})
	m.vs.boxPct, m.vs.boxKnown = pct, true
	m.vs.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	if err := m.writeBoxVolume(ctx, pct); err != nil {
		m.logger.Debug("spotify: box SetVolume from Spotify event failed", "err", err, "pct", pct)
	}
	if pct != asked {
		// The slider in the Spotify app still shows what was asked for. Move
		// it to the ceiling, or it would sit above a level the speaker will
		// never play at. The engine echoes this push, and the echo filter
		// keeps it from coming back here.
		if err := m.engineSetVolume(ctx, pct); err != nil {
			m.logger.Debug("spotify: could not move the Spotify slider to the volume ceiling", "err", err, "pct", pct)
		}
		m.logger.Info("spotify: volume held at the speaker's ceiling", "asked", asked, "pct", pct)
	} else {
		m.logger.Info("spotify: volume mirrored to box", "pct", pct)
	}
	cancel()
	// A group's volume must reach every follower, not just the master
	// this go-librespot runs on. Handed to a worker so a slow/offline
	// follower can never stall the event loop. The level asked for goes out,
	// not this speaker's ceiling: each follower applies its own.
	m.requestGroupVolume(asked)
}

// VolumeSnapshot is the spotify_volume debug section: what the bridge last
// knew on each side, plus the engine's live Connect volume (one GET on the
// loopback, only when a bundle or the debug page asks).
func (m *Manager) VolumeSnapshot() map[string]any {
	m.vs.mu.Lock()
	out := map[string]any{
		"eventsLive":     m.vs.eventsLive,
		"speakerPct":     -1,
		"engineEventPct": -1,
		"boxPushes":      m.vs.pushes,
		"maxJumpGuard":   m.vs.guardFired,
		"lastSeedReason": m.vs.lastSeedReason,
		"lastSeedPct":    m.vs.lastSeedPct,
	}
	if m.vs.boxKnown {
		out["speakerPct"] = m.vs.boxPct
	}
	if m.vs.engineKnown {
		out["engineEventPct"] = m.vs.enginePct
	}
	if !m.vs.lastSeedAt.IsZero() {
		out["lastSeedAt"] = m.vs.lastSeedAt.UTC().Format(time.RFC3339)
	}
	live := m.vs.eventsLive
	m.vs.mu.Unlock()
	out["engineConnectPct"] = -1
	if live && m.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if b, err := m.apiGet(ctx, "/player/volume"); err == nil && len(b) > 0 {
			var v struct {
				Value int `json:"value"`
				Max   int `json:"max"`
			}
			if json.Unmarshal(b, &v) == nil && v.Max > 0 {
				out["engineConnectPct"] = v.Value * 100 / v.Max
			}
		}
	}
	return out
}
