package webui

// Zone-mirror reconcile guards and dissolve propagation (#342).
//
// The 5-minute PeriodicZoneReconcile used to re-push the master's persisted
// lastPlay to every mirror slave unconditionally. Two real-world failures:
// a master sitting in STANDBY sprayed its stale last stream onto slaves that
// were busy playing something else (a Spotify playlist was yanked back to the
// master's old radio station every 5 minutes), and a healthy mirroring slave
// was re-pushed anyway, re-buffering its stream every tick. The pure decision
// helpers here make the reconcile repair only what is actually broken; the
// purge endpoint lets a dissolving master clear the group from every member's
// persisted store, so a forgotten zones.json cannot keep re-asserting a group
// the user tore down (both directions of a stale mutual group were seen in
// the wild).

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// playingStates are the box playStatus values that mean audio is actually
// coming out (or about to): anything else is stopped, paused, or unknown.
func isPlayingStatus(playStatus string) bool {
	return playStatus == "PLAY_STATE" || playStatus == "BUFFERING_STATE"
}

// masterMirrorSkipReason decides whether the periodic reconcile may mirror at
// all. Empty means proceed. np is the MASTER box's live now_playing;
// lastPlayURL is the loopback stream URL STR last told the master to play
// (lastPlay.boxURL). The master must be audibly playing that exact stream:
// lastPlay is persisted on NAND and survives reboots, so an idle master still
// "remembers" a station from days ago — mirroring that onto the slaves is how
// a dissolved-in-spirit group kept hijacking speakers (#342).
func masterMirrorSkipReason(np nowPlayingSnapshot, lastPlayURL string) string {
	switch {
	case np.Source == "":
		return "master state unreadable"
	case np.Source == "STANDBY":
		return "master in standby"
	case np.Location != lastPlayURL:
		return "master is on another source"
	case !isPlayingStatus(np.PlayStatus):
		return "master stream not playing (" + np.PlayStatus + ")"
	}
	return ""
}

// slaveMirrorAction decides whether the periodic reconcile (re)points one
// slave at the master's stream. np is the SLAVE box's live now_playing and
// slaveURL the LAN-reachable master stream URL (host = master's stream proxy).
// The reconcile only repairs the mirror: it never wakes a speaker from
// standby and never takes a speaker off another source it is playing — the
// user's direct action on a box always outranks the group (#342).
func slaveMirrorAction(np nowPlayingSnapshot, slaveURL string) (push bool, reason string) {
	masterHost := hostPortOf(slaveURL)
	switch {
	case np.Source == "":
		return false, "state unreadable"
	case np.Source == "STANDBY":
		return false, "in standby"
	case np.Location == slaveURL:
		if isPlayingStatus(np.PlayStatus) {
			return false, "already mirroring"
		}
		return true, "dropped off the mirror stream"
	case masterHost != "" && hostPortOf(np.Location) == masterHost:
		// Still pulling from the master's stream proxy, but an old stream (the
		// master changed station): re-point to the current one.
		return true, "on a stale master stream"
	case np.Source == "INVALID_SOURCE":
		// Awake but idle (nothing selected): joining the group is what the
		// user asked for when forming it.
		return true, "idle"
	default:
		return false, "on another source"
	}
}

// hostPortOf returns the host:port of a URL, or "" when it does not parse.
func hostPortOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// fetchNowPlaying reads a box's :8090/now_playing. Best-effort: any error
// yields an empty snapshot (Source == ""), which every caller treats as
// "state unreadable — do nothing".
func fetchNowPlaying(ctx context.Context, host string) nowPlayingSnapshot {
	var snap nowPlayingSnapshot
	b, err := boxGet(ctx, "http://"+host+":8090/now_playing", 16<<10)
	if err != nil {
		return snap
	}
	var np struct {
		Source      string `xml:"source,attr"`
		ContentItem struct {
			Location string `xml:"location,attr"`
			ItemName string `xml:"itemName"`
		} `xml:"ContentItem"`
		PlayStatus string `xml:"playStatus"`
	}
	if xml.Unmarshal(b, &np) == nil {
		snap.Source = np.Source
		snap.Location = np.ContentItem.Location
		snap.ItemName = np.ContentItem.ItemName
		snap.PlayStatus = np.PlayStatus
	}
	return snap
}

// logMirrorSkip logs a reconcile skip at INFO only when the reason for that
// key (master, or "slave <ip>") changed since the last tick, so a diagnostic
// bundle shows every state transition without the 5-minute steady state
// drowning the log. Only ever called from the single reconcile goroutine.
func (s *Server) logMirrorSkip(key, reason string) {
	if s.mirrorSkips == nil {
		s.mirrorSkips = make(map[string]string)
	}
	if s.mirrorSkips[key] == reason {
		s.logger.Debug("zone mirror: skip (unchanged)", "who", key, "reason", reason)
		return
	}
	s.mirrorSkips[key] = reason
	s.logger.Info("zone mirror: not pushing", "who", key, "reason", reason)
}

// clearMirrorSkip forgets a remembered skip reason once a push happens, so a
// later identical skip is logged again as a fresh transition.
func (s *Server) clearMirrorSkip(key string) {
	if s.mirrorSkips != nil {
		delete(s.mirrorSkips, key)
	}
}

// zoneReferences reports whether z includes the given speaker — by deviceID
// (case-insensitive) or by IP hint — as master or as a member. Used by the
// purge endpoint to decide whether a peer's dissolve concerns this box's
// persisted zone.
func zoneReferences(z zones.Zone, deviceID, ip string) bool {
	match := func(d, i string) bool {
		return (deviceID != "" && strings.EqualFold(d, deviceID)) ||
			(ip != "" && i != "" && i == ip)
	}
	if match(z.Master, z.MasterIP) {
		return true
	}
	for _, m := range z.Slaves {
		if match(m.DeviceID, m.IP) {
			return true
		}
	}
	return false
}

// handleZonePurge clears this box's persisted zone when it references the
// calling speaker. POST {"deviceID": "...", "ip": "..."}. A master that
// dissolves its group calls this on every member, so a stale zones.json on a
// member (including the mutual master-of-each-other corruption seen in #342)
// stops re-asserting a group the user tore down. Only the persisted store is
// touched; the firmware zone was already dissolved by the caller.
func (s *Server) handleZonePurge(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var req struct {
		DeviceID string `json:"deviceID"`
		IP       string `json:"ip"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024)).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.DeviceID == "" && req.IP == "" {
		http.Error(w, "deviceID or ip required", http.StatusBadRequest)
		return
	}
	cleared := false
	if s.zones != nil {
		if z, ok := s.zones.Get(); ok && zoneReferences(z, req.DeviceID, req.IP) {
			if err := s.zones.Clear(); err != nil {
				s.logger.Warn("zone purge: clear store failed", "err", err)
			} else {
				cleared = true
				s.logger.Info("zone purge: cleared persisted zone on request of a dissolving peer",
					"peerDeviceID", req.DeviceID, "peerIP", req.IP)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cleared": cleared})
}

// purgePeerZones asks every member of a just-dissolved group to drop any
// persisted zone that references this box (best-effort, in the background).
// Without this, a member that itself persisted a zone naming this box kept
// re-forming the group forever; the user dissolved it on one box and the
// other dragged everyone back in (#342).
func (s *Server) purgePeerZones(self boxapi.ZoneMember, members []boxapi.ZoneMember) {
	body, err := json.Marshal(map[string]string{"deviceID": self.DeviceID, "ip": self.IP})
	if err != nil {
		return
	}
	client := &http.Client{Timeout: 4 * time.Second}
	for _, m := range members {
		if m.IP == "" {
			continue
		}
		go func(ip string) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			// :17008 is the externally reachable agent entry on every chassis
			// (see mirrorStreamPort); plain :8888 covers agents where the
			// REDIRECT is not in place.
			for _, port := range []string{"17008", "8888"} {
				req, rerr := http.NewRequestWithContext(ctx, http.MethodPost,
					"http://"+net.JoinHostPort(ip, port)+"/api/box/zone/purge", strings.NewReader(string(body)))
				if rerr != nil {
					continue
				}
				req.Header.Set("Content-Type", "application/json")
				resp, derr := client.Do(req)
				if derr != nil {
					continue
				}
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					s.logger.Info("zone: peer's persisted zone purged", "peer", ip)
					return
				}
			}
			// Unreachable, no STR, or an agent from before this endpoint: the
			// mirror guards still keep a stale peer zone from hijacking anyone.
			s.logger.Warn("zone: could not purge peer's persisted zone (older agent or unreachable)", "peer", ip)
		}(m.IP)
	}
}
