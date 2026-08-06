package webui

// Pause and Stop have to reach whatever is actually playing.
//
// STR's transport controls drive the UPnP renderer, and for most of this
// project's life that was right: STR pushed the audio, so the UPnP transport
// WAS the playback. Native radio presets changed that. A native station is
// fetched by the speaker itself and runs on the box's own player, with the
// active source reading LOCAL_INTERNET_RADIO. A UPnP Pause then acts on an idle
// transport: it succeeds, reports success, and the music keeps playing.
//
// That is what a reporter described on 2026-08-06 without knowing the cause:
// "starting stations works, volume works, AUX and Bluetooth work, but Pause and
// Stop have no effect." His bundle shows source=LOCAL_INTERNET_RADIO,
// playStatus=PLAY_STATE and every port reachable, which rules out the speaker
// being unwell and leaves only the wrong addressee.
//
// So the control follows the source: UPNP keeps the renderer path exactly as it
// was, and anything the box plays itself gets a remote key instead, which is
// what the physical remote sends.

import (
	"context"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// boxOwnedSource reports whether the speaker is playing this source ITSELF, so
// the UPnP renderer is not the thing to talk to.
//
// An allowlist of STR's own sources rather than a list of everything else: the
// source names differ per model and a name we forgot must fall on the safe
// side. Getting it wrong in this direction sends a harmless extra remote key;
// getting it wrong the other way is the dead Pause button this fixes.
func boxOwnedSource(src string) bool {
	switch src {
	case "", "UPNP", "STANDBY", "INVALID_SOURCE":
		return false
	default:
		return true
	}
}

// transportKeyFallback sends the Bose remote key for a transport action when
// the speaker owns the playback. Returns true when it handled the request.
//
// Deliberately best-effort and quiet: the UPnP attempt already ran (or was
// skipped), the user pressed a button, and the worst outcome of an extra key on
// a box that was not playing anything is nothing at all.
func (s *Server) transportKeyFallback(ctx context.Context, key string) bool {
	if s.boxHost == "" {
		return false
	}
	src := fetchNowPlaying(ctx, s.boxHost).Source
	if !boxOwnedSource(src) {
		return false
	}
	kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Second)
	defer cancel()
	if err := boxapi.New(s.boxHost).Key(kctx, key); err != nil {
		s.logger.Warn("transport: the speaker plays this source itself and did not take the remote key",
			"source", src, "key", key, "err", err)
		return false
	}
	s.logger.Info("transport: the speaker plays this source itself, sent the remote key instead of a UPnP action",
		"source", src, "key", key)
	return true
}
