package presets

import (
	"slices"
	"strings"
)

// TypeQueue is the preset type of a saved music-library folder (Preset.Items).
const TypeQueue = "queue"

// SameFolder reports whether two folder presets (Type "queue") hold the same
// music-library folder, the test behind the one-folder-one-key rule (#1695).
//
//   - Both know their folder (Preset.Folder): they are the same when they name
//     the same media server and the same container on it. Two different
//     servers are never the same folder, even when they serve the same album:
//     a second server is a deliberate second copy and stays allowed.
//   - Otherwise, or when the containers differ on the same server (an album
//     a server lists under several trees), they are the same when they carry
//     exactly the same tracks, compared by URL and regardless of order. A
//     track URL names its server's host, so the same album from a different
//     server never matches here either.
//
// Shuffle, name and artwork play no part: a folder saved shuffled is still
// that folder, as a Spotify playlist is the same playlist with shuffle on.
func SameFolder(a, b Preset) bool {
	if a.Type != TypeQueue || b.Type != TypeQueue {
		return false
	}
	if fa, fb := a.Folder, b.Folder; fa.known() && fb.known() {
		if !sameServerUDN(fa.ServerUDN, fb.ServerUDN) {
			return false
		}
		if fa.ContainerID == fb.ContainerID {
			return true
		}
	}
	return sameTrackURLs(a.Items, b.Items)
}

// known reports whether a folder reference names both a server and a
// container.
func (f *FolderRef) known() bool {
	return f != nil && strings.TrimSpace(f.ServerUDN) != "" && strings.TrimSpace(f.ContainerID) != ""
}

// sameServerUDN compares two UPnP UDNs the way the media-server list does:
// without the "uuid:" prefix and case-insensitively.
func sameServerUDN(a, b string) bool {
	return strings.EqualFold(trimUUIDPrefix(a), trimUUIDPrefix(b))
}

func trimUUIDPrefix(udn string) string {
	udn = strings.TrimSpace(udn)
	if len(udn) >= 5 && strings.EqualFold(udn[:5], "uuid:") {
		return udn[5:]
	}
	return udn
}

// sameTrackURLs reports whether two track lists carry the same non-empty
// URLs, each as often, in any order. Items without a URL are skipped (they
// never play), and two lists without a single URL are not the same folder.
func sameTrackURLs(a, b []PresetItem) bool {
	ua, ub := trackURLs(a), trackURLs(b)
	if len(ua) == 0 || len(ua) != len(ub) {
		return false
	}
	slices.Sort(ua)
	slices.Sort(ub)
	return slices.Equal(ua, ub)
}

func trackURLs(items []PresetItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it.URL != "" {
			out = append(out, it.URL)
		}
	}
	return out
}
