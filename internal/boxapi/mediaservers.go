package boxapi

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// UPnP media servers, and registering one as a native music source.
//
// The speaker discovers DLNA/UPnP media servers on the LAN by itself and lists
// them at /listMediaServers, but it will not play from one until that server is
// registered as a STORED_MUSIC account. Once it is, the source turns READY and
// the box browses and plays the server NATIVELY: no stream proxy, no UPnP push
// from STR, and the server appears in the original Bose app as well.
//
// Measured end to end on a Portable against a FRITZ!Box 6690 (2026-08-10).
// Everything below is the shape the firmware actually accepts; the reference
// for it is thlucas1/bosesoundtouchapi, since none of this is in Bose's public
// API document.

// MediaServer is one DLNA/UPnP server the speaker has discovered.
type MediaServer struct {
	// ID is the server's UPnP UDN WITHOUT the "uuid:" prefix, exactly as the
	// box reports it. It is what the source account is built from, and it must
	// match case-sensitively.
	ID           string `json:"id"`
	IP           string `json:"ip"`
	Manufacturer string `json:"manufacturer"`
	ModelName    string `json:"modelName"`
	FriendlyName string `json:"friendlyName"`
	// Registered is filled in by callers that also read /sources; the box's own
	// media-server list says nothing about whether a server is usable yet.
	Registered bool `json:"registered"`
}

// SourceAccount is the sourceAccount value that identifies this server as a
// music source. The trailing "/0" selects the server's first (and, on every
// server measured, only) account.
func (m MediaServer) SourceAccount() string {
	if strings.TrimSpace(m.ID) == "" {
		return ""
	}
	return m.ID + "/0"
}

// ListMediaServers reads /listMediaServers: every DLNA/UPnP media server the
// speaker can currently see. Discovery is the firmware's own, so this works on
// a box that has never had a music source registered.
func (c *Client) ListMediaServers(ctx context.Context) ([]MediaServer, error) {
	var raw struct {
		Servers []struct {
			ID           string `xml:"id,attr"`
			IP           string `xml:"ip,attr"`
			Manufacturer string `xml:"manufacturer,attr"`
			ModelName    string `xml:"model_name,attr"`
			FriendlyName string `xml:"friendly_name,attr"`
		} `xml:"media_server"`
	}
	if err := c.getXML(ctx, "/listMediaServers", &raw); err != nil {
		return nil, err
	}
	out := make([]MediaServer, 0, len(raw.Servers))
	for _, s := range raw.Servers {
		if strings.TrimSpace(s.ID) == "" {
			continue
		}
		out = append(out, MediaServer{
			ID: s.ID, IP: s.IP, Manufacturer: s.Manufacturer,
			ModelName: s.ModelName, FriendlyName: s.FriendlyName,
		})
	}
	return out, nil
}

// musicServiceAccountBody builds the <credentials> document both the set and
// the remove endpoint take.
func musicServiceAccountBody(source, displayName, account string) string {
	return `<credentials source="` + xmlEscape(source) +
		`" displayName="` + xmlEscape(displayName) + `"><user>` +
		xmlEscape(account) + `</user><pass></pass></credentials>`
}

// RegisterMediaServer registers a media server as a native STORED_MUSIC source.
//
// POST, not PUT: the firmware answers 405 to a PUT here even though this reads
// like a write of a single setting.
//
// The box answers 200 immediately, but the source does NOT appear at once. The
// speaker then calls out to its marge (STR) with an addSource callback, STR
// answers it and serves the account's source list, and only then does /sources
// gain the entry as READY. Measured live, that round trip took minutes rather
// than seconds, so a caller must not treat "not READY yet" as failure.
func (c *Client) RegisterMediaServer(ctx context.Context, m MediaServer) error {
	acct := m.SourceAccount()
	if acct == "" {
		return fmt.Errorf("media server has no id")
	}
	name := strings.TrimSpace(m.FriendlyName)
	if name == "" {
		name = "Media server"
	}
	return c.postXML(ctx, "/setMusicServiceAccount",
		musicServiceAccountBody("STORED_MUSIC", name, acct))
}

// UnregisterMediaServer removes the STORED_MUSIC account again. The display
// name must match the one the source was registered under, which is why callers
// pass the source's own display name back rather than a fresh guess.
func (c *Client) UnregisterMediaServer(ctx context.Context, m MediaServer) error {
	acct := m.SourceAccount()
	if acct == "" {
		return fmt.Errorf("media server has no id")
	}
	name := strings.TrimSpace(m.FriendlyName)
	if name == "" {
		name = "Media server"
	}
	return c.postXML(ctx, "/removeMusicServiceAccount",
		musicServiceAccountBody("STORED_MUSIC", name, acct))
}

// MediaItem is one entry from a media server: a folder to descend into or a
// track to play.
type MediaItem struct {
	Name string `json:"name"`
	// Type is the firmware's own word: "dir" for a container, "track" for
	// something playable. Anything else is passed through untouched.
	Type string `json:"type"`
	// Location addresses the item inside the server ("4:cont1:20:0:0:" for a
	// folder, "5:audio5:part11:11:5 TRACK" for a track). Opaque: build nothing
	// from it, hand it back as-is.
	Location string `json:"location"`
	Playable bool   `json:"playable"`
}

// BrowseMediaServer lists one container of a registered media server.
//
// An empty location browses the root. To descend, pass an item's Location and
// Name back.
//
// The request shape is NOT obvious and the firmware rejects every variation.
// Measured 2026-08-10: the container must be an <item> carrying <name> and
// <type> BEFORE its <ContentItem>. A bare <ContentItem> as a direct child of
// <navigate>, or a <mediaItemContainer>, both answer
// 500 "field not found on 'navigate'". None of this is in Bose's public API
// document; the shape comes from thlucas1/bosesoundtouchapi plus live probing.
func (c *Client) BrowseMediaServer(ctx context.Context, account, location, name string, start, count int) ([]MediaItem, int, error) {
	if strings.TrimSpace(account) == "" {
		return nil, 0, fmt.Errorf("no media server account")
	}
	if start < 1 {
		start = 1
	}
	if count <= 0 || count > 200 {
		count = 100
	}
	var b strings.Builder
	b.WriteString(`<navigate source="STORED_MUSIC" sourceAccount="` + xmlEscape(account) + `">`)
	b.WriteString(`<startItem>` + strconv.Itoa(start) + `</startItem>`)
	b.WriteString(`<numItems>` + strconv.Itoa(count) + `</numItems>`)
	if strings.TrimSpace(location) != "" {
		if strings.TrimSpace(name) == "" {
			name = "Folder"
		}
		b.WriteString(`<item Playable="1"><name>` + xmlEscape(name) + `</name><type>dir</type>` +
			`<ContentItem source="STORED_MUSIC" type="dir" location="` + xmlEscape(location) +
			`" sourceAccount="` + xmlEscape(account) + `" isPresetable="true"><itemName>` +
			xmlEscape(name) + `</itemName></ContentItem></item>`)
	}
	b.WriteString(`</navigate>`)

	var raw struct {
		TotalItems int `xml:"totalItems"`
		Items      []struct {
			Playable string `xml:"Playable,attr"`
			Name     string `xml:"name"`
			Type     string `xml:"type"`
			// The item's OWN ContentItem is the DIRECT child; the one nested
			// inside mediaItemContainer describes the PARENT container and must
			// never be mistaken for it. Selecting only direct children keeps the
			// parent out, and the last direct child is the item's own.
			Content []struct {
				Location string `xml:"location,attr"`
				Type     string `xml:"type,attr"`
			} `xml:"ContentItem"`
		} `xml:"items>item"`
	}
	if err := c.postXMLInto(ctx, "/navigate", b.String(), &raw); err != nil {
		return nil, 0, err
	}
	out := make([]MediaItem, 0, len(raw.Items))
	for _, it := range raw.Items {
		loc := ""
		if n := len(it.Content); n > 0 {
			loc = it.Content[n-1].Location
		}
		out = append(out, MediaItem{
			Name: strings.TrimSpace(it.Name), Type: strings.TrimSpace(it.Type),
			Location: loc, Playable: it.Playable == "1",
		})
	}
	return out, raw.TotalItems, nil
}

// PlayMediaItem points the speaker at one item of a media server. The location
// is the one BrowseMediaServer returned; the firmware activates it itself, so
// nothing is streamed through STR.
func (c *Client) PlayMediaItem(ctx context.Context, account, location, name string) error {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(location) == "" {
		return fmt.Errorf("media item needs an account and a location")
	}
	return c.postXML(ctx, "/select",
		`<ContentItem source="STORED_MUSIC" location="`+xmlEscape(location)+
			`" sourceAccount="`+xmlEscape(account)+`" isPresetable="true"><itemName>`+
			xmlEscape(name)+`</itemName></ContentItem>`)
}

// RegisteredMediaServerAccounts returns the sourceAccount of every STORED_MUSIC
// entry currently in /sources, whatever its status.
//
// Status is deliberately NOT filtered on: `status` in /sources is a connection
// indicator rather than a capability, and treating UNAVAILABLE as "not
// registered" would make a caller re-register a source that is already there.
func (c *Client) RegisteredMediaServerAccounts(ctx context.Context) (map[string]bool, error) {
	srcs, err := c.GetSources(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, s := range srcs {
		if strings.EqualFold(s.Source, "STORED_MUSIC") && s.SourceAccount != "" {
			out[s.SourceAccount] = true
		}
	}
	return out, nil
}
