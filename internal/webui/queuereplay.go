// Replaying a Recently-played folder card as a whole folder again.
//
// A folder played as an auto-advancing queue is recorded as ONE Recently-played
// card, and that card stores the FIRST track's URL as its replay target
// (queueplay.go). For the cover and the "what was this" line that is right. For
// the replay button it was quietly wrong: both clients sent that single URL
// through the single-track play path, which clears the queue by design, so the
// folder played its first song, nothing advanced, and the speaker stopped with
// the indicator sitting amber.
//
// Reported on 2026-09-26 against v0.9.87 in discussion #978 by the same person
// who had already had the MIME half of this replay fixed (#817). It is a
// different defect from that one: the track that plays is correct, it is the
// eleven after it that never arrive.
//
// The card key is the folder's identity: "queue:<server UDN>:<container id>",
// written by the desktop app when it starts the queue. So the cure is to browse
// that container again and start a real queue from it. Doing that here rather
// than in the desktop app is what also gives the PHONE remote a folder replay,
// which it never had: the phone cannot browse a NAS itself, which is why its
// replay button went through the same single-URL path.

package webui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/dlna"
)

const (
	// queueCardKeyPrefix marks a Recently-played card that stands for a folder
	// rather than a single track. Written by the desktop app (views/library.js).
	queueCardKeyPrefix = "queue:"
	// queueReplayMaxItems caps one replayed folder. Same ceiling the desktop
	// Library pages to (LIB_MAX) and the search walk uses per container, for the
	// same reason: the box holds the queue in memory.
	queueReplayMaxItems = 2500
	// queueReplayBudget bounds the whole handler, browsing included. A folder of
	// a few hundred tracks is a handful of Browse calls against a NAS that may
	// be asleep, and the desktop app waits on this call.
	queueReplayBudget = 60 * time.Second
)

// parseQueueCardKey splits "queue:<udn>:<container>" into its two halves.
//
// The UDN carries colons of its own ("uuid:4d696e69-..."), the container id
// normally does not, so the container is what follows the LAST colon. A key
// whose container half is empty still resolves, to the server root: that is what
// the desktop app writes when the folder it played WAS the root.
func parseQueueCardKey(key string) (udn, container string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(key), queueCardKeyPrefix)
	if !found {
		return "", "", false
	}
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return "", "", false
	}
	udn = strings.TrimSpace(rest[:i])
	container = strings.TrimSpace(rest[i+1:])
	if udn == "" {
		return "", "", false
	}
	if container == "" {
		container = "0"
	}
	return udn, container, true
}

type queueReplayRequest struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Art  string `json:"art"`
}

// handleQueueReplayCard answers POST /api/queue/replay-card: browse the folder a
// Recently-played card stands for and start the whole thing as a queue again.
func (s *Server) handleQueueReplayCard(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
	var req queueReplayRequest
	if !decodeJSONRequest(w, r, 1<<16, &req) {
		return
	}
	udn, container, ok := parseQueueCardKey(req.Key)
	if !ok {
		http.Error(w, "not a folder card", http.StatusBadRequest)
		return
	}
	if s.mediaServers == nil || !s.mediaServerRegistered(udn) {
		// Same rule as browse and search: only servers the user registered as
		// music sources, so this endpoint cannot be used to make the speaker
		// poke at arbitrary UPnP devices on the LAN.
		http.Error(w, "not a registered music source", http.StatusNotFound)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), queueReplayBudget)
	defer cancel()
	srv, live, found := s.resolveMediaServer(ctx, udn)
	if !found {
		writeJSON(w, http.StatusOK, s.libraryOfflineReply(udnKey(udn), live))
		return
	}
	items, err := s.browseQueueItems(ctx, srv, container)
	if err != nil {
		s.logger.Info("folder card replay: the folder could not be read",
			"server", srv.FriendlyName, "container", container, "err", err)
		writeJSON(w, http.StatusOK, s.libraryOfflineReply(udnKey(udn), live))
		return
	}
	if len(items) == 0 {
		// The folder is still there but holds nothing playable any more (renamed,
		// re-tagged, moved). Say that rather than starting an empty queue.
		writeJSON(w, http.StatusOK, map[string]any{"error": "the folder has no playable tracks any more", "code": "folder-empty"})
		return
	}

	name := strings.TrimSpace(req.Name)
	art := strings.TrimSpace(req.Art)
	if art == "" {
		art = items[0].Art
	}
	card := recentCardCtx{key: req.Key, name: name, art: art}
	// Inherit the sticky shuffle/repeat the user last chose (playmode.go). A
	// replay is not the place to reset it, and it is not an explicit choice
	// either, so nothing is saved back.
	shuffle, rep, _ := s.loadPlayMode()
	// Detached like the other queue start (#252): the standby wake inside
	// startQueue can outlast the caller's HTTP timeout, and the first track must
	// still reach the speaker after the app gave up waiting.
	playCtx, playCancel := context.WithTimeout(context.WithoutCancel(r.Context()), playDetachTimeout)
	defer playCancel()
	s.logger.Info("folder card replay: starting the folder as a queue",
		"server", srv.FriendlyName, "container", container, "tracks", len(items), "shuffle", shuffle)
	if err := s.startQueue(playCtx, items, -1, shuffle, rep, card); err != nil {
		if isGroupedRejection(err) {
			s.writeGroupedPlayError(w, err)
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "tracks": len(items)})
}

// mediaServerRegistered reports whether this UDN is one of the servers the user
// picked as a music source.
func (s *Server) mediaServerRegistered(udn string) bool {
	for _, reg := range s.mediaServers.List() {
		if udnKey(reg.ID) == udnKey(udn) {
			return true
		}
	}
	return false
}

// browseQueueItems pages one container and returns its playable tracks in server
// order. Non-recursive, exactly like the desktop app's "play folder": the queue
// is what the open folder holds, not everything below it.
//
// The paging follows the same rule the #666 search walk had to learn, and for the
// same reason: a SHORT PAGE is not the end of the container. Plenty of
// ContentDirectory servers silently cap RequestedCount, so advancing by the page
// size that was ASKED for, and stopping as soon as fewer come back, would replay
// only the first hundred tracks of a long album list on exactly those servers.
// Advance by what actually arrived and let TotalMatches say when it is done.
func (s *Server) browseQueueItems(ctx context.Context, srv dlna.Server, container string) ([]queueItem, error) {
	out := make([]queueItem, 0, 64)
	seen := make(map[string]bool)
	for start := 0; start < queueReplayMaxItems; {
		res, err := dlna.Browse(ctx, srv, container, start, libraryBrowsePage)
		if err != nil {
			if len(out) > 0 {
				// Part of the folder is in hand. Playing that beats refusing the
				// replay because page four timed out.
				s.logger.Info("folder card replay: stopping the browse early",
					"container", container, "have", len(out), "err", err)
				return out, nil
			}
			return nil, err
		}
		fresh := 0
		for _, it := range res.Items {
			if it.StreamURL == "" || seen[it.StreamURL] {
				continue
			}
			seen[it.StreamURL] = true
			fresh++
			out = append(out, queueItem{
				URL:      it.StreamURL,
				Title:    it.Title,
				Art:      it.AlbumArtURL,
				Mime:     trackMime(it),
				Duration: time.Duration(it.DurationSec) * time.Second,
			})
			if len(out) >= queueReplayMaxItems {
				return out, nil
			}
		}
		returned := len(res.Containers) + len(res.Items)
		start += returned
		// A server that answers nothing, or one that ignores StartingIndex and
		// hands back the same children forever, both end here.
		if returned == 0 || (fresh == 0 && len(res.Containers) == 0) {
			return out, nil
		}
		if res.TotalMatches > 0 {
			if start >= res.TotalMatches {
				return out, nil
			}
		} else if returned < libraryBrowsePage {
			// No count from the server: the short page is all there is to go on.
			return out, nil
		}
	}
	return out, nil
}
