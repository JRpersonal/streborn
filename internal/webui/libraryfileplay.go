// A music-library file started through POST /api/play by a client that does
// not say it is one.
//
// The MIME in the play request is what tells the agent a URL is a FILE on a
// media server and not a radio stream. The desktop app and the phone page
// send it, read back from the Recently-played card. A third-party remote that
// replays such a card sends only the card's URL and name (#1871, 2026-11-12).
// The agent then took the file for a radio station: it handed it to the
// speaker as a station, answered "playing", and filed a new card under Radio
// next to the album's Library card. With the music server switched off
// nothing played at all, and the Radio card was all that was left of it.
//
// Two things follow here, both from what the agent already holds in memory:
//
//   - a URL the Recently-played ring or a folder key knows as a library file
//     is played and filed as one, whoever asks and whatever they leave out;
//   - a library file whose server accepts no connection is refused before the
//     speaker is woken and before anything is filed, with the same
//     library-server-offline answer a key gives (#1339).

package webui

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/JRpersonal/streborn/internal/presets"
)

// knownLibraryFile reports whether this agent holds fileURL as a
// music-library file, and the MIME and length it keeps for it: the newest
// Library card of the Recently-played ring that replays this URL, else a
// folder key that holds the file. Only an entry that carries a MIME counts,
// because the MIME is what the play needs. All in memory; nothing asks the
// media server.
func (s *Server) knownLibraryFile(fileURL string) (mime string, durationSec int, ok bool) {
	if fileURL == "" {
		return "", 0, false
	}
	if s.recent != nil {
		ring := s.recent.All() // oldest first
		for i := len(ring) - 1; i >= 0; i-- {
			e := ring[i]
			if e.Source != "upnp" || e.CardURL != fileURL || e.Mime == "" {
				continue
			}
			return e.Mime, e.DurationSec, true
		}
	}
	if s.presets != nil {
		for _, p := range s.presets.All() {
			if p.Type != "queue" {
				continue
			}
			for _, it := range p.Items {
				if it.URL == fileURL && it.Mime != "" {
					return it.Mime, it.DurationSec, true
				}
			}
		}
	}
	return "", 0, false
}

// adoptKnownLibraryFile completes a play request that names no MIME for a URL
// this agent knows as a library file, so the play goes the library way (direct,
// seekable, filed under Library) instead of the radio way.
func (s *Server) adoptKnownLibraryFile(req *playRequest) {
	if req.Mime != "" {
		return
	}
	mime, dur, ok := s.knownLibraryFile(req.URL)
	if !ok {
		return
	}
	s.logger.Info("play request: no MIME sent, but this agent knows the URL as a music-library file, playing it as one",
		"title", req.Title, "mime", mime, "url", req.URL)
	req.Mime = mime
	if req.DurationSec <= 0 {
		req.DurationSec = dur
	}
}

// libraryFileOfflineRefusal is the answer to a play of a single library file
// whose music server is off: nothing accepts a connection at the file's
// address, so the speaker could only time out on it. nil when the server
// answers, and when the address is a name rather than an IP: a name this
// agent cannot resolve says nothing about what the speaker reaches, and a
// media server hands out IP addresses anyway.
//
// The body is the library-server-offline one of a key (playrefusal.go)
// without a slot: both apps and the phone page already read it.
func (s *Server) libraryFileOfflineRefusal(ctx context.Context, fileURL, title string) *playRefusal {
	host := urlHost(fileURL)
	if net.ParseIP(host) == nil {
		return nil
	}
	if s.queueTrackHostAnswers(ctx, fileURL) {
		return nil
	}
	server := s.registeredServerAt(host)
	msg := "The music server is not answering. Switch it on and try again."
	if server != "" {
		msg = "The music server " + server + " is not answering. Switch it on and try again."
	}
	s.logger.Warn("play request: the file's music server is off, the speaker is not handed the file",
		"title", title, "server", server, "url", fileURL)
	return jsonRefusal(http.StatusBadGateway, map[string]any{
		"error": msg, "code": libraryServerOfflineCode,
		"offline": true, "server": server,
		"name": title, "url": fileURL,
	})
}

// registeredServerAt names the registered music source that lives at host, ""
// when none does. A media server serves its files from the address its device
// description sits at; the port usually differs, so only the host is compared.
func (s *Server) registeredServerAt(host string) string {
	if s.mediaServers == nil || host == "" {
		return ""
	}
	for _, reg := range s.mediaServers.List() {
		if urlHost(reg.Location) == host {
			return strings.TrimSpace(reg.Name)
		}
	}
	return ""
}

// radioKeyOfflineServerRefusal is the answer to an app or phone press on a key
// saved as a radio station whose stream is a file on a registered music server
// that is off. Such a key exists when a library file was saved to a key from a
// card that filed it under Radio, the mistake v1.6.1 stopped making for new
// plays (#1871, 2026-11-13): it holds the file's URL and nothing that names it
// a library file, so the library-key check (healLibraryKeyIfMoved) passes it by.
// Pressed with the server off, the speaker was woken, answered the dead file
// with BAD_URL, and the press still filed a Radio card and answered "playing".
//
// Only a key whose address is the host of a registered music server is asked,
// and only with a connect to that address: an internet station is never
// probed, and a server that accepts the connection is left to play. It runs
// before the wake and needs nothing from the speaker. A hardware press has no
// screen and goes ahead, as for every library key.
func (s *Server) radioKeyOfflineServerRefusal(ctx context.Context, slot int, p presets.Preset) *playRefusal {
	switch strings.ToLower(strings.TrimSpace(p.Type)) {
	case "spotify", "queue", presets.TypeNative:
		return nil
	}
	if isLibraryTrackKey(p) || !isPlainHTTPURL(p.StreamURL) {
		return nil
	}
	host := urlHost(p.StreamURL)
	if net.ParseIP(host) == nil {
		return nil
	}
	server := s.registeredServerAt(host)
	if server == "" || s.queueTrackHostAnswers(ctx, p.StreamURL) {
		return nil
	}
	s.logger.Warn("preset slot recall (app): a key saved as a station points at a music server that is off, not waking the speaker",
		"slot", slot, "name", p.Name, "server", server, "url", p.StreamURL)
	return s.libraryServerOfflineRefusal(slot, p.Name, &libraryKeyUnplayable{Server: server})
}
