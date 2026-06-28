// Package boxapi is a thin client for the Bose BoseApp REST API
// on port 8090. The box itself hosts it — we proxy it via the stick
// so the desktop app has a simple JSON interface.
//
// Most endpoints are XML (format: <field>value</field>); we
// parse that into Go structs and return JSON to the outside.
package boxapi

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Client wraps http.Client + box host.
type Client struct {
	Host string // e.g. "127.0.0.1" or the box IP
	HTTP *http.Client
}

// New creates a client with defaults.
func New(host string) *Client {
	return &Client{
		Host: host,
		HTTP: &http.Client{Timeout: 6 * time.Second},
	}
}

// ---------- Data model ----------

// Info contains the static description of the box.
type Info struct {
	DeviceID         string `json:"deviceID"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	Version          string `json:"version"`
	Variant          string `json:"variant"`
	ModuleType       string `json:"moduleType"`
	MargeAccountUUID string `json:"margeAccountUUID"`
	IP               string `json:"ipAddress"`
	CountryCode      string `json:"countryCode"`
}

// SetupStatus is the response from /setup. State is e.g. "SETUP_AP_OOB"
// (box opens the Bose setup AP) or "SETUP_INACTIVE" (setup finished);
// SystemState e.g. "SETUP_LANG_NOT_SET" / "SETUP_LANG_SET".
type SetupStatus struct {
	State       string `json:"state"`
	SystemState string `json:"systemState"`
}

// Volume is the current volume state.
type Volume struct {
	Target int  `json:"target"`
	Actual int  `json:"actual"`
	Muted  bool `json:"muted"`
}

// Bass + capabilities combined.
type Bass struct {
	Target  int  `json:"target"`
	Actual  int  `json:"actual"`
	Min     int  `json:"min"`
	Max     int  `json:"max"`
	Default int  `json:"default"`
	Avail   bool `json:"available"`
}

// Network is the active Wi-Fi state.
type Network struct {
	WifiProfileCount int                `json:"wifiProfileCount"`
	Interfaces       []NetworkInterface `json:"interfaces"`
}

// NetworkInterface is a Wi-Fi adapter.
type NetworkInterface struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	MAC       string `json:"macAddress"`
	IP        string `json:"ipAddress"`
	SSID      string `json:"ssid"`
	Frequency int    `json:"frequencyKHz"`
	State     string `json:"state"`
	Signal    string `json:"signal"`
	Mode      string `json:"mode"`
}

// Source is an entry from /sources (Spotify, AirPlay etc).
type Source struct {
	Source        string `json:"source"`
	SourceAccount string `json:"sourceAccount"`
	Status        string `json:"status"`
	IsLocal       bool   `json:"isLocal"`
	Multiroom     bool   `json:"multiroomallowed"`
	DisplayName   string `json:"displayName"`
}

// Settings is the combined response for the box settings tab.
type Settings struct {
	Info    Info     `json:"info"`
	Volume  Volume   `json:"volume"`
	Bass    Bass     `json:"bass"`
	Network Network  `json:"network"`
	Sources []Source `json:"sources"`
}

// ---------- Read ----------

// LoadSettings fetches all relevant states in parallel with one call.
// On errors from individual endpoints the fields are left empty.
func (c *Client) LoadSettings(ctx context.Context) (Settings, error) {
	var s Settings
	type result struct{ err error }
	get := func(path string, dst any) error {
		return c.getXML(ctx, path, dst)
	}

	// Info
	if info, err := c.GetInfo(ctx); err == nil {
		s.Info = info
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
	if net, err := c.GetNetwork(ctx); err == nil {
		s.Network = net
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

// GetInfo reads /info and returns the static box description incl.
// margeAccountUUID (empty = not paired / after factory reset),
// moduleType/variant (taigan/scm/...) and the LAN IP from the
// networkInfo block (prefers a real address over "0.0.0.0").
func (c *Client) GetInfo(ctx context.Context) (Info, error) {
	var raw struct {
		DeviceID         string `xml:"deviceID,attr"`
		Name             string `xml:"name"`
		Type             string `xml:"type"`
		MargeAccountUUID string `xml:"margeAccountUUID"`
		ModuleType       string `xml:"moduleType"`
		Variant          string `xml:"variant"`
		CountryCode      string `xml:"countryCode"`
		Components       []struct {
			Category string `xml:"componentCategory"`
			SwVer    string `xml:"softwareVersion"`
		} `xml:"components>component"`
		NetworkInfo []struct {
			Type string `xml:"type,attr"`
			IP   string `xml:"ipAddress"`
		} `xml:"networkInfo"`
	}
	if err := c.getXML(ctx, "/info", &raw); err != nil {
		return Info{}, err
	}
	info := Info{
		DeviceID:         strings.TrimSpace(raw.DeviceID),
		Name:             strings.TrimSpace(raw.Name),
		Type:             strings.TrimSpace(raw.Type),
		MargeAccountUUID: strings.TrimSpace(raw.MargeAccountUUID),
		ModuleType:       strings.TrimSpace(raw.ModuleType),
		Variant:          strings.TrimSpace(raw.Variant),
		CountryCode:      strings.TrimSpace(raw.CountryCode),
	}
	for _, comp := range raw.Components {
		if comp.Category == "SCM" || info.Version == "" {
			// Software version often has trailing build info —
			// keep only the leading numbers.dots.numbers.
			info.Version = stripBuildSuffix(comp.SwVer)
		}
	}
	for _, n := range raw.NetworkInfo {
		ip := strings.TrimSpace(n.IP)
		if ip != "" && ip != "0.0.0.0" {
			info.IP = ip
			if n.Type == "SCM" {
				break // SCM is the authoritative address
			}
		}
	}
	return info, nil
}

// GetNetwork reads /networkInfo (interface state, IP, profile count).
func (c *Client) GetNetwork(ctx context.Context) (Network, error) {
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
	if err := c.getXML(ctx, "/networkInfo", &raw); err != nil {
		return Network{}, err
	}
	net := Network{WifiProfileCount: raw.Count}
	for _, i := range raw.Interfaces {
		net.Interfaces = append(net.Interfaces, NetworkInterface{
			Type: i.Type, Name: i.Name, MAC: i.MAC, IP: i.IP,
			SSID: i.SSID, Frequency: i.Frequency,
			State: i.State, Signal: i.Signal, Mode: i.Mode,
		})
	}
	return net, nil
}

// GetSetupStatus reads /setup. On a box in factory state this
// returns e.g. state="SETUP_AP_OOB" systemstate="SETUP_LANG_NOT_SET".
func (c *Client) GetSetupStatus(ctx context.Context) (SetupStatus, error) {
	var raw struct {
		State       string `xml:"state,attr"`
		SystemState string `xml:"systemstate,attr"`
	}
	if err := c.getXML(ctx, "/setup", &raw); err != nil {
		return SetupStatus{}, err
	}
	return SetupStatus{
		State:       strings.TrimSpace(raw.State),
		SystemState: strings.TrimSpace(raw.SystemState),
	}, nil
}

// GetActiveWirelessProfile reads /getActiveWirelessProfile and returns
// the SSID of the stored Wi-Fi profile ("" if none is set).
// Caution: a set profile does NOT mean the box is also
// associated (on BCO/taigan a profile can be persisted
// without the AP->STA switch ever being completed).
func (c *Client) GetActiveWirelessProfile(ctx context.Context) (string, error) {
	var raw struct {
		SSID string `xml:"ssid"`
	}
	if err := c.getXML(ctx, "/getActiveWirelessProfile", &raw); err != nil {
		return "", err
	}
	return strings.TrimSpace(raw.SSID), nil
}

// ---------- Write ----------

// ---------- Multiroom (read-only) ----------

// ZoneMember describes a slave in a SoundTouch zone.
// The Bose firmware returns per member the deviceID as element text
// and the LAN IP as an attribute.
type ZoneMember struct {
	DeviceID string `json:"deviceID"`
	IP       string `json:"ip"`
	Role     string `json:"role,omitempty"`
}

// Zone is the state of the classic SoundTouch multiroom group.
// Master is the deviceID of the box that broadcasts the stream; Members
// are all boxes that follow the stream. SenderIP is the LAN IP of the
// master. For a standalone box the firmware returns an
// empty `<zone />` element — then Master and Members are empty.
type Zone struct {
	Master   string       `json:"master,omitempty"`
	SenderIP string       `json:"senderIP,omitempty"`
	Members  []ZoneMember `json:"members"`
}

// Group is the state of the stereo-pair group concept (two ST10 as an L/R
// pair). For a single, unpaired box the firmware returns an
// empty body (live verified: NOT `<group />`); getXML catches that
// and then returns an empty Group without error.
//
// Only the SoundTouch 10 supports real stereo pairs. All models list
// /addGroup in /supportedURLs (live verified on taigan), so that does
// NOT work as a gate; the box firmware is the final authority.
type Group struct {
	ID             string       `json:"id,omitempty"`
	Name           string       `json:"name,omitempty"`
	MasterDeviceID string       `json:"masterDeviceID,omitempty"`
	Members        []ZoneMember `json:"members"`
}

// GetZone reads /getZone and returns the current multiroom zone.
// For a standalone box the zone is empty (Master == "").
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
		dev := strings.TrimSpace(m.DeviceID)
		// The firmware /getZone body lists the master as a member too (and STR now
		// sends it that way in /setZone). Members here means the SLAVES, so drop the
		// master entry: keeps len(Members)==len(slaves) for every consumer (the
		// reconcile guard, the "main of {n}" label, the box-selector group count),
		// regardless of whether a given model echoes the master back.
		if z.Master != "" && strings.EqualFold(dev, z.Master) {
			continue
		}
		z.Members = append(z.Members, ZoneMember{
			DeviceID: dev,
			IP:       strings.TrimSpace(m.IP),
			Role:     strings.TrimSpace(m.Role),
		})
	}
	return z, nil
}

// GetGroup reads /getGroup (stereo pair). For an unpaired box the
// response is empty and an empty Group is returned (no error).
//
// Schema (derived live from the firmware docs, NOT the earlier guessed
// <groupMember>): the members are <roles><groupRole> with deviceId/role/
// ipAddress as child elements, plus <masterDeviceId> at the group level.
func (c *Client) GetGroup(ctx context.Context) (Group, error) {
	var raw struct {
		ID             string `xml:"id,attr"`
		Name           string `xml:"name"`
		MasterDeviceID string `xml:"masterDeviceId"`
		Roles          []struct {
			DeviceID string `xml:"deviceId"`
			Role     string `xml:"role"`
			IP       string `xml:"ipAddress"`
		} `xml:"roles>groupRole"`
	}
	if err := c.getXML(ctx, "/getGroup", &raw); err != nil {
		return Group{}, err
	}
	g := Group{
		ID:             strings.TrimSpace(raw.ID),
		Name:           strings.TrimSpace(raw.Name),
		MasterDeviceID: strings.TrimSpace(raw.MasterDeviceID),
		Members:        make([]ZoneMember, 0, len(raw.Roles)),
	}
	for _, m := range raw.Roles {
		g.Members = append(g.Members, ZoneMember{
			DeviceID: strings.TrimSpace(m.DeviceID),
			IP:       strings.TrimSpace(m.IP),
			Role:     strings.TrimSpace(m.Role),
		})
	}
	return g, nil
}

// groupXML builds the <group> request body for AddGroup. masterDeviceID is the
// deviceID of the master speaker (by Bose convention typically the LEFT one); members
// lists both speakers with deviceId/role(LEFT|RIGHT)/ipAddress each. Schema live
// from the firmware docs:
//
//	<group><name>..</name><masterDeviceId>..</masterDeviceId>
//	  <roles>
//	    <groupRole><deviceId>..</deviceId><role>LEFT</role><ipAddress>..</ipAddress></groupRole>
//	    <groupRole><deviceId>..</deviceId><role>RIGHT</role><ipAddress>..</ipAddress></groupRole>
//	  </roles></group>
func groupXML(name, masterDeviceID string, members []ZoneMember) string {
	var b strings.Builder
	b.WriteString(`<group><name>`)
	b.WriteString(xmlEscape(name))
	b.WriteString(`</name><masterDeviceId>`)
	b.WriteString(xmlEscape(masterDeviceID))
	b.WriteString(`</masterDeviceId><roles>`)
	for _, m := range members {
		b.WriteString(`<groupRole><deviceId>`)
		b.WriteString(xmlEscape(m.DeviceID))
		b.WriteString(`</deviceId><role>`)
		b.WriteString(xmlEscape(m.Role))
		b.WriteString(`</role><ipAddress>`)
		b.WriteString(xmlEscape(m.IP))
		b.WriteString(`</ipAddress></groupRole>`)
	}
	b.WriteString(`</roles></group>`)
	return b.String()
}

// AddGroup creates a real L/R stereo pair (POST /addGroup to the master).
// name is a display label; masterDeviceID the deviceID of the master; members
// MUST contain exactly two speakers, each with role "LEFT" or "RIGHT",
// deviceID and LAN IP. Only the ST10 actually pairs; on other models
// the firmware responds with an error, which the caller passes through to the app.
func (c *Client) AddGroup(ctx context.Context, name, masterDeviceID string, members []ZoneMember) error {
	return c.postXML(ctx, "/addGroup", groupXML(name, masterDeviceID, members))
}

// RemoveGroup dissolves the stereo pair of this box. The firmware documents
// this as a GET (response is the now-empty Group); directed at the master.
func (c *Client) RemoveGroup(ctx context.Context) error {
	var ignore struct{}
	return c.getXML(ctx, "/removeGroup", &ignore)
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
		b.WriteString(`"`)
		// Preserve the L/R role for a stereo pair so the firmware re-forms it with
		// the correct channels; GetZone/GetGroup parse role, so round-trip it.
		if s.Role != "" {
			b.WriteString(` role="`)
			b.WriteString(xmlEscape(s.Role))
			b.WriteString(`"`)
		}
		b.WriteString(`>`)
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
	// /setZone on SoundTouch 10 (rhino) and 20 (spotty) takes the master ONLY in
	// the master="" attribute; the <member> list carries just the SLAVES. Listing
	// the master as its own <member> (the "documented" body from the Bose API doc /
	// thlucas1 / Home Assistant / gesellix) made the firmware SILENTLY reject the
	// zone: every native formation across a live 2x ST10 + 2x ST20 fleet read back
	// liveMaster="" liveMembers=0 (multiroom regression v0.8.0..v0.8.2, commit
	// df7764a). The slaves-only body of v0.7.29 is what actually works on the
	// hardware, so we send that. addZoneSlave/removeZoneSlave already list only the
	// affected slaves. (#70)
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

// SetName changes the display name of the box. Caution: Bose also resets
// the margeURL in the box state to the default update server in the
// process. Our autoPair re-attaches it on the next tick.
func (c *Client) SetName(ctx context.Context, name string) error {
	body := fmt.Sprintf(`<name>%s</name>`, xmlEscape(name))
	return c.postXML(ctx, "/name", body)
}

// SetVolume sets the target volume (0-100).
func (c *Client) SetVolume(ctx context.Context, v int) error {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	body := fmt.Sprintf(`<volume>%d</volume>`, v)
	return c.postXML(ctx, "/volume", body)
}

// SetBass sets the bass value (range from bassCapabilities — typical
// ST10 range -9..0).
func (c *Client) SetBass(ctx context.Context, b int) error {
	body := fmt.Sprintf(`<bass>%d</bass>`, b)
	return c.postXML(ctx, "/bass", body)
}

// ---------- helpers ----------

func (c *Client) url(path string) string {
	return fmt.Sprintf("http://%s:8090%s", c.Host, path)
}

// SiteSurvey kicks the box radio into a ~5 s scan via /performWirelessSiteSurvey
// and returns the SSIDs the box can actually SEE. SoundTouch speakers are 2.4 GHz
// only, so a 5 GHz-only network never appears here. STR uses this as a pre-flight
// before a Wi-Fi change so it never strands the box on a network it cannot join
// (the box would otherwise leave its current network and fail to join the new
// one, forcing a Bose-app re-pair).
func (c *Client) SiteSurvey(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/performWirelessSiteSurvey"), strings.NewReader(`<PerformWirelessSiteSurvey timeout="5"/>`))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("box site survey: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	var raw struct {
		Items []struct {
			SSID string `xml:"ssid,attr"`
		} `xml:"items>item"`
	}
	if err := xml.Unmarshal(ensureUTF8(body), &raw); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(raw.Items))
	for _, it := range raw.Items {
		if s := strings.TrimSpace(it.SSID); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
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
	// An empty (or whitespace-only) 200 body means "no state" on several
	// firmware reads: a standalone box returns an empty body from /getGroup
	// (live verified on taigan, NOT `<group/>`). xml.Unmarshal would fail that
	// with io.EOF, so leave dst zero-valued and report success instead.
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	return xml.Unmarshal(ensureUTF8(body), dst)
}

// ensureUTF8 returns b unchanged when it is already valid UTF-8, otherwise it
// reinterprets the bytes as Latin-1 (ISO-8859-1) and re-encodes them as UTF-8.
// The SoundTouch firmware labels /info as UTF-8 but sometimes emits an umlaut
// box name as a lone Latin-1 byte ("ü" = 0xFC). xml.Unmarshal then rejects the
// whole document as invalid UTF-8, so GetInfo loses the deviceID + name (which
// would also defeat the zone master-id resolution in webui.localDeviceID), and
// the name renders garbled as "K�che" downstream (#70, Albrecht). Latin-1 maps
// 1:1 to the first 256 code points, so ASCII is untouched and only high bytes
// are widened; a body that is already valid UTF-8 is returned as-is.
func ensureUTF8(b []byte) []byte {
	if utf8.Valid(b) {
		return b
	}
	out := make([]byte, 0, len(b)+8)
	for _, c := range b {
		out = utf8.AppendRune(out, rune(c))
	}
	return out
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

// stripBuildSuffix shortens "27.0.6.46330.5043500 epdbuild.trunk..." to
// "27.0.6.46330.5043500".
func stripBuildSuffix(s string) string {
	if i := strings.Index(s, " "); i >= 0 {
		return s[:i]
	}
	return s
}
