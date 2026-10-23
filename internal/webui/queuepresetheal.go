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
// Deliberately narrow: the HOST is rewritten, never the path. The port moves
// only when the server was seen through a TRACK URL whose port differs from the
// key's and the key's old address no longer answers (#1366: a server that comes
// back on another port is a move too). A device-description URL never moves a
// port: a DLNA server's content port is not always its description port
// (Synology serves descriptions on 50001 and content on 50002), so a port
// "correction" from there would break working keys.

package webui

import (
	"context"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/dlna"
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
	// Still at an address that answers: nothing to do, and this is the normal
	// case.
	//
	// The store is not asked about that, in either direction. Only the agent's
	// OWN library resolves write the address into it, and the desktop app
	// browses the server itself, so on a box driven from the desktop the store
	// keeps the address it was given when the server was enabled. When the
	// server then moved, store and key agreed on the dead address and the heal
	// stood down on exactly the case it was written for (#1366: the server moved
	// overnight, both folder keys went silent, "Play folder" on the same
	// playlist worked). And a key that already sits where the server answers
	// must not be dragged back to a stale store address either. So ask the
	// key's own address once, a single TCP connect: on a server that is there
	// this costs a few milliseconds, and only a host that does not answer goes
	// on to the store and the resolve.
	if s.queueTrackHostAnswers(ctx, p.Items[0].URL) {
		return false, "", ""
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
		if loc, found := s.queueHealResolveServer(rctx, target.ID); found {
			newHost = urlHost(loc)
		}
	}
	if newHost == "" || newHost == oldHost {
		return false, "", ""
	}
	if rewriteQueuePresetHost(p, oldHost, newHost) == 0 {
		return false, "", ""
	}
	return true, oldHost, newHost
}

// rewriteQueuePresetHost moves every track URL and cover of a folder key that
// sits on oldHost to newHost, the key's own cover included: a folder whose
// tracks carry no cover is saved with the folder's, and that one pointed at the
// dead address too, so the key showed the placeholder (#1366). It returns how
// many TRACKS moved; a cover alone moving is not a heal.
func rewriteQueuePresetHost(p *presets.Preset, oldHost, newHost string) int {
	return rewriteQueuePresetAddr(p, oldHost, "", newHost, "")
}

// rewriteQueuePresetAddr is rewriteQueuePresetHost with a port move on top: a
// URL on oldHost whose port is oldPort ends up on newHost:newPort, every other
// URL on oldHost only changes host. Empty ports leave every port as it is.
func rewriteQueuePresetAddr(p *presets.Preset, oldHost, oldPort, newHost, newPort string) int {
	// The store hands out presets whose Items share its own backing array, so
	// rewriting in place would change the stored key behind the store's lock.
	p.Items = append([]presets.PresetItem(nil), p.Items...)
	n := 0
	for i := range p.Items {
		if u, okRe := rewriteURLAddr(p.Items[i].URL, oldHost, oldPort, newHost, newPort); okRe {
			p.Items[i].URL = u
			n++
		}
		if u, okRe := rewriteURLAddr(p.Items[i].Art, oldHost, oldPort, newHost, newPort); okRe {
			p.Items[i].Art = u
		}
	}
	if n > 0 {
		if u, okRe := rewriteURLAddr(p.Art, oldHost, oldPort, newHost, newPort); okRe {
			p.Art = u
		}
	}
	return n
}

// realignQueuePresetArt moves the covers of a folder key onto host, the host
// its tracks sit on, and reports whether any cover moved.
//
// The first version of this heal (v0.9.88) moved the tracks and left the covers
// behind, so a key pressed after the server moved played again but showed the
// placeholder for a cover from then on; #1366 is that key, noticed after an app
// update that had nothing to do with it. Those keys have their tracks on the
// live address already, which is why no later sighting of the server moved
// them: a track rewrite finds nothing on the old host. A cover of a library key
// is always the media server's own (its upnp:albumArtURI), so a cover on a
// different host than the tracks is a cover left on a dead address.
//
// keep names the hosts a cover must NOT be taken off: another registered
// server's, the speaker's own, and loopback. A cover there is not this server's
// stale address, whatever else it is.
func realignQueuePresetArt(p *presets.Preset, host string, keep func(string) bool) bool {
	if p == nil || host == "" {
		return false
	}
	move := func(raw string) (string, bool) {
		h := urlHost(raw)
		if h == "" || h == host || keep(h) {
			return raw, false
		}
		return rewriteURLHost(raw, h, host)
	}
	moved := false
	if u, ok := move(p.Art); ok {
		p.Art, moved = u, true
	}
	for i := range p.Items {
		u, ok := move(p.Items[i].Art)
		if !ok {
			continue
		}
		if !moved {
			// Same rule as the track rewrite: never write into the store's
			// backing array.
			p.Items = append([]presets.PresetItem(nil), p.Items...)
		}
		p.Items[i].Art, moved = u, true
	}
	return moved
}

// artHostKeeper is the keep rule for realignQueuePresetArt: hosts that are
// some other registered server's address, the speaker's own address, or
// loopback are never mistaken for a stale address of the server named by id.
func (s *Server) artHostKeeper(id string) func(string) bool {
	keep := map[string]bool{}
	if s.mediaServers != nil {
		for _, r := range s.mediaServers.List() {
			if udnKey(r.ID) == udnKey(id) {
				continue
			}
			if h := urlHost(r.Location); h != "" {
				keep[h] = true
			}
		}
	}
	if h := strings.TrimSpace(s.boxHost); h != "" {
		keep[h] = true
	}
	return func(h string) bool {
		if keep[h] {
			return true
		}
		if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
			return true
		}
		return strings.EqualFold(h, "localhost")
	}
}

// queueHostProbeTimeout bounds the connect that asks whether the address a key
// and the store agree on still answers. A LAN host that is there answers in
// milliseconds; one that is gone must not hold the key press up for long.
const queueHostProbeTimeout = 1500 * time.Millisecond

// queueTrackHostAnswers reports whether the host:port of a track URL accepts a
// TCP connection. A refusal counts as "not there" just like a timeout: either
// way the speaker cannot fetch the track from it.
func (s *Server) queueTrackHostAnswers(ctx context.Context, rawURL string) bool {
	if s.queueHostAnswers != nil {
		return s.queueHostAnswers(ctx, rawURL)
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil || u.Hostname() == "" {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		}
	}
	d := net.Dialer{Timeout: queueHostProbeTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// queueHealResolveServer finds where a registered media server answers now and
// returns its device-description URL.
func (s *Server) queueHealResolveServer(ctx context.Context, id string) (string, bool) {
	if s.queueHealResolve != nil {
		return s.queueHealResolve(ctx, id)
	}
	srv, _, found := s.resolveMediaServer(ctx, id)
	if !found {
		return "", false
	}
	return srv.Location, true
}

// serverSighting is one moment STR is TOLD where a registered media server
// lives now: a URL on its current address, and whether that URL is a track.
//
// A track URL carries the content port, so a key whose tracks sit on the same
// host but another port has moved too. A device-description URL says nothing
// about the content port, so only the host counts then.
type serverSighting struct {
	url   string
	track bool
	// learned names the sighting in the log: "library" for a folder started
	// from the Library, "browse" for a folder the app opened.
	learned string
}

// healQueuePresetsFromLiveQueue corrects the folder keys of a media server from
// a queue that was just started from that server's Library.
//
// "Play folder" is the one moment STR is TOLD where a server lives now: the
// desktop app browsed it a second ago, and every track URL of the queue carries
// its current address. Without using that, a key whose server moved stayed on
// the dead address until somebody pressed it, and until then it showed the
// placeholder instead of its cover and never lit up while its own playlist
// played (#1366). The key press still heals on its own; this only gets there
// first, and needs no network for it.
//
// The server is the one the folder card names (its UDN), and a key counts as
// that server's when it records the server's name, the identity #733 made the
// durable one. Two registered servers sharing that name is not certain enough,
// so nothing moves then.
func (s *Server) healQueuePresetsFromLiveQueue(card recentCardCtx, items []queueItem) {
	if s.presets == nil || s.mediaServers == nil || len(items) == 0 {
		return
	}
	udn, _, ok := s.splitQueueCardKey(card.key)
	if !ok {
		return
	}
	reg, found := s.registeredServerByUDN(udn)
	if !found {
		return
	}
	s.healQueuePresetsForServer(context.Background(), reg, serverSighting{url: items[0].URL, track: true, learned: "library"})
}

// healQueuePresetsFromBrowse corrects the folder keys of a media server the app
// just browsed through the agent: the Browse answered, so the resolved address
// is where the server lives now (#1366). The desktop app browses servers on its
// own and calls this endpoint once per server it opened, precisely so the keys
// follow the server at that moment and not only on a key press or "Play folder".
//
// A page with tracks names the content port; a page of folders only (the root
// of most servers) moves the host alone.
func (s *Server) healQueuePresetsFromBrowse(ctx context.Context, srv dlna.Server, res dlna.BrowseResult) {
	if s.presets == nil || s.mediaServers == nil {
		return
	}
	reg, found := s.registeredServerByUDN(srv.UDN)
	if !found {
		return
	}
	seen := serverSighting{url: srv.Location, learned: "browse"}
	for _, it := range res.Items {
		if strings.TrimSpace(it.StreamURL) != "" {
			seen = serverSighting{url: it.StreamURL, track: true, learned: "browse"}
			break
		}
	}
	s.healQueuePresetsForServer(ctx, reg, seen)
}

// registeredServerByUDN finds the registered media server with that UDN, with
// or without the "uuid:" prefix.
func (s *Server) registeredServerByUDN(udn string) (mediaservers.Server, bool) {
	if s.mediaServers == nil || udnKey(udn) == "" {
		return mediaservers.Server{}, false
	}
	for _, r := range s.mediaServers.List() {
		if udnKey(r.ID) == udnKey(udn) {
			return r, true
		}
	}
	return mediaservers.Server{}, false
}

// healQueuePresetsForServer puts every folder key of reg on the address the
// sighting names, the store entry included, and persists what changed.
//
// Two registered servers sharing reg's name is not certain enough: a key only
// records the name, so nothing moves then. A key whose tracks sit on the live
// host but another port is only moved after its own address stopped answering:
// a server that serves two ports keeps both keys working, and the probe is the
// one network call this path can make.
func (s *Server) healQueuePresetsForServer(ctx context.Context, reg mediaservers.Server, seen serverSighting) {
	if s.presets == nil || s.mediaServers == nil {
		return
	}
	liveHost := urlHost(seen.url)
	if liveHost == "" {
		return
	}
	livePort := ""
	if seen.track {
		livePort = urlPort(seen.url)
	}
	name := strings.TrimSpace(strings.ToLower(reg.Name))
	if name == "" {
		return
	}
	for _, r := range s.mediaServers.List() {
		if r.ID != reg.ID && strings.TrimSpace(strings.ToLower(r.Name)) == name {
			return
		}
	}
	// The store learns the address too, so the agent's own browse and the next
	// key press start from where the server is instead of where it was.
	if regHost := urlHost(reg.Location); regHost != "" && regHost != liveHost {
		if loc, okRe := rewriteURLHost(reg.Location, regHost, liveHost); okRe {
			if err := s.mediaServers.SetLocation(reg.ID, loc); err != nil {
				s.logger.Info("queue preset: could not store the server's new address", "err", err)
			}
			s.mediaLocMu.Lock()
			if _, known := s.mediaLoc[udnKey(reg.ID)]; known {
				s.mediaLoc[udnKey(reg.ID)] = loc
			}
			s.mediaLocMu.Unlock()
			s.logger.Info("queue preset: the library showed the music server at a new address",
				"server", reg.Name, "was", regHost, "now", liveHost, "learned", seen.learned)
		}
	}
	keep := s.artHostKeeper(reg.ID)
	for _, p := range s.presets.All() {
		if p.Type != "queue" || len(p.Items) == 0 {
			continue
		}
		if strings.TrimSpace(strings.ToLower(p.Source)) != name {
			continue
		}
		oldHost, oldPort := urlHost(p.Items[0].URL), urlPort(p.Items[0].URL)
		if oldHost == "" {
			continue
		}
		newPort := oldPort
		if livePort != "" && livePort != oldPort {
			newPort = livePort
		}
		moved := 0
		switch {
		case oldHost != liveHost:
			moved = rewriteQueuePresetAddr(&p, oldHost, oldPort, liveHost, newPort)
		case newPort != oldPort && !s.queueTrackHostAnswers(ctx, p.Items[0].URL):
			moved = rewriteQueuePresetAddr(&p, oldHost, oldPort, liveHost, newPort)
		}
		onLive := moved > 0 || oldHost == liveHost
		artMoved := onLive && realignQueuePresetArt(&p, liveHost, keep)
		if moved == 0 && !artMoved {
			continue
		}
		if moved > 0 {
			s.logger.Info("queue preset: the music server moved, pointing the key at its new address",
				"slot", p.Slot, "server", p.Source, "was", net.JoinHostPort(oldHost, oldPort),
				"now", net.JoinHostPort(liveHost, newPort), "tracks", len(p.Items), "learned", seen.learned)
		} else {
			s.logger.Info("queue preset: the key's cover still named the music server's old address, moved it",
				"slot", p.Slot, "server", p.Source, "now", liveHost, "learned", seen.learned)
		}
		if err := s.presets.SetSlot(p); err != nil {
			s.logger.Info("queue preset: could not store the corrected address", "slot", p.Slot, "err", err)
		}
	}
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

// urlPort is the port of an absolute URL, the scheme's default when the URL
// names none, so "http://h/x" and "http://h:80/x" sit on the same address.
func urlPort(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Hostname() == "" {
		return ""
	}
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// rewriteURLHost swaps the host of an absolute URL, keeping scheme, port, path
// and query exactly as they were. Reports false when the URL does not sit on
// that host at all, so a folder mixing two servers only moves what moved.
func rewriteURLHost(raw, oldHost, newHost string) (string, bool) {
	return rewriteURLAddr(raw, oldHost, "", newHost, "")
}

// rewriteURLAddr is rewriteURLHost with a port move on top: a URL on
// oldHost:oldPort ends up on newHost:newPort; one on oldHost with another port
// only changes host. Empty ports leave the port exactly as it was.
func rewriteURLAddr(raw, oldHost, oldPort, newHost, newPort string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return raw, false
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Hostname() != oldHost {
		return raw, false
	}
	port := u.Port()
	if oldPort != "" && newPort != "" && oldPort != newPort && urlPort(raw) == oldPort {
		port = newPort
	}
	if port != "" {
		u.Host = net.JoinHostPort(newHost, port)
	} else {
		u.Host = newHost
	}
	return u.String(), true
}

// healQueuePresetIfMoved runs the heal for one slot and persists the result.
// Written only when something actually changed, because this runs on a key press
// and the speaker's flash is not free.
//
// The covers are checked even when the tracks did not move: a key an older
// agent healed has its tracks on the live address and its covers on the dead
// one (#1366), and the press is the moment that key is in hand.
func (s *Server) healQueuePresetIfMoved(ctx context.Context, slot int, p *presets.Preset) {
	changed, from, to := s.healQueuePresetHost(ctx, p)
	artMoved := false
	if p != nil && p.Type == "queue" && len(p.Items) > 0 && s.mediaServers != nil {
		reg, _ := matchRegisteredServer(s.mediaServers.List(), p.Source)
		artMoved = realignQueuePresetArt(p, urlHost(p.Items[0].URL), s.artHostKeeper(reg.ID))
	}
	if !changed && !artMoved {
		return
	}
	if changed {
		s.logger.Info("queue preset: the music server moved, pointing the key at its new address",
			"slot", slot, "server", p.Source, "was", from, "now", to, "tracks", len(p.Items))
	} else {
		s.logger.Info("queue preset: the key's cover still named the music server's old address, moved it",
			"slot", slot, "server", p.Source, "now", urlHost(p.Items[0].URL))
	}
	if s.presets == nil {
		return
	}
	if err := s.presets.SetSlot(*p); err != nil {
		// The play still goes ahead with the healed items in memory; only the
		// next recall pays for the heal again.
		s.logger.Info("queue preset: could not store the corrected address", "slot", slot, "err", err)
	}
}
