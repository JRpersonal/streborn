// Responses: the stub responders for the emulated Bose cloud endpoints.

package marge

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// respondPowerOn responds to POST /streaming/support/power_on.
// The box sends diagnostic data at boot; we must respond with an "OK"
// so the box does not mark us as "Cloud down".
func (s *Server) respondPowerOn(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<response status="OK">
  <server-time>` + time.Now().UTC().Format("2006-01-02T15:04:05Z") + `</server-time>
</response>`))
}

// respondStreamingSupport is the catchall for all other /streaming/support/* paths.
func (s *Server) respondStreamingSupport(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><response status="OK"/>`))
}

// respondBmxRegistry responds to GET /bmx/registry/v1/services with a
// service registry. The STSCertified code path
// `BMXController::GetServicesCB()` parses this response and REMOVES every
// service that does not appear in the list
// ("is no longer supported, so removing it"). So we must actively list all
// music services so STSCertified does not shut down the workers.
//
// askAgainAfter triggers the polling interval. Without the value the
// polling stops immediately.
func (s *Server) respondBmxRegistry(w http.ResponseWriter, r *http.Request) {
	// Served in the firmware's own schema (see bmxservices.go). The two
	// placeholders point back at this agent so the box reaches our adapters.
	// Always the agent's own webui port on loopback: that is where the BMX
	// adapter endpoints live (internal/webui/lir.go). Deriving it from the
	// request Host would point at the marge port instead, which serves the
	// cloud stub and not the adapters, and the box would silently fail to
	// resolve a station.
	base := "http://127.0.0.1:8888"
	_ = r
	body := strings.ReplaceAll(bmxServicesJSON, "{BMX_SERVER}", base)
	body = strings.ReplaceAll(body, "{MEDIA_SERVER}", base+"/media")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// respondBmxAvailability answers GET /bmx/registry/v1/servicesAvailability,
// which the registry's own _links block points at and STR never served.
func (s *Server) respondBmxAvailability(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"services":[{"canAdd":true,"canRemove":false,"service":"TUNEIN"}]}`))
}

// respondBmxGeneric is the catchall for other /bmx/* paths.
func (s *Server) respondBmxGeneric(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// respondSourceProviders responds to GET /streaming/sourceproviders with
// a list of music service providers. From the BoseApp binary we know:
// the wire format is XML (not Protobuf), the schema has two fields per
// provider: id and name. The box reads this, registers the providers and makes
// the associated sources READY.
//
// If TUNEIN is in the list, INTERNET_RADIO should become available as a source
// and preset buttons with internet radio stations should work.
func (s *Server) respondSourceProviders(w http.ResponseWriter, _ *http.Request) {
	// ProtoToMarkup convention:
	//   message sourceProviders { repeated SourceProvider sourceprovider = 1; }
	//   message SourceProvider {
	//     optional string id = 1;             // → attribute id="..."
	//     optional Common.String name = 2;    // → child <name>VALUE</name>
	//   }
	// Wrapper on the outside, same as for addDevice success:
	// <response status="OK"><sourceProviders>...</sourceProviders></response>
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	// Reflect the box's pre-existing account-linked cloud sources (Deezer
	// "Path A") so the box does not drop them. No-op when the reflect file is
	// empty/absent (the default on a fresh install or a box that never had one).
	var extra strings.Builder
	for _, r := range s.reflected() {
		id := xmlEscapeText(r.Source)
		if id == "" {
			continue
		}
		extra.WriteString(`<sourceprovider id="` + id + `"><name>` + xmlEscapeText(r.Name) + `</name></sourceprovider>`)
	}
	// This catalogue is what the account's <sourceproviderid> values resolve
	// against, so the ids MUST be the firmware's NUMBERS, not names: STR used
	// to answer id="TUNEIN" while an account source said 11, and a reference
	// that resolves to nothing leaves the source unregistered. The full list is
	// sent (not only the ids STR uses) so the firmware does not take a
	// "usual ids missing" path, and the declaration is the standalone form the
	// real cloud used. The timestamps are constant; the firmware only stores
	// them.
	const spTS = "2012-09-19T12:43:00.000+00:00"
	providers := []struct{ ID, Name string }{
		{"1", "PANDORA"}, {"2", "INTERNET_RADIO"}, {"3", "OFF"}, {"4", "LOCAL"},
		{"5", "AIRPLAY"}, {"6", "CURRATED_RADIO"}, {"7", "STORED_MUSIC"},
		{"8", "SLAVE_SOURCE"}, {"9", "AUX"}, {"10", "RECOMMENDED_INTERNET_RADIO"},
		{"11", "LOCAL_INTERNET_RADIO"}, {"12", "GLOBAL_INTERNET_RADIO"},
		{"13", "HELLO"}, {"14", "DEEZER"}, {"15", "SPOTIFY"}, {"16", "IHEART"},
		{"17", "SIRIUSXM"}, {"18", "GOOGLE_PLAY_MUSIC"}, {"19", "QQMUSIC"},
		{"20", "AMAZON"}, {"21", "LOCAL_MUSIC"}, {"22", "WBMX"},
		{"23", "SOUNDCLOUD"}, {"24", "TIDAL"}, {"25", "TUNEIN"},
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" standalone="yes"?><sourceProviders>`)
	for _, p := range providers {
		b.WriteString(`<sourceprovider id="` + p.ID + `"><createdOn>` + spTS +
			`</createdOn><name>` + p.Name + `</name><updatedOn>` + spTS +
			`</updatedOn></sourceprovider>`)
	}
	b.WriteString(extra.String())
	b.WriteString(`</sourceProviders>`)
	_, _ = w.Write([]byte(b.String()))
}

// respondAddDevice is the response to the AddDevice sync that the box triggers
// after POST /setMargeAccount. Path: /streaming/account/<accountId>/device/
//
// Observed from box-spy: the box sends
//
//	POST /streaming/account/<accountId>/device/
//	Content-Type: application/vnd.bose.streaming-v1.2+xml
//	Authorization: <userAuthToken from PairDeviceWithAccount>
//	Body: <device deviceid="..."><name>...</name><macaddress>...</macaddress></device>
//
// The box expects an adddeviceresponse XML with a margetoken field as response.
// If margetoken is not empty, the state machine goes to MargeStateAssociated.
// addDeviceFormat controls the XML format of the adddeviceresponse via env var.
// Values: "elem" (default), "attr", "wrap", "elem201", "attr201", "wrap201",
// "self".
func addDeviceFormat() string {
	v := os.Getenv("STICK_ADD_DEVICE_FORMAT")
	if v == "" {
		// wrap201 made the box reach MargeStateAssociated in the sweep on
		// 2026-05-15 (it then fetches
		// /streaming/sourceproviders).
		return "wrap201"
	}
	return v
}

// deviceIDFromAddDeviceBody pulls the deviceid the box states about itself out
// of the addDevice POST body:
//
//	<device deviceid="AABBCCDDEEFF"><name>..</name><macaddress>..</macaddress></device>
//
// Returns "" when the body is absent, unreadable or shaped differently, so a
// firmware that posts something else simply leaves the current id in place.
func deviceIDFromAddDeviceBody(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024))
	if err != nil {
		return ""
	}
	// The body is consumed here, so hand a fresh reader back to the rest of the
	// handler chain; nothing downstream reads it today, but a silent one-shot
	// body would be a nasty trap for whoever adds that later.
	r.Body = io.NopCloser(bytes.NewReader(body))
	var doc struct {
		DeviceID string `xml:"deviceid,attr"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return ""
	}
	return ValidDeviceID(doc.DeviceID)
}

// ValidDeviceID normalises a box-stated device id and rejects anything that is
// not one: uppercase, 12 characters, hex only, no separators. Returns "" for an
// unusable value, so a caller can simply keep whatever it already had.
//
// Worth validating rather than trusting: a wrong id here is not a cosmetic
// defect, it makes the firmware discard the entire account payload and take the
// hardware preset buttons down with it, and a malformed value would be far
// harder to spot afterwards than a rejected one.
func ValidDeviceID(raw string) string {
	id := strings.ToUpper(strings.TrimSpace(raw))
	if len(id) != 12 {
		return ""
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789ABCDEF", c) {
			return ""
		}
	}
	return id
}

func (s *Server) respondAddDevice(w http.ResponseWriter, r *http.Request) {
	format := addDeviceFormat()
	token := os.Getenv("STICK_MARGE_TOKEN")
	if token == "" {
		token = "11111111-1111-1111-1111-111111111111"
	}
	// The box states its own id in this POST, and it does so seconds BEFORE it
	// fetches the account. That makes this the earliest authoritative correction
	// for a deviceID the agent could only guess at startup, and it costs
	// nothing: the body is already being read. Without it the account payload
	// can name a different id than the box has, the firmware does not find
	// itself in <devices>, and it silently drops the whole account - taking the
	// source registration, and with it the hardware preset keys, down with it.
	if id := deviceIDFromAddDeviceBody(r); id != "" && s.SetDeviceID(id) {
		s.logger.Warn("marge: adopting the deviceID the box reported for itself (the startup guess named a different interface)",
			slog.String("comp", "marge"), slog.String("deviceID", id))
	}
	s.logger.Info("addDevice response sent",
		slog.String("comp", "marge"),
		slog.String("clientPath", r.URL.Path),
		slog.String("format", format),
	)
	// Bose ProtoToMarkup convention: TYPE_STRING fields become XML
	// attributes on the parent element, message fields become child
	// elements. Example in the box request:
	//   <device deviceid="DEVICEID_PLACEHOLDER">          // string field as attribute
	//     <name>...</name>                         // Common.String message as child
	//     <macaddress>...</macaddress>
	//   </device>
	// margetoken is an optional string, so an attribute.
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	// The association handshake is header-driven, not only body-driven: the
	// firmware reads the token from a Credentials header and identifies the
	// answered call from METHOD_NAME, the way the real cloud replied. Sending
	// the token ONLY inside the XML (what STR did until now) can leave the
	// MargeClient short of a completed association, and every later
	// setMargeAccount then starts a fresh onboarding instead of re-affirming
	// the existing one - which is exactly the source bounce and self-off that
	// made STR remove its login maintenance (see project_selfoff_login_
	// maintenance). Additive: the body stays byte-identical, so a firmware
	// that only reads the XML is unaffected.
	w.Header().Set("Credentials", "Bearer "+token)
	w.Header().Set("METHOD_NAME", "addDevice")
	w.Header().Set("Location", r.URL.Path)

	status := http.StatusOK
	if strings.Contains(format, "201") {
		status = http.StatusCreated
	}
	var body string
	switch {
	case strings.HasPrefix(format, "attr"):
		body = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" ?>
<adddeviceresponse margetoken=%q></adddeviceresponse>`, token)
	case strings.HasPrefix(format, "self"):
		body = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" ?>
<adddeviceresponse margetoken=%q/>`, token)
	case strings.HasPrefix(format, "wrap"):
		body = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" ?>
<response status="OK"><adddeviceresponse><margetoken>%s</margetoken></adddeviceresponse></response>`, token)
	case strings.HasPrefix(format, "valueonly"):
		// ProtoToMarkup value_only option: the outer tag directly contains
		// the string value, no inner margetoken element.
		body = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" ?>
<adddeviceresponse>%s</adddeviceresponse>`, token)
	case strings.HasPrefix(format, "minimal"):
		body = fmt.Sprintf(`<adddeviceresponse><margetoken>%s</margetoken></adddeviceresponse>`, token)
	default: // "elem"
		body = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" ?>
<adddeviceresponse><margetoken>%s</margetoken></adddeviceresponse>`, token)
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// respondAccountFull responds to /streaming/account/<id>/full with minimal
// FullAccount XML. The box uses this after AddDevice to load the account settings,
// devices and sources.
func (s *Server) respondAccountFull(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	// FullAccount.proto: account { mode, devices, sources, providerSettings, ... }
	// Sources contains MargeSource.source with type=INTERNET_RADIO and
	// sourceproviderid=INTERNET_RADIO. This should make the box register the
	// source as available.
	// ProtoToMarkup convention:
	//   string field → attribute
	//   Common.String field → child element with text content
	//   message field → nested child element
	// The root element is not called "fullAccount" but matches the message
	// name "account" or the parent field name. Here we try
	// <fullAccount> as root (matches the filename convention).
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>
<fullAccount>
  <mode><text>global</text></mode>
  <sources>` + staticRadioSourceXML() + s.storedMusicXML() + `
  </sources>
</fullAccount>`))
}

// reflectedSourcesXML renders the reflected account-linked cloud sources (Deezer
// "Path A") as <source> elements for the account response, or "" when none are
// reflected. Shared so the live account handler and tests agree on the shape.
// reflectSourceFormat selects the XML shape of a reflected account source via
// the STR_REFLECT_SOURCE_FORMAT env var (or, if unset, the reflectFormatPath
// marker file), so the shape the box accepts as a READY
// (playable) source can be swept on hardware, the same way addDeviceFormat sweeps
// the addDevice reply. The box marking a re-advertised account source (Deezer)
// READY again would mean the source went UNAVAILABLE only because STR stopped
// advertising it, not because the cached account login expired. Empty/"default"
// keeps the original shape, so this is a no-op unless explicitly set.
// Values: "default" (empty credential), "status" (+ status="READY"),
// "statususer" (status + a non-empty username credential), "minimal" (id+type+name).
func (s *Server) reflectSourceFormat() string {
	if v := strings.TrimSpace(os.Getenv("STR_REFLECT_SOURCE_FORMAT")); v != "" {
		return v
	}
	if s.reflectFormatPath != "" {
		if b, err := os.ReadFile(s.reflectFormatPath); err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				return v
			}
		}
	}
	return "default"
}

// renderReflectedSource renders one reflected account source as a <source>
// element in the chosen format. "default" reproduces the historical shape
// byte-for-byte.
func renderReflectedSource(format, acct, typ, name string) string {
	switch format {
	case "status":
		return "\n    <source id=\"" + acct + "\" type=\"" + typ + "\" status=\"READY\">" +
			"<credential type=\"\" text=\"\"/><name>" + name + "</name>" +
			"<username>" + acct + "</username><sourceproviderid>" + typ + "</sourceproviderid>" +
			"<sourcename>" + name + "</sourcename></source>"
	case "statususer":
		return "\n    <source id=\"" + acct + "\" type=\"" + typ + "\" status=\"READY\">" +
			"<credential type=\"USERNAME\" text=\"" + acct + "\"/><name>" + name + "</name>" +
			"<username>" + acct + "</username><sourceproviderid>" + typ + "</sourceproviderid>" +
			"<sourcename>" + name + "</sourcename></source>"
	case "minimal":
		return "\n    <source id=\"" + acct + "\" type=\"" + typ + "\">" +
			"<name>" + name + "</name><sourceproviderid>" + typ + "</sourceproviderid></source>"
	default: // "default": the original shape
		return "\n    <source id=\"" + acct + "\" type=\"" + typ + "\">" +
			"<credential type=\"\" text=\"\"/><name>" + name + "</name>" +
			"<username>" + acct + "</username><sourceproviderid>" + typ + "</sourceproviderid>" +
			"<sourcename>" + name + "</sourcename></source>"
	}
}

func (s *Server) reflectedSourcesXML() string {
	format := s.reflectSourceFormat()
	var b strings.Builder
	for _, r := range s.reflected() {
		typ := xmlEscapeText(strings.ToUpper(strings.TrimSpace(r.Source)))
		if typ == "" {
			continue
		}
		acct := xmlEscapeText(r.Account)
		name := xmlEscapeText(r.Name)
		if name == "" {
			name = typ
		}
		b.WriteString(renderReflectedSource(format, acct, typ, name))
	}
	return b.String()
}

// respondProviderSettings responds to /streaming/account/<id>/provider_settings.
// Music service provider settings (Spotify token, etc). We return empty.
func (s *Server) respondProviderSettings(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>
<providerSettings/>`))
}

// respondMargeAccountFull returns a "configured" Marge account.
// When the box requests account info, we say "yes, you are logged in".
func (s *Server) respondMargeAccountFull(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// Reflect the box's pre-existing account-linked cloud sources (Deezer
	// "Path A") inside the account so the box re-registers them and plays them
	// via its own cached token. Best-effort + experimental: the exact schema the
	// box consumes here is unverified; this is a no-op when nothing is reflected
	// (the safe default on a fresh install or a box that never had a cloud src).
	// The account ALWAYS carries the native internet-radio source, plus any
	// reflected account-linked cloud sources. Until now this block existed
	// only when a Deezer reflection was configured, so on a normal box the
	// account advertised no sources at all - and a source the account does
	// not name is one the firmware will not keep.
	// The account schema is the one captured from the real cloud, not a
	// hand-invented one: <id> (not <uuid>), <accountStatus>ENABLED</...> as an
	// ELEMENT (not a status attribute), plus mode / preferredLanguage /
	// providerSettings, and the standalone XML declaration. STR's earlier shape
	// looked plausible but shared almost no element names with it, which is the
	// likely reason the firmware never picked up the <sources> block inside.
	// The <devices> block is part of the captured schema and is emitted even
	// when we know little about the box: the firmware looks for its OWN device
	// entry in the account it just fetched, and an account that does not list
	// it is not an account it belongs to.
	s.mu.RLock()
	devID := s.deviceID
	s.mu.RUnlock()
	const accTS = "2020-01-01T00:00:00.000+00:00"
	devices := "<devices/>"
	if devID != "" {
		devices = `<devices><device deviceid="` + xmlEscapeText(devID) + `">` +
			`<attachedProduct product_code=""><components/><productlabel></productlabel>` +
			`<serialnumber></serialnumber></attachedProduct>` +
			`<createdOn>` + accTS + `</createdOn>` +
			`<recents/>` +
			`<serialnumber>` + xmlEscapeText(devID) + `</serialnumber>` +
			`<updatedOn>` + accTS + `</updatedOn>` +
			`</device></devices>`
	}
	_, _ = w.Write([]byte(`<?xml version="1.0" standalone="yes"?>` +
		`<account><id>stick@local</id>` +
		`<accountStatus>ENABLED</accountStatus>` +
		devices +
		`<mode>global</mode>` +
		`<preferredLanguage>en</preferredLanguage>` +
		`<providerSettings/>` +
		`<sources>` + staticRadioSourceXML() + s.storedMusicXML() + s.reflectedSourcesXML() + `</sources>` +
		`</account>`))
}

func (s *Server) respondPresets(w http.ResponseWriter) {
	s.mu.RLock()
	presets := s.presets
	source := s.presetSource
	s.mu.RUnlock()
	// The live source (the stick preset store) wins over the static list: the
	// box re-reads its cloud presets during every re-onboarding, and an empty
	// answer makes the firmware wipe its own key registrations.
	if source != nil {
		if live := source(); len(live) > 0 {
			presets = live
		}
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	if len(presets) == 0 {
		_, _ = w.Write([]byte(EmptyPresetsXML))
		return
	}
	tpl, err := template.New("presets").Parse(PresetsXMLTemplate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = tpl.Execute(w, struct{ Presets []Preset }{Presets: presets})
}

func (s *Server) respondRecents(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(EmptyRecentsXML))
}

// logRecentPayload records what the box tells marge when a station starts.
//
// This is the ONLY per-station message in the whole marge conversation:
// everything else happens at boot or at pairing. It is therefore the line that
// answers "did the speaker actually change station at 06:45:14, and to what"
// in a diagnostic bundle, and it is written once per station change, not on a
// poll. The record the firmware sends carries a name, a location and a type
// and NO artwork field, which is part of why the station display cannot be
// given a per-station logo (see docs/FIRMWARE-NOTES.md).
func (s *Server) logRecentPayload(r *http.Request) {
	if r == nil || r.Body == nil {
		return
	}
	// The spy middleware has already buffered and restored the body, so reading
	// it here is safe and does not starve any later handler.
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if err != nil {
		return
	}
	s.logger.Info("marge recents: the box reported what it just started playing",
		slog.String("station", innerText(body, "name")),
		slog.String("contentItemType", innerText(body, "contentItemType")),
		slog.Int("bytes", len(body)))
}

// innerText pulls the text of the first <tag>...</tag> out of an XML document
// without a full parse. Enough for the few short fields of a recents record,
// and it cannot fail on a document shape we have not seen.
func innerText(doc []byte, tag string) string {
	s := string(doc)
	open, closing := "<"+tag+">", "</"+tag+">"
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, closing)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func (s *Server) respondServiceAvailability(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	tpl, err := template.New("svc").Parse(ServiceAvailabilityXMLTemplate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = tpl.Execute(w, struct{ Services []ServiceAvailability }{Services: DefaultServices})
}

func (s *Server) respondSources(w http.ResponseWriter) {
	s.mu.RLock()
	sources := s.sources
	deviceID := s.deviceID
	s.mu.RUnlock()

	if len(sources) == 0 {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><sources deviceID="%s"/>`, deviceID)
		return
	}
	tpl, err := template.New("sources").Parse(SourcesXMLTemplate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_ = tpl.Execute(w, struct {
		DeviceID string
		Items    []SourceItem
	}{DeviceID: deviceID, Items: sources})
}

func (s *Server) respondAccount(w http.ResponseWriter) {
	s.mu.RLock()
	acc := s.account
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	if acc == nil {
		// Confirms to the box that no account is configured.
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><MargeAccount status="UNCONFIGURED"/>`))
		return
	}
	tpl, err := template.New("acc").Parse(AccountConfiguredXMLTemplate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = tpl.Execute(w, acc)
}

func (s *Server) respondConfigStatus(w http.ResponseWriter) {
	s.mu.RLock()
	configured := s.account != nil
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	if configured {
		_, _ = w.Write([]byte(SoundTouchConfiguredXML))
	} else {
		_, _ = w.Write([]byte(SoundTouchNotConfiguredXML))
	}
}

// respondAddSource answers the box's OWN source-account registration callback,
// POST /streaming/account/<accountId>/source.
//
// This is the step that decides whether a preset may be activated by the box
// itself. The firmware posts the account it wants for a source
// (<username>UUID/0</username>) and only marks that source READY once the
// cloud confirms it; from then on its own /select of a ContentItem carrying
// that sourceAccount is legal. STR answered this path through the generic
// catchall with {"status":"ok"}, so no source account was ever confirmed -
// which is why STR's presets carry the pseudo-account "UPnPUserName" that
// appears in no source list, and why the box answers its own preset
// activation with 1036 UNABLE_TO_PROCESS_NOT_LOGGED_IN: not "this box is
// signed out" but "this preset's account is not a registered source".
//
// The reply mirrors the addDevice shape: 201, the METHOD_NAME header naming
// the answered call, an ETag, and a <source> element echoing the username the
// box asked for.
func (s *Server) respondAddSource(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8*1024))
	username := firstXMLValue(string(body), "username")
	providerID := firstXMLValue(string(body), "sourceproviderid")
	s.logger.Info("addSource callback answered", slog.String("comp", "marge"),
		slog.String("path", r.URL.Path), slog.String("username", username),
		slog.String("askedProvider", providerID))
	s.mu.Lock()
	s.registered = append(s.registered, registeredSource{
		ID: "1", Username: username, ProviderID: "7",
		Name: "Stored Music", SourceName: "STORED_MUSIC",
	})
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.Header().Set("METHOD_NAME", "addSource")
	w.Header().Set("ETag", `"str-source-1"`)
	w.Header().Set("Location", r.URL.Path)
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>` +
		`<source id="1" type="Audio"><credential type="" text=""/>` +
		`<name>Stored Music</name><username>` + xmlEscapeText(username) + `</username>` +
		`<sourceproviderid>7</sourceproviderid><sourcename>STORED_MUSIC</sourcename></source>`))
}

// firstXMLValue pulls the text of the first <tag>...</tag> out of a body
// without a full parse (the firmware's XML is tiny and hand-rolled).
func firstXMLValue(body, tag string) string {
	open, close := "<"+tag+">", "</"+tag+">"
	i := strings.Index(body, open)
	if i < 0 {
		return ""
	}
	rest := body[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j])
}

// respondAccountSources answers GET /streaming/account/<accountId>/sources,
// the list the box fetches right AFTER its addSource callback was confirmed.
//
// Measured on an ST10 (2026-08-02): POST .../source is followed within 600 ms
// by GET .../sources. Until now that landed in the generic account catchall,
// so the box got no list back and never promoted the account it had just
// registered to READY - the last missing link in the chain that makes a
// preset's sourceAccount a real, activatable account.
//
// Sources registered through addSource are remembered in memory per account
// so this list can name them; a reboot re-runs the registration.
func (s *Server) respondAccountSources(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	regs := make([]registeredSource, len(s.registered))
	copy(regs, s.registered)
	s.mu.RUnlock()
	format := sourcesListFormat()
	var b strings.Builder
	for _, r := range regs {
		b.WriteString(renderAccountSource(format, r))
	}
	inner := staticRadioSourceXML() + s.storedMusicXML() + b.String()
	var body string
	switch format {
	case "wrap":
		body = `<?xml version="1.0" encoding="UTF-8" ?><response status="OK"><sources>` + inner + `</sources></response>`
	case "account":
		body = `<?xml version="1.0" encoding="UTF-8" ?><account><sources>` + inner + `</sources></account>`
	case "fullaccount":
		body = `<?xml version="1.0" encoding="UTF-8" ?><fullAccount><mode><text>global</text></mode><sources>` + inner + `</sources></fullAccount>`
	case "flat":
		body = `<?xml version="1.0" encoding="UTF-8" ?>` + inner
	default:
		body = `<?xml version="1.0" encoding="UTF-8" ?><sources>` + inner + `</sources>`
	}
	s.logger.Info("account sources list served", slog.String("comp", "marge"),
		slog.Int("count", len(regs)), slog.String("format", format))
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.Header().Set("METHOD_NAME", "getSources")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// renderAccountSource renders one registered source. The firmware is picky
// about which attributes it accepts, so the sweep varies them.
func renderAccountSource(format string, r registeredSource) string {
	switch format {
	case "minimal":
		return `<source id="` + xmlEscapeText(r.ID) + `" type="Audio"><username>` +
			xmlEscapeText(r.Username) + `</username><sourceproviderid>` +
			xmlEscapeText(r.ProviderID) + `</sourceproviderid></source>`
	case "named":
		return `<source id="` + xmlEscapeText(r.ID) + `" type="` + xmlEscapeText(r.SourceName) + `" status="READY">` +
			`<credential type="" text=""/><name>` + xmlEscapeText(r.Name) + `</name><username>` +
			xmlEscapeText(r.Username) + `</username><sourceproviderid>` + xmlEscapeText(r.ProviderID) +
			`</sourceproviderid><sourcename>` + xmlEscapeText(r.SourceName) + `</sourcename></source>`
	default:
		return `<source id="` + xmlEscapeText(r.ID) + `" type="Audio" status="READY">` +
			`<credential type="" text=""/><name>` + xmlEscapeText(r.Name) + `</name><username>` +
			xmlEscapeText(r.Username) + `</username><sourceproviderid>` + xmlEscapeText(r.ProviderID) +
			`</sourceproviderid><sourcename>` + xmlEscapeText(r.SourceName) + `</sourcename></source>`
	}
}

// sourcesListFormat selects the account-sources shape for the hardware sweep:
// the env var first, then a file so a running margelab can be switched between
// attempts without a restart. Values: default, wrap, account, fullaccount,
// flat, minimal, named.
func sourcesListFormat() string {
	if v := strings.TrimSpace(os.Getenv("STR_SOURCES_FORMAT")); v != "" {
		return v
	}
	if p := strings.TrimSpace(os.Getenv("STR_SOURCES_FORMAT_FILE")); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				return v
			}
		}
	}
	return "default"
}

// staticRadioSourceXML is the LOCAL_INTERNET_RADIO source the account always
// advertises. It is what lets the box play custom internet radio natively
// again: the BMX service entry carries anonymousAccount.autoCreate+enabled, so
// the firmware creates the account for this source ITSELF - no addSource
// round-trip and no login, which is why a ContentItem on this source cannot
// fail the not-logged-in check that breaks UPNP presets (1036).
//
// The shape is the one community implementations converged on: the numeric
// provider id 11, sourcename LOCAL_INTERNET_RADIO, and an EMPTY username. It
// must appear in BOTH the account response and the account source list, or the
// firmware drops the source again on its next poll.
// UPNP is deliberately NOT registered here, and must not be added back.
// Measured on an ST10 (FW 27.0.6, 2026-08-02): registering it as an account
// source with provider id 21 changed nothing. The box still answered its own
// hardware preset with 1036 / UpnpRcvdContentItemInWrongState and flapped
// UPNP -> INVALID_SOURCE -> UPNP. The decisive observation is that
// GET /sources reports UPNP as status="UNAVAILABLE" even WHILE it is the
// actively playing source, so the firmware's availability check can never
// pass. UPNP is the box's local MediaRenderer: it moves only when an external
// controller sets the AVTransport URI, and "WrongState" refers to that
// transport state machine, not to a login. Serving it as an account source is
// what the firmware answers with INVALID_SOURCE.
func staticRadioSourceXML() string {
	const ts = "2020-01-01T00:00:00.000+00:00"
	return `<source id="3" type="Audio">` +
		`<createdOn>` + ts + `</createdOn>` +
		`<credential type="token"></credential>` +
		`<name>Local Internet Radio</name>` +
		`<sourceproviderid>11</sourceproviderid>` +
		`<sourcename>LOCAL_INTERNET_RADIO</sourcename>` +
		`<sourceSettings/>` +
		`<updatedOn>` + ts + `</updatedOn>` +
		`<username></username>` +
		`</source>`
}

// storedMusicSourcesXML renders the user's DLNA/UPnP media servers as account
// sources, so the box PICKS THEM UP ITSELF instead of being told about them.
//
// This is the same channel radio arrives on. The box polls
// GET /streaming/account/<id>/full at boot (measured in the marge request log:
// two /full reads within a second of the account handshake, and no /sources read
// at all unless an addSource just happened), and it keeps whatever that document
// advertises. Sitting in that document is therefore all a source needs; a push
// to /setMusicServiceAccount only matters for making a NEW server usable within
// the current session, before the next poll.
//
// The element set and order are copied from staticRadioSourceXML deliberately.
// A source rendered into /full that omits an element the firmware expects is the
// one documented way to make the whole account document fail rather than just
// that entry, so the safe move is to differ from the known-good source in
// nothing but the three values that have to change: the numeric provider id 7,
// the sourcename STORED_MUSIC, and the username, which is the media server's
// UPnP id with "/0" appended.
//
// ids start at 10 so they cannot collide with the radio source's fixed id 3.
func storedMusicSourcesXML(list []registeredSource) string {
	if len(list) == 0 {
		return ""
	}
	const ts = "2020-01-01T00:00:00.000+00:00"
	var b strings.Builder
	for i, r := range list {
		name := r.Name
		if strings.TrimSpace(name) == "" {
			name = "Music library"
		}
		b.WriteString(`<source id="` + strconv.Itoa(10+i) + `" type="Audio">` +
			`<createdOn>` + ts + `</createdOn>` +
			`<credential type="token"></credential>` +
			`<name>` + xmlEscapeText(name) + `</name>` +
			`<sourceproviderid>7</sourceproviderid>` +
			`<sourcename>STORED_MUSIC</sourcename>` +
			`<sourceSettings/>` +
			`<updatedOn>` + ts + `</updatedOn>` +
			`<username>` + xmlEscapeText(r.Username) + `</username>` +
			`</source>`)
	}
	return b.String()
}

// storedMusicXML is the current media-server source block for the account
// responses, under the read lock.
func (s *Server) storedMusicXML() string {
	s.mu.RLock()
	list := make([]registeredSource, len(s.storedMusic))
	copy(list, s.storedMusic)
	s.mu.RUnlock()
	return storedMusicSourcesXML(list)
}

// SetStoredMusicSources publishes the user's enabled media servers so every
// account response advertises them. The agent calls this at startup from the
// persisted store, and again whenever the user enables or removes one.
//
// Replaces the whole set: the store is the authority on what the user wants.
func (s *Server) SetStoredMusicSources(servers []StoredMusicSource) {
	list := make([]registeredSource, 0, len(servers))
	for _, srv := range servers {
		if strings.TrimSpace(srv.Account) == "" {
			continue
		}
		list = append(list, registeredSource{
			Username: srv.Account, ProviderID: "7",
			Name: srv.Name, SourceName: "STORED_MUSIC",
		})
	}
	s.mu.Lock()
	s.storedMusic = list
	s.mu.Unlock()
	s.logger.Info("media server sources published to the account", slog.String("comp", "marge"),
		slog.Int("count", len(list)))
}

// StoredMusicSource is one media server the account advertises. Account is the
// media server's UPnP id with "/0" appended, exactly as the box reports the id
// in /listMediaServers.
type StoredMusicSource struct {
	Account string
	Name    string
}
