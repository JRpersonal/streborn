// Functional options and setter hooks for constructing the webui Server.

package webui

import (
	"context"
	"strings"

	"github.com/JRpersonal/streborn/internal/alarm"
	"github.com/JRpersonal/streborn/internal/autopair"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/groupkeys"
	"github.com/JRpersonal/streborn/internal/keyactions"
	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
	"github.com/JRpersonal/streborn/internal/region"
	"github.com/JRpersonal/streborn/internal/streamproxy"
	"github.com/JRpersonal/streborn/internal/upnp"
	"github.com/JRpersonal/streborn/internal/webhooks"
	"github.com/JRpersonal/streborn/internal/zones"
)

// SetWifiSignalFn wires a provider for the latest Wi-Fi signal class
// (from the boxws gabbo stream). cmd/agent calls this after creating the
// WebSocket client.
func (s *Server) SetWifiSignalFn(fn func() string) { s.wifiSignalFn = fn }

// SetBoxNameFn wires a provider for the box display name and model the agent
// currently knows (typically the mDNS announcer snapshot). cmd/agent calls
// this after the announcer is up.
func (s *Server) SetBoxNameFn(fn func() (name, model string)) { s.boxNameFn = fn }

// SetOnlineFn wires the on-demand internet reachability check the display
// messages use to tell a dead station from a dead connection.
func (s *Server) SetOnlineFn(fn func() bool) { s.onlineFn = fn }

// SetNetworkChangedFn wires the post-switch state refresh (#697): called after
// a CONFIRMED live Wi-Fi switch (the wpaConfirmed arm and the boot guard's
// finishCorrection), once the DHCP renew has been requested. cmd/agent owns
// the refresh because the stale state lives there: the mDNS announcers frozen
// on the boot address, the peer roster's self-entry at the old IP, and the
// per-IP REDIRECT rules. The callback is invoked on its own goroutine; it is
// expected to wait for the new lease itself.
func (s *Server) SetNetworkChangedFn(fn func(reason string)) { s.networkChangedFn = fn }

// SetNativePresetLocatorFn wires the decision of whether a preset slot can be
// stored as a native LOCAL_INTERNET_RADIO station instead of a UPnP stream.
// The function returns the orion station location, or "" when the box has not
// registered the radio source and the slot must keep the UPnP form. cmd/agent
// owns that probe because it also owns the box host and the association state.
func (s *Server) SetNativePresetLocatorFn(fn func(name, streamURL, art string) string) {
	s.nativePresetLocator = fn
}

// nativePresetLocation asks the wired locator for a slot's native location,
// returning "" when no locator is wired (the desktop-side and test servers).
func (s *Server) nativePresetLocation(name, streamURL, art string) string {
	if s.nativePresetLocator == nil {
		return ""
	}
	return s.nativePresetLocator(name, streamURL, art)
}

// writeBoxPreset stores one slot on the box in the best form this speaker
// accepts: a native radio station where the box has that source registered, the
// UPnP stream otherwise.
//
// Saving a preset went straight to the UPnP form, so a station the user had
// just saved reacted on the slow path (an error the agent recovers from, about
// eight seconds) until the next reconcile sweep upgraded it. Pressing the key
// right after saving is exactly what a user does, so it is worth getting right
// at the moment of the save rather than a cycle later.
//
// isSpotify slots are never native: the box activates a native item entirely by
// itself, and a Spotify preset needs STR to tell the local engine which
// playlist to load.
func (s *Server) writeBoxPreset(ctx context.Context, slot int, name, streamURL, art string, isSpotify bool) error {
	if !isSpotify {
		if loc := s.nativePresetLocation(name, streamURL, art); loc != "" {
			if err := boxcli.AddPresetNative(ctx, s.boxHost, slot, name, loc); err == nil {
				return nil
			}
			// Fall through to the UPnP form: a key that costs a recovery round
			// beats one that was never written.
		}
	}
	return boxcli.AddPreset(ctx, s.boxHost, slot, name, streamURL)
}

// Option is a functional option for New.
type Option func(*Server)

// WithPresets wires the store for preset CRUD.
func WithPresets(p *presets.Store) Option {
	return func(s *Server) { s.presets = p }
}

// WithZones wires the multiroom zone persistence store so formed zones survive
// a reboot/standby and auto-reform (#70).
func WithZones(z *zones.Store) Option {
	return func(s *Server) { s.zones = z }
}

// WithMediaServers wires the store of DLNA/UPnP media servers the user enabled
// as native music sources, so they are restored after a reboot (the speaker
// itself drops them a minute or so into every boot).
func WithMediaServers(m *mediaservers.Store) Option {
	return func(s *Server) { s.mediaServers = m }
}

// StoredMusicSource is one media server the marge account advertises. Account
// is the server's UPnP id with "/0" appended.
type StoredMusicSource struct {
	Account string
	Name    string
}

// WithStoredMusicPublisher wires the marge bridge that publishes the enabled
// media servers into the account document the box polls.
func WithStoredMusicPublisher(f func([]StoredMusicSource)) Option {
	return func(s *Server) { s.publishStoredMusic = f }
}

// WithBoxHost sets the Bose box IP/hostname for UPnP calls.
func WithBoxHost(host string) Option {
	return func(s *Server) {
		s.boxHost = host
		s.renderer = upnp.NewBoseRenderer(host)
		// A 404 from this speaker's renderer may be a name it cannot survive
		// (#1582, speakername.go); one rate-limited probe tells.
		s.renderer.OnNotFound = s.NoteRendererNotFound
	}
}

// WithLastPlayPath wires the NAND path the last-played stream is persisted to,
// so the power-on resume survives an agent restart over a long standby (#119).
func WithLastPlayPath(path string) Option {
	return func(s *Server) { s.lastPlayPath = path }
}

// WithBoxSnapshotPath wires the NAND path of the pre-takeover box snapshot
// (internal/boxsnapshot) so GET /api/box/snapshot can serve it.
func WithBoxSnapshotPath(path string) Option {
	return func(s *Server) { s.snapshotPath = path }
}

// WithReflectSourcesPath wires the reflect-sources file so the experimental
// restore endpoint can re-advertise account-linked cloud sources (Deezer).
func WithReflectSourcesPath(path string) Option {
	return func(s *Server) { s.reflectPath = path }
}

// WithAutoPair gives the server access to the AutoPair manager so that
// play calls can re-pair the box again after waking it from standby.
func WithAutoPair(m *autopair.Manager) Option {
	return func(s *Server) { s.autoPair = m }
}

// WithRegion passes the country code chosen by the setup wizard.
// Exposed via /api/region so the desktop app can derive its defaults
// for radio search and language from it.
func WithRegion(cc string) Option {
	return func(s *Server) { s.region = strings.ToUpper(cc) }
}

// WithRegionFile sets the persistent path for changes from
// /api/region (PUT). Without this path changes are only in memory.
func WithRegionFile(path string) Option {
	return func(s *Server) { s.regionFile = path }
}

// WithResumeOnPowerOnFile sets the persistent path for the per-box "resume the
// last station on power-on" opt-out (default on). Without it the default NAND
// path (defaultResumeOnPowerOnPath) is used.
func WithResumeOnPowerOnFile(path string) Option {
	return func(s *Server) { s.resumeOnPowerOnPath = path }
}

// WithDisplayTrackFile sets the persistent path for the per-box "show the live
// radio track on the speaker display" opt-in. Empty uses the default path
// (defaultDisplayTrackPath).
func WithDisplayTrackFile(path string) Option {
	return func(s *Server) { s.displayTrackPath = path }
}

// WithStreamProxy wires in the stream proxy. When set, the /stream/
// endpoint is registered. Bose ContentItems are then linked with
// http://127.0.0.1:8888/stream/<slot> instead of the real CDN URL —
// streams survive token expiry.
func WithStreamProxy(p *streamproxy.Server) Option {
	return func(s *Server) { s.streamProxy = p }
}

// WithWebhooks wires the user-configured webhook store (thumbs trigger).
func WithWebhooks(w *webhooks.Store) Option {
	return func(s *Server) { s.webhooks = w }
}

// WithGroupKeys wires the saved groups bound to the remote's thumbs keys
// (#863), served at /api/groupkeys.
func WithGroupKeys(g *groupkeys.Store) Option {
	return func(s *Server) { s.groupKeys = g }
}

// WithKeyActions wires the built-in actions bound to the remote's thumbs
// keys (the sleep timer), served at /api/keyactions.
func WithKeyActions(k *keyactions.Store) Option {
	return func(s *Server) { s.keyActions = k }
}

// WithAlarms wires the alarm clock: the document an editor saves, served at
// /api/alarms, and the separate file recording what has already been fired.
// Both or neither - the scheduler does not run without the state store.
func WithAlarms(a *alarm.Store, st *alarm.StateStore) Option {
	return func(s *Server) { s.alarms, s.alarmState = a, st }
}

// tellSpotifyUserStopped forwards a user stop to the Spotify manager without
// holding the caller up: the engine pause is a local HTTP call with its own
// short deadline.
func (s *Server) tellSpotifyUserStopped(reason string) {
	if s.spotifyUserStopped == nil {
		return
	}
	go s.spotifyUserStopped(context.Background(), reason)
}

// PeerLink is one other STR speaker on the LAN, as shown in the on-box page's
// "Other speakers" section so a phone can hop between speakers.
type PeerLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// DeviceID is how the firmware names this speaker in a zone. The phone
	// needs it to ask for a group at all: /setZone identifies every member by
	// device, and the phone was the only client that could not name one.
	DeviceID string `json:"deviceID,omitempty"`
	// IP is the address a zone member is reached at, split out of URL so the
	// phone does not have to parse it back out of a string.
	IP string `json:"ip,omitempty"`
	// Reachable is false for a peer that was seen recently over mDNS but did not
	// answer a web-port probe on the last sweep. Such peers are still listed (so a
	// speaker briefly missed by a lossy mDNS round does not vanish and reappear,
	// #404/#381/#385) but the on-box page renders them dimmed / non-clickable.
	Reachable bool `json:"reachable"`
}

// WithPeers registers the resolver that lists the other STR speakers on the
// network (name + reachable web URL). nil disables the "Other speakers" section.
func WithPeers(fn func(ctx context.Context) []PeerLink) Option {
	return func(s *Server) { s.peersFn = fn }
}

// PeerSeed is one speaker pushed into the agent's peer list from outside (the
// desktop app distributes its known-speakers set to every agent so the on-box
// picker is complete even where local mDNS is lossy).
type PeerSeed struct {
	Name string `json:"name"`
	Host string `json:"host"`
	Port int    `json:"port"`
	// DeviceID identifies the seeded speaker independently of its address, so
	// the agent can drop a seed that is really itself at a previous address
	// (#697). Optional: apps predating it send none.
	DeviceID string `json:"deviceID,omitempty"`
}

// WithCachedPeers registers a reader of the peer list as the agent already
// holds it, with no mDNS browse and no probes. Paths that run under the box
// command lock (the stereo follower refusal) use it instead of WithPeers'
// resolver, which may browse for seconds. nil leaves those paths without names.
func WithCachedPeers(fn func() []PeerLink) Option {
	return func(s *Server) { s.cachedPeersFn = fn }
}

// WithPeerSeed registers the sink for externally pushed peers (POST
// /api/peers/seed). nil returns 404 on the endpoint.
func WithPeerSeed(fn func([]PeerSeed)) Option {
	return func(s *Server) { s.peerSeedFn = fn }
}

// WithPeerForget registers the remover for one pushed/stale peer entry (POST
// /api/peers/forget). Returns whether the host was known. nil: 404.
func WithPeerForget(fn func(host string) bool) Option {
	return func(s *Server) { s.peerForgetFn = fn }
}

// WithPairPartnerGone registers the lookup for the other half of a restored
// stereo pair that did not answer at agent start. The firmware refuses every
// source activation while a pair is incomplete, so a speaker in that state
// cannot play at all, and without this the app can only say "the speaker is not
// responding" (measured 2026-09-29, partner absent eighteen days).
func WithPairPartnerGone(fn func() (ip, deviceID string)) Option {
	return func(s *Server) { s.pairPartnerGone = fn }
}

// WithPairPartnerRecheck registers the re-probe for a partner recorded as
// gone: it asks the partner again (rate-limited, no network call when nothing
// is recorded) and returns the verdict afterwards. The play path and the zone
// endpoint use it before refusing or warning, so a partner that was only
// missed at agent start (network not up yet after a reboot, #1208) stops
// blocking the pair the moment it answers.
func WithPairPartnerRecheck(fn func() (ip, deviceID string)) Option {
	return func(s *Server) { s.pairPartnerRecheck = fn }
}

// WithStereoFollower registers the stereo-pair role check the play paths ask
// before they wake or push: whether this speaker is the FOLLOWING half of a
// pair, and which speaker leads it. The agent answers it from the pair record
// in marge and the speaker's own deviceID (the webui cannot: it reaches its
// speaker over loopback and does not know which half it is). nil means "not a
// follower", which keeps every play path exactly as it was.
func WithStereoFollower(fn func() StereoFollower) Option {
	return func(s *Server) { s.stereoFollowerFn = fn }
}

// WithNativeServices wires the firmware's own US services (Pandora,
// iHeartRadio; #243) to /api/us-services, the account endpoints and the
// diagnostic bundle. Nil leaves the endpoints answering 501.
func WithNativeServices(b NativeServicesBackend) Option {
	return func(s *Server) { s.native = b }
}

// WithRegionResolver wires the region resolver: PUT /api/region feeds it, and
// /api/agent/version reports the effective region from it.
func WithRegionResolver(r *region.Resolver) Option {
	return func(s *Server) { s.regionRes = r }
}

// WithMargeGroups bridges the marge stereo-pair record (get/set/clear) so the
// pairing and dissolve flows keep BOTH members' marges on one canonical pair
// document, and /api/marge/group lets the desktop app relay it to the partner.
// cmd/agent wires the same fields through WithMarge (engines.go); this form
// takes the five functions one by one.
func WithMargeGroups(get func() (string, bool, bool), set func(string) error, clear func(string), rename func(string) error, name func() string) Option {
	return func(s *Server) {
		s.margeGroupGet = get
		s.margeGroupSet = set
		s.margeGroupClear = clear
		s.margeGroupRename = rename
		s.margeGroupName = name
	}
}

// WithRecent wires the recently-played ring (#135) so the play handlers record
// the user's listening history and GET /api/recent serves it.
func WithRecent(r *recent.Store) Option {
	return func(s *Server) { s.recent = r }
}
