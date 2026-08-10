package boxapi

import (
	"context"
	"fmt"
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
