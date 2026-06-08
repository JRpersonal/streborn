// Package boxapi ist ein duenner Client fuer die Bose BoseApp REST API
// auf Port 8090. Die Box selbst hostet die — wir proxien sie via Stick
// damit die Desktop App eine simple JSON Schnittstelle hat.
//
// Die meisten Endpoints sind XML (Format: <feld>wert</feld>); wir
// parsen das in Go Structs und liefern JSON nach aussen.
package boxapi

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client kapselt http.Client + Box Host.
type Client struct {
	Host string // z.B. "127.0.0.1" oder die Box IP
	HTTP *http.Client
}

// New erzeugt einen Client mit defaults.
func New(host string) *Client {
	return &Client{
		Host: host,
		HTTP: &http.Client{Timeout: 6 * time.Second},
	}
}

// ---------- Datenmodell ----------

// Info enthaelt die statische Beschreibung der Box.
type Info struct {
	DeviceID    string `json:"deviceID"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Version     string `json:"version"`
	Variant     string `json:"variant"`
	CountryCode string `json:"countryCode"`
}

// Volume ist aktueller Lautstaerke Zustand.
type Volume struct {
	Target int  `json:"target"`
	Actual int  `json:"actual"`
	Muted  bool `json:"muted"`
}

// Bass + Capabilities zusammengefasst.
type Bass struct {
	Target  int  `json:"target"`
	Actual  int  `json:"actual"`
	Min     int  `json:"min"`
	Max     int  `json:"max"`
	Default int  `json:"default"`
	Avail   bool `json:"available"`
}

// Network ist der aktive WLAN Zustand.
type Network struct {
	WifiProfileCount int                `json:"wifiProfileCount"`
	Interfaces       []NetworkInterface `json:"interfaces"`
}

// NetworkInterface ein WLAN Adapter.
type NetworkInterface struct {
	Type       string `json:"type"`
	Name       string `json:"name"`
	MAC        string `json:"macAddress"`
	IP         string `json:"ipAddress"`
	SSID       string `json:"ssid"`
	Frequency  int    `json:"frequencyKHz"`
	State      string `json:"state"`
	Signal     string `json:"signal"`
	Mode       string `json:"mode"`
}

// Source ist ein Eintrag aus /sources (Spotify, AirPlay etc).
type Source struct {
	Source        string `json:"source"`
	SourceAccount string `json:"sourceAccount"`
	Status        string `json:"status"`
	IsLocal       bool   `json:"isLocal"`
	Multiroom     bool   `json:"multiroomallowed"`
	DisplayName   string `json:"displayName"`
}

// Settings ist die kombinierte Antwort fuer den Box Einstellungen Tab.
type Settings struct {
	Info    Info     `json:"info"`
	Volume  Volume   `json:"volume"`
	Bass    Bass     `json:"bass"`
	Network Network  `json:"network"`
	Sources []Source `json:"sources"`
}

// ---------- Read ----------

// LoadSettings holt alle relevanten Zustaende mit einem Aufruf parallel.
// Bei Fehlern einzelner Endpoints werden die Felder leer gelassen.
func (c *Client) LoadSettings(ctx context.Context) (Settings, error) {
	var s Settings
	type result struct{ err error }
	get := func(path string, dst any) error {
		return c.getXML(ctx, path, dst)
	}

	// Info
	{
		var raw struct {
			DeviceID    string `xml:"deviceID,attr"`
			Name        string `xml:"name"`
			Type        string `xml:"type"`
			Components  []struct {
				Category string `xml:"componentCategory"`
				SwVer    string `xml:"softwareVersion"`
			} `xml:"components>component"`
			Variant     string `xml:"variant"`
			CountryCode string `xml:"countryCode"`
		}
		if err := get("/info", &raw); err == nil {
			s.Info = Info{
				DeviceID:    raw.DeviceID,
				Name:        raw.Name,
				Type:        raw.Type,
				Variant:     raw.Variant,
				CountryCode: raw.CountryCode,
			}
			for _, c := range raw.Components {
				if c.Category == "SCM" || s.Info.Version == "" {
					// Software Version hat oft Trailing Buildinfo —
					// nur die ersten Zahlen Punkte Zahlen behalten.
					s.Info.Version = stripBuildSuffix(c.SwVer)
				}
			}
		}
	}

	// Volume
	{
		var raw struct {
			Target int    `xml:"targetvolume"`
			Actual int    `xml:"actualvolume"`
			Mute   string `xml:"muteenabled"`
		}
		if err := get("/volume", &raw); err == nil {
			s.Volume = Volume{
				Target: raw.Target,
				Actual: raw.Actual,
				Muted:  strings.EqualFold(raw.Mute, "true"),
			}
		}
	}

	// Bass + Capabilities
	{
		var bass struct {
			Target int `xml:"targetbass"`
			Actual int `xml:"actualbass"`
		}
		if err := get("/bass", &bass); err == nil {
			s.Bass.Target = bass.Target
			s.Bass.Actual = bass.Actual
		}
		var caps struct {
			Available string `xml:"bassAvailable"`
			Min       int    `xml:"bassMin"`
			Max       int    `xml:"bassMax"`
			Default   int    `xml:"bassDefault"`
		}
		if err := get("/bassCapabilities", &caps); err == nil {
			s.Bass.Min = caps.Min
			s.Bass.Max = caps.Max
			s.Bass.Default = caps.Default
			s.Bass.Avail = strings.EqualFold(caps.Available, "true")
		}
	}

	// Network
	{
		var raw struct {
			Count      int `xml:"wifiProfileCount,attr"`
			Interfaces []struct {
				Type      string `xml:"type,attr"`
				Name      string `xml:"name,attr"`
				MAC       string `xml:"macAddress,attr"`
				IP        string `xml:"ipAddress,attr"`
				SSID      string `xml:"ssid,attr"`
				Frequency int    `xml:"frequencyKHz,attr"`
				State     string `xml:"state,attr"`
				Signal    string `xml:"signal,attr"`
				Mode      string `xml:"mode,attr"`
			} `xml:"interfaces>interface"`
		}
		if err := get("/networkInfo", &raw); err == nil {
			s.Network.WifiProfileCount = raw.Count
			for _, i := range raw.Interfaces {
				s.Network.Interfaces = append(s.Network.Interfaces, NetworkInterface{
					Type: i.Type, Name: i.Name, MAC: i.MAC, IP: i.IP,
					SSID: i.SSID, Frequency: i.Frequency,
					State: i.State, Signal: i.Signal, Mode: i.Mode,
				})
			}
		}
	}

	// Sources
	{
		var raw struct {
			Items []struct {
				Source        string `xml:"source,attr"`
				SourceAccount string `xml:"sourceAccount,attr"`
				Status        string `xml:"status,attr"`
				IsLocal       string `xml:"isLocal,attr"`
				Multiroom     string `xml:"multiroomallowed,attr"`
				Name          string `xml:",chardata"`
			} `xml:"sourceItem"`
		}
		if err := get("/sources", &raw); err == nil {
			for _, it := range raw.Items {
				s.Sources = append(s.Sources, Source{
					Source:        it.Source,
					SourceAccount: it.SourceAccount,
					Status:        it.Status,
					IsLocal:       strings.EqualFold(it.IsLocal, "true"),
					Multiroom:     strings.EqualFold(it.Multiroom, "true"),
					DisplayName:   strings.TrimSpace(it.Name),
				})
			}
		}
	}

	return s, nil
}

// ---------- Write ----------

// ---------- Multiroom (read-only) ----------

// ZoneMember beschreibt einen Slave in einer SoundTouch Zone.
// Die Bose Firmware liefert je Member die deviceID als Element-Text
// und die LAN-IP als Attribut.
type ZoneMember struct {
	DeviceID string `json:"deviceID"`
	IP       string `json:"ip"`
	Role     string `json:"role,omitempty"`
}

// Zone ist der Zustand der klassischen SoundTouch Multiroom Gruppe.
// Master ist die deviceID des Boxes die den Stream broadcasts; Members
// sind alle Boxen die dem Stream folgen. SenderIP ist die LAN-IP des
// Masters. Bei einer alleine stehenden Box liefert die Firmware ein
// leeres `<zone />` Element — dann sind Master und Members leer.
type Zone struct {
	Master   string       `json:"master,omitempty"`
	SenderIP string       `json:"senderIP,omitempty"`
	Members  []ZoneMember `json:"members"`
}

// Group ist der Zustand des neueren Stereo-Pair-Group-Konzepts
// (zwei ST10 als L/R Paar). Auch hier liefert die Firmware bei einer
// einzelnen Box ein leeres `<group />`.
type Group struct {
	ID      string       `json:"id,omitempty"`
	Name    string       `json:"name,omitempty"`
	Members []ZoneMember `json:"members"`
}

// GetZone liest /getZone und liefert die aktuelle Multiroom Zone.
// Bei einer alleinstehenden Box ist Zone leer (Master == "").
func (c *Client) GetZone(ctx context.Context) (Zone, error) {
	var raw struct {
		Master   string `xml:"master,attr"`
		SenderIP string `xml:"senderIPAddress,attr"`
		Members  []struct {
			DeviceID string `xml:",chardata"`
			IP       string `xml:"ipaddress,attr"`
			Role     string `xml:"role,attr"`
		} `xml:"member"`
	}
	if err := c.getXML(ctx, "/getZone", &raw); err != nil {
		return Zone{}, err
	}
	z := Zone{
		Master:   strings.TrimSpace(raw.Master),
		SenderIP: strings.TrimSpace(raw.SenderIP),
		Members:  make([]ZoneMember, 0, len(raw.Members)),
	}
	for _, m := range raw.Members {
		z.Members = append(z.Members, ZoneMember{
			DeviceID: strings.TrimSpace(m.DeviceID),
			IP:       strings.TrimSpace(m.IP),
			Role:     strings.TrimSpace(m.Role),
		})
	}
	return z, nil
}

// GetGroup liest /getGroup (Stereo Pair). Bei einer ST10 die nicht im
// Pair laeuft ist die Antwort leer.
func (c *Client) GetGroup(ctx context.Context) (Group, error) {
	var raw struct {
		ID      string `xml:"id,attr"`
		Name    string `xml:"name"`
		Members []struct {
			DeviceID string `xml:",chardata"`
			IP       string `xml:"ipaddress,attr"`
			Role     string `xml:"role,attr"`
		} `xml:"groupMember"`
	}
	if err := c.getXML(ctx, "/getGroup", &raw); err != nil {
		return Group{}, err
	}
	g := Group{
		ID:      strings.TrimSpace(raw.ID),
		Name:    strings.TrimSpace(raw.Name),
		Members: make([]ZoneMember, 0, len(raw.Members)),
	}
	for _, m := range raw.Members {
		g.Members = append(g.Members, ZoneMember{
			DeviceID: strings.TrimSpace(m.DeviceID),
			IP:       strings.TrimSpace(m.IP),
			Role:     strings.TrimSpace(m.Role),
		})
	}
	return g, nil
}

// ---------- Multiroom (write) ----------

// zoneXML builds the <zone> request body shared by SetZone / AddZoneSlave /
// RemoveZoneSlave. master is the device that leads (and streams to) the zone:
// the call must be POSTed to that box. Each slave contributes one
// <member ipaddress="..">deviceID</member> line. senderIPAddress is the
// master's LAN IP, which the firmware uses as the stream source address;
// omitted when empty (the firmware fills it in for add/remove).
func zoneXML(master ZoneMember, slaves []ZoneMember) string {
	var b strings.Builder
	b.WriteString(`<zone master="`)
	b.WriteString(xmlEscape(master.DeviceID))
	b.WriteString(`"`)
	if master.IP != "" {
		b.WriteString(` senderIPAddress="`)
		b.WriteString(xmlEscape(master.IP))
		b.WriteString(`"`)
	}
	b.WriteString(`>`)
	for _, s := range slaves {
		b.WriteString(`<member ipaddress="`)
		b.WriteString(xmlEscape(s.IP))
		b.WriteString(`">`)
		b.WriteString(xmlEscape(s.DeviceID))
		b.WriteString(`</member>`)
	}
	b.WriteString(`</zone>`)
	return b.String()
}

// SetZone creates (or replaces) the multiroom zone led by master with the
// given slaves. POST it to the master box; any existing zone on the master is
// replaced. The master must be actively playing a source for the slaves to
// produce sound (see #70 design notes), so callers should start playback on
// the master first.
func (c *Client) SetZone(ctx context.Context, master ZoneMember, slaves []ZoneMember) error {
	return c.postXML(ctx, "/setZone", zoneXML(master, slaves))
}

// AddZoneSlave adds slaves to the zone already led by master. The master must
// already lead a zone (call SetZone first); the firmware rejects an add to a
// box that is not yet a master.
func (c *Client) AddZoneSlave(ctx context.Context, master ZoneMember, slaves []ZoneMember) error {
	return c.postXML(ctx, "/addZoneSlave", zoneXML(master, slaves))
}

// RemoveZoneSlave drops slaves from the zone led by master. Removing the last
// remaining slave dissolves the zone; re-form it with SetZone.
func (c *Client) RemoveZoneSlave(ctx context.Context, master ZoneMember, slaves []ZoneMember) error {
	return c.postXML(ctx, "/removeZoneSlave", zoneXML(master, slaves))
}

// SetName aendert den Anzeigenamen der Box. Achtung: Bose setzt
// dabei in der Box State auch die margeURL zurueck auf den Default
// Update Server. Unser autoPair haengt das im naechsten Tick wieder ein.
func (c *Client) SetName(ctx context.Context, name string) error {
	body := fmt.Sprintf(`<name>%s</name>`, xmlEscape(name))
	return c.postXML(ctx, "/name", body)
}

// SetVolume setzt den Ziel Volume (0-100).
func (c *Client) SetVolume(ctx context.Context, v int) error {
	if v < 0 { v = 0 }
	if v > 100 { v = 100 }
	body := fmt.Sprintf(`<volume>%d</volume>`, v)
	return c.postXML(ctx, "/volume", body)
}

// SetBass setzt den Bass Wert (Range aus bassCapabilities — typischer
// ST10 Bereich -9..0).
func (c *Client) SetBass(ctx context.Context, b int) error {
	body := fmt.Sprintf(`<bass>%d</bass>`, b)
	return c.postXML(ctx, "/bass", body)
}

// ---------- helpers ----------

func (c *Client) url(path string) string {
	return fmt.Sprintf("http://%s:8090%s", c.Host, path)
}

func (c *Client) getXML(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("box %s: %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return err
	}
	return xml.Unmarshal(body, dst)
}

func (c *Client) postXML(ctx context.Context, path, body string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path), strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("box %s: %d: %s", path, resp.StatusCode, string(b))
	}
	return nil
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", `'`, "&apos;")
	return r.Replace(s)
}

// stripBuildSuffix kuerzt "27.0.6.46330.5043500 epdbuild.trunk..." auf
// "27.0.6.46330.5043500".
func stripBuildSuffix(s string) string {
	if i := strings.Index(s, " "); i >= 0 {
		return s[:i]
	}
	return s
}
