// Connection lifecycle: the Client struct, its constructor, the reconnect
// loop, the WebSocket keepalive, and the small state accessors it exposes.

package boxws

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client holds the connection to the box.
type Client struct {
	logger  *slog.Logger
	url     string
	handler Handler

	// lastSignal is the most recent Wi-Fi signal class the box reported
	// over the gabbo stream (GOOD_SIGNAL / MARGINAL_SIGNAL / ...). On BCO
	// speakers (Portable, scm ST20) /networkInfo exposes no signal, so
	// the settings UI uses this instead. Guarded; read via LastWifiSignal.
	// lastSignalAt: connectionState frames fire on connection TRANSITIONS,
	// i.e. mostly at boot while the link is still settling, and then never
	// again in steady state. Without an expiry, a low boot-time reading
	// stuck for the whole uptime and a Portable one meter from the router
	// showed "marginal signal" all day (Jens, 2026-07-12).
	mu           sync.Mutex
	lastSignal   string
	lastSignalAt time.Time
	// lastLang / lastLangAt time the most recent languageUpdated frame so a
	// second frame with a DIFFERENT value arriving within languageRevertWindow
	// is flagged as a firmware-side revert (Wave: user saves German=2, box
	// broadcasts 2 then 3 within 200 ms and the setting is back on English).
	lastLang   string
	lastLangAt time.Time
	// err1036Times is a sliding window of recent 1036 rejections; when the box
	// answers essentially every recall with 1036 (the storm state that today
	// only a power-cycle or soft reboot clears), one bounded WARN marks the
	// storm so bundles can correlate it with the boot clock and marge trail.
	err1036Times   []time.Time
	lastStormLogAt time.Time
	// prevEndedIdle marks that the previous WS session ended in a plain idle
	// read timeout, so the next "connected" phase marker logs at Debug instead
	// of churning the NAND log. Only touched from the Run loop goroutine.
	prevEndedIdle bool
	// lastSource tracks the most recent active source seen on a now-selection /
	// now-playing frame, so the aux webhook fires once on the transition to AUX
	// rather than repeatedly while AUX stays the active source.
	lastSource string

	// unknownFrames counts the frame shapes STR does not handle, keyed by the
	// element name, so the first of each shape can be logged in full and the
	// repeats only counted. unknownSummaryAt times the periodic roll-up.
	// Guarded by mu.
	unknownFrames    map[string]int
	unknownSummaryAt time.Time

	// lastInvalidSourceAt / lastPresetPressAt time the box's own UPnP-source
	// teardown. On scm/mojo firmware (ST30) a preset switch AND an involuntary
	// stream drop both tear STR's UPNP source down through INVALID_SOURCE and
	// emit a transient STOP_STATE nowPlaying frame. Treating that STOP_STATE as a
	// deliberate user stop latched lastUserStop, which then suppressed BOTH the
	// box-side-drop recovery (maybeRePush: a radio stream stayed dead after a few
	// minutes) and the recall verify retry (verifyPlayURL: a re-press never
	// recovered a SetURI that raced the wake) - the preset buttons looked broken
	// (#ST30 "button 2 dies after a few minutes, re-press does not fix it",
	// 2026-07-11). These stamps let the STOP_STATE handler tell that teardown
	// apart from a genuine stop. Guarded by mu.
	lastInvalidSourceAt time.Time
	lastPresetPressAt   time.Time

	// lastOwnCmdAt is when STR itself last issued a transport-mutating SOAP
	// command (SetURI/Play/Pause/Stop), stamped via NoteOwnTransportCommand from
	// the upnp renderer's OnTransportCommand hook. The box answers a SOAP Stop
	// (and a SetURI flip of an active transport) with a nowPlaying STOP_STATE
	// that looks exactly like the user pressing stop; the v0.9.16 wrong-state
	// repair (Stop+ClearURI ~1.5 s into a recall, after the press window
	// expired) therefore latched a phantom user stop that aborted its OWN
	// verify loop and stood every recovery down (#252 post-v0.9.16: "remote
	// useless"). stopStateIsTeardown reads this stamp to excuse such an echo -
	// unless a fresh physical key press accompanied it, which means the user
	// really did stop. Guarded by mu.
	lastOwnCmdAt time.Time

	// lastUpnpActiveAt is when STR's own source (UPNP) last stopped being the
	// active source. The firmware's give-up after a failed self-activation
	// reaches STANDBY through INVALID_SOURCE (UPNP -> INVALID_SOURCE ->
	// STANDBY), and the prev==UPNP gate alone made that route bypass the
	// standby handling entirely. Guarded by mu.
	lastUpnpActiveAt time.Time

	// lastNativeActiveAt is when the box last stopped being on a native radio
	// station. Used to tell a station the box ABANDONED (it reaches
	// INVALID_SOURCE shortly after) from the normal teardown of a preset
	// change (which returns to the native source within a few hundred ms).
	// Guarded by mu.
	lastNativeActiveAt time.Time

	// nativeStartedAt is when the box last ENTERED the native radio source.
	// lastNativeActiveAt records when it left, which cannot tell how long the
	// station actually lasted; this can. A speaker that drops the station it
	// started a second ago has abandoned it, whoever it hands over to.
	// Guarded by mu.
	nativeStartedAt time.Time

	// upnpEpisode is true while the box sits in the INVALID_SOURCE (or
	// subsequent STANDBY) state it entered FROM STR's UPNP source. The
	// rolling upnpFlapWindow only covers the fast give-up flap; a struggling
	// box can dwell in INVALID_SOURCE for 16+ seconds (the state the
	// sys-power nudge exists for), and a user power press at the end of that
	// dwell produced INVALID_SOURCE->STANDBY with the window long expired -
	// so no standby classification ran, nothing latched, and the recall
	// verify's wake actively powered the just-switched-off box back on
	// (#197). Set on UPNP->INVALID_SOURCE, cleared when the source becomes
	// anything other than INVALID_SOURCE/STANDBY. Guarded by mu.
	upnpEpisode bool

	// lastStandbyFlapAt times the box's own UPNP<->STANDBY oscillation on a
	// spontaneous firmware source power-off (#419). That drop is not a single
	// transition: the box flips UPNP->STANDBY->UPNP within ~100 ms, and the
	// STANDBY->UPNP leg carries a nowPlaying STOP_STATE whose source attribute
	// reads UPNP (not INVALID_SOURCE/STANDBY), so stopStateIsTeardown missed it
	// and fired OnUserStop. That latched a user-stop that then defeated the #419
	// spontaneous-off exemption on the NEXT leg of the same oscillation, so #197
	// tore the transport down and every recovery path stood down until a power
	// pull (bundle 17, three sm2 boxes on v0.9.15). Stamping every flap to OR
	// from STANDBY lets the STOP_STATE handler recognise the bounce. Guarded by mu.
	lastStandbyFlapAt time.Time

	// Thumb-trigger heuristic state. The remote thumbs keys surface only as a
	// generic <userActivityUpdate/>; we treat a "lone" one (no volume / now
	// playing / preset event around it) as a thumb press and fire
	// OnThumbActivity once, debounced. See noteExplainedActivity / noteUserActivity.
	thumbMu        sync.Mutex
	thumbPending   *time.Timer
	thumbExplained time.Time
	thumbLastFire  time.Time
	// lastUserActivityLog debounces the INFO log of an incoming userActivity
	// frame so a volume ramp (which also emits userActivity) cannot churn the
	// NAND log, while an isolated thumb press is still recorded. See
	// noteUserActivity.
	lastUserActivityLog time.Time
	// lastUserActivityAt is when the box last emitted ANY userActivityUpdate
	// frame (box buttons and IR remote keys alike; the firmware sends it as a
	// generic ping alongside the concrete event). The webui's standby handler
	// reads it (via LastUserActivity) to tell a physical power-off, which is
	// accompanied by such a frame, from the firmware spontaneously powering
	// off STR's UPnP source with no user input at all (#419). Guarded by thumbMu.
	lastUserActivityAt time.Time

	// onLoginError fires when the box rejects a source because it considers
	// itself not signed into an account (errorUpdate value 1036
	// UNABLE_TO_PROCESS_NOT_LOGGED_IN, seen on the SoundTouch 300). The agent
	// wires this to a forced re-login plus a signal that stands the recall retry
	// down, so STR does not thrash a box that keeps rejecting the UPnP source
	// (repeated re-pushes flap the source and can wedge the box). Rate-limited
	// via lastLoginErrFire.
	loginErrMu   sync.Mutex
	onLoginError func()
	// onSourcesChanged fires when the box announces a changed source list.
	// Guarded by loginErrMu, like onLoginError.
	onSourcesChanged func()
	// onNativeDropped fires when the box abandons a native radio station it had
	// just accepted. Guarded by loginErrMu.
	onNativeDropped  func()
	lastLoginErrFire time.Time
}

// loginErrDedup rate-limits the not-logged-in callback so a box that emits the
// error repeatedly triggers at most one re-login attempt per window.
const loginErrDedup = 20 * time.Second

// SetOnLoginError registers a callback fired when the box rejects a source with
// a not-logged-in error. Rate-limited internally; the callback runs in its own
// goroutine so the read loop is never blocked.
func (c *Client) SetOnLoginError(fn func()) {
	c.loginErrMu.Lock()
	c.onLoginError = fn
	c.loginErrMu.Unlock()
}

// SetOnSourcesChanged registers a callback fired when the box announces that its
// source list changed (<sourcesUpdated/>).
//
// This is the box telling us the exact moment its registered sources became
// different, and it matters for preset form: whether a preset can be stored as
// a native radio station depends on the radio source being registered, and that
// registration completes a few seconds AFTER the agent's own startup check runs.
// Without this signal the agent keeps a stale "not available" answer until its
// cache expires and writes UPnP presets in the meantime. The callback runs in
// its own goroutine so the read loop is never blocked.
func (c *Client) SetOnSourcesChanged(fn func()) {
	c.loginErrMu.Lock()
	c.onSourcesChanged = fn
	c.loginErrMu.Unlock()
}

// SetOnNativeDropped registers a callback fired when the box leaves a native
// radio station on its own, i.e. it accepted the station and then abandoned it.
// The agent counts these to decide whether this speaker can keep native presets
// at all. Runs in its own goroutine so the read loop is never blocked.
func (c *Client) SetOnNativeDropped(fn func()) {
	c.loginErrMu.Lock()
	c.onNativeDropped = fn
	c.loginErrMu.Unlock()
}

func (c *Client) fireNativeDropped() {
	c.loginErrMu.Lock()
	fn := c.onNativeDropped
	c.loginErrMu.Unlock()
	if fn != nil {
		go fn()
	}
}

// fireSourcesChanged invokes the sources-changed callback, if one is registered.
func (c *Client) fireSourcesChanged() {
	c.loginErrMu.Lock()
	fn := c.onSourcesChanged
	c.loginErrMu.Unlock()
	if fn != nil {
		go fn()
	}
}

// fireLoginError invokes the registered not-logged-in callback, at most once per
// loginErrDedup window.
func (c *Client) fireLoginError() {
	c.loginErrMu.Lock()
	fn := c.onLoginError
	if fn == nil || (!c.lastLoginErrFire.IsZero() && time.Since(c.lastLoginErrFire) < loginErrDedup) {
		c.loginErrMu.Unlock()
		return
	}
	c.lastLoginErrFire = time.Now()
	c.loginErrMu.Unlock()
	go fn()
}

// wsKeepaliveInterval is how often STR sends a WebSocket ping control frame to
// hold the gabbo connection open. The Bose WS server reaps an idle connection
// after ~10 min; without traffic STR reconnected on a stuck ~10.5 min cadence
// and missed the box's preset/now-selection frames in every gap (#183, observed
// 205x over 2.5 days in a diagnostic bundle). A ping every ~4 min keeps the
// socket alive well under that window. STR stays read-only at the gabbo
// application layer: a protocol ping needs no gabbo request semantics and the
// server answers it with a pong.
const wsKeepaliveInterval = 4 * time.Minute

// wsReadDeadline bounds how long the reader waits without ANY life sign (data
// frame or pong). Two keepalive intervals plus slack: two consecutive lost
// pings mean the peer is genuinely gone.
const wsReadDeadline = 2*wsKeepaliveInterval + 3*time.Minute

// wsWriteTimeout bounds a single ping write so a half-dead socket cannot wedge
// the keepalive goroutine; a failed/blocked write closes the conn and the read
// loop returns, triggering a clean reconnect.
const wsWriteTimeout = 10 * time.Second

// LastWifiSignal returns the most recent Wi-Fi signal class seen on the
// gabbo stream, or "" if none observed yet.
func (c *Client) LastWifiSignal() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Expire stale readings: better an honest "not reported" in the UI than
	// a boot-time class presented as current (see lastSignalAt above).
	if c.lastSignal == "" || time.Since(c.lastSignalAt) > wifiSignalTTL {
		return ""
	}
	return c.lastSignal
}

// wifiSignalTTL bounds how long a gabbo-reported signal class counts as
// current. connectionState frames only fire on transitions, so anything
// older describes a long-gone moment (usually the boot association).
const wifiSignalTTL = 15 * time.Minute

// languageRevertWindow is how close together two languageUpdated frames with
// different values must be to count as a firmware-side revert rather than two
// independent user changes. The live Wave captures show 38-183 ms; 2 s leaves
// margin without misreading a user changing their mind.
const languageRevertWindow = 2 * time.Second

// noteLanguageUpdated logs sysLanguage transitions and flags the Wave revert
// pattern: a fresh value overwritten by a different one within
// languageRevertWindow. The WARN carries both values and the gap so a bundle
// can be correlated (by millisecond timestamp) with the marge request trail in
// debug/state and with any app-side save, answering WHO wrote the second value.
func (c *Client) noteLanguageUpdated(v string) {
	now := time.Now()
	c.mu.Lock()
	prev, prevAt := c.lastLang, c.lastLangAt
	c.lastLang, c.lastLangAt = v, now
	c.mu.Unlock()
	if prev != "" && prev != v && now.Sub(prevAt) < languageRevertWindow {
		c.logger.Warn("box ws: sysLanguage overwritten right after a change (firmware-side revert; correlate with marge_recent_requests and any app save at this timestamp)",
			"from", prev, "to", v, "afterMs", now.Sub(prevAt).Milliseconds())
		return
	}
	c.logger.Info("box ws: languageUpdated", "sysLanguage", v, "prev", prev)
}

// storm1036Threshold / storm1036Window / storm1036LogEvery bound the 1036-storm
// marker: at least storm1036Threshold rejections inside storm1036Window, logged
// at most once per storm1036LogEvery so a day-long storm (observed on a mojo
// ST30: every press 1036, all day) does not churn the NAND log.
const (
	storm1036Threshold = 6
	storm1036Window    = 10 * time.Minute
	storm1036LogEvery  = 30 * time.Minute
)

// note1036 feeds the storm detector. The WARN is diagnostic only (no behavior
// hangs off it): it timestamps the storm START so bundles can correlate it
// with the boot clock state (plug-pull RTC loss poisons the firmware, #419
// Finding 4), TLS handshake failures on marge-tls, and the marge trail.
func (c *Client) note1036() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	keep := c.err1036Times[:0]
	for _, t := range c.err1036Times {
		if now.Sub(t) < storm1036Window {
			keep = append(keep, t)
		}
	}
	c.err1036Times = append(keep, now)
	if len(c.err1036Times) < storm1036Threshold || now.Sub(c.lastStormLogAt) < storm1036LogEvery {
		return
	}
	c.lastStormLogAt = now
	c.logger.Warn("box ws: 1036 storm - the box rejects essentially every recall (check clock_status and marge-tls handshake errors in this bundle; a soft reboot has cleared this state in the field)",
		"count", len(c.err1036Times), "windowMin", int(storm1036Window.Minutes()))
}

// Storm1036 reports whether the box is currently rejecting essentially every
// recall, and since when. Same window and threshold as the log marker above.
//
// Until now the storm was diagnostic only, visible in a bundle after the fact.
// But it is the one state where the user's own remedy is actively harmful: the
// advice that spreads between users is to pull the plug, and a plug pull resets
// the box's clock to 2015 and poisons the whole next boot, while a SOFT reboot
// clears the state (#419 Finding 4, reproduced twice on-site). Reporting it lets
// the app say so at the moment it matters instead of after the damage.
//
// The "since" is the OLDEST rejection still inside the window, i.e. how long the
// box has been in this state as far as we can still see.
func (c *Client) Storm1036() (active bool, count int, since time.Time) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	var oldest time.Time
	n := 0
	for _, t := range c.err1036Times {
		if now.Sub(t) >= storm1036Window {
			continue
		}
		n++
		if oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
	}
	return n >= storm1036Threshold, n, oldest
}

// LastUserActivity returns when the box last reported a userActivityUpdate
// frame (any physical key on the box or the IR remote), or the zero time if
// none has been seen since the agent started. The webui's standby handler uses
// it to tell a user power-off from the firmware spontaneously powering off
// STR's UPnP source (#419): a real key press is accompanied by such a frame,
// a spontaneous drop is not.
func (c *Client) LastUserActivity() time.Time {
	c.thumbMu.Lock()
	defer c.thumbMu.Unlock()
	return c.lastUserActivityAt
}

// New creates a Client. url example: "ws://127.0.0.1:8080/".
func New(logger *slog.Logger, url string, handler Handler) *Client {
	return &Client{logger: logger, url: url, handler: handler}
}

// Run blocks and reconnects automatically when the connection drops. Stop via
// ctx cancel.
//
// The box does not send its own keepalive frames; STR pings the socket itself
// (wsKeepaliveInterval) so a long idle period no longer tears the connection
// down. A reconnect resyncs the box state via the OnConnected hook. When
// nothing happens for a long time that is normal - no WARN spam for it.
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		// A session that lived a while was a healthy connection: reset the
		// backoff so the NEXT reattach is fast again. Without this the backoff
		// initialized once, hit its 8 s cap during the first outage and stayed
		// there for the whole agent lifetime, adding a flat 8 s to every
		// reconnect forever (part of the fixed 674.5 s self-timeout cadence in
		// the 2026-07-26 field bundle).
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		if err != nil {
			// A read timeout is normal when the box is not active; the
			// reconnect runs cleanly. Other errors are interesting though.
			if strings.Contains(err.Error(), "i/o timeout") {
				c.logger.Debug("box websocket idle reconnect", "retry_in", backoff)
				c.prevEndedIdle = true
			} else {
				c.logger.Warn("box websocket connection lost", "err", err, "retry_in", backoff)
				c.prevEndedIdle = false
			}
		} else {
			c.prevEndedIdle = false
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		// Cap kept low (8s, not 30s) so STR reattaches quickly after the box
		// wakes from a deep/overnight standby. The lost first press after such a
		// standby (#183) is recovered by the OnConnected hook below, but a short
		// reconnect window shrinks how long the box shows "service unavailable"
		// before STR takes over.
		if backoff < 8*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) runOnce(ctx context.Context) error {
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{"gabbo"}
	dialer.HandshakeTimeout = 10 * time.Second

	conn, _, err := dialer.DialContext(ctx, c.url, http.Header{})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Phase marker at WARN so a reconnect after standby/resume is visible in
	// the diagnostic bundle without raising log level. A reconnect after a
	// plain idle self-timeout logs at Debug instead: that churn (a full WARN +
	// re-sync block every ~11 min) rotated the 32 KB NAND log in ~3.5 h and
	// destroyed the actual failure evidence in the 2026-07-26 field bundle.
	if c.prevEndedIdle {
		c.logger.Debug("box websocket phase: connected (after idle reconnect)", "url", c.url)
	} else {
		c.logger.Warn("box websocket phase: connected", "url", c.url)
	}

	// After a deep/overnight standby the box wakes and emits its first
	// preset/now-selection frame BEFORE this reconnect lands (the backoff had
	// grown while the box was unreachable), so that first hardware press is lost
	// and nothing plays until a second press (#183). Give the handler a chance to
	// recover a stuck wake on every (re)connect. Optional interface so handlers
	// that do not need it (tests) are unaffected; run in a goroutine so the probe
	// never blocks the reader loop.
	if oc, ok := c.handler.(interface{ OnConnected(context.Context) }); ok {
		go oc.OnConnected(ctx)
	}

	// Application-level keepalive: the box never sends keepalive frames and its
	// WS server drops an idle connection after ~10 min, which forced a stuck
	// ~10.5 min reconnect cadence that lost preset frames in every gap (#183).
	// Ping it every wsKeepaliveInterval so the socket stays alive. WriteControl
	// is the only writer and is safe to call concurrently with the reader. The
	// goroutine exits when this runOnce returns (conn.Close unblocks the read
	// and closeKeepalive is closed) or when ctx is cancelled.
	// The box answers our keepalive pings with pongs, but gorilla/websocket
	// consumes pongs INSIDE ReadMessage without returning from it: without a
	// pong handler the per-iteration read deadline below was never refreshed
	// while the reader blocked on an idle socket, so the client tore down its
	// own healthy connection like clockwork (live: 18 reconnects at
	// 674.5 s +-0.2 s in the 2026-07-26 ST10 bundle = 660 s deadline + capped
	// backoff + re-sync tail). The June keepalive fix only moved the old
	// ~10.5 min firmware-drop cadence to this ~11.2 min self-timeout. With
	// each pong pushing the deadline, it now fires only on a genuinely dead
	// peer.
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsReadDeadline))
	})

	closeKeepalive := make(chan struct{})
	defer close(closeKeepalive)
	go func() {
		t := time.NewTicker(wsKeepaliveInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-closeKeepalive:
				return
			case <-t.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteTimeout)); err != nil {
					// A failed ping means the socket is gone; close it so the
					// reader returns and Run reconnects. Debug, not Warn: an idle
					// reconnect is routine and must not spam the NAND log.
					c.logger.Debug("box websocket keepalive ping failed, reconnecting", "err", err)
					_ = conn.Close()
					return
				}
				c.logger.Debug("box websocket keepalive ping sent")
			}
		}
	}()

	// Reader Loop
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Read deadline sits above two keepalive intervals; the pong handler
		// above refreshes it on every keepalive answer, so it only fires on a
		// genuinely dead peer (no pong, no data). Reconnect stays clean:
		// OnConnected resyncs box state on every reconnect.
		_ = conn.SetReadDeadline(time.Now().Add(wsReadDeadline))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		if mt != websocket.TextMessage && mt != websocket.BinaryMessage {
			continue
		}
		c.handleMessage(ctx, data)
	}
}
