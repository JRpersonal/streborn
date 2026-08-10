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
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
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
	// deviceIDPath persists the device id the box confirmed about itself, so
	// the first account request of the next boot is already answered with it
	// rather than with the interface-MAC guess. See deviceid_persist.go.
	deviceIDPath string
	// groupRestored marks a record restored from NAND that no live signal has
	// confirmed yet (no firmware post, no canonical install this run). A Bose
	// factory reset wipes the firmware's own pairing but not /mnt/nv/streborn,
	// so a restored record can describe a pair that no longer exists; the
	// agent clears it when the firmware reports no group after startup.
	groupRestored bool

	// registered holds the source accounts the box registered through its
	// addSource callback this run (see respondAddSource).
	registered []registeredSource

	// storedMusic holds the DLNA/UPnP media servers the user enabled, published
	// into every account response so the box picks them up on its own poll
	// instead of being pushed to. Seeded at startup from the agent's persisted
	// store; see SetStoredMusicSources.
	storedMusic []registeredSource

	// forward relays the box's cloud traffic to a developer machine when set
	// (see forward.go). Empty = answer locally. Never persisted.
	forward string
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

// SetDeviceID replaces the deviceID used in responses, and reports whether the
// value actually changed.
//
// This has to be mutable, and the reason is worth stating: the id is what the
// firmware looks for in the <devices> block of the account payload, and when it
// does not find ITSELF there it discards the whole payload - including the
// <sources> block that registers the radio source, which is what makes the
// hardware preset keys work. The startup value is a guess derived from a
// network interface MAC, and on a speaker with two interfaces (measured on an
// ST10: SCM 94E3..., SMSC 10CE...) the guess picks the wrong one. The box knows
// its own id and states it twice - in GET /info and in the addDevice POST it
// sends seconds before it asks for the account - so both of those correct us.
func (s *Server) SetDeviceID(id string) bool {
	id = strings.ToUpper(strings.TrimSpace(id))
	if id == "" {
		return false
	}
	s.mu.Lock()
	changed := s.deviceID != id
	s.deviceID = id
	s.mu.Unlock()
	if changed {
		// Only on an actual change: this is a NAND write, and a speaker's
		// standby countdown restarts on every write to it.
		s.persistDeviceID(id)
	}
	return changed
}

// DeviceID returns the id currently used in responses.
func (s *Server) DeviceID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deviceID
}

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
	// After the options, so a previously confirmed id supersedes the MAC guess
	// passed via WithDeviceID, and before the listener binds, so the first
	// request of this boot is already answered with it.
	s.loadDeviceID()
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

// registeredSource is one source account the box registered via its addSource
// callback, kept so the follow-up GET .../sources can list it back.
type registeredSource struct {
	ID, Username, ProviderID, Name, SourceName string
}
