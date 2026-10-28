package main

import "time"

// Speaker discovery types: the cache entry, its grace-window constants, and BoxInfo (the Wails model the frontend renders). Split out of app_discovery.go (itself a wave-1 move out of app.go); the comments are the issue history and travel with the code.

// discEntry is one cached discovery result plus when it was last
// genuinely seen (not counting cache re-adds).
type discEntry struct {
	box  BoxInfo
	seen time.Time
	// firstMiss is when the box first failed ALL probes in an unbroken run
	// of misses (zero while the box keeps answering). Eviction is based on
	// this, not on `seen`: discovery cycles only run on user actions, so a
	// wall-clock TTL on `seen` evicted a speaker that rebooted after the
	// user had simply been listening for a few minutes - the very first
	// failed cycle saw a long-expired timestamp and dropped it with no way
	// back short of a manual refresh. Miss-based, every box gets the full
	// grace window of ACTUAL misses to finish its reboot.
	firstMiss time.Time
	// strMisses counts the consecutive cycles in which this STR box answered
	// ONLY on the stock :8090 (presence-only sightings, see
	// mergeDiscoveryCacheWith); strMissSince is when that streak began. Both
	// reset on a confirmed agent sighting. Once the streak is long enough
	// (strGoneMisses over at least strGoneMinSpan) the record is degraded to
	// a stock box flagged STRNotRunning, instead of serving the last agent
	// version forever to a speaker whose agent is gone.
	strMisses    int
	strMissSince time.Time
}

// strGoneMisses / strGoneMinSpan define when a cached STR box whose agent keeps
// silent while its stock Bose port answers is degraded to "STR not running".
// Both must hold: at least strGoneMisses consecutive presence-only sightings
// spanning at least strGoneMinSpan. The count alone would let three quick
// Refresh presses during a slow reboot degrade a healthy box; the span alone
// would degrade on a single cycle after a long idle. At the periodic refresh
// cadence (one probe a minute) this is the third refresh, about three minutes
// after the agent stopped answering. A post-OTA reboot is protected separately
// by otaRebootGrace, which outlasts this window.
const (
	strGoneMisses  = 3
	strGoneMinSpan = 2 * time.Minute
)

// stockPinGrace is how long after an in-app uninstall a box is force-classified
// as stock (see App.stockPinned). The window has to outlast the OS mDNS cache
// of the agent's last announcement and the box's reboot; an agent that answers
// a live probe inside the window (a reinstall) lifts the pin at once.
const stockPinGrace = 15 * time.Minute

// discoveryStickyTTL is how long a box stays in the list after its last
// genuine sighting. Long enough to cover a box rebooting (~60-120s on a
// slow BCO box) so it does not disappear mid-reboot, short enough that a
// truly powered-off box drops out reasonably soon.
const discoveryStickyTTL = 100 * time.Second

// discoverySTRStickyTTL is the longer eviction grace for a box already known to
// run STR. A post-OTA reboot can take longer than discoveryStickyTTL while the
// agent restarts; without this the STR cache entry is evicted mid-reboot and a
// transient stock sighting relabels the box as "needs install" until a manual
// Refresh (#108). A removed STR box lingers at most this long, an acceptable
// trade for not flickering to a wrong reinstall offer.
const discoverySTRStickyTTL = 6 * time.Minute

// otaRebootGrace is how long after STR triggers an agent OTA the target IP is
// force-classified as STR. It must comfortably cover the box rebooting and the
// agent coming back up (so the stock Bose port answering first cannot relabel
// the box), while being short enough that a genuinely re-flashed-to-stock box
// would correct itself soon after. See otaPinned and mergeDiscoveryCache (#108).
const otaRebootGrace = 4 * time.Minute

// discoveryOfflineRetention is how long a box that misses EVERY probe stays
// listed as a greyed-out offline tile after its grace window (the sticky TTLs
// above) ran out. Requirement (2026-07-26): a speaker once sighted must not
// vanish from the list just because it is rebooting or a scan misfired; it
// greys out, shows how long it has been unseen, and comes back to life the
// moment it answers. Only a box silent for a full day is dropped.
const discoveryOfflineRetention = 24 * time.Hour

// strKnownTTL is how long a box's confirmed-STR identity is remembered by
// deviceID after its last STR sighting. It must comfortably outlast a runtime
// Wi-Fi change / band switch and a box left off overnight so the speaker never
// flickers to a false "STR not installed" reinstall prompt when it returns on a
// new DHCP lease, while still being short enough that a box genuinely reverted
// to stock (uninstalled outside the app, or NAND wiped) eventually reclassifies.
// An in-app uninstall clears the entry immediately (see UninstallSTR), so this
// only bounds the exotic out-of-band case. See strKnown and mergeDiscoveryCache.
const strKnownTTL = 24 * time.Hour

// BoxInfo is the speaker entry passed to the frontend for selection.
// Kind distinguishes STR-equipped speakers from stock Bose speakers
// that still need a USB-stick install.
type BoxInfo struct {
	Name     string `json:"name"`
	Host     string `json:"host"` // IPv4 for the REST API
	Port     int    `json:"port"` // typically 8888 for STR, 8090 for stock
	DeviceID string `json:"deviceID"`
	// BoxDeviceID is the SoundTouch id the FIRMWARE calls this speaker: the
	// agent announces it next to its own deviceID and the :8090 /info probe
	// reports the same value. The speakers name each other by this one in their
	// zone documents, and it is not always what this record's DeviceID ended up
	// holding, so a group's master matched no box in the list at all (field
	// bundle 2026-09-07: three single-chip SoundTouch 10s). Kept as a SECOND
	// field rather than replacing DeviceID: that one is what every stored
	// record is keyed on. Empty for an older agent that does not announce it
	// and for a speaker whose firmware has not answered.
	BoxDeviceID  string `json:"boxDeviceID,omitempty"`
	FriendlyName string `json:"friendlyName"`
	Model        string `json:"model"`
	Version      string `json:"version"`
	// Build is the agent's build stamp (YYYY-MM-DD-HHMM) as
	// announced via mDNS TXT. Empty if the speaker runs an older
	// agent that does not yet broadcast build, or if Kind == "stock".
	// Used by the frontend update indicators to flag stamp drift
	// even when version strings match.
	Build string `json:"build"`
	// AgentBinarySha256 is the hash of the agent binary the speaker runs, from
	// its own /api/agent/version. Compared against the hash of the agent THIS
	// app carries, it answers "is this speaker current" without trusting a
	// build stamp. Empty for an older agent that does not report it, in which
	// case the version and stamp decide as before.
	AgentBinarySha256 string `json:"agentBinarySha256,omitempty"`
	// AgentRunningSha256 is the hash of the binary the agent IS running, as
	// opposed to the one on its disk. The pair is what tells a successful update
	// apart from a push that landed and did not boot; either one alone gets one
	// of those two cases wrong.
	AgentRunningSha256 string `json:"agentRunningSha256,omitempty"`
	// Offline marks a speaker that was seen earlier but has missed every probe
	// for longer than its reboot grace window (e.g. it is rebooting for a long
	// time, powered off, or off the LAN). The tile stays listed greyed out
	// (sticky list, 2026-07-26) until the box answers again or the 24 h
	// retention expires. OfflineSinceSec is how long ago the miss streak
	// started, for the "last seen ..." tooltip; only meaningful while Offline.
	Offline         bool `json:"offline,omitempty"`
	OfflineSinceSec int  `json:"offlineSinceSec,omitempty"`
	// STRNotRunning marks a speaker STR once ran on whose agent has stopped
	// answering for good while the stock Bose firmware still does (the box
	// answers :8090, the agent port stays silent across several refreshes:
	// uninstalled out of band, NAND wiped, agent crashed). Kind is "stock" on
	// such a record, so the app offers the install like for any stock box;
	// the flag lets the UI say "STR not running" instead of "Ready for STR"
	// and drop the "unplug the speaker" advice, which does not help a box
	// that answers. Persisted in the cache until the agent answers again.
	STRNotRunning bool `json:"strNotRunning,omitempty"`
	// STRSilent is the transient per-cycle sibling of STRNotRunning: on THIS
	// refresh the stock :8090 answered but the STR agent did not, while the
	// record still counts as STR. It lets the Settings pane tell "the
	// speaker answers its Bose firmware, STR is not running" from "nothing
	// answers, unplug it" right away, without waiting for the degrade above.
	// Never persisted to the discovery cache.
	STRSilent bool `json:"strSilent,omitempty"`
	// OTAPending marks a box inside the post-OTA discovery pin window: STR is
	// mid-update on it, so the frontend must not flag "update available" while
	// the agent restarts and cannot answer its real version yet. This replaces
	// the old behaviour of stamping the APP's version onto the record, which
	// falsified the displayed version of a box the update never reached (#775:
	// unreachable speakers showed the target v0.9.65, boxNeedsUpdate went
	// false fleet-wide, and a retry toasted "already up to date"). Transient,
	// never persisted to the discovery cache.
	OTAPending bool `json:"otaPending,omitempty"`
	// BoxHealth is the agent's wedged-control verdict ("ok" or "wedged"): a
	// wedged box accepts transport pushes but never plays, and only a
	// power-cycle clears it. The UI turns "wedged" into a pull-the-plug hint.
	BoxHealth string `json:"boxHealth,omitempty"`
	// Internet is the agent's passive verdict on whether the speaker reaches
	// the internet: "ok", "unreachable" or "unknown" (empty from agents that
	// predate it). InternetReason is the short cause behind "unreachable"
	// (dns, timeout, route, connect). The UI turns "unreachable" into "this
	// speaker cannot reach the internet, check the router": the app reaching
	// the speaker over the LAN proves the Wi-Fi works, so rebooting it, which
	// is what an owner did for two hours on 2026-10-14, cannot help.
	Internet       string `json:"internet,omitempty"`
	InternetReason string `json:"internetReason,omitempty"`
	// ArcadeAt is when the speaker saved its last round of a display game
	// (unix seconds as a string, empty when none). The app fetches the
	// scores only when it changes, to tell the player about a new highscore.
	ArcadeAt string `json:"arcadeAt,omitempty"`
	// ConflictingMod names a rival cloud-free SoundTouch tool (e.g. "AfterTouch")
	// whose leftover files STR found on the box. Two such tools fight over the
	// cloud redirect, OLED, Wi-Fi and presets; the UI warns the user to remove it
	// (#270). Empty on an STR-only box.
	ConflictingMod string `json:"conflictingMod,omitempty"`
	// ForeignCloudURL is the Bose cloud address the speaker is set to ask when
	// that address is not the stock one, e.g. a leftover from an earlier
	// OpenCloudTouch install. STR redirects only the stock hostnames to its own
	// listeners, so such a speaker never reaches STR at all: its presets answer
	// "not logged in" and nothing plays, while every other field here looks
	// healthy (#986). Empty on a healthy box. The Settings action that removes a
	// rival mod's leftovers also resets this.
	ForeignCloudURL string `json:"foreignCloudURL,omitempty"`
	// GroupKeyError is the reason the last thumbs-key press was refused, when
	// it was recent. Before this the refusal was invisible: the owner pressed
	// the key, nothing happened, and only a diagnostic bundle explained why.
	GroupKeyError string `json:"groupKeyError,omitempty"`
	// GroupKeyErrorKind classifies GroupKeyError (master-unresponsive,
	// member-unresolved, form-refused, dissolve-failed, other), so the app
	// explains the refusal in the user's language and keeps the raw reason
	// for the details. Empty from an agent that predates it.
	GroupKeyErrorKind string `json:"groupKeyErrorKind,omitempty"`
	// HoldRefusedSource is what the speaker was playing when a long press on a
	// key could not be kept, empty when there was none. Typically SPOTIFY: STR
	// has no preset form for it, so the firmware keeps the old station and the
	// key looks as if it snapped back by itself.
	HoldRefusedSource string `json:"holdRefusedSource,omitempty"`
	HoldRefusedSlot   string `json:"holdRefusedSlot,omitempty"`
	// StaleFolderKeySlot is the folder key whose saved file addresses the
	// media server no longer has (the server answers, every path is 404,
	// typically after a USB stick swap on a router's media server), empty
	// when none. Only saving the folder on the key again cures it.
	StaleFolderKeySlot string `json:"staleFolderKeySlot,omitempty"`
	// SpeakerNameUnsafe is true while the speaker's stored name carries a
	// character its firmware's UPnP renderer cannot survive ("&", "<", ">" or
	// invalid UTF-8): Spotify and the music library cannot play on it until it
	// is renamed and restarted (#1582).
	SpeakerNameUnsafe bool `json:"speakerNameUnsafe,omitempty"`
	// Storm1036 is true while the box rejects essentially every preset recall
	// (Bose error 1036, "not logged in"). Nothing the user presses will play
	// until the state clears, and the remedy people reach for on their own,
	// pulling the plug, resets the box clock and poisons the next boot, while a
	// SOFT reboot clears it (#419 Finding 4). The UI says so and offers the
	// reboot. Storm1036SinceSec is how long it has been going.
	Storm1036         bool `json:"storm1036,omitempty"`
	Storm1036SinceSec int  `json:"storm1036SinceSec,omitempty"`
	// RecallRefusal is the storm's quiet sibling: the agent latched
	// consecutive recalls whose source self-dropped to STANDBY without a
	// single 1036. Joined into the same banner (a soft restart clears both).
	RecallRefusal         bool `json:"recallRefusal,omitempty"`
	RecallRefusalSinceSec int  `json:"recallRefusalSinceSec,omitempty"`
	// WLANCredsMissing is true when STR has no saved Wi-Fi on the box: it only
	// stays online while the stick or an ethernet cable is inserted and strands
	// the user on the next cold boot. The UI warns the user to run STR's own
	// Wi-Fi setup (#270).
	WLANCredsMissing bool `json:"wlanCredsMissing,omitempty"`
	// SerialNumber is the human-readable Bose PackagedProduct serial
	// (the sticker on the bottom of the speaker, e.g.
	// "000000P00000000AA"). Pulled from /info on 8090; empty if the
	// box was not reachable on that port during discovery. Used by
	// the Setup-target picker so users with two or three identical
	// speakers on the same LAN can tell them apart by something other
	// than the Bose-default friendly name "SoundTouch 20".
	SerialNumber string `json:"serialNumber"`
	// Kind is "str" for speakers running an STR agent, "stock" for
	// vanilla Bose SoundTouch speakers that the desktop app can
	// offer to flash. Frontend renders the two kinds differently.
	Kind string `json:"kind"`
	// PortVerified is true when Port was confirmed reachable by an
	// actual HTTP probe (probeSTR), false when it is only the
	// mDNS-announced port. On BCO boxes (Portable, ST20-spotty) the
	// agent announces :8888 via mDNS but the chipset firewall drops
	// direct :8888; only the REDIRECTed :17008 is reachable. The merge
	// in DiscoverBoxes prefers a verified port over an announced one so
	// agent calls (radio, presets) do not hit the firewalled :8888.
	PortVerified bool `json:"portVerified"`
}
