// Package marge emuliert den Bose Marge Server (streaming.bose.com).
// Marge ist der interne Codename für den Bose Cloud Server, der
// Presets, Account Daten und Multiroom Steuerung verwaltet.
//
// Diese Implementierung läuft in zwei Modi gleichzeitig:
//
//  1. Spy: jede eingehende Anfrage wird mit Method, Pfad, Headers und Body
//     in den Logs aufgezeichnet. Damit lernen wir was die Box wirklich
//     anfragt sobald die DNS Umleitung steht.
//
//  2. Stub: für die wahrscheinlichsten Endpoints liefern wir sinnvolle
//     Defaults zurück. Die Antworten sind so konstruiert dass die Box im
//     Zweifel "alles ok, kein Account, keine Presets" interpretiert.
package marge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"text/template"
	"time"
)

// Server hält die Konfiguration und den HTTP Handler für die Marge Emulation.
type Server struct {
	logger   *slog.Logger
	mu       sync.RWMutex
	account  *AccountInfo
	presets  []Preset
	sources  []SourceItem
	deviceID string

	// requestLog speichert die letzten N Requests für Debug Zwecke
	// (zugaenglich ueber /__spy/log auf demselben Listener).
	requestLog    []SpyEntry
	requestLogMax int
}

// SpyEntry ist ein einzelner gelogged HTTP Request.
type SpyEntry struct {
	When    time.Time
	Method  string
	Path    string
	Headers http.Header
	Body    string
}

// Option ist ein Functional Option Pattern für die Konfiguration.
type Option func(*Server)

// WithDeviceID setzt die deviceID die in Antworten verwendet wird.
func WithDeviceID(id string) Option {
	return func(s *Server) { s.deviceID = id }
}

// WithSpyLogSize setzt wie viele Request Snapshots vorgehalten werden.
func WithSpyLogSize(n int) Option {
	return func(s *Server) { s.requestLogMax = n }
}

// WithPresets initialisiert die Preset Liste.
func WithPresets(p []Preset) Option {
	return func(s *Server) { s.presets = p }
}

// WithSources initialisiert die Source Liste.
func WithSources(items []SourceItem) Option {
	return func(s *Server) { s.sources = items }
}

// New erstellt einen neuen Marge Server.
func New(logger *slog.Logger, opts ...Option) *Server {
	s := &Server{
		logger:        logger,
		requestLogMax: 200,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Handler liefert den HTTP Handler für die Marge Endpunkte.
//
// Wir nutzen einen Catchall Handler der jede Anfrage durch den Spy
// schickt, und dahinter ein Pattern Matching auf bekannte URL Schemata.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Diagnose Endpunkte. Praefix __ damit es nicht mit potentiellen
	// echten Marge Pfaden kollidiert.
	mux.HandleFunc("/__spy/log", s.handleSpyLog)
	mux.HandleFunc("/healthz", s.handleHealthz)

	// Catchall, faengt alles andere.
	mux.HandleFunc("/", s.handleCatchall)

	return s.spyMiddleware(mux)
}

// Run startet einen optionalen eigenständigen Listener (für Tests).
// Im Produktivbetrieb wird Handler() in den zentralen Listener gemountet.
func (s *Server) Run(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler()}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		return srv.Close()
	case err := <-errCh:
		return err
	}
}

// spyMiddleware loggt jeden eingehenden Request bevor er an den eigentlichen
// Handler weitergereicht wird. Body wird gepuffert damit er sowohl gelogged
// als auch vom Handler gelesen werden kann.
func (s *Server) spyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Body kopieren, damit downstream lesen kann.
		var bodyCopy []byte
		if r.Body != nil {
			buf, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB Limit
			if err == nil {
				bodyCopy = buf
				r.Body = io.NopCloser(bytes.NewReader(buf))
			}
		}

		entry := SpyEntry{
			When:    time.Now(),
			Method:  r.Method,
			Path:    r.URL.RequestURI(),
			Headers: r.Header.Clone(),
			Body:    string(bodyCopy),
		}
		s.recordSpy(entry)

		// Auf Debug damit die periodischen Bose Lisa Polls (alle paar min)
		// nicht den Log fluten. Bei Fehlern wird im Handler INFO/WARN
		// geloggt.
		s.logger.Debug("marge request",
			slog.String("method", entry.Method),
			slog.String("path", entry.Path),
			slog.Int("bodyBytes", len(bodyCopy)),
			slog.String("ua", r.UserAgent()),
			slog.String("contentType", r.Header.Get("Content-Type")),
		)

		next.ServeHTTP(w, r)
	})
}

// recordSpy speichert einen Eintrag im Ring Buffer.
func (s *Server) recordSpy(e SpyEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestLog = append(s.requestLog, e)
	if len(s.requestLog) > s.requestLogMax {
		s.requestLog = s.requestLog[len(s.requestLog)-s.requestLogMax:]
	}
}

// RecentRequests gibt eine Kopie der zuletzt gesehenen Requests zurueck.
func (s *Server) RecentRequests() []SpyEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SpyEntry, len(s.requestLog))
	copy(out, s.requestLog)
	return out
}

// handleHealthz ist der Standard Probe Endpunkt.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleSpyLog gibt den Request Log als simpler Plaintext zurueck.
// Nur fuer Debug Zwecke gedacht, nicht produktiv exponieren.
func (s *Server) handleSpyLog(w http.ResponseWriter, _ *http.Request) {
	entries := s.RecentRequests()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, e := range entries {
		fmt.Fprintf(w, "%s  %s %s\n", e.When.Format(time.RFC3339), e.Method, e.Path)
		for k, vs := range e.Headers {
			for _, v := range vs {
				fmt.Fprintf(w, "  %s: %s\n", k, v)
			}
		}
		if e.Body != "" {
			fmt.Fprintf(w, "  ---\n  %s\n", strings.ReplaceAll(e.Body, "\n", "\n  "))
		}
		fmt.Fprintln(w, "----------------------------------------")
	}
}

// handleCatchall reagiert auf alles was nicht von einem konkreten Handler
// bedient wird. Pattern Matching auf bekannte Pfad Schemata, sonst ein
// generisches 200 OK mit XML.
func (s *Server) handleCatchall(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// TuneIn Partner Subdomain wird in /etc/hosts auf 127.0.0.1 umgeleitet
	// fuer den Fall dass STSCertified den Endpoint je rufen wuerde. Aktuell
	// passiert das in dieser FW nicht (siehe internal/marge/tunein.go).
	// Falls Box doch dort connectet, faellt der Request in den catchall
	// default mit generischem 200 OK <ack/>.

	// Reale Bose Cloud Endpoints aus mitgeschnittenem Traffic
	switch {
	case strings.HasPrefix(path, "/streaming/support/power_on"):
		s.respondPowerOn(w, r)
		return
	case strings.HasPrefix(path, "/streaming/support/"):
		s.respondStreamingSupport(w, r)
		return
	case strings.HasPrefix(path, "/streaming/sourceproviders"):
		s.respondSourceProviders(w, r)
		return
	// AddDevice Sync: /streaming/account/<accountId>/device/ POST
	// Box ruft das nach POST /setMargeAccount auf der Box selbst.
	// Antwort muss adddeviceresponse XML sein mit margetoken Element.
	case strings.HasPrefix(path, "/streaming/account/") && strings.Contains(path, "/device") && r.Method == http.MethodPost:
		s.respondAddDevice(w, r)
		return
	case strings.HasPrefix(path, "/streaming/account") || strings.HasPrefix(path, "/streaming/auth"):
		s.respondMargeAccountFull(w, r)
		return
	case strings.HasPrefix(path, "/bmx/registry/"):
		s.respondBmxRegistry(w, r)
		return
	case strings.HasPrefix(path, "/bmx/"):
		s.respondBmxGeneric(w, r)
		return
	}

	// Fallback Pattern Matching (legacy)
	switch {
	case strings.Contains(path, "preset"):
		s.respondPresets(w)
	case strings.Contains(path, "recent"):
		s.respondRecents(w)
	case strings.Contains(path, "service") && strings.Contains(path, "avail"):
		s.respondServiceAvailability(w)
	case strings.Contains(path, "source"):
		s.respondSources(w)
	case strings.Contains(path, "account") || strings.Contains(path, "auth"):
		s.respondAccount(w)
	case strings.Contains(path, "config"):
		s.respondConfigStatus(w)
	default:
		// Generisches 200 OK damit die Box nicht in Retry Loops geht.
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ack/>`))
	}
}

// respondPowerOn antwortet auf POST /streaming/support/power_on.
// Box sendet beim Boot diagnostic data, wir muessen mit einem "OK"
// antworten damit die Box uns nicht als "Cloud down" markiert.
func (s *Server) respondPowerOn(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<response status="OK">
  <server-time>` + time.Now().UTC().Format("2006-01-02T15:04:05Z") + `</server-time>
</response>`))
}

// respondStreamingSupport ist Catchall fuer alle anderen /streaming/support/* Pfade.
func (s *Server) respondStreamingSupport(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><response status="OK"/>`))
}

// respondBmxRegistry antwortet auf GET /bmx/registry/v1/services mit einer
// Service Registry. STSCertified Code Pfad
// `BMXController::GetServicesCB()` parsed diese Antwort und ENTFERNT jeden
// Service der nicht in der Liste vorkommt
// ("is no longer supported, so removing it"). Wir muessen also alle Music
// Services aktiv listen damit STSCertified die Worker nicht abschaltet.
//
// askAgainAfter triggert das polling Intervall. Ohne den Wert stoppt
// das polling sofort.
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
    {"name": "IHEART", "url": "https://api2.iheart.com", "baseURL": "https://api2.iheart.com", "version": "v1", "apikey": "stick-fake-key", "askAgainAfter": 3600},
    {"name": "SPOTIFY", "url": "https://streaming.bose.com", "baseURL": "https://streaming.bose.com", "version": "v1", "apikey": "stick-fake-key", "askAgainAfter": 3600},
    {"name": "DEEZER", "url": "https://streaming.bose.com", "baseURL": "https://streaming.bose.com", "version": "v1", "apikey": "stick-fake-key", "askAgainAfter": 3600}
  ],
  "askAgainAfter": 3600,
  "ts": ` + fmt.Sprintf("%d", time.Now().Unix()) + `
}`))
}

// respondBmxGeneric ist Catchall fuer andere /bmx/* Pfade.
func (s *Server) respondBmxGeneric(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// respondSourceProviders antwortet auf GET /streaming/sourceproviders mit
// einer Liste von Music Service Providern. Aus dem BoseApp Binary wissen wir:
// das Wire Format ist XML (nicht Protobuf), das Schema hat zwei Felder pro
// Provider: id und name. Box liest das, registriert die Provider und macht
// die zugehoerigen Sources READY.
//
// Wenn TUNEIN in der Liste ist, sollte INTERNET_RADIO als Source verfuegbar
// werden und Preset Tasten mit Internet Radio Stations funktionieren.
func (s *Server) respondSourceProviders(w http.ResponseWriter, _ *http.Request) {
	// ProtoToMarkup Konvention:
	//   message sourceProviders { repeated SourceProvider sourceprovider = 1; }
	//   message SourceProvider {
	//     optional string id = 1;             // → attribute id="..."
	//     optional Common.String name = 2;    // → child <name>VALUE</name>
	//   }
	// Wrapper aussen genauso wie bei addDevice success:
	// <response status="OK"><sourceProviders>...</sourceProviders></response>
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	// ohne response wrapper, da AddDevice wrap201 nur fuer den initialen
	// Pair Call relevant ist.
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>
<sourceProviders><sourceprovider id="TUNEIN"><name>TuneIn Radio</name></sourceprovider><sourceprovider id="INTERNET_RADIO"><name>Internet Radio</name></sourceprovider><sourceprovider id="STORED_MUSIC"><name>Stored Music</name></sourceprovider></sourceProviders>`))
}

// respondAddDevice ist die Antwort auf den AddDevice Sync den die Box nach
// POST /setMargeAccount triggert. Pfad: /streaming/account/<accountId>/device/
//
// Aus Box-Spy beobachtet: Box schickt
//   POST /streaming/account/<accountId>/device/
//   Content-Type: application/vnd.bose.streaming-v1.2+xml
//   Authorization: <userAuthToken aus PairDeviceWithAccount>
//   Body: <device deviceid="..."><name>...</name><macaddress>...</macaddress></device>
//
// Box erwartet als Response ein adddeviceresponse XML mit margetoken Feld.
// Wenn margetoken nicht leer ist, geht die State Machine in MargeStateAssociated.
// addDeviceFormat steuert das XML Format der adddeviceresponse via Env Var.
// Werte: "elem" (default), "attr", "wrap", "elem201", "attr201", "wrap201",
// "self".
func addDeviceFormat() string {
	v := os.Getenv("STICK_ADD_DEVICE_FORMAT")
	if v == "" {
		// wrap201 hat die Box im Sweep am 15.05.2026 dazu gebracht
		// MargeStateAssociated zu erreichen (sie ruft danach
		// /streaming/sourceproviders ab).
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
	s.logger.Info("addDevice antwort gesendet",
		slog.String("comp", "marge"),
		slog.String("clientPath", r.URL.Path),
		slog.String("format", format),
	)
	// Bose ProtoToMarkup Konvention: TYPE_STRING Felder werden zu XML
	// Attributes auf dem parent Element, message Felder werden zu Child
	// Elements. Beispiel im Box Request:
	//   <device deviceid="DEVICEID_PLACEHOLDER">          // string field als attribute
	//     <name>...</name>                         // Common.String message als child
	//     <macaddress>...</macaddress>
	//   </device>
	// margetoken ist optional string, also Attribute.
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
		// ProtoToMarkup value_only Option: outer tag enthaelt direkt
		// den string value, kein inner margetoken element.
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

// respondAccountFull antwortet auf /streaming/account/<id>/full mit minimaler
// FullAccount XML. Box benutzt das nach AddDevice um die Account Settings,
// Devices und Sources zu laden.
func (s *Server) respondAccountFull(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	// FullAccount.proto: account { mode, devices, sources, providerSettings, ... }
	// Sources enthaelt MargeSource.source mit type=INTERNET_RADIO und
	// sourceproviderid=INTERNET_RADIO. Damit sollte die Box die Source
	// als verfuegbar registrieren.
	// ProtoToMarkup Konvention:
	//   string field → attribute
	//   Common.String field → child element mit text content
	//   message field → nested child element
	// Root element heisst nicht "fullAccount" sondern matched die message
	// name "account" oder der parent field name. Hier probieren wir
	// <fullAccount> als root (matched filename Konvention).
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>
<fullAccount>
  <mode><text>global</text></mode>
  <sources>
    <source id="TuneInUser" type="INTERNET_RADIO">
      <credential type="" text=""/>
      <name>TuneIn Radio</name>
      <username>TuneInUser</username>
      <sourceproviderid>INTERNET_RADIO</sourceproviderid>
      <sourcename>TuneIn Radio</sourcename>
    </source>
  </sources>
</fullAccount>`))
}

// respondGroup antwortet auf /streaming/account/<id>/device/<deviceid>/group/.
// Box prueft ob das Geraet schon in einer Multiroom Gruppe ist. Wir sagen
// "nein, kein Group".
func (s *Server) respondGroup(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>
<errors deviceID="DEVICEID_PLACEHOLDER"><error value="5550" name="GROUP_NO_GROUP_TO_REMOVE_ERROR" severity="Unknown">5550</error></errors>`))
}

// respondProviderSettings antwortet auf /streaming/account/<id>/provider_settings.
// Music Service Provider Settings (Spotify Token, etc). Wir geben leer zurueck.
func (s *Server) respondProviderSettings(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>
<providerSettings/>`))
}

// respondMargeAccountFull liefert ein "konfiguriertes" Marge Account.
// Wenn die Box Account Info erfragt, sagen wir "ja du bist eingeloggt".
func (s *Server) respondMargeAccountFull(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<account status="ACTIVE">
  <uuid>streborn-local-account</uuid>
  <email>local@streborn</email>
  <token>local-token-v1</token>
  <created>2026-01-01T00:00:00Z</created>
</account>`))
}

func (s *Server) respondPresets(w http.ResponseWriter) {
	s.mu.RLock()
	presets := s.presets
	s.mu.RUnlock()

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
		// Bestaetigt der Box dass kein Account konfiguriert ist.
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

// SetAccount setzt den aktuellen Marge Account zur Laufzeit.
func (s *Server) SetAccount(acc *AccountInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.account = acc
}

// SetPresets ueberschreibt die Preset Liste zur Laufzeit.
func (s *Server) SetPresets(p []Preset) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.presets = p
}
