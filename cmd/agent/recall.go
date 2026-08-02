// URL predicates shared by the preset recall and verify paths.

package main

import (
	"regexp"
	"strings"
	"time"
)

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

// ownNativePresetLocPrefix is the second shape STR writes: a native
// LOCAL_INTERNET_RADIO station whose descriptor is served by this agent's own
// orion adapter. It is relative on purpose (the firmware resolves it against
// the BMX service baseUrl). The prune must recognise it too, or a native slot
// the store no longer backs would survive as a dead hardware key forever.
const ownNativePresetLocPrefix = "/core02/svc-bmx-adapter-orion/prod/orion/station?data="

// isOwnBoxPresetLocation reports whether loc is a box-preset location STR
// itself wrote (strict match), the only shape the prune may remove.
func isOwnBoxPresetLocation(loc string) bool {
	return ownBoxPresetLocRe.MatchString(loc) ||
		strings.HasPrefix(loc, ownNativePresetLocPrefix)
}

// isPlayableURL reports whether u is an absolute HTTP(S) URL the UPnP renderer can
// actually load. Stale Bose-cloud ContentItems use relative, schemeless locations
// (e.g. "/v1/playback/station/...") that the box rejects with UPnP 402.
func isPlayableURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
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
