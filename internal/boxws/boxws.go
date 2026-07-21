// Package boxws connects to the Bose WebSocket notification stream on port
// 8080 (subprotocol "gabbo") and reacts to incoming events.
//
// When a user presses a physical preset button on the box, the BoseApp sends
// an `<updates>` message over this WebSocket with `presetSelectionUpdated` or
// `nowPlayingUpdated`. We hook the event and trigger our UPnP player with the
// associated stream URL.
//
// This is what makes the hardware preset buttons work even though Bose's own
// music services are disabled in the firmware.
package boxws

import (
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// PresetEvent is fired when the box reports that a preset slot was
// selected.
type PresetEvent struct {
	Slot int
}

// Handler receives incoming events from the box WebSocket.
type Handler interface {
	// OnPresetSelected is fired when the box reports that a preset slot was
	// actively selected (physical hardware button or API trigger). location
	// and title come from the box ContentItem and can be sent to the box over
	// UPnP.
	OnPresetSelected(ctx context.Context, slot int, location string, title string)

	// OnRemoteSkip is fired when the remote presses next/previous track (the
	// box cannot skip a UPnP source itself and reports QPLAY_SKIP_*_FAILED).
	// forward=true -> next, false -> prev.
	OnRemoteSkip(ctx context.Context, forward bool)

	// OnUserStop is fired when the box reports that playback was stopped
	// (playStatus STOP_STATE in a nowPlayingUpdated event), i.e. the user
	// deliberately stopped it via remote/box. The agent uses this to avoid
	// running the auto-resume against a deliberate stop.
	OnUserStop(ctx context.Context)

	// OnThumbActivity is fired when the box reports a "bare"
	// userActivityUpdate: a key press without an accompanying
	// volume/nowPlaying/preset event. On this firmware the remote thumb keys
	// only deliver this generic event with no up/down identity; a bare
	// userActivity is the best available approximation for a thumb press. The
	// agent uses it as a (single, non up/down-distinguishable) trigger for a
	// configured webhook. Debounced and filtered against volume/preset in
	// boxws; still heuristic, hence live tunable.
	OnThumbActivity(ctx context.Context)

	// OnPowerKey is fired on a powerStateUpdated (power button / standby
	// change). For the optional "power" webhook (additive only: STR cannot
	// suppress the firmware-side power on/off). Beta.
	OnPowerKey(ctx context.Context)

	// OnSourceAux is fired when the active source switches to AUX. For the
	// optional "aux" webhook (additive only; the firmware switches the input
	// anyway). Detected heuristically via the source change, hence beta.
	OnSourceAux(ctx context.Context)

	// OnZoneChanged is fired when the box changes its multiroom zone or its
	// stereo pair (zoneUpdated). This lets STR also know about groups that
	// were NOT formed in STR (e.g. a stereo pair defined in AfterTouch/Bose),
	// instead of discarding the frame as "unrecognized". z.Master == "" means
	// the zone was dissolved.
	OnZoneChanged(ctx context.Context, z ZoneState)

	// OnPowerWake is fired when the box comes out of standby: either via a
	// powerStateUpdated (NOT STANDBY) on firmware that sends it, OR, on
	// SoundTouch firmware that does NOT send a powerStateUpdated (Portable/
	// taigan, confirmed live 2026-06-13), via the DO_NOT_RESUME restore of the
	// last selection on wake. Driver for the optional "resume the last station
	// on power-on" default. A self-wake (stereo pair/zone) looks identical and
	// is caught downstream via the zone membership (webui.boxInZone), not here.
	OnPowerWake(ctx context.Context)

	// OnPresetsChanged fires when the box reports its own preset list
	// (presetsUpdated). It delivers ALL of the box's presets, including foreign
	// sources (DEEZER, LOCAL_INTERNET_RADIO, ...) that STR did not set. This lets
	// STR show and recall the box's existing presets (the box plays e.g. a Deezer
	// preset through its own cached account) instead of just logging the slot IDs.
	OnPresetsChanged(ctx context.Context, presets []BoxPreset)
}

// BoxPreset is one preset reported by the box's own presetsUpdated frame,
// including its source. STR uses it to detect, show and keep foreign presets
// (ones STR did not set), such as Deezer.
type BoxPreset struct {
	Slot          int    `json:"slot"`          // 1..6
	Source        string `json:"source"`        // DEEZER / LOCAL_INTERNET_RADIO / SPOTIFY / UPNP / ...
	Type          string `json:"type"`          // playlist / stationurl / tracklistRadio / ...
	Location      string `json:"location"`      // stream URL, Deezer playlist ID, ...
	SourceAccount string `json:"sourceAccount"` // linked account (e.g. Deezer account)
	Name          string `json:"name"`          // itemName
}

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
	// lastSource tracks the most recent active source seen on a now-selection /
	// now-playing frame, so the aux webhook fires once on the transition to AUX
	// rather than repeatedly while AUX stays the active source.
	lastSource string

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
	loginErrMu       sync.Mutex
	onLoginError     func()
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

// thumbExplainWindow is how close an explained event (volume/preset/now
// playing) must be to a userActivity for that activity to count as "explained"
// (i.e. NOT a thumb). thumbSettle is how long we wait after a lone userActivity
// before firing, to let any sibling event arrive and cancel it.
const (
	thumbExplainWindow = 600 * time.Millisecond
	thumbSettle        = 500 * time.Millisecond
	thumbDebounce      = 2 * time.Second
	// userActivityLogDedup is the minimum gap between INFO log lines for an
	// incoming userActivity frame. A thumb press emits a single frame, so an
	// isolated press is always logged; a volume ramp emits many, which collapse
	// to one line per window so the NAND log does not churn (#187).
	userActivityLogDedup = 3 * time.Second
)

// wsKeepaliveInterval is how often STR sends a WebSocket ping control frame to
// hold the gabbo connection open. The Bose WS server reaps an idle connection
// after ~10 min; without traffic STR reconnected on a stuck ~10.5 min cadence
// and missed the box's preset/now-selection frames in every gap (#183, observed
// 205x over 2.5 days in a diagnostic bundle). A ping every ~4 min keeps the
// socket alive well under that window. STR stays read-only at the gabbo
// application layer: a protocol ping needs no gabbo request semantics and the
// server answers it with a pong.
const wsKeepaliveInterval = 4 * time.Minute

// wsWriteTimeout bounds a single ping write so a half-dead socket cannot wedge
// the keepalive goroutine; a failed/blocked write closes the conn and the read
// loop returns, triggering a clean reconnect.
const wsWriteTimeout = 10 * time.Second

// noteExplainedActivity records that a concrete, identifiable action just
// happened (volume change, preset/now-selection, now-playing change, power).
// It cancels any pending thumb fire, because that activity explains the
// userActivity ping and it is therefore not a thumb press.
func (c *Client) noteExplainedActivity() {
	c.thumbMu.Lock()
	c.thumbExplained = time.Now()
	if c.thumbPending != nil {
		c.thumbPending.Stop()
		c.thumbPending = nil
	}
	c.thumbMu.Unlock()
}

// noteUserActivity handles a userActivityUpdate frame. If no explained event
// happened just before it, it arms a short settle timer; if no explained event
// arrives during the settle window either, it fires OnThumbActivity once
// (debounced). Both the before- and after-cases are covered, so a volume key
// (which emits volumeUpdated alongside userActivity, in either order) does not
// misfire.
func (c *Client) noteUserActivity(ctx context.Context, raw []byte) {
	c.thumbMu.Lock()
	defer c.thumbMu.Unlock()
	// Every userActivityUpdate is a physical key press (box or IR remote),
	// whether or not it is later explained by a sibling event. Stamp it for
	// LastUserActivity's spontaneous-power-off discriminator (#419).
	c.lastUserActivityAt = time.Now()
	// Record that a userActivity frame arrived at INFO (deduped), independent of
	// whether the heuristic ends up firing. A "the thumb key does nothing" report
	// (#187) is otherwise undiagnosable from a bundle: we cannot tell a frame that
	// never arrived (box sends nothing for thumbs on this firmware) from one that
	// arrived and was suppressed. The raw frame is also captured so we can see
	// whether it carries any attribute that distinguishes thumb-up from -down.
	if time.Since(c.lastUserActivityLog) > userActivityLogDedup {
		c.lastUserActivityLog = time.Now()
		c.logger.Info("box ws: user-activity frame received", "frame", preview(raw, 400))
	}
	if time.Since(c.thumbExplained) < thumbExplainWindow {
		return // explained by a recent volume/preset/now-playing event
	}
	if c.thumbPending != nil {
		return // already waiting to fire
	}
	framePrev := preview(raw, 400) // captured for the fire log below
	c.thumbPending = time.AfterFunc(thumbSettle, func() {
		c.thumbMu.Lock()
		c.thumbPending = nil
		explained := time.Since(c.thumbExplained) < thumbExplainWindow
		debounced := !c.thumbLastFire.IsZero() && time.Since(c.thumbLastFire) < thumbDebounce
		if explained || debounced {
			c.thumbMu.Unlock()
			// A lone user-activity reached the settle timer but was then
			// suppressed. Both outcomes are otherwise invisible, which makes a
			// "the thumb key does nothing" report (#187) impossible to diagnose
			// from a bundle: we cannot tell a missing frame from a suppressed
			// one. Log it at INFO. This path only runs for activity that was NOT
			// already explained at arrival (volume ramps return earlier), so it
			// stays rare and does not churn the NAND log.
			switch {
			case explained:
				c.logger.Info("box ws: user-activity settled as explained, not firing thumb")
			default:
				c.logger.Info("box ws: user-activity debounced, thumb already fired recently")
			}
			return
		}
		c.thumbLastFire = time.Now()
		c.thumbMu.Unlock()
		c.logger.Info("box ws: lone user-activity -> thumb trigger", "frame", framePrev)
		if c.handler != nil {
			c.handler.OnThumbActivity(ctx)
		}
	})
}

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

// attrValue pulls attr="VALUE" out of a raw XML fragment, or "".
func attrValue(s, attr string) string {
	key := attr + `="`
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	r := s[i+len(key):]
	if j := strings.IndexByte(r, '"'); j >= 0 {
		return r[:j]
	}
	return ""
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
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// A read timeout is normal when the box is not active; the
			// reconnect runs cleanly. Other errors are interesting though.
			if strings.Contains(err.Error(), "i/o timeout") {
				c.logger.Debug("box websocket idle reconnect", "retry_in", backoff)
			} else {
				c.logger.Warn("box websocket connection lost", "err", err, "retry_in", backoff)
			}
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

	// Phase marker at WARN so a reconnect after standby/resume is
	// visible in the diagnostic bundle without raising log level.
	c.logger.Warn("box websocket phase: connected", "url", c.url)

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
		// Read deadline sits above two keepalive intervals (wsKeepaliveInterval):
		// a healthy idle socket is held open by our ping, so the only thing this
		// deadline should now fire on is a genuinely dead peer (no pong, no data).
		// Reconnect stays clean: OnConnected resyncs box state on every reconnect.
		_ = conn.SetReadDeadline(time.Now().Add(2*wsKeepaliveInterval + 3*time.Minute))
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

// handleMessage parses an incoming XML notification.
//
// Bose's WebSocket format for hardware preset buttons (measured 2026-05-15):
//
//	<updates deviceID="...">
//	  <nowSelectionUpdated>
//	    <preset id="1" ... >
//	      <ContentItem source="UPNP" location="http://..." sourceAccount="..." isPresetable="true">
//	        <itemName>NDR Info</itemName>
//	      </ContentItem>
//	    </preset>
//	  </nowSelectionUpdated>
//	</updates>
//
// The box follows up with `<nowSelectionUpdated><preset id="0">` and
// INVALID_SOURCE when it cannot activate the source. We only care about the
// first event with id >= 1.
// wsContentItem is the <ContentItem> Bose nests inside a preset or nowPlaying.
type wsContentItem struct {
	Source        string `xml:"source,attr"`
	Type          string `xml:"type,attr"`
	Location      string `xml:"location,attr"`
	SourceAccount string `xml:"sourceAccount,attr"`
	IsPresetable  string `xml:"isPresetable,attr"`
	ItemName      string `xml:"itemName"`
}

// wsPreset is a <preset> element (nowSelectionUpdated / presetSelectionUpdated /
// presetsUpdated). Inner keeps the raw element body so a status marker like
// INVALID_SOURCE / DO_NOT_RESUME can be matched within this element only, never
// against an unrelated frame's track title.
type wsPreset struct {
	ID          string        `xml:"id,attr"`
	ContentItem wsContentItem `xml:"ContentItem"`
	Inner       string        `xml:",innerxml"`
}

// wsNowPlaying is the <nowPlaying> body. Bose has shipped playStatus both as a
// child element and as an attribute across firmware builds, so capture both and
// resolve with playStatus(); reading it typed is what stops a track title that
// merely contains "STOP_STATE" from firing the user-stop suppressor.
type wsNowPlaying struct {
	Source         string        `xml:"source,attr"`
	PlayStatusEl   string        `xml:"playStatus"`
	PlayStatusAttr string        `xml:"playStatus,attr"`
	ContentItem    wsContentItem `xml:"ContentItem"`
}

func (n wsNowPlaying) playStatus() string {
	if n.PlayStatusEl != "" {
		return n.PlayStatusEl
	}
	return n.PlayStatusAttr
}

// gabboFrame is the typed view of one <updates> notification. Bose sends one
// update child per frame; the rest stay nil. Dispatching on which child is
// present (and reading status markers from typed sub-fields) replaces the old
// whole-frame strings.Contains sniffing, which mis-fired whenever a station
// name or track title happened to contain a marker word such as STOP_STATE,
// INVALID_SOURCE, or even an update element name.
type gabboFrame struct {
	XMLName        xml.Name      `xml:"updates"`
	NowSelection   *wsPreset     `xml:"nowSelectionUpdated>preset"`
	PresetSelected *wsPreset     `xml:"presetSelectionUpdated>preset"`
	NowPlaying     *wsNowPlaying `xml:"nowPlayingUpdated>nowPlaying"`

	// Presence-plus-body markers. The *struct with an Inner field is non-nil
	// exactly when the child element is in the frame; Inner bounds any substring
	// match (e.g. STANDBY) to that element's own body.
	ConnectionState *struct {
		Inner string `xml:",innerxml"`
	} `xml:"connectionStateUpdated"`
	PowerState *struct {
		Inner string `xml:",innerxml"`
	} `xml:"powerStateUpdated"`
	VolumeUpdated *struct{} `xml:"volumeUpdated"`
	UserActivity  *struct{} `xml:"userActivityUpdate"`

	// PresetsUpdated carries the box's full preset list when it changes; the
	// landing spot for preset sync from the box (#14).
	PresetsUpdated *struct {
		Presets []wsPreset `xml:"presets>preset"`
	} `xml:"presetsUpdated"`

	// ZoneUpdated carries the box's multiroom zone / stereo-pair membership when
	// it changes. Non-nil whenever a <zoneUpdated><zone> is present; an empty
	// <zone/> (Master == "") means the zone dissolved (#70, Klaus 2026-06-12).
	ZoneUpdated *wsZone `xml:"zoneUpdated>zone"`
}

// wsZone is the <zone> body of a zoneUpdated frame. Bose puts the master's
// deviceID in the master attr, its LAN IP in senderIPAddress, whether THIS box
// leads in senderIsMaster, and one <member ipaddress="..">deviceID</member> per
// follower.
type wsZone struct {
	Master         string         `xml:"master,attr"`
	SenderIP       string         `xml:"senderIPAddress,attr"`
	SenderIsMaster string         `xml:"senderIsMaster,attr"`
	Members        []wsZoneMember `xml:"member"`
}

type wsZoneMember struct {
	DeviceID string `xml:",chardata"`
	IP       string `xml:"ipaddress,attr"`
	Role     string `xml:"role,attr"`
}

func (z *wsZone) toState() ZoneState {
	st := ZoneState{
		Master:         strings.TrimSpace(z.Master),
		SenderIP:       strings.TrimSpace(z.SenderIP),
		SenderIsMaster: strings.EqualFold(strings.TrimSpace(z.SenderIsMaster), "true"),
	}
	for _, m := range z.Members {
		st.Members = append(st.Members, ZoneMemberState{
			DeviceID: strings.TrimSpace(m.DeviceID),
			IP:       strings.TrimSpace(m.IP),
			Role:     strings.TrimSpace(m.Role),
		})
	}
	return st
}

// ZoneState is the typed multiroom/stereo-pair membership delivered to the
// handler on a zoneUpdated frame. Master == "" means the zone dissolved.
type ZoneState struct {
	Master         string
	SenderIP       string
	SenderIsMaster bool
	Members        []ZoneMemberState
}

// ZoneMemberState is one follower in a ZoneState.
type ZoneMemberState struct {
	DeviceID string
	IP       string
	Role     string
}

// presetTeardownWindow / invalidSourceTeardownWindow bound how soon after a
// hardware preset press or an INVALID_SOURCE flap a STOP_STATE still counts as
// the box's own teardown rather than a deliberate user stop. Kept short: the
// teardown STOP_STATE arrives within a fraction of a second of the flap/press on
// the observed scm/mojo firmware, while a real stop the user makes seconds later
// is well outside these windows and still honoured.
const (
	presetTeardownWindow        = 4 * time.Second
	invalidSourceTeardownWindow = 2 * time.Second
	// standbyFlapTeardownWindow bounds how soon after a UPNP<->STANDBY flap a
	// STOP_STATE still counts as the spontaneous-off oscillation's teardown
	// (#419) rather than a deliberate stop. The observed bounce completes in
	// ~100-150 ms; 3 s covers a slow flap without swallowing a real stop the user
	// makes seconds after a genuine power event.
	standbyFlapTeardownWindow = 3 * time.Second
)

// stopStateIsTeardown reports whether a STOP_STATE nowPlaying frame is the box's
// own UPnP-source teardown (a preset switch or an involuntary stream drop, both
// of which flap the source through INVALID_SOURCE) rather than a deliberate user
// stop. Only a genuine stop must fire OnUserStop; a teardown must not, or the
// latched user-stop suppresses the drop recovery and the recall retry and the
// preset buttons look dead (#ST30 2026-07-11). Returns the reason for the log.
func (c *Client) stopStateIsTeardown(np *wsNowPlaying) (bool, string) {
	// The frame itself admits the box could not hold STR's source: either the
	// nowPlaying source attribute or its nested ContentItem reads INVALID_SOURCE
	// (the failed self-activation) or STANDBY (a power-off teardown, already
	// covered elsewhere but never a "user stopped the stream").
	if np != nil {
		for _, src := range []string{np.Source, np.ContentItem.Source} {
			if src == "INVALID_SOURCE" || src == "STANDBY" {
				return true, "nowPlaying source=" + src
			}
		}
	}
	c.mu.Lock()
	sincePress := time.Since(c.lastPresetPressAt)
	sinceInvalid := time.Since(c.lastInvalidSourceAt)
	sinceStandbyFlap := time.Since(c.lastStandbyFlapAt)
	pressSet := !c.lastPresetPressAt.IsZero()
	invalidSet := !c.lastInvalidSourceAt.IsZero()
	standbyFlapSet := !c.lastStandbyFlapAt.IsZero()
	c.mu.Unlock()
	if pressSet && sincePress < presetTeardownWindow {
		return true, "hardware preset pressed " + sincePress.Round(time.Millisecond).String() + " ago"
	}
	if invalidSet && sinceInvalid < invalidSourceTeardownWindow {
		return true, "source flapped to INVALID_SOURCE " + sinceInvalid.Round(time.Millisecond).String() + " ago"
	}
	// The spontaneous-off oscillation (#419) flips UPNP->STANDBY->UPNP and emits a
	// STOP_STATE on the way back up whose source reads UPNP, so the two checks
	// above miss it. A STOP_STATE within a moment of a STANDBY flap is that
	// bounce, not a deliberate stop: reading it as a user stop re-latched
	// lastUserStop and defeated the #419 spontaneous-off exemption on the next leg
	// of the same oscillation, so #197 cleared the transport and the box went
	// silent until a power pull (bundle 17, three sm2 boxes on v0.9.15). A real
	// power press is unaffected: on this firmware it emits a userActivityUpdate,
	// so HandleEnterStandby still classifies and latches it via its own path.
	if standbyFlapSet && sinceStandbyFlap < standbyFlapTeardownWindow {
		return true, "source flapped through STANDBY " + sinceStandbyFlap.Round(time.Millisecond).String() + " ago"
	}
	return false, ""
}

func (c *Client) handleMessage(ctx context.Context, data []byte) {
	c.logger.Debug("box ws frame", "bytes", len(data), "preview", preview(data, 400))

	s := string(data)

	// Remote next/prev keys: the box cannot skip a UPnP source itself, so it
	// emits a QPLAY_SKIP_*_FAILED error. These are firmware error codes that
	// never appear in user-supplied text, so a whole-frame match is safe and
	// they short-circuit before the typed parse.
	switch {
	case strings.Contains(s, "QPLAY_SKIP_NEXT_FAILED"):
		if c.handler != nil {
			c.handler.OnRemoteSkip(ctx, true)
		}
		return
	case strings.Contains(s, "QPLAY_SKIP_PREV_FAILED"):
		if c.handler != nil {
			c.handler.OnRemoteSkip(ctx, false)
		}
		return
	}

	var f gabboFrame
	if err := xml.Unmarshal(data, &f); err != nil {
		// Not parseable as an <updates> envelope. Still try the source/AUX
		// attribute scan below (it is attribute-only and cannot be fooled by
		// text), then log it as unrecognized.
		c.logger.Debug("box ws xml parse error", "err", err)
	}

	// AUX webhook (beta): fire once when the active source transitions to AUX.
	// STR never selects AUX itself, so this is always a user press (front panel
	// or remote; app recalls use a different path). source is read as a raw
	// attribute (source="..."), which only ever appears in markup, never in
	// escaped text content, so this scan is not subject to the title
	// false-positive that the typed parse fixes elsewhere. Tracking lastSource
	// means it fires on the change, not on every AUX frame.
	if c.handler != nil {
		if src := attrValue(s, "source"); src != "" {
			c.mu.Lock()
			changed := src != c.lastSource
			prev := c.lastSource
			c.lastSource = src
			// Stamp every flap to INVALID_SOURCE (the box's failed self-activation
			// of STR's UPNP source): a STOP_STATE within a moment of it is that
			// teardown, not a user stop. See stopStateIsTeardown.
			if src == "INVALID_SOURCE" {
				c.lastInvalidSourceAt = time.Now()
			}
			// Stamp every flap to OR from STANDBY: the spontaneous-off oscillation
			// (#419) carries a STOP_STATE on its STANDBY->UPNP leg whose source reads
			// UPNP, so this is the only trace that the STOP_STATE belongs to the
			// bounce and is not a deliberate user stop. See stopStateIsTeardown.
			if src == "STANDBY" || prev == "STANDBY" {
				c.lastStandbyFlapAt = time.Now()
			}
			c.mu.Unlock()
			if changed {
				// Log every source transition at INFO (rare by construction: only
				// on change). This is how we learn the exact label the firmware
				// uses for external inputs like AirPlay on each model (#122), so a
				// diagnostic bundle pins down what the box actually reports when
				// the app cannot tell it is playing.
				c.logger.Info("box ws: source changed", "from", prev, "to", src)
				if src == "AUX" {
					c.handler.OnSourceAux(ctx)
				}
				// #197: some ST20 (scm) firmware oscillates UPNP->STANDBY->UPNP on a
				// power-off, re-selecting STR's UPnP source so the speaker switches
				// itself back on. When STR's own source (UPNP) drops to STANDBY, give
				// the handler a chance to clear the transport so the box has nothing to
				// bounce back to. Optional interface so handlers that do not need it
				// (tests) are unaffected. Gated to prev==UPNP so it only fires for
				// STR-driven playback, never an AUX/Spotify power-off.
				if src == "STANDBY" && prev == "UPNP" {
					if h, ok := c.handler.(interface{ OnEnterStandby(context.Context) }); ok {
						h.OnEnterStandby(ctx)
					}
				}
			}
		}
	}

	// known tracks whether this frame matched any gabbo type STR understands.
	// Frames that match nothing are logged in full at INFO at the end so the
	// genuinely new, user-initiated events we are still mapping out (the preset
	// long-press "store" gesture and the remote's thumbs keys) can be identified
	// from a real box. These are rare, so logging them fully does not churn the
	// NAND log the way the periodic connectionState/nowPlaying frames would.
	// connectionState and nowPlaying fire every few seconds on some boxes (the
	// Portable flaps GOOD_SIGNAL<->EXCELLENT_SIGNAL constantly), so they stay at
	// DEBUG; powerState transitions are rare and useful and stay at INFO.
	known := false
	switch {
	case f.PowerState != nil:
		known = true
		c.noteExplainedActivity()
		standby := strings.Contains(f.PowerState.Inner, "STANDBY")
		// INFO with the resolved direction: a real power press surfaces here, while
		// a self-wake (zone / stereo pair) surfaces instead as the DO_NOT_RESUME
		// now-selection restore below. This split is the discriminator the power-on
		// resume relies on, so keep it visible in bundles for the hardware check.
		c.logger.Info("box ws phase: powerState event", "standby", standby, "preview", preview(data, 200))
		if c.handler != nil {
			if standby {
				// power webhook (beta): fire only on the transition to standby (power
				// off). STR never powers the box off itself (it only wakes it for a
				// recall), so a standby event is always a user press; this avoids the
				// webhook false-firing on STR's own wake. The STANDBY match is bounded
				// to the powerState element body. Rate-limited per id downstream.
				c.handler.OnPowerKey(ctx)
			} else {
				// A real power-ON: the box left standby. A self-wake does NOT arrive as
				// a powerState (it comes as the DO_NOT_RESUME restore -> OnSelfWake), so
				// this is the verified user-wake the optional power-on resume binds to.
				// The resume is gated by a per-box setting AND suppressed if a
				// DO_NOT_RESUME was seen in the same window, so it can never resume a
				// self-wake.
				c.handler.OnPowerWake(ctx)
			}
		}
	case f.ConnectionState != nil:
		known = true
		c.logger.Debug("box ws phase: connectionState event", "preview", preview(data, 200))
		// Capture the Wi-Fi signal class; on BCO boxes this is the only place it
		// is reported (/networkInfo has no signal there). Attribute-only scan.
		if sig := attrValue(s, "signal"); sig != "" {
			c.mu.Lock()
			c.lastSignal = sig
			c.lastSignalAt = time.Now()
			c.mu.Unlock()
		}
	case f.VolumeUpdated != nil:
		// A volume change is identifiable activity: the box emits a
		// userActivityUpdate alongside it, so mark this as "explained" and the
		// thumb heuristic will not treat that ping as a thumb press.
		known = true
		c.noteExplainedActivity()
	case f.UserActivity != nil:
		// The remote thumbs keys surface ONLY as this generic ping (no up/down
		// identity). Treat a lone one as a thumb press; noteUserActivity
		// debounces it and suppresses it when an explained event bracketed it.
		known = true
		c.noteUserActivity(ctx, data)
	case f.NowPlaying != nil && f.NowSelection == nil:
		known = true
		c.noteExplainedActivity()
		c.logger.Debug("box ws phase: nowPlaying event", "preview", preview(data, 200))
		// STOP_STATE in a nowPlaying update is the box reporting that playback
		// was stopped (the user pressed stop on the remote/box). Read from the
		// typed playStatus, so a track title containing "STOP_STATE" can no
		// longer trip it. INFO, not DEBUG: stops are rare and this is the signal
		// the re-push decision hinges on, so it must be visible in a bundle.
		if f.NowPlaying.playStatus() == "STOP_STATE" && c.handler != nil {
			if teardown, why := c.stopStateIsTeardown(f.NowPlaying); teardown {
				// Not a user stop: the box tore its own UPNP source down (a preset
				// switch or an involuntary stream drop). Firing OnUserStop here
				// latched a phantom user-stop that killed the drop recovery and the
				// re-press retry, so the buttons looked dead (#ST30 2026-07-11).
				c.logger.Info("box ws: STOP_STATE during a source teardown, not a user stop (recovery stays armed)", "reason", why)
			} else {
				c.logger.Info("box ws: playback stopped (STOP_STATE), treating as user stop")
				c.handler.OnUserStop(ctx)
			}
		}
	case f.PresetsUpdated != nil:
		// The box reported its own preset list (#14). Surface the full set incl.
		// foreign sources (DEEZER etc.) so STR can show/preserve/recall them
		// (Option C: the box plays a Deezer preset via its own cached account).
		known = true
		bps := make([]BoxPreset, 0, len(f.PresetsUpdated.Presets))
		slots := make([]string, 0, len(f.PresetsUpdated.Presets))
		for _, p := range f.PresetsUpdated.Presets {
			slots = append(slots, p.ID)
			slot, err := strconv.Atoi(strings.TrimSpace(p.ID))
			if err != nil || slot < 1 || slot > 6 {
				continue
			}
			ci := p.ContentItem
			bps = append(bps, BoxPreset{
				Slot: slot, Source: ci.Source, Type: ci.Type, Location: ci.Location,
				SourceAccount: ci.SourceAccount, Name: ci.ItemName,
			})
		}
		// DEBUG, not INFO: the box re-emits presetsUpdated in bursts (~20x around
		// boot / a preset sync), and the MultiWriter logger appends every line to
		// the NAND log, so an INFO burst is a stack of rapid NAND writes for no
		// diagnostic gain. The slots are still captured at DEBUG when needed.
		c.logger.Debug("box ws: presetsUpdated", "count", len(slots), "slots", strings.Join(slots, ","))
		if c.handler != nil && len(bps) > 0 {
			c.handler.OnPresetsChanged(ctx, bps)
		}
	case f.ZoneUpdated != nil:
		// The box's multiroom zone / stereo pair changed. Previously this frame
		// fell through as an "unrecognized frame" (Klaus 2026-06-12), so STR was
		// blind to box-formed groups: it could not show or dissolve them, and a
		// stereo pair sourced from STR played mono. Surface it typed so the agent
		// can track and reconcile it.
		known = true
		z := f.ZoneUpdated.toState()
		if z.Master == "" {
			c.logger.Info("box ws: zoneUpdated -> zone dissolved")
		} else {
			c.logger.Info("box ws: zoneUpdated", "master", z.Master, "senderIsMaster", z.SenderIsMaster,
				"members", len(z.Members))
		}
		if c.handler != nil {
			c.handler.OnZoneChanged(ctx, z)
		}
	}

	pe := f.NowSelection
	if pe == nil {
		pe = f.PresetSelected
	}
	if pe == nil {
		if !known {
			// Some events arrive as a BARE root element, not wrapped in <updates>
			// (the box sends <userActivityUpdate/> and <errorUpdate> this way), so
			// the typed <updates> parse above leaves them nil. Recover the ones we
			// act on by the ROOT element name. This is structural (the element
			// name), NOT a content substring, so it keeps the title false-positive
			// protection the typed parse added.
			switch rootLocalName(s) {
			case "userActivityUpdate":
				// Lone thumb ping (see noteUserActivity). Regressed for bare frames
				// when the parser went typed; this restores it (live box log
				// 2026-06-12 showed bare <userActivityUpdate/> as unrecognized).
				c.noteUserActivity(ctx, data)
				return
			case "errorUpdate":
				// The box reports playback/source failures as a bare <errorUpdate>
				// frame (UPnP SetURI rejected, wrong state, bad URL, audio timeout).
				// These used to fall through to the generic "unrecognized frame" INFO
				// line, so real box errors were buried in diagnostics. Surface them at
				// WARN with the code/name so a bundle shows exactly what the box
				// rejected: e.g. 1036 UpnpRcvdContentItemInWrongState (SetURI raced a
				// standby wake), 3101 AUDIO_ERROR_BAD_URL (a stale/unplayable preset),
				// 3103 AUDIO_ERROR_TIMEOUT. Diagnostic only; STR's recall/verify paths
				// already recover, this just makes the cause visible.
				if v, name, sev, detail := parseBoxError(s); v != "" {
					c.logger.Warn("box ws: box reported error",
						"value", v, "name", name, "severity", sev, "detail", detail)
					// 1036 UNABLE_TO_PROCESS_NOT_LOGGED_IN: the box refuses the UPnP
					// source because it does not think it is signed into an account.
					// Re-pushing just flaps the source and can wedge the box (Michal's
					// ST300). Signal the agent to force a re-login and stand the recall
					// retry down instead of thrashing.
					//
					// The box reuses code 1036 for two flavors that can arrive
					// SEPARATELY or TOGETHER:
					//   - name UNABLE_TO_PROCESS_NOT_LOGGED_IN: the box refuses the
					//     source because it lost its logged-in/associated state (it can
					//     keep a margeAccountUUID yet still report this). The 5-min
					//     autopair heartbeat skips a box that carries a UUID, so only a
					//     forced re-assert of setMargeAccount (ForcePair) restores it.
					//   - detail UpnpRcvdContentItemInWrongState: the routine race of a
					//     SetURI against a standby wake / a preset->preset teardown, and
					//     the expected teardown when /setZone kills an in-flight UPnP
					//     session during group forming (#70). By itself NOT a login
					//     problem: firing the self-heal on it killed the recall retry
					//     and forced a pointless re-pair on every wake race.
					//
					// The decisive field is the NAME (the box's authoritative reason).
					// Real hardware-preset rejections on the Portable/ST10/ST20/ST30
					// carry BOTH markers at once -
					// name=UNABLE_TO_PROCESS_NOT_LOGGED_IN detail=UpnpRcvdContentItemInWrongState
					// (53/53 field log lines) - because the box could not activate its
					// own stored ContentItem PRECISELY because it is not logged in. The
					// previous detail-first check misread every one of those as a plain
					// wake race and re-pushed the identical SetURI into a box that keeps
					// answering 1036 until a power pull, never re-registering it. So
					// classify by the name first: a not-logged-in name means re-login,
					// even when the wrong-state teardown rides along in the detail.
					notLoggedIn := strings.Contains(strings.ToUpper(name), "NOT_LOGGED_IN")
					wrongState := strings.Contains(detail, "UpnpRcvdContentItemInWrongState")
					switch {
					case notLoggedIn:
						// Root cause: the box is not logged in. Re-assert the account so
						// it accepts STR's source. Also nudge the recall's verify to
						// re-point once the re-login lands (the box hangs
						// attached-but-buffering otherwise); the self-heal is rate-limited
						// and the verify re-push is a no-op once the box plays.
						c.fireLoginError()
						if h, ok := c.handler.(interface{ OnSourceRejected(context.Context) }); ok {
							h.OnSourceRejected(ctx)
						}
					case wrongState:
						// Pure wake/teardown race (name is a plain UNABLE_TO_PROCESS, no
						// NOT_LOGGED_IN). It does NOT retry on its own and can hang
						// attached-but-buffering on the Spotify stream without reaching
						// audio, which needed a manual second preset press to clear (ST30
						// 4->5 switch, 2026-07-14). Signal the recall so its verify
						// re-points instead of trusting that stuck state. NOT a login
						// problem, so it must not fire the re-login self-heal.
						if h, ok := c.handler.(interface{ OnSourceRejected(context.Context) }); ok {
							h.OnSourceRejected(ctx)
						}
					case v == "1036":
						c.fireLoginError()
					}
					return
				}
			}
			// Surface anything still unrecognized so we can map the events STR does
			// not yet handle (the preset long-press store gesture). INFO and rare,
			// so it stays in a diagnostic bundle without spamming the NAND log.
			c.logger.Info("box ws unrecognized frame", "bytes", len(data), "body", preview(data, 1800))
		}
		return
	}

	// A preset / now-selection change is identifiable activity (the user
	// recalled a preset); it explains any accompanying userActivity, so the
	// thumb heuristic must not fire on a preset press.
	c.noteExplainedActivity()

	var slot int
	_, _ = fmt.Sscanf(pe.ID, "%d", &slot)
	if slot < 1 || slot > 6 {
		// id="0" + INVALID_SOURCE follows the real press when the box cannot
		// play the source itself. Ignore it for playback, but log it once on
		// INVALID_SOURCE: this is the box's own failed self-activation that
		// shows "service unavailable" on the display (#22) before STR takes
		// over. Markers are matched within this preset element only (pe.Inner),
		// so an unrelated frame's title cannot trip it.
		if strings.Contains(pe.Inner, "INVALID_SOURCE") || strings.Contains(pe.Inner, "DISABLED") {
			// A standby wake or a source teardown makes the box restore its last
			// now-selection and, because it cannot natively play STR's UPNP
			// source, mark it INVALID_SOURCE + type=DO_NOT_RESUME. STR used to
			// INVERT that signal and resume the last stream, which made boxes
			// start playing on their own after any standby wake and kept AirPlay
			// from staying stopped (Klaus + Brecht diagnostics, 2026-06-12):
			// wake -> play -> 500 -> retry, on a loop. DO_NOT_RESUME means exactly
			// what it says. STR now stands down; playback only follows an explicit
			// user action: a real preset press (slot 1-6 below) or an app recall.
			if strings.Contains(pe.Inner, "DO_NOT_RESUME") {
				// The box left standby and, unable to play its UPNP selection
				// itself, restored it as INVALID_SOURCE + DO_NOT_RESUME. On this
				// firmware that is the ONLY power-on signal: no powerStateUpdated is
				// ever sent (verified live on a Portable/taigan 2026-06-13). So this
				// is what drives the optional power-on resume. The box will NOT play
				// it natively; STR's resume decides, gated by a per-box opt-out and a
				// zone-membership self-wake guard (see webui.ResumeLastPlay), so a
				// stereo-pair self-wake never auto-resumes.
				c.logger.Info("box ws: power-on wake (DO_NOT_RESUME restore), box will not resume natively")
				if c.handler != nil {
					c.handler.OnPowerWake(ctx)
				}
			} else {
				// DEBUG: the box emits this id=0 INVALID_SOURCE self-activation after
				// EVERY hardware preset press (the actual press is logged at INFO
				// just below), so at INFO it doubled the NAND writes per press for no
				// extra signal.
				c.logger.Debug("box self-activation rejected preset (shows 'service unavailable')",
					"id", pe.ID, "source", pe.ContentItem.Source,
					"location", pe.ContentItem.Location, "preview", preview(data, 240))
			}
		}
		return
	}
	c.logger.Info("hardware preset pressed",
		"slot", slot,
		"location", pe.ContentItem.Location,
		"source", pe.ContentItem.Source,
		"title", pe.ContentItem.ItemName,
	)
	// Stamp the press so the STOP_STATE this switch teardown emits a moment later
	// is recognised as teardown, not a user stop (see stopStateIsTeardown).
	c.mu.Lock()
	c.lastPresetPressAt = time.Now()
	c.mu.Unlock()
	if c.handler != nil {
		c.handler.OnPresetSelected(ctx, slot,
			pe.ContentItem.Location, pe.ContentItem.ItemName)
	}
}

// rootLocalName returns the local name of the first XML start element (the
// frame's root), or "" if the data is not parseable. Used to recognise
// bare-root frames the <updates>-typed parse does not capture.
// parseBoxError extracts the fields of a bare <errorUpdate><error .../></errorUpdate>
// gabbo frame. Returns an empty value when s is not an error frame, so the caller
// can fall through to the generic unrecognized-frame path.
func parseBoxError(s string) (value, name, severity, detail string) {
	var e struct {
		XMLName xml.Name `xml:"errorUpdate"`
		Error   struct {
			Value    string `xml:"value,attr"`
			Name     string `xml:"name,attr"`
			Severity string `xml:"severity,attr"`
			Detail   string `xml:",chardata"`
		} `xml:"error"`
	}
	if err := xml.Unmarshal([]byte(s), &e); err != nil {
		return "", "", "", ""
	}
	return e.Error.Value, e.Error.Name, e.Error.Severity, strings.TrimSpace(e.Error.Detail)
}

func rootLocalName(s string) string {
	dec := xml.NewDecoder(strings.NewReader(s))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
}

func preview(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}
