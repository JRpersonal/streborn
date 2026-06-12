// Package boxws verbindet sich mit dem Bose WebSocket Notification Stream
// auf Port 8080 (Subprotocol "gabbo") und reagiert auf eingehende Events.
//
// Wenn ein User eine physische Preset Taste auf der Box drueckt, sendet
// die BoseApp ueber diesen WebSocket eine `<updates>` Nachricht mit
// `presetSelectionUpdated` oder `nowPlayingUpdated`. Wir hooken den Event
// und triggern unseren UPnP Player mit der zugehoerigen Stream URL.
//
// Damit funktionieren die Hardware Preset Tasten obwohl Bose's eigene
// Music Services in der FW deaktiviert sind.
package boxws

import (
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// PresetEvent wird gefeuert wenn die Box meldet dass ein Preset Slot
// ausgewaehlt wurde.
type PresetEvent struct {
	Slot int
}

// Handler bekommt eingehende Events aus dem Box WebSocket.
type Handler interface {
	// OnPresetSelected wird gefeuert wenn die Box meldet dass ein Preset
	// Slot aktiv ausgewaehlt wurde (physische Hardware Taste oder
	// API Trigger). location und title kommen aus dem Box ContentItem
	// und koennen ueber UPnP an die Box geschickt werden.
	OnPresetSelected(ctx context.Context, slot int, location string, title string)

	// OnRemoteSkip wird gefeuert wenn die Fernbedienung Naechster/Vorheriger
	// Titel drueckt (die Box kann eine UPnP Quelle nicht selbst skippen und
	// meldet QPLAY_SKIP_*_FAILED). forward=true -> next, false -> prev.
	OnRemoteSkip(ctx context.Context, forward bool)

	// OnUserStop wird gefeuert wenn die Box meldet dass die Wiedergabe
	// gestoppt wurde (playStatus STOP_STATE in einem nowPlayingUpdated Event),
	// also der Nutzer ueber Fernbedienung/Box bewusst gestoppt hat. Der Agent
	// nutzt das um die Auto-Wiederaufnahme nicht gegen einen gewollten Stop
	// laufen zu lassen.
	OnUserStop(ctx context.Context)

	// OnThumbActivity wird gefeuert wenn die Box ein "nacktes"
	// userActivityUpdate meldet: ein Tastendruck ohne begleitendes
	// Volume-/NowPlaying-/Preset-Event. Die Fernbedienungs-Daumen liefern auf
	// dieser Firmware nur dieses generische Event ohne Hoch/Runter-Kennung;
	// ein nacktes userActivity ist die beste verfuegbare Naeherung fuer einen
	// Daumendruck. Der Agent nutzt es als (einzelnen, nicht
	// hoch/runter-unterscheidbaren) Trigger fuer einen konfigurierten Webhook.
	// Entprellt und gegen Volume/Preset gefiltert in boxws; trotzdem
	// heuristisch, daher live tunebar.
	OnThumbActivity(ctx context.Context)

	// OnWakeResume wird gefeuert wenn der Nutzer die Box per Power-Taste aus
	// dem Standby aufweckt. Die Box gibt dafuer kein powerStateUpdated; sie
	// versucht stattdessen ihre letzte Auswahl wiederherzustellen und meldet,
	// weil sie den STR-UPNP-Stream nicht selbst spielen kann, ein
	// nowSelectionUpdated id=0 mit source=INVALID_SOURCE und type=DO_NOT_RESUME.
	// Genau das nutzen wir als "Power an"-Signal: der Agent spielt den zuletzt
	// gespielten Stream (das letzte Preset) wieder an.
	OnWakeResume(ctx context.Context)

	// OnPowerKey wird bei einem powerStateUpdated (Power-Taste / Standby-Wechsel)
	// gefeuert. Fuer den optionalen "power"-Webhook (nur zusaetzlich: STR kann das
	// firmware-seitige Ein/Ausschalten nicht unterdruecken). Beta.
	OnPowerKey(ctx context.Context)

	// OnSourceAux wird gefeuert wenn die aktive Quelle auf AUX wechselt. Fuer den
	// optionalen "aux"-Webhook (nur zusaetzlich; die Firmware schaltet den Eingang
	// ohnehin um). Heuristisch ueber den source-Wechsel erkannt, daher Beta.
	OnSourceAux(ctx context.Context)
}

// Client haelt die Verbindung zur Box.
type Client struct {
	logger  *slog.Logger
	url     string
	handler Handler

	// lastSignal is the most recent Wi-Fi signal class the box reported
	// over the gabbo stream (GOOD_SIGNAL / MARGINAL_SIGNAL / ...). On BCO
	// speakers (Portable, scm ST20) /networkInfo exposes no signal, so
	// the settings UI uses this instead. Guarded; read via LastWifiSignal.
	mu         sync.Mutex
	lastSignal string
	// lastSource tracks the most recent active source seen on a now-selection /
	// now-playing frame, so the aux webhook fires once on the transition to AUX
	// rather than repeatedly while AUX stays the active source.
	lastSource string

	// Thumb-trigger heuristic state. The remote thumbs keys surface only as a
	// generic <userActivityUpdate/>; we treat a "lone" one (no volume / now
	// playing / preset event around it) as a thumb press and fire
	// OnThumbActivity once, debounced. See noteExplainedActivity / noteUserActivity.
	thumbMu       sync.Mutex
	thumbPending  *time.Timer
	thumbExplained time.Time
	thumbLastFire  time.Time
}

// thumbExplainWindow is how close an explained event (volume/preset/now
// playing) must be to a userActivity for that activity to count as "explained"
// (i.e. NOT a thumb). thumbSettle is how long we wait after a lone userActivity
// before firing, to let any sibling event arrive and cancel it.
const (
	thumbExplainWindow = 600 * time.Millisecond
	thumbSettle        = 500 * time.Millisecond
	thumbDebounce      = 2 * time.Second
)

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
func (c *Client) noteUserActivity(ctx context.Context) {
	c.thumbMu.Lock()
	defer c.thumbMu.Unlock()
	if time.Since(c.thumbExplained) < thumbExplainWindow {
		return // explained by a recent volume/preset/now-playing event
	}
	if c.thumbPending != nil {
		return // already waiting to fire
	}
	c.thumbPending = time.AfterFunc(thumbSettle, func() {
		c.thumbMu.Lock()
		c.thumbPending = nil
		explained := time.Since(c.thumbExplained) < thumbExplainWindow
		debounced := !c.thumbLastFire.IsZero() && time.Since(c.thumbLastFire) < thumbDebounce
		if explained || debounced {
			c.thumbMu.Unlock()
			return
		}
		c.thumbLastFire = time.Now()
		c.thumbMu.Unlock()
		c.logger.Info("box ws: lone user-activity -> thumb trigger")
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
	return c.lastSignal
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

// New erzeugt einen Client. url Beispiel: "ws://127.0.0.1:8080/".
func New(logger *slog.Logger, url string, handler Handler) *Client {
	return &Client{logger: logger, url: url, handler: handler}
}

// Run blockiert und reconnected automatisch wenn die Verbindung abbricht.
// Stop via ctx Cancel.
//
// Box sendet keine eigenen Keepalive Frames. Wenn lange nichts passiert
// (kein Tastendruck, kein Volume Change), ist das normal - keine WARN
// Spam dafuer.
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
			// Read timeout ist normal wenn Box nicht aktiv ist, reconnect
			// laeuft sauber. Andere Errors hingegen interessant.
			if strings.Contains(err.Error(), "i/o timeout") {
				c.logger.Debug("box websocket idle reconnect", "retry_in", backoff)
			} else {
				c.logger.Warn("box websocket Verbindung verloren", "err", err, "retry_in", backoff)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
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

	// Reader Loop
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Longer read deadline weil Box keine Keepalive Frames sendet.
		// Reconnect ist trotzdem sauber - kein Datenverlust.
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
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

// handleMessage parsed eine eingehende XML Notification.
//
// Bose's WebSocket Format fuer Hardware Preset Tasten (gemessen 15.05.2026):
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
// Box folgt mit `<nowSelectionUpdated><preset id="0">` und INVALID_SOURCE
// wenn sie den Source nicht aktivieren kann. Wir interessieren uns nur fuer
// den ersten Event mit id >= 1.
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
		c.logger.Info("box ws phase: powerState event", "preview", preview(data, 200))
		// power webhook (beta): fire only on the transition to standby (power
		// off). STR never powers the box off itself (it only wakes it for a
		// recall), so a standby event is always a user press; this avoids the
		// webhook false-firing on STR's own wake. The STANDBY match is bounded to
		// the powerState element body. Rate-limited per id downstream.
		if c.handler != nil && strings.Contains(f.PowerState.Inner, "STANDBY") {
			c.handler.OnPowerKey(ctx)
		}
	case f.ConnectionState != nil:
		known = true
		c.logger.Debug("box ws phase: connectionState event", "preview", preview(data, 200))
		// Capture the Wi-Fi signal class; on BCO boxes this is the only place it
		// is reported (/networkInfo has no signal there). Attribute-only scan.
		if sig := attrValue(s, "signal"); sig != "" {
			c.mu.Lock()
			c.lastSignal = sig
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
		c.noteUserActivity(ctx)
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
			c.logger.Info("box ws: playback stopped (STOP_STATE), treating as user stop")
			c.handler.OnUserStop(ctx)
		}
	case f.PresetsUpdated != nil:
		// The box reported a change to its own preset list (#14). Logged as the
		// foundation for box->stick preset sync; no action wired yet.
		known = true
		slots := make([]string, 0, len(f.PresetsUpdated.Presets))
		for _, p := range f.PresetsUpdated.Presets {
			slots = append(slots, p.ID)
		}
		c.logger.Info("box ws: presetsUpdated", "count", len(slots), "slots", strings.Join(slots, ","))
	}

	pe := f.NowSelection
	if pe == nil {
		pe = f.PresetSelected
	}
	if pe == nil {
		// Not a preset / now-selection frame. Surface anything we did not
		// recognize so we can map the events STR does not yet handle (preset
		// long-press store gesture, remote thumbs keys). INFO and rare by
		// construction, so it stays in the diagnostic bundle without spamming
		// the NAND log.
		if !known {
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
		// id="0" + INVALID_SOURCE folgt auf den echten Press wenn Box den Source
		// nicht selbst spielen kann. Ignorieren fuer die Wiedergabe, aber bei
		// INVALID_SOURCE einmal loggen: das ist die Box-eigene fehlgeschlagene
		// Self-Aktivierung, die das Display "Dienst nicht verfuegbar" zeigt
		// (#22), bevor STR uebernimmt. Markers are matched within this preset
		// element only (pe.Inner), so an unrelated frame's title cannot trip it.
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
				c.logger.Info("box ws: standby wake / source teardown signalled DO_NOT_RESUME, not resuming")
			} else {
				c.logger.Info("box self-activation rejected preset (shows 'service unavailable')",
					"id", pe.ID, "source", pe.ContentItem.Source,
					"location", pe.ContentItem.Location, "preview", preview(data, 240))
			}
		}
		return
	}
	c.logger.Info("hardware preset gedrueckt",
		"slot", slot,
		"location", pe.ContentItem.Location,
		"source", pe.ContentItem.Source,
		"title", pe.ContentItem.ItemName,
	)
	if c.handler != nil {
		c.handler.OnPresetSelected(ctx, slot,
			pe.ContentItem.Location, pe.ContentItem.ItemName)
	}
}

func preview(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}
