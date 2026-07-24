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
	"encoding/base64"
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
	// box's /addGroup. nil means no pair. Kept in memory only: the box firmware
	// owns the actual pairing across reboots, so on an agent restart the box
	// simply re-creates the record on its next /addGroup or group poll.
	group *groupRecord
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
	case strings.HasPrefix(path, "/streaming/account") && strings.Contains(path, "/provider_settings"):
		s.respondProviderSettings(w, r)
		return
	case strings.HasPrefix(path, "/streaming/account") || strings.HasPrefix(path, "/streaming/auth"):
		s.respondMargeAccountFull(w, r)
		return
	// The BMX services-availability go/no-go: the box gates auto-adding an
	// anonymous-account service (and thus the bmx_token POST that mounts it) on
	// this returning the service with canAdd:true. Must sit before the generic
	// /bmx/registry/ case, which would otherwise swallow it into the services
	// list. Contains() matches both /bmx/registry/v1/servicesAvailability and the
	// ../servicesAvailability href resolution (/bmx/registry/servicesAvailability).
	case strings.Contains(path, "servicesAvailability"):
		s.respondBmxServicesAvailability(w, r)
		return
	case strings.HasPrefix(path, "/bmx/registry/"):
		s.respondBmxRegistry(w, r)
		return
	// Per-source anonymous token mint for the BMX radio services advertised in
	// the registry. The box POSTs baseUrl + _links.bmx_token.href once it sees
	// authenticationModel.anonymousAccount enabled, and mounts the source READY
	// on a valid token. Orion (LOCAL_INTERNET_RADIO) and TuneIn have distinct
	// token shapes. These must sit before the /bmx/ catchall (TuneIn) and the
	// legacy fallback (Orion path is not under /bmx/).
	case strings.Contains(path, "svc-bmx-adapter-orion") && strings.HasSuffix(path, "/token"):
		s.respondOrionToken(w, r)
		return
	// The box GETs the Orion station endpoint when it resolves a stored
	// LOCAL_INTERNET_RADIO preset location (the absolute URL in the preset's
	// ContentItem). It expects a BmxPlaybackResponse whose streamUrl points at
	// STR's own stream proxy. Without this it fell through to the generic <ack/>.
	case strings.Contains(path, "svc-bmx-adapter-orion") && strings.Contains(path, "/station"):
		s.respondOrionStation(w, r)
		return
	case strings.HasPrefix(path, "/bmx/tunein") && strings.HasSuffix(path, "/token"):
		s.respondTuneInToken(w, r)
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
	// The registry is served in TWO shapes at once for compatibility:
	//   - the legacy `services` array (STR's original guess; kept so STSCertified
	//     does not drop the infra services it already accepts), and
	//   - the `bmx_services` array in the exact shape gesellix reverse-engineered
	//     from real Bose cloud captures. The box's BMXController reads the entries
	//     and, for any service whose authenticationModel.anonymousAccount is
	//     enabled+autoCreate, mints a per-source token by POSTing the service's
	//     _links.bmx_token.href against baseUrl - with NO real credential. That is
	//     the missing step that keeps radio sources UNAVAILABLE and forces the
	//     box's own preset activation down the account-gated path that 1036s.
	//     LOCAL_INTERNET_RADIO (Orion "Custom Stations", provider id 11) carries
	//     the anonymous account; its token endpoint is answered by respondOrionToken.
	//     baseUrl points at content.api.bose.io, which the box redirects to this
	//     stub, so the token POST lands here. askAgainAfter is kept short during
	//     the bring-up so the box re-polls quickly after the flag lands.
	const askAgainAfter = 120
	ts := time.Now().Unix()
	_, _ = w.Write([]byte(`{
  "_links": { "bmx_services_availability": { "href": "../servicesAvailability" } },
  "askAgainAfter": ` + fmt.Sprintf("%d", askAgainAfter) + `,
  "ts": ` + fmt.Sprintf("%d", ts) + `,
  "services": [
    {"name": "streaming", "url": "https://streaming.bose.com", "version": "v1.2", "askAgainAfter": ` + fmt.Sprintf("%d", askAgainAfter) + `},
    {"name": "content", "url": "https://content.api.bose.io", "version": "v1", "askAgainAfter": ` + fmt.Sprintf("%d", askAgainAfter) + `},
    {"name": "marge", "url": "https://streaming.bose.com", "version": "v1", "askAgainAfter": ` + fmt.Sprintf("%d", askAgainAfter) + `}
  ],
  "bmx_services": [
    {
      "_links": { "bmx_token": { "href": "/token" }, "bmx_navigate": { "href": "/navigate" }, "self": { "href": "/" } },
      "askAdapter": false,
      "assets": { "color": "#000000", "description": "Custom radio stations.", "name": "Custom Stations" },
      "authenticationModel": { "anonymousAccount": { "autoCreate": true, "enabled": true } },
      "baseUrl": "https://content.api.bose.io/core02/svc-bmx-adapter-orion/prod/orion",
      "id": { "name": "LOCAL_INTERNET_RADIO", "value": 11 },
      "streamTypes": [ "liveRadio" ]
    },
    {
      "_links": { "bmx_token": { "href": "/v1/token" }, "bmx_navigate": { "href": "/v1/navigate" }, "self": { "href": "/" } },
      "askAdapter": false,
      "assets": { "color": "#000000", "description": "TuneIn radio.", "name": "TuneIn" },
      "authenticationModel": { "anonymousAccount": { "autoCreate": true, "enabled": true } },
      "baseUrl": "https://content.api.bose.io/bmx/tunein",
      "id": { "name": "TUNEIN", "value": 25 },
      "streamTypes": [ "liveRadio", "onDemand" ]
    }
  ]
}`))
}

// respondOrionToken answers the per-source token mint that the box POSTs for an
// anonymous-account BMX radio service (LOCAL_INTERNET_RADIO / Orion). The box
// sends no credential; it just wants an access/refresh token back so it can mark
// the source logged-in and mount it READY. Shape matches gesellix HandleOrionToken
// (access_token == refresh_token, empty _embedded.bmx_account, no expiry field).
// Without this the box's token POST fell through to the generic <ack/> and the
// source never mounted.
func (s *Server) respondOrionToken(w http.ResponseWriter, _ *http.Request) {
	const token = "stick-orion-anon-token"
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"_embedded":{"bmx_account":{"displayName":"","username":""}},"access_token":"` + token + `","refresh_token":"` + token + `"}`))
}

// respondTuneInToken answers the TuneIn anonymous token mint. gesellix echoes the
// posted refresh_token; a stable token is sufficient here.
func (s *Server) respondTuneInToken(w http.ResponseWriter, _ *http.Request) {
	const token = "stick-tunein-anon-token"
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"access_token":"` + token + `","refresh_token":"` + token + `"}`))
}

// respondOrionStation resolves a LOCAL_INTERNET_RADIO station the box GETs when
// it follows a stored preset location. The preset location carries the station
// as ?data=<base64(JSON{name,imageUrl,streamUrl})>; STR decodes it and returns a
// BmxPlaybackResponse (gesellix's exact shape) whose audio.streamUrl points at
// STR's continuous ADTS/MP3/AAC stream proxy - NEVER HLS (fw27 cannot parse it).
// This is the cloud-free path gesellix uses: no anonymous account or token mint,
// the box just fetches the streamUrl and plays.
func (s *Server) respondOrionStation(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Name      string `json:"name"`
		ImageURL  string `json:"imageUrl"`
		StreamURL string `json:"streamUrl"`
	}
	if data := r.URL.Query().Get("data"); data != "" {
		raw, err := base64.RawURLEncoding.DecodeString(data)
		if err != nil {
			// Tolerate std base64 too (a client that URL-encoded +/=): the box's
			// own presets use RawURLEncoding, but be liberal in what we accept.
			raw, err = base64.StdEncoding.DecodeString(data)
		}
		if err == nil {
			_ = json.Unmarshal(raw, &p)
		}
	}
	s.logger.Info("orion station playback resolved",
		slog.String("comp", "marge"),
		slog.String("name", p.Name),
		slog.String("streamUrl", p.StreamURL),
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"audio":{"hasPlaylist":true,"isRealtime":true,"streamUrl":%q,"streams":[{"hasPlaylist":true,"isRealtime":true,"streamUrl":%q}]},"imageUrl":%q,"name":%q,"streamType":"liveRadio"}`,
		p.StreamURL, p.StreamURL, p.ImageURL, p.Name)
}

// respondBmxServicesAvailability answers GET /bmx/registry/v1/servicesAvailability.
// This is the missing go/no-go the box waits on after /streaming/sourceproviders:
// it only auto-creates an anonymous account (and POSTs the service's bmx_token
// endpoint that mounts the source READY) for a service listed here with
// canAdd:true. Advertise TUNEIN as addable - it carries the anonymousAccount in
// the registry - which is what unsticks the sm2 ST10 that otherwise polled the
// registry, fetched sourceproviders and stopped. Shape matches gesellix
// bmx_services_availability.json (minus the login-gated SiriusXM entry).
func (s *Server) respondBmxServicesAvailability(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"services":[{"canAdd":true,"canRemove":false,"service":"TUNEIN"},{"canAdd":true,"canRemove":false,"service":"LOCAL_INTERNET_RADIO"}]}`))
}

// respondProviderSettings answers GET /streaming/account/<id>/provider_settings,
// the last leg of the marge streaming handshake (the box fetches it right after
// account/full). Answering it in the gesellix shape instead of letting it fall
// through to the account XML removes a divergence the taigan/scm boxes complete.
func (s *Server) respondProviderSettings(w http.ResponseWriter, r *http.Request) {
	acct := "stick@local"
	if i := strings.Index(r.URL.Path, "/account/"); i >= 0 {
		rest := r.URL.Path[i+len("/account/"):]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			acct = rest[:j]
		} else if rest != "" {
			acct = rest
		}
	}
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	a := xmlEscapeText(acct)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<providerSettings><providerSetting boseID="` + a + `" keyName="ELIGIBLE_FOR_TRIAL" value="false" providerID="14"/><providerSetting boseID="` + a + `" keyName="STREAMING_QUALITY" value="2" providerID="15"/></providerSettings>`))
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
	// Provider ids are DECIMAL (the box binds a source's <sourceproviderid> to
	// these), names symbolic - matching gesellix and the Bose provider-id enum
	// (INTERNET_RADIO=2, LOCAL_INTERNET_RADIO=11, TUNEIN=25, RADIO_BROWSER=39).
	// TUNEIN=25 is the one the account/full source correlates against.
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sourceProviders><sourceprovider id="2"><createdOn>2012-09-19T12:43:00.000+00:00</createdOn><name>INTERNET_RADIO</name><updatedOn>2012-09-19T12:43:00.000+00:00</updatedOn></sourceprovider><sourceprovider id="11"><createdOn>2013-01-10T09:45:00.000+00:00</createdOn><name>LOCAL_INTERNET_RADIO</name><updatedOn>2013-01-10T09:45:00.000+00:00</updatedOn></sourceprovider><sourceprovider id="25"><createdOn>2016-04-08T17:27:21.000+00:00</createdOn><name>TUNEIN</name><updatedOn>2016-04-08T17:27:21.000+00:00</updatedOn></sourceprovider><sourceprovider id="39"><createdOn>2026-03-14T22:47:00.000+00:00</createdOn><name>RADIO_BROWSER</name><updatedOn>2026-03-14T22:47:00.000+00:00</updatedOn></sourceprovider>` + extra.String() + `</sourceProviders>`))
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
	// Declare a TUNEIN source carrying a token credential. The BMX registry now
	// advertises TUNEIN with an anonymous auto-created account, so a source of
	// that type here with a token credential is what makes the box mint the
	// per-source token (against the registry's bmx_token endpoint) and mount the
	// source READY - the missing link that left every radio source UNAVAILABLE
	// and forced preset activation down the account-gated UPNP path (1036). The
	// source type/providerid are aligned with the registry TUNEIN entry.
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>
<fullAccount>
  <mode><text>global</text></mode>
  <sources>
    <source id="TuneInUser" type="TUNEIN">
      <credential type="token">stick-tunein-anon-token</credential>
      <name>TuneIn</name>
      <username>TuneInUser</username>
      <sourceproviderid>TUNEIN</sourceproviderid>
      <sourcename>TuneIn</sourcename>
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
	s.mu.Lock()
	s.group = &g
	s.mu.Unlock()

	roles := make([]string, 0, len(g.Roles))
	for _, role := range g.Roles {
		roles = append(roles, role.Role+"="+role.DeviceID)
	}
	s.logger.Info("marge group created",
		slog.String("comp", "marge"),
		slog.String("groupId", g.ID),
		slog.String("master", g.MasterDeviceID),
		slog.String("roles", strings.Join(roles, ",")),
	)

	status := http.StatusCreated
	if strings.HasSuffix(groupCreateFormat(), "200") {
		status = http.StatusOK
	}
	body = []byte(`<?xml version="1.0" encoding="UTF-8" ?>` + renderGroupXML(&g))
	if strings.HasPrefix(groupCreateFormat(), "wrap") {
		body = []byte(`<?xml version="1.0" encoding="UTF-8" ?><response status="OK">` + renderGroupXML(&g) + `</response>`)
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
	s.mu.Unlock()
	s.logger.Info("marge group deleted",
		slog.String("comp", "marge"), slog.Bool("existed", existed))
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?><response status="OK"/>`))
}

// respondMargeAccountFull returns a "configured" Marge account in the exact
// shape gesellix reverse-engineered from real Bose cloud captures: root
// <account id=..> with accountStatus/mode/preferredLanguage and a <sources>
// list. The TUNEIN <source> carries a decimal <sourceproviderid>25</> (matching
// the sourceproviders catalog) and a non-empty <credential type="token">; that
// pair is what the box binds to mount the source READY (on the marge-only
// chassis - taigan/scm - this account/full source IS the whole lever; the sm2
// ST10 additionally needs the BMX servicesAvailability go-ahead). No <status>
// element is sent: READY is the box's own internal mount state once account +
// token + provider bind together.
func (s *Server) respondMargeAccountFull(w http.ResponseWriter, r *http.Request) {
	acct := "stick@local"
	if i := strings.Index(r.URL.Path, "/account/"); i >= 0 {
		rest := r.URL.Path[i+len("/account/"):]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			acct = rest[:j]
		} else if rest != "" {
			acct = rest
		}
	}
	// The BCO/taigan firmware (Bose_Lisa/27.0.6) mounts its cloud sources ONLY
	// from account/full, and ONLY when it finds its OWN deviceid enumerated in
	// <devices> (the sources are device-scoped; the box adopts them only for the
	// device it paired as). Without the <devices> block the box decodes an empty
	// source registry and mounts nothing -> native preset press 1036s. The sm2
	// path (BMX registry) does not need this, so it is additive. deviceID is the
	// box's own MAC-derived id (set at wiring + refreshed from the AddDevice POST).
	s.mu.RLock()
	dev := s.deviceID
	s.mu.RUnlock()
	devicesBlock := ""
	if dev != "" {
		devicesBlock = `
  <devices>
    <device deviceid="` + xmlEscapeText(dev) + `"><name>SoundTouch</name></device>
  </devices>`
	}
	a := xmlEscapeText(acct)
	w.Header().Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<account id="` + a + `">
  <accountStatus>OK</accountStatus>` + devicesBlock + `
  <mode>global</mode>
  <preferredLanguage>en</preferredLanguage>
  <providerSettings>
    <providerSetting boseID="` + a + `" keyName="ELIGIBLE_FOR_TRIAL" value="false" providerID="14"/>
    <providerSetting boseID="` + a + `" keyName="STREAMING_QUALITY" value="2" providerID="15"/>
  </providerSettings>
  <sources>
    <source id="10004" type="Audio">
      <createdOn>2017-07-20T16:43:48.000+00:00</createdOn>
      <credential type="token">stick-tunein-anon-token</credential>
      <name>TUNEIN</name>
      <sourceproviderid>25</sourceproviderid>
      <sourcename></sourcename>
      <sourceSettings/>
      <updatedOn>2017-07-20T16:43:48.000+00:00</updatedOn>
      <username></username>
    </source>
    <source id="10011" type="Audio">
      <createdOn>2017-07-20T16:43:48.000+00:00</createdOn>
      <credential type="token">stick-orion-anon-token</credential>
      <name>LOCAL_INTERNET_RADIO</name>
      <sourceproviderid>11</sourceproviderid>
      <sourcename></sourcename>
      <sourceSettings/>
      <updatedOn>2017-07-20T16:43:48.000+00:00</updatedOn>
      <username>LOCAL_INTERNET_RADIOUserName</username>
    </source>` + s.reflectedSourcesXML() + `
  </sources>
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
