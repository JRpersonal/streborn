// A single library track on a key must survive the music server changing its
// address, the way a folder key does (queuepresetheal.go).
//
// A track saved from the Library tab is a radio-shaped preset whose StreamURL
// is the file on the LAN media server, address included (#1065). The folder
// heal only ever looked at queue presets, so when the reporter's MinimServer
// got a new address (#1366), the folder key on slot 2 was healed on its press
// and played, while the two single-track keys next to it were handed to the
// speaker on the dead address: "preset slot recall (app): direct library file",
// AUDIO_ERROR_TIMEOUT, "single track: the speaker dropped the track without
// playing it". Tracking issue #919: library presets die when the media server
// renumbers its links.
//
// Which server a single-track key belongs to: the name it recorded at save
// time (Source, written by the Library tab's save and by the hold-to-save),
// else the one registered server whose address host the file sits on. The
// host match is what heals keys saved before the name was reliable (the
// hold-save fallback "Music library" names no server) without re-saving them.
// The same rules as the folder heal apply otherwise: the key's own address is
// probed first, a port only moves on a track sighting, and a radio station on
// a public host is never a library key (libraryFileMime says so).

package webui

import (
	"context"
	"net"

	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
)

// isLibraryTrackKey reports whether p is a single library track on a key: a
// radio-shaped preset saved from a media server whose file the speaker is
// handed directly (#1065).
func isLibraryTrackKey(p presets.Preset) bool {
	return libraryFileMime(p) != ""
}

// libraryTrackKeyServer picks the registered server a single-track key came
// from: the one its Source names, else the only registered server whose
// address host the file sits on. Two servers on that host is not certain
// enough, so nothing is picked then.
func libraryTrackKeyServer(regs []mediaservers.Server, p presets.Preset) (mediaservers.Server, bool) {
	if reg, ok := matchRegisteredServer(regs, p.Source); ok {
		return reg, true
	}
	return registeredServerOnHost(regs, urlHost(p.StreamURL))
}

// registeredServerOnHost is the one registered server whose address sits on
// host; none when no server or more than one does.
func registeredServerOnHost(regs []mediaservers.Server, host string) (mediaservers.Server, bool) {
	if host == "" {
		return mediaservers.Server{}, false
	}
	var found mediaservers.Server
	n := 0
	for _, r := range regs {
		if urlHost(r.Location) == host {
			found = r
			n++
		}
	}
	if n != 1 {
		return mediaservers.Server{}, false
	}
	return found, true
}

// libraryTrackKeyBelongsTo reports whether a single-track key is reg's: its
// Source names reg, or the file sits on the host reg was registered at
// (regHost, the store's address BEFORE a sighting moved it) or already on the
// live host, with no other registered server on that host to confuse it with.
func libraryTrackKeyBelongsTo(regs []mediaservers.Server, reg mediaservers.Server, regHost, liveHost string, p presets.Preset) bool {
	if named, ok := matchRegisteredServer(regs, p.Source); ok {
		return named.ID == reg.ID
	}
	host := urlHost(p.StreamURL)
	if host == "" || (host != regHost && host != liveHost) {
		return false
	}
	for _, r := range regs {
		if r.ID != reg.ID && urlHost(r.Location) == host {
			return false
		}
	}
	return true
}

// rewriteLibraryKeyAddr moves the file and the cover of a single-track key
// from oldHost:oldPort to newHost:newPort (rewriteURLAddr's rules) and returns
// how many FILES moved, so a cover alone moving is not a heal. The path and
// query are untouched: MinimServer's "*" escapes and the "$!picture" cover
// suffix must come out byte for byte.
func rewriteLibraryKeyAddr(p *presets.Preset, oldHost, oldPort, newHost, newPort string) int {
	n := 0
	if u, ok := rewriteURLAddr(p.StreamURL, oldHost, oldPort, newHost, newPort); ok {
		p.StreamURL = u
		n++
	}
	if n > 0 {
		if u, ok := rewriteURLAddr(p.Art, oldHost, oldPort, newHost, newPort); ok {
			p.Art = u
		}
	}
	return n
}

// healLibraryKeyHost rewrites a single-track key's file URL onto the address
// its media server answers at now. It reports whether anything changed and
// the two hosts, for the log line and the caller's decision to persist. The
// same stand-downs as healQueuePresetHost: not a library key, no host, the
// key's own address still answers, no way to tell which server it came from,
// nowhere to point it.
func (s *Server) healLibraryKeyHost(ctx context.Context, p *presets.Preset) (changed bool, from, to string) {
	if p == nil || !isLibraryTrackKey(*p) || s.mediaServers == nil {
		return false, "", ""
	}
	oldHost := urlHost(p.StreamURL)
	if oldHost == "" {
		return false, "", ""
	}
	regs := s.mediaServers.List()
	if len(regs) == 0 {
		return false, "", ""
	}
	// One TCP connect to the key's own address, for the reasons the folder heal
	// gives: the store is not trusted in either direction, and a key that sits
	// where the server answers is never dragged anywhere.
	if s.queueTrackHostAnswers(ctx, p.StreamURL) {
		return false, "", ""
	}
	target, ok := libraryTrackKeyServer(regs, *p)
	if !ok {
		return false, "", ""
	}
	newHost := urlHost(target.Location)
	if newHost == "" || newHost == oldHost {
		rctx, cancel := context.WithTimeout(ctx, queuePresetHealTimeout)
		defer cancel()
		if loc, found := s.queueHealResolveServer(rctx, target.ID); found {
			newHost = urlHost(loc)
		}
	}
	if newHost == "" || newHost == oldHost {
		return false, "", ""
	}
	// A resolve hands back the device-description URL, which says nothing
	// about the content port, so only the host moves here.
	if rewriteLibraryKeyAddr(p, oldHost, "", newHost, "") == 0 {
		return false, "", ""
	}
	return true, oldHost, newHost
}

// healLibraryKeyIfMoved runs the single-track heal for one slot on a key
// press, moves a cover left behind on a dead address, and persists what
// changed. Written only when something changed: this runs on a key press and
// the speaker's flash is not free. A key that is not a library track is left
// exactly as it is, radio stations included.
//
// After the address, the file itself: a server that renumbered its file URLs
// (#919) is asked for the track's current one when the key knows the track's
// object id (libraryreresolve.go).
//
// It returns the verdict that the key's music server is off (#1339) when the
// file did not serve and the server could not be found; nil otherwise, always
// for a key that serves or that stores no server identity. The app's recall
// refuses the press on it; a hardware press has no screen and goes ahead.
func (s *Server) healLibraryKeyIfMoved(ctx context.Context, slot int, p *presets.Preset) *libraryServerOffline {
	s.healLibraryKey(ctx, slot, p, nil)
	_, off := s.reresolveLibraryKey(ctx, slot, p)
	return off
}

// healLibraryKey is healLibraryKeyIfMoved with the background sweep's store
// guard (storeHealedKey): with orig set the heal is only written while the
// slot still holds orig. It reports whether the key changed in memory.
func (s *Server) healLibraryKey(ctx context.Context, slot int, p *presets.Preset, orig *presets.Preset) bool {
	if p == nil || !isLibraryTrackKey(*p) || s.mediaServers == nil {
		return false
	}
	port := urlPort(p.StreamURL)
	changed, from, to := s.healLibraryKeyHost(ctx, p)
	reg, _ := libraryTrackKeyServer(s.mediaServers.List(), *p)
	artMoved := realignQueuePresetArt(p, urlHost(p.StreamURL), s.artHostKeeper(reg.ID))
	if !changed && !artMoved {
		return false
	}
	if changed {
		s.logger.Info("library key: the music server moved, pointing the key at its new address",
			"slot", slot, "server", p.Source, "was", net.JoinHostPort(from, port), "now", net.JoinHostPort(to, port))
	} else {
		s.logger.Info("library key: the key's cover still named the music server's old address, moved it",
			"slot", slot, "server", p.Source, "now", urlHost(p.StreamURL))
	}
	// The play still goes ahead with the healed URL in memory when the write
	// fails; only the next press pays for the heal again.
	s.storeHealedKey("library key", slot, *p, orig)
	return true
}
