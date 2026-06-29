// Command streborn is the agent that runs directly on the Bose SoundTouch
// box and emulates the Bose cloud endpoints locally.
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
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
	"github.com/JRpersonal/streborn/internal/boxsnapshot"
	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/boxws"
	"github.com/JRpersonal/streborn/internal/hosts"
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/netutil"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
	"github.com/JRpersonal/streborn/internal/shepherd"
	"github.com/JRpersonal/streborn/internal/spotify"
	"github.com/JRpersonal/streborn/internal/streamproxy"
	"github.com/JRpersonal/streborn/internal/syscheck"
	"github.com/JRpersonal/streborn/internal/sysinfo"
	"github.com/JRpersonal/streborn/internal/tlsgen"
	"github.com/JRpersonal/streborn/internal/upnp"
	"github.com/JRpersonal/streborn/internal/webhooks"
	"github.com/JRpersonal/streborn/internal/webui"
	"github.com/JRpersonal/streborn/internal/zones"
	usbstick "github.com/JRpersonal/streborn/usb-stick"
)

// version is the semver version. The build date is set separately via
// -ldflags so that "1.0.0" can be shown while the build date is still
// available.
var (
	version    = "1.0.0"
	buildStamp = "dev"
)

func init() {
	webui.SetAgentVersion(version)
	webui.SetAgentBuild(buildStamp)
}

func main() {
	// Handle subcommands before flag.Parse() so their own flags are not
	// swallowed by the global flag set.
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

// runShepherdCmd handles the shepherd subcommand.
// Invocations:
//
//	streborn shepherd install   -- set up /mnt/nv/shepherd
//	streborn shepherd remove    -- remove /mnt/nv/shepherd
//	streborn shepherd status    -- show the current state
func runShepherdCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: shepherd {install|remove|status}")
	}

	fs := flag.NewFlagSet("shepherd", flag.ContinueOnError)
	shepherdDir := fs.String("dir", shepherd.DefaultShepherdDir, "Shepherd override directory")
	boseDir := fs.String("bose-config", shepherd.DefaultBoseConfigDir, "Bose config directory")
	bin := fs.String("binary", shepherd.DefaultStickBin, "Path to the agent binary")
	presetsPath := fs.String("presets", shepherd.DefaultPresetsPath, "Path to presets.json")

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
		return fmt.Errorf("unknown subcommand: %s", cmd)
	}
}

// nandPresetsPath is the canonical on-NAND preset store. NAND (ubifs) survives
// reboots and the stick being removed; a stick mountpoint does not.
const nandPresetsPath = "/mnt/nv/streborn/presets.json"

// canonicalPresetsPath keeps the preset store on NAND. If the configured path
// is on removable media (a USB stick under /media or /run/media, the pre-NAND
// boot-script default), it redirects to nandPresetsPath so saves persist across
// a reboot, migrating a still-readable stick copy once if NAND has none yet
// (#120). Any other path (including an explicit /mnt/nv override) is left as-is.
func canonicalPresetsPath(p string, logger *slog.Logger) string {
	clean := filepath.Clean(p)
	if !strings.HasPrefix(clean, "/media/") && !strings.HasPrefix(clean, "/run/media/") {
		return p
	}
	if _, err := os.Stat(nandPresetsPath); os.IsNotExist(err) {
		if data, rerr := os.ReadFile(p); rerr == nil && len(data) > 0 {
			if mkErr := os.MkdirAll(filepath.Dir(nandPresetsPath), 0o755); mkErr == nil {
				if werr := os.WriteFile(nandPresetsPath, data, 0o644); werr == nil {
					logger.Warn("presets: migrated stick preset store to NAND", "from", p, "to", nandPresetsPath)
				} else {
					logger.Warn("presets: NAND migration write failed", "err", werr)
				}
			}
		}
	}
	logger.Warn("presets: redirecting removable-media preset path to NAND so it survives reboot",
		"flag", p, "using", nandPresetsPath)
	return nandPresetsPath
}

func run() error {
	var (
		presetsPath     = flag.String("presets", "/media/sda1/presets.json", "Path to presets.json on the USB stick")
		webuiAddr       = flag.String("listen-webui", ":8888", "Address for the config web UI")
		margeAddr       = flag.String("listen-marge", ":80", "Address for the marge emulation HTTP (streaming.bose.com)")
		margeTLSAddr    = flag.String("listen-marge-tls", ":8443", "Address for the marge emulation HTTPS")
		bmxAddr         = flag.String("listen-bmx", ":81", "Address for the BMX emulation HTTP (content.api.bose.io)")
		hostsPath       = flag.String("hosts", "/etc/hosts", "Path to the hosts file")
		applyHosts      = flag.Bool("apply-hosts", true, "Modify the hosts file on start and restore it on stop")
		tlsDir          = flag.String("tls-dir", tlsgen.DefaultCADir, "Directory for the CA and server certificate")
		tlsEnabled      = flag.Bool("tls", true, "Enable TLS termination on listen-marge-tls")
		logLevel        = flag.String("log-level", "info", "Log level: debug, info, warn, error")
		boxHost         = flag.String("box-host", "127.0.0.1", "Bose box IP for UPnP calls (webui /api/play). 127.0.0.1 when the agent runs on the box, otherwise the LAN IP.")
		regionFile      = flag.String("region-file", "", "Path to region.txt with the ISO country code (from the setup wizard). The default radio country and language are derived from it.")
		pendingNameFile = flag.String("pending-name-file", "", "Path to name.txt from the setup wizard. Its contents are applied once as the box name (plus a UID suffix) and the file is deleted afterwards.")
		printVersion    = flag.Bool("version", false, "Print the version and exit")
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

	// Determine the DeviceID from the MAC so marge responses return the
	// real box ID. If no MAC is found, continue with an empty ID.
	deviceID, err := sysinfo.DeviceID(nil)
	if err != nil {
		logger.Warn("could not determine DeviceID", "err", err)
		deviceID = ""
	} else {
		logger.Info("DeviceID detected", "deviceID", deviceID)
	}

	// Reclaim regenerable NAND junk once at startup. The writable NAND is tiny
	// (~31 MB, shared with the Bose firmware); an interrupted OTA can leave a
	// stale ~10 MB binary .new, and an older desktop app could leave a ~28 MB
	// streborn-install staging dir, either of which then blocks the next OTA and
	// can starve go-librespot. Today that junk is only swept inside an OTA write
	// or on a full run.sh reboot, so an agent that self-restarts (e.g. after an
	// OTA) never clears it. Doing it here lets a tight box self-heal on the next
	// agent start. Safe: never touches Bose files or the live binaries.
	webui.ReclaimNAND()

	// Load presets. On error do not crash but continue with an empty list, so
	// the agent at least stays alive on its listeners and remains correctable.
	//
	// Phase-marker logs at WARN level so a remote diagnostic bundle shows
	// exactly which path was taken — was the file there? was it empty?
	// did parse succeed? how many slots ended up in the in-memory store?
	// Without this, an "empty presets" report (#60) is indistinguishable
	// from a fresh install, a corrupt file, or an agent restart racing
	// the store load.
	// Presets MUST live on NAND so they survive a reboot. A box whose on-NAND
	// boot script predates the NAND-presets change launches the agent with
	// --presets pointing at the USB stick (the old default), and the stick is
	// removed after install: every save then lands on an absent mountpoint and
	// the presets vanish on the next reboot (#120). The bootstrap self-heal
	// above rewrites that boot script, but only takes effect a reboot later, so
	// also harden it here: if the flag points at removable media, redirect to
	// the canonical NAND path and migrate a still-readable stick copy once.
	*presetsPath = canonicalPresetsPath(*presetsPath, logger)

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

	// Webhook config (user-defined HTTP requests fired on a box trigger, e.g. the
	// remote thumbs keys -> a smart-home toggle). Persisted on NAND so it survives
	// a stick removal. Missing file is fine (empty config).
	webhooksStore, whErr := webhooks.Load("/mnt/nv/streborn/webhooks.json", logger.With("comp", "webhooks"))
	if whErr != nil {
		logger.Warn("webhooks config load failed, continuing with empty config", "err", whErr)
	}

	// Multiroom zone membership (#70 beta), persisted on NAND so a formed zone
	// auto-reforms after reboot/standby without the user re-grouping. Missing
	// file means standalone. Only the master box ever persists a zone.
	zonesStore, zErr := zones.Load("/mnt/nv/streborn/zones.json")
	if zErr != nil {
		logger.Warn("zones config load failed, continuing standalone", "err", zErr)
	}

	// Recently-played ring (#135), persisted on NAND (debounced; see the recent
	// package). A load error is non-fatal: start with an empty history.
	recentStore, rErr := recent.Load("/mnt/nv/streborn/recent.json")
	if rErr != nil {
		logger.Warn("recent history load failed, starting empty", "err", rErr)
	}

	// Modify the hosts file
	var hostsMgr *hosts.Manager
	if *applyHosts {
		hostsMgr = hosts.New(*hostsPath, logger)
		if err := hostsMgr.Apply(hosts.DefaultEntries()); err != nil {
			logger.Warn("hosts file could not be modified", "err", err)
			hostsMgr = nil
		}
	}

	// Initialize subsystems
	margeSrv := marge.New(logger.With("comp", "marge"),
		marge.WithDeviceID(deviceID),
		marge.WithReflectSourcesPath(boxsnapshot.ReflectPath()),
		marge.WithReflectSourceFormatPath("/mnt/nv/streborn/reflect-format"))
	bmxSrv := bmx.New(logger.With("comp", "bmx"))
	// The AutoPair manager is created up here so it can also be used in the
	// WS and webui handlers.
	autoPair := autopair.New(logger.With("comp", "autopair"), autopair.Config{
		BoxHost: *boxHost,
	})

	// Initial preset sync to the box in the background. The box must know all
	// presets as UPnP ContentItems so the hardware buttons can trigger the
	// nowSelectionUpdated WebSocket event with a location. Plus a periodic
	// reconciler (every 5 min) so inconsistencies caused by a box reboot or
	// Bose state resets are healed automatically — the user normally never
	// needs to press the "repair hardware buttons" button.
	go initialBoxPresetSync(store, *boxHost, logger)
	go periodicPresetReconcile(store, *boxHost, logger)

	// Read the region from a file on start (provisioned by the setup wizard).
	region := loadRegion(*regionFile, logger)

	// The stream proxy makes Bose ContentItems resistant to token expiry:
	// instead of the real CDN URL, Bose gets http://127.0.0.1:8888/stream/<slot>
	// and the stick agent reconnects internally on drops.
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
	const goLibrespotPath = "/mnt/nv/streborn/bin/go-librespot"
	// One-shot system check: record kernel/CPU/NEON/RAM/NAND and whether the
	// go-librespot sidecar is actually deployed, so every diagnostic shows the
	// prerequisites for a clean run. In particular it surfaces go_librespot=MISSING
	// (the binary ships only via the stick->NAND boot sync, not the agent OTA), the
	// real reason Spotify stays unavailable on a box never re-synced from a stick
	// (#45/#105) rather than a CPU/arch problem (the ST20 runs the same armv7l 3.14
	// kernel + NEON as the Portable where Spotify works).
	syscheck.Run(logger, goLibrespotPath)
	spotifyMgr := spotify.New(goLibrespotPath, "/mnt/nv/streborn/sp-cache", "ST Reborn", spotifyBox, logger.With("comp", "spotify"))

	webuiSrv := webui.New(*webuiAddr, logger.With("comp", "webui"),
		webui.WithPresets(store),
		webui.WithBoxHost(*boxHost),
		webui.WithBoxSnapshotPath(boxsnapshot.DefaultPath()),
		webui.WithLastPlayPath("/mnt/nv/streborn/last-play.json"),
		webui.WithReflectSourcesPath(boxsnapshot.ReflectPath()),
		webui.WithAutoPair(autoPair),
		webui.WithRegion(region),
		webui.WithRegionFile(*regionFile),
		webui.WithStreamProxy(streamProxySrv),
		webui.WithSpotifyStream(spotifyMgr.ServeOgg),
		webui.WithSpotifyControl(func(ctx context.Context, uri, account string, shuffle bool) error {
			return spotifyMgr.PlayAccount(ctx, uri, account, spotify.PlayOptions{Shuffle: shuffle})
		}),
		webui.WithSpotifyUser(spotifyMgr.CurrentUsername),
		webui.WithSpotifyMeta(spotifyMgr.PlaylistMeta),
		webui.WithSpotifyStreaming(spotifyMgr.Streaming),
		webui.WithSpotifyReady(spotifyMgr.Ready),
		webui.WithSpotifyCanRecall(spotifyMgr.CanRecall),
		webui.WithSpotifyPremiumRequired(spotifyMgr.PremiumRequired),
		webui.WithSpotifyExportCred(spotifyMgr.ExportCredential),
		webui.WithSpotifyImportCred(spotifyMgr.ImportCredential),
		webui.WithSpotifySetRecalling(spotifyMgr.SetRecalling),
		webui.WithSpotifyInfo(spotifyMgr.ServeInfo),
		webui.WithSpotifyReload(spotifyMgr.ReloadBinary),
		webui.WithSpotifyStop(spotifyMgr.StopEngine),
		webui.WithSpotifySwitchedAway(spotifyMgr.SwitchedAway),
		webui.WithPeers(func(ctx context.Context) []webui.PeerLink {
			return browsePeers(ctx, logger.With("comp", "peers"))
		}),
		webui.WithWebhooks(webhooksStore),
		webui.WithZones(zonesStore),
		webui.WithRecent(recentStore))

	// Re-assert a persisted multiroom group (native or mirror) so it survives
	// reboot/standby/Wi-Fi outage without the user re-grouping (#70 beta).
	// No-op when standalone. Lives on the server so the mirror path can reach
	// the current stream + the UPnP renderer.
	go webuiSrv.PeriodicZoneReconcile()

	// Auto-re-push (#4): when the Bose renderer drops a proxied stream on its
	// own (reported: radio stops after ~11 min with no upstream error), the
	// webui resumes it conservatively (only if the box stays on and idle).
	streamProxySrv.SetOnDisconnect(webuiSrv.HandleStreamDisconnect)

	// ICY radio text: the proxy parses the live StreamTitle out of the
	// stream; push it to the box display by re-issuing the current stream URI
	// with the new title. Gated behind STR_ICY_DISPLAY inside the handler until
	// the mid-stream re-set is verified not to glitch audio on real hardware.
	streamProxySrv.SetOnTitle(webuiSrv.HandleStreamTitle)

	// Hardware preset buttons: the box sends a presetSelectionUpdated event via
	// WebSocket on 8080 (gabbo protocol) when the user physically presses a
	// button. We hook the event and trigger our UPnP player.
	renderer := upnp.NewBoseRenderer(*boxHost)
	wsHandler := &presetWsHandler{
		logger:   logger.With("comp", "boxws"),
		store:    store,
		renderer: renderer,
		autoPair: autoPair,
		boxHost:  *boxHost,
		spotify:  spotifyMgr,
		// A box/remote stop seen over gabbo tells the webui to hold the
		// auto-re-push, so a deliberate stop is not immediately undone.
		onUserStop: webuiSrv.NoteUserStop,
		webhooks:   webhooksStore,
		// Record hardware-preset recalls so the wake-resume + auto-re-push know
		// what to bring back.
		noteLastPlay: webuiSrv.NoteLastPlay,
		// Record hardware-preset presses into Recently-played (#135); the hardware
		// recall bypasses the webui play handlers that capture app-driven plays.
		noteRecentPreset: webuiSrv.NoteRecentPreset,
		// Power-on resume (Bose-style power-on preset, default on): a power press
		// resumes the last station; ResumeLastPlay is gated by the per-box opt-out
		// and a zone-membership guard so a stereo-pair self-wake never auto-resumes.
		onPowerWake: webuiSrv.ResumeLastPlay,
		// Recover a lost first press after a deep-standby wake (#183): when the box
		// reappears awake-but-idle on a gabbo reconnect, re-push the last stream.
		// Reuses the power-on resume guards (opt-out, zone, user-stop).
		onBoxReconnect: webuiSrv.RecoverAfterReconnect,
		// Clear the transport when the box powers off STR's UPnP source, so ST20
		// (scm) firmware that bounces UPNP<->STANDBY does not turn itself back on
		// (#197). Zone-guarded and debounced in the webui.
		onEnterStandby: webuiSrv.HandleEnterStandby,
		// Let the hardware-recall verify stand down when the user powered the box
		// off mid-recall, so it does not re-push the stream into a power-off (#197).
		recentlyPoweredOff: webuiSrv.RecentlyPoweredOff,
		// Surface the box's own presets (incl. foreign sources like Deezer) to the
		// webui so the app can show/preserve them (Option C). Map boxws -> webui at
		// the composition root to keep the two packages decoupled.
		noteBoxPresets: func(bps []boxws.BoxPreset) {
			out := make([]webui.BoxPreset, 0, len(bps))
			for _, p := range bps {
				out = append(out, webui.BoxPreset{
					Slot: p.Slot, Source: p.Source, Type: p.Type, Location: p.Location,
					SourceAccount: p.SourceAccount, Name: p.Name,
				})
			}
			webuiSrv.NoteBoxPresets(out)
		},
		// Let a hardware press of a queue preset (a saved DLNA folder) start the
		// webui play-queue instead of the single-track recall.
		recallSlot: webuiSrv.RecallSlot,
	}
	// When the user starts playback from the Spotify app (selecting this device)
	// while the box is on another source, point the box at the Spotify stream so
	// it actually plays instead of staying on the current source (#14).
	spotifyMgr.SetOnActivate(func(cbCtx context.Context) {
		if *boxHost != "" {
			wctx, cancel := context.WithTimeout(cbCtx, 8*time.Second)
			_ = boxcli.WakeAndWait(wctx, *boxHost, 6*time.Second, logger)
			cancel()
		}
		pctx, cancel := context.WithTimeout(cbCtx, 15*time.Second)
		if err := renderer.PlayURLMime(pctx, spotifyStreamURL, "Spotify", "", "audio/ogg"); err != nil {
			logger.Warn("spotify: auto-switch box to Spotify stream failed", "err", err)
		}
		cancel()
	})
	// Record each Spotify song into the recently-played ring under the active
	// Spotify card (#135), so its card shows the songs that played, not just the
	// playlist frame. No-op until a Spotify card has been recorded via a recall.
	spotifyMgr.SetOnTrack(webuiSrv.NoteRecentSpotifyTrack)
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

	// Box WebSocket listener for hardware preset buttons
	wg.Add(1)
	go func() {
		defer wg.Done()
		wsClient.Run(ctx)
	}()

	// Spotify preset audio plane (#78, P1): supervise librespot. Idles
	// (returns immediately) until a credential is cached, so it is safe to
	// start unconditionally.
	go spotifyMgr.Run(ctx)

	// Capture the box's presets + sources ONCE, as early as possible, before
	// STR's marge takeover makes the box drop account-linked cloud sources it
	// had (Deezer, Amazon, ...) and the presets bound to them. Persisted to
	// NAND write-once; served to the app via /api/box/snapshot so it can warn
	// the user and show what was there. See internal/boxsnapshot.
	go boxsnapshot.Capture(ctx, *boxHost, boxsnapshot.DefaultPath(), logger.With("comp", "boxsnapshot"))

	// Auto-pair background: pairs the box automatically on start. Re-pairs
	// every 5 minutes in case the box is ever lost. Plus: the WS handler
	// triggers TriggerNow on a preset press so pairing happens immediately
	// after waking from standby.
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
		health := time.NewTicker(5 * time.Minute)
		defer health.Stop()
		// The guard polls far more often than the heartbeat log: a runaway can
		// fall from a safe level toward OOM well within a 5-minute window.
		guard := time.NewTicker(60 * time.Second)
		defer guard.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-health.C:
				logResourceHealth(logger)
			case <-guard.C:
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
	// Let the version endpoint report the box name/model the announcer holds,
	// so the desktop app never has to fall back to "str-<ip>" when its own
	// /info probe is slow right after this agent restarts (#108).
	webuiSrv.SetBoxNameFn(func() (string, string) {
		mdnsMu.Lock()
		ann := mdnsAnnouncer
		mdnsMu.Unlock()
		return ann.Snapshot()
	})
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

	// If the USB stick has a newer run.sh than the NAND run-override.sh:
	// copy it. This is the self-update path for the bootstrap. Without it
	// the old run-override.sh from the very first setup runs forever and new
	// setup wizard configs are ignored.
	go syncRunOverrideFromStick(logger)

	// TLS termination for marge on 8443. iptables redirects the real box
	// request from 443 to it. Skip when TLS is disabled.
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

	// Persist the recently-played tail on a clean shutdown so it survives the
	// reboot (the in-flight debounce timer may not have fired yet). #135.
	if err := recentStore.Flush(); err != nil {
		logger.Warn("recent history flush on shutdown failed", "err", err)
	}

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

// httpErrorLogWriter bridges the stdlib http.Server ErrorLog into slog. The
// Bose firmware opens a TCP connection to the redirected streaming.bose.com TLS
// port (our marge-tls listener) about once a minute and closes it without ever
// sending a ClientHello, so net/http logged `http: TLS handshake error from
// <box>: EOF` to the process default logger every 60s. On the speaker that
// default logger is teed to the NAND log, so the benign probe churned ~1440
// NAND writes a day for no diagnostic value (seen across every box in #185 /
// #187 bundles). Route that handshake noise to DEBUG (dropped at the default
// info level) while keeping any genuine server error (e.g. a recovered handler
// panic, which also reaches ErrorLog) at WARN so it still lands in a bundle.
type httpErrorLogWriter struct {
	logger *slog.Logger
	name   string
}

func (w httpErrorLogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	if msg == "" {
		return len(p), nil
	}
	if strings.Contains(msg, "TLS handshake error") {
		w.logger.Debug("http server error", "comp", w.name, "msg", msg)
	} else {
		w.logger.Warn("http server error", "comp", w.name, "msg", msg)
	}
	return len(p), nil
}

// newHTTPErrorLog returns the *log.Logger to wire into http.Server.ErrorLog so
// per-connection noise does not bypass slog and hit the NAND log directly.
func newHTTPErrorLog(logger *slog.Logger, name string) *log.Logger {
	return log.New(httpErrorLogWriter{logger: logger, name: name}, "", 0)
}

// startHTTP starts an HTTP server in a goroutine and reports errors to errs.
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
			ErrorLog:          newHTTPErrorLog(logger, name),
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

// startHTTPS starts an HTTPS server analogous to startHTTP, with the
// supplied TLS configuration.
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
			ErrorLog:          newHTTPErrorLog(logger, name),
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

// presetWsHandler implements boxws.Handler and, on a hardware preset
// button press, calls the UPnP renderer with the stream URL from the preset store.
type presetWsHandler struct {
	logger   *slog.Logger
	store    *presets.Store
	renderer *upnp.Renderer
	autoPair *autopair.Manager
	boxHost  string
	spotify  *spotify.Manager
	// onUserStop is invoked when the box reports a deliberate playback stop
	// over gabbo (STOP_STATE). Wired to webui.NoteUserStop so the auto-re-push
	// does not fight a wanted stop. nil-safe.
	onUserStop func()
	// webhooks fires the user-configured HTTP request on a "thumb" trigger (a
	// lone userActivityUpdate, see OnThumbActivity). nil-safe.
	webhooks *webhooks.Store
	// noteLastPlay records a hardware-preset recall as the webui's lastPlay so
	// the auto-re-push and the wake-resume know what to resume (the hardware path
	// plays straight through the renderer, bypassing the webui's own lastPlay).
	// Wired to webui.NoteLastPlay. nil-safe.
	noteLastPlay func(boxURL, title, art, mime string)
	// noteRecentPreset records a hardware-preset press into the recently-played
	// ring (#135). Wired to webui.NoteRecentPreset. nil-safe.
	noteRecentPreset func(presets.Preset)
	// onPowerWake is invoked when the box leaves standby on a power press: a
	// powerStateUpdated on firmware that sends it, or the DO_NOT_RESUME selection
	// restore on firmware that does not (Portable/taigan). Resumes the last
	// station, the Bose-style power-on preset. Wired to webui.ResumeLastPlay, which
	// is gated by the per-box opt-out and a zone-membership self-wake guard.
	// nil-safe.
	onPowerWake func()
	// onBoxReconnect fires after the gabbo WS (re)connects. After a deep/overnight
	// standby the box wakes and emits its first preset/now-selection frame before
	// STR has reconnected, so the press is lost and nothing plays until a second
	// press (#183). This recovers that stuck wake. Wired to
	// webui.RecoverAfterReconnect, which reuses the power-on resume guards. nil-safe.
	onBoxReconnect func()
	// onEnterStandby fires when STR's UPnP source drops to STANDBY (a power-off
	// seen over gabbo). It clears the box transport so ST20 (scm) firmware that
	// oscillates UPNP<->STANDBY does not switch the speaker back on (#197). Wired to
	// webui.HandleEnterStandby, which is zone-guarded and debounced. nil-safe.
	onEnterStandby func()
	// recentlyPoweredOff reports whether STR saw this box drop UPNP->STANDBY within
	// the bounce window. The hardware-preset recall verify (verifyPlayURL) checks it
	// so it does NOT re-push the stream when the user powered the box off mid-recall
	// (the box reads "not playing" because it is in standby), which on scm ST20
	// firmware switched the speaker back on (#197). Wired to webui.RecentlyPoweredOff.
	// nil-safe.
	recentlyPoweredOff func() bool
	// noteBoxPresets records the box's OWN preset list (gabbo presetsUpdated),
	// including foreign sources like Deezer that STR did not set, into the webui
	// so the app can show and preserve them (Option C). Wired to
	// webui.NoteBoxPresets. nil-safe.
	noteBoxPresets func([]boxws.BoxPreset)
	// recallSlot lets the webui claim a hardware preset press for a queue preset
	// (a saved DLNA folder): it returns true and starts the play-queue when the
	// slot is a queue preset, false otherwise so the single-track recall below
	// still runs. The queue lives in webui, so the queue start happens there.
	// Wired to webui.Server.RecallSlot. nil-safe.
	recallSlot func(ctx context.Context, slot int) bool
}

// OnPresetsChanged forwards the box's own preset list to the webui (Option C).
func (h *presetWsHandler) OnPresetsChanged(_ context.Context, presets []boxws.BoxPreset) {
	if h.noteBoxPresets != nil {
		h.noteBoxPresets(presets)
	}
}

func (h *presetWsHandler) OnPresetSelected(ctx context.Context, slot int, location, title string) {
	// Per-key webhook (beta): fire the configured "preset<slot>" webhook on a
	// hardware preset press (front panel or remote; app recalls take a different
	// path and never reach here). In replace mode, withhold the preset playback
	// so only the webhook runs (the user has cleared the STR preset for this
	// slot); in additional mode, the preset plays AND the webhook fires.
	if h.webhooks != nil && slot >= 1 && slot <= 6 {
		id := fmt.Sprintf("preset%d", slot)
		replace := h.webhooks.ButtonReplaceEnabled(id)
		if h.webhooks.FireButton(ctx, id) {
			h.logger.Info("preset webhook fired", "slot", slot, "replace", replace)
		}
		if replace {
			h.logger.Info("preset webhook replace mode: withholding preset playback", "slot", slot)
			return
		}
	}
	// A queue preset (a saved DLNA folder) is recalled by the webui's play-queue,
	// not the single-track UPnP play below. Let it claim the press; it returns
	// true (and starts the queue) only for a queue preset, so every other preset
	// type falls through to the existing behaviour unchanged.
	if h.recallSlot != nil && h.recallSlot(ctx, slot) {
		if h.noteRecentPreset != nil {
			if p, ok := h.store.Get(slot); ok {
				h.noteRecentPreset(p)
			}
		}
		return
	}
	// The URL stays the proxy URL (location = http://127.0.0.1:8888/stream/N)
	// so the stream proxy handles the reconnect on token expiry. Name + icon
	// come from the stick preset store — the Bose ContentItem metadata has no
	// art entry, so we must actively pack the album art URL into the DIDL-Lite
	// metadata via our PlayURL call, otherwise the display (ST20/30) shows no
	// logo.
	url := location
	name := title
	icon := ""
	if p, ok := h.store.Get(slot); ok {
		// Recently-played (#135): record the pressed preset (radio or Spotify)
		// from the authoritative store entry, before the source-specific recall.
		if h.noteRecentPreset != nil {
			h.noteRecentPreset(p)
		}
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
		// The box's ContentItem location can be a STALE Bose-cloud reference on a
		// preset that predates STR or was never re-synced: e.g. a pre-shutdown
		// TuneIn entry with location="/v1/playback/station/..." source=TUNEIN.
		// Playing that fails with UPnP 402 "No URI supplied" and verifyPlayURL then
		// retries it in a storm, so the button looks dead and the box churns
		// (#45/#105, Brecht 2026-06-20). Our store holds the authoritative STR
		// proxy URL, so prefer it whenever the box handed us something that is not
		// one of STR's own stream URLs.
		if p.StreamURL != "" && url != p.StreamURL && !isSTRStreamURL(url) {
			h.logger.Info("hardware preset: box location is not an STR stream, using store URL",
				"slot", slot, "boxLocation", url, "storeURL", p.StreamURL)
			url = p.StreamURL
		}
	}
	if url == "" {
		h.logger.Info("hardware preset pressed, no mapping", "slot", slot)
		return
	}
	// A stale cloud preset with no STR replacement (a TuneIn/relative location and
	// no store StreamURL) is not playable: the box answers SetAVTransportURI with
	// UPnP 402, and verifyPlayURL below would then hammer it in a retry storm.
	// Stand down with a clear, actionable log instead (re-save fixes it).
	if !isPlayableURL(url) {
		h.logger.Warn("hardware preset is a stale cloud entry (e.g. old TuneIn), not playable; re-save it in the app",
			"slot", slot, "location", url)
		return
	}

	// This is a radio (non-Spotify) recall. Tell the Spotify manager the user
	// switched away so its #14 auto-attach does not yank the box back to a
	// still-advancing go-librespot a second later (reported: Spotify->radio
	// played radio ~1s then jumped back to the Spotify preset).
	if h.spotify != nil {
		h.spotify.SwitchedAway(ctx)
	}

	// Wake the box from standby + ensure pairing.
	if h.boxHost != "" {
		wakeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		if err := boxcli.WakeAndWait(wakeCtx, h.boxHost, 6*time.Second, h.logger); err != nil {
			h.logger.Warn("could not bring box out of STANDBY", "err", err)
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
	// Record this hardware recall as the last play so the auto-re-push and the
	// power-button wake-resume know what to bring back (the webui only tracks its
	// own soft plays otherwise).
	if h.noteLastPlay != nil {
		h.noteLastPlay(url, name, icon, "")
	}
	// Verify+retry in the background: the first hardware press after a cold
	// boot can race the box/agent bringup so nothing plays until a second
	// press. This re-issues until the box actually plays. Affects radio too.
	go h.verifyPlayURL(slot, url, name, icon)
	h.logger.Info("hardware preset mapped to upnp", "slot", slot, "name", name)
}

// isSTRStreamURL reports whether u is one of STR's own stream URLs (the radio
// stream proxy or the Spotify Ogg passthrough), as opposed to a stale Bose
// ContentItem location that a re-sync has not yet replaced.
func isSTRStreamURL(u string) bool {
	return strings.Contains(u, "/stream/") || strings.Contains(u, "/spotify/")
}

// isPlayableURL reports whether u is an absolute HTTP(S) URL the UPnP renderer can
// actually load. Stale Bose-cloud ContentItems use relative, schemeless locations
// (e.g. "/v1/playback/station/...") that the box rejects with UPnP 402.
func isPlayableURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// --- Peer discovery for the on-box web UI "Other speakers" section ---

var (
	peersMu       sync.Mutex
	peersCache    []webui.PeerLink
	peersCachedAt time.Time
)

// browsePeers discovers the other STR speakers on the LAN over mDNS and returns
// a link to each one's web UI, so a phone on the on-box page can hop between
// speakers without re-typing an address. Resource-light by design: a short mDNS
// browse plus at most two TCP reachability probes per peer, and the whole result
// is cached, so repeated page loads cost at most one browse per cache window.
func browsePeers(ctx context.Context, logger *slog.Logger) []webui.PeerLink {
	const cacheTTL = 45 * time.Second
	peersMu.Lock()
	if !peersCachedAt.IsZero() && time.Since(peersCachedAt) < cacheTTL {
		c := peersCache
		peersMu.Unlock()
		return c
	}
	peersMu.Unlock()

	bctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	ch, err := discovery.Browse(bctx, logger)
	if err != nil {
		logger.Debug("peers browse failed", "err", err)
		return nil
	}
	mine := ownIPv4s()
	seen := map[string]bool{}
	var out []webui.PeerLink
	for inst := range ch {
		if inst.Kind != discovery.KindSTR {
			continue // only STR speakers, not stock Bose
		}
		ip, self := "", false
		for _, a := range inst.IPv4 {
			if mine[a] {
				self = true
				break
			}
			if ip == "" {
				ip = a
			}
		}
		if self || ip == "" || seen[ip] {
			continue
		}
		port := reachableWebPort(ip)
		if port == 0 {
			continue // neither web port answered; skip rather than link a dead URL
		}
		seen[ip] = true
		name := inst.FriendlyName
		if name == "" {
			name = inst.Name
		}
		out = append(out, webui.PeerLink{Name: name, URL: fmt.Sprintf("http://%s:%d/", ip, port)})
	}
	peersMu.Lock()
	peersCache = out
	peersCachedAt = time.Now()
	peersMu.Unlock()
	return out
}

// ownIPv4s returns this speaker's own LAN IPv4 addresses, used to drop the
// speaker itself from the discovered peer list.
func ownIPv4s() map[string]bool {
	m := map[string]bool{}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if v4 := ipn.IP.To4(); v4 != nil {
				m[v4.String()] = true
			}
		}
	}
	return m
}

// reachableWebPort returns a peer's externally reachable web port: STR's direct
// webui port (8888 on sm2/rhino/mojo) or the BCO REDIRECT port (17008 on
// taigan/scm), whichever accepts a connection; 0 when neither answers. This
// probe is why no per-model port has to be carried in mDNS.
func reachableWebPort(ip string) int {
	for _, p := range []int{8888, 17008} {
		if dialable(ip, p) {
			return p
		}
	}
	return 0
}

func dialable(ip string, port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// OnRemoteSkip handles the SoundTouch remote's next/prev track keys during
// Spotify playback: the box cannot skip a UPnP source itself (it emits
// QPLAY_SKIP_*_FAILED), so we skip in go-librespot instead. The new track
// reaches the box after its buffer drains. No-op unless Spotify is streaming.
func (h *presetWsHandler) OnRemoteSkip(ctx context.Context, forward bool) {
	if h.spotify == nil || !h.spotify.Streaming() {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var err error
	if forward {
		err = h.spotify.Next(sctx)
	} else {
		err = h.spotify.Prev(sctx)
	}
	if err != nil {
		h.logger.Warn("spotify remote skip failed", "forward", forward, "err", err)
		return
	}
	h.logger.Info("spotify remote skip", "forward", forward)
}

// OnUserStop is fired when the box reports a deliberate playback stop over
// gabbo. It tells the webui's auto-re-push to stand down so a wanted stop holds
// (v0.7.0: a single stop did not stick because the resume restarted it).
func (h *presetWsHandler) OnUserStop(_ context.Context) {
	if h.onUserStop != nil {
		h.onUserStop()
	}
}

// OnThumbActivity fires the user-configured webhook when the box reports a lone
// userActivityUpdate (the best available signal for a remote thumbs key on this
// firmware; up and down are indistinguishable, so it is a single toggle-style
// trigger). The detection + debounce live in boxws; here we just fire.
func (h *presetWsHandler) OnThumbActivity(ctx context.Context) {
	if h.webhooks == nil {
		return
	}
	h.webhooks.FireThumb(ctx)
}

// OnPowerKey fires the configured "power" webhook on a power-off (standby)
// event. Additional-only: STR cannot suppress the firmware power toggle. boxws
// only calls this on the standby transition, which STR never causes itself, so
// the webhook does not false-fire on STR's own wake-for-recall.
func (h *presetWsHandler) OnPowerKey(ctx context.Context) {
	if h.webhooks != nil {
		if h.webhooks.FireButton(ctx, "power") {
			h.logger.Info("power webhook fired")
		}
	}
}

// OnPowerWake resumes the last station when the speaker is switched on, the
// Bose-style power-on preset (default on, opt-out per box). boxws fires this on a
// power-on wake: a powerStateUpdated on firmware that sends one, or the
// DO_NOT_RESUME selection restore on firmware that does not. The actual resume
// (webui.ResumeLastPlay) is gated by the per-box setting and a zone-membership
// guard, so a stereo-pair self-wake (which looks identical on the wire) never
// makes the box start playing on its own.
func (h *presetWsHandler) OnPowerWake(_ context.Context) {
	if h.onPowerWake != nil {
		h.logger.Info("power-on detected, attempting last-station resume")
		h.onPowerWake()
	}
}

// OnConnected fires after the gabbo WebSocket (re)connects (boxws optional
// hook). It recovers the lost-first-press case (#183): when the box wakes from a
// deep/overnight standby it emits the preset/now-selection frame before STR has
// reconnected, so OnPresetSelected never runs and the display shows "service
// unavailable" until a second press. On reconnect STR checks the box and, if it
// is awake but its restored STR selection is not playing, re-pushes the last
// stream through the guarded resume (opt-out, zone, user-stop). A routine idle
// reconnect (box in standby) or a box already playing is a no-op.
func (h *presetWsHandler) OnConnected(_ context.Context) {
	if h.onBoxReconnect != nil {
		h.onBoxReconnect()
	}
}

// OnEnterStandby fires when the box's UPnP (STR) source drops to STANDBY on a
// power-off. It clears the transport so ST20 (scm) firmware that oscillates
// UPNP<->STANDBY does not switch the speaker back on (#197). boxws calls this via
// an optional interface, so only handlers that wire it (this one) react.
func (h *presetWsHandler) OnEnterStandby(_ context.Context) {
	if h.onEnterStandby != nil {
		h.onEnterStandby()
	}
}

// OnSourceAux fires the configured "aux" webhook when the box switches to the
// AUX input. Additional-only.
func (h *presetWsHandler) OnSourceAux(ctx context.Context) {
	if h.webhooks != nil {
		if h.webhooks.FireButton(ctx, "aux") {
			h.logger.Info("aux webhook fired")
		}
	}
}

// OnZoneChanged records the box's live multiroom/stereo-pair membership. Log
// only on purpose: the box may have formed this zone itself (AfterTouch / Bose
// app), and STR must NOT feed a box-native group into the reconcile store, or
// PeriodicZoneReconcile would try to re-form it via /setZone and fight the
// firmware's own pairing. The desktop multiroom tab already reads the live zone
// via /getZone polling and can dissolve it; this typed log makes box-formed
// groups visible in a diagnostic bundle instead of an "unrecognized frame".
func (h *presetWsHandler) OnZoneChanged(_ context.Context, z boxws.ZoneState) {
	if z.Master == "" {
		h.logger.Info("zone changed: dissolved")
		return
	}
	h.logger.Info("zone changed", "master", z.Master, "senderIsMaster", z.SenderIsMaster, "members", len(z.Members))
}

// spotifyStreamURL is the agent-local URL the box's UPnP renderer fetches for
// ad-hoc Spotify audio (see boxurl.SpotifyDefault for the .ogg-suffix rationale).
var spotifyStreamURL = boxurl.SpotifyDefault()

// playSpotifyPreset recalls a Spotify preset: wake + pair the box, tell
// go-librespot to play the saved URI (autonomous, no app), then point the
// box at the live /spotify/stream so it plays the audio over UPnP.
func (h *presetWsHandler) playSpotifyPreset(ctx context.Context, slot int, p presets.Preset) {
	// Log the inputs up front so a remote "recall does nothing" report (e.g.
	// ST20 #45) shows immediately which precondition failed: no Spotify
	// manager, no stored account/URI on the preset, or go-librespot not ready.
	h.logger.Info("spotify preset recall start", "slot", slot,
		"hasURI", p.URI != "", "account", p.Account, "type", p.Type, "spotifyMgr", h.spotify != nil)
	if h.spotify == nil {
		h.logger.Warn("spotify preset recall: no Spotify manager on this box", "slot", slot)
		return
	}
	if p.URI == "" {
		h.logger.Warn("spotify preset recall: preset has no Spotify URI, cannot autoplay", "slot", slot, "name", p.Name)
		return
	}
	// No live session AND no persisted Spotify login means go-librespot has no way
	// to start playback on its own, so a hardware press would do nothing but thrash
	// the box. Skip with a clear log instead of the silent retry loop (#45 Pierre).
	// Gate on CanRecall (live session OR persisted credential), NOT a persisted
	// credential alone: a box with a live-but-never-persisted zeroconf session
	// plays Spotify fine yet LoggedIn() is false, and gating on the credential
	// alone wrongly skipped its recall (Patrick, ST10, 2026-06-24). Mirror of the
	// soft/app path in internal/webui (the two recall paths must stay in sync).
	if !h.spotify.CanRecall(ctx) {
		h.logger.Warn("spotify preset recall: speaker not logged into Spotify and no live session; log it into Spotify once first", "slot", slot, "name", p.Name)
		return
	}
	if !h.spotify.Ready() {
		// Cold start: pressed right after boot, before go-librespot finished
		// authenticating. Wait briefly instead of doing nothing (which left the
		// box on the idle "select a preset" screen and forced a second press
		// once go-librespot was ready). Bounded so a genuinely unconfigured
		// manager does not hang the handler forever.
		h.logger.Info("spotify preset pressed before manager ready, waiting", "slot", slot)
		for i := 0; i < 24 && !h.spotify.Ready(); i++ {
			time.Sleep(500 * time.Millisecond)
		}
		if !h.spotify.Ready() {
			h.logger.Warn("spotify preset pressed but manager not ready after wait", "slot", slot)
			return
		}
	}
	// A free/open Spotify account cannot autonomously play a saved context, so a
	// hardware-button recall would silently produce no audio. Skip it and log the
	// reason rather than thrashing the box (#45). The desktop app surfaces the
	// "needs Premium" note; the hardware press has no UI to show it.
	if h.spotify.PremiumRequired() {
		h.logger.Warn("spotify preset recall: account is free/open, recall needs Premium; skipping", "slot", slot, "name", p.Name)
		return
	}

	// Mark a recall BEFORE the box attaches (PlayURLMime below / the box's own
	// self-activation) so ServeOgg does not resume the old mid-position track;
	// Play drives the new shuffled track from its start.
	h.spotify.SetRecalling()
	playCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	// Show the preset on the box display IMMEDIATELY: point the box at this
	// slot's stream first so now_playing shows the name and a buffering state
	// right away. Without this the box flashes its own "service unavailable /
	// select a preset" while we load, and the user, seeing no feedback, presses
	// another preset and causes chaos. The box buffers until go-librespot
	// produces audio just below. Uses the per-slot URL the box already
	// self-activated, so this re-confirms it rather than switching URLs.
	slotURL := boxurl.SpotifySlot(slot)
	if err := h.renderer.PlayURLMime(playCtx, slotURL, p.Name, p.Art, "audio/ogg"); err != nil {
		h.logger.Warn("spotify upnp play (display) failed, will verify+retry", "slot", slot, "err", err)
	}
	// Record this hardware recall as the last play so the power-button
	// wake-resume can bring the Spotify stream back.
	if h.noteLastPlay != nil {
		h.noteLastPlay(slotURL, p.Name, p.Art, "audio/ogg")
	}
	// Wake from standby + ensure pairing (the box is awake on a hardware press,
	// so these return fast); kept AFTER the display push so the buffering state
	// shows without waiting on them.
	if h.boxHost != "" {
		wakeCtx, c := context.WithTimeout(ctx, 8*time.Second)
		if err := boxcli.WakeAndWait(wakeCtx, h.boxHost, 6*time.Second, h.logger); err != nil {
			h.logger.Warn("could not bring box out of STANDBY", "err", err)
		}
		c()
	}
	if h.autoPair != nil {
		pairCtx, c := context.WithTimeout(ctx, 6*time.Second)
		h.autoPair.TriggerNow(pairCtx)
		c()
	}
	// Load the playlist (audio): a default preset resumes where the user left off
	// (shuffle off, in-order); a shuffle preset starts on a fresh random track.
	if err := h.spotify.PlayAccount(playCtx, p.URI, p.Account, spotify.PlayOptions{Shuffle: p.Shuffle}); err != nil {
		h.logger.Warn("spotify play (initial) failed, will verify+retry", "slot", slot, "err", err)
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
	// Up to 5 attempts (~25s): a box waking from a deep/overnight standby can
	// take longer than the old 3-attempt (~15s) window to finish bringing its
	// network and playback subsystem back up before it accepts the stream (#183).
	for attempt := 1; attempt <= 5; attempt++ {
		time.Sleep(5 * time.Second)
		if boxIsPlaying(h.boxHost) {
			return
		}
		// The user powered the box off during the recall: the box reads "not
		// playing" only because it is in standby. Re-pushing here re-arms the
		// transport the power-off cleared, which on scm ST20 firmware bounces the
		// speaker back on (#197, the "start via preset then power off" trigger). A
		// genuine deep-standby wake (#183) carries no recent power-off, so the
		// legitimate retry still runs.
		if h.recentlyPoweredOff != nil && h.recentlyPoweredOff() {
			h.logger.Info("hardware recall: box powered off mid-recall, not re-pushing (#197)", "slot", slot)
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
		// Success = the box is actually on the Spotify stream. Use the
		// location-aware check, not a bare play-state: a bounce-to-radio reads
		// as playing (would skip recovery -> double-tap) and a bare Streaming()
		// flaps to false even while Spotify plays (re-pointing on that flap
		// re-attaches and restarts the track). boxPlayingSpotify keys off the
		// now_playing location, so it is true only when Spotify really plays.
		if h.spotify.Streaming() || boxPlayingSpotify(h.boxHost) {
			return
		}
		// Stand down if the user powered the box off mid-recall, so the re-point
		// below does not re-wake a box the user just switched off (#197).
		if h.recentlyPoweredOff != nil && h.recentlyPoweredOff() {
			h.logger.Info("spotify recall: box powered off mid-recall, not re-pointing (#197)", "slot", slot)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		// Re-point the box at the stream WITHOUT re-Play: ServeOgg resumes
		// go-librespot on attach, so this re-attaches the box without
		// reshuffling/restarting the track. A re-Play here was the cause of the
		// "same song restarts a few seconds in" the user saw. Only the final
		// attempt does a full re-Play, to recover a genuine cold-boot auth race
		// where the playlist never loaded at all.
		if attempt == 3 {
			h.logger.Warn("spotify recall not playing, full re-Play (last resort)", "slot", slot)
			_ = h.spotify.PlayAccount(ctx, p.URI, p.Account, spotify.PlayOptions{Shuffle: p.Shuffle})
		} else {
			h.logger.Warn("spotify recall not playing yet, re-pointing box", "slot", slot, "attempt", attempt)
		}
		// Re-point at the PER-SLOT Ogg URL (not the default), matching the initial
		// recall and the soft path: each Spotify preset gets a unique box-side
		// location so two Spotify presets do not collide on one URL (#22).
		_ = h.renderer.PlayURLMime(ctx, boxurl.SpotifySlot(slot), p.Name, p.Art, "audio/ogg")
		cancel()
	}
	h.logger.Warn("spotify recall still not playing after retries", "slot", slot)
}

// loadRegion reads the country code from region.txt on the stick. Empty
// if the file does not exist or is empty; in that case the app later falls
// back to the browser/user default.
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
	logger.Info("region loaded", "country", out)
	return out
}

// syncRunOverrideFromStick keeps the NAND run-override.sh in sync with
// the run.sh on the stick. Important: rc.local prioritises NAND over the
// stick, so a stale NAND script would ignore the new setup wizard features
// (name.conf, region.conf, etc.).
//
// If the files are identical: no-op (no flash writes).
func syncRunOverrideFromStick(logger *slog.Logger) {
	const stickPath = "/media/sda1/run.sh"
	const nandPath = "/mnt/nv/streborn/run-override.sh"

	time.Sleep(5 * time.Second) // give the stick time to mount

	stickData, err := os.ReadFile(stickPath)
	if err != nil {
		logger.Debug("run.sh on stick not readable, skipping sync", "err", err)
		return
	}
	nandData, _ := os.ReadFile(nandPath)
	if len(nandData) > 0 && bytesEqual(stickData, nandData) {
		return // already identical
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

// applyPendingBoxName applies a box name left by the setup wizard once to
// the Bose box and appends the last 4 hex digits of the DeviceID as a UID
// suffix so duplicates across multiple boxes on the LAN are ruled out. On
// success the file is deleted.
func applyPendingBoxName(ctx context.Context, boxHost, path, deviceID string, logger *slog.Logger) {
	if boxHost == "" || path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		// no file, nothing to apply
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
	// The box must be reachable. Wait until the BoseApp web server is up.
	time.Sleep(10 * time.Second)
	c := boxapi.New(boxHost)
	for attempt := 0; attempt < 12; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.SetName(callCtx, wanted)
		cancel()
		if err == nil {
			logger.Info("setup wizard box name applied", "name", wanted)
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
	logger.Warn("could not set box name from setup, giving up", "path", path)
}

// pollBoxInfo polls the box /info regularly and keeps the mDNS TXT fields
// for FriendlyName and Model up to date. This way:
//
//  1. The desktop app knows the name as soon as the user renames the box
//     (e.g. via BoseApp HTTP), without a box reboot.
//  2. The model TXT field is promoted to the real value ("SoundTouch 10",
//     etc.) as soon as the Bose firmware serves /info on :8090. On the first
//     announce it still holds the generic fallback "SoundTouch" because :8090
//     typically comes up 20+ seconds after the agent start — the loop here
//     seals that race without blocking the boot.
//
// First round after a short delay, then with a short ticker until the model
// is detected (race recovery), after which the ticker drops back to 30s.
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
				logger.Info("box model detected", "type", model)
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

// proxyStreamURL returns the stable loopback URL for a preset. The Bose
// UPnP player opens it — the stream proxy in the stick agent resolves the
// real station redirect behind it and reconnects on token expiry without
// Bose noticing.
func proxyStreamURL(slot int) string {
	return boxurl.StreamSlot(slot)
}

// boxPresetURL is the location stored in the box's OWN preset slot. On a
// hardware press the box first tries to activate this stored ContentItem itself
// (before STR's recall takes over). Radio uses the per-slot stream proxy.
// Spotify must use the single live Ogg stream STR actually serves, not
// /stream/<slot> (which has no Spotify source): otherwise the box's own
// activation fails with INVALID_SOURCE and the display flashes "service
// unavailable" / "select a preset" before the recall (#22). Pointing it at
// /spotify/stream.ogg makes the box's own activation attach cleanly (it shows
// the preset name + buffers) until STR loads the right playlist.
func boxPresetURL(p presets.Preset) string {
	return boxurl.Preset(p.Slot, p.Type == "spotify")
}

// initialBoxPresetSync waits for the box to boot and syncs all stick
// presets to the box's internal preset store. With a retry loop: failed
// slots are retried after 10s, up to 12 times. Background: the Bose
// firmware is sometimes not yet ready for AddPreset calls at boot (autopair
// not done, marge state not initialised). Without retries, slots would stay
// permanently without a box entry — hardware buttons 1-6 would then trigger
// nothing. Initial 30 s wait (was 12 s): measured in practice that the Bose
// firmware needs ~60 s after a cold boot before /info on 8090 responds and
// the marge state is ready. 12 s was optimistic.
// 12 retry slots with a 10 s pause each = ~2 minutes of total runway.
func initialBoxPresetSync(store *presets.Store, boxHost string, logger *slog.Logger) {
	time.Sleep(30 * time.Second)
	specs := make([]boxcli.PresetSpec, 0, 6)
	for _, p := range store.All() {
		specs = append(specs, boxcli.PresetSpec{
			Slot: p.Slot, Name: p.Name, StreamURL: boxPresetURL(p),
		})
	}
	if len(specs) == 0 {
		return
	}
	logger.Info("starting initial box preset sync", "count", len(specs))

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

// periodicPresetReconcile checks every 5 minutes whether the box still has
// all stick presets in its own list. Missing slots are restored via
// boxcli.AddPreset. This way the fix applies automatically without user
// action when, e.g., the Bose firmware has lost individual entries after a
// standby cycle.
func periodicPresetReconcile(store *presets.Store, boxHost string, logger *slog.Logger) {
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
	//
	// Converge FAST after a cold boot, then idle. A blind 90s pre-wait
	// meant the hardware buttons stayed unregistered for ~90s+ after every
	// reboot, so an early press hit "button not assigned" (#4). The box
	// /info / preset subsystem comes up ~20-45s in, so polling every 10s from
	// 15s wins that first full re-sync as soon as the box is ready, then the
	// loop drops to a 5 min maintenance cadence. reconcileOnce is gated on the
	// box being out of OOB and reachable, so the early polls are cheap no-ops
	// until it is ready. The fast interval re-tightens automatically if the
	// box later drops back to OOB (ready=false -> fullDone=false).
	time.Sleep(15 * time.Second)
	fullDone := false
	for {
		ready := reconcileOnce(store, boxHost, logger, !fullDone)
		fullDone = ready
		if fullDone {
			time.Sleep(5 * time.Minute)
		} else {
			time.Sleep(10 * time.Second)
		}
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
				Slot: p.Slot, Name: p.Name, StreamURL: boxPresetURL(p),
			})
		}
	}
	if len(missing) == 0 {
		return true
	}
	if forceFull {
		logger.Info("preset reconcile: full re-sync after box became ready (registers hardware buttons)", "slots", len(missing))
	} else {
		logger.Info("preset reconcile: missing slots on box, syncing", "missing", len(missing))
	}
	errs := boxcli.SyncAllPresets(context.Background(), boxHost, missing)
	for slot, err := range errs {
		if err == nil {
			logger.Info("preset reconcile healed", "slot", slot)
		}
	}
	return true
}

// fetchBoxPresetSlots reads GET /presets from the Bose API and returns a
// map of which slot is set in the box's list.
func fetchBoxPresetSlots(boxHost string) (map[int]bool, error) {
	client := http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:8090/presets", boxHost))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := map[int]bool{}
	// Bose format: <presets><preset id="1" ...> ... </preset></presets>
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

// lastN returns the last n characters of s.
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
	memGuardThresholdKB = 6 * 1024
	// While Spotify is actively streaming the firmware leak is GROWING, so the
	// old "never interrupt playback" hold-off let it run to an uncontrolled OOM
	// (garbled audio then crash/reboot, live 2026-06-10). Below this critical
	// floor we reboot even during playback: a clean reboot + auto-resume beats
	// the firmware OOM. Set below the ~4.4 MB normal-session dip so it only fires
	// on a genuine runaway, not a healthy self-limiting session.
	memGuardCriticalKB   = 4 * 1024
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
	playing := (sp != nil && sp.Streaming()) || boxIsPlaying(boxHost)
	if playing && avail > memGuardCriticalKB {
		// Tolerate low memory during playback to avoid interrupting, but only down
		// to the critical floor. The Spotify manager's single-connection invariant
		// should keep steady playback stable; this is a last-resort net for any
		// runaway, where a controlled reboot + auto-resume beats the uncontrolled
		// firmware OOM (garbled audio then crash).
		logger.Warn("memory guard: low memAvail but playing and above critical, holding off", "memAvailKB", avail, "criticalKB", memGuardCriticalKB)
		return
	}
	logger.Warn("memory guard: memAvail critically low, rebooting box to clear firmware leak", "memAvailKB", avail, "playing", playing)
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

// boxPlayingSpotify reports whether the box's now_playing is STR's Spotify
// stream in a play/buffering state. It is the reliable success signal for the
// Spotify recall verify, where a bare play-state check (boxIsPlaying) and a
// bare Streaming() check each fail one way: right after a press the box can
// bounce off STR's preset to the PREVIOUS (radio) preset, which boxIsPlaying
// reads as "playing" and would wrongly skip recovery (the first-press
// double-tap); while Streaming() flaps to false even when the box is happily
// playing Spotify, and re-pointing on that flap re-attaches the box and
// restarts the track. The now_playing location tells the two apart.
func boxPlayingSpotify(boxHost string) bool {
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
	if !strings.Contains(s, "spotify/stream") {
		return false
	}
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
