// Interfaces for the two on-box services the server drives through injected
// functions: the Spotify engine (internal/spotify) and the marge stereo-pair
// record (internal/marge). webui does not import either package; cmd/agent
// adapts the concrete managers to these interfaces and wires them with
// WithSpotify and WithMarge.
//
// The server keeps its per-capability function fields (spotifyPlay,
// margeGroupSet, ...) and every call site keeps its nil check: a nil field
// still means "feature absent". The two options here only populate those
// fields from one value, so the tests that set a single field directly keep
// working unchanged.

package webui

import (
	"context"
	"net/http"
	"time"
)

// SpotifyEngine is the control plane a Spotify-capable speaker needs from the
// go-librespot manager. Each method maps 1:1 onto one Server function field
// (named in the comment), with the same signature the field has.
//
// A method set this large is deliberate: the fields have grown one at a time
// with the recall, skip and preset-save paths, and folding them into fewer,
// broader methods would change what each call site can observe.
type SpotifyEngine interface {
	// SwitchedAway (spotifySwitchedAway): the box was pointed at a non-Spotify
	// source, so the #14 auto-attach stands down.
	SwitchedAway(ctx context.Context)
	// UserStopped (spotifyUserStopped): the user stopped or paused the speaker,
	// so the engine is paused and does not bring it back by itself.
	UserStopped(ctx context.Context, reason string)
	// ServeStream (spotifyStream): the live Ogg handler the box pulls at
	// /spotify/stream.ogg and its per-slot aliases.
	ServeStream(w http.ResponseWriter, r *http.Request)
	// Play (spotifyPlay): start a Spotify URI on the given account (empty: the
	// current login); shuffle and repeat select the playback mode.
	Play(ctx context.Context, uri, account string, shuffle, repeat bool) error
	// CurrentUser (spotifyUser): the account go-librespot is logged in as.
	CurrentUser(ctx context.Context) string
	// PlayingContext (spotifyContext): the context URI playing right now.
	PlayingContext() string
	// Shuffling (spotifyShuffle): the engine's live shuffle state.
	Shuffling(ctx context.Context) bool
	// Repeating (spotifyRepeat): the engine's live repeat state.
	Repeating(ctx context.Context) bool
	// Meta (spotifyMeta): a context's stable cover image and human title.
	Meta(ctx context.Context, uri string) (cover, title string)
	// Streaming (spotifyStreaming): the box is pulling audio from the Ogg
	// stream right now (a stalled attachment does not count).
	Streaming() bool
	// Skip (spotifySkip): next (forward) or previous track.
	Skip(ctx context.Context, forward bool) error
	// SkipBoundary (spotifySkipBoundary): when a user skip's track boundary
	// last reached the forwarded stream.
	SkipBoundary() time.Time
	// ArmRecallCut (spotifyArmRecallCut): arm the boundary cut for a recall.
	ArmRecallCut()
	// BoundaryStaleKB (spotifyBoundaryStaleKB): stale KB fed to the box's
	// current attachment before the last cut boundary.
	BoundaryStaleKB() int64
	// Ready (spotifyReady): the engine has finished authenticating.
	Ready() bool
	// CanRecall (spotifyCanRecall): a live session or a persisted credential
	// exists, so a recall can proceed.
	CanRecall(ctx context.Context) bool
	// PremiumRequired (spotifyPremiumRequired): the account is free/open and
	// cannot do autonomous recall playback.
	PremiumRequired() bool
	// SetRecalling (spotifySetRecalling): mark an in-flight recall.
	SetRecalling()
	// SuppressActivate (spotifySuppressActivate): hold the auto-repoint off
	// for the window.
	SuppressActivate(d time.Duration)
	// ExpectReattach (spotifyExpectReattach): the next re-attach is STR's own
	// re-push, not a storm.
	ExpectReattach(window time.Duration)
}

// Optional Spotify capabilities. WithSpotify fills the matching fields only
// when the engine value also implements the sub-interface, and leaves them
// nil otherwise. The rule is: a field is nil exactly when the engine does not
// implement the sub-interface that carries it. Those are the fields whose nil
// state is visible from outside (a route not registered in Run, a feature not
// advertised by the version endpoint), so a reduced engine, such as a test
// fake, gets the same server it would have had with the fields left unset.
// cmd/agent's adapter implements all three, so on the speaker every field is
// set, as it was before these interfaces existed.

// SpotifyCredentialSync moves the go-librespot login between speakers (#45
// sync). Without it the /spotify/credential routes are not registered.
type SpotifyCredentialSync interface {
	// ExportCred (spotifyExportCred): this box's active credential.
	ExportCred() ([]byte, error)
	// ImportCred (spotifyImportCred): install a credential copied from another
	// speaker and log in with it.
	ImportCred(ctx context.Context, data []byte) error
}

// SpotifyStatusPages serves the engine's own HTTP pages. Without it
// /spotify/info and /spotify/quality are not registered.
type SpotifyStatusPages interface {
	// ServeInfo (spotifyInfo): GET /spotify/info, the live Spotify state.
	ServeInfo(w http.ResponseWriter, r *http.Request)
	// ServeQuality (spotifyQuality): GET/POST /spotify/quality, the preferred
	// streaming bitrate (#728).
	ServeQuality(w http.ResponseWriter, r *http.Request)
}

// SpotifyEngineLifecycle restarts or stops the supervised engine process for
// an OTA sidecar write. Without it the version endpoint does not advertise
// engineHotSwap and the space-pressed OTA write cannot free the engine's NAND
// blocks (it falls back to a best-effort os.Remove).
type SpotifyEngineLifecycle interface {
	// Reload (spotifyReload): re-exec the engine from its just-overwritten
	// binary; reports whether a running engine was restarted (#240).
	Reload() bool
	// Stop (spotifyStop): stop the engine and wait for it to exit (#119).
	Stop() bool
}

// WithSpotify wires the whole Spotify control plane from one engine value.
// The base interface fills the fields every Spotify-capable speaker has; the
// optional sub-interfaces above fill theirs only when implemented (see the
// rule there). A nil engine leaves every field untouched.
func WithSpotify(e SpotifyEngine) Option {
	return func(s *Server) {
		if e == nil {
			return
		}
		s.spotifySwitchedAway = e.SwitchedAway
		s.spotifyUserStopped = e.UserStopped
		s.spotifyStream = e.ServeStream
		s.spotifyPlay = e.Play
		s.spotifyUser = e.CurrentUser
		s.spotifyContext = e.PlayingContext
		s.spotifyShuffle = e.Shuffling
		s.spotifyRepeat = e.Repeating
		s.spotifyMeta = e.Meta
		s.spotifyStreaming = e.Streaming
		s.spotifySkip = e.Skip
		s.spotifySkipBoundary = e.SkipBoundary
		s.spotifyArmRecallCut = e.ArmRecallCut
		s.spotifyBoundaryStaleKB = e.BoundaryStaleKB
		s.spotifyReady = e.Ready
		s.spotifyCanRecall = e.CanRecall
		s.spotifyPremiumRequired = e.PremiumRequired
		s.spotifySetRecalling = e.SetRecalling
		s.spotifySuppressActivate = e.SuppressActivate
		s.spotifyExpectReattach = e.ExpectReattach
		if c, ok := e.(SpotifyCredentialSync); ok {
			s.spotifyExportCred = c.ExportCred
			s.spotifyImportCred = c.ImportCred
		}
		if p, ok := e.(SpotifyStatusPages); ok {
			s.spotifyInfo = p.ServeInfo
			s.spotifyQuality = p.ServeQuality
		}
		if l, ok := e.(SpotifyEngineLifecycle); ok {
			s.spotifyReload = l.Reload
			// The OTA write path reads the stop hook from a package variable
			// (agent_admin.go), exactly as the former WithSpotifyStop set it.
			s.spotifyStop = l.Stop
			engineStopHook = l.Stop
		}
	}
}

// MargeGroups is the marge stereo-pair record the pairing and dissolve flows
// keep in step on both members. Each method maps 1:1 onto one Server field
// with the same signature; the names are those of marge.Server, which
// satisfies the interface directly.
type MargeGroups interface {
	// GroupSnapshot (margeGroupGet): the stored pair document, whether it is
	// the canonical one, and whether a pair is stored at all.
	GroupSnapshot() (xmlDoc string, canonical bool, ok bool)
	// SetCanonicalGroup (margeGroupSet): install the canonical pair document.
	SetCanonicalGroup(xmlDoc string) error
	// ClearGroup (margeGroupClear): drop the stored pair, with the reason
	// logged.
	ClearGroup(reason string)
	// RenameGroup (margeGroupRename): rename the stored pair.
	RenameGroup(name string) error
	// GroupName (margeGroupName): the stored pair's display name (#775).
	GroupName() string
}

// MargeForwarder is the optional developer relay switch (margelab.go).
// Without it POST /api/debug/marge-lab stays disabled; the same nil rule as
// for the Spotify sub-interfaces applies.
type MargeForwarder interface {
	// SetForward (margeForward): relay the box's cloud conversation to the
	// target, or clear the relay.
	SetForward(target string) error
}

// WithMarge wires the marge pair record from one value, and the developer
// relay when the value also implements MargeForwarder. It is the
// interface-shaped twin of WithMargeGroups, which stays for callers that
// pass the five functions one by one. A nil value leaves every field
// untouched.
func WithMarge(m MargeGroups) Option {
	return func(s *Server) {
		if m == nil {
			return
		}
		s.margeGroupGet = m.GroupSnapshot
		s.margeGroupSet = m.SetCanonicalGroup
		s.margeGroupClear = m.ClearGroup
		s.margeGroupRename = m.RenameGroup
		s.margeGroupName = m.GroupName
		if f, ok := m.(MargeForwarder); ok {
			s.margeForward = f.SetForward
		}
	}
}
