package main

// The composition root of the agent, split into phases. run() in main.go
// parses the flags, creates the logger and the signal context, and runs the
// phases in the order phases() lists them; the order is load-bearing (see
// bindListeners) and pinned by a test.

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/discovery"
	"github.com/JRpersonal/streborn/internal/alarm"
	"github.com/JRpersonal/streborn/internal/autopair"
	"github.com/JRpersonal/streborn/internal/bmx"
	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/boxsnapshot"
	"github.com/JRpersonal/streborn/internal/boxwrites"
	"github.com/JRpersonal/streborn/internal/boxws"
	"github.com/JRpersonal/streborn/internal/clocksync"
	"github.com/JRpersonal/streborn/internal/dnsboot"
	"github.com/JRpersonal/streborn/internal/groupkeys"
	"github.com/JRpersonal/streborn/internal/hosts"
	"github.com/JRpersonal/streborn/internal/internet"
	"github.com/JRpersonal/streborn/internal/keyactions"
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/mdnshost"
	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
	strregion "github.com/JRpersonal/streborn/internal/region"
	"github.com/JRpersonal/streborn/internal/spotify"
	"github.com/JRpersonal/streborn/internal/streamproxy"
	"github.com/JRpersonal/streborn/internal/syscheck"
	"github.com/JRpersonal/streborn/internal/sysinfo"
	"github.com/JRpersonal/streborn/internal/tlsgen"
	"github.com/JRpersonal/streborn/internal/upnp"
	"github.com/JRpersonal/streborn/internal/webhooks"
	"github.com/JRpersonal/streborn/internal/webui"
	"github.com/JRpersonal/streborn/internal/zones"

	"github.com/JRpersonal/streborn/anonymise"
)

// agentFlags are the command-line flags run() parsed, by value.
type agentFlags struct {
	presetsPath     string
	webuiAddr       string
	margeAddr       string
	margeTLSAddr    string
	bmxAddr         string
	hostsPath       string
	applyHosts      bool
	tlsDir          string
	tlsEnabled      bool
	boxHost         string
	regionFile      string
	pendingNameFile string
}

// agent holds what the phases share. A field is set by the phase named in
// its comment and read by later ones; nothing here is read before it is set,
// because the phases run in phases() order. Never copied: it carries the
// WaitGroup and the announcer mutex.
type agent struct {
	flags  agentFlags
	logger *slog.Logger
	// ctx is the signal context every goroutine derives from; cancel ends it
	// early on a subsystem error.
	ctx    context.Context
	cancel context.CancelFunc
	// wg tracks the listeners and the loops wait() waits for; errs carries the
	// first listener failure to it.
	wg   sync.WaitGroup
	errs chan error

	// bootstrap
	deviceID string
	// boxLog is the syslog reader once buildBoxLink made it; the start-up
	// splash, which runs from bootstrap, asks it whether the firmware's
	// display controller has finished booting.
	boxLog atomic.Pointer[boxlog.Reader]

	// buildStores
	store            *presets.Store
	webhooksStore    *webhooks.Store
	groupKeysStore   *groupkeys.Store
	keyActionsStore  *keyactions.Store
	alarmStore       *alarm.Store
	alarmState       *alarm.StateStore
	zonesStore       *zones.Store
	mediaServerStore *mediaservers.Store
	recentStore      *recent.Store
	hostsMgr         *hosts.Manager
	foreignPresets   *foreignPresetStore
	regionRes        *strregion.Resolver

	// buildMarge
	margeSrv   *marge.Server
	bootReason string

	// buildMedia
	bmxSrv         *bmx.Resolver
	autoPair       *autopair.Manager
	region         string
	streamProxySrv *streamproxy.Server
	spotifyMgr     *spotify.Manager
	mdnsHostResp   *mdnshost.Responder

	// buildWebui
	webuiSrv *webui.Server

	// buildBoxLink
	wsClient      *boxws.Client
	keyTrace      *boxlog.Reader
	seedFirstRead chan struct{}

	// startDeferred, once the mDNS announce landed; nil until then and when
	// it failed. Read under mdnsMu by the seams wireLate binds and by wait().
	mdnsMu        sync.Mutex
	mdnsAnnouncer *discovery.Announcer
}

// phase is one step of the start-up sequence.
type phase struct {
	name string
	run  func() error
}

// phases is the start-up order. It is the one place that order is written
// down: the six build phases prepare everything the listeners need without
// waiting on the speaker, bindListeners binds them, and only then does the
// heavy init start. TestAgentPhasesBindListenersBeforeHeavyInit pins it.
func (a *agent) phases() []phase {
	return []phase{
		{"bootstrap", a.bootstrap},
		{"buildStores", a.buildStores},
		{"buildMarge", a.buildMarge},
		{"buildMedia", a.buildMedia},
		{"buildWebui", a.buildWebui},
		{"buildBoxLink", a.buildBoxLink},
		{"bindListeners", a.bindListeners},
		{"startBackground", a.startBackground},
		{"wireLate", a.wireLate},
		{"startDeferred", a.startDeferred},
	}
}

// runPhases runs the phases in order and stops at the first error, naming the
// phase. observe, when set, is told each phase's name before it runs (the
// order test records them).
func runPhases(phases []phase, observe func(name string)) error {
	for _, p := range phases {
		if observe != nil {
			observe(p.name)
		}
		if err := p.run(); err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
		}
	}
	return nil
}

// bootstrap runs the pre-store start-up work: the supplemental TLS roots, the
// boot-script self-heal, the version stamp, sshd, the deviceID guess and the
// NAND reclaim.
func (a *agent) bootstrap() error {
	// Local names, so the body reads as it did in run().
	logger := a.logger

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

	a.deviceID = deviceID
	return nil
}

// buildStores loads every NAND-backed store the servers are built on: the
// presets, webhooks, group keys, key actions, alarms, zones, media servers and
// the recently-played ring, plus the hosts manager, the foreign-preset
// preservation and the region resolver.
func (a *agent) buildStores() error {
	// Local names, so the body reads as it did in run().
	logger, deviceID := a.logger, a.deviceID

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
	a.flags.presetsPath = canonicalPresetsPath(a.flags.presetsPath, logger)

	if st, statErr := os.Stat(a.flags.presetsPath); statErr == nil {
		logger.Warn("preset store phase: file present",
			"file", a.flags.presetsPath, "bytes", st.Size(), "mtime", st.ModTime().UTC().Format(time.RFC3339))
	} else if os.IsNotExist(statErr) {
		logger.Warn("preset store phase: file absent", "file", a.flags.presetsPath)
	} else {
		logger.Warn("preset store phase: file stat failed", "file", a.flags.presetsPath, "err", statErr)
	}
	store, err := presets.Load(a.flags.presetsPath)
	if err != nil {
		logger.Warn("preset store phase: load failed, continuing with empty list",
			"err", err, "file", a.flags.presetsPath)
		store = presets.New()
	} else {
		logger.Warn("preset store phase: ready",
			"count", len(store.All()), "file", a.flags.presetsPath)
	}

	// Webhook config (user-defined HTTP requests fired on a box trigger, e.g. the
	// remote thumbs keys -> a smart-home toggle). Persisted on NAND so it survives
	// a stick removal. Missing file is fine (empty config).
	webhooksStore, whErr := webhooks.Load("/mnt/nv/streborn/webhooks.json", logger.With("comp", "webhooks"))
	if whErr != nil {
		logger.Warn("webhooks config load failed, continuing with empty config", "err", whErr)
	}

	// Group keys (#863): saved groups bound to the remote's thumbs keys. Read
	// once here, written only when the app saves; a press reads the main
	// speaker's live zone and reuses the zone form/dissolve endpoints, over
	// loopback when this speaker leads the group and over the LAN otherwise.
	groupKeysStore, gkErr := groupkeys.Load("/mnt/nv/streborn/group-keys.json", logger.With("comp", "groupkeys"))
	if gkErr != nil {
		logger.Warn("group keys load failed, continuing with none", "err", gkErr)
	}
	groupKeysStore.SetClient(groupkeys.NewHTTPClient(groupkeys.HTTPOptions{
		IsSelf: func(m groupkeys.Member) bool {
			if m.DeviceID != "" && deviceID != "" && strings.EqualFold(m.DeviceID, deviceID) {
				return true
			}
			return m.IP != "" && ownIPv4s()[m.IP]
		},
		// The announced id, so the client can tell an id match from an
		// address-only match: after a DHCP renumbering another speaker's
		// stored address can be this box's own.
		SelfDeviceID: deviceID,
		PortHint:     peerWebPort,
		// Current addresses by id: the roster for peers, the LAN interface
		// for this box itself (a member's stored address may be stale too,
		// and the main speaker enrols whoever answers at it).
		PeerIP: func(id string) string {
			if deviceID != "" && strings.EqualFold(id, deviceID) {
				return ownLANIPv4()
			}
			return peerIPByDeviceID(id)
		},
		PeerDeviceID:  peerDeviceIDAt,
		PeerDeviceIDs: peerDeviceIDsAt,
	}))
	webui.RegisterDebugSection("group_keys", groupKeysStore.Snapshot)

	// Key actions: the sleep timer on a thumbs key. Read once here, written
	// only when the app saves; a press steps the RAM-only timer.
	keyActionsStore, kaErr := keyactions.Load("/mnt/nv/streborn/key-actions.json", logger.With("comp", "keyactions"))
	if kaErr != nil {
		logger.Warn("key actions load failed, continuing with none", "err", kaErr)
	}
	webui.RegisterDebugSection("key_actions", keyActionsStore.Snapshot)

	// Alarm clock: a preset, but timed. The document is read once here and
	// written only when an editor saves it; the scheduler keeps what it has
	// already fired in a separate file, so a fire at 06:30 can never clobber an
	// edit made a minute earlier.
	alarmStore, alErr := alarm.Load("/mnt/nv/streborn/alarms.json", logger.With("comp", "alarm"))
	if alErr != nil {
		logger.Warn("alarms load failed, continuing with none", "err", alErr)
	}
	alarmState := alarm.LoadState("/mnt/nv/streborn/alarms-state.json", logger.With("comp", "alarm"))

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
	if a.flags.applyHosts {
		hostsMgr = hosts.New(a.flags.hostsPath, logger)
	}

	// Box-owned presets STR did not write (Deezer, TuneIn, STORED_MUSIC, ...),
	// preserved so the marge preset answer below cannot starve them out of the
	// box's own list (see foreignpresets.go).
	foreignPresets := newForeignPresetStore("/mnt/nv/streborn/foreign-presets.json",
		logger.With("comp", "foreignpresets"))
	foreignPresets.ownedByStore = func(slot int, location string) bool {
		p, ok := store.Get(slot)
		return ok && p.IsNative() && p.Native.Location == location
	}

	// The speaker's country: the wizard region (set below once region.txt is
	// read) wins, the firmware's countryCode (fed by pollBoxInfo, cached on
	// NAND so the first availability answer after a boot is right) is the
	// fallback. A US speaker gets the firmware's own Pandora and iHeartRadio.
	regionRes := strregion.New("/mnt/nv/streborn/box-country.txt", logger.With("comp", "region"))

	a.store = store
	a.webhooksStore = webhooksStore
	a.groupKeysStore = groupKeysStore
	a.keyActionsStore = keyActionsStore
	a.alarmStore = alarmStore
	a.alarmState = alarmState
	a.zonesStore = zonesStore
	a.mediaServerStore = mediaServerStore
	a.recentStore = recentStore
	a.hostsMgr = hostsMgr
	a.foreignPresets = foreignPresets
	a.regionRes = regionRes
	return nil
}

// buildMarge creates the marge stand-in (streaming.bose.com) with its stores
// and forensic sections, starts the deviceID correction and the pair
// adoption, and records why this agent started.
func (a *agent) buildMarge() error {
	// Local names, so the body reads as it did in run().
	logger, ctx, deviceID := a.logger, a.ctx, a.deviceID
	store, foreignPresets, regionRes := a.store, a.foreignPresets, a.regionRes

	// Initialize subsystems
	margeSrv := marge.New(logger.With("comp", "marge"),
		marge.WithDeviceID(deviceID),
		marge.WithReflectSourcesPath(boxsnapshot.ReflectPath()),
		// Persist the stereo-pair record so the firmware's group poll keeps
		// getting the same answer across agent restarts (a "not grouped"
		// fallback after a restart is what invited the firmware to re-create
		// the record from its own point of view).
		marge.WithGroupPath("/mnt/nv/streborn/marge-group.json"),
		// With no record stored, the group poll asks the firmware what it holds
		// and adopts a healthy pair instead of answering it away (a pair formed
		// in the Bose app survives the first STR boot this way).
		marge.WithFirmwareGroupProbe(firmwareGroupProbe(a.flags.boxHost)),
		marge.WithDeviceIDPath("/mnt/nv/streborn/deviceid"),
		// The firmware's own Pandora and iHeartRadio (#243): on for a US
		// speaker, elsewhere only with the opt-in marker. The stand-in then
		// reports them available and keeps the source the firmware's own
		// client registers (docs/streaming/us-services.md).
		marge.WithPandora("/mnt/nv/streborn/pandora-optin", "/mnt/nv/streborn/pandora-source.json"),
		marge.WithIHeart("/mnt/nv/streborn/iheart-source.json"),
		marge.WithRegion(regionRes.Country),
		// The speaker's own hold-to-store gesture PUTs the playing station to
		// marge; keep it in the STR store so the app shows the key and the
		// reconcile keeps it registered (see holdstore.go).
		// The live queue and the logo chains come from the web server, which
		// is created further down; heldWebui is bound to it there.
		marge.WithPresetKeeper(newHeldPresetKeeper(store, heldLiveFrom(&heldWebui), logger.With("comp", "holdstore"))),
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
				if p.IsNative() {
					// A Pandora or iHeartRadio station the speaker plays
					// itself: the cloud view carries its own ContentItem, so
					// a re-read keeps the key on that station.
					out = append(out, marge.Preset{
						ID:            p.Slot,
						Source:        margeXMLEscape(p.Native.Source),
						Type:          margeXMLEscape(p.Native.ItemType),
						Location:      margeXMLEscape(p.Native.Location),
						SourceAccount: margeXMLEscape(p.Native.SourceAccount),
						ItemName:      margeXMLEscape(p.Name),
						ContainerArt:  margeXMLEscape(p.Native.ContainerArt),
					})
					continue
				}
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
	go correctDeviceIDFromBox(ctx, margeSrv, a.flags.boxHost, logger)

	// Forensic sections for /api/debug/state. The marge trail (millisecond
	// timestamps) is what lets a bundle answer whether the box exchanged
	// anything with marge inside the ~200 ms window of a Wave sysLanguage
	// revert, and the clock verdict correlates 1036 storms / dead-playback
	// boots with a plug-pull RTC loss (#419 Finding 4). Registered before the
	// listeners spawn so the very first debug fetch already carries them.
	// Whether this speaker's hardware keys run on the native path or still
	// depend on the 1036 recovery machinery. Every incoming bundle becomes a
	// data point for deciding when that machinery can be retired.
	nativeStatusHost := a.flags.boxHost
	webui.RegisterDebugSection("native_presets", func() any {
		return nativePresetStatus(nativeStatusHost)
	})
	// The store recovery's verdict (#882): what the box listed at agent start,
	// how many of those slots STR itself wrote, and why an empty store was or
	// was not refilled. Without it a bundle from a reinstalled speaker could
	// not say whether the six dead keys were ever seen.
	webui.RegisterDebugSection("preset_recovery", presetRecoveryStatus)
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
	// STR's own splash on the Portable's OLED after an install, an update or
	// a box boot (internal/oled; a no-op on every other panel and when the
	// user switched it off). Decided here because the OTA marker is consumed
	// by webui.New further down; drawn in the background once BoseApp's
	// display thread is up.
	splashKind := oled.DecideKind(agentStamp(), webui.OTARebootPending(), strings.HasPrefix(bootReason, "box-boot"))
	// Every panel hold (splash, notices, track overlay, games) waits for the
	// firmware's display controller to leave Bootup: a hold before that
	// leaves the speaker's own screens dark until a reboot (#1456, #841).
	// Wired before anything can post a notice.
	displayControllerUp := func() bool {
		r := a.boxLog.Load()
		return r != nil && r.DisplayControllerUp()
	}
	oled.SetDisplayUp(displayControllerUp)
	go oled.ShowAtStart(splashKind, logger.With("comp", "oled"), displayControllerUp)
	// The short display notices (saved keys, empty keys, find this speaker)
	// log under the same component; a box boot arms the "keys restored
	// after reboot" notice, shown once per speaker (displaynotices.go).
	oled.SetNoticeLogger(logger.With("comp", "oled"))
	if strings.HasPrefix(bootReason, "box-boot") {
		armRestoreNotice(time.Now())
	}
	go func() {
		ledger := time.NewTicker(time.Hour)
		defer ledger.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ledger.C:
			}
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
			c := boxapi.New(a.flags.boxHost)
			emptyReads := 0
			for i := 0; i < 40; i++ { // give a slow boot up to ~20 min
				select {
				case <-ctx.Done():
					return
				case <-time.After(30 * time.Second):
				}
				if !margeSrv.GroupRestoredUnconfirmed() {
					return // confirmed by a live signal meanwhile
				}
				gctx, gcancel := context.WithTimeout(ctx, 6*time.Second)
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
	// A pair the firmware holds but STR does not (first boot after installing
	// STR on a speaker paired in the Bose app) is adopted before the firmware's
	// group poll can be answered without it.
	go adoptFirmwarePairAtStartup(ctx, margeSrv, a.flags.boxHost, logger.With("comp", "groupadopt"))
	webui.RegisterDebugSection("clock_status", clockStatusSnapshot)
	// display_clock: the firmware's own view of the clock on the display, read
	// only when a diagnostic is taken (#841: a frozen display clock while the
	// system clock was correct, and the bundle could not show which).
	displayClockBase := "http://" + net.JoinHostPort(a.flags.boxHost, "8090")
	webui.RegisterDebugSection("display_clock", func() any { return displayClockSnapshot(displayClockBase, time.Now) })
	// net_reachability captures the "plays radio but unreachable" fault: the
	// cheap carrier/IP/gateway-ARP snapshot the health tick watches, plus the
	// on-demand INPUT chain, routes, ARP table and listeners for a bundle.
	webui.RegisterDebugSection("net_reachability", netReachabilityDebugSection)
	// internet: the passive "can this speaker reach the internet" verdict
	// (internal/internet) behind /api/agent/version's internet field, with
	// the evidence: last success, last failure, its reason and source. Fed by
	// the clock sync and the stream proxy; no probe or timer of its own.
	webui.RegisterDebugSection("internet", internet.Snapshot)
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
		root := tlsgen.ReadRootCAPEM(a.flags.tlsDir)
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

	a.margeSrv = margeSrv
	a.bootReason = bootReason
	return nil
}

// buildMedia creates the bmx stand-in, the AutoPair manager, the stream
// proxy and the Spotify engine supervisor, starts the preset sync and the
// reconcile loop, reads the wizard region, answers for this speaker's own
// mDNS name, and restores the peer roster.
func (a *agent) buildMedia() error {
	// Local names, so the body reads as it did in run().
	logger, ctx, deviceID := a.logger, a.ctx, a.deviceID
	store, webhooksStore, zonesStore := a.store, a.webhooksStore, a.zonesStore
	margeSrv, regionRes := a.margeSrv, a.regionRes

	bmxSrv := bmx.New(logger.With("comp", "bmx"))
	// The AutoPair manager is created up here so it can also be used in the
	// WS and webui handlers.
	autoPair := autopair.New(logger.With("comp", "autopair"), autopair.Config{
		BoxHost: a.flags.boxHost,
	})

	// Initial preset sync to the box in the background. The box must know all
	// presets as UPnP ContentItems so the hardware buttons can trigger the
	// nowSelectionUpdated WebSocket event with a location. Plus a periodic
	// reconciler (every 5 min) so inconsistencies caused by a box reboot or
	// Bose state resets are healed automatically — the user normally never
	// needs to press the "repair hardware buttons" button.
	go initialBoxPresetSync(store, a.flags.boxHost, logger)
	go periodicPresetReconcile(store, a.flags.boxHost, logger, webhooksStore)

	// Read the region from a file on start (provisioned by the setup wizard).
	region := loadRegion(a.flags.regionFile, logger)
	regionRes.SetSTR(region)

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
	spotifyBox := boxapi.New(a.flags.boxHost)
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
	// The Ogg chain seam (internal/spotify/oggchain.go) reads the box's free
	// memory to split a long track before the memory guard fires, and stamps
	// the engine's RSS onto its seam lines so a bundle can tell firmware
	// retention from the engine holding the track.
	spotifyMgr.MemAvailKB = func() int64 { a, _ := readMemKB(); return a }
	// Where this speaker stands in a stereo pair, so the Spotify app shows the
	// PAIR as one device instead of its two halves (#976).
	//
	// The marge group record is the source, NOT the zone store. A marge group IS
	// a stereo pair by construction (describesPair refuses a document without a
	// masterDeviceId and exactly two roles, and nothing failing it is stored or
	// answered with), and a pair formed through STR leaves
	// zones.json empty: measured on two ST10s on 2026-09-27, where the zone store
	// carried nothing at all while marge_group.present was true on both halves.
	// Reading the zone store would have made this whole change a no-op.
	//
	// The pair name comes from the same record, which is the one the Bose app
	// reads and writes, so a rename in either shows up in the other.
	//
	// The self id is read from marge too rather than from the startup guess: that
	// guess takes a MAC off the first interface it finds and a two-interface
	// speaker has two, so correctDeviceIDFromBox replaces it with the box's own
	// answer. Comparing the pair master against the wrong MAC would make a master
	// think it is a follower and take its own Connect entry down, which is the one
	// failure that leaves a pair with no Spotify entry at all.
	spotifyMgr.SetStereoFn(func() (spotify.StereoRole, string) {
		master, name, ok := margeSrv.GroupPair()
		if !ok || master == "" {
			return spotify.StereoNone, ""
		}
		self := margeSrv.DeviceID()
		if self == "" {
			// Not knowing who we are is not a reason to guess at a role. The box
			// answers within the first couple of minutes and the watcher picks it
			// up on its next tick.
			return spotify.StereoNone, ""
		}
		if strings.EqualFold(master, self) {
			return spotify.StereoMaster, name
		}
		return spotify.StereoFollower, name
	})
	spotifyMgr.EngineRSSKB = func(pid int) int64 {
		rss, _ := readProcStatus(fmt.Sprintf("/proc/%d/status", pid))
		return rss
	}
	webui.RegisterDebugSection("spotify_chain", func() any {
		snap := spotifyMgr.ChainSnapshot()
		snap["engineRSSKB"] = engineRSSKB(spotifyMgr)
		return snap
	})
	// Whether the engine has a saved Spotify login, and whether Spotify
	// refusing it was handled (set aside, waiting for a tap). No account names.
	webui.RegisterDebugSection("spotify_auth", func() any {
		return spotifyMgr.CredentialSnapshot()
	})
	// Per-playlist resume points and the skip_to_uri the last recall used, so a
	// report of a preset resuming on another preset's track (discussion #1077)
	// can be settled from the bundle. URIs and times only.
	webui.RegisterDebugSection("spotify_resume", func() any {
		return spotifyMgr.ResumeSnapshot()
	})
	// The box <-> Spotify volume bridge: the speaker level and the Connect
	// volume the Spotify app is shown, so "the slider shows the wrong volume"
	// can be settled from a bundle.
	webui.RegisterDebugSection("spotify_volume", func() any {
		return spotifyMgr.VolumeSnapshot()
	})

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
			if resp, err := mdnshost.Start(ctx, logger.With("comp", "mdnshost"), label, ip); err != nil {
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
				// Tell the HTTP surface too: the desktop app reads this back to put a
				// name on the phone-remote QR code instead of an address.
				webui.SetMDNSHostName(resp.Name())
				mdnsHostResp = resp
				// Put the counters in the diagnostic bundle. The open question
				// they answer: a SoundTouch 30 here is missing from four
				// browses in a row while its engine runs and is logged in. If
				// queriesSeen climbs on that box, the questions arrive and the
				// answers are getting lost; if it stays at zero, the box is
				// never being asked. Those are different faults.
				webui.RegisterDebugSection("mdns_host", func() any { return resp.Stats() })
				// Closed in wait(), once the listeners are down.
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
			gctx, gcancel := context.WithTimeout(ctx, 3*time.Second)
			defer gcancel()
			z, err := boxapi.New(a.flags.boxHost).GetZone(gctx)
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
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			_ = browsePeers(ctx, logger.With("comp", "peers"))
		}
	}()

	// The salt behind the pseudonyms in anything this speaker exports (the
	// phone's diagnostic button, the debug state a bundle carries). Generated
	// once and kept on NAND, never inside an export: unsalted, a MAC token was
	// a 24-bit sweep away from the address itself (#971).
	if err := anonymise.LoadOrCreateSalt("/mnt/nv/streborn"); err != nil {
		logger.Warn("could not establish the anonymisation salt, exports fall back to unsalted tokens", "err", err)
	}

	a.bmxSrv = bmxSrv
	a.autoPair = autoPair
	a.region = region
	a.streamProxySrv = streamProxySrv
	a.spotifyMgr = spotifyMgr
	a.mdnsHostResp = mdnsHostResp
	return nil
}

// buildWebui creates the web server (:8888) over the stores and engines, binds
// the reconcile seams to it, starts the zone reconcile, the WLAN boot guard,
// the leave-setup watcher and the language gate, and wires the stream proxy
// to it.
func (a *agent) buildWebui() error {
	// Local names, so the body reads as it did in run().
	logger, ctx, store := a.logger, a.ctx, a.store
	autoPair, region, streamProxySrv, spotifyMgr, margeSrv := a.autoPair, a.region, a.streamProxySrv, a.spotifyMgr, a.margeSrv
	webhooksStore, groupKeysStore, keyActionsStore := a.webhooksStore, a.groupKeysStore, a.keyActionsStore
	alarmStore, alarmState, zonesStore, mediaServerStore := a.alarmStore, a.alarmState, a.zonesStore, a.mediaServerStore
	recentStore, regionRes, bootReason := a.recentStore, a.regionRes, a.bootReason

	webuiSrv := webui.New(a.flags.webuiAddr, logger.With("comp", "webui"),
		webui.WithPresets(store),
		webui.WithBoxHost(a.flags.boxHost),
		webui.WithBoxSnapshotPath(boxsnapshot.DefaultPath()),
		webui.WithLastPlayPath("/mnt/nv/streborn/last-play.json"),
		webui.WithReflectSourcesPath(boxsnapshot.ReflectPath()),
		webui.WithAutoPair(autoPair),
		webui.WithRegion(region),
		webui.WithRegionFile(a.flags.regionFile),
		webui.WithStreamProxy(streamProxySrv),
		// The whole Spotify control plane, audio plane and engine lifecycle
		// in one value; see spotify_adapter.go for the glue that is not a
		// plain 1:1 forward (Streaming, Skip).
		webui.WithSpotify(&spotifyEngine{m: spotifyMgr}),
		webui.WithPairPartnerGone(margeSrv.PartnerUnreachable),
		webui.WithPairPartnerRecheck(margeSrv.RecheckPartner),
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
		webui.WithGroupKeys(groupKeysStore),
		webui.WithKeyActions(keyActionsStore),
		webui.WithAlarms(alarmStore, alarmState),
		webui.WithZones(zonesStore),
		webui.WithMediaServers(mediaServerStore),
		webui.WithStoredMusicPublisher(func(list []webui.StoredMusicSource) {
			out := make([]marge.StoredMusicSource, 0, len(list))
			for _, m := range list {
				out = append(out, marge.StoredMusicSource{Account: m.Account, Name: m.Name})
			}
			margeSrv.SetStoredMusicSources(out)
		}),
		// The stereo-pair record and the developer relay (marge_adapter.go).
		webui.WithMarge(margeSrv),
		webui.WithNativeServices(margeSrv),
		webui.WithRegionResolver(regionRes),
		webui.WithRecent(recentStore))

	webui.RegisterDebugSection("alarms", webuiSrv.AlarmsSnapshot)
	// Read only when a diagnostic is taken (no timer): the region, the
	// availability list as served, the stand-in's masked records and recent
	// registrations, and the firmware's own PANDORA and IHEART lines from
	// /sources.
	webui.RegisterDebugSection("usServices", webuiSrv.USServicesDebugSnapshot)

	// The preset reconcile has to know when STR woke this speaker for a group:
	// its own native preset write makes the firmware select the radio source
	// and play, which is silent on a speaker that was already idle and a room
	// full of music on one that was just woken and un-muted (#900). The
	// reconcile loop sleeps before its first pass, and the seams are atomic,
	// so wiring it here is early enough and race-free.
	setQuietWakeEpisodeActive(webuiSrv.QuietWakeEpisodeActive)
	// ... and when the user last asked for playback, so a forced pass does not
	// write into a play that is still starting (#1322).
	setLastUserPlayStartFn(webuiSrv.LastUserPlayStart)
	// ... and whether this speaker follows a group or stereo pair: its
	// firmware refuses every preset write while it does (#1077).
	setGroupFollowerFn(func() (bool, string) {
		master, _, ok := margeSrv.GroupPair()
		if decided, follower := pairRole(master, margeSrv.DeviceID(), ok); decided {
			if follower {
				return true, "stereo pair follower"
			}
			return false, ""
		}
		return webuiSrv.GroupFollower()
	})

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
	go webuiSrv.StartWLANBootGuard(ctx, bootReason)

	// Publish the user's DLNA/UPnP music sources into the marge account, which
	// the box polls for itself at boot and keeps whatever it finds there, exactly
	// the way radio arrives. That is the entire persistence mechanism: no write
	// to the speaker, so nothing here can disturb its standby countdown. Done
	// synchronously and early, because the box's account poll comes seconds
	// after its own boot.
	webuiSrv.PublishMediaServers()
	// The speaker's hold-to-store gesture reads the live queue and the logo
	// chains from here (holdstore.go).
	heldWebui.Store(webuiSrv)

	// Auto-leave the out-of-box SETUP source. A box that installed STR over the
	// network but never finished Bose's app-driven onboarding keeps the SETUP
	// source active: the display shows "follow the SoundTouch app instructions"
	// and every play is refused, though the box is otherwise ready (live:
	// ST300 + scm-ST30, 2026-07-09). One POST /setup SETUP_LEAVE clears it and
	// UPnP radio plays. Watch for it and repair it so no user has to power-cycle.
	//
	// It also records every entry into setup on the webui server, so a bundle
	// and /api/agent/version can report an episode the desktop app could not
	// see at all: while the firmware raises its setup access point the speaker
	// leaves the LAN, and the app's install wait then times out and calls a
	// successful install a failure (#873).
	go leaveSetupSourceWatcher(ctx, a.flags.boxHost, logger, webuiSrv)
	// The cause behind a SETUP source that comes back on every wake: a box
	// whose language was never set (sysLanguage 0, SETUP_LANG_NOT_SET). Close
	// that gate once per boot (boxlanguage.go).
	langFile := "/mnt/nv/streborn/lang.txt"
	if a.flags.regionFile != "" {
		langFile = filepath.Join(filepath.Dir(a.flags.regionFile), "lang.txt")
	}
	go newBoxLanguageGate(a.flags.boxHost, langFile, regionRes.Country, logger.With("comp", "boxlang")).run(ctx)

	// Auto-re-push (#4): when the Bose renderer drops a proxied stream on its
	// own (reported: radio stops after ~11 min with no upstream error), the
	// webui resumes it conservatively (only if the box stays on and idle).
	streamProxySrv.SetOnDisconnect(webuiSrv.HandleStreamDisconnect)
	// The same for the Spotify audio path, which had no recovery at all until
	// a reporter's speaker lost the Wi-Fi for two seconds at half past one in
	// the morning and stayed silent until he pressed a button.
	spotifyMgr.SetOnSinkDetach(webuiSrv.HandleSpotifyStreamDetach)
	// Wedge detection (see internal/webui/wedge.go): the proxy's last-fetch /
	// last-failure timestamps tell a wedged box apart from a failing station.
	webuiSrv.SetStreamActivityFn(streamProxySrv.LastActivity)
	webuiSrv.SetOnlineFn(streamProxySrv.BoxHasOutbound)
	// The last message the speaker display showed, and the last decision
	// taken, so a "my speaker said X" report can be matched.
	webui.RegisterDebugSection("display_message", webuiSrv.DisplayMessageSnapshot)
	// Surface speaker-side failure states ("wedged", "login-error") in
	// /api/stream-status, so the app can name the real cause instead of
	// blaming the station and cycling radio-browser alternates.
	streamProxySrv.SetBoxStateFn(webuiSrv.BoxStateHint)
	// Queue presets register on the box as a native /stream/<slot> station but
	// carry no single stored StreamURL. When the box self-activates that station
	// (a hardware source switch), this lets the proxy resolve the play queue's
	// current track and briefly hold for it, instead of 404-ing into the old
	// station (the ST20 folder-preset race).
	streamProxySrv.SetQueueLiveURLFn(webuiSrv.QueueLiveURL)
	// A speaker woken for a group resumes its own last station; when that key
	// is gone, a 404 would surface as a firmware error after the zone join and
	// take the group down. During the wake the proxy serves silence instead.
	streamProxySrv.SetHoldMissingSlotFn(webuiSrv.QuietWakeEpisodeActive)

	// ICY radio text: the proxy parses the live StreamTitle out of the
	// stream and hands it to the handler, which draws it on the panel where
	// STR owns one (trackOnPanel) and otherwise re-issues the current stream
	// URI with the new title, but only when the user switched the
	// display-track setting on (displayTrackEnabled, a NAND flag file;
	// STR_ICY_DISPLAY=1 only forces it on for dev/testing).
	streamProxySrv.SetOnTitle(webuiSrv.HandleStreamTitle)
	// On a speaker whose panel STR draws the track on (internal/oled), the
	// end of the stream takes it off again.
	streamProxySrv.SetOnTitleEnd(webuiSrv.HandleStreamEnded)

	a.webuiSrv = webuiSrv
	return nil
}

// buildBoxLink wires the speaker's own signals: the gabbo WebSocket handler
// and client, the syslog key trace, the hold-to-store saver, the UPnP
// renderer and the Spotify intent hooks, and starts the box preset seed.
func (a *agent) buildBoxLink() error {
	// Local names, so the body reads as it did in run().
	logger, ctx, store := a.logger, a.ctx, a.store
	webhooksStore, groupKeysStore, keyActionsStore, recentStore := a.webhooksStore, a.groupKeysStore, a.keyActionsStore, a.recentStore
	foreignPresets, autoPair, streamProxySrv, spotifyMgr := a.foreignPresets, a.autoPair, a.streamProxySrv, a.spotifyMgr
	margeSrv, webuiSrv := a.margeSrv, a.webuiSrv

	// Hardware preset buttons: the box sends a presetSelectionUpdated event via
	// WebSocket on 8080 (gabbo protocol) when the user physically presses a
	// button. We hook the event and trigger our UPnP player.
	renderer := upnp.NewBoseRenderer(a.flags.boxHost)
	// The speaker's own playback clock, for the Spotify buffer-lag measurement
	// (internal/spotify/boxlag.go): the app shows the position go-librespot has
	// delivered, the room hears what the box has rendered, and this is the only
	// side that can say how far apart they are. Measured on stream attach and on
	// each track boundary, never polled.
	//
	// RelPosition, not PositionInfo: the measurement needs to know whether the
	// box gave a readable clock, and PositionInfo reports call success while
	// mapping an unreadable RelTime to zero. A zero there would be recorded as
	// the box having played none of the audio it was given, i.e. the largest lag
	// the measurement can express, and nothing in the bundle would say so.
	spotifyMgr.SetBoxPositionFn(renderer.RelPosition)
	// publishBoxPresets is the ONE place a box preset list read from the
	// speaker (a gabbo presetsUpdated frame, the boot seed read, the
	// reconcile's own /presets read) reaches the webui cache and the
	// foreign-preset preservation. boxws forwards EMPTY frames as well since
	// #882; what an empty list means depends on the STR store, which only this
	// root knows: with an empty store the firmware really has nothing and the
	// desktop grid must follow (six stale "playable" tiles were the #882
	// symptom); with a non-empty store it is a boot-window partial list the
	// reconcile is about to re-register, and the cache keeps the last real
	// list. The foreign preservation keeps its own partial-list rule.
	//
	// It is also where a slot gets its Lost verdict: STR-origin, nothing in
	// the store, no webhook-only key on it. The desktop cannot make that call
	// (a webhook placeholder looks exactly like a dead key from the location),
	// so it is made here, once, for every list that reaches the cache.
	publishBoxPresets := func(bps []webui.BoxPreset) {
		stampLostBoxPresets(bps, store, webhooksStore)
		if len(bps) > 0 || len(store.All()) == 0 {
			webuiSrv.NoteBoxPresets(bps)
		}
		foreignPresets.NoteBoxList(bps)
	}
	setBoxPresetListSink(publishBoxPresets)
	wsHandler := &presetWsHandler{
		logger:   logger.With("comp", "boxws"),
		store:    store,
		renderer: renderer,
		autoPair: autoPair,
		boxHost:  a.flags.boxHost,
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
		// A thumbs key that carries a saved group forms/dissolves it (#863)
		// and wins over a webhook on the same key.
		groupKeys: groupKeysStore,
		// A thumbs key that carries the sleep timer steps it 15/30/60/off;
		// it wins over a webhook on the same key, like a group.
		keyActions: keyActionsStore,
		onSleepKey: webuiSrv.SleepKeyPress,
		// A thumbs key or AUX bound to snooze snoozes a ringing alarm; the
		// alarm clock also follows the source (STANDBY ends a ringing alarm,
		// AUX is the AUX key).
		onSnoozeKey:    webuiSrv.AlarmSnoozePress,
		onAuxSnoozeKey: webuiSrv.AlarmAuxKeyPress,
		onAlarmSource:  webuiSrv.AlarmSourceChanged,
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
		// A native preset the firmware activated itself, which is also what its
		// power-on resume looks like.
		noteNativeLastPlay: webuiSrv.NoteNativeLastPlay,
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
		// A key that cannot play says why on the speaker display (display
		// models only; the webui applies every guard).
		showDisplayMessage: webuiSrv.ShowKeyMessage,
		noteBoxHealthy:     webuiSrv.NoteBoxHealthy,
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
		// The firmware raised its own setup access point (#873): record the
		// episode for the app's after-the-fact explanation and wake the
		// leave-setup watcher, so the clear happens now rather than at its next
		// tick (up to five minutes away on the reporter's box) while still
		// going through the watcher's clear budget and backoff.
		onSetupAPRaised: func() {
			webuiSrv.NoteBoxSetupEpisode()
			nudgeLeaveSetupWatcher()
		},
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
					StrOrigin: isOwnBoxPresetLocation(p.Location),
				})
			}
			publishBoxPresets(out)
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
			wsHandler.OnUserStop(ctx)
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
					sctx, scancel := context.WithTimeout(ctx, 8*time.Second)
					defer scancel()
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
	// What drawing the playback screen costs: how long STR held the panel,
	// how often it drew, and the CPU time of BoseApp and the agent, so two
	// readings a few minutes apart (screen on, screen off) give the
	// difference. Read only when a diagnostic is taken.
	webui.RegisterDebugSection("display_cost", func() any {
		return map[string]any{
			"track":        oled.TrackStats(),
			"boseAppCPUms": oled.ProcessCPUms("BoseApp"),
			"agentCPUms":   oled.SelfCPUms(),
			"loadavg":      readLoadAvg(),
			"at":           time.Now().Format(time.RFC3339),
		}
	})
	webui.RegisterDebugSection("foreign_presets", foreignPresets.DebugState)
	// What the library play queue did: how many tracks it was given, how many it
	// advanced through, and the reason it ended. Before #960 a folder that
	// stopped part way through left no trace at all, so a bundle could not tell
	// a finished list from a stop somebody pressed.
	webui.RegisterDebugSection("queue_episodes", func() any { return webuiSrv.QueueForensics() })
	// When the user starts playback from the Spotify app (selecting this device)
	// while the box is on another source, point the box at the Spotify stream so
	// it actually plays instead of staying on the current source (#14).
	spotifyMgr.SetOnActivate(func(cbCtx context.Context) {
		// A sleeping speaker resumes its own last station on power-on, which
		// played for seconds before the push took over: muted through the
		// wake, its level comes back once the push is done.
		restoreLevel := webuiSrv.WakeSilentlyForPlay(cbCtx)
		defer restoreLevel()
		pctx, cancel := context.WithTimeout(cbCtx, 15*time.Second)
		// A native station the speaker is playing swallows the push while the
		// SOAP calls answer success (#1065); the shared helper stops it first
		// and checks the speaker really switched over.
		if err := webuiSrv.PushOverNativeStation(pctx, "Spotify", func() error {
			return renderer.PlayURLMime(pctx, spotifyStreamURL, "Spotify", "", "audio/ogg")
		}); err != nil {
			// A speaker that is a MEMBER of a group refuses transport control
			// outright, so this can never succeed while the group stands, and
			// the listener just sees Spotify move to another speaker. Record it
			// so the apps can say why instead of leaving the silence unexplained.
			if webui.IsGroupedRejection(err) {
				spotifyMgr.NoteGroupedRefusal()
				logger.Warn("spotify: this speaker is a member of a group, so the firmware refuses to point it at the Spotify stream; it cannot be used on its own until the group is dissolved", "err", err)
			} else {
				logger.Warn("spotify: auto-switch box to Spotify stream failed", "err", err)
			}
		} else {
			spotifyMgr.ClearGroupedRefusal()
			// The speaker now plays Spotify: a library queue it was on is
			// over, or its watcher pushes the next track over Spotify (#1301).
			webuiSrv.StopQueueForSpotify()
			webuiSrv.PanelSpotifyActive()
		}
		cancel()
	})
	// Record each Spotify song into the recently-played ring under the active
	// Spotify card (#135), so its card shows the songs that played, not just the
	// playlist frame. The engine's context rides along so a session started in
	// the Spotify app, which no recall ever opened a card for, gets its own
	// playlist card instead of being dropped (recent_spotify.go).
	// ...and to the speaker display, where STR draws the playback screen.
	spotifyMgr.SetOnTrack(func(tc spotify.TrackChange) {
		webuiSrv.NoteRecentSpotifyTrack(webui.SpotifyNowPlaying{
			Track: tc.Track, Artist: tc.Artist, Cover: tc.Cover, TrackURI: tc.TrackURI, ContextURI: tc.ContextURI,
		})
		webuiSrv.PanelSpotifyTrack(tc.Track, tc.Artist)
	})
	refreshTrackPanel = webuiSrv.RefreshTrackPanel
	wsClient := boxws.New(
		logger.With("comp", "boxws"),
		fmt.Sprintf("ws://%s:8080/", a.flags.boxHost),
		wsHandler,
	)
	// The speaker's own key trace: BoseApp decodes every remote and console
	// key and logs it into the firmware's RAM-only syslog ring once its
	// IrDevice/ConsoleButtons facilities are raised over the TAP port. That is
	// the only place Back, Forward, Thumbs up and Thumbs down are told apart
	// (the gabbo bus sends one identical bare frame for all four, #827). One
	// logread child, no NAND, no timer; the reader re-raises the level on
	// every (re)start because BoseApp forgets it at reboot.
	keyTrace := boxlog.New(
		logger.With("comp", "boxlog"),
		func(ctx context.Context, cmd string) (string, error) { return boxcli.Send(ctx, a.flags.boxHost, cmd) },
		wsHandler.OnKeyEvent,
	)
	wsHandler.keyTrace = keyTrace
	a.boxLog.Store(keyTrace)
	// The firmware refuses a preset write in its syslog only; the reconcile
	// reads this to tell a refused rewrite from a landed one.
	setPresetRefusalSource(keyTrace.LastPresetRefusal)
	webui.RegisterDebugSection("box_keys", func() any { return keyTrace.Snapshot() })
	// The ring is also the wake signal: the firmware logs every standby and
	// wake transition there, so a speaker switched on at the box or by its
	// remote is noticed even on a chassis whose gabbo bus never sends the
	// power frame, or while the WebSocket is between its idle recycles. The
	// handler delivers it through the same doors the bus uses and dedupes
	// the two origins (cmd/agent/powergate.go); the standby gate is the
	// dispatcher's own (STR's source was the active one).
	keyTrace.SetPowerHandler(wsHandler.OnBoxPowerEvent)
	// A held preset key while STR's own stream plays (#1217): the firmware
	// refuses to store a UPnP push, so the agent pairs the key line with the
	// firmware's own hold verdict and saves what plays (holdsave.go).
	holdSave := newHoldSaver(logger.With("comp", "holdsave"), func(ctx context.Context, slot int) (webui.HoldSaved, error) {
		srv := heldWebui.Load()
		if srv == nil {
			return webui.HoldSaved{}, fmt.Errorf("web server not up yet")
		}
		return srv.HoldSaveLive(ctx, slot)
	})
	wsHandler.holdSaver = holdSave
	// The same press tells the display notices that a key is empty, and lets
	// the marge keeper tell the user's store gesture from the boot-time sync
	// (displaynotices.go).
	holdSave.emptyKey = func(slot int) (bool, bool) {
		if srv := heldWebui.Load(); srv != nil {
			return srv.KeyEmpty(slot)
		}
		return false, false
	}
	// A physical preset key still down after heldScreenAfter takes the panel
	// before the firmware's own hold message (holdsave.go, oled keyhold.go).
	holdSave.screen = oledHeldScreen{}
	heldGestureSaver.Store(holdSave)
	keyTrace.SetPresetGestureHandler(holdSave.NoteGesture)
	// A combination also tells the leave-setup watcher that the setup which
	// follows was asked for (#1340).
	keyTrace.SetComboHandler(func(at time.Time) {
		holdSave.NoteCombination(at)
		noteSetupAsked(at)
	})
	keyTrace.SetWebKeyHandler(holdSave.NotePress)
	webuiSrv.SetHoldSaveTestFn(holdSave.SetAcceptAppKeys)
	// The same ring carries the firmware saying it got no audio out. Until now
	// those lines were classified for the bundle and delivered to nobody, so a
	// first press that produced silence was visible afterwards and unanswerable
	// at the time. See cmd/agent/firstpressrescue.go.
	host := a.flags.boxHost
	wsHandler.playingNow = func(context.Context) bool { return boxIsPlaying(host) }
	wsHandler.sourceAndPlaying = func(context.Context) (string, bool, bool) { return boxSourceAndPlaying(host) }
	// A press that fell back to Bluetooth/AUX counts as failed only when the
	// station was neither resolved nor fetched after it (#1170).
	wsHandler.stationActivity = func() time.Time {
		fetched, _ := streamProxySrv.LastActivity()
		if resolved := webuiSrv.LastStationResolve(); resolved.After(fetched) {
			return resolved
		}
		return fetched
	}
	keyTrace.SetPlayFailureHandler(wsHandler.OnPlayFailure)
	wsHandler.strSourceRecently = wsClient.UPnPActiveRecently
	// While the socket is up the bus's own standby frame is what acts (it
	// arrives with the key stamp the standby classifier reads); the ring's
	// copy waits for it and only goes through when the bus stays silent.
	wsHandler.busLive = wsClient.Connected
	// The same ring carries the firmware's own forensics (why a stream did
	// not start, standby/wake, Wi-Fi, marge complaints, overload). The reader
	// keeps a bounded classified copy and a redacted tail in RAM for the
	// diagnostic bundle; SSIDs are hashed on the speaker before they leave it.
	// "powerSignal" in the section says which origin (bus or ring) delivered
	// the last standby and wake, so a bundle shows whether the ring path is
	// the one that fired.
	webui.RegisterDebugSection("box_syslog_events", func() any {
		m := keyTrace.EventsSnapshot()
		ps, ok := m["powerSignal"].(map[string]any)
		if !ok {
			ps = map[string]any{}
			m["powerSignal"] = ps
		}
		ps["delivery"] = wsHandler.powerGate.snapshot()
		return m
	})
	webui.RegisterDebugSection("box_syslog_tail", func() any { return keyTrace.TailSnapshot() })
	// The frozen setup episode (#873). The rolling tail above is 150 lines and
	// a busy box overwrites it in seconds, so a quarter of an hour of setup
	// flapping was gone long before a bundle was exported. This one is
	// snapshotted the moment the firmware's setup state machine first speaks.
	webui.RegisterDebugSection("box_setup_tail", func() any { return keyTrace.SetupTailSnapshot() })
	// Tell the gabbo classifier about STR's OWN transport commands: the box
	// answers a SOAP Stop (and a SetURI flip) with a STOP_STATE frame that is
	// indistinguishable from the user pressing stop, and reading it as a user
	// stop latched a phantom stand-down that killed the very recall the command
	// belonged to (#252 post-v0.9.16: the wrong-state repair's Stop+ClearURI
	// aborted its own verify). Both renderer instances drive THIS box, so both
	// stamp the same classifier.
	renderer.OnTransportCommand = wsClient.NoteOwnTransportCommand
	webuiSrv.SetTransportCommandHook(wsClient.NoteOwnTransportCommand)
	// STR's own key presses (the STOP key ending a native station before a
	// UPnP push) also make the box emit a userActivityUpdate, which must not
	// veto that excusal as if a person had pressed stop (#1394).
	webuiSrv.SetOwnKeyHook(wsClient.NoteOwnKeyPress)
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
	// off STR's UPnP source (#419). The key the syslog ring decoded counts
	// too: a standby that reaches the handler from the ring while the
	// WebSocket is between its idle recycles has no bus stamp to go with it,
	// and the ring's own key line is then the only evidence that the user
	// pressed power. Whichever origin saw a key more recently wins.
	webuiSrv.SetUserActivityFn(func() time.Time {
		t := wsClient.LastUserActivity()
		if k := keyTrace.LastKeyAt(); k.After(t) {
			return k
		}
		return t
	})
	// Report an ongoing "the box refuses every recall" state so the app can
	// offer a soft reboot, which clears it, instead of leaving the user with
	// the plug pull they would otherwise try (#419 Finding 4).
	webuiSrv.SetStorm1036Fn(wsClient.Storm1036)
	// The last hold-to-store the firmware made and STR could not keep, so the
	// app can say so instead of leaving the key to snap back in silence.
	webuiSrv.SetHeldRefusalFn(func() (time.Time, int, string, string, bool) {
		r, ok := margeSrv.LastHeldRefusal()
		if !ok {
			return time.Time{}, 0, "", "", false
		}
		return r.At, r.Slot, r.Source, r.Name, true
	})
	// A save or delete on that slot through the app settles it (#1217).
	webuiSrv.SetHeldRefusalClearFn(margeSrv.ClearHeldRefusal)
	// ...and keep the rejections STR PROVOKES out of that count, or the banner
	// fires on a healthy speaker. Two paths provoke them: the firmware answers
	// its own power-on self-resume with 1036 (it restarts a source it does not
	// consider itself logged into), and a zone teardown kills every member's
	// in-flight UPnP session. Forming and dissolving a group a few times was
	// enough to raise the banner on a speaker that was fine (field,
	// 2026-09-07). Two seconds for the power-on (powerToggle1036Window): the
	// self-resume follows the toggle immediately, anything later is the box
	// speaking for itself.
	boxcli.SetBeforePowerToggle(func() {
		wsClient.Suppress1036Until(time.Now().Add(powerToggle1036Window))
		// Said out loud, in the same words internal/webui uses for its own
		// stand-downs: without a line here a bundle from a box that really
		// does reject everything around each wake looks exactly like a bundle
		// from a healthy one, because the count that would have shown it is
		// the thing being suppressed.
		logger.Debug("1036: storm counting stood down for a self-inflicted rejection",
			"why", "power toggle", "for", powerToggle1036Window.String())
	})
	webuiSrv.SetSuppress1036Fn(wsClient.Suppress1036Until)
	// The stereo balance is the one setting the firmware will not take over
	// HTTP, so its write goes out on this socket instead (gesellix on #70; the
	// envelope and the evidence are in internal/boxws/send.go). Wired here
	// because the socket client is the only thing that can send it.
	webuiSrv.SetBalanceWriteFn(wsClient.SetBalance)
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
	// The seed read goes through publishBoxPresets like every other list read,
	// so the foreign-preset preservation is seeded from the same first read
	// (the very first re-onboarding after agent start then already serves the
	// box-owned slots; the wipe this guards against happens exactly there).
	go seedBoxPresetsAndRecoverStore(store, recentStore, a.flags.boxHost,
		publishBoxPresets,
		logger.With("comp", "presetrecovery"),
		func() { close(seedFirstRead) })

	a.wsClient = wsClient
	a.keyTrace = keyTrace
	a.seedFirstRead = seedFirstRead
	return nil
}

// bindListeners binds marge, bmx and the web UI and starts the box WebSocket
// client. It runs before any heavy init on purpose: the boot watchdog in
// usb-stick/run.sh wants :8888 bound within its first checks (see the comment
// inside). Nothing slow may be added to this phase or to a phase before it.
func (a *agent) bindListeners() error {
	// Local names, so the body reads as it did in run().
	logger, ctx := a.logger, a.ctx
	margeSrv, bmxSrv, webuiSrv, wsClient := a.margeSrv, a.bmxSrv, a.webuiSrv, a.wsClient

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
	startHTTP(ctx, &a.wg, a.errs, "marge", a.flags.margeAddr, margeSrv.Handler(), logger)
	startHTTP(ctx, &a.wg, a.errs, "bmx", a.flags.bmxAddr, bmxSrv.Handler(), logger)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		if err := webuiSrv.Run(ctx); err != nil {
			a.errs <- fmt.Errorf("webui: %w", err)
		}
	}()

	// Box WebSocket listener for hardware preset buttons
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		wsClient.Run(ctx)
	}()
	return nil
}

// startBackground starts the loops that ride alongside the listeners: the key
// trace reader, the Spotify engine supervisor, the box snapshot, AutoPair,
// the heartbeat, the health and memory guard, and the BatteryMonitor
// fallback.
func (a *agent) startBackground() error {
	// Local names, so the body reads as it did in run().
	logger, ctx := a.logger, a.ctx
	keyTrace, spotifyMgr, autoPair, seedFirstRead := a.keyTrace, a.spotifyMgr, a.autoPair, a.seedFirstRead

	// Key trace reader (see keyTrace above). Returns at once on a host without
	// logread, so a dev build is unaffected.
	go keyTrace.Run(ctx)

	// Spotify preset audio plane (#78, P1): supervise librespot. Idles
	// (returns immediately) until a credential is cached, so it is safe to
	// start unconditionally.
	go spotifyMgr.Run(ctx)

	// Capture the box's presets + sources ONCE, as early as possible, before
	// STR's marge takeover makes the box drop account-linked cloud sources it
	// had (Deezer, Amazon, ...) and the presets bound to them. Persisted to
	// NAND write-once; served to the app via /api/box/snapshot so it can warn
	// the user and show what was there. See internal/boxsnapshot.
	go boxsnapshot.Capture(ctx, a.flags.boxHost, boxsnapshot.DefaultPath(), logger.With("comp", "boxsnapshot"))

	// Auto-pair background: pairs the box automatically on start. Re-pairs
	// every 5 minutes in case the box is ever lost. Plus: the WS handler
	// triggers TriggerNow on a preset press so pairing happens immediately
	// after waking from standby. Starts only after the preset recovery's
	// first box read was attempted (bounded wait): the first pair cycle
	// forces a re-assert, and the re-onboarding it triggers wipes the box
	// preset list the recovery needs to read (#252 warm-restart race).
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
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
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		runHeartbeat(ctx.Done(), spotifyMgr, logger)
	}()
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		logResourceHealth(logger, spotifyMgr)
		checkNetworkReachability(logger)
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
				logResourceHealth(logger, spotifyMgr)
				checkNetworkReachability(logger)
			case <-guard.C:
				memoryGuardCheck(logger, spotifyMgr, a.flags.boxHost)
			}
		}
	}()

	// BatteryMonitor fallback: on the Portable the Bose BatteryMonitor
	// deadlocks at init and never serves 127.0.0.1:17002, so BoseApp's
	// battery client connect-storms it and leaks fds until the box reboots
	// (~27 min). We accept on :17002 ourselves when it is unserved, which
	// stops the storm. No-op where BatteryMonitor is healthy. See
	// boseapp_recovery.go for the full root-cause writeup.
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		serveBatteryMonitorFallback(ctx, logger)
	}()
	return nil
}

// wireLate binds the seams that need the mDNS announcer, which startDeferred
// creates: the roster's self-identity, the post-switch network refresh, the
// box name for the version endpoint, and the native preset locator.
func (a *agent) wireLate() error {
	// Local names, so the body reads as it did in run().
	logger, ctx, deviceID := a.logger, a.ctx, a.deviceID
	webuiSrv, mdnsHostResp := a.webuiSrv, a.mdnsHostResp

	// === Deferred heavy init (background) ===
	// Everything below this point runs while the agent's listeners
	// are already serving. Slow steps are isolated in their own
	// goroutines so a stall in one (e.g. TLS CA generation on a
	// flash-bound NAND) does not delay another (e.g. mDNS announce).
	// Every goroutine run() starts derives from ctx (the signal context
	// created at the top of run()), so shutdown terminates them promptly;
	// the handful of helpers that take no context (the preset reconcile,
	// the stick run.sh sync, the OLED splash) are one-shot or sleep-bound
	// and end with the process.
	//
	// Order within each goroutine is preserved from the previous
	// sync flow; only the cross-goroutine ordering changes.

	// Let the version endpoint report the box name/model the announcer holds,
	// so the desktop app never has to fall back to "str-<ip>" when its own
	// /info probe is slow right after this agent restarts (#108).
	// The peer roster needs our own name to recognise a stale entry for an
	// address we no longer hold (see browsePeers).
	setPeerSelfNameFn(func() string {
		a.mdnsMu.Lock()
		ann := a.mdnsAnnouncer
		a.mdnsMu.Unlock()
		n, _ := ann.Snapshot()
		return n
	})
	// The roster's second self-signal: every id this speaker answers under, so
	// an entry adopted from our own stale announcement compares equal even under
	// a placeholder name and an address we no longer hold (#697). Read from the
	// announcer on every call, exactly like the name above: a snapshot taken
	// here would quietly stop matching the TXT record the day anything
	// re-announces a different id, and this check is the last one left when both
	// the address and the name are useless. The boot id stays in the list as a
	// second identity, and the firmware's own SoundTouch id joins it once the
	// box has answered: the app resolves a speaker's id from the firmware where
	// it can, so a record naming this box can carry an id our own announcement
	// never did.
	setPeerSelfDeviceIDsFn(func() []string {
		a.mdnsMu.Lock()
		ann := a.mdnsAnnouncer
		a.mdnsMu.Unlock()
		return announcerSelfDeviceIDs(ann, deviceID)
	})
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
				a.mdnsMu.Lock()
				ann := a.mdnsAnnouncer
				a.mdnsMu.Unlock()
				return ann.Reannounce(r)
			},
		}, logger)
	})
	webuiSrv.SetBoxNameFn(func() (string, string) {
		a.mdnsMu.Lock()
		ann := a.mdnsAnnouncer
		a.mdnsMu.Unlock()
		return ann.Snapshot()
	})
	// Let the web UI's preset sync store slots natively too, on the same terms
	// as the agent's own reconcile: only when the box reports the radio source
	// registered, otherwise "" and the UPnP form is kept.
	setNativeReadyLogger(logger)
	boxcli.SetDiagLogger(logger)
	webuiSrv.SetNativePresetLocatorFn(func(name, streamURL, art string) string {
		if !nativeRadioReady(ctx, a.flags.boxHost) {
			return ""
		}
		return webui.OrionStationLocation(streamURL, name, art)
	})
	return nil
}

// startDeferred runs the heavy init in the background while the listeners
// already serve: the mDNS announce and /info poll, the pending box name, the
// resolver and clock repair, the run.sh self-update, the trust store repair,
// the TLS listener, the deferred /etc/hosts redirect and the self-probe.
func (a *agent) startDeferred() error {
	// Local names, so the body reads as it did in run().
	logger, ctx, deviceID := a.logger, a.ctx, a.deviceID
	region, regionRes, margeSrv := a.region, a.regionRes, a.margeSrv
	mdnsHostResp, hostsMgr := a.mdnsHostResp, a.hostsMgr

	// mDNS announce: detect model from box /info, then announce. The
	// model lookup is a 5 s blocking call against the Bose firmware
	// which on a cold boot may not yet be answering. Doing it here
	// (after listeners are up) costs nothing user-visible — the
	// desktop app's discovery retries until the announce lands.
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()

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
				FriendlyName: "Bose SoundTouch " + discovery.LastN(deviceID, 6),
				Model:        "SoundTouch",
				Version:      version,
				Build:        buildStamp,
			},
		)
		if err != nil {
			logger.Warn("mDNS announce failed, continuing without", "err", err)
			return
		}
		a.mdnsMu.Lock()
		a.mdnsAnnouncer = ann
		a.mdnsMu.Unlock()

		// Background poll: refresh name AND model in mDNS TXT as
		// soon as /info on :8090 responds, then continue watching
		// for renames the user might do via the BoseApp HTTP API.
		go pollBoxInfo(ctx, a.flags.boxHost, region, regionRes, ann, logger)

		// Register again whenever the LAN address moves, the first lease after
		// a boot above all: the agent is often up before it, and until then
		// there is nothing to announce on. Repoint the name responder with it.
		go ann.WatchAddresses(ctx, 15*time.Second, func([]string) {
			if ip := routableIPv4(); ip != nil && mdnsHostResp != nil {
				mdnsHostResp.SetAddress(ip)
			}
		})
	}()

	if a.flags.pendingNameFile != "" {
		go applyPendingBoxName(ctx, a.flags.boxHost, a.flags.pendingNameFile, logger)
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
	// And keep watching it. The bootstrap above runs before the interface is
	// necessarily up, and with no default gateway it can only write the public
	// fallbacks; udhcpc then appends the household resolver BEHIND them. Go asks
	// nameservers in order with a five second timeout each, so every lookup on
	// such a boot costs ten seconds before the resolver that answers is reached,
	// and the firmware gives a station five to ten. The speaker then plays
	// nothing at all, which is what an ST20 was found doing while the ST10 beside
	// it played the same station (2026-09-27). This moves the household resolver
	// back in front as soon as it appears.
	go dnsboot.MaintainResolver(ctx, logger.With("comp", "dnsboot"))
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
	go repairTrustStoreWhenBootstrapIsDone(ctx, a.flags.tlsDir, logger.With("comp", "tlsgen"))

	// TLS termination for marge on 8443. iptables redirects the real box
	// request from 443 to it. Skip when TLS is disabled.
	// EnsureBundle generates a per-box CA on the very first boot, which
	// touches NAND and can take several seconds — keep this off the
	// listener-boot path.
	if a.flags.tlsEnabled {
		go func() {
			tlsMgr := tlsgen.New(a.flags.tlsDir, nil, logger.With("comp", "tlsgen"))
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
			startHTTPS(ctx, &a.wg, a.errs, "marge-tls", a.flags.margeTLSAddr,
				margeSrv.Handler(), tlsConfig, logger)
		}()
	}

	logger.Warn("agent listeners spawned, deferred init continues in background",
		"webui", a.flags.webuiAddr, "marge", a.flags.margeAddr, "bmx", a.flags.bmxAddr, "tlsEnabled", a.flags.tlsEnabled, "margeTLS", a.flags.margeTLSAddr)

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
		waitAddr := a.flags.margeAddr
		if a.flags.tlsEnabled {
			waitAddr = a.flags.margeTLSAddr
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
			keepNTPPinned(ctx, hostsMgr, logger.With("comp", "ntphosts"))
		}()
	}

	// Self-probe loopback connect to each listener address. When the
	// box is reachable but :8888 silently does not answer, the bash
	// watchdog (agent_port_bound in run.sh) cannot always tell on a
	// BusyBox without ss/netstat. The Go side has full net access and
	// can prove from inside the agent process whether each port is
	// actually accepting connections. Logs at WARN so the result shows
	// in any diagnostic capture.
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		runSelfProbe(ctx, logger.With("comp", "selfprobe"), []selfProbeTarget{
			{name: "webui", addr: a.flags.webuiAddr},
			{name: "marge", addr: a.flags.margeAddr},
			{name: "bmx", addr: a.flags.bmxAddr},
			{name: "marge-tls", addr: a.flags.margeTLSAddr},
		})
	}()
	return nil
}

// wait blocks until a shutdown signal or the first subsystem error, then
// winds the agent down: the panel is handed back, the listeners and loops are
// waited for, the recently-played tail is flushed, the announcer, the name
// responder and the hosts file are restored.
func (a *agent) wait() error {
	// Local names, so the body reads as it did in run().
	logger, ctx := a.logger, a.ctx
	recentStore, hostsMgr, mdnsHostResp := a.recentStore, a.hostsMgr, a.mdnsHostResp

	var firstErr error
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-a.errs:
		firstErr = err
		logger.Error("subsystem error, shutting down", "err", err)
		a.cancel()
	}
	// Hand the panel back before anything else winds down. The kernel would
	// release BoseApp's flip thread at exit anyway; this leaves it cleanly.
	oled.ClearTrack("agent exit")

	a.wg.Wait()

	// Persist the recently-played tail on a clean shutdown so it survives the
	// reboot (the in-flight debounce timer may not have fired yet). #135.
	if err := recentStore.Flush(); err != nil {
		logger.Warn("recent history flush on shutdown failed", "err", err)
	}

	a.mdnsMu.Lock()
	mdnsAnn := a.mdnsAnnouncer
	a.mdnsMu.Unlock()
	if mdnsAnn != nil {
		mdnsAnn.Close()
	}

	if hostsMgr != nil {
		if err := hostsMgr.Restore(); err != nil {
			logger.Warn("hosts file restore failed", "err", err)
		}
	}

	if mdnsHostResp != nil {
		_ = mdnsHostResp.Close()
	}

	logger.Info("streborn exited")
	return firstErr
}
