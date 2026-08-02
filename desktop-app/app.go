// STR Desktop App: finds all sticks on the LAN via mDNS, lists them
// and controls them via REST API. Wails app, backend is Go, frontend HTML/JS.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"streborn-app/agentbin"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/sticksetup"
)

// App is the central state struct.
type App struct {
	ctx        context.Context
	logger     *slog.Logger
	logFile    *os.File // kept so ExportDiagnosticLogs can Sync before reading
	httpClient *http.Client

	// probeSTRFn/portOpenFn are the network probes RefreshKnownBoxes feeds
	// into classifyKnownBox (probeSTR and portOpen when nil). Injectable so
	// tests can assert the live/offline eviction contract without sockets:
	// the pre-98883aa regression (an offline box merged back into the cache
	// every cycle, so it never expired) was invisible to tests exactly
	// because these calls were hard-wired.
	probeSTRFn func(ctx context.Context, host string) (BoxInfo, bool)
	portOpenFn func(host string, port int, timeoutMs int) bool

	// Install progress: the current network-install phase and start time, read by
	// the shared waitForSSHOpen/waitForAgent heartbeat loops so they emit
	// phase-labeled, elapsed-stamped install:progress events through the long
	// silent stretches (the 3-5 min :17000 unlock, the up-to-240s agent wait).
	// installMargeHits, when non-nil, returns the factory-reset bootstrap's
	// marge-callback count so the UI can flag a firewall block (0 callbacks) instead
	// of dying silently. Single-flight in practice (the frontend
	// networkInstallRunning guard serialises installs).
	installPhase     string
	installStart     time.Time
	installMargeHits func() int64

	// portCache maps a box host to the agent port last seen answering it.
	// BCO boxes (Portable/taigan, ST20-spotty) expose the agent only on
	// the redirected :17008, classic boxes answer :8888 directly, and mDNS
	// announces :8888 either way, so a box record can carry the wrong
	// port. boxDo tries the cached/known port, falls back to the other on
	// any transport failure, and caches whichever connects. This is
	// self-healing: if the box froze and the app got pinned to a port that
	// no longer answers (observed: a freeze made :17008 time out, discovery
	// fell back to the announced :8888 and never retried :17008), the next
	// call simply fails over and re-pins to the working port.
	portMu    sync.Mutex
	portCache map[string]int

	// libraryServers caches the result of the most recent
	// ListMediaServers call so subsequent BrowseLibrary calls can
	// resolve a UDN to a Server without a fresh SSDP sweep on every
	// folder click. Cleared and rebuilt on ListMediaServers.
	libraryMu      sync.Mutex
	libraryServers map[string]dlna.Server

	// userLocale is the active UI language (BCP-47, e.g. "de"/"en")
	// reported by the frontend via SetAppLocale. Server-side
	// provisioning paths that set the box display language (the
	// Setup-AP push) map it to a Bose sysLanguage so we never force a
	// hardcoded language on the user. Guarded because Wails dispatches
	// method calls from arbitrary goroutines.
	localeMu   sync.RWMutex
	userLocale string

	// discCache keeps recently-discovered boxes so a single missed mDNS
	// or TCP cycle does not make a box flicker out of the list and back.
	// mDNS multicast drops, a box mid-reboot, or marginal Wi-Fi (all
	// observed live on a spotty ST20, #90) otherwise cause the box
	// to vanish and radio/presets to fail with "Failed to fetch" until
	// the next cycle re-finds it. See mergeDiscoveryCache.
	discMu    sync.Mutex
	discCache map[string]discEntry
	// knownSpeakersWritten fingerprints the last-persisted known-speakers
	// set so unchanged discovery cycles skip the file write.
	knownSpeakersMu      sync.Mutex
	knownSpeakersWritten string

	// otaPinned maps a speaker IP to the time STR last initiated an agent OTA
	// on it. During the post-OTA reboot the agent is down while the box's stock
	// Bose port still answers, so discovery would briefly reclassify the box as
	// stock and offer a USB reinstall (#108). Because STR itself triggered the
	// update, it KNOWS that IP runs STR: while the pin is fresh, discovery forces
	// the box to stay classified as STR regardless of what the half-booted box
	// reports. Guarded by discMu (same lock as discCache, always held together).
	otaPinned map[string]time.Time

	// strKnown remembers every box we have positively confirmed as running STR,
	// keyed by its stable Bose deviceID (not its IP). The IP-keyed discCache and
	// otaPinned above cannot protect a box across a DHCP change: a runtime Wi-Fi
	// change, and especially a 2.4<->5 GHz band switch, can move the speaker to a
	// new lease, so it reappears under a brand-new discovery key with no STR
	// history. On a LAN where mDNS is dead (many Fritz!Box setups announce
	// nothing for _streborn._tcp; observed live 2026-07-05 with instancesFromMDNS=0
	// every cycle) the /24 sweep then sees only the box's stock :8090 before its
	// agent is reachable again, and Settings tells the user to reinstall a speaker
	// that never lost STR. deviceID survives the IP change, so a stock sighting of
	// a device we recently knew as STR is relabelled STR. Guarded by discMu.
	strKnown map[string]discEntry

	// logoCache memoises resolved station-logo URLs (ResolveStationLogo)
	// so the same station is validated against DuckDuckGo at most once per
	// app run. Value "" means "no real logo, draw a monogram".
	logoMu    sync.Mutex
	logoCache map[string]string
}

// NewApp creates a new App instance.
func NewApp() *App {
	// Log level: INFO by default; set STR_DEBUG=1 in the environment (dev/support
	// sessions) to get DEBUG detail without shipping verbose logs in releases.
	logLevel := slog.LevelInfo
	if os.Getenv("STR_DEBUG") != "" {
		logLevel = slog.LevelDebug
	}
	logger, logFile := newFileLogger(logLevel)
	return &App{
		logger:         logger,
		logFile:        logFile,
		httpClient:     &http.Client{Timeout: 6 * time.Second},
		libraryServers: map[string]dlna.Server{},
	}
}

// appCtx returns the Wails runtime context, or context.Background() before
// startup has set it. A bound App method can run before startup (Wails dispatches
// from arbitrary goroutines), and context.WithTimeout panics on a nil parent, so
// every timeout/request that parents on a.ctx must go through here.
func (a *App) appCtx() context.Context {
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Clear any leftover "<exe>.old" from a previous self-update (#71).
	a.cleanupOldBinary()
	// Route the dlna package's logs through our file logger so the
	// per-interface SSDP M-SEARCH summary lines land in str.log next
	// to the STR discovery cycles. Without this, a media server scan
	// that returns zero results is indistinguishable from "no servers
	// on the LAN" in the diagnostic bundle.
	dlna.Logger = a.logger.With("comp", "dlna")
	// Passive SSDP NOTIFY listener, running for the whole app lifetime.
	// A media server on THIS PC (e.g. Windows Media Player sharing)
	// announces itself via multicast NOTIFY but does not answer
	// same-host M-SEARCH (on Windows, unicast to the shared :1900 is
	// swallowed by the SSDP Discovery service), so without this the
	// Library tab never finds it (#341). DiscoverServers merges what
	// the listener has heard into every scan.
	dlna.StartAnnounceListener(ctx)
	// Same for sticksetup, so USB-stick discovery timing (a slow search
	// while Windows finishes mounting a freshly inserted stick) is visible
	// in the diagnostic bundle instead of an unexplained UI hang.
	sticksetup.Logger = a.logger.With("comp", "sticksetup")
	// And for the SSH client cache, so its one-time slow-handshake WARN (a
	// router that drops the sshd's reverse-DNS queries taxes every new
	// connection) lands in the diagnostic bundle next to the install steps
	// it explains.
	boxSSHClients.setLogger(a.logger.With("comp", "boxssh"))
	// Verbose startup line so users always see SOMETHING in the
	// log when they hit "Save diagnostic logs", even on a session
	// where they did not poke any features that emit further logs.
	a.logger.Info("Desktop App started",
		"version", appVersion,
		"build", appBuild,
		"logFile", LogFilePath(),
		"agentbinAvailable", agentbin.Available())
}

// LogClientError records an error the frontend caught (a global
// window onerror or an unhandledrejection) into str.log. Frontend
// JavaScript crashes do not otherwise reach the file logger, so
// without this a startup "flashes up and quits" leaves no trace to
// diagnose. Best-effort, never throws back into JS.
func (a *App) LogClientError(msg string) {
	if a.logger != nil {
		a.logger.Error("frontend error", "detail", msg)
	}
}
