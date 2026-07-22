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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/JRpersonal/streborn/internal/clocksync"
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
		pendingNameFile = flag.String("pending-name-file", "", "Path to name.txt from the setup wizard. Its contents are applied once as the box name, verbatim, and the file is deleted afterwards.")
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

	// Create the /etc/hosts manager now (so the shutdown path can Restore it),
	// but DEFER the actual redirect until the marge listeners answer — see the
	// deferred Apply after the listener boot below.
	var hostsMgr *hosts.Manager
	if *applyHosts {
		hostsMgr = hosts.New(*hostsPath, logger)
	}

	// Initialize subsystems
	margeSrv := marge.New(logger.With("comp", "marge"),
		marge.WithDeviceID(deviceID),
		marge.WithReflectSourcesPath(boxsnapshot.ReflectPath()),
		marge.WithReflectSourceFormatPath("/mnt/nv/streborn/reflect-format"),
		// The box re-reads its cloud presets from marge during every
		// setMargeAccount re-onboarding. Answering with an empty <presets/>
		// made the firmware WIPE its own hardware-key registrations after
		// every forced re-login ("Preset noch nicht festgelegt" until the
		// reconcile healed them minutes later). Serve the stick store live so
		// the cloud view always matches the keys.
		marge.WithPresetSource(func() []marge.Preset {
			all := store.All()
			out := make([]marge.Preset, 0, len(all))
			for _, p := range all {
				out = append(out, marge.Preset{
					ID:            p.Slot,
					Source:        "UPNP",
					Type:          "audio",
					Location:      boxPresetURL(p),
					SourceAccount: "UPnPUserName",
					ItemName:      margeXMLEscape(p.Name),
					ContainerArt:  margeXMLEscape(firstArtURL(p.Art)),
				})
			}
			return out
		}))
	// A configured account makes every legacy account/config probe answer
	// "signed in": some firmwares poll marge account endpoints that fell into
	// the UNCONFIGURED fallback, which reads as "not logged in" and feeds the
	// 1036 rejections on fresh installs (boxes with a cached pre-shutdown Bose
	// account never ask). Matches the ACTIVE account respondMargeAccountFull
	// already reports on the /streaming paths.
	margeSrv.SetAccount(&marge.AccountInfo{
		AccountUUID:  "streborn-local-account",
		AccountEmail: "stick@local",
		AuthToken:    "local-token-v1",
		CreatedAt:    "2026-01-01T00:00:00Z",
	})
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
	// Mirror a Spotify Connect volume change onto the whole multiroom group:
	// go-librespot runs only on the master, so feed it the current followers'
	// IPs. LIVE-verified on every use: zones.json deliberately outlives the
	// firmware zone (a member leaves to play its own source, a reboot drops
	// it) and its member IPs are stale DHCP hints, so trusting it raw made a
	// Connect volume change yank speakers that had left the group long ago.
	// Only the box's own /getZone says who follows RIGHT NOW; the persisted
	// zone is just the cheap precondition. Cached briefly because Connect
	// volume events arrive in bursts.
	if zonesStore != nil {
		var (
			gvMu  sync.Mutex
			gvAt  time.Time
			gvIPs []string
		)
		spotifyMgr.SetGroupSlaveIPsFn(func() []string {
			gvMu.Lock()
			defer gvMu.Unlock()
			if time.Since(gvAt) < 5*time.Second {
				return gvIPs
			}
			gvAt = time.Now()
			gvIPs = nil
			persisted, ok := zonesStore.Get()
			if !ok {
				return nil
			}
			gctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			z, err := boxapi.New(*boxHost).GetZone(gctx)
			if err != nil || z.Master == "" || !strings.EqualFold(z.Master, persisted.Master) {
				// No live zone (or led by someone else): nothing to fan to.
				return nil
			}
			for _, mem := range z.Members {
				// The member list can include the master itself; volume for
				// the master is already handled by the box mirror.
				if mem.IP == "" || strings.EqualFold(mem.DeviceID, z.Master) {
					continue
				}
				gvIPs = append(gvIPs, mem.IP)
			}
			return gvIPs
		})
	}

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
		webui.WithSpotifyContext(spotifyMgr.PlayingContext),
		webui.WithSpotifyMeta(spotifyMgr.PlaylistMeta),
		webui.WithSpotifyStreaming(spotifyMgr.Streaming),
		webui.WithSpotifySkip(func(ctx context.Context, forward bool) error {
			if forward {
				return spotifyMgr.Next(ctx)
			}
			return spotifyMgr.Prev(ctx)
		}),
		webui.WithSpotifyReady(spotifyMgr.Ready),
		webui.WithSpotifyCanRecall(spotifyMgr.CanRecall),
		webui.WithSpotifyPremiumRequired(spotifyMgr.PremiumRequired),
		webui.WithSpotifyExportCred(spotifyMgr.ExportCredential),
		webui.WithSpotifyImportCred(spotifyMgr.ImportCredential),
		webui.WithSpotifySetRecalling(spotifyMgr.SetRecalling),
		webui.WithSpotifySuppressActivate(spotifyMgr.SuppressActivate),
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

	// Auto-leave the out-of-box SETUP source. A box that installed STR over the
	// network but never finished Bose's app-driven onboarding keeps the SETUP
	// source active: the display shows "follow the SoundTouch app instructions"
	// and every play is refused, though the box is otherwise ready (live:
	// ST300 + scm-ST30, 2026-07-09). One POST /setup SETUP_LEAVE clears it and
	// UPnP radio plays. Watch for it and repair it so no user has to power-cycle.
	go leaveSetupSourceWatcher(context.Background(), *boxHost, logger)

	// Auto-re-push (#4): when the Bose renderer drops a proxied stream on its
	// own (reported: radio stops after ~11 min with no upstream error), the
	// webui resumes it conservatively (only if the box stays on and idle).
	streamProxySrv.SetOnDisconnect(webuiSrv.HandleStreamDisconnect)
	// Wedge detection (see internal/webui/wedge.go): the proxy's last-fetch /
	// last-failure timestamps tell a wedged box apart from a failing station.
	webuiSrv.SetStreamActivityFn(streamProxySrv.LastActivity)
	// Surface speaker-side failure states ("wedged", "login-error") in
	// /api/stream-status, so the app can name the real cause instead of
	// blaming the station and cycling radio-browser alternates.
	streamProxySrv.SetBoxStateFn(webuiSrv.BoxStateHint)

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
		// The physical remote Next/Prev take the hardware path, NOT the app's soft
		// skip: the box tears its UPnP source down on its own failed native skip, so
		// a layered go-librespot skip wedges it (3102). HardwareSkip recovers a
		// Spotify preset with a single clean slot recall instead (see its doc).
		onRemoteSkip: webuiSrv.HardwareSkip,
		webhooks:     webhooksStore,
		// Record hardware-preset recalls so the wake-resume + auto-re-push know
		// what to bring back. Returns the recall generation for supersession.
		noteLastPlay: webuiSrv.NoteLastPlay,
		// Supersession: a hardware verify stands down as soon as a newer play
		// (hardware or app) bumps the shared recall generation, mirroring the
		// soft path's verifyRecall guard ("pressed 2, got 1").
		recallGenFn: webuiSrv.RecallGeneration,
		// Wedge detection (power-cycle hint) fed from the hardware path too.
		noteRecallExhausted: webuiSrv.NoteRecallExhausted,
		noteBoxHealthy:      webuiSrv.NoteBoxHealthy,
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
		// The absolute variant is preferred: the rolling 6s window could expire
		// between verify ticks and let a re-push wake the powered-off box.
		recentlyPoweredOff: webuiSrv.RecentlyPoweredOff,
		standbyStopAfter:   webuiSrv.StandbyStoppedAfter,
		// A hardware preset press is the strongest possible "play" signal: it
		// clears any deliberate-stop latch an earlier (or spontaneous, #419)
		// power-off armed, so the recall is not suppressed by stale intent.
		noteUserPlay: webuiSrv.NoteUserPlay,
		// Ground truth for the recall verify: the box pulling THIS slot's proxied
		// stream (still open, or served a sustained stretch) proves it is playing
		// what the recall pushed, where now_playing can still name the previous
		// preset for seconds after the switch. Slot-scoped and liveness-aware so
		// neither cross-traffic nor a dead 36ms fetch can certify a failed
		// recall as healthy (#252).
		slotPulled: streamProxySrv.SlotPulledSince,
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
	// Tell the gabbo classifier about STR's OWN transport commands: the box
	// answers a SOAP Stop (and a SetURI flip) with a STOP_STATE frame that is
	// indistinguishable from the user pressing stop, and reading it as a user
	// stop latched a phantom stand-down that killed the very recall the command
	// belonged to (#252 post-v0.9.16: the wrong-state repair's Stop+ClearURI
	// aborted its own verify). Both renderer instances drive THIS box, so both
	// stamp the same classifier.
	renderer.OnTransportCommand = wsClient.NoteOwnTransportCommand
	webuiSrv.SetTransportCommandHook(wsClient.NoteOwnTransportCommand)
	// The standby classifier reads the same stamp: a source flip right after
	// STR's own push (a wake-resume/recall the firmware rejects) must not be
	// classified as a user power-off.
	webuiSrv.SetOwnTransportCmdFn(wsClient.LastOwnTransportCommand)
	// A completed (re-)onboarding wipes the box's hardware-key preset
	// registrations; re-register them right away instead of waiting for the
	// reconcile cadence.
	autoPair.SetOnPaired(func() { requestPresetKeyResyncUrgent(logger) })
	// Let the WebUI fill the Wi-Fi signal from the gabbo stream on BCO
	// boxes, whose /networkInfo reports no signal.
	webuiSrv.SetWifiSignalFn(wsClient.LastWifiSignal)
	// Let HandleEnterStandby tell a physical power-off (accompanied by a
	// userActivityUpdate key frame) from the firmware spontaneously powering
	// off STR's UPnP source (#419).
	webuiSrv.SetUserActivityFn(wsClient.LastUserActivity)

	// When the box rejects a source as not-logged-in (errorUpdate 1036, seen on
	// the SoundTouch 300), force a re-login and stand the recall retry down so
	// STR self-heals instead of thrashing the box into a wedge (rate-limited in
	// boxws).
	wsClient.SetOnLoginError(webuiSrv.NoteBoxLoginError)

	// Seed the box-native preset snapshot once at start and, if the NAND preset
	// store came up empty while the box still lists STR presets, restore what
	// the recently-played history can identify (#252: presets displayed as
	// "unassigned although they are assigned" and every hardware press 404ed
	// after a pre-v0.9.14 standby power-cut wiped presets.json and the OTA
	// restart surfaced the loss).
	go seedBoxPresetsAndRecoverStore(store, recentStore, *boxHost,
		func(bps []webui.BoxPreset) { webuiSrv.NoteBoxPresets(bps) },
		logger.With("comp", "presetrecovery"))

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
		go applyPendingBoxName(context.Background(), *boxHost, *pendingNameFile, logger)
	}

	// Correct an implausibly old system clock in the background. SoundTouch
	// speakers have no battery-backed RTC, so a cold boot can land in 2015, which
	// breaks TLS for HTTPS radio and the Spotify sidecar. run.sh syncs the clock
	// once at boot but can miss a network that is not up yet; this keeps retrying
	// from an HTTP Date header until a valid time is set, then exits (a no-op when
	// the clock is already fine). See internal/clocksync and #296.
	//
	// One synchronous attempt FIRST (short timeout), before the goroutine and
	// before we serve: on a stick-free network install run.sh never ran at
	// install time to do the boot Date sync, so without this the very first
	// requests could hit a 2015 clock until the goroutine catches up. Best-effort
	// - if the network is not up yet the goroutine below keeps retrying (#375).
	func() {
		sctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
		clocksync.SyncNowIfImplausible(sctx, logger)
	}()
	go clocksync.RunUntilSynced(ctx, logger, 30*time.Second)

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

	// Deferred /etc/hosts redirect. Applying it at agent start pointed the box's
	// Bose hosts (streaming.bose.com / content.api.bose.io) at 127.0.0.1 while
	// marge-TLS on :443 was not listening yet (that listener waits on first-boot
	// CA generation). On the BCO/scm SoundTouch 20 the box's NetManager runs a
	// connectivity probe against those hosts; a connection-refused during that
	// window is read as "no internet", so NetManager re-associates the Wi-Fi.
	// The scm ethernet-only path persists no Wi-Fi profile, so that re-associate
	// drops the speaker offline ("Wi-Fi Not Provided", #302/#303). Waiting until
	// the marge endpoint actually accepts closes the window; on healthy boxes the
	// redirect just lands a few seconds later, which is harmless.
	if hostsMgr != nil {
		waitAddr := *margeAddr
		if *tlsEnabled {
			waitAddr = *margeTLSAddr
		}
		go func() {
			waitListenerReady(ctx, waitAddr, 30*time.Second, logger)
			if ctx.Err() != nil {
				return
			}
			if err := hostsMgr.Apply(hosts.DefaultEntries()); err != nil {
				logger.Warn("hosts file could not be modified", "err", err)
			} else {
				logger.Info("hosts redirect applied after marge endpoint ready", "endpoint", waitAddr)
			}
		}()
	}

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
// waitListenerReady blocks until a plain TCP dial to the loopback side of addr
// succeeds (proving the local listener accepts), ctx is cancelled, or timeout
// elapses. Used to hold the /etc/hosts redirect until marge is actually up. A
// TLS listener still accepts the TCP connection, so this works for :443 too.
func waitListenerReady(ctx context.Context, addr string, timeout time.Duration, logger *slog.Logger) {
	var port string
	if _, p, err := net.SplitHostPort(addr); err == nil {
		port = p
	} else {
		port = strings.TrimPrefix(addr, ":")
	}
	target := net.JoinHostPort("127.0.0.1", port)
	deadline := time.Now().Add(timeout)
	for {
		d := net.Dialer{Timeout: time.Second}
		if c, err := d.DialContext(ctx, "tcp", target); err == nil {
			_ = c.Close()
			return
		}
		if ctx.Err() != nil {
			return
		}
		if time.Now().After(deadline) {
			if logger != nil {
				logger.Warn("marge endpoint not ready before timeout, applying hosts redirect anyway", "endpoint", target)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

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
	// lastUserStop is when OnUserStop last fired (guarded by lastUserStopMu).
	// The hardware recall verifies (verifyPlayURL / verifySpotifyPlaying)
	// compare it against their recall start so a deliberate stop DURING the
	// verify window stands the re-push down instead of being overridden --
	// stop-after-recall-start, mirroring the webui side's stand-down, not a
	// rolling window (an older stop must not suppress a recall the user just
	// asked for).
	lastUserStopMu sync.Mutex
	lastUserStop   time.Time
	// lastSourceReject is when the box last rejected STR's UPnP source with a 1036
	// UpnpRcvdContentItemInWrongState (a preset->preset switch racing the previous
	// source's teardown). verifySpotifyPlaying consults it so it re-points instead
	// of standing down on the box's "attached + buffering" appearance while the box
	// is actually stuck on that rejection and never plays (ST30 4->5, 2026-07-14).
	sourceRejectMu   sync.Mutex
	lastSourceReject time.Time
	// onRemoteSkip advances playback on a hardware remote Next/Prev key, source-
	// aware (Spotify or the STR play queue). Wired to webui.TransportSkip so the
	// hardware keys use the same skip logic as the phone remote; without it a
	// folder skip stalled until the current track ended naturally (#300). nil-safe.
	onRemoteSkip func(ctx context.Context, forward bool) (string, error)
	// webhooks fires the user-configured HTTP request on a "thumb" trigger (a
	// lone userActivityUpdate, see OnThumbActivity). nil-safe.
	webhooks *webhooks.Store
	// noteLastPlay records a hardware-preset recall as the webui's lastPlay so
	// the auto-re-push and the wake-resume know what to resume (the hardware path
	// plays straight through the renderer, bypassing the webui's own lastPlay).
	// Returns the new recall generation for the supersession check below.
	// Wired to webui.NoteLastPlay. nil-safe.
	noteLastPlay func(boxURL, title, art, mime string) uint64
	// recallGenFn reads the webui's current recall generation. A hardware
	// verify captures the generation its own noteLastPlay returned and stands
	// down as soon as the live value moves on: a newer play (second hardware
	// press or an app recall) supersedes the old loop, which otherwise kept
	// re-pushing its stale URL over the user's newest choice for up to ~26s
	// ("pressed 2, got 1"). The soft path has had exactly this guard
	// (verifyRecall's recallGen) all along. Wired to webui.RecallGeneration.
	// nil-safe.
	recallGenFn func() uint64
	// pressSeq orders the hardware presses themselves. It is bumped on the
	// gabbo read loop (strictly in press order) before the slow recall work is
	// handed to a goroutine, so a stale recall goroutine can recognise that a
	// newer press exists even before either has reached noteLastPlay.
	pressSeq atomic.Uint64
	// Wedge detection hooks (webui.NoteRecallExhausted / NoteBoxHealthy): the
	// hardware recall path reports exhausted/successful verifies so the
	// power-cycle hint also fires when the user only uses the preset keys.
	noteRecallExhausted func()
	noteBoxHealthy      func()
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
	// standbyStopAfter is the absolute variant of recentlyPoweredOff: it reports
	// a power-off strictly AFTER the given time (the press). The rolling 6s
	// window could expire between two verify ticks and let a re-push wake the
	// powered-off box after all; the absolute stamp cannot. Preferred when
	// wired; recentlyPoweredOff stays as the fallback. Wired to
	// webui.StandbyStoppedAfter. nil-safe.
	standbyStopAfter func(t time.Time) bool
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
	// noteUserPlay records a hardware preset press as an explicit user play in
	// the webui: it clears the deliberate-stop latches (a press outranks any
	// earlier stop, #419) and anchors the standby-flip discriminator. Wired to
	// webui.Server.NoteUserPlay. nil-safe.
	noteUserPlay func()
	// Test seams over the box CLI / now_playing probes. nil = the real
	// implementations (boxcli.WakeAndWait, boxcli.PowerOn, boxPlayingURL,
	// boxNowPlayingSource); the verify tests stub them so wake ordering and
	// the stuck-source nudge are assertable without hardware.
	wakeBox      func(ctx context.Context, host string) error
	sysPowerFn   func(ctx context.Context, host string) error
	boxPlayingFn func(url string) bool
	boxSourceFn  func() string

	// slotPulled reports whether the box is credibly playing THIS slot's
	// proxied stream for a recall anchored at the given time. It is the recall
	// verify's ground truth: the box pulling THIS slot's audio through the
	// proxy proves it plays what this recall pushed, whereas now_playing lags
	// and can still name the PREVIOUS preset seconds after the box switched.
	// Slot-scoped AND liveness-aware on purpose: the old global stamp let any
	// proxied fetch certify a failed recall, and even the slot stamp alone let
	// a 36ms fetch that died in the box's re-login bounce count as success
	// (#252 field bundles). Wired to streamproxy.Server.SlotPulledSince.
	// nil-safe.
	slotPulled func(slot int, since time.Time) bool
}

// slotPulledSince reports whether the box is credibly playing THIS slot's
// proxied stream for a recall anchored at t. A preset that plays a direct
// (non-proxied) URL never stamps; the now_playing check decides for it.
func (h *presetWsHandler) slotPulledSince(slot int, t time.Time) bool {
	if h.slotPulled == nil {
		return false
	}
	return h.slotPulled(slot, t)
}

// superseded reports whether a newer hardware press (pressSeq moved past seq)
// or a newer play of any kind (the webui recall generation moved past gen)
// exists. Verify/re-push loops of an older recall stand down on it instead of
// fighting the newest recall for the transport. gen==0 means the generation
// was never captured (no webui wired); only the press sequence decides then.
func (h *presetWsHandler) superseded(seq, gen uint64) bool {
	if h.pressSeq.Load() != seq {
		return true
	}
	if gen != 0 && h.recallGenFn != nil && h.recallGenFn() != gen {
		return true
	}
	return false
}

// wake brings the box out of standby (no-op without a configured box host or
// when the box is already awake). Seam-aware for the verify tests.
func (h *presetWsHandler) wake(ctx context.Context) error {
	if h.boxHost == "" {
		return nil
	}
	if h.wakeBox != nil {
		return h.wakeBox(ctx, h.boxHost)
	}
	return boxcli.WakeAndWait(ctx, h.boxHost, 6*time.Second, h.logger)
}

// sysPowerToggle sends one `sys power` toggle over the TAP CLI (the stuck-
// INVALID_SOURCE nudge). Seam-aware for the verify tests.
func (h *presetWsHandler) sysPowerToggle(ctx context.Context) error {
	if h.boxHost == "" {
		return nil
	}
	if h.sysPowerFn != nil {
		return h.sysPowerFn(ctx, h.boxHost)
	}
	return boxcli.PowerOn(ctx, h.boxHost)
}

// playingURL reports whether the box's now_playing points at url in a play
// state. Seam-aware for the verify tests.
func (h *presetWsHandler) playingURL(url string) bool {
	if h.boxPlayingFn != nil {
		return h.boxPlayingFn(url)
	}
	return boxPlayingURL(h.boxHost, url)
}

// currentBoxSource returns the box's now_playing source attribute ("" on any
// error). Seam-aware for the verify tests.
func (h *presetWsHandler) currentBoxSource() string {
	if h.boxSourceFn != nil {
		return h.boxSourceFn()
	}
	return boxNowPlayingSource(h.boxHost)
}

// poweredOffSince reports whether the user powered the box off after t,
// preferring the absolute stamp over the rolling window (see standbyStopAfter).
func (h *presetWsHandler) poweredOffSince(t time.Time) bool {
	if h.standbyStopAfter != nil {
		return h.standbyStopAfter(t)
	}
	if h.recentlyPoweredOff != nil {
		return h.recentlyPoweredOff()
	}
	return false
}

// OnPresetsChanged forwards the box's own preset list to the webui (Option C).
func (h *presetWsHandler) OnPresetsChanged(_ context.Context, presets []boxws.BoxPreset) {
	if h.noteBoxPresets != nil {
		h.noteBoxPresets(presets)
	}
}

func (h *presetWsHandler) OnPresetSelected(ctx context.Context, slot int, location, title string) {
	// Sequence + press time are taken while still on the gabbo read loop, so
	// rapid presses are ordered exactly as the user made them and every
	// stand-down check anchors to when the user actually pressed, not to when
	// STR got around to the recall.
	seq := h.pressSeq.Add(1)
	pressAt := time.Now()
	// Per-key webhook (beta): fire the configured "preset<slot>" webhook on a
	// hardware preset press (front panel or remote; app recalls take a different
	// path and never reach here). In replace mode, withhold the preset playback
	// so only the webhook runs (the user has cleared the STR preset for this
	// slot); in additional mode, the preset plays AND the webhook fires. The
	// replace decision is a config read and stays synchronous; the HTTP fire
	// runs in a goroutine because a slow webhook target stalled the read loop
	// for up to 8s per press, delaying every queued frame.
	if h.webhooks != nil && slot >= 1 && slot <= 6 {
		id := fmt.Sprintf("preset%d", slot)
		replace := h.webhooks.ButtonReplaceEnabled(id)
		go func() {
			if h.webhooks.FireButton(ctx, id) {
				h.logger.Info("preset webhook fired", "slot", slot, "replace", replace)
			}
		}()
		if replace {
			h.logger.Info("preset webhook replace mode: withholding preset playback", "slot", slot)
			return
		}
	}
	// The press is the user explicitly asking for playback: clear any stale
	// deliberate-stop latch BEFORE the recall so a preceding (or spontaneous,
	// #419) power-off cannot suppress it, and anchor the webui's standby-flip
	// discriminator so a source flip during this recall is not read as a
	// user power-off.
	if h.noteUserPlay != nil {
		h.noteUserPlay()
	}
	// Everything below talks to the box (SOAP, wake, pairing) and takes seconds
	// on a cold or wedged box - exactly the boxes whose presses were failing.
	// It used to run synchronously right here on the gabbo read loop, holding
	// up every queued frame (the teardown STOP_STATE, the press's own trailing
	// userActivityUpdate, 1036 errorUpdates, the user's next press) for up to
	// ~18s. All the teardown windows in boxws and the #419 power-off
	// discriminator in the webui measure at frame-PROCESSING time, so that
	// delay made them read the box's routine teardown as user intent: a
	// phantom user stop killed the verify, and the trailing key frame turned a
	// mid-recall standby flap into a "user power-off" that cleared the
	// transport - the ST20 that "switches itself off on every preset press"
	// and the ST30 whose remote presses never play (#252). The read loop must
	// keep draining; the recall runs beside it.
	go h.recallPreset(ctx, seq, pressAt, slot, location, title)
}

// recallPreset is the slow half of a hardware preset press: the queue-preset
// claim, the Spotify branch, URL selection, the UPnP push, wake, pairing and
// the background verify. Runs OFF the gabbo read loop; seq/pressAt come from
// the press event and drive supersession and the stand-down anchors.
func (h *presetWsHandler) recallPreset(ctx context.Context, seq uint64, pressAt time.Time, slot int, location, title string) {
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
	// mime is the DIDL protocolInfo label for the recall. Presets saved from an
	// AAC/HE-AAC station carry their codec; labelling them with the audio/mpeg
	// default made the box decode them as MPEG and play silence (#252).
	mime := ""
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
			h.playSpotifyPreset(ctx, seq, pressAt, slot, p)
			return
		}
		if p.Name != "" {
			name = p.Name
		}
		icon = p.Art
		// A preset stored before the codec was recorded has none, and an AAC
		// station then got the audio/mpeg default and played silence (#252). Read
		// the codec off the station URL in that case.
		mime = upnp.MimeForCodecOrURL(p.Codec, p.StreamURL)
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

	// Record this hardware recall as the last play BEFORE the push: the returned
	// recall generation is what stands every OLDER verify loop down, and bumping
	// it first means a stale loop cannot clobber this recall while the SetURI is
	// still in flight. The auto-re-push and the power-button wake-resume read
	// the same record to know what to bring back (the webui only tracks its own
	// soft plays otherwise); the mime rides along so re-pushes keep the AAC
	// label too (#252).
	var gen uint64
	if h.noteLastPlay != nil {
		if h.pressSeq.Load() != seq {
			// A newer press exists before this recall even pushed: never let the
			// older goroutine overwrite the newer press's lastPlay or URL.
			h.logger.Info("hardware recall superseded before push, standing down", "slot", slot)
			return
		}
		gen = h.noteLastPlay(url, name, icon, mime)
	}
	// Push the stream FIRST, before the wake and pairing, mirroring the Spotify
	// path. On a hardware/remote press the box is already awake (it just emitted
	// the gabbo frame) and briefly shows its own "Service Unavailable" flash from
	// failing to natively self-activate the UPNP preset; getting STR's SetURI in
	// as early as possible shortens that flash (#383). Pairing is not a
	// precondition for the SetURI, and a cold-standby race (UPnP 1036) is caught
	// by the background verifyPlayURL retry below.
	playCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var playErr error
	if mime != "" {
		playErr = h.renderer.PlayURLMime(playCtx, url, name, icon, mime)
	} else {
		playErr = h.renderer.PlayURL(playCtx, url, name, icon)
	}
	if playErr != nil {
		h.logger.Warn("upnp play (initial) failed, will verify+retry", "slot", slot, "err", playErr)
	}

	// Wake from standby (fast on a hardware press, the box is already awake) AFTER
	// the display push, so the SetURI is not delayed behind it.
	if h.boxHost != "" {
		wakeCtx, wcancel := context.WithTimeout(ctx, 8*time.Second)
		if err := boxcli.WakeAndWait(wakeCtx, h.boxHost, 6*time.Second, h.logger); err != nil {
			h.logger.Warn("could not bring box out of STANDBY", "err", err)
		}
		wcancel()
	}
	if h.autoPair != nil {
		// Fire-and-forget, mirroring the app-recall path (9a9b0c7): the
		// :8090 pair POST hangs for seconds on several firmwares, and running
		// it inline here kept the box's own "SERVICE NOT AVAILABLE" flash on
		// screen for that whole gap on every hardware press (#270). Pairing
		// is not a precondition for the UPnP push above.
		go func() {
			pairCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			h.autoPair.TriggerNow(pairCtx)
		}()
	}
	// Verify+retry in the background: the first hardware press after a cold
	// boot can race the box/agent bringup so nothing plays until a second
	// press. This re-issues until the box actually plays. Affects radio too.
	go h.verifyPlayURL(seq, gen, pressAt, slot, url, name, icon, mime)
	h.logger.Info("hardware preset mapped to upnp", "slot", slot, "name", name, "mime", mime)
}

// isSTRStreamURL reports whether u is one of STR's own stream URLs (the radio
// stream proxy or the Spotify Ogg passthrough), as opposed to a stale Bose
// ContentItem location that a re-sync has not yet replaced. Deliberately loose
// (substring): used only to PREFER the store URL over a box-provided location.
func isSTRStreamURL(u string) bool {
	return strings.Contains(u, "/stream/") || strings.Contains(u, "/spotify/")
}

// ownBoxPresetLocRe matches exactly the locations STR itself writes into the
// box's preset slots (boxurl.Preset / boxurl.StreamSlot / boxurl.SpotifySlot).
// The reconcile prune keys DELETION off this, so it must never match a foreign
// station URL that merely contains "/stream/".
var ownBoxPresetLocRe = regexp.MustCompile(`^http://127\.0\.0\.1:\d+/(?:stream/[1-6]|spotify/stream(?:-[1-6])?\.ogg)$`)

// isOwnBoxPresetLocation reports whether loc is a box-preset location STR
// itself wrote (strict match), the only shape the prune may remove.
func isOwnBoxPresetLocation(loc string) bool {
	return ownBoxPresetLocRe.MatchString(loc)
}

// isPlayableURL reports whether u is an absolute HTTP(S) URL the UPnP renderer can
// actually load. Stale Bose-cloud ContentItems use relative, schemeless locations
// (e.g. "/v1/playback/station/...") that the box rejects with UPnP 402.
func isPlayableURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// --- Peer discovery for the on-box web UI "Other speakers" section ---

// peerEntry accumulates what we know about one other STR speaker across mDNS
// sweeps, so a peer missed in a single lossy round is not dropped from the list.
type peerEntry struct {
	name      string
	port      int // last web port that answered (0 = never reached)
	lastSeen  time.Time
	reachable bool // answered a web-port probe on the most recent sweep
}

var (
	peersMu       sync.Mutex
	peersByIP     = map[string]*peerEntry{}
	peersBrowseAt time.Time
)

// browsePeers discovers the other STR speakers on the LAN over mDNS and returns a
// link to each one's web UI, so a phone on the on-box page can hop between
// speakers without re-typing an address.
//
// A single 2.5s mDNS window plus a drop-on-unreachable filter and a 45s wholesale
// cache made each speaker show a DIFFERENT, often incomplete subset (8-box fleets
// saw 5-6; a box that failed one probe vanished for 45s): #404 / disc-381 /
// disc-385. Now the results MERGE into a longer-lived per-IP map: each sweep
// refreshes the peers it sees, a peer stays listed for peerTTL after it was last
// seen (marked offline if it is not currently reachable, so the page can dim it
// rather than drop it), and sweeps are throttled to rebrowseEvery so repeated page
// loads stay cheap. The browse window is widened so more peers answer per round.
func browsePeers(ctx context.Context, logger *slog.Logger) []webui.PeerLink {
	const (
		rebrowseEvery = 15 * time.Second
		browseWindow  = 3500 * time.Millisecond
		peerTTL       = 7 * time.Minute
	)
	peersMu.Lock()
	needBrowse := peersBrowseAt.IsZero() || time.Since(peersBrowseAt) >= rebrowseEvery
	peersMu.Unlock()

	if needBrowse {
		bctx, cancel := context.WithTimeout(ctx, browseWindow)
		ch, err := discovery.Browse(bctx, logger)
		mine := ownIPv4s()
		type found struct {
			ip, name string
			port     int
		}
		var fresh []found
		if err == nil {
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
				if self || ip == "" {
					continue
				}
				name := inst.FriendlyName
				if name == "" {
					name = inst.Name
				}
				fresh = append(fresh, found{ip: ip, name: name, port: reachableWebPort(ip)})
			}
		} else {
			logger.Debug("peers browse failed", "err", err)
		}
		cancel()

		peersMu.Lock()
		now := time.Now()
		for _, f := range fresh {
			e := peersByIP[f.ip]
			if e == nil {
				e = &peerEntry{}
				peersByIP[f.ip] = e
			}
			if f.name != "" {
				e.name = f.name
			}
			e.lastSeen = now
			e.reachable = f.port != 0
			if f.port != 0 {
				e.port = f.port
			}
		}
		peersBrowseAt = now
		peersMu.Unlock()
	}

	peersMu.Lock()
	defer peersMu.Unlock()
	now := time.Now()
	out := make([]webui.PeerLink, 0, len(peersByIP))
	for ip, e := range peersByIP {
		if now.Sub(e.lastSeen) > peerTTL {
			delete(peersByIP, ip)
			continue
		}
		port := e.port
		if port == 0 {
			port = 8888 // never reached yet: best-effort URL so the entry still resolves once it comes up
		}
		out = append(out, webui.PeerLink{
			Name:      e.name,
			URL:       fmt.Sprintf("http://%s:%d/", ip, port),
			Reachable: e.reachable,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].URL < out[j].URL
	})
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

// OnRemoteSkip handles the SoundTouch remote's next/prev track keys. The box
// cannot skip a UPnP source itself (it emits QPLAY_SKIP_*_FAILED), so STR skips
// on its behalf: go-librespot during Spotify, or the STR play queue during
// folder/library playback. It routes through the same source-aware skip as the
// phone remote (webui.TransportSkip). Before this the hardware keys only skipped
// Spotify, so a folder skip stalled until the current track ended naturally,
// which the box surfaced as "Action Unavailable" for the remaining track time
// (#300). A no-op on a non-skippable source (radio, aux) just does nothing.
func (h *presetWsHandler) OnRemoteSkip(ctx context.Context, forward bool) {
	if h.onRemoteSkip == nil {
		return
	}
	// Off the gabbo read loop: a skip against a slow/wedged transport held the
	// loop for up to 8s per press, delaying every queued frame and skewing the
	// processing-time classification windows (#252). The webui skip serializes
	// box commands internally, so concurrent presses do not interleave SOAP.
	go func() {
		sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		src, err := h.onRemoteSkip(sctx, forward)
		if err != nil {
			h.logger.Warn("remote skip failed", "forward", forward, "source", src, "err", err)
			return
		}
		h.logger.Info("remote skip", "forward", forward, "source", src)
	}()
}

// OnUserStop is fired when the box reports a deliberate playback stop over
// gabbo. It tells the webui's auto-re-push to stand down so a wanted stop holds
// (v0.7.0: a single stop did not stick because the resume restarted it), and
// records the stop time so the hardware recall verifies can stand down too.
func (h *presetWsHandler) OnUserStop(_ context.Context) {
	h.lastUserStopMu.Lock()
	h.lastUserStop = time.Now()
	h.lastUserStopMu.Unlock()
	if h.onUserStop != nil {
		h.onUserStop()
	}
}

// userStoppedSince reports whether the box reported a deliberate stop (gabbo
// STOP_STATE -> OnUserStop) after start.
func (h *presetWsHandler) userStoppedSince(start time.Time) bool {
	h.lastUserStopMu.Lock()
	defer h.lastUserStopMu.Unlock()
	return userStopAbortsVerify(start, h.lastUserStop)
}

// OnSourceRejected records that the box rejected STR's UPnP source with a 1036
// UpnpRcvdContentItemInWrongState (boxws optional hook). The routine preset->
// preset switch race: the box is still tearing down the previous source when
// STR's SetURI lands, refuses it, and can hang attached-but-buffering on the
// Spotify stream without ever playing. verifySpotifyPlaying reads the timestamp
// to re-point in that case instead of trusting the stuck state.
func (h *presetWsHandler) OnSourceRejected(_ context.Context) {
	h.sourceRejectMu.Lock()
	h.lastSourceReject = time.Now()
	h.sourceRejectMu.Unlock()
	// A 1036 rejection precedes the forced re-login, and the re-onboarding
	// that follows is when the firmware wipes its hardware-key preset
	// registrations (field bundles: "missing=5/6" healed right after every
	// "forced re-login sent"). Schedule the heal proactively; own short
	// budget, so a routine ask cannot starve it.
	requestPresetKeyResyncUrgent(h.logger)
}

// lastSourceRejectTime returns when the box last rejected STR's source (zero if
// never), for the verify loop's re-point-on-wrong-state decision.
func (h *presetWsHandler) lastSourceRejectTime() time.Time {
	h.sourceRejectMu.Lock()
	defer h.sourceRejectMu.Unlock()
	return h.lastSourceReject
}

// userStopAbortsVerify is the recall-verify stand-down decision: only a user
// stop that happened strictly AFTER the recall started aborts the verify
// re-push loop (stop-after-recall-start, the same semantics the webui's soft
// recall side settled on), never a rolling window. An older stop must not
// suppress the recall the user just asked for; strict After also biases a
// same-instant tie toward completing the recall, since the recall's own
// transport flip can emit a transient STOP_STATE.
func userStopAbortsVerify(recallStart, lastStop time.Time) bool {
	return !lastStop.IsZero() && lastStop.After(recallStart)
}

// OnThumbActivity fires the user-configured webhook when the box reports a lone
// userActivityUpdate (the best available signal for a remote thumbs key on this
// firmware; up and down are indistinguishable, so it is a single toggle-style
// trigger). The detection + debounce live in boxws; here we just fire.
func (h *presetWsHandler) OnThumbActivity(ctx context.Context) {
	// A lone userActivityUpdate is ALSO the only trace a DEAD hardware preset
	// key leaves: the box's key layer can lose its preset registrations while
	// /presets still lists every slot (#342, display shows "Action
	// unavailable"), and then a press emits no selection frame at all - which
	// is exactly this trigger. The missing-only reconcile never heals that
	// state, so ask it for one forced full re-sync (rate-limited). If the
	// press really was a thumbs key, the extra AddPreset round is harmless.
	requestPresetKeyResync(h.logger)
	if h.webhooks == nil {
		return
	}
	h.webhooks.FireThumb(ctx)
}

// presetResyncAsk flags one forced full box-preset re-sync for the periodic
// reconcile (the #342 dead-key self-heal). presetResyncLast rate-limits the
// routine requests so repeated thumbs presses cannot cause a re-sync storm;
// presetResyncUrgentLast is a SEPARATE, shorter budget for the deterministic
// wipe moments (a 1036 source rejection precedes a forced re-login, and the
// re-onboarding is when the firmware drops its key registrations) so a routine
// ask consumed minutes earlier cannot starve the heal that is actually needed
// now (field bundles 2026-07-22: five dead-key presses produced no resync
// because the boot-time ask had eaten the 10-minute budget).
var (
	presetResyncAsk        atomic.Bool
	presetResyncLast       atomic.Int64 // unix seconds of the last accepted routine request
	presetResyncUrgentLast atomic.Int64 // unix seconds of the last accepted urgent request
)

func requestPresetKeyResync(logger *slog.Logger) {
	const minGapSec = 2 * 60
	now := time.Now().Unix()
	last := presetResyncLast.Load()
	if last != 0 && now-last < minGapSec {
		return
	}
	if !presetResyncLast.CompareAndSwap(last, now) {
		return
	}
	presetResyncAsk.Store(true)
	if logger != nil {
		logger.Info("preset self-heal: scheduling a full box preset re-sync (#342)")
	}
}

// requestPresetKeyResyncUrgent is requestPresetKeyResync for the moments where
// a box-side key wipe is EXPECTED (a 1036 rejection / forced re-login, a fresh
// pairing): it uses its own short budget so it cannot be starved by a routine
// ask, and the reconcile's 10s wake bounds how often the forced pass can run.
func requestPresetKeyResyncUrgent(logger *slog.Logger) {
	const minGapSec = 60
	now := time.Now().Unix()
	last := presetResyncUrgentLast.Load()
	if last != 0 && now-last < minGapSec {
		return
	}
	if !presetResyncUrgentLast.CompareAndSwap(last, now) {
		return
	}
	presetResyncAsk.Store(true)
	if logger != nil {
		logger.Info("preset self-heal: urgent full box preset re-sync scheduled (re-login/pairing wipes the key registrations)")
	}
}

// OnPowerKey fires the configured "power" webhook on a power-off (standby)
// event. Additional-only: STR cannot suppress the firmware power toggle. boxws
// only calls this on the standby transition, which STR never causes itself, so
// the webhook does not false-fire on STR's own wake-for-recall.
func (h *presetWsHandler) OnPowerKey(ctx context.Context) {
	if h.webhooks != nil {
		// Async: the webhook HTTP request must not stall the gabbo read loop.
		go func() {
			if h.webhooks.FireButton(ctx, "power") {
				h.logger.Info("power webhook fired")
			}
		}()
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
	// The firmware loses hardware-key preset registrations across power
	// cycles (field bundles 2026-07-22: the reconcile heals "missing=5/6"
	// slots again and again; users see "preset not assigned" until the next
	// 5-minute reconcile tick, and "after power-on only the first program
	// plays"). Ask for a full re-sync right at the wake so the keys work
	// within seconds instead of minutes. Rate-limited internally.
	requestPresetKeyResync(h.logger)
	// The fake marge login decays across the same power cycles: re-check the
	// pairing right at the wake. On a healthy box this is one /info read; on
	// a login-suspect box (a recent 1036 NOT_LOGGED_IN) it re-asserts the
	// account before the user's first press instead of after its failure.
	if h.autoPair != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			h.autoPair.TriggerNow(ctx)
		}()
	}
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
	// A gabbo reconnect usually means the box just came back from a standby
	// or reboot - exactly when the firmware tends to have dropped its
	// hardware-key preset registrations (see OnPowerWake). Ask for a full
	// re-sync so the keys are registered again within seconds.
	requestPresetKeyResync(h.logger)
	// Re-check the fake login too (see OnPowerWake): the reconnect moments
	// are when the MargeHSM state decays on fresh-install boxes.
	if h.autoPair != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			h.autoPair.TriggerNow(ctx)
		}()
	}
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
		// Async: the webhook HTTP request must not stall the gabbo read loop.
		go func() {
			if h.webhooks.FireButton(ctx, "aux") {
				h.logger.Info("aux webhook fired")
			}
		}()
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
func (h *presetWsHandler) playSpotifyPreset(ctx context.Context, seq uint64, pressAt time.Time, slot int, p presets.Preset) {
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
	// wake-resume can bring the Spotify stream back. The returned recall
	// generation feeds the verify's supersession check below.
	var gen uint64
	if h.noteLastPlay != nil {
		if h.pressSeq.Load() != seq {
			h.logger.Info("spotify recall superseded before push, standing down", "slot", slot)
			return
		}
		gen = h.noteLastPlay(slotURL, p.Name, p.Art, "audio/ogg")
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
		// Fire-and-forget (9a9b0c7): never let a hanging :8090 pair POST sit
		// between the button press and playback (#270).
		go func() {
			pairCtx, c := context.WithTimeout(context.Background(), 6*time.Second)
			defer c()
			h.autoPair.TriggerNow(pairCtx)
		}()
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
	go h.verifySpotifyPlaying(seq, gen, pressAt, slot, p)
	h.logger.Info("spotify preset recalled", "slot", slot, "name", p.Name, "account", p.Account)
}

// verifyPlayURL confirms the box started playing a UPnP (radio) recall and
// re-issues it a few times if not, fixing the "first hardware press after
// reboot does nothing" race for radio presets too. mime is the DIDL label of
// the initial play ("" = audio/mpeg default); the retries must re-issue with
// the SAME label or an AAC station recovered here would fall back to silence
// (#252).
func (h *presetWsHandler) verifyPlayURL(seq, gen uint64, pressAt time.Time, slot int, url, name, icon, mime string) {
	// All stand-down checks anchor to pressAt, the moment the user pressed the
	// key (stamped on the gabbo read loop). The old anchor - verify-start,
	// i.e. after the initial SOAP push and wake - meant a user stop or a 1036
	// rejection that landed DURING a slow push was invisible to the loop.
	// Fast recovery for the wrong-state race, before the 5 s loop below: on a
	// Wave the box answers every hardware press with 1036
	// UpnpRcvdContentItemInWrongState about 0.8 s in, because it first tries to
	// activate its OWN stored ContentItem (which no longer works without the
	// Bose cloud) and STR's push lands while that teardown is still running.
	// Waiting a full verify tick to react leaves the user in silence for five
	// seconds; re-pushing as soon as the rejection is visible is the automatic
	// version of the second press users learned to do by hand.
	h.rePushAfterSourceReject(seq, gen, pressAt, slot, url, name, icon, mime)
	// Up to 5 attempts (~25s): a box waking from a deep/overnight standby can
	// take longer than the old 3-attempt (~15s) window to finish bringing its
	// network and playback subsystem back up before it accepts the stream (#183).
	nudged := false
	for attempt := 1; attempt <= 5; attempt++ {
		time.Sleep(5 * time.Second)
		// A newer press or app recall owns the transport now: this loop's URL is
		// stale, and re-pushing it would flip the box back to the previous
		// station ("pressed 2, got 1"). The soft path has had this guard all
		// along (verifyRecall's recallGen); the hardware loop lacked it.
		if h.superseded(seq, gen) {
			h.logger.Info("hardware recall superseded by a newer play, standing down", "slot", slot)
			return
		}
		// Success means the box is playing THIS recall, not merely "some play
		// state". A bare play-state check silently passed the exact failure the
		// verify exists for: on a Wave every hardware press is rejected with 1036
		// UpnpRcvdContentItemInWrongState, the box then flips UPNP -> INVALID_SOURCE
		// -> UPNP while still reporting a stale PLAY/BUFFERING state from the
		// previous stream, and it never fetches the new URL. boxIsPlaying read that
		// as success at the first tick, so the recall returned silently: the display
		// showed the station, no audio ever came, no retry ran and no wedge strike
		// was recorded. The Spotify verify already keys off the now_playing location
		// for this very reason (see boxPlayingSpotify); radio now does too.
		// The box pulling THIS SLOT's proxied stream since the press is proof it
		// is playing what we pushed, and it is checked FIRST because now_playing
		// lags: a Portable kept naming the PREVIOUS preset for seconds after it
		// had already opened the new stream, so a location check alone declared a
		// healthy recall dead and the "repair" tore the working stream down.
		if h.slotPulledSince(slot, pressAt) || h.playingURL(url) {
			if h.noteBoxHealthy != nil {
				h.noteBoxHealthy()
			}
			return
		}
		// The user powered the box off during the recall: the box reads "not
		// playing" only because it is in standby. Re-pushing here re-arms the
		// transport the power-off cleared, which on scm ST20 firmware bounces the
		// speaker back on (#197, the "start via preset then power off" trigger). A
		// genuine deep-standby wake (#183) carries no recent power-off, so the
		// legitimate retry still runs.
		if h.poweredOffSince(pressAt) {
			h.logger.Info("hardware recall: box powered off mid-recall, not re-pushing (#197)", "slot", slot)
			return
		}
		// The user deliberately stopped playback (gabbo STOP_STATE, e.g. the Bose
		// remote's stop key) after this press: the stop must hold, like the webui
		// side's stand-down, instead of being overridden by a re-push.
		if h.userStoppedSince(pressAt) {
			h.logger.Info("hardware recall: user stopped playback mid-recall, not re-pushing", "slot", slot)
			return
		}
		h.logger.Warn("hardware recall not playing yet, retrying", "slot", slot, "attempt", attempt)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		// A box stuck in INVALID_SOURCE ignores wake (WakeAndWait only toggles
		// out of STANDBY) and inertly ACKs every SetURI+Play without ever
		// fetching audio; in the mojo/ST30 field bundle the ONLY successful
		// source activation of the day followed a real sys-power toggle. If the
		// box still reports INVALID_SOURCE by the third attempt, send one
		// bounded nudge before pushing again.
		if nudgeStuckSource(attempt, nudged, h.currentBoxSource()) {
			nudged = true
			h.logger.Warn("hardware recall: box stuck in INVALID_SOURCE, sending one sys-power nudge", "slot", slot)
			if err := h.sysPowerToggle(ctx); err != nil {
				h.logger.Warn("hardware recall: sys-power nudge failed", "slot", slot, "err", err)
			}
			time.Sleep(1500 * time.Millisecond)
		}
		// Wake the box first: after the 1036 wrong-state rejection the firmware
		// often gives up on its failed self-activation and powers the source off
		// through INVALID_SOURCE -> STANDBY (field bundles 2026-07-22, all
		// models: the box "switches itself off" after the press). Every retry
		// then pushed SetURI+Play into a sleeping box and the recall never
		// converged, even though the forced re-login had already landed. A
		// user power-off cannot reach this line (poweredOffSince stands the
		// loop down above), so waking here only ever reverses the box's own
		// give-up. No-op when the box is awake (a single now_playing read).
		if err := h.wake(ctx); err != nil {
			h.logger.Warn("hardware recall retry: could not wake box", "slot", slot, "err", err)
		}
		if mime != "" {
			_ = h.renderer.PlayURLMime(ctx, url, name, icon, mime)
		} else {
			_ = h.renderer.PlayURL(ctx, url, name, icon)
		}
		cancel()
	}
	h.logger.Warn("hardware recall still not playing after retries", "slot", slot)
	if h.noteRecallExhausted != nil {
		h.noteRecallExhausted()
	}
}

// sourceRejectProbeDelay is how long verifyPlayURL waits before looking for a
// wrong-state rejection of the recall it just pushed. The box reports the 1036
// about 0.8 s after a hardware press and its source flap settles within ~50 ms
// of that, so this both catches the rejection and lets the teardown finish; a
// recall that simply started normally is already playing by now and is left
// alone.
const sourceRejectProbeDelay = 1500 * time.Millisecond

// rePushAfterSourceReject re-issues a recall the box refused with 1036
// UpnpRcvdContentItemInWrongState, without waiting for the first 5 s verify
// tick. The rejection means the box positively declined this stream, so
// pushing it again is a repair rather than a guess; a recall that is already
// playing, a box the user powered off, a deliberate stop, and a recall a newer
// press superseded are all left alone. Fires at most once per recall: the
// verify loop owns everything after it.
func (h *presetWsHandler) rePushAfterSourceReject(seq, gen uint64, pressAt time.Time, slot int, url, name, icon, mime string) {
	time.Sleep(sourceRejectProbeDelay)
	if !h.lastSourceRejectTime().After(pressAt) {
		return // the box did not refuse this recall; the normal verify governs
	}
	if h.superseded(seq, gen) {
		// A newer press/recall owns the transport: clearing it and re-pushing
		// THIS press's URL would actively tear the newer recall down.
		return
	}
	if h.slotPulledSince(slot, pressAt) || h.playingURL(url) {
		return // refused once, then started anyway
	}
	if h.poweredOffSince(pressAt) {
		return // #197: never push into a box the user just switched off
	}
	if h.userStoppedSince(pressAt) {
		return
	}
	h.logger.Warn("hardware recall: box refused the stream (wrong state), clearing the transport and pushing again", "slot", slot)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// The rejection's INVALID_SOURCE flap can end in STANDBY within ~1s of the
	// press (the box gives up on its failed self-activation and powers off);
	// re-pushing into that sleeping box did nothing. Wake it first - a no-op
	// when it is awake, and a user power-off never reaches this line (checked
	// above).
	if err := h.wake(ctx); err != nil {
		h.logger.Warn("wrong-state repair: could not wake box", "slot", slot, "err", err)
	}
	// The box refused the stream because its transport is stuck in the wrong
	// state: it is still holding its own dead-cloud ContentItem active (the box
	// answers with name=UNABLE_TO_PROCESS_NOT_LOGGED_IN detail=WrongState). Pushing
	// the IDENTICAL SetURI onto that stuck state is what the box just rejected, so a
	// blind re-push is rejected again and again until a power pull (bundle 55/56
	// ST30, 59/60 Wave, all v0.9.15). Force the transport out of the wrong state
	// first - Stop + ClearURI - then set the URI and Play from a clean slate. This
	// mirrors the proven clean-slot recall that fixed the analogous hardware-skip
	// INVALID_SOURCE wedge on Spotify (HardwareSkip, 59da772). The re-login
	// self-heal fired in parallel from the boxws NOT_LOGGED_IN routing.
	h.clearTransportForRePush(ctx, slot)
	if mime != "" {
		_ = h.renderer.PlayURLMime(ctx, url, name, icon, mime)
	} else {
		_ = h.renderer.PlayURL(ctx, url, name, icon)
	}
}

// clearTransportForRePush forces the box's UPnP transport out of a stuck
// wrong-state before a re-push: a Stop followed by an empty SetAVTransportURI
// (ClearURI) so the firmware releases the dead ContentItem it keeps trying to
// self-activate. Best-effort - both calls are advisory and a wedged renderer may
// ACK them without acting - but starting the re-push from an emptied transport is
// what turns the persistent 1036 loop into a recoverable one. Kept tiny and
// side-effect-free on the happy path: it only runs on the wrong-state repair.
func (h *presetWsHandler) clearTransportForRePush(ctx context.Context, slot int) {
	if h.renderer == nil {
		return
	}
	if err := h.renderer.Stop(ctx); err != nil {
		h.logger.Debug("wrong-state repair: transport stop returned (expected if already stopped)", "slot", slot, "err", err)
	}
	if err := h.renderer.ClearURI(ctx); err != nil {
		h.logger.Debug("wrong-state repair: clear transport URI returned", "slot", slot, "err", err)
	}
}

// verifySpotifyPlaying confirms the box reached a playing state after a Spotify
// recall and re-issues the recall a few times if not, fixing the "first press
// after reboot does nothing" race without needing a second press.
func (h *presetWsHandler) verifySpotifyPlaying(seq, gen uint64, pressAt time.Time, slot int, p presets.Preset) {
	// Anchors mirror verifyPlayURL: pressAt (the moment of the key press) for
	// the user-stop / power-off stand-downs, seq/gen for supersession.
	// Track the box's 1036 wrong-state rejections so a fresh one within a verify
	// tick forces a re-point instead of trusting the box's playing-looking state.
	lastRejectSeen := pressAt
	for attempt := 1; attempt <= 3; attempt++ {
		time.Sleep(5 * time.Second)
		// A newer press or app recall owns the transport: re-pointing at this
		// Spotify slot now would yank the user's newest choice (e.g. a radio
		// press made during this loop's 15s window used to bounce back to
		// Spotify). Stand down.
		if h.superseded(seq, gen) {
			h.logger.Info("spotify recall superseded by a newer play, standing down", "slot", slot)
			return
		}
		// A box that just rejected STR's source (1036 UpnpRcvdContentItemInWrongState,
		// the preset->preset switch race) can report attached + BUFFERING on the
		// Spotify location without ever reaching audio, then detach ~30s later with no
		// music (ST30 4->5, 2026-07-14: "loaded 30s, never played, second press worked").
		// So if a NEW rejection landed since the last tick, do not stand down on the
		// playing-check; fall through to re-point, the automatic version of the user's
		// second press.
		rej := h.lastSourceRejectTime()
		freshReject := rej.After(lastRejectSeen)
		if rej.After(lastRejectSeen) {
			lastRejectSeen = rej
		}
		// Success = the box is actually on the Spotify stream. Use the
		// location-aware check, not a bare play-state: a bounce-to-radio reads
		// as playing (would skip recovery -> double-tap) and a bare Streaming()
		// flaps to false even while Spotify plays (re-pointing on that flap
		// re-attaches and restarts the track). boxPlayingSpotify keys off the
		// now_playing location, so it is true only when Spotify really plays.
		//
		// A fresh 1036 wrong-state normally forces a re-point (the box can hang
		// attached+buffering after it). But if the box has ALREADY reached real
		// PLAY_STATE despite that transient flap, re-pointing would only knock the
		// already-playing box back into buffering (ST30 4->5: "right song plays a
		// few seconds, then stops to the buffering logo"). So on a fresh reject,
		// still stand down when the box is GENUINELY playing (PLAY_STATE, not merely
		// BUFFERING); only re-point a box that is stuck.
		if (h.spotify.Streaming() || boxPlayingSpotify(h.boxHost)) &&
			(!freshReject || boxReallyPlayingSpotify(h.boxHost)) {
			return
		}
		// Stand down if the user powered the box off mid-recall, so the re-point
		// below does not re-wake a box the user just switched off (#197).
		if h.poweredOffSince(pressAt) {
			h.logger.Info("spotify recall: box powered off mid-recall, not re-pointing (#197)", "slot", slot)
			return
		}
		// A deliberate stop (gabbo STOP_STATE) after this press must hold; do
		// not re-point the box over the user's stop.
		if h.userStoppedSince(pressAt) {
			h.logger.Info("spotify recall: user stopped playback mid-recall, not re-pointing", "slot", slot)
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

// applyPendingBoxName applies a box name left by the setup wizard once to the
// Bose box, verbatim. The name is one the user deliberately typed for this
// speaker during setup, so it is used exactly as chosen: appending the DeviceID
// as a UID suffix here made the user's own name look untidy on every install and
// update (#133, #292). Duplicate-name disambiguation is the caller's concern (the
// user simply gives two speakers two different names); STR does not second-guess
// a name the user picked. On success the file is deleted.
func applyPendingBoxName(ctx context.Context, boxHost, path string, logger *slog.Logger) {
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
	wanted := raw
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
		// SyncAllPresets returns ONLY the failed slots; absence means the
		// slot landed. The old loop ranged over the error map and looked for
		// nil values, which never occur - so successes were re-pushed on all
		// 12 attempts and "all synced" could never log (#342).
		errs := boxcli.SyncAllPresets(context.Background(), boxHost, retrySpecs)
		for _, spec := range retrySpecs {
			err, failed := errs[spec.Slot]
			if !failed {
				delete(pending, spec.Slot)
				logger.Info("box preset synced", "slot", spec.Slot, "name", spec.Name, "attempt", attempt)
			} else if attempt == 5 {
				logger.Warn("box preset sync failed permanently", "slot", spec.Slot, "err", err)
			} else {
				logger.Debug("box preset sync fail, retry", "slot", spec.Slot, "attempt", attempt, "err", err)
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
		force := !fullDone
		if !force && presetResyncAsk.CompareAndSwap(true, false) {
			// Dead-key self-heal (#342): a hardware press produced no
			// selection frame, so the box's key layer likely lost its
			// registrations even though /presets still lists them.
			force = true
		}
		ready := reconcileOnce(store, boxHost, logger, force)
		fullDone = ready
		if fullDone {
			// Maintenance cadence, but wake early when the self-heal asked
			// for a forced re-sync so a dead key recovers in seconds, not
			// after up to five minutes.
			for waited := time.Duration(0); waited < 5*time.Minute && !presetResyncAsk.Load(); waited += 10 * time.Second {
				time.Sleep(10 * time.Second)
			}
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
	boxLocs, err := fetchBoxPresets(boxHost)
	if err != nil {
		logger.Debug("preset reconcile: box presets not readable", "err", err)
		return false
	}
	// Add the STR store presets the box is missing (or all, on a forced full
	// re-sync). strSlots also drives the prune pass below.
	strSlots := map[int]bool{}
	var missing []boxcli.PresetSpec
	for _, p := range stick {
		strSlots[p.Slot] = true
		if _, onBox := boxLocs[p.Slot]; forceFull || !onBox {
			missing = append(missing, boxcli.PresetSpec{
				Slot: p.Slot, Name: p.Name, StreamURL: boxPresetURL(p),
			})
		}
	}
	syncFailed := false
	if len(missing) > 0 {
		if forceFull {
			logger.Info("preset reconcile: full re-sync after box became ready (registers hardware buttons)", "slots", len(missing))
		} else {
			logger.Info("preset reconcile: missing slots on box, syncing", "missing", len(missing))
		}
		// SyncAllPresets returns ONLY failed slots; the old nil-check here
		// could never fire, so healed slots never logged and - worse -
		// persistent AddPreset failures were swallowed silently, invisible
		// in every diagnostic bundle (#342).
		errs := boxcli.SyncAllPresets(context.Background(), boxHost, missing)
		for _, spec := range missing {
			if serr, failed := errs[spec.Slot]; failed {
				syncFailed = true
				logger.Warn("preset reconcile: AddPreset failed", "slot", spec.Slot, "err", serr)
			} else {
				logger.Info("preset reconcile healed", "slot", spec.Slot)
			}
		}
	}
	// Prune STR-owned box presets the store no longer backs, so a stale preset
	// from an earlier install does not linger as a dead button. The box's Bose
	// firmware keeps its preset list across an STR reinstall, but STR's store is
	// fresh, so those slots show a name yet cannot play (the store stream URL is
	// gone) - the reporter saw old presets reappear after a reinstall and not
	// work. Remove ONLY locations STR itself wrote (strict boxurl shape, not the
	// loose "/stream/" substring match, which could misread a foreign Icecast
	// URL containing /stream/ as STR-owned and delete a working box preset); a
	// foreign preset (e.g. a box-cached Deezer entry) or any slot STR does have
	// is left untouched.
	for slot, loc := range boxLocs {
		if strSlots[slot] || !isOwnBoxPresetLocation(loc) {
			continue
		}
		rctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		if rerr := boxcli.RemovePreset(rctx, boxHost, slot); rerr != nil {
			logger.Warn("preset reconcile: could not remove a stale STR preset", "slot", slot, "err", rerr)
		} else {
			logger.Info("preset reconcile: removed a stale STR preset the store no longer backs (dead button after reinstall)", "slot", slot)
		}
		cancel()
	}
	// A pass with failed AddPresets is NOT done: returning true here dropped
	// the loop to the 5-minute maintenance cadence while the box's key layer
	// stayed empty, which is exactly the "hardware keys dead for ~6.5 minutes
	// after every power cycle" window in the field bundles (a just-booted
	// BoseApp accepts GET /presets but rejects AddPreset for a while). Report
	// not-ready so the 10s fast cadence retries until every slot registered.
	if syncFailed {
		logger.Warn("preset reconcile: some AddPresets failed, keeping the fast retry cadence")
		return false
	}
	return true
}

// fetchBoxPresets reads GET /presets from the Bose API and returns each set
// slot's ContentItem location (slot -> location URL). The location is what tells
// STR-owned presets (its own /stream/ or /spotify/ URLs) apart from foreign ones,
// so the reconcile can prune only its own stale entries and never a box-native
// preset.
func fetchBoxPresets(boxHost string) (map[int]string, error) {
	entries, err := fetchBoxPresetsFull(boxHost)
	if err != nil {
		return nil, err
	}
	out := map[int]string{}
	for _, e := range entries {
		out[e.Slot] = e.Location
	}
	return out, nil
}

// boxPresetEntry is one slot of the box's own :8090/presets list, with enough
// ContentItem detail to seed the webui's box-native snapshot and to identify a
// lost STR preset for the store recovery.
type boxPresetEntry struct {
	Slot     int
	Location string
	Name     string
	Source   string
	Type     string
	Account  string
}

// fetchBoxPresetsFull reads GET /presets and returns each set slot's
// ContentItem fields.
func fetchBoxPresetsFull(boxHost string) ([]boxPresetEntry, error) {
	client := http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:8090/presets", boxHost))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out []boxPresetEntry
	// Bose format: <presets><preset id="1" ...><ContentItem location="..."/></preset></presets>
	for _, blk := range presetBlockRegex.FindAllStringSubmatch(string(body), -1) {
		slot := 0
		fmt.Sscanf(blk[1], "%d", &slot)
		if slot < 1 || slot > 6 {
			continue
		}
		e := boxPresetEntry{Slot: slot}
		if m := presetLocationRegex.FindStringSubmatch(blk[0]); m != nil {
			e.Location = m[1]
		}
		if m := presetItemNameRegex.FindStringSubmatch(blk[0]); m != nil {
			e.Name = xmlEntityUnescape(m[1])
		}
		if m := presetSourceRegex.FindStringSubmatch(blk[0]); m != nil {
			e.Source = m[1]
		}
		if m := presetTypeRegex.FindStringSubmatch(blk[0]); m != nil {
			e.Type = m[1]
		}
		if m := presetAccountRegex.FindStringSubmatch(blk[0]); m != nil {
			e.Account = m[1]
		}
		out = append(out, e)
	}
	return out, nil
}

// xmlEntityUnescape reverses the five predefined XML entities in text content
// (Bose escapes station names like "Pop & Rock" in /presets).
var xmlEntityReplacer = strings.NewReplacer(
	"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'")

func xmlEntityUnescape(s string) string { return xmlEntityReplacer.Replace(s) }

// margeXMLEscape escapes user-provided text (station names, art URLs) for the
// marge preset template, which is a text/template and does not escape XML.
var margeXMLEscaper = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

func margeXMLEscape(s string) string { return margeXMLEscaper.Replace(s) }

// firstArtURL returns the first entry of a pipe-separated art fallback chain
// (how preset.Art is persisted); the box only ever gets one URL.
func firstArtURL(art string) string {
	if idx := strings.Index(art, "|"); idx >= 0 {
		return art[:idx]
	}
	return art
}

// seedBoxPresetsAndRecoverStore runs once in the background at agent start.
// Two jobs (#252, the ST20 whose presets showed "unassigned although they are
// assigned"):
//
//  1. Seed the webui's box-native preset snapshot from :8090/presets. The
//     snapshot otherwise stays empty until the box happens to emit a gabbo
//     presetsUpdated frame, so the app showed every slot as unassigned even
//     though the box's own list still held them.
//  2. If the NAND preset store came up EMPTY while the box still lists STR's
//     own /stream/ presets, the store was lost (the pre-v0.9.14 non-durable
//     save left presets.json at 0 bytes after a standby power-cut; the loss
//     surfaced when the OTA restarted the agent). Every hardware press then
//     404s at the stream proxy: the buttons are dead although the box's key
//     layer works. Restore what the recently-played history can identify by
//     exact station/playlist name, so the buttons come back without the user
//     re-saving every slot.
func seedBoxPresetsAndRecoverStore(store *presets.Store, recentStore *recent.Store, boxHost string, seed func([]webui.BoxPreset), logger *slog.Logger) {
	if boxHost == "" || store == nil {
		return
	}
	// The box needs ~60s after a cold boot before :8090 answers; poll gently
	// and give up quietly after ~10 minutes (the periodic reconcile keeps
	// running regardless).
	var entries []boxPresetEntry
	for i := 0; i < 30; i++ {
		time.Sleep(20 * time.Second)
		var err error
		entries, err = fetchBoxPresetsFull(boxHost)
		if err == nil {
			break
		}
		entries = nil
	}
	if len(entries) == 0 {
		return
	}
	if seed != nil {
		bps := make([]webui.BoxPreset, 0, len(entries))
		for _, e := range entries {
			bps = append(bps, webui.BoxPreset{
				Slot: e.Slot, Source: e.Source, Type: e.Type,
				Location: e.Location, SourceAccount: e.Account, Name: e.Name,
			})
		}
		seed(bps)
		logger.Info("box preset snapshot seeded from :8090/presets", "slots", len(bps))
	}
	if recentStore == nil || len(store.All()) > 0 {
		return
	}
	recovered := 0
	recents := recentStore.All()
	for _, e := range entries {
		if !isOwnBoxPresetLocation(e.Location) || e.Name == "" {
			continue
		}
		if _, exists := store.Get(e.Slot); exists {
			continue
		}
		wantSpotify := strings.Contains(e.Location, "/spotify/")
		// Newest matching history entry wins (the ring is oldest-first).
		for i := len(recents) - 1; i >= 0; i-- {
			r := recents[i]
			if r.CardName != e.Name || r.CardURL == "" {
				continue
			}
			if wantSpotify != (r.Source == "spotify") {
				continue
			}
			p := presets.Preset{Slot: e.Slot, Name: e.Name, Art: r.CardArt, Homepage: r.Homepage}
			if wantSpotify {
				p.Type = "spotify"
				p.URI = r.CardURL
				p.Account = r.Account
			} else {
				p.Type = "radio"
				p.StreamURL = r.CardURL
			}
			if err := store.SetSlot(p); err != nil {
				logger.Warn("preset store recovery: could not save recovered slot", "slot", e.Slot, "err", err)
			} else {
				// Warn on purpose: this must be visible in a diagnostic bundle.
				logger.Warn("preset store recovery: restored a lost preset from the recently-played history",
					"slot", e.Slot, "name", e.Name, "type", p.Type)
				recovered++
			}
			break
		}
	}
	if recovered > 0 {
		logger.Warn("preset store recovery: the preset store was empty while the box still lists STR presets (likely wiped by a pre-v0.9.14 standby power-cut); restored what the history could identify",
			"recovered", recovered)
	}
}

// presetBlockRegex captures one <preset id="N" ...> ... </preset> block; (?s)
// lets . span the newlines Bose puts inside the block. presetLocationRegex then
// pulls the ContentItem location out of that block.
var presetBlockRegex = regexp.MustCompile(`(?s)<preset id="(\d+)".*?</preset>`)
var presetLocationRegex = regexp.MustCompile(`location="([^"]*)"`)
var presetItemNameRegex = regexp.MustCompile(`<itemName>([^<]*)</itemName>`)
var presetSourceRegex = regexp.MustCompile(`source="([^"]*)"`)
var presetTypeRegex = regexp.MustCompile(`type="([^"]*)"`)
var presetAccountRegex = regexp.MustCompile(`sourceAccount="([^"]*)"`)

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
	// A reboot fired seconds after boot — stacked on the install/bootstrap/OTA
	// reboot — trips Bose's shepherdd watchdog into --recovery mode, where the
	// Bose services never start and radio cannot play until a manual power-cycle
	// (the box shows the alternating amber LED pattern). First seen on the lisa
	// chassis (Wave, SA-4, #372) and now confirmed on ginger (SoundTouch 300,
	// reported 2026-07-11 after a v0.9.4 OTA). Wait for the Bose stack to come up
	// first so shepherd marks this boot successful and resets its crash-loop
	// counter; the reboot that follows is then a clean single reboot.
	settleBeforeFragileReboot(logger)
	// Flush pending writes (the stick log, the bootstrap files and the
	// guard stamp on NAND) before we pull the rug out. busybox `sync`
	// keeps this portable at compile time, matching the reboot exec.
	_ = exec.Command("sync").Run()
	time.Sleep(2 * time.Second)
	if err := exec.Command("reboot").Run(); err != nil {
		logger.Error("bootstrap reboot: reboot command failed, continuing on stale boot path", "err", err)
	}
}

// boxVariant returns the Bose chassis codename from /proc/variant (e.g. "lisa",
// "taigan", "rhino", "mojo"), lowercased, or "" when it cannot be read.
func boxVariant() string {
	b, err := os.ReadFile("/proc/variant")
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(b)))
}

// verifiedFastRebootChassis are the Bose chassis codenames Jens has
// hardware-verified to survive a stacked early-boot reboot without shepherdd
// entering --recovery, so they skip the settle wait and reboot immediately.
// Every OTHER chassis (lisa/Wave/SA-4 #372, ginger/ST300, and any unknown or
// unreadable variant) waits for the Bose stack first: the amber-recovery trip
// turned out NOT to be lisa-only, so the safe default is to settle unless a
// chassis is on this proven-fast allowlist.
var verifiedFastRebootChassis = map[string]bool{
	// rhino (ST10) was removed 2026-07-14: two field ST10s went network-unstable
	// on an OTA reboot until a manual power-cycle (#403), the same shepherdd
	// --recovery trip as lisa (#372) and ginger/ST300 (a56b0ae). Three of this
	// allowlist's assumptions have now been disproven, so only chassis with
	// repeated first-hand confirmation stay: mojo and taigan have each survived
	// many stacked reboots on Jens' own boxes. The settle wait is a cheap
	// best-effort with no downside on a genuinely fast box.
	"mojo":   true, // SoundTouch 30 (scm/sm2)
	"taigan": true, // SoundTouch Portable
}

// settleBeforeFragileReboot delays a STR-initiated early-boot reboot until the
// Bose service stack has come up on this boot, so a reboot stacked on the
// install/bootstrap/OTA reboot does not trip shepherdd into --recovery mode
// (the alternating amber LED lockup that needs a manual power-cycle, #372 lisa +
// ginger/ST300). Waiting for :8090 to answer means shepherd has marked this boot
// successful and reset its crash-loop counter. Skipped on the hardware-verified
// fast chassis; best-effort everywhere else — after the settle window it reboots
// anyway.
func settleBeforeFragileReboot(logger *slog.Logger) {
	variant := boxVariant()
	if verifiedFastRebootChassis[variant] {
		return
	}
	const (
		maxWait = 100 * time.Second
		grace   = 5 * time.Second
	)
	logger.Info("bootstrap reboot: waiting for the Bose stack (:8090) before rebooting so shepherdd does not enter recovery (#372)", "variant", variant)
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		if dialable("127.0.0.1", 8090) {
			logger.Info("bootstrap reboot: Bose stack is up (:8090); shepherd boot marked healthy, proceeding with a single clean reboot")
			time.Sleep(grace) // let shepherd finish marking the boot successful
			return
		}
		time.Sleep(2 * time.Second)
	}
	logger.Warn("bootstrap reboot: Bose stack did not come up within the settle window; rebooting anyway (best-effort)", "variant", variant)
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

// boxNowPlayingSource returns the source attribute of the box's now_playing
// (e.g. "SETUP", "STANDBY", "UPNP", "INVALID_SOURCE"), or "" on any error.
func boxNowPlayingSource(boxHost string) string {
	if boxHost == "" {
		boxHost = "127.0.0.1"
	}
	cl := &http.Client{Timeout: 4 * time.Second}
	resp, err := cl.Get("http://" + boxHost + ":8090/now_playing")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	m := regexp.MustCompile(`source="([^"]*)"`).FindSubmatch(b)
	if len(m) == 2 {
		return string(m[1])
	}
	return ""
}

// leaveSetupSourceWatcher clears a stuck out-of-box SETUP source so the box can
// play. It checks soon after boot (the common case: a fresh network install
// that never went through Bose's app onboarding), retries a few times to catch
// a box whose agent came up before the firmware settled, then drops to a slow
// maintenance poll. A POST /setup SETUP_LEAVE is harmless when the box is not
// in setup, so a stray check costs nothing. See boxapi.LeaveSetup.
func leaveSetupSourceWatcher(ctx context.Context, boxHost string, logger *slog.Logger) {
	if boxHost == "" {
		boxHost = "127.0.0.1"
	}
	client := boxapi.New(boxHost)
	check := func() {
		if boxNowPlayingSource(boxHost) != "SETUP" {
			return
		}
		lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := client.LeaveSetup(lctx)
		cancel()
		if err != nil {
			logger.Warn("leave-setup: could not clear the out-of-box SETUP source", "err", err)
			return
		}
		logger.Info("leave-setup: cleared the box's stuck out-of-box SETUP source so it can play (no power-cycle needed)")
	}
	// Prompt initial sweep: a handful of tries over the first ~2 minutes.
	for i := 0; i < 8; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
		check()
	}
	// Maintenance: a box can re-enter the SETUP source after a firmware event,
	// so keep a slow watch running.
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
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

// nudgeStuckSource is the sys-power-nudge decision: the box still reports
// INVALID_SOURCE at the third verify attempt and no nudge ran yet. Bounded to
// exactly one nudge per recall; earlier attempts give the normal wake+re-push
// a chance first.
func nudgeStuckSource(attempt int, nudged bool, source string) bool {
	return attempt == 3 && !nudged && source == "INVALID_SOURCE"
}

// boxPlayingURL reports whether the box is in a play/buffering state AND its
// now_playing actually points at wantURL, the URL this recall pushed. It is the
// success signal for the radio recall verify.
//
// The location check is what a bare play-state check misses: a box that rejects
// the recall (1036 UpnpRcvdContentItemInWrongState, chronic on the Wave) keeps
// reporting the PREVIOUS stream's play state while never fetching the new one,
// so the verify passed at its first tick and the user was left with a display
// that shows the station and no audio at all.
//
// Deliberately forgiving in one direction: firmware whose now_playing carries NO
// location at all falls back to the plain play-state verdict, so a model that
// simply does not report a location does not end up in an endless re-push loop.
func boxPlayingURL(boxHost, wantURL string) bool {
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
	return nowPlayingIsURL(string(b), wantURL)
}

// nowPlayingIsURL is boxPlayingURL's verdict over an already-fetched
// now_playing document; split out so the discriminator is testable without
// hardware or a fixed :8090 listener.
func nowPlayingIsURL(doc, wantURL string) bool {
	if !strings.Contains(doc, "PLAY_STATE") && !strings.Contains(doc, "BUFFERING_STATE") {
		return false
	}
	// Compare on the path (e.g. "/stream/5"), not the whole URL: the box echoes
	// the location it was given, but host spellings differ across the paths that
	// build these URLs (loopback vs the box's LAN address).
	want := streamPath(wantURL)
	if want == "" || !strings.Contains(doc, `location="`) {
		return true // nothing to compare against; keep the old play-state verdict
	}
	return strings.Contains(doc, want)
}

// streamPath reduces an STR stream URL to the path+query the box echoes back in
// now_playing ("http://127.0.0.1:8888/stream/5" -> "/stream/5"), so a location
// comparison does not depend on which host spelling built the URL. Returns ""
// for anything that is not an STR stream URL, which disables the comparison.
func streamPath(u string) string {
	i := strings.Index(u, "/stream/")
	if i < 0 {
		return ""
	}
	return u[i:]
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

// boxReallyPlayingSpotify is the strict form of boxPlayingSpotify: the box is on
// the Spotify stream AND actually in PLAY_STATE (audio flowing), not merely
// BUFFERING. The verify loop uses it to avoid re-pointing, and thereby disrupting,
// a box that has genuinely started playing after a transient 1036 wrong-state flap
// on a preset->preset switch.
func boxReallyPlayingSpotify(boxHost string) bool {
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
	return strings.Contains(s, "spotify/stream") && strings.Contains(s, "PLAY_STATE")
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
