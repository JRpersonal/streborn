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
func (c *Client) handleMessage(ctx context.Context, data []byte) {
	c.logger.Debug("box ws frame", "bytes", len(data), "preview", preview(data, 400))

	s := string(data)

	// Remote next/prev keys: the box cannot skip a UPnP source itself, so it
	// emits a QPLAY_SKIP_*_FAILED error. Catch that and skip in go-librespot
	// instead, so the remote's track-skip keys work during Spotify playback.
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


	// Phase markers for standby/resume diagnostics (#60). powerState
	// transitions are rare and genuinely useful, so they stay at INFO.
	// connectionState and nowPlaying, however, fire every few seconds on
	// some boxes (the Portable flaps GOOD_SIGNAL<->EXCELLENT_SIGNAL
	// constantly): at WARN/INFO they wrote ~1 MB of NAND log every few
	// hours, churning the flash and rotating real diagnostics out within
	// one session. They are demoted to DEBUG so the on-box log stays
	// small and useful; the Wi-Fi signal is still captured below for the
	// settings UI regardless of log level.
	switch {
	case strings.Contains(s, "powerStateUpdated"):
		c.logger.Info("box ws phase: powerState event", "preview", preview(data, 200))
	case strings.Contains(s, "connectionStateUpdated"):
		c.logger.Debug("box ws phase: connectionState event", "preview", preview(data, 200))
		// Capture the Wi-Fi signal class; on BCO boxes this is the only
		// place it is reported (/networkInfo has no signal there).
		if sig := attrValue(s, "signal"); sig != "" {
			c.mu.Lock()
			c.lastSignal = sig
			c.mu.Unlock()
		}
	case strings.Contains(s, "nowPlayingUpdated") && !strings.Contains(s, "nowSelectionUpdated"):
		c.logger.Debug("box ws phase: nowPlaying event", "preview", preview(data, 200))
		// A STOP_STATE in a nowPlaying update is the box reporting that
		// playback was stopped (the user pressed stop on the remote/box).
		// Surface it so the agent can suppress the auto-re-push for a wanted
		// stop. INFO, not DEBUG: stops are rare and this is the signal the
		// re-push decision hinges on, so it must be visible in a bundle.
		if strings.Contains(s, "STOP_STATE") && c.handler != nil {
			c.logger.Info("box ws: playback stopped (STOP_STATE), treating as user stop")
			c.handler.OnUserStop(ctx)
		}
	}

	if !strings.Contains(s, "nowSelectionUpdated") && !strings.Contains(s, "presetSelectionUpdated") {
		return
	}

	type contentItem struct {
		Source        string `xml:"source,attr"`
		Location      string `xml:"location,attr"`
		SourceAccount string `xml:"sourceAccount,attr"`
		ItemName      string `xml:"itemName"`
	}
	type presetEl struct {
		ID          string      `xml:"id,attr"`
		ContentItem contentItem `xml:"ContentItem"`
	}
	type updates struct {
		XMLName                xml.Name  `xml:"updates"`
		NowSelectionUpdated    *presetEl `xml:"nowSelectionUpdated>preset"`
		PresetSelectionUpdated *presetEl `xml:"presetSelectionUpdated>preset"`
	}

	var u updates
	if err := xml.Unmarshal(data, &u); err != nil {
		c.logger.Debug("xml parse error", "err", err)
		return
	}
	pe := u.NowSelectionUpdated
	if pe == nil {
		pe = u.PresetSelectionUpdated
	}
	if pe == nil {
		return
	}
	var slot int
	_, _ = fmt.Sscanf(pe.ID, "%d", &slot)
	if slot < 1 || slot > 6 {
		// id="0" + INVALID_SOURCE folgt auf den echten Press wenn Box
		// den Source nicht selbst spielen kann. Ignorieren fuer die
		// Wiedergabe, aber bei INVALID_SOURCE einmal loggen: das ist die
		// Box-eigene fehlgeschlagene Self-Aktivierung, die das Display
		// "Dienst nicht verfuegbar" zeigt (#22), bevor STR uebernimmt.
		if strings.Contains(s, "INVALID_SOURCE") || strings.Contains(s, "DISABLED") {
			c.logger.Info("box self-activation rejected preset (shows 'service unavailable')",
				"id", pe.ID, "source", pe.ContentItem.Source,
				"location", pe.ContentItem.Location, "preview", preview(data, 240))
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
