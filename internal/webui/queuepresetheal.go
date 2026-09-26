// A saved folder key must survive the music server changing its address.
//
// A queue preset stores the tracks as ABSOLUTE URLs, because that is what the
// speaker has to fetch. Those URLs carry the server's IP, so the day the router
// hands the NAS a different lease, all six keys pointing at it go dead at once
// and nothing says why. The workaround people arrive at is a static lease, and
// it is a fair workaround, but it is one the owner should not have to find:
// STR knows which server the key came from (the preset records its name) and it
// knows where that server lives now (the media-server store, kept current by
// every browse and every discovery round).
//
// Reported on 2026-09-25 in discussion #977 as the third point of an Apple Music
// write-up: "the host device running MinimServer needs a static IP address.
// Without it, router IP reassignments will break the preset keys mapped to the
// playlists."
//
// Same family as #733/#726, where a media server that moved had to be re-found
// by NAME rather than by the address last seen. This is that lesson applied one
// layer further in, to the URLs already written into a preset.
//
// Deliberately narrow: only the HOST is rewritten, never the port or the path.
// A DLNA server's content port is not always its description port (Synology
// serves descriptions on 50001 and content on 50002), so a port "correction"
// would break working keys to fix a case nobody reported.

package webui

import (
	"context"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
)

// queuePresetHealTimeout bounds the live re-resolve. Only spent when the stored
// address is stale too, and a key press is waiting on it, so it is short.
const queuePresetHealTimeout = 5 * time.Second

// healQueuePresetHost rewrites a queue preset's track URLs onto the address the
// media server answers at now. It reports whether anything changed and the two
// hosts, for the log line and the caller's decision to persist.
//
// It returns false without touching the preset in every case where the answer is
// not certain: no items, no host, the server still at that address, no way to
// tell which server the key came from. A wrong rewrite is worse than a dead key,
// because a dead key is at least honest about being dead.
func (s *Server) healQueuePresetHost(ctx context.Context, p *presets.Preset) (changed bool, from, to string) {
	if p == nil || p.Type != "queue" || len(p.Items) == 0 || s.mediaServers == nil {
		return false, "", ""
	}
	oldHost := urlHost(p.Items[0].URL)
	if oldHost == "" {
		return false, "", ""
	}
	regs := s.mediaServers.List()
	if len(regs) == 0 {
		return false, "", ""
	}
	// Still at that address: nothing to do, and this is the normal case, so it
	// costs one string compare per registered server and no network at all.
	for _, reg := range regs {
		if urlHost(reg.Location) == oldHost {
			return false, "", ""
		}
	}
	// Which server was this key saved from? The preset records the server's
	// display name (Source), which is exactly the identity #733 established as
	// the durable one. One registered server and no name is the other clear
	// case: there is nothing else it could have come from.
	target, ok := matchRegisteredServer(regs, p.Source)
	if !ok {
		return false, "", ""
	}
	newHost := urlHost(target.Location)
	if newHost == "" || newHost == oldHost {
		// The store is stale as well. Ask the network once, bounded: without
		// this the heal can only fix a server whose move STR already noticed.
		rctx, cancel := context.WithTimeout(ctx, queuePresetHealTimeout)
		defer cancel()
		if srv, _, found := s.resolveMediaServer(rctx, target.ID); found {
			newHost = urlHost(srv.Location)
		}
	}
	if newHost == "" || newHost == oldHost {
		return false, "", ""
	}
	n := 0
	for i := range p.Items {
		if u, okRe := rewriteURLHost(p.Items[i].URL, oldHost, newHost); okRe {
			p.Items[i].URL = u
			n++
		}
		if u, okRe := rewriteURLHost(p.Items[i].Art, oldHost, newHost); okRe {
			p.Items[i].Art = u
		}
	}
	if n == 0 {
		return false, "", ""
	}
	return true, oldHost, newHost
}

// matchRegisteredServer picks the server a preset came from: by the name the
// preset recorded, else the only one registered.
func matchRegisteredServer(regs []mediaservers.Server, source string) (mediaservers.Server, bool) {
	want := strings.TrimSpace(strings.ToLower(source))
	if want != "" {
		for _, reg := range regs {
			if strings.TrimSpace(strings.ToLower(reg.Name)) == want {
				return reg, true
			}
		}
		// A name that matches nothing registered is not a licence to guess at
		// the only server: the key may well belong to a server the owner has
		// since removed as a music source.
		return mediaservers.Server{}, false
	}
	if len(regs) == 1 {
		return regs[0], true
	}
	return mediaservers.Server{}, false
}

// urlHost is the hostname of an absolute URL, without the port.
func urlHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return ""
	}
	return u.Hostname()
}

// rewriteURLHost swaps the host of an absolute URL, keeping scheme, port, path
// and query exactly as they were. Reports false when the URL does not sit on
// that host at all, so a folder mixing two servers only moves what moved.
func rewriteURLHost(raw, oldHost, newHost string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return raw, false
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Hostname() != oldHost {
		return raw, false
	}
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort(newHost, port)
	} else {
		u.Host = newHost
	}
	return u.String(), true
}

// healQueuePresetIfMoved runs the heal for one slot and persists the result.
// Written only when something actually changed, because this runs on a key press
// and the speaker's flash is not free.
func (s *Server) healQueuePresetIfMoved(ctx context.Context, slot int, p *presets.Preset) {
	changed, from, to := s.healQueuePresetHost(ctx, p)
	if !changed {
		return
	}
	s.logger.Info("queue preset: the music server moved, pointing the key at its new address",
		"slot", slot, "server", p.Source, "was", from, "now", to, "tracks", len(p.Items))
	if s.presets == nil {
		return
	}
	if err := s.presets.SetSlot(*p); err != nil {
		// The play still goes ahead with the healed items in memory; only the
		// next recall pays for the heal again.
		s.logger.Info("queue preset: could not store the corrected address", "slot", slot, "err", err)
	}
}
