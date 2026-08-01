// Package marge emulates the Bose Marge server (streaming.bose.com).
// Marge is the internal codename for the Bose cloud server that
// manages presets, account data and multiroom control.
//
// This implementation runs in two modes at the same time:
//
//  1. Spy: every incoming request is recorded in the logs with method, path,
//     headers and body. This lets us learn what the box actually
//     requests once the DNS redirection is in place.
//
//  2. Stub: for the most likely endpoints we return sensible
//     defaults. The responses are constructed so that the box, when in
//     doubt, interprets "all ok, no account, no presets".
package marge

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/JRpersonal/streborn/internal/boxsnapshot"
	"github.com/JRpersonal/streborn/internal/netutil"
)

// Server holds the configuration and the HTTP handler for the Marge emulation.
type Server struct {
	logger   *slog.Logger
	mu       sync.RWMutex
	account  *AccountInfo
	presets  []Preset
	sources  []SourceItem
	deviceID string

	// presetSource, when set, provides the preset list live on every request
	// (wired to the stick preset store). See WithPresetSource.
	presetSource func() []Preset

	// reflectPath points at the reflect-sources file (internal/boxsnapshot).
	// Account-linked cloud sources listed there (e.g. Deezer) are re-advertised
	// to the box in the source-provider + account responses so the box keeps
	// the source and plays it via its own cached token ("Path A"). Empty or
	// missing file = no reflection (the safe default).
	reflectPath string

	// reflectFormatPath points at an optional NAND marker file whose content
	// selects the reflected-source XML shape (see reflectSourceFormat). It lets
	// the Deezer source-revival sweep change the shape with a single file write
	// and a box re-sync, no env var or launch-script edit. Empty = env var only.
	reflectFormatPath string

	// requestLog stores the last N requests for debug purposes
	// (accessible via /__spy/log on the same listener).
	requestLog    []SpyEntry
	requestLogMax int

	// group holds the stereo-pair (L/R) record the ST10 firmware created "on
	// marge" via POST /streaming/account/<acct>/group/, the cloud half of the
	// box's /addGroup. nil means no pair.
	//
	// groupCanonical marks a record installed by STR (the app/agent pairing
	// flow) rather than by the box's own POST. The real Bose cloud held ONE
	// group document per account that both members polled; with one marge per
	// box, each firmware instead re-creates the record from its own point of
	// view, and the RIGHT box then stores a self-centered document naming
	// ITSELF as master/LEFT (live: Rolf's pair, 2026-07-31). While a canonical
	// record is set, firmware posts that disagree on the master are answered
	// with the canonical document instead of being stored, so the pair view
	// can no longer diverge. Persisted to groupPath so it survives an agent
	// restart (the firmware polls the group and must keep getting the same
	// answer, not a "not grouped" fallback).
	group          *groupRecord
	groupCanonical bool
	groupPath      string
	// groupRestored marks a record restored from NAND that no live signal has
	// confirmed yet (no firmware post, no canonical install this run). A Bose
	// factory reset wipes the firmware's own pairing but not /mnt/nv/streborn,
	// so a restored record can describe a pair that no longer exists; the
	// agent clears it when the firmware reports no group after startup.
	groupRestored bool
}

// SpyEntry is a single logged HTTP request.
type SpyEntry struct {
	When    time.Time
	Method  string
	Path    string
	Headers http.Header
	Body    string
}

// Option is a functional option pattern for the configuration.
type Option func(*Server)

// WithDeviceID sets the deviceID used in responses.
func WithDeviceID(id string) Option {
	return func(s *Server) { s.deviceID = id }
}

// WithSpyLogSize sets how many request snapshots are retained.
func WithSpyLogSize(n int) Option {
	return func(s *Server) { s.requestLogMax = n }
}

// WithPresets initializes the preset list.
func WithPresets(p []Preset) Option {
	return func(s *Server) { s.presets = p }
}

// WithPresetSource wires a live preset provider, read fresh on every request.
// This is what the box's post-setMargeAccount re-onboarding consumes: answering
// it with an empty <presets/> made the firmware WIPE its own hardware-key
// preset registrations after every forced re-login (field bundles 2026-07-22:
// "preset reconcile: missing slots on box, syncing missing=5/6" right after
// each "forced re-login sent", users saw "Preset noch nicht festgelegt"). A
// live source keeps the cloud view identical to the stick store without any
// refresh choreography.
func WithPresetSource(fn func() []Preset) Option {
	return func(s *Server) { s.presetSource = fn }
}

// WithSources initializes the source list.
func WithSources(items []SourceItem) Option {
	return func(s *Server) { s.sources = items }
}

// WithReflectSourcesPath wires the reflect-sources file so the box keeps its
// pre-existing account-linked cloud sources (Deezer "Path A").
func WithReflectSourcesPath(path string) Option {
	return func(s *Server) { s.reflectPath = path }
}

// WithReflectSourceFormatPath wires the NAND marker file whose content selects
// the reflected-source XML shape, for the Deezer source-revival sweep.
func WithReflectSourceFormatPath(path string) Option {
	return func(s *Server) { s.reflectFormatPath = path }
}

// WithGroupPath wires the file the stereo-pair group record is persisted to,
// so the record survives an agent restart. Empty keeps the record in memory
// only (tests, dev).
func WithGroupPath(path string) Option {
	return func(s *Server) { s.groupPath = path }
}

// reflected returns the cloud sources to re-advertise to the box, read fresh
// from the reflect-sources file each call (cheap; lets the app's restore action
// add entries without restarting the agent).
func (s *Server) reflected() []boxsnapshot.ReflectSource {
	if s.reflectPath == "" {
		return nil
	}
	return boxsnapshot.LoadReflect(s.reflectPath)
}

// xmlEscapeText escapes text/attribute content for the hand-built XML responses.
func xmlEscapeText(in string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(in)
}

// New creates a new Marge server.
func New(logger *slog.Logger, opts ...Option) *Server {
	s := &Server{
		logger:        logger,
		requestLogMax: 200,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.loadGroup()
	return s
}

// Handler returns the HTTP handler for the Marge endpoints.
//
// We use a catchall handler that sends every request through the spy,
// and behind that a pattern matching on known URL schemes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Diagnostic endpoints. Prefix __ so it does not collide with potential
	// real Marge paths.
	mux.HandleFunc("/__spy/log", s.handleSpyLog)
	mux.HandleFunc("/healthz", s.handleHealthz)

	// Catchall, catches everything else.
	mux.HandleFunc("/", s.handleCatchall)

	return s.spyMiddleware(mux)
}

// Run starts an optional standalone listener (for tests).
// In production Handler() is mounted into the central listener.
// Uses SO_REUSEADDR so test runs can rebind a freshly-released port
// without a TIME_WAIT cooldown.
func (s *Server) Run(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler()}
	ln, err := netutil.ListenTCP(ctx, addr)
	if err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		return srv.Close()
	case err := <-errCh:
		return err
	}
}

// spyMiddleware logs every incoming request before it is passed on to the
// actual handler. The body is buffered so it can be both logged
// and read by the handler.
func (s *Server) spyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Copy the body so downstream can read it.
		var bodyCopy []byte
		if r.Body != nil {
			buf, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
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

		// At debug level so the periodic Bose Lisa polls (every few min)
		// do not flood the log. On errors INFO/WARN is logged in the
		// handler.
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

// recordSpy stores an entry in the ring buffer.
func (s *Server) recordSpy(e SpyEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestLog = append(s.requestLog, e)
	if len(s.requestLog) > s.requestLogMax {
		s.requestLog = s.requestLog[len(s.requestLog)-s.requestLogMax:]
	}
}

// RecentRequests returns a copy of the most recently seen requests.
func (s *Server) RecentRequests() []SpyEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SpyEntry, len(s.requestLog))
	copy(out, s.requestLog)
	return out
}

// RecentRequestLines renders the newest n spy entries as compact one-line
// strings (millisecond timestamps) for the diagnostic bundle. The trail is what
// lets a bundle answer "did the box talk to marge inside THIS 200 ms window?"
// - the question the Wave sysLanguage revert investigation hangs on.
func (s *Server) RecentRequestLines(n int) []string {
	entries := s.RecentRequests()
	if n > 0 && len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, fmt.Sprintf("%s %s %s bodyBytes=%d",
			e.When.Format("2006-01-02T15:04:05.000Z07:00"), e.Method, e.Path, len(e.Body)))
	}
	return out
}

// handleHealthz is the standard probe endpoint.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleSpyLog returns the request log as plain text.
// Intended for debug purposes only, do not expose in production.
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

// handleCatchall responds to everything that is not served by a concrete
// handler. Pattern matching on known path schemes, otherwise a
// generic 200 OK with XML.
func (s *Server) handleCatchall(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// The TuneIn partner subdomain is redirected to 127.0.0.1 in /etc/hosts
	// in case STSCertified ever calls the endpoint. Currently this
	// does not happen in this FW (see internal/marge/tunein.go).
	// If the box does connect there, the request falls into the catchall
	// default with a generic 200 OK <ack/>.

	// Real Bose cloud endpoints from captured traffic
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
	// Stereo-pair group CRUD (#166). During /addGroup the ST10 firmware creates
	// the L/R group record "on marge" via POST /streaming/account/<acct>/group/,
	// polls it via GET /streaming/account/<acct>/device/<dev>/group/, and drops
	// it on /removeGroup. Without a handler the POST fell through to the generic
	// account response below, so the box could not parse a group back and failed
	// with GROUP_CREATE_GROUP_ON_MARGE_ERROR (5580) -> /addGroup HTTP 500. Must
	// sit before the /device and generic /streaming/account cases, since the poll
	// path contains "/device" too.
	case strings.HasPrefix(path, "/streaming/account/") && strings.Contains(path, "/group"):
		s.handleMargeGroup(w, r)
		return
	// AddDevice sync: /streaming/account/<accountId>/device/ POST
	// The box calls this after POST /setMargeAccount on the box itself.
	// The response must be adddeviceresponse XML with a margetoken element.
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

	// Fallback pattern matching (legacy)
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
		// Generic 200 OK so the box does not go into retry loops.
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ack/>`))
	}
}

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
<sourceProviders><sourceprovider id="TUNEIN"><name>TuneIn Radio</name></sourceprovider><sourceprovider id="INTERNET_RADIO"><name>Internet Radio</name></sourceprovider><sourceprovider id="STORED_MUSIC"><name>Stored Music</name></sourceprovider>` + extra.String() + `</sourceProviders>`))
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

// groupRole is one <groupRole> entry inside a stereo-pair group descriptor.
type groupRole struct {
	DeviceID string `xml:"deviceId"`
	Role     string `xml:"role"`
	IP       string `xml:"ipAddress"`
}

// groupRecord mirrors the <group> descriptor the ST10 firmware POSTs to marge
// to create the L/R stereo pair, and the shape the box's own /getGroup returns:
// id as an attribute, name/masterDeviceId as child elements, and the members as
// <roles><groupRole>. Live captured 2026-07-10 from EC24B8B790CC.
type groupRecord struct {
	XMLName        xml.Name    `xml:"group"`
	ID             string      `xml:"id,attr"`
	Name           string      `xml:"name"`
	MasterDeviceID string      `xml:"masterDeviceId"`
	Roles          []groupRole `xml:"roles>groupRole"`
}

// groupCreateFormat selects the shape of the group-create acknowledgement, so
// the response the firmware accepts can be swept on hardware the same way
// addDeviceFormat sweeps the AddDevice reply. Values: "bare201" (default: HTTP
// 201 Created + a bare <group id=...>), "bare200", "wrap201"/"wrap200" (the
// <response status="OK"> envelope the AddDevice path uses). Empty falls back to
// the default.
func groupCreateFormat() string {
	if v := strings.TrimSpace(os.Getenv("STICK_GROUP_CREATE_FORMAT")); v != "" {
		return v
	}
	return "bare201"
}

// margeGroupID derives a stable, non-empty group id from the master device id
// so a create and the follow-up poll echo the same id. The box treats the
// marge group id as opaque (its own /getGroup returns a firmware-assigned id).
func margeGroupID(master string) string {
	m := strings.TrimSpace(master)
	if m == "" {
		m = "stereo"
	}
	return "str-grp-" + m
}

// renderGroupXML renders a group record in the <group id=...> shape the box's
// /getGroup parses, echoing the posted roles back (with ipAddress only when the
// firmware supplied one).
func renderGroupXML(g *groupRecord) string {
	var b strings.Builder
	b.WriteString(`<group id="`)
	b.WriteString(xmlEscapeText(g.ID))
	b.WriteString(`"><name>`)
	b.WriteString(xmlEscapeText(g.Name))
	b.WriteString(`</name><masterDeviceId>`)
	b.WriteString(xmlEscapeText(g.MasterDeviceID))
	b.WriteString(`</masterDeviceId><roles>`)
	for _, role := range g.Roles {
		b.WriteString(`<groupRole><deviceId>`)
		b.WriteString(xmlEscapeText(role.DeviceID))
		b.WriteString(`</deviceId><role>`)
		b.WriteString(xmlEscapeText(role.Role))
		b.WriteString(`</role>`)
		if strings.TrimSpace(role.IP) != "" {
			b.WriteString(`<ipAddress>`)
			b.WriteString(xmlEscapeText(role.IP))
			b.WriteString(`</ipAddress>`)
		}
		b.WriteString(`</groupRole>`)
	}
	b.WriteString(`</roles></group>`)
	return b.String()
}

// persistedGroup is the on-NAND shape of the stored stereo-pair record: the
// group document itself plus whether it is canonical (STR-installed) rather
// than a firmware self-report.
type persistedGroup struct {
	Canonical bool   `json:"canonical"`
	XML       string `json:"xml"`
}

// loadGroup restores the persisted group record at startup. Best-effort: a
// missing or unreadable file simply means no pair.
func (s *Server) loadGroup() {
	if s.groupPath == "" {
		return
	}
	data, err := os.ReadFile(s.groupPath)
	if err != nil {
		return
	}
	var pg persistedGroup
	if err := json.Unmarshal(data, &pg); err != nil {
		s.logger.Warn("marge group: persisted record unreadable, ignoring",
			slog.String("comp", "marge"), slog.String("err", err.Error()))
		return
	}
	var g groupRecord
	if err := xml.Unmarshal([]byte(pg.XML), &g); err != nil {
		s.logger.Warn("marge group: persisted record XML unreadable, ignoring",
			slog.String("comp", "marge"), slog.String("err", err.Error()))
		return
	}
	s.mu.Lock()
	s.group = &g
	s.groupCanonical = pg.Canonical
	s.groupRestored = true
	s.mu.Unlock()
	s.logger.Info("marge group: restored persisted record",
		slog.String("comp", "marge"), slog.String("groupId", g.ID),
		slog.String("master", g.MasterDeviceID), slog.Bool("canonical", pg.Canonical))
}

// persistGroupLocked writes (or removes) the on-NAND copy of the current
// record. Callers hold s.mu. Best-effort: a write failure only costs the
// record an agent restart, so it is logged and swallowed.
func (s *Server) persistGroupLocked() {
	if s.groupPath == "" {
		return
	}
	if s.group == nil {
		if err := os.Remove(s.groupPath); err != nil && !os.IsNotExist(err) {
			s.logger.Warn("marge group: could not remove persisted record",
				slog.String("comp", "marge"), slog.String("err", err.Error()))
		}
		return
	}
	pg := persistedGroup{Canonical: s.groupCanonical, XML: renderGroupXML(s.group)}
	data, err := json.Marshal(pg)
	if err != nil {
		return
	}
	if err := os.WriteFile(s.groupPath, data, 0o644); err != nil {
		s.logger.Warn("marge group: persist failed",
			slog.String("comp", "marge"), slog.String("err", err.Error()))
	}
}

// GroupSnapshot returns the current group document and whether it is the
// canonical (STR-installed) record. ok is false when no pair is stored.
func (s *Server) GroupSnapshot() (xmlDoc string, canonical bool, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.group == nil {
		return "", false, false
	}
	return renderGroupXML(s.group), s.groupCanonical, true
}

// SetCanonicalGroup installs the canonical pair document (from the pairing
// flow on this box, or relayed from the master's agent via the desktop app for
// the partner). From now on firmware posts that disagree on the master are
// answered with this document instead of stored (see createMargeGroup).
func (s *Server) SetCanonicalGroup(xmlDoc string) error {
	var g groupRecord
	if err := xml.Unmarshal([]byte(xmlDoc), &g); err != nil {
		return fmt.Errorf("parse group document: %w", err)
	}
	if strings.TrimSpace(g.MasterDeviceID) == "" || len(g.Roles) != 2 {
		return fmt.Errorf("group document needs a masterDeviceId and exactly two roles (got master=%q roles=%d)", g.MasterDeviceID, len(g.Roles))
	}
	if strings.TrimSpace(g.ID) == "" {
		g.ID = margeGroupID(g.MasterDeviceID)
	}
	s.mu.Lock()
	s.group = &g
	s.groupCanonical = true
	s.groupRestored = false
	s.persistGroupLocked()
	s.mu.Unlock()
	s.logger.Info("marge group: canonical pair document installed",
		slog.String("comp", "marge"), slog.String("groupId", g.ID),
		slog.String("master", g.MasterDeviceID))
	return nil
}

// ClearGroup drops the stored pair record (dissolve, from this box's own flow
// or relayed for the partner). No-op when nothing is stored.
func (s *Server) ClearGroup(reason string) {
	s.mu.Lock()
	existed := s.group != nil
	s.group = nil
	s.groupCanonical = false
	s.groupRestored = false
	s.persistGroupLocked()
	s.mu.Unlock()
	if existed {
		s.logger.Info("marge group: cleared", slog.String("comp", "marge"), slog.String("reason", reason))
	}
}

// GroupRestoredUnconfirmed reports whether the stored record came from NAND
// and no live signal (firmware post, canonical install) has confirmed it this
// run. The agent's post-startup check clears such a record when the firmware
// reports no group: a Bose factory reset wipes the box's own pairing but not
// /mnt/nv/streborn, and a phantom record must not keep answering the group
// poll with a pair that no longer exists.
func (s *Server) GroupRestoredUnconfirmed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.group != nil && s.groupRestored
}

// CanonicalGroupXML renders the canonical stereo-pair document the pairing
// flow installs on BOTH members' marges: master = LEFT, partner = RIGHT, the
// group id derived from the master so every copy agrees.
func CanonicalGroupXML(name, masterID, masterIP, partnerID, partnerIP string) string {
	g := &groupRecord{
		ID:             margeGroupID(masterID),
		Name:           name,
		MasterDeviceID: masterID,
		Roles: []groupRole{
			{DeviceID: masterID, Role: "LEFT", IP: masterIP},
			{DeviceID: partnerID, Role: "RIGHT", IP: partnerIP},
		},
	}
	return renderGroupXML(g)
}

// handleMargeGroup dispatches the stereo-pair group CRUD the firmware runs
// against marge as the cloud half of /addGroup and /removeGroup.
func (s *Server) handleMargeGroup(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost, http.MethodPut:
		s.createMargeGroup(w, r)
	case http.MethodDelete:
		s.deleteMargeGroup(w, r)
	default: // GET/HEAD: the box's "is this device in a group?" poll.
		s.readMargeGroup(w, r)
	}
}

// createMargeGroup answers the firmware's "create this group on marge" POST.
// It stores the record and echoes it back with a server-assigned id, which is
// what unblocks the box's /addGroup (previously HTTP 500 / error 5580).
func (s *Server) createMargeGroup(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var g groupRecord
	if err := xml.Unmarshal(body, &g); err != nil {
		s.logger.Warn("marge group create: could not parse body",
			slog.String("comp", "marge"), slog.String("err", err.Error()))
	}
	if strings.TrimSpace(g.ID) == "" {
		g.ID = margeGroupID(g.MasterDeviceID)
	}
	stored := &g
	s.mu.Lock()
	// While a canonical pair document is installed, NO firmware post replaces
	// it — the record only changes via SetCanonicalGroup/ClearGroup. A post
	// naming a DIFFERENT master is the known self-centered re-create of the
	// RIGHT box (each firmware reports the pair from its own point of view;
	// the real Bose cloud had one shared document); an agreeing post must not
	// replace the record either, or the firmware's own shape would silently
	// become "canonical". Echo the canonical document back in both cases so
	// the firmware adopts the shared view.
	// Any firmware post is live proof a pair still exists on the box side, so
	// a restored record is confirmed either way.
	s.groupRestored = false
	if s.groupCanonical && s.group != nil {
		stored = s.group
		selfCentered := !strings.EqualFold(strings.TrimSpace(g.MasterDeviceID), strings.TrimSpace(stored.MasterDeviceID))
		s.mu.Unlock()
		if selfCentered {
			s.logger.Warn("marge group create: firmware posted a self-centered pair document, answering with the canonical one",
				slog.String("comp", "marge"),
				slog.String("postedMaster", g.MasterDeviceID),
				slog.String("canonicalMaster", stored.MasterDeviceID))
		} else {
			s.logger.Info("marge group create: firmware re-created the pair, keeping the canonical document",
				slog.String("comp", "marge"), slog.String("master", stored.MasterDeviceID))
		}
	} else {
		s.group = stored
		s.persistGroupLocked()
		s.mu.Unlock()
	}

	roles := make([]string, 0, len(stored.Roles))
	for _, role := range stored.Roles {
		roles = append(roles, role.Role+"="+role.DeviceID)
	}
	s.logger.Info("marge group created",
		slog.String("comp", "marge"),
		slog.String("groupId", stored.ID),
		slog.String("master", stored.MasterDeviceID),
		slog.String("roles", strings.Join(roles, ",")),
	)

	status := http.StatusCreated
	if strings.HasSuffix(groupCreateFormat(), "200") {
		status = http.StatusOK
	}
	body = []byte(`<?xml version="1.0" encoding="UTF-8" ?>` + renderGroupXML(stored))
	if strings.HasPrefix(groupCreateFormat(), "wrap") {
		body = []byte(`<?xml version="1.0" encoding="UTF-8" ?><response status="OK">` + renderGroupXML(stored) + `</response>`)
	}
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// readMargeGroup answers the periodic group poll. When a pair exists we return
// it so the box keeps the pair; otherwise we preserve the historical standalone
// behaviour (the box tolerates the account response as "not grouped").
func (s *Server) readMargeGroup(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	g := s.group
	s.mu.RUnlock()
	if g == nil {
		s.respondMargeAccountFull(w, r)
		return
	}
	s.logger.Debug("marge group poll answered from store",
		slog.String("comp", "marge"), slog.String("groupId", g.ID))
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>` + renderGroupXML(g)))
}

// deleteMargeGroup drops the stored pair when the box dissolves it (/removeGroup
// -> the firmware's group DELETE on marge).
func (s *Server) deleteMargeGroup(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	existed := s.group != nil
	s.group = nil
	s.groupCanonical = false
	s.persistGroupLocked()
	s.mu.Unlock()
	s.logger.Info("marge group deleted",
		slog.String("comp", "marge"), slog.Bool("existed", existed))
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?><response status="OK"/>`))
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

// SetAccount sets the current Marge account at runtime.
func (s *Server) SetAccount(acc *AccountInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.account = acc
}

// SetPresets overwrites the preset list at runtime.
func (s *Server) SetPresets(p []Preset) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.presets = p
}
