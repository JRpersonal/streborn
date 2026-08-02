// Responses: the stub responders for the emulated Bose cloud endpoints.

package marge

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
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
func (s *Server) respondBmxRegistry(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{
  "services": [
    {"name": "streaming", "url": "https://streaming.bose.com", "version": "v1.2", "askAgainAfter": 3600},
    {"name": "content", "url": "https://content.api.bose.io", "version": "v1", "askAgainAfter": 3600},
    {"name": "marge", "url": "https://streaming.bose.com", "version": "v1", "askAgainAfter": 3600},
    {"name": "TUNEIN", "url": "https://7f5055e9ff15f2a5035a488b81ec10f4.api.radiotime.com", "baseURL": "https://7f5055e9ff15f2a5035a488b81ec10f4.api.radiotime.com", "version": "v1", "apikey": "stick-fake-key", "askAgainAfter": 3600},
    {"name": "INTERNET_RADIO", "url": "https://7f5055e9ff15f2a5035a488b81ec10f4.api.radiotime.com", "baseURL": "https://7f5055e9ff15f2a5035a488b81ec10f4.api.radiotime.com", "version": "v1", "apikey": "stick-fake-key", "askAgainAfter": 3600},
    {"name": "LOCAL_INTERNET_RADIO", "url": "https://content.api.bose.io", "baseURL": "https://content.api.bose.io", "version": "v1", "apikey": "stick-fake-key", "askAgainAfter": 3600},
    {"name": "IHEART", "url": "https://api2.iheart.com", "baseURL": "https://api2.iheart.com", "version": "v1", "apikey": "stick-fake-key", "askAgainAfter": 3600},
    {"name": "SPOTIFY", "url": "https://streaming.bose.com", "baseURL": "https://streaming.bose.com", "version": "v1", "apikey": "stick-fake-key", "askAgainAfter": 3600},
    {"name": "DEEZER", "url": "https://streaming.bose.com", "baseURL": "https://streaming.bose.com", "version": "v1", "apikey": "stick-fake-key", "askAgainAfter": 3600}
  ],
  "askAgainAfter": 3600,
  "ts": ` + fmt.Sprintf("%d", time.Now().Unix()) + `
}`))
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
	// without response wrapper, since AddDevice wrap201 is only relevant for the
	// initial pair call.
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>
<sourceProviders><sourceprovider id="TUNEIN"><name>TuneIn Radio</name></sourceprovider><sourceprovider id="INTERNET_RADIO"><name>Internet Radio</name></sourceprovider><sourceprovider id="LOCAL_INTERNET_RADIO"><name>Internet Radio</name></sourceprovider><sourceprovider id="STORED_MUSIC"><name>Stored Music</name></sourceprovider>` + extra.String() + `</sourceProviders>`))
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

func (s *Server) respondAddDevice(w http.ResponseWriter, r *http.Request) {
	format := addDeviceFormat()
	token := os.Getenv("STICK_MARGE_TOKEN")
	if token == "" {
		token = "11111111-1111-1111-1111-111111111111"
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
  <sources>` + s.radioSourceXML() + `
  </sources>
</fullAccount>`))
}

// radioSourceXML renders the native internet-radio source advertised in the
// account. Its shape is swept on hardware via the same marker file as the
// reflected-source sweep, because the box today does NOT list INTERNET_RADIO
// in /sources although we advertise it - and a native radio preset can only
// activate once the box accepts this source. Values (marker file content or
// STR_RADIO_SOURCE_FORMAT):
//
//	default    - the historical shape (INTERNET_RADIO / TuneInUser)
//	status     - default + status="READY"
//	tunein     - source type TUNEIN with a TuneIn account name
//	tuneinboth - both a TUNEIN and an INTERNET_RADIO source
//	anon       - INTERNET_RADIO with an anonymous auto-create credential
//	minimal    - id + type + name only
//
// Anything unknown falls back to "default", so a stale marker cannot break a
// user's box.
func (s *Server) radioSourceXML() string {
	src := func(id, typ, name, extraAttr, cred string) string {
		return "\n    <source id=\"" + id + "\" type=\"" + typ + "\"" + extraAttr + ">" +
			cred + "<name>" + name + "</name><username>" + id + "</username>" +
			"<sourceproviderid>" + typ + "</sourceproviderid><sourcename>" + name + "</sourcename></source>"
	}
	const emptyCred = "<credential type=\"\" text=\"\"/>"
	switch s.radioSourceFormat() {
	case "status":
		return src("TuneInUser", "INTERNET_RADIO", "TuneIn Radio", " status=\"READY\"", emptyCred)
	case "tunein":
		return src("TuneInUserName", "TUNEIN", "TuneIn Radio", " status=\"READY\"", emptyCred)
	case "tuneinboth":
		return src("TuneInUserName", "TUNEIN", "TuneIn Radio", " status=\"READY\"", emptyCred) +
			src("TuneInUser", "INTERNET_RADIO", "Internet Radio", " status=\"READY\"", emptyCred)
	case "anon":
		return src("TuneInUser", "INTERNET_RADIO", "TuneIn Radio", " status=\"READY\"",
			"<credential type=\"ANONYMOUS\" text=\"\"/><anonymousAccount autoCreate=\"true\"/>")
	case "minimal":
		return "\n    <source id=\"TuneInUser\" type=\"INTERNET_RADIO\"><name>TuneIn Radio</name>" +
			"<sourceproviderid>INTERNET_RADIO</sourceproviderid></source>"
	default:
		return src("TuneInUser", "INTERNET_RADIO", "TuneIn Radio", "", emptyCred)
	}
}

// radioSourceFormat reads the sweep selector: env first (dev), then the NAND
// marker file (on-box sweeps without a rebuild), else "default".
func (s *Server) radioSourceFormat() string {
	if v := strings.TrimSpace(os.Getenv("STR_RADIO_SOURCE_FORMAT")); v != "" {
		return v
	}
	if s.radioFormatPath != "" {
		if b, err := os.ReadFile(s.radioFormatPath); err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				return v
			}
		}
	}
	return "default"
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
	srcBlock := ""
	if sx := s.reflectedSourcesXML(); sx != "" {
		srcBlock = "\n  <sources>" + sx + "\n  </sources>"
	}
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<account status="ACTIVE">
  <uuid>streborn-local-account</uuid>
  <email>local@streborn</email>
  <token>local-token-v1</token>
  <created>2026-01-01T00:00:00Z</created>` + srcBlock + `
</account>`))
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
