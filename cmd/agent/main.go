// Command streborn is the agent that runs directly on the Bose SoundTouch
// box and emulates the Bose cloud endpoints locally.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
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
	"github.com/JRpersonal/streborn/internal/boxwrites"
	"github.com/JRpersonal/streborn/internal/boxws"
	"github.com/JRpersonal/streborn/internal/clocksync"
	"github.com/JRpersonal/streborn/internal/dnsboot"
	"github.com/JRpersonal/streborn/internal/hosts"
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/mdnshost"
	"github.com/JRpersonal/streborn/internal/mediaservers"
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
	// The process default routes into the same destination as the named logger.
	// Library code falls back to slog.Default() when no logger was handed to it
	// (the boxapi client's 2xx error-envelope survey is the case that exposed
	// this), and without this line that output went to bare stderr, of which a
	// diagnostic bundle keeps almost nothing. The survey exists to be read out
	// of bundles across the fleet, so it has to land where bundles look.
	slog.SetDefault(logger)
	logger.Info("streborn starting", "version", version)

	// FIRST, before anything can open a TLS connection. crypto/x509 builds the
	// system pool once per process and caches it, so this has to happen while
	// nothing has needed it yet; a later call would look correct in a test and
	// do nothing at all on the speaker.
	//
	// On a speaker whose own store is complete this finds nothing to add and
	// changes nothing. On the scm chassis, whose ca-bundle.crt carries 2
	// certificates instead of 166, it is the difference between playing
	// internet radio and refusing every station.
	tlsgen.ApplySupplementalRoots(logger.With("comp", "tlsgen"))

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
	//
	// This is only the STARTING value, and it is a guess: it takes a MAC from
	// the first interface it finds. A speaker with two network interfaces has
	// two MACs and uses exactly one of them as its identity (measured on an
	// ST10: it reports networkInfo type="SCM" 94E3... as its deviceID and
	// type="SMSC" 10CE... as the other), so the guess can name the wrong one.
	// That matters more than it looks: the id goes into the <devices> block of
	// the emulated account, the firmware discards an account in which it cannot
	// find itself, and the discarded account is what registers the radio source
	// behind the hardware preset keys. correctDeviceIDFromBox below replaces
	// the guess with the box's own answer as soon as it responds.
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

	// DLNA/UPnP media servers the user turned into native music sources. The
	// speaker drops the registration about a minute into every boot (it re-checks
	// the account against marge, whose record of it was in memory and went away
	// with the restart), so STR remembers the choice and puts it back. A load
	// error is non-fatal: start with nothing enabled.
	mediaServerStore, msErr := mediaservers.Load("/mnt/nv/streborn/mediaservers.json")
	if msErr != nil {
		logger.Warn("media server config load failed, starting with none enabled", "err", msErr)
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

	// Box-owned presets STR did not write (Deezer, TuneIn, STORED_MUSIC, ...),
	// preserved so the marge preset answer below cannot starve them out of the
	// box's own list (see foreignpresets.go).
	foreignPresets := newForeignPresetStore("/mnt/nv/streborn/foreign-presets.json",
		logger.With("comp", "foreignpresets"))

	// Initialize subsystems
	margeSrv := marge.New(logger.With("comp", "marge"),
		marge.WithDeviceID(deviceID),
		marge.WithReflectSourcesPath(boxsnapshot.ReflectPath()),
		// Persist the stereo-pair record so the firmware's group poll keeps
		// getting the same answer across agent restarts (a "not grouped"
		// fallback after a restart is what invited the firmware to re-create
		// the record from its own point of view).
		marge.WithGroupPath("/mnt/nv/streborn/marge-group.json"),
		marge.WithDeviceIDPath("/mnt/nv/streborn/deviceid"),
		// The box re-reads its cloud presets from marge during every
		// setMargeAccount re-onboarding. Answering with an empty <presets/>
		// made the firmware WIPE its own hardware-key registrations after
		// every forced re-login ("Preset noch nicht festgelegt" until the
		// reconcile healed them minutes later). Serve the stick store live so
		// the cloud view always matches the keys.
		marge.WithPresetSource(func() []marge.Preset {
			all := store.All()
			out := make([]marge.Preset, 0, len(all))
			taken := make(map[int]bool, len(all))
			for _, p := range all {
				taken[p.Slot] = true
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
			// The box's OWN presets STR did not write ride along, or the
			// firmware drops them on its next cloud re-read (a Deezer slot 3
			// vanished exactly that way, 2026-08-17).
			return append(out, foreignPresets.MargePresets(taken)...)
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

	// Replace the MAC-derived deviceID guess with the box's own answer as soon
	// as its firmware responds. Runs in the background so a slow-booting box
	// never delays the listeners; the box's addDevice POST corrects the value
	// too, so this is the first of two independent paths to the right id.
	go correctDeviceIDFromBox(context.Background(), margeSrv, *boxHost, logger)

	// Forensic sections for /api/debug/state. The marge trail (millisecond
	// timestamps) is what lets a bundle answer whether the box exchanged
	// anything with marge inside the ~200 ms window of a Wave sysLanguage
	// revert, and the clock verdict correlates 1036 storms / dead-playback
	// boots with a plug-pull RTC loss (#419 Finding 4). Registered before the
	// listeners spawn so the very first debug fetch already carries them.
	// Whether this speaker's hardware keys run on the native path or still
	// depend on the 1036 recovery machinery. Every incoming bundle becomes a
	// data point for deciding when that machinery can be retired.
	nativeStatusHost := *boxHost
	webui.RegisterDebugSection("native_presets", func() any {
		return nativePresetStatus(nativeStatusHost)
	})
	webui.RegisterDebugSection("marge_recent_requests", func() any {
		return margeSrv.RecentRequestLines(60)
	})
	// The stereo-pair record as THIS box's marge stores it. A bundle from each
	// member shows in one glance whether the pair view diverged (the RIGHT box
	// storing itself as master was invisible without SSH before this).
	webui.RegisterDebugSection("marge_group", func() any {
		xmlDoc, canonical, ok := margeSrv.GroupSnapshot()
		if !ok {
			return map[string]any{"present": false}
		}
		return map[string]any{"present": true, "canonical": canonical, "xml": xmlDoc}
	})
	// Box-write ledger (#instrument-dont-guess): every write STR performs
	// against the box firmware is counted with the box's source at write
	// time. One aggregated WARN per hour at most (a write-free hour logs
	// nothing - the healthy overnight state), running totals in
	// /api/debug/state, and a boot-reason marker so a bundle distinguishes
	// "the box rebooted" from "only the agent respawned" - together they make
	// "who wrote to the box at 3am and was it asleep" a one-grep answer.
	bootReason := agentBootReason()
	logger.Info("box-write ledger armed", "bootReason", bootReason)
	webui.RegisterDebugSection("box_write_ledger", func() any {
		return map[string]any{
			"bootReason": bootReason,
			"totals":     boxwrites.Totals(),
		}
	})
	// Why the PREVIOUS run stopped. Read before the new heartbeat overwrites
	// what the dead process left, and before anything else can crash. An agent
	// that vanishes and is respawned by the watchdog otherwise leaves no trace
	// at all: a live Portable did exactly that mid-Spotify on 2026-08-14 and
	// the log could only say the process was gone. See crashforensics.go.
	noteAgentStart(bootReason, logger)
	webui.RegisterDebugSection("last_exit", lastExitSnapshot)
	go func() {
		for {
			time.Sleep(time.Hour)
			if line := boxwrites.Format(boxwrites.SnapshotReset()); line != "" {
				logger.Warn("box-write ledger: writes in the last hour", "writes", line)
			}
		}
	}()

	// A pair record restored from NAND can describe a pair that no longer
	// exists: a Bose factory reset wipes the firmware's own pairing
	// (GroupService) but not /mnt/nv/streborn. Once the firmware answers, two
	// consecutive empty /getGroup reads 30s apart with still no live
	// confirmation (no firmware group post, no canonical install) drop the
	// phantom record, so the group poll stops resurrecting a dead pair.
	// Reads only; the single NAND write is the one-time record removal.
	if margeSrv.GroupRestoredUnconfirmed() {
		go func() {
			c := boxapi.New(*boxHost)
			emptyReads := 0
			for i := 0; i < 40; i++ { // give a slow boot up to ~20 min
				time.Sleep(30 * time.Second)
				if !margeSrv.GroupRestoredUnconfirmed() {
					return // confirmed by a live signal meanwhile
				}
				gctx, gcancel := context.WithTimeout(context.Background(), 6*time.Second)
				g, err := c.GetGroup(gctx)
				gcancel()
				if err != nil {
					emptyReads = 0 // firmware not answering yet; start over
					continue
				}
				if g.ID != "" || len(g.Members) > 0 {
					return // firmware still paired: the restored record is real
				}
				if emptyReads++; emptyReads >= 2 {
					margeSrv.ClearGroup("firmware reports no pair after restart (factory reset?)")
					return
				}
			}
		}()
	}
	webui.RegisterDebugSection("clock_status", clockStatusSnapshot)
	// dns_status makes the #487 fault class visible in one glance: a bundle
	// with an empty nameserver list explains dead radio, a stuck clock, an
	// unpaired box and a crash-looping Spotify engine all at once.
	webui.RegisterDebugSection("dns_status", func() any {
		return map[string]any{
			"nameservers":     dnsboot.Nameservers(),
			"resolv_conf":     dnsboot.RawResolvConf(),
			"default_gateway": dnsboot.DefaultGateway(),
		}
	})
	// tls_trust is the counterpart for the fault class dns_status cannot
	// see: the box resolves and connects fine, but its overlaid trust
	// store no longer carries the vendor's public roots, so every https
	// station AND the Spotify engine die with the same
	// "certificate signed by unknown authority". Without this section a
	// bundle shows only that terse error and it reads like a station
	// problem.
	// "repair" says whether the agent had to rebuild a store and whether that
	// worked, which "stores" alone cannot show: a repaired box and a box that
	// was never broken both read healthy afterwards.
	webui.RegisterDebugSection("tls_trust", func() any {
		root := tlsgen.ReadRootCAPEM(*tlsDir)
		return map[string]any{
			"stores": tlsgen.TrustStoreSnapshot(root),
			"repair": tlsgen.LastTrustRepair(),
			// Which common authorities this speaker actually has. A count of
			// certificates says nothing about WHICH ones, and a speaker can
			// hold 157 of them and still be missing the two that most internet
			// radio depends on.
			"wellKnownRoots": tlsgen.WellKnownRoots(),
			// What STR added for its own connections, and why it did not when
			// it did not. A speaker that needed nothing reports applied=false
			// with a reason, which is the normal and healthy answer.
			"supplement": tlsgen.LastSupplement(),
		}
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
	go periodicPresetReconcile(store, *boxHost, logger, webhooksStore)

	// Read the region from a file on start (provisioned by the setup wizard).
	region := loadRegion(*regionFile, logger)

	// The stream proxy makes Bose ContentItems resistant to token expiry:
	// instead of the real CDN URL, Bose gets http://127.0.0.1:8888/stream/<slot>
	// and the stick agent reconnects internally on drops.
	streamProxySrv := streamproxy.New(store, logger.With("comp", "streamproxy"))
	// Radio dropout forensics: how often the current station forces a reconnect
	// and why (eof vs read-fail), so an intermittent-gap report is diagnosed from
	// the bundle instead of guessed.
	webui.RegisterDebugSection("radio_stream_health", streamProxySrv.HealthSnapshot)

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

	// Answer for this speaker's own mDNS name BEFORE the engine is told to
	// advertise it. The order is the safety property, not a detail: the engine
	// names the box's Linux hostname today, which is the Bose chassis codename,
	// and nothing on the network answers a query for it. Pointing the engine at
	// a new name that is equally unanswered would be a step backwards, because
	// the codename at least still sits in some routers' caches. So the name is
	// only handed over once it means something, and a responder that cannot
	// start leaves everything exactly as it is today.
	// Captured for the post-switch network refresh (#697): after a live Wi-Fi
	// move the responder must be repointed at the new address, or it keeps
	// answering the box's own name with the boot address, cache-flush bit set.
	var mdnsHostResp *mdnshost.Responder
	if label := mdnshost.LabelFor(deviceID); label != "" {
		if ip := routableIPv4(); ip != nil {
			if resp, err := mdnshost.Start(context.Background(), logger.With("comp", "mdnshost"), label, ip); err != nil {
				logger.Warn("mdns host: not answering for this speaker's own name, leaving the engine on the chassis name",
					"err", err, "label", label)
			} else {
				spotifyMgr.SetZeroconfHost(resp.Label())
				// The agent's own service gets the same treatment. Its entry
				// names the chassis codename today just like the Spotify one
				// did, so three SoundTouch 10s announce themselves under one
				// name. It is less visible than the Spotify fault only because
				// the desktop app takes the address out of the service answer
				// instead of looking the name up.
				mdnsHostLabel = resp.Label()
				mdnsHostResp = resp
				// Put the counters in the diagnostic bundle. The open question
				// they answer: a SoundTouch 30 here is missing from four
				// browses in a row while its engine runs and is logged in. If
				// queriesSeen climbs on that box, the questions arrive and the
				// answers are getting lost; if it stays at zero, the box is
				// never being asked. Those are different faults.
				webui.RegisterDebugSection("mdns_host", func() any { return resp.Stats() })
				defer func() { _ = resp.Close() }()
			}
		} else {
			logger.Warn("mdns host: no routable address yet, leaving the engine on the chassis name", "label", label)
		}
	}
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

	// Restore the sticky speaker-picker list before the webui serves its first
	// page, so the picker is complete from the very first load after a reboot.
	loadPersistedPeers(logger.With("comp", "peers"))
	// Keep the peer verdicts fresh INDEPENDENTLY of page loads: the sweep and
	// the TCP fallback probes used to run only when someone requested
	// /api/peers, so the first render after opening the page showed stale
	// "unreachable" states for speakers that were fine (live 2026-07-26: two
	// running ST10/ST30 rendered dimmed, then flipped clickable on the next
	// poll). A background tick keeps lastSeen current so the first render is
	// already right. browsePeers throttles internally, so an open page's own
	// polls and this tick never double-browse.
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for range t.C {
			_ = browsePeers(context.Background(), logger.With("comp", "peers"))
		}
	}()

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
		webui.WithSpotifyShuffle(spotifyMgr.ShufflingContext),
		webui.WithSpotifySkipBoundary(spotifyMgr.LastSkipBoundary),
		webui.WithSpotifyMeta(spotifyMgr.PlaylistMeta),
		// "Streaming" must mean audio is actually flowing, not merely that the
		// box holds the connection open: a stalled sink (attached, zero audio
		// pages) used to satisfy the recall verify, so a failed Spotify recall
		// reported success while the user stared at a spinner until the box
		// timed out (field 2026-07-27). Treat a stall as not-streaming so the
		// verify keeps working on it and the failure becomes visible.
		webui.WithSpotifyStreaming(func() bool {
			return spotifyMgr.Streaming() && !spotifyMgr.StreamStalled()
		}),
		webui.WithSpotifySkip(func(ctx context.Context, forward bool) error {
			// Arm the skip-cut first, so the boundary this skip produces drops
			// the old track's unsent tail instead of playing it out.
			spotifyMgr.NoteSkip()
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
		// Recall boundary cut: armed with SetRecalling so a preset re-press
		// never feeds the box the old track's audio during the load preamble;
		// the stale-KB probe lets the recall re-push stand down when the cut
		// kept the box clean.
		webui.WithSpotifyArmRecallCut(spotifyMgr.ArmRecallCut),
		webui.WithSpotifyBoundaryStaleKB(spotifyMgr.LastBoundaryStaleKB),
		webui.WithSpotifySuppressActivate(spotifyMgr.SuppressActivate),
		webui.WithSpotifyExpectReattach(spotifyMgr.ExpectReattach),
		webui.WithSpotifyInfo(spotifyMgr.ServeInfo),
		webui.WithSpotifyQuality(spotifyMgr.ServeQuality),
		webui.WithSpotifyReload(spotifyMgr.ReloadBinary),
		webui.WithSpotifyStop(spotifyMgr.StopEngine),
		webui.WithSpotifySwitchedAway(spotifyMgr.SwitchedAway),
		webui.WithPeers(func(ctx context.Context) []webui.PeerLink {
			return browsePeers(ctx, logger.With("comp", "peers"))
		}),
		webui.WithPeerSeed(func(seeds []webui.PeerSeed) {
			seedPeers(seeds, logger.With("comp", "peers"))
		}),
		webui.WithPeerForget(func(host string) bool {
			return forgetPeer(host, logger.With("comp", "peers"))
		}),
		webui.WithWebhooks(webhooksStore),
		webui.WithZones(zonesStore),
		webui.WithMediaServers(mediaServerStore),
		webui.WithStoredMusicPublisher(func(list []webui.StoredMusicSource) {
			out := make([]marge.StoredMusicSource, 0, len(list))
			for _, m := range list {
				out = append(out, marge.StoredMusicSource{Account: m.Account, Name: m.Name})
			}
			margeSrv.SetStoredMusicSources(out)
		}),
		webui.WithMargeGroups(margeSrv.GroupSnapshot, margeSrv.SetCanonicalGroup, margeSrv.ClearGroup),
		webui.WithMargeForward(margeSrv.SetForward),
		webui.WithRecent(recentStore))

	// Re-assert a persisted multiroom group (native or mirror) so it survives
	// reboot/standby/Wi-Fi outage without the user re-grouping (#70 beta).
	// No-op when standalone. Lives on the server so the mirror path can reach
	// the current stream + the UPnP renderer.
	go webuiSrv.PeriodicZoneReconcile()

	// Check which Wi-Fi the speaker actually came up on, and move it back if
	// the firmware picked the old network out of its own profile store. Runs
	// only after a real box boot (the guard checks bootReason itself) and only
	// when the user has moved this speaker at least once; a healthy boot costs
	// one wpa_cli status read. See internal/webui/wlanguard.go.
	go webuiSrv.StartWLANBootGuard(context.Background(), bootReason)

	// Publish the user's DLNA/UPnP music sources into the marge account, which
	// the box polls for itself at boot and keeps whatever it finds there, exactly
	// the way radio arrives. That is the entire persistence mechanism: no write
	// to the speaker, so nothing here can disturb its standby countdown. Done
	// synchronously and early, because the box's account poll comes seconds
	// after its own boot.
	webuiSrv.PublishMediaServers()

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
		// A pair torn down anywhere (the Bose app included) clears STR's record
		// on the speaker that reports it, so no speaker is left believing it is
		// still half of a pair and therefore unpairable.
		margeGroupClear: margeSrv.ClearGroup,
		// A zone the FIRMWARE dropped by itself never reached STR's own group
		// document, which then kept the power-on resume switched off for good.
		// The forming frame goes to the same place so a dissolve the firmware
		// emits mid-change is not held against the group it then forms.
		zoneChanged: webuiSrv.NoteBoxZoneState,
		// Record hardware-preset recalls so the wake-resume + auto-re-push know
		// what to bring back. Returns the recall generation for supersession.
		noteLastPlay: webuiSrv.NoteLastPlay,
		// Conditional post-recall re-push (shared with the app path): drop the
		// box's buffer only when stale pre-boundary audio reached it despite
		// the armed recall cut.
		repushAfterRecall: webuiSrv.ReattachAfterSpotifyRecall,
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
		onStandbyExit:  webuiSrv.RunDeferredResume,
		// Any music start on this box re-forms its persisted default group and
		// wakes the stored members (#70): hardware keys and Spotify Connect
		// starts reach the agent only through this gabbo signal.
		onSourcePlaying: webuiSrv.KickDefaultGroup,
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
		// Login-error carve-out for the recall verify: a NOT_LOGGED_IN rejection for
		// a recall makes a now-closed short slot pull an unreliable "played" signal
		// (the box's re-login source bounce serves the stream ~3s then drops it with
		// no audio). slotFetchLive tells a still-open pull from that bounce;
		// loginErrorSinceFn tells whether a 1036 landed for this recall.
		slotFetchLive:     streamProxySrv.SlotFetchLive,
		loginErrorSinceFn: webuiSrv.LoginErrorSince,
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
			foreignPresets.NoteBoxList(out)
		},
		// Let a hardware press of a queue preset (a saved DLNA folder) start the
		// webui play-queue instead of the single-track recall.
		recallSlot: webuiSrv.RecallSlot,
	}
	// A pause, stop or transfer-away pressed in the SPOTIFY app arrives only as
	// a go-librespot event: no gabbo key frame, no STR endpoint. Without this
	// wiring the starved box's source drop classified as a spontaneous firmware
	// off and the auto-revive switched the speaker right back on ("playback
	// cannot be stopped from the Spotify app", #78). OnUserStop stamps BOTH
	// latches (hardware-verify stand-down + webui NoteUserStop), exactly like a
	// gabbo STOP_STATE; a Connect play/active clears the webui latch again so
	// the app-initiated resume paths keep working. Echoes of STR's own staged
	// recall commands are filtered inside the manager.
	spotifyMgr.SetConnectIntentHooks(
		func(event string) {
			wsHandler.OnUserStop(context.Background())
			// Durable half of the app-pause fix (Klaus, 2026-08): with the
			// engine paused the box keeps its UPnP selection, drains its
			// buffer on the starved stream and re-fetches it, which used to
			// attach-resume the engine and restart the music the user just
			// paused. Actively stop the box transport so it stops fetching at
			// all. The STOP_STATE the box answers with is excused by the
			// own-transport-command classifier wired below, exactly like every
			// other SOAP Stop STR sends. Gated on Streaming(): an engine event
			// arriving while the box plays a NON-Spotify source (radio after a
			// source switch) must not stop that source. Async so a slow box
			// cannot stall the engine's event loop this hook runs on.
			if spotifyMgr.Streaming() {
				go func() {
					sctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
					defer cancel()
					if err := renderer.Stop(sctx); err != nil {
						logger.Debug("spotify: stopping the box transport after a Spotify-app pause failed", "err", err, "event", event)
						return
					}
					logger.Info("spotify: Spotify-app pause, stopped the box transport so the starved stream is not re-fetched", "event", event)
				}()
			}
		},
		webuiSrv.ClearUserStop,
	)
	// What the phones actually ask for, alongside what the speaker reports. The
	// remote picks its language from the browser and from the speaker, and the
	// browser value is flattened to en-US by anti-fingerprinting settings in some
	// browsers. How often that hits real users is not worth reasoning about, so it
	// is recorded: the next report about a remote in the wrong language arrives
	// with the evidence attached.
	webui.RegisterDebugSection("phone_languages", func() any { return webuiSrv.PhoneLanguages() })
	// The :8090 outage record (the 2026-08-18 ST20 zone-leader freeze was only
	// provable by hand-counting stale-status WARNs) and the box-owned preset
	// slots STR preserves in the marge answer (the Deezer slot-3 loss class).
	webui.RegisterDebugSection("boseapp_health", func() any { return webuiSrv.BoseAppHealth() })
	webui.RegisterDebugSection("foreign_presets", foreignPresets.DebugState)
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
	// bmx_adapter answers the two questions a failed hardware preset press
	// raises and no bundle could answer before (#600): what the speaker itself
	// complained about and what it was acting on at the time, and whether it
	// ever fetched the service list that tells it where STR's adapters live.
	// A native radio preset's location is RELATIVE to the baseUrl in that
	// list, so registryFetches=0 means the speaker cannot resolve a press at
	// all, however healthy everything else looks.
	webui.RegisterDebugSection("bmx_adapter", func() any {
		fetches, last := margeSrv.RegistryFetches()
		lastStr := ""
		if !last.IsZero() {
			lastStr = last.Format(time.RFC3339)
		}
		return map[string]any{
			"registryFetches":   fetches,
			"lastRegistryFetch": lastStr,
			"boxErrors":         wsClient.BoxErrors(),
		}
	})
	// The standby classifier reads the same stamp: a source flip right after
	// STR's own push (a wake-resume/recall the firmware rejects) must not be
	// classified as a user power-off.
	webuiSrv.SetOwnTransportCmdFn(wsClient.LastOwnTransportCommand)
	// A completed (re-)onboarding wipes the box's hardware-key preset
	// registrations; re-register them right away instead of waiting for the
	// reconcile cadence.
	autoPair.SetOnPaired(func() { requestPresetKeyResyncUrgent(logger, "paired") })
	// Let the WebUI fill the Wi-Fi signal from the gabbo stream on BCO
	// boxes, whose /networkInfo reports no signal.
	webuiSrv.SetWifiSignalFn(wsClient.LastWifiSignal)
	// Let HandleEnterStandby tell a physical power-off (accompanied by a
	// userActivityUpdate key frame) from the firmware spontaneously powering
	// off STR's UPnP source (#419).
	webuiSrv.SetUserActivityFn(wsClient.LastUserActivity)
	// Report an ongoing "the box refuses every recall" state so the app can
	// offer a soft reboot, which clears it, instead of leaving the user with
	// the plug pull they would otherwise try (#419 Finding 4).
	webuiSrv.SetStorm1036Fn(wsClient.Storm1036)
	// The volume restore consults the same signal so a hand-adjusted level
	// during a recall recovery is never clamped back to the pre-recall
	// snapshot (which after a deep standby is the box's own wake default).
	wsHandler.lastUserActivity = wsClient.LastUserActivity

	// When the box rejects a source as not-logged-in (errorUpdate 1036, seen on
	// the SoundTouch 300), force a re-login and stand the recall retry down so
	// STR self-heals instead of thrashing the box into a wedge (rate-limited in
	// boxws).
	wsClient.SetOnLoginError(webuiSrv.NoteBoxLoginError)
	// The box registering a source is what decides whether presets can be stored
	// natively, so re-probe the moment it says the list changed instead of waiting
	// for the cached verdict to expire.
	wsClient.SetOnSourcesChanged(invalidateNativeRadioReady)
	// A speaker that accepts a native station and then abandons it gets its
	// presets put back on the UPnP form (see noteNativeStreamDropped).
	wsClient.SetOnNativeDropped(noteNativeStreamDropped)

	// Seed the box-native preset snapshot once at start and, if the NAND preset
	// store came up empty while the box still lists STR presets, restore what
	// the recently-played history can identify (#252: presets displayed as
	// "unassigned although they are assigned" and every hardware press 404ed
	// after a pre-v0.9.14 standby power-cut wiped presets.json and the OTA
	// restart surfaced the loss). seedFirstRead closes once the first box
	// preset read was attempted; autopair's start waits on it (bounded) so the
	// first forced re-assert cannot re-onboard the box - and thereby wipe its
	// preset list - before the recovery had its one chance to snapshot it.
	seedFirstRead := make(chan struct{})
	go seedBoxPresetsAndRecoverStore(store, recentStore, *boxHost,
		func(bps []webui.BoxPreset) {
			webuiSrv.NoteBoxPresets(bps)
			// Seed the foreign-preset preservation from the same first read, so
			// the very first re-onboarding after agent start already serves the
			// box-owned slots (the wipe this guards against happens exactly
			// there; NoteBoxList unescapes the regex-captured values).
			foreignPresets.NoteBoxList(bps)
		},
		logger.With("comp", "presetrecovery"),
		func() { close(seedFirstRead) })

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
	// after waking from standby. Starts only after the preset recovery's
	// first box read was attempted (bounded wait): the first pair cycle
	// forces a re-assert, and the re-onboarding it triggers wipes the box
	// preset list the recovery needs to read (#252 warm-restart race).
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-seedFirstRead:
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return
		}
		autoPair.RunBackground(ctx, 8*time.Second, 5*time.Minute)
	}()

	// Resource heartbeat: a one-line MemAvailable + loadavg snapshot
	// every 5 minutes. The box has ~120 MB RAM and no swap, so a slow
	// leak ends in an OOM freeze that otherwise leaves no trace; this
	// makes the RAM/load trend before a freeze visible in the on-box log
	// for post-mortem. Negligible NAND traffic (12 lines/hour), now that
	// the per-second connectionState spam is gone.
	// The heartbeat rides alongside the health loop but at its own cadence: the
	// health log answers "is this box trending toward trouble", the heartbeat
	// answers "what did the process look like in the half minute before it was
	// killed", and only the second one survives the kill.
	wg.Add(1)
	go func() {
		defer wg.Done()
		runHeartbeat(ctx.Done(), spotifyMgr.Streaming, logger)
	}()
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
	// The peer roster needs our own name to recognise a stale entry for an
	// address we no longer hold (see browsePeers).
	peerSelfNameFn = func() string {
		mdnsMu.Lock()
		ann := mdnsAnnouncer
		mdnsMu.Unlock()
		n, _ := ann.Snapshot()
		return n
	}
	// The roster's second self-signal: the deviceID this agent announces. Same
	// value the announcer carries in its TXT record, so an entry adopted from
	// our own stale announcement compares equal even under a placeholder name
	// and an address we no longer hold (#697).
	peerSelfDeviceIDFn = func() string { return deviceID }
	// Post-switch network refresh (#697): fired by the webui after a CONFIRMED
	// live Wi-Fi switch. Runs on its own goroutine (the webui hook spawns it)
	// and waits for the new DHCP lease itself before repointing the mDNS
	// responder, re-registering the announcer, purging the roster's stale
	// self-entries, and deleting REDIRECT rules for addresses no longer held.
	webuiSrv.SetNetworkChangedFn(func(reason string) {
		refreshAfterNetworkChange(reason, netRefreshDeps{
			setMDNSHostAddr: func(ip net.IP) {
				if mdnsHostResp != nil {
					mdnsHostResp.SetAddress(ip)
				}
			},
			currentMDNSAddr: func() net.IP {
				if mdnsHostResp == nil {
					return nil
				}
				return mdnsHostResp.Addr()
			},
			reannounce: func(r string) error {
				mdnsMu.Lock()
				ann := mdnsAnnouncer
				mdnsMu.Unlock()
				return ann.Reannounce(r)
			},
		}, logger)
	})
	webuiSrv.SetBoxNameFn(func() (string, string) {
		mdnsMu.Lock()
		ann := mdnsAnnouncer
		mdnsMu.Unlock()
		return ann.Snapshot()
	})
	// Let the web UI's preset sync store slots natively too, on the same terms
	// as the agent's own reconcile: only when the box reports the radio source
	// registered, otherwise "" and the UPnP form is kept.
	setNativeReadyLogger(logger)
	boxcli.SetDiagLogger(logger)
	webuiSrv.SetNativePresetLocatorFn(func(name, streamURL, art string) string {
		if !nativeRadioReady(context.Background(), *boxHost) {
			return ""
		}
		return webui.OrionStationLocation(streamURL, name, art)
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
				HostName:     mdnsHostLabel,
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
	// DNS bootstrap BEFORE the clock sync: a box whose DHCP lease carries no
	// nameserver cannot resolve the clock hosts, cannot pair (autopair is
	// gated on a plausible clock) and cannot play radio, and every repair path
	// STR owns needs the very thing that is missing (#487). Repairing the
	// resolver first unlocks all three. No-op on a healthy box.
	dnsboot.EnsureResolver(logger.With("comp", "dnsboot"))
	noteBootClock(logger)
	func() {
		sctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
		clocksync.SyncNowIfImplausible(sctx, logger)
	}()
	go func() {
		clocksync.RunUntilSynced(ctx, logger, 30*time.Second)
		noteClockHealed(logger)
	}()

	// If the USB stick has a newer run.sh than the NAND run-override.sh:
	// copy it. This is the self-update path for the bootstrap. Without it
	// the old run-override.sh from the very first setup runs forever and new
	// setup wizard configs are ignored.
	go syncRunOverrideFromStick(logger)

	// Repair a trust store that lost the vendor's public roots, which kills
	// every https station AND the Spotify engine with the same terse
	// "certificate signed by unknown authority". The boot script no longer
	// produces that state, but it is the only thing that ever did and an
	// over-the-air update cannot deliver a boot script: it replaces the agent
	// binary, and the NAND copy of run-override.sh is refreshed from a USB
	// stick alone. Without this the affected speakers would re-break at every
	// boot for good, however many updates they took. See
	// internal/tlsgen/trustrepair.go.
	go repairTrustStoreWhenBootstrapIsDone(ctx, *tlsDir, logger.With("comp", "tlsgen"))

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

// routableIPv4 returns the address this speaker is reachable on, which is the
// address its own mDNS name has to resolve to. Chosen by walking the interfaces
// rather than by asking the kernel for a route, because a SoundTouch 30 keeps
// an ethernet port that is up and disconnected while it runs on Wi-Fi, and that
// port is exactly the wrong answer.
func routableIPv4() net.IP {
	return netutil.FirstLANIPv4()
}

// mdnsHostLabel is this speaker's own mDNS label, set only once the responder
// for it is live. Empty means every announcement falls back to the box's Linux
// hostname exactly as before, which is the safe half of this change.
var mdnsHostLabel string
