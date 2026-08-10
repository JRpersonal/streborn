// Functional options and setter hooks for constructing the webui Server.

package webui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/autopair"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
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

// WithSpotifySwitchedAway wires the Spotify manager's source-switch hook, called
// when the box is pointed at a non-Spotify source so the #14 auto-attach stands
// down (otherwise a radio recall jumps back to Spotify a second later).
func WithSpotifySwitchedAway(f func(ctx context.Context)) Option {
	return func(s *Server) { s.spotifySwitchedAway = f }
}

// WithSpotifyStream registers the handler that serves go-librespot's
// live Ogg to the box at /spotify/stream (the Spotify-preset audio
// plane).
func WithSpotifyStream(h http.HandlerFunc) Option {
	return func(s *Server) { s.spotifyStream = h }
}

// WithSpotifyInfo registers the handler that reports live Spotify state
// (ready, measured bitrate, device name) at /spotify/info.
func WithSpotifyInfo(h http.HandlerFunc) Option {
	return func(s *Server) { s.spotifyInfo = h }
}

// WithSpotifyReload injects the Spotify manager's live engine reload, called
// after an OTA sidecar write (handleAgentSidecar) so a freshly delivered
// go-librespot is hot-swapped in place without a box reboot. The function
// returns whether a running engine was restarted. Wiring it also makes the
// version endpoint advertise engineHotSwap=true so the desktop app skips its
// post-delivery activation reboot (#240).
func WithSpotifyReload(f func() bool) Option {
	return func(s *Server) { s.spotifyReload = f }
}

// WithSpotifyStop injects the Spotify manager's engine-stop, used by the
// space-pressed OTA write to genuinely free the regenerable go-librespot engine
// (stop the process so its NAND blocks release, then drop the binary) when a tight
// box cannot otherwise hold the agent update (#119). nil leaves the previous
// best-effort os.Remove (a no-op while the engine runs).
func WithSpotifyStop(f func() bool) Option {
	return func(s *Server) { s.spotifyStop = f; engineStopHook = f }
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

// WithSpotifyControl registers the function that starts playback of a
// Spotify URI on a given account in go-librespot (the Spotify-preset
// control plane). An empty account plays with the current login; shuffle
// selects a fresh random start over the default resume-where-left-off.
func WithSpotifyControl(play func(ctx context.Context, uri, account string, shuffle bool) error) Option {
	return func(s *Server) { s.spotifyPlay = play }
}

// WithSpotifyUser registers the resolver for go-librespot's current account,
// used to stamp the account onto a newly saved Spotify preset.
func WithSpotifyUser(user func(ctx context.Context) string) Option {
	return func(s *Server) { s.spotifyUser = user }
}

// WithSpotifyContext registers the resolver for the Spotify context URI
// go-librespot is currently playing, used by the preset-save path to stamp the
// live account when saving the content that is playing right now.
func WithSpotifyContext(ctxURI func() string) Option {
	return func(s *Server) { s.spotifyContext = ctxURI }
}

// WithSpotifyMeta registers the resolver for a Spotify context's stable cover
// image and human title, stamped onto a newly saved Spotify preset.
func WithSpotifyMeta(meta func(ctx context.Context, uri string) (cover, title string)) Option {
	return func(s *Server) { s.spotifyMeta = meta }
}

// WithSpotifyStreaming registers the predicate that reports whether the box is
// currently pulling the Ogg stream, used by verifyRecall to avoid a disruptive
// re-issue while Spotify is already playing.
func WithSpotifyStreaming(streaming func() bool) Option {
	return func(s *Server) { s.spotifyStreaming = streaming }
}

// WithSpotifyReady registers the predicate that reports whether go-librespot has
// finished authenticating, so a soft Spotify recall can wait out a cold start.
func WithSpotifyReady(ready func() bool) Option {
	return func(s *Server) { s.spotifyReady = ready }
}

// WithSpotifySkip registers the hook that skips go-librespot to the next
// (forward=true) or previous track, wired to the phone remote's Previous/Next
// controls. Mirrors the hardware remote's Spotify skip.
func WithSpotifySkip(skip func(ctx context.Context, forward bool) error) Option {
	return func(s *Server) { s.spotifySkip = skip }
}

// WithSpotifyPremiumRequired registers the predicate that reports whether the
// logged-in Spotify account is free/open and so cannot do the autonomous recall
// playback a preset needs (#45). The recall handler uses it to answer with a
// clear "needs Premium" message instead of failing silently.
func WithSpotifyPremiumRequired(f func() bool) Option {
	return func(s *Server) { s.spotifyPremiumRequired = f }
}

// WithSpotifyCanRecall registers the predicate that reports whether a Spotify
// recall can proceed (a live go-librespot session OR a persisted credential), so
// a recall on a genuinely-never-logged-in speaker fails with a clear, actionable
// message while one with a live session still plays (#45; Patrick, 2026-06-24).
func WithSpotifyCanRecall(f func(ctx context.Context) bool) Option {
	return func(s *Server) { s.spotifyCanRecall = f }
}

// WithMargeGroups bridges the marge stereo-pair record (get/set/clear) so the
// pairing and dissolve flows keep BOTH members' marges on one canonical pair
// document, and /api/marge/group lets the desktop app relay it to the partner.
func WithMargeGroups(get func() (string, bool, bool), set func(string) error, clear func(string)) Option {
	return func(s *Server) {
		s.margeGroupGet = get
		s.margeGroupSet = set
		s.margeGroupClear = clear
	}
}

// WithMargeForward registers the marge stub's developer relay switch, enabling
// POST /api/debug/marge-lab. Unset leaves the endpoint disabled.
func WithMargeForward(f func(target string) error) Option {
	return func(s *Server) { s.margeForward = f }
}

// WithSpotifyExportCred registers the function that returns this box's active
// go-librespot credential so it can be copied to other speakers (#45 sync).
func WithSpotifyExportCred(f func() ([]byte, error)) Option {
	return func(s *Server) { s.spotifyExportCred = f }
}

// WithSpotifyImportCred registers the function that installs a credential copied
// from another speaker and restarts go-librespot to log in with it (#45 sync).
func WithSpotifyImportCred(f func(ctx context.Context, data []byte) error) Option {
	return func(s *Server) { s.spotifyImportCred = f }
}

// WithSpotifySetRecalling registers the hook that marks an in-flight recall so
// ServeOgg drives the new track from its start.
func WithSpotifySetRecalling(setRecalling func()) Option {
	return func(s *Server) { s.spotifySetRecalling = setRecalling }
}

// WithSpotifySuppressActivate registers the hook that holds go-librespot's
// auto-repoint off for a window, so the hardware-skip recovery's clean slot
// recall is not raced by the #14 auto-attach.
func WithSpotifySuppressActivate(suppress func(time.Duration)) Option {
	return func(s *Server) { s.spotifySuppressActivate = suppress }
}

// WithRecent wires the recently-played ring (#135) so the play handlers record
// the user's listening history and GET /api/recent serves it.
func WithRecent(r *recent.Store) Option {
	return func(s *Server) { s.recent = r }
}
