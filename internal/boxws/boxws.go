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
}

// Client haelt die Verbindung zur Box.
type Client struct {
	logger   *slog.Logger
	url      string
	handler  Handler
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

	c.logger.Info("box websocket verbunden", "url", c.url)

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
		// den Source nicht selbst spielen kann. Ignorieren.
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
