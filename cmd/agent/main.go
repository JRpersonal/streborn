// Command streborn ist der Agent, der direkt auf der Bose SoundTouch
// Box läuft und die Bose Cloud Endpunkte lokal emuliert.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/JRpersonal/streborn/discovery"
	"github.com/JRpersonal/streborn/internal/autopair"
	"github.com/JRpersonal/streborn/internal/bmx"
	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/boxws"
	"github.com/JRpersonal/streborn/internal/hosts"
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/netutil"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/shepherd"
	"github.com/JRpersonal/streborn/internal/spotify"
	"github.com/JRpersonal/streborn/internal/streamproxy"
	"github.com/JRpersonal/streborn/internal/sysinfo"
	"github.com/JRpersonal/streborn/internal/tlsgen"
	"github.com/JRpersonal/streborn/internal/upnp"
	"github.com/JRpersonal/streborn/internal/webui"
	usbstick "github.com/JRpersonal/streborn/usb-stick"
)

// version ist die Semver Version. Build Datum wird separat ueber -ldflags
// gesetzt damit man "1.0.0" anzeigen kann und das Build Datum trotzdem
// verfuegbar ist.
var (
	version    = "1.0.0"
	buildStamp = "dev"
)

func init() {
	webui.SetAgentVersion(version)
	webui.SetAgentBuild(buildStamp)
}

func main() {
	// Subcommands vor flag.Parse() abhandeln, damit ihre eigenen Flags
	// nicht vom globalen flag Set verschluckt werden.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "shepherd":
			if err := runShepherdCmd(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			return
		}
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runShepherdCmd haendlet das shepherd Subcommand.
// Aufrufe:
//
//	streborn shepherd install   -- /mnt/nv/shepherd aufsetzen
//	streborn shepherd remove    -- /mnt/nv/shepherd entfernen
//	streborn shepherd status    -- aktuellen Stand zeigen
func runShepherdCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("verwendung: shepherd {install|remove|status}")
	}

	fs := flag.NewFlagSet("shepherd", flag.ContinueOnError)
	shepherdDir := fs.String("dir", shepherd.DefaultShepherdDir, "Shepherd Override Verzeichnis")
	boseDir := fs.String("bose-config", shepherd.DefaultBoseConfigDir, "Bose Config Verzeichnis")
	bin := fs.String("binary", shepherd.DefaultStickBin, "Pfad zum Agent Binary")
	presetsPath := fs.String("presets", shepherd.DefaultPresetsPath, "Pfad zur presets.json")

	cmd := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	logger := newLogger("info")
	mgr := shepherd.New(shepherd.Config{
		ShepherdDir:   *shepherdDir,
		BoseConfigDir: *boseDir,
		AgentBinary:   *bin,
		PresetsPath:   *presetsPath,
	}, logger)

	switch cmd {
	case "install":
		return mgr.Install()
	case "remove", "uninstall":
		return mgr.Uninstall()
	case "status":
		st, err := mgr.Check()
		if err != nil {
			return err
		}
		fmt.Printf("ShepherdDir   : %s\n", *shepherdDir)
		fmt.Printf("DirExists     : %v\n", st.DirExists)
		fmt.Printf("HasOwnConfig  : %v\n", st.HasOwnConfig)
		fmt.Printf("Missing       : %v\n", st.MissingSymlinks)
		fmt.Printf("Broken        : %v\n", st.BrokenSymlinks)
		fmt.Printf("Healthy       : %v\n", st.IsHealthy())
		return nil
	default:
		return fmt.Errorf("unbekanntes Subcommand: %s", cmd)
	}
}

func run() error {
	var (
		presetsPath     = flag.String("presets", "/media/sda1/presets.json", "Pfad zur presets.json auf dem USB Stick")
		webuiAddr       = flag.String("listen-webui", ":8888", "Adresse für das Config Web UI")
		margeAddr       = flag.String("listen-marge", ":80", "Adresse für die Marge Emulation HTTP (streaming.bose.com)")
		margeTLSAddr    = flag.String("listen-marge-tls", ":8443", "Adresse für die Marge Emulation HTTPS")
		bmxAddr         = flag.String("listen-bmx", ":81", "Adresse für die BMX Emulation HTTP (content.api.bose.io)")
		hostsPath       = flag.String("hosts", "/etc/hosts", "Pfad zur hosts Datei")
		applyHosts      = flag.Bool("apply-hosts", true, "Hosts Datei beim Start manipulieren und beim Stop wiederherstellen")
		tlsDir          = flag.String("tls-dir", tlsgen.DefaultCADir, "Verzeichnis fuer CA und Server Cert")
		tlsEnabled      = flag.Bool("tls", true, "TLS Termination aktivieren auf listen-marge-tls")
		logLevel        = flag.String("log-level", "info", "Log Level: debug, info, warn, error")
		boxHost         = flag.String("box-host", "127.0.0.1", "Bose Box IP fuer UPnP Calls (Webui /api/play). 127.0.0.1 wenn Agent auf Box laeuft, sonst LAN IP.")
		regionFile      = flag.String("region-file", "", "Pfad zur region.txt mit ISO Country Code (vom Setup Wizard). Default Radio Land und Sprache leiten wir daraus ab.")
		pendingNameFile = flag.String("pending-name-file", "", "Pfad zur name.txt vom Setup Wizard. Inhalt wird einmalig als Box Name angewendet (plus UID Suffix) und die Datei danach geloescht.")
		printVersion    = flag.Bool("version", false, "Version ausgeben und beenden")
	)
	flag.Parse()

	if *printVersion {
		fmt.Println(version)
		return nil
	}

	logger := newLogger(*logLevel)
	logger.Info("streborn starting", "version", version)

	// Self-heal the bootstrap layer if the agent OTA brought a newer
	// binary onto a box whose run.sh / rc.local still date from an
	// older release. Without this, an HTTP- or SSH-OTA only refreshes
	// the agent binary; the on-NAND run.sh and rc.local stay at
	// whatever vintage the last stick install wrote, and the resulting
	// mix-of-versions produces silent feature gaps (shim path missing,
	// WLAN-creds not persisted, sysLanguage gate POSTed at 0, etc.).
	// Live-verified on a scm/spotty ST20 on 2026-05-30: an SSH-OTA to
	// the v0.5.23 agent left the v2 (15.05.2026) run-override.sh in
	// place because nothing replaced it. The agent embeds the matching
	// run.sh and rc.local via usbstick.Files() and writes them out on
	// startup whenever the disk copies differ from the embedded ones.
	// Best-effort: any write failure is logged and the agent continues.
	if syncBootstrapFromEmbedded(logger) {
		// The on-NAND boot path (run-override.sh / rc.local) was stale
		// relative to this binary and has just been refreshed. Those
		// scripts only take effect on the NEXT boot, so the rest of THIS
		// boot would still run the old WLAN / shim / gate logic. Rather
		// than leave the box one manual power-cycle short of a clean
		// state, reboot ourselves once (guarded against loops) so the
		// very next boot runs the boot path that matches this binary.
		maybeRebootAfterBootstrapSync(logger)
	}

	// Keep the on-box version.txt in lockstep with the running binary.
	// The desktop reads version.txt (via the stick / SSH diagnostic
	// fallback) to display a box's version, but only stick-prep ever
	// wrote it, never the OTA path, so after an agent OTA the box kept
	// reporting the pre-update version (#94). Stamping it here means any
	// update path (HTTP-OTA, SSH-OTA, manual) is reflected the moment the
	// new binary boots. Best-effort.
	stampVersionFiles(logger)

	ensureSshdRunning(logger)

	// DeviceID aus MAC ermitteln, damit Marge Antworten die echte Box ID
	// zurueckgeben. Wenn keine MAC gefunden wird, weiter mit leerer ID.
	deviceID, err := sysinfo.DeviceID(nil)
	if err != nil {
		logger.Warn("could not determine DeviceID", "err", err)
		deviceID = ""
	} else {
		logger.Info("DeviceID detected", "deviceID", deviceID)
	}

	// Presets laden. Bei Fehler nicht crashen sondern mit leerer Liste weitermachen,
	// damit der Agent zumindest auf seinen Listenern lebt und korrigierbar bleibt.
	//
	// Phase-marker logs at WARN level so a remote diagnostic bundle shows
	// exactly which path was taken — was the file there? was it empty?
	// did parse succeed? how many slots ended up in the in-memory store?
	// Without this, an "empty presets" report (#60) is indistinguishable
	// from a fresh install, a corrupt file, or an agent restart racing
	// the store load.
	if st, statErr := os.Stat(*presetsPath); statErr == nil {
		logger.Warn("preset store phase: file present",
			"file", *presetsPath, "bytes", st.Size(), "mtime", st.ModTime().UTC().Format(time.RFC3339))
	} else if os.IsNotExist(statErr) {
		logger.Warn("preset store phase: file absent", "file", *presetsPath)
	} else {
		logger.Warn("preset store phase: file stat failed", "file", *presetsPath, "err", statErr)
	}
	store, err := presets.Load(*presetsPath)
	if err != nil {
		logger.Warn("preset store phase: load failed, continuing with empty list",
			"err", err, "file", *presetsPath)
		store = presets.New()
	} else {
		logger.Warn("preset store phase: ready",
			"count", len(store.All()), "file", *presetsPath)
	}

	// Hosts Datei manipulieren
	var hostsMgr *hosts.Manager
	if *applyHosts {
		hostsMgr = hosts.New(*hostsPath, logger)
		if err := hostsMgr.Apply(hosts.DefaultEntries()); err != nil {
			logger.Warn("hosts file could not be modified", "err", err)
			hostsMgr = nil
		}
	}

	// Subsysteme initialisieren
	margeSrv := marge.New(logger.With("comp", "marge"),
		marge.WithDeviceID(deviceID))
	bmxSrv := bmx.New(logger.With("comp", "bmx"))
	// AutoPair Manager wird oben angelegt damit er auch im WS und Webui
	// Handler genutzt werden kann.
	autoPair := autopair.New(logger.With("comp", "autopair"), autopair.Config{
		BoxHost: *boxHost,
	})

	// Initial Preset Sync zur Box im Hintergrund. Box muss alle Presets
	// als UPNP ContentItems kennen damit Hardware Tasten den
	// nowSelectionUpdated WebSocket Event mit Location ausloesen koennen.
	// Plus periodischer Reconciler (alle 5 min) damit Inkonsistenzen
	// die durch Box Reboot oder Bose State Resets entstehen, automatisch
	// geheilt werden — der User braucht den "Hardware Tasten reparieren"
	// Button im Normalfall nie zu druecken.
	go initialBoxPresetSync(store, *boxHost, logger)
	go periodicPresetReconcile(store, *boxHost, logger)

	// Region beim Start aus Datei lesen (vom Setup Wizard provisioniert).
	region := loadRegion(*regionFile, logger)

	// Stream Proxy macht Bose ContentItems gegen Token Expiry resistent:
	// statt der echten CDN URL bekommt Bose http://127.0.0.1:8888/stream/<slot>
	// und der Stick Agent reconnectet intern bei Drops.
	streamProxySrv := streamproxy.New(store, logger.With("comp", "streamproxy"))

	// Spotify preset audio plane (#78, P1): the agent supervises
	// go-librespot and serves its live audio (PCM wrapped as a WAV
	// stream) at /spotify/stream so the box plays it over UPnP. A Spotify
	// preset press calls go-librespot's local play API (no token plane)
	// and points the box at /spotify/stream. Idles until the binary is
	// present and the device is tap-authenticated once in the Spotify
	// app. Started below once ctx exists.
	// go-librespot reads the speaker's friendly name and volume through this
	// Bose REST client: the Spotify device + its mDNS advert then carry the
	// speaker's own name, and Spotify-app volume changes are mirrored onto it.
	spotifyBox := boxapi.New(*boxHost)
	spotifyMgr := spotify.New("/mnt/nv/streborn/bin/go-librespot", "/mnt/nv/streborn/sp-cache", "ST Reborn", spotifyBox, logger.With("comp", "spotify"))

	webuiSrv := webui.New(*webuiAddr, logger.With("comp", "webui"),
		webui.WithPresets(store),
		webui.WithBoxHost(*boxHost),
		webui.WithAutoPair(autoPair),
		webui.WithRegion(region),
		webui.WithRegionFile(*regionFile),
		webui.WithStreamProxy(streamProxySrv),
		webui.WithSpotifyStream(spotifyMgr.ServeOgg),
		webui.WithSpotifyControl(spotifyMgr.Play),
		webui.WithSpotifyInfo(spotifyMgr.ServeInfo))

	// Auto-re-push (#4): when the Bose renderer drops a proxied stream on its
	// own (reported: radio stops after ~11 min with no upstream error), the
	// webui resumes it conservatively (only if the box stays on and idle).
	streamProxySrv.SetOnDisconnect(webuiSrv.HandleStreamDisconnect)

	// Hardware Preset Tasten: Box sendet via WebSocket auf 8080 (gabbo Protocol)
	// einen presetSelectionUpdated event wenn der User physisch eine Taste
	// drueckt. Wir hooken den Event und triggern unseren UPnP Player.
	renderer := upnp.NewBoseRenderer(*boxHost)
	wsHandler := &presetWsHandler{
		logger:   logger.With("comp", "boxws"),
		store:    store,
		renderer: renderer,
		autoPair: autoPair,
		boxHost:  *boxHost,
		spotify:  spotifyMgr,
	}
	wsClient := boxws.New(
		logger.With("comp", "boxws"),
		fmt.Sprintf("ws://%s:8080/", *boxHost),
		wsHandler,
	)
	// Let the WebUI fill the Wi-Fi signal from the gabbo stream on BCO
	// boxes, whose /networkInfo reports no signal.
	webuiSrv.SetWifiSignalFn(wsClient.LastWifiSignal)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 3)

	// === Listener boot FIRST ===
	// Bind marge / bmx / webui before any slow init (box /info,
	// TLS bundle generation, mDNS announce). The boot-watchdog in
	// usb-stick/run.sh checks ALIVE + BOUND every 5s starting at
	// t=5s; on weak SoundTouch hardware the previous order spent
	// 20-30s on pre-listen work (5s boxapi /info timeout, first-
	// boot CA generation, etc.) and the watchdog killed the agent
	// mid-init in a respawn loop. Listeners up first means :8888
	// answers in 1-2s and the watchdog sees BOUND=1 from the
	// first check.
	startHTTP(ctx, &wg, errs, "marge", *margeAddr, margeSrv.Handler(), logger)
	startHTTP(ctx, &wg, errs, "bmx", *bmxAddr, bmxSrv.Handler(), logger)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := webuiSrv.Run(ctx); err != nil {
			errs <- fmt.Errorf("webui: %w", err)
		}
	}()

	// Box WebSocket Listener fuer Hardware Preset Tasten
	wg.Add(1)
	go func() {
		defer wg.Done()
		wsClient.Run(ctx)
	}()

	// Spotify preset audio plane (#78, P1): supervise librespot. Idles
	// (returns immediately) until a credential is cached, so it is safe to
	// start unconditionally.
	go spotifyMgr.Run(ctx)

	// Auto Pair Background: pairt die Box automatisch beim Start. Re-pairt
	// alle 5 Minuten falls die Box mal verloren geht. Plus: WS Handler
	// triggert TriggerNow bei Preset Press damit Pair sofort kommt nach
	// Standby Aufwachen.
	wg.Add(1)
	go func() {
		defer wg.Done()
		autoPair.RunBackground(ctx, 8*time.Second, 5*time.Minute)
	}()

	// Resource heartbeat: a one-line MemAvailable + loadavg snapshot
	// every 5 minutes. The box has ~120 MB RAM and no swap, so a slow
	// leak ends in an OOM freeze that otherwise leaves no trace; this
	// makes the RAM/load trend before a freeze visible in the on-box log
	// for post-mortem. Negligible NAND traffic (12 lines/hour), now that
	// the per-second connectionState spam is gone.
	wg.Add(1)
	go func() {
		defer wg.Done()
		logResourceHealth(logger)
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				logResourceHealth(logger)
				memoryGuardCheck(logger, spotifyMgr, *boxHost)
			}
		}
	}()

	// BatteryMonitor fallback: on the Portable the Bose BatteryMonitor
	// deadlocks at init and never serves 127.0.0.1:17002, so BoseApp's
	// battery client connect-storms it and leaks fds until the box reboots
	// (~27 min). We accept on :17002 ourselves when it is unserved, which
	// stops the storm. No-op where BatteryMonitor is healthy. See
	// boseapp_recovery.go for the full root-cause writeup.
	wg.Add(1)
	go func() {
		defer wg.Done()
		serveBatteryMonitorFallback(ctx, logger)
	}()

	// === Deferred heavy init (background) ===
	// Everything below this point runs while the agent's listeners
	// are already serving. Slow steps are isolated in their own
	// goroutines so a stall in one (e.g. TLS CA generation on a
	// flash-bound NAND) does not delay another (e.g. mDNS announce).
	// All goroutines respect ctx so shutdown still terminates them
	// promptly.
	//
	// Order within each goroutine is preserved from the previous
	// sync flow; only the cross-goroutine ordering changes.

	// mDNS announce: detect model from box /info, then announce. The
	// model lookup is a 5 s blocking call against the Bose firmware
	// which on a cold boot may not yet be answering. Doing it here
	// (after listeners are up) costs nothing user-visible — the
	// desktop app's discovery retries until the announce lands.
	var (
		mdnsMu        sync.Mutex
		mdnsAnnouncer *discovery.Announcer
	)
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Announce mDNS IMMEDIATELY with a generic fallback model.
		// Reading /info synchronously here used to race the Bose
		// firmware's :8090 endpoint, which on rhino ST10 comes up
		// at uptime ~43s but the agent's bootstrap finishes at
		// uptime ~22s. The 5s-timeout LoadSettings then got
		// connection-refused and we silently fell back to
		// model="SoundTouch", a generic string the desktop app's
		// stockModelLabel() does not map to any friendly name, so
		// the box picker's model column stayed empty forever
		// (observed live 2026-05-24 on ST10 .66 v0.5.12). pollBoxInfo
		// below replaces the fallback once /info responds for real.
		ann, err := discovery.Announce(
			logger.With("comp", "discovery"),
			discovery.Config{
				Port:         8888,
				DeviceID:     deviceID,
				FriendlyName: "Bose SoundTouch " + lastN(deviceID, 6),
				Model:        "SoundTouch",
				Version:      version,
				Build:        buildStamp,
			},
		)
		if err != nil {
			logger.Warn("mDNS announce failed, continuing without", "err", err)
			return
		}
		mdnsMu.Lock()
		mdnsAnnouncer = ann
		mdnsMu.Unlock()

		// Background poll: refresh name AND model in mDNS TXT as
		// soon as /info on :8090 responds, then continue watching
		// for renames the user might do via the BoseApp HTTP API.
		go pollBoxInfo(ctx, *boxHost, region, ann, logger)
	}()

	if *pendingNameFile != "" {
		go applyPendingBoxName(context.Background(), *boxHost, *pendingNameFile, deviceID, logger)
	}

	// Wenn der USB Stick ein neueres run.sh hat als das NAND
	// run-override.sh: kopieren. Das ist der Selbst-Update-Pfad fuer
	// den Bootstrap. Ohne das laeuft die alte run-override.sh aus dem
	// allerersten Setup auf ewig und neue Setup Wizard Configs werden
	// ignoriert.
	go syncRunOverrideFromStick(logger)

	// TLS Termination fuer Marge auf 8443. iptables redirected die echte
	// Box Anfrage von 443 dorthin. Wenn TLS deaktiviert wird, ueberspringen.
	// EnsureBundle generates a per-box CA on the very first boot, which
	// touches NAND and can take several seconds — keep this off the
	// listener-boot path.
	if *tlsEnabled {
		go func() {
			tlsMgr := tlsgen.New(*tlsDir, nil, logger.With("comp", "tlsgen"))
			bundle, regenerated, err := tlsMgr.EnsureBundle()
			if err != nil {
				logger.Error("TLS bundle unavailable, continuing without TLS listener", "err", err)
				return
			}
			// run.sh's bind-mount block reads /mnt/nv/streborn/ca/root.crt
			// before the agent starts. When EnsureBundle has just
			// replaced a stale bundle, that mount is now serving the
			// previous root CA and Bose will reject our new server cert
			// with `tls: unknown certificate authority`. Patch the live
			// overlays in place via O_APPEND so the new root joins the
			// trust set without a remount.
			if regenerated {
				if err := tlsgen.RefreshTrustStore(bundle.RootCAPEM, logger.With("comp", "tlsgen")); err != nil {
					logger.Warn("trust store refresh after CA regen failed, Bose may reject our cert until next boot", "err", err)
				}
			}
			cert, err := bundle.TLSCert()
			if err != nil {
				logger.Error("TLS cert not loadable, continuing without TLS listener", "err", err)
				return
			}
			tlsConfig := &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
			}
			startHTTPS(ctx, &wg, errs, "marge-tls", *margeTLSAddr,
				margeSrv.Handler(), tlsConfig, logger)
		}()
	}

	logger.Warn("agent listeners spawned, deferred init continues in background",
		"webui", *webuiAddr, "marge", *margeAddr, "bmx", *bmxAddr, "tlsEnabled", *tlsEnabled, "margeTLS", *margeTLSAddr)

	// Self-probe loopback connect to each listener address. When the
	// box is reachable but :8888 silently does not answer, the bash
	// watchdog (agent_port_bound in run.sh) cannot always tell on a
	// BusyBox without ss/netstat. The Go side has full net access and
	// can prove from inside the agent process whether each port is
	// actually accepting connections. Logs at WARN so the result shows
	// in any diagnostic capture.
	wg.Add(1)
	go func() {
		defer wg.Done()
		runSelfProbe(ctx, logger.With("comp", "selfprobe"), []selfProbeTarget{
			{name: "webui", addr: *webuiAddr},
			{name: "marge", addr: *margeAddr},
			{name: "bmx", addr: *bmxAddr},
			{name: "marge-tls", addr: *margeTLSAddr},
		})
	}()

	var firstErr error
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errs:
		firstErr = err
		logger.Error("subsystem error, shutting down", "err", err)
		cancel()
	}

	wg.Wait()

	mdnsMu.Lock()
	mdnsAnn := mdnsAnnouncer
	mdnsMu.Unlock()
	if mdnsAnn != nil {
		mdnsAnn.Close()
	}

	if hostsMgr != nil {
		if err := hostsMgr.Restore(); err != nil {
			logger.Warn("hosts file restore failed", "err", err)
		}
	}

	logger.Info("streborn exited")
	return firstErr
}

// startHTTP startet einen HTTP Server in einer Goroutine und meldet Fehler an errs.
//
// The listener is opened via netutil.ListenTCP, which sets SO_REUSEADDR on
// the socket. Without that, a watchdog-driven respawn while the previous
// listener is still in TIME_WAIT fails with "address already in use".
//
// Phase-marker logs are at WARN level on purpose: visible on any
// --log-level setting and in the diagnostic bundle's tail capture.
func startHTTP(ctx context.Context, wg *sync.WaitGroup, errs chan<- error, name, addr string, handler http.Handler, logger *slog.Logger) {
	logger.Warn("listener phase: spawn", "comp", name, "addr", addr)
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv := &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		}
		logger.Warn("listener phase: calling ListenTCP", "comp", name, "addr", addr)
		ln, err := netutil.ListenTCP(ctx, addr)
		if err != nil {
			logger.Error("listener phase: ListenTCP failed", "comp", name, "addr", addr, "err", err)
			errs <- fmt.Errorf("%s: listen %s: %w", name, addr, err)
			return
		}
		logger.Warn("listener phase: ListenTCP succeeded", "comp", name, "addr", addr, "local", ln.Addr().String())
		serveErr := make(chan error, 1)
		go func() {
			serveErr <- srv.Serve(ln)
		}()
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		case err := <-serveErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("%s: %w", name, err)
			}
		}
	}()
}

// startHTTPS startet einen HTTPS Server analog zu startHTTP, mit der
// uebergebenen TLS Konfiguration.
func startHTTPS(ctx context.Context, wg *sync.WaitGroup, errs chan<- error, name, addr string, handler http.Handler, tlsConfig *tls.Config, logger *slog.Logger) {
	logger.Warn("listener phase: spawn TLS", "comp", name, "addr", addr)
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv := &http.Server{
			Addr:              addr,
			Handler:           handler,
			TLSConfig:         tlsConfig,
			ReadHeaderTimeout: 10 * time.Second,
		}
		logger.Warn("listener phase: calling ListenTCP TLS", "comp", name, "addr", addr)
		ln, err := netutil.ListenTCP(ctx, addr)
		if err != nil {
			logger.Error("listener phase: ListenTCP TLS failed", "comp", name, "addr", addr, "err", err)
			errs <- fmt.Errorf("%s: listen %s: %w", name, addr, err)
			return
		}
		logger.Warn("listener phase: ListenTCP TLS succeeded", "comp", name, "addr", addr, "local", ln.Addr().String())
		// ServeTLS upgrades the listener with the supplied TLSConfig.
		// We pass empty paths since the cert is in TLSConfig.Certificates.
		serveErr := make(chan error, 1)
		go func() {
			serveErr <- srv.ServeTLS(ln, "", "")
		}()
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		case err := <-serveErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("%s: %w", name, err)
			}
		}
	}()
}

// presetWsHandler implementiert boxws.Handler und ruft bei Hardware Preset
// Tasten den UPnP Renderer mit der Stream URL aus dem Preset Store auf.
type presetWsHandler struct {
	logger   *slog.Logger
	store    *presets.Store
	renderer *upnp.Renderer
	autoPair *autopair.Manager
	boxHost  string
	spotify  *spotify.Manager
}

func (h *presetWsHandler) OnPresetSelected(ctx context.Context, slot int, location, title string) {
	// URL bleibt die Proxy URL (location = http://127.0.0.1:8888/stream/N)
	// damit der Stream Proxy den Reconnect bei Token Expiry uebernimmt.
	// Name + Icon kommen aus dem Stick Preset Store — die Bose ContentItem
	// Metadata hat keinen Art Eintrag, daher muessen wir die Album Art
	// URL aktiv ueber unser PlayURL Aufruf ins DIDL Lite Metadata
	// reinpacken, sonst zeigt das Display (ST20/30) kein Logo.
	url := location
	name := title
	icon := ""
	if p, ok := h.store.Get(slot); ok {
		// Spotify presets do not have a playable HTTP StreamURL. They are
		// recalled by telling go-librespot to play the saved URI and then
		// pointing the box's UPnP renderer at our live /spotify/stream.
		if p.Type == "spotify" && p.URI != "" {
			h.playSpotifyPreset(ctx, slot, p)
			return
		}
		if p.Name != "" {
			name = p.Name
		}
		icon = p.Art
		// Fallback: NetManager occasionally fires nowSelectionUpdated
		// with an empty location — observed when Bose's preset cache
		// was populated while BoseApp had not yet fully loaded the
		// NetManager DB at boot. Our own store always has the
		// authoritative URL, so use it whenever the event field is
		// empty. Symmetric with the software-preset code path.
		if url == "" && p.StreamURL != "" {
			url = p.StreamURL
			h.logger.Info("hardware preset location empty, falling back to store URL", "slot", slot)
		}
	}
	if url == "" {
		h.logger.Info("hardware preset gedrueckt, kein Mapping", "slot", slot)
		return
	}

	// Box aufwecken aus Standby + Pair sicherstellen.
	if h.boxHost != "" {
		wakeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		if err := boxcli.WakeAndWait(wakeCtx, h.boxHost, 6*time.Second, h.logger); err != nil {
			h.logger.Warn("Box konnte nicht aus STANDBY geholt werden", "err", err)
		}
		cancel()
	}
	if h.autoPair != nil {
		pairCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		h.autoPair.TriggerNow(pairCtx)
		cancel()
	}

	playCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := h.renderer.PlayURL(playCtx, url, name, icon); err != nil {
		h.logger.Warn("upnp play (initial) failed, will verify+retry", "slot", slot, "err", err)
	}
	// Verify+retry in the background: the first hardware press after a cold
	// boot can race the box/agent bringup so nothing plays until a second
	// press. This re-issues until the box actually plays. Affects radio too.
	go h.verifyPlayURL(slot, url, name, icon)
	h.logger.Info("hardware preset zu upnp gemapped", "slot", slot, "name", name)
}

// spotifyStreamURL is the agent-local URL the box's UPnP renderer fetches
// for Spotify audio. The agent runs on the box, so 127.0.0.1:8888 reaches
// it (same host:port the radio stream proxy uses). The .ogg suffix is
// required: the Bose renderer keys playability off the URL extension and
// rejects an extensionless Ogg stream (INVALID_SOURCE).
const spotifyStreamURL = "http://127.0.0.1:8888/spotify/stream.ogg"

// playSpotifyPreset recalls a Spotify preset: wake + pair the box, tell
// go-librespot to play the saved URI (autonomous, no app), then point the
// box at the live /spotify/stream so it plays the audio over UPnP.
func (h *presetWsHandler) playSpotifyPreset(ctx context.Context, slot int, p presets.Preset) {
	if h.spotify == nil || !h.spotify.Ready() {
		h.logger.Warn("spotify preset pressed but manager not ready", "slot", slot)
		return
	}

	// Wake from standby + ensure pairing, same as the radio path.
	if h.boxHost != "" {
		wakeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		if err := boxcli.WakeAndWait(wakeCtx, h.boxHost, 6*time.Second, h.logger); err != nil {
			h.logger.Warn("Box konnte nicht aus STANDBY geholt werden", "err", err)
		}
		cancel()
	}
	if h.autoPair != nil {
		pairCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		h.autoPair.TriggerNow(pairCtx)
		cancel()
	}

	// Start go-librespot FIRST so the Ogg is already flowing when the box
	// attaches (no data-starve timeout on join); then point the box at the
	// stream. ServeOgg seeks the track to 0 on attach so the box still gets
	// a fresh header sequence to sync to.
	playCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if err := h.spotify.Play(playCtx, p.URI); err != nil {
		h.logger.Warn("spotify play (initial) failed, will verify+retry", "slot", slot, "err", err)
	} else if err := h.renderer.PlayURLMime(playCtx, spotifyStreamURL, p.Name, p.Art, "audio/ogg"); err != nil {
		h.logger.Warn("spotify upnp play (initial) failed, will verify+retry", "slot", slot, "err", err)
	}
	// Verify+retry in the background: the first press after a cold boot races
	// go-librespot's auth, so the box gets no audio and the user had to press
	// twice. This retries until the box actually plays, with no latency on the
	// happy path (the initial attempt above already played).
	go h.verifySpotifyPlaying(slot, p)
	h.logger.Info("spotify preset recalled", "slot", slot, "name", p.Name, "account", p.Account)
}

// verifyPlayURL confirms the box started playing a UPnP (radio) recall and
// re-issues it a few times if not, fixing the "first hardware press after
// reboot does nothing" race for radio presets too.
func (h *presetWsHandler) verifyPlayURL(slot int, url, name, icon string) {
	for attempt := 1; attempt <= 3; attempt++ {
		time.Sleep(5 * time.Second)
		if boxIsPlaying(h.boxHost) {
			return
		}
		h.logger.Warn("hardware recall not playing yet, retrying", "slot", slot, "attempt", attempt)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_ = h.renderer.PlayURL(ctx, url, name, icon)
		cancel()
	}
	h.logger.Warn("hardware recall still not playing after retries", "slot", slot)
}

// verifySpotifyPlaying confirms the box reached a playing state after a Spotify
// recall and re-issues the recall a few times if not, fixing the "first press
// after reboot does nothing" race without needing a second press.
func (h *presetWsHandler) verifySpotifyPlaying(slot int, p presets.Preset) {
	for attempt := 1; attempt <= 3; attempt++ {
		time.Sleep(5 * time.Second)
		if boxIsPlaying(h.boxHost) {
			return
		}
		h.logger.Warn("spotify recall not playing yet, retrying", "slot", slot, "attempt", attempt)
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		if err := h.spotify.Play(ctx, p.URI); err == nil {
			_ = h.renderer.PlayURLMime(ctx, spotifyStreamURL, p.Name, p.Art, "audio/ogg")
		}
		cancel()
	}
	h.logger.Warn("spotify recall still not playing after retries", "slot", slot)
}

// loadRegion liest den Country Code aus der region.txt vom Stick. Leer
// wenn die Datei nicht existiert oder leer ist; in dem Fall faellt die
// App spaeter auf Browser/User Default zurueck.
func loadRegion(path string, logger *slog.Logger) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		logger.Debug("region file not readable", "path", path, "err", err)
		return ""
	}
	cc := ""
	for _, r := range string(b) {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			cc += string(r)
		}
	}
	if len(cc) < 2 {
		return ""
	}
	cc = cc[:2]
	// Uppercase
	out := ""
	for _, r := range cc {
		if r >= 'a' && r <= 'z' {
			r = r - 32
		}
		out += string(r)
	}
	logger.Info("Region geladen", "country", out)
	return out
}

// syncRunOverrideFromStick haelt das NAND run-override.sh aktuell mit
// dem run.sh auf dem Stick. Wichtig: rc.local priorisiert NAND vor
// Stick, daher wuerde ein veraltetes NAND Script die neuen Setup
// Wizard Features (name.conf, region.conf etc) ignorieren.
//
// Wenn die Files identisch sind: no-op (keine Flash Writes).
func syncRunOverrideFromStick(logger *slog.Logger) {
	const stickPath = "/media/sda1/run.sh"
	const nandPath = "/mnt/nv/streborn/run-override.sh"

	time.Sleep(5 * time.Second) // dem Stick Zeit zum mounten geben

	stickData, err := os.ReadFile(stickPath)
	if err != nil {
		logger.Debug("run.sh on stick not readable, skipping sync", "err", err)
		return
	}
	nandData, _ := os.ReadFile(nandPath)
	if len(nandData) > 0 && bytesEqual(stickData, nandData) {
		return // schon identisch
	}
	tmp := nandPath + ".new"
	if err := os.WriteFile(tmp, stickData, 0o755); err != nil {
		logger.Warn("run-override.sh sync write failed", "err", err)
		return
	}
	if err := os.Rename(tmp, nandPath); err != nil {
		logger.Warn("run-override.sh sync rename failed", "err", err)
		os.Remove(tmp)
		return
	}
	logger.Info("run-override.sh updated on NAND from stick", "bytes", len(stickData))
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// applyPendingBoxName wendet einen vom Setup Wizard hinterlegten Box
// Namen einmalig auf die Bose Box an und haengt die letzten 4 Hex der
// DeviceID als UID Suffix an damit Dopplungen in mehreren Boxen im LAN
// ausgeschlossen sind. Bei Erfolg wird die Datei geloescht.
func applyPendingBoxName(ctx context.Context, boxHost, path, deviceID string, logger *slog.Logger) {
	if boxHost == "" || path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		// keine Datei, nichts anzuwenden
		return
	}
	raw := strings.TrimSpace(string(b))
	if raw == "" {
		_ = os.Remove(path)
		return
	}
	suffix := ""
	if len(deviceID) >= 4 {
		suffix = strings.ToUpper(deviceID[len(deviceID)-4:])
	}
	wanted := raw
	if suffix != "" && !strings.HasSuffix(strings.ToUpper(wanted), suffix) {
		wanted = raw + " " + suffix
	}
	// Box muss erreichbar sein. Warten bis BoseApp Webserver hochgefahren.
	time.Sleep(10 * time.Second)
	c := boxapi.New(boxHost)
	for attempt := 0; attempt < 12; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.SetName(callCtx, wanted)
		cancel()
		if err == nil {
			logger.Info("Setup Wizard Box Name angewendet", "name", wanted)
			_ = os.Remove(path)
			return
		}
		logger.Debug("box name set failed, will retry", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	logger.Warn("Box Name aus Setup konnte nicht gesetzt werden, gebe auf", "path", path)
}

// pollBoxInfo fragt regelmaessig die Box /info ab und haelt die mDNS TXT
// Felder fuer FriendlyName und Model aktuell. Damit:
//
//  1. Der Desktop App kennt den Namen sobald der User die Box umbenennt
//     (z.B. via BoseApp HTTP), ohne Box Reboot.
//  2. Das model TXT Feld wird auf den echten Wert ("SoundTouch 10" etc)
//     hochgezogen, sobald die Bose Firmware /info auf :8090 ausliefert.
//     Beim ersten Announce steht dort noch der generische Fallback
//     "SoundTouch" weil :8090 typisch 20+ Sekunden nach dem Agent-Start
//     hochkommt — der Loop hier dichtet die Race ab ohne den Boot zu
//     blockieren.
//
// Erste Runde nach kurzem Delay, dann mit kurzem Ticker bis Model
// erkannt ist (race-Recovery), danach geht der Ticker auf 30s zurueck.
func pollBoxInfo(ctx context.Context, boxHost, region string, ann *discovery.Announcer, logger *slog.Logger) {
	if boxHost == "" || ann == nil {
		return
	}
	time.Sleep(2 * time.Second)
	client := boxapi.New(boxHost)
	var (
		lastName       string
		lastModel      string
		regionLogged   bool
		modelEverFound bool
	)
	doOne := func() {
		fetchCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		s, err := client.LoadSettings(fetchCtx)
		if err != nil {
			logger.Debug("pollBoxInfo fail", "err", err)
			return
		}
		if model := strings.TrimSpace(s.Info.Type); model != "" {
			if !modelEverFound {
				logger.Info("Box Modell erkannt", "type", model)
				modelEverFound = true
			}
			if model != lastModel {
				if err := ann.UpdateModel(model); err != nil {
					logger.Warn("mDNS UpdateModel failed", "err", err)
				} else {
					logger.Info("mDNS model updated", "model", model)
					lastModel = model
				}
			}
		}
		if name := strings.TrimSpace(s.Info.Name); name != "" && name != lastName {
			if err := ann.UpdateFriendlyName(name); err != nil {
				logger.Warn("mDNS UpdateFriendlyName failed", "err", err)
			} else {
				logger.Info("mDNS FriendlyName updated", "name", name)
				lastName = name
			}
		}
		if !regionLogged {
			// Bose's countryCode is set at the factory or during
			// the original Bose pairing flow and is rarely the
			// user's actual location after STR install. STR uses
			// region.txt (written by the setup wizard) for radio
			// defaults. Log the mismatch once so it is documented
			// in diagnostic bundles and not mistaken for a bug.
			boseCC := strings.ToUpper(strings.TrimSpace(s.Info.CountryCode))
			if region != "" && boseCC != "" && region != boseCC {
				logger.Info("Region: STR uses region.txt for radio defaults; Bose firmware countryCode is informational only",
					"strRegion", region, "boseCountryCode", boseCC)
			}
			regionLogged = true
		}
	}
	// Fast cadence until we have a real model, then back off to 30s.
	// Without backoff we'd keep hitting :8090 every 4s forever even
	// after model is stable — overkill, since name changes are rare
	// and 30s catches them within one UI refresh cycle.
	fast := time.NewTicker(4 * time.Second)
	defer fast.Stop()
	doOne()
	for !modelEverFound {
		select {
		case <-ctx.Done():
			return
		case <-fast.C:
			doOne()
		}
	}
	slow := time.NewTicker(30 * time.Second)
	defer slow.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-slow.C:
			doOne()
		}
	}
}

// proxyStreamURL gibt die stabile loopback URL fuer ein Preset zurueck.
// Bose UPnP Player oeffnet die — der Stream Proxy im Stick Agent loest
// dahinter den echten Sender Redirect auf und reconnectet bei Token
// Expiry, ohne dass Bose etwas merkt.
func proxyStreamURL(slot int) string {
	return fmt.Sprintf("http://127.0.0.1:8888/stream/%d", slot)
}

// initialBoxPresetSync wartet auf den Box Boot und synct alle Stick
// Presets an den Box internen Preset Store. Mit Retry Loop: bei
// fehlgeschlagenen Slots wird nach 10s erneut versucht, bis zu 12 mal.
// Hintergrund: die Bose Firmware ist beim Boot manchmal noch nicht
// bereit fuer AddPreset Calls (autopair noch nicht durch, marge state
// noch nicht initialisiert). Ohne Retry blieben Slots dauerhaft ohne
// Box Eintrag — Hardware Tasten 1-6 wuerden dann nichts ausloesen.
// Initial 30 s Warten (war 12 s): in der Praxis gemessen, dass die
// Bose Firmware nach einem Cold Boot ~60 s braucht bis /info auf 8090
// antwortet und marge State steht. 12 s war optimistisch.
// 12 Retry Slots mit je 10 s Pause = ~2 Minuten gesamter Runway.
func initialBoxPresetSync(store *presets.Store, boxHost string, logger *slog.Logger) {
	time.Sleep(30 * time.Second)
	specs := make([]boxcli.PresetSpec, 0, 6)
	for _, p := range store.All() {
		specs = append(specs, boxcli.PresetSpec{
			Slot: p.Slot, Name: p.Name, StreamURL: proxyStreamURL(p.Slot),
		})
	}
	if len(specs) == 0 {
		return
	}
	logger.Info("starte initial box preset sync", "anzahl", len(specs))

	pending := make(map[int]boxcli.PresetSpec, len(specs))
	for _, p := range specs {
		pending[p.Slot] = p
	}

	for attempt := 0; attempt < 12 && len(pending) > 0; attempt++ {
		if attempt > 0 {
			time.Sleep(10 * time.Second)
		}
		retrySpecs := make([]boxcli.PresetSpec, 0, len(pending))
		for _, p := range pending {
			retrySpecs = append(retrySpecs, p)
		}
		errs := boxcli.SyncAllPresets(context.Background(), boxHost, retrySpecs)
		for slot, err := range errs {
			if err == nil {
				delete(pending, slot)
				logger.Info("box preset synced", "slot", slot, "name", pending[slot].Name, "attempt", attempt)
			} else if attempt == 5 {
				logger.Warn("box preset sync failed permanently", "slot", slot, "err", err)
			} else {
				logger.Debug("box preset sync fail, retry", "slot", slot, "attempt", attempt, "err", err)
			}
		}
	}
	if len(pending) == 0 {
		logger.Info("all box presets synced successfully")
	}
}

// periodicPresetReconcile prueft alle 5 Minuten ob die Box noch alle
// Stick Presets in ihrer eigenen Liste hat. Fehlende Slots werden via
// boxcli.AddPreset nachgepflegt. Damit greift der Fix automatisch ohne
// User Aktion wenn z.B. die Bose Firmware nach einem Standby Cycle
// einzelne Eintraege verloren hat.
func periodicPresetReconcile(store *presets.Store, boxHost string, logger *slog.Logger) {
	// 90s nach Boot anfangen, danach im 5 min Takt
	time.Sleep(90 * time.Second)
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	// fullDone tracks whether we have done a full re-sync since the box
	// last became ready. The boot-time preset sync can run before the
	// box's preset / hardware-button subsystem is fully up; the slots
	// then show in /presets (so the missing-only path skips them) yet
	// the physical buttons do not recognise them until a fresh AddPreset
	// re-registers them once the box is ready. So the FIRST reconcile
	// after the box leaves OOB re-pushes ALL slots, not just missing
	// ones. Resets when the box drops back to OOB so a re-provision
	// re-registers the buttons. Live-confirmed on a taigan Portable
	// 2026-06-01: buttons 1/2 stayed "empty" until a full re-sync even
	// though /presets listed them.
	fullDone := false
	for {
		ready := reconcileOnce(store, boxHost, logger, !fullDone)
		fullDone = ready
		<-tick.C
	}
}

// reconcileOnce returns true once the box is out of OOB and reachable.
// When forceFull is set it re-pushes EVERY stick preset rather than only
// the slots missing from the box's /presets list (see fullDone above).
func reconcileOnce(store *presets.Store, boxHost string, logger *slog.Logger, forceFull bool) bool {
	stick := store.All()
	if len(stick) == 0 {
		return false
	}
	// Do not push presets while the box is still in out-of-box setup.
	// In OOB the Marge state machine is NotAssociated, so every
	// AddPreset fails with "MargeHSM is in the wrong state" and just
	// spams BoseApp's log (and ours) once per cycle. Wait until the box
	// has joined a network. Live-observed on a taigan Portable in OOB,
	// 2026-05-31.
	if boxInSetupOOB(boxHost) {
		logger.Debug("preset reconcile: box still in OOB setup (MargeHSM not associated), skipping until it joins a network")
		return false
	}
	boxSlots, err := fetchBoxPresetSlots(boxHost)
	if err != nil {
		logger.Debug("preset reconcile: box presets not readable", "err", err)
		return false
	}
	var missing []boxcli.PresetSpec
	for _, p := range stick {
		if forceFull || !boxSlots[p.Slot] {
			missing = append(missing, boxcli.PresetSpec{
				Slot: p.Slot, Name: p.Name, StreamURL: proxyStreamURL(p.Slot),
			})
		}
	}
	if len(missing) == 0 {
		return true
	}
	if forceFull {
		logger.Info("preset reconcile: full re-sync after box became ready (registers hardware buttons)", "slots", len(missing))
	} else {
		logger.Info("preset reconcile: fehlende Slots auf Box, sync", "fehlend", len(missing))
	}
	errs := boxcli.SyncAllPresets(context.Background(), boxHost, missing)
	for slot, err := range errs {
		if err == nil {
			logger.Info("preset reconcile geheilt", "slot", slot)
		}
	}
	return true
}

// fetchBoxPresetSlots liest GET /presets von der Bose API und liefert
// eine Map welcher Slot in der Box Liste gesetzt ist.
func fetchBoxPresetSlots(boxHost string) (map[int]bool, error) {
	client := http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:8090/presets", boxHost))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := map[int]bool{}
	// Bose Format: <presets><preset id="1" ...> ... </preset></presets>
	for _, m := range presetIDRegex.FindAllStringSubmatch(string(body), -1) {
		slot := 0
		fmt.Sscanf(m[1], "%d", &slot)
		if slot >= 1 && slot <= 6 {
			out[slot] = true
		}
	}
	return out, nil
}

var presetIDRegex = regexp.MustCompile(`<preset id="(\d+)"`)

// boxInSetupOOB reports whether BoseApp's /setup says the box is still
// in out-of-box setup (SETUP_AP_OOB). Pushing presets in that state
// fails with "MargeHSM is in the wrong state" and only spams the log,
// so the reconciler waits until the box has joined a network. On any
// read error we return false (proceed) so a firmware whose /setup
// differs never silently stops reconciling on a working box.
func boxInSetupOOB(boxHost string) bool {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:8090/setup", boxHost))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return strings.Contains(string(body), "SETUP_AP_OOB")
}

// lastN gibt die letzten n Zeichen von s zurueck.
func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ensureSshdRunning keeps the box reachable via SSH whether the agent
// boot came in via a fresh stick run.sh (which has its own
// ensure_sshd_running shell function) or via OTA-only update (which
// replaces only the binary and leaves the on-NAND run-override.sh
// untouched). Without this the OTA path loses the diagnostic channel
// the first time the agent crashes, and `SaveDiagnosticBundle`'s
// SSH-fallback layer comes back empty.
//
// Pre-1.0 we explicitly prefer diagnostic access over the residual
// risk of a known-default Bose root password; tracked under the
// existing box-security-hardening roadmap.
//
// bootstrapTargets lists the on-NAND files the agent will replace
// when their disk content differs from what is embedded in the
// agent binary. /mnt/nv/rc.local is read once by /etc/init.d/
// shelby_local at boot; /mnt/nv/streborn/run-override.sh is what
// that rc.local exec's. Both must stay in sync with the agent
// version so the boot path uses the same shim / WLAN / gate logic
// the running agent expects.
var bootstrapTargets = []struct {
	embedded string
	target   string
	desc     string
}{
	{"run.sh", "/mnt/nv/streborn/run-override.sh", "boot bootstrap script"},
	{"rc.local", "/mnt/nv/rc.local", "shelby_local entry point"},
}

// syncBootstrapFromEmbedded compares the bootstrap files embedded
// in this agent binary against the on-NAND copies and replaces any
// that differ. Runs once on agent startup. Atomic via tmp-file +
// rename; on any failure we leave the existing file in place and
// log so the next diagnostic bundle captures the reason. Skipped
// silently in dev environments where /mnt/nv does not exist.
func syncBootstrapFromEmbedded(logger *slog.Logger) (changed bool) {
	if _, err := os.Stat("/mnt/nv"); err != nil {
		// Not on the box (developer machine, CI). No-op.
		return false
	}
	stickFS := usbstick.Files()
	for _, t := range bootstrapTargets {
		embedded, err := fs.ReadFile(stickFS, t.embedded)
		if err != nil {
			logger.Warn("bootstrap sync: embedded file not readable",
				"name", t.embedded, "err", err)
			continue
		}
		current, _ := os.ReadFile(t.target)
		if bytes.Equal(embedded, current) {
			// Already current. Quiet path.
			continue
		}
		// Ensure parent directory exists. /mnt/nv/streborn may be
		// missing on a freshly-flashed-and-reset box that still has
		// the old rc.local but no streborn dir tree yet.
		if i := strings.LastIndex(t.target, "/"); i > 0 {
			_ = os.MkdirAll(t.target[:i], 0o755)
		}
		tmp := t.target + ".str-bootstrap-sync"
		_ = os.Remove(tmp) // tolerate stale tmp from a previous crashed run
		if err := os.WriteFile(tmp, embedded, 0o755); err != nil {
			logger.Warn("bootstrap sync: write failed",
				"tmp", tmp, "err", err)
			continue
		}
		if err := os.Rename(tmp, t.target); err != nil {
			logger.Warn("bootstrap sync: atomic rename failed, leaving old in place",
				"tmp", tmp, "target", t.target, "err", err)
			_ = os.Remove(tmp)
			continue
		}
		// WARN so a diagnostic bundle pinpoints the boot where the
		// bootstrap layer caught up. The replacement only takes effect
		// on the NEXT boot: this boot's already-running shelby_local
		// and run-override.sh are whatever they were before the sync.
		logger.Warn("bootstrap sync: replaced on-NAND file with embedded copy",
			"target", t.target,
			"desc", t.desc,
			"oldBytes", len(current),
			"newBytes", len(embedded),
			"effective", "next boot")
		changed = true
	}
	return changed
}

// bootstrapRebootStampPath records the fingerprint of the embedded
// bootstrap set we last rebooted for. It is the loop breaker: if a
// NAND write silently fails to persist, syncBootstrapFromEmbedded
// would report "changed" on every boot, and an unconditional
// post-sync reboot would turn that into a boot loop that bricks the
// box. We reboot at most once per embedded fingerprint.
const bootstrapRebootStampPath = "/mnt/nv/streborn/.str-bootstrap-reboot-stamp"

// embeddedBootstrapStamp is a stable fingerprint of the bootstrap
// files embedded in THIS binary. It changes only when the embedded
// run.sh / rc.local change, i.e. across agent releases that touch the
// boot path. Returns "" if the embedded files cannot be read, in
// which case the caller must not reboot (it cannot guard the loop).
func embeddedBootstrapStamp() string {
	h := sha256.New()
	stickFS := usbstick.Files()
	for _, t := range bootstrapTargets {
		b, err := fs.ReadFile(stickFS, t.embedded)
		if err != nil {
			return ""
		}
		_, _ = h.Write([]byte(t.embedded))
		_, _ = h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// maybeRebootAfterBootstrapSync reboots the box once so a freshly
// written run-override.sh / rc.local take effect immediately instead
// of on the user's next manual power-cycle. This is the "STR reboots
// itself into a clean state" path: after a stick install or agent OTA
// the running boot used the OLD scripts; one clean reboot lands the
// box on the boot path that matches the running binary.
//
// Guarded so it can never loop:
//   - If the embedded fingerprint cannot be computed, do not reboot.
//   - If we already rebooted for exactly this fingerprint (marker
//     matches) yet the sync still reported a change, the NAND write is
//     not persisting; rebooting again would loop, so we stay up in a
//     degraded state and log loudly instead.
func maybeRebootAfterBootstrapSync(logger *slog.Logger) {
	stamp := embeddedBootstrapStamp()
	if stamp == "" {
		logger.Warn("bootstrap reboot: skipped, cannot fingerprint embedded boot files (no loop guard possible)")
		return
	}
	if prev, err := os.ReadFile(bootstrapRebootStampPath); err == nil &&
		strings.TrimSpace(string(prev)) == stamp {
		logger.Error("bootstrap reboot: on-NAND boot files STILL differ after a prior reboot for this exact version - the NAND write is not persisting; refusing to reboot again to avoid a boot loop, continuing on the stale boot path",
			"stamp", stamp)
		return
	}
	if err := os.WriteFile(bootstrapRebootStampPath, []byte(stamp+"\n"), 0o644); err != nil {
		logger.Warn("bootstrap reboot: could not persist loop-guard stamp, refusing to reboot",
			"path", bootstrapRebootStampPath, "err", err)
		return
	}
	logger.Warn("bootstrap reboot: boot path was refreshed, rebooting once so the new run-override.sh/rc.local run this cycle instead of waiting for a manual power-cycle",
		"stamp", stamp)
	// Flush pending writes (the stick log, the bootstrap files and the
	// guard stamp on NAND) before we pull the rug out. busybox `sync`
	// keeps this portable at compile time, matching the reboot exec.
	_ = exec.Command("sync").Run()
	time.Sleep(2 * time.Second)
	if err := exec.Command("reboot").Run(); err != nil {
		logger.Error("bootstrap reboot: reboot command failed, continuing on stale boot path", "err", err)
	}
}

// stampVersionFiles writes this binary's version (semver + build stamp)
// to the on-box version.txt files so the desktop always sees the
// version that is actually running, not whatever the last stick-prep
// wrote. Without it, an OTA (which replaces only the binary) left
// version.txt at the old build and the box kept reporting the
// pre-update version (#94). NAND is the reliable target; the FAT32
// stick copy is best-effort (one small write, not in the boot-critical
// path). Atomic via tmp + rename; skipped where the parent dir is
// absent (dev host, no stick).
func stampVersionFiles(logger *slog.Logger) {
	stamp := version
	if buildStamp != "" && buildStamp != "dev" {
		stamp = version + "+" + buildStamp
	}
	for _, p := range []string{"/mnt/nv/streborn/version.txt", "/media/sda1/version.txt"} {
		dir := p[:strings.LastIndex(p, "/")]
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		tmp := p + ".str-new"
		if err := os.WriteFile(tmp, []byte(stamp+"\n"), 0o644); err != nil {
			logger.Debug("version stamp: write failed", "path", p, "err", err)
			continue
		}
		if err := os.Rename(tmp, p); err != nil {
			logger.Debug("version stamp: rename failed", "path", p, "err", err)
			_ = os.Remove(tmp)
		}
	}
}

// Best-effort: if sshd is already running, the init script
// no-ops; if no sshd init script exists (unexpected on Bose
// firmware), we just log and continue.
func ensureSshdRunning(logger *slog.Logger) {
	// Cheap pre-check: avoid spawning the init script if sshd is
	// already up — saves a fork on every agent restart.
	if out, err := exec.Command("pidof", "sshd").Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return
	}
	for _, attempt := range [][]string{
		{"/etc/init.d/sshd", "start"},
		{"/usr/sbin/sshd"},
	} {
		cmd := exec.Command(attempt[0], attempt[1:]...)
		if err := cmd.Run(); err == nil {
			logger.Info("sshd started", "via", attempt[0])
			return
		}
	}
	logger.Warn("sshd start: no usable init script found, SSH will not come up from agent")
}

// nandLogPath is the persistent log file on UBIFS. It is captured in
// full by the diagnostic bundle (unlike /tmp/streborn-agent.log which
// the bundle only grabs the last 8 KB of). All slog output is mirrored
// here so remote-box bug reports include the whole agent startup, not
// just the tail after the listener loops have already settled.
const nandLogPath = "/mnt/nv/streborn/agent.log"

// nandLogMax caps the persistent log so a long-running agent does not
// fill the small NAND volume (~31 MB, shared with the Bose firmware). On
// overflow the file is rotated to agent.log.1 and a fresh agent.log starts,
// so the pair holds at most 2x this. 256 KiB still covers several fresh boots
// of debug output while keeping the log footprint well under 512 KiB;
// run.sh's cleanup_nand trims it further on each boot.
const nandLogMax = 256 * 1024

// logResourceHealth records a one-line snapshot of available memory and
// system load. On this hardware (~120 MB RAM, no swap) a slow leak ends
// in an OOM freeze; this heartbeat makes the RAM/load trend leading up to
// such a freeze visible in the on-box log for post-mortem analysis.
// Best-effort: missing /proc entries just log -1.
func logResourceHealth(logger *slog.Logger) {
	avail, total := readMemKB()
	rss, threads := readSelfRSS()
	logger.Info("resource health",
		"memAvailableKB", avail,
		"memTotalKB", total,
		"loadavg", readLoadAvg(),
		// The agent's own RSS and thread count. If memAvailable trends
		// down while these stay flat, the leak is BoseApp's (firmware);
		// if these climb too, it is ours. This attributes the leak that
		// precedes the recurring BoseApp freeze without guesswork.
		"agentRSSKB", rss,
		"agentThreads", threads)
}

// memory-guard tunables. The Spotify Ogg path leaves a residual box-side
// firmware leak (~1.3 MB/min while playing) that only a reboot frees (pause,
// standby and re-push do not). The guard reboots the box ONLY when memory is
// critically low AND nothing is playing, so the leak is reset during idle and
// never causes an OOM mid-playback. When idle the leak does not grow, so the
// low reading is stable and there is no race with the 5-minute cycle.
const (
	// Live observation (2026-06-05, 35 min continuous Spotify with the 16 KB
	// flush fix): memAvail declined to a self-limiting floor of ~9 MB (brief
	// dips to ~4.4 MB) and the box did NOT OOM/reboot. So this is a true-OOM
	// backstop only: 6 MB sits below the normal ~9 MB idle floor (no reboot
	// after a normal session) yet above the danger zone. When idle the leak
	// does not grow, so a low reading is stable and the 5-min cycle is fine.
	memGuardThresholdKB  = 6 * 1024
	memGuardMinUptimeSec = 900 // never reboot in the first 15 min (boot-loop guard)
)

// memoryGuardCheck reboots the box when free memory is critically low and the
// box is idle, to clear the accumulated firmware leak from Spotify playback.
// Conservative and heavily logged: it skips while Spotify streams, while the
// box is playing anything (do not interrupt radio either), and early after
// boot. The reboot itself clears the condition, so no loop stamp is needed.
func memoryGuardCheck(logger *slog.Logger, sp *spotify.Manager, boxHost string) {
	avail, _ := readMemKB()
	if avail < 0 || avail > memGuardThresholdKB {
		return // healthy
	}
	if up := readUptimeSec(); up >= 0 && up < memGuardMinUptimeSec {
		logger.Warn("memory guard: low memAvail but uptime too short, holding off", "memAvailKB", avail, "uptimeSec", up)
		return
	}
	if sp != nil && sp.Streaming() {
		logger.Warn("memory guard: low memAvail but Spotify is streaming, not rebooting", "memAvailKB", avail)
		return
	}
	if boxIsPlaying(boxHost) {
		logger.Warn("memory guard: low memAvail but box is playing, not rebooting", "memAvailKB", avail)
		return
	}
	logger.Warn("memory guard: memAvail critically low while idle, rebooting box to clear firmware leak", "memAvailKB", avail)
	_ = exec.Command("sync").Run()
	if err := exec.Command("reboot").Run(); err != nil {
		logger.Error("memory guard: reboot failed", "err", err)
	}
}

// readUptimeSec returns system uptime in seconds, or -1 on error.
func readUptimeSec() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return -1
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return -1
	}
	sec, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return -1
	}
	return int64(sec)
}

// boxIsPlaying reports whether the Bose box is actively rendering audio (any
// source), so the memory guard never reboots mid-playback. Best-effort: any
// error or a non-play state counts as not playing.
func boxIsPlaying(boxHost string) bool {
	if boxHost == "" {
		boxHost = "127.0.0.1"
	}
	cl := &http.Client{Timeout: 4 * time.Second}
	resp, err := cl.Get("http://" + boxHost + ":8090/now_playing")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	s := string(b)
	return strings.Contains(s, "PLAY_STATE") || strings.Contains(s, "BUFFERING_STATE")
}

func readSelfRSS() (rssKB, threads int64) {
	rssKB, threads = -1, -1
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "VmRSS:":
			rssKB, _ = strconv.ParseInt(f[1], 10, 64)
		case "Threads:":
			threads, _ = strconv.ParseInt(f[1], 10, 64)
		}
	}
	return
}

func readMemKB() (avail, total int64) {
	avail, total = -1, -1
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "MemAvailable:":
			avail, _ = strconv.ParseInt(f[1], 10, 64)
		case "MemTotal:":
			total, _ = strconv.ParseInt(f[1], 10, 64)
		}
	}
	return
}

func readLoadAvg() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	if f := strings.Fields(string(b)); len(f) >= 3 {
		return f[0] + " " + f[1] + " " + f[2]
	}
	return ""
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	// Mirror to NAND so the diagnostic bundle sees more than the last
	// 8 KB of /tmp/streborn-agent.log. Best-effort: if NAND is read
	// only or full, fall back to stderr-only — agent must boot either
	// way. Rotation happens on open if the existing file already
	// exceeds nandLogMax.
	var writer io.Writer = os.Stderr
	if f := openNandLog(); f != nil {
		writer = io.MultiWriter(os.Stderr, f)
	}
	return slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: lvl}))
}

// openNandLog opens /mnt/nv/streborn/agent.log in append mode, rotating
// it to agent.log.1 first if it already exceeds nandLogMax. Returns
// nil on any error so the caller falls back to stderr-only.
func openNandLog() *os.File {
	if st, err := os.Stat(nandLogPath); err == nil && st.Size() > nandLogMax {
		// Best-effort rotate. Failure here just means we keep appending
		// to a slightly oversized log; not worth bailing.
		_ = os.Rename(nandLogPath, nandLogPath+".1")
	}
	f, err := os.OpenFile(nandLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "newLogger: NAND log unavailable, stderr only:", err)
		return nil
	}
	return f
}

// selfProbeTarget names a TCP endpoint the agent should be able to
// reach via loopback right after its listeners are spawned.
type selfProbeTarget struct {
	name string
	addr string // ":8888" — leading colon ok, normalised below
}

// runSelfProbe attempts loopback connect to each target every 2 s for
// the first 30 s, then once a minute for the next 5 minutes. Each
// outcome (ok / refused / timeout) is logged at WARN with the elapsed
// time since probe start, so the diagnostic bundle shows exactly when
// (or whether) each listener accepted its first connection.
//
// This is purely observational; the probe never restarts a listener.
// It is the inside-the-agent counterpart to run.sh's agent_port_bound,
// useful when BusyBox lacks ss/netstat and the bash probe is blind.
func runSelfProbe(ctx context.Context, logger *slog.Logger, targets []selfProbeTarget) {
	start := time.Now()
	probe := func() {
		for _, t := range targets {
			addr := t.addr
			if strings.HasPrefix(addr, ":") {
				addr = "127.0.0.1" + addr
			}
			elapsed := time.Since(start).Round(time.Second)
			d := net.Dialer{Timeout: 2 * time.Second}
			conn, err := d.DialContext(ctx, "tcp", addr)
			if err != nil {
				logger.Warn("self-probe: connect failed", "target", t.name, "addr", addr, "elapsed", elapsed.String(), "err", err.Error())
				continue
			}
			_ = conn.Close()
			logger.Debug("self-probe: connect ok", "target", t.name, "addr", addr, "elapsed", elapsed.String())
		}
	}

	// Phase 1: every 2 s for 30 s — listener bring-up window.
	fastTicker := time.NewTicker(2 * time.Second)
	defer fastTicker.Stop()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	probe()
fast:
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			break fast
		case <-fastTicker.C:
			probe()
		}
	}

	// Phase 2: once a minute for 5 minutes — covers slow boot variants.
	slowTicker := time.NewTicker(60 * time.Second)
	defer slowTicker.Stop()
	slowDeadline := time.NewTimer(5 * time.Minute)
	defer slowDeadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-slowDeadline.C:
			return
		case <-slowTicker.C:
			probe()
		}
	}
}
