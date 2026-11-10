// Stereo pairs: the /api/box/zone method switch, the pair name, the
// firmware's own group read, the marge pair document and its relay to the
// partner, and the cleanup of a stale pair.
//
// This file used to hold all of multiroom as well. It was split by topic;
// the rest lives in zones_get.go, zones_balance.go, zones_form.go,
// zones_form_native.go, zones_afterform.go, zones_followers.go,
// zones_stereo_pair.go, zones_reconcile.go and zones_dissolve.go.
// handleSpotifyCredential has nothing to do with groups; it was in this file
// before the split and stays here.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// handleBoxZone serves the SoundTouch multiroom zone (#70, BETA):
//
//	GET    -> the live zone the box reports {"master","senderIP","members"[]}
//	POST   -> form/replace a zone with THIS box as master (body: master + slaves)
//	DELETE -> dissolve the zone this box leads
//
// POST/DELETE also persist to the zones store so the zone auto-reforms after a
// reboot/standby/Wi-Fi outage without the user re-grouping. This is the blind
// beta path: it drives the native Bose /setZone family directly and logs every
// step (master, slaves, the firmware's read-back) into agent.log so multi-speaker
// testers' diagnostic bundles show exactly what the firmware did.
func (s *Server) handleBoxZone(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleZoneGet(w, r)
	case http.MethodPost:
		s.handleZoneForm(w, r)
	case http.MethodDelete:
		s.handleZoneDissolve(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleStereoName renames the stored stereo pair (POST {"name": "..."}). The
// name lives in the marge group record, which is the same place the Bose app
// reads and edits, so one rename shows up in STR and the Bose app alike. The
// desktop app calls this on BOTH members (their marges each hold the pair
// document; agent-to-agent HTTP is blocked between series-I boxes).
func (s *Server) handleStereoName(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.margeGroupRename == nil {
		http.Error(w, "stereo rename not wired", http.StatusNotImplemented)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024)).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.margeGroupRename(req.Name); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// The marge record moves STR's own Spotify entry; the speaker's native
	// "(L+R)" entry only moves when the firmware is told (#1752). After the
	// marge write, never before: the firmware answers /updateGroup with a
	// round trip to marge, and a canonical record still carrying the old name
	// would be echoed straight back to it.
	// Detached from the request: a client that stops waiting must not abort
	// the firmware half of a rename whose marge half already happened. The
	// calls carry their own budgets.
	fw := s.renamePairOnBoxFirmware(context.WithoutCancel(r.Context()), req.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "firmware": fw})
}

// handleBoxGroup reads the box's current stereo pair group.
// Read-only. Response is {"id":"...","name":"...","members":[...]}.
// For a box without a pair, id is empty and members is empty.
func (s *Server) handleBoxGroup(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	g, err := c.GetGroup(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// handleMargeGroupDoc exposes this box's marge stereo-pair record so both
// members of a pair can be kept on ONE canonical document. GET returns the
// stored record; POST installs a canonical document (body = the group XML);
// DELETE clears the record (dissolve). The desktop app is the usual caller:
// it relays the master's document to the partner because agent-to-agent HTTP
// is blocked between series-I boxes.
func (s *Server) handleMargeGroupDoc(w http.ResponseWriter, r *http.Request) {
	if s.margeGroupGet == nil || s.margeGroupSet == nil || s.margeGroupClear == nil {
		http.Error(w, "marge group bridge not wired", http.StatusNotImplemented)
		return
	}
	switch r.Method {
	case http.MethodGet:
		xmlDoc, canonical, ok := s.margeGroupGet()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no pair record stored"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "xml": xmlDoc, "canonical": canonical})
	case http.MethodPost, http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil || len(bytes.TrimSpace(body)) == 0 {
			http.Error(w, "empty group document", http.StatusBadRequest)
			return
		}
		if err := s.margeGroupSet(string(body)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.logger.Info("stereo: canonical pair document installed on this marge (relay)")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case http.MethodDelete:
		s.margeGroupClear("relay dissolve")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// healStaleStereoGroups clears a stale stereo pair from both speakers'
// firmwares (and this box's marge record) so a fresh /addGroup can proceed.
// The partner's firmware API (:8090) is reachable even between series-I boxes;
// only the agent ports are firewalled there.
func (s *Server) healStaleStereoGroups(ctx context.Context, c *boxapi.Client, partner boxapi.ZoneMember) {
	s.logger.Warn("stereo: firmware reports GROUP_ALREADY_EXISTS (5510), clearing the stale pair on both speakers and retrying")
	if g, err := c.GetGroup(ctx); err == nil && (g.ID != "" || len(g.Members) > 0) {
		s.logger.Info("stereo: stale pair on this speaker", "id", g.ID, "master", g.MasterDeviceID, "members", len(g.Members))
	}
	if err := c.RemoveGroup(ctx); err != nil {
		s.logger.Warn("stereo: stale-pair removeGroup failed on this speaker", "err", err)
	}
	if partner.IP != "" {
		pc := boxapi.New(partner.IP)
		if pg, err := pc.GetGroup(ctx); err == nil && (pg.ID != "" || len(pg.Members) > 0) {
			s.logger.Info("stereo: stale pair on the partner", "id", pg.ID, "master", pg.MasterDeviceID, "members", len(pg.Members))
			if err := pc.RemoveGroup(ctx); err != nil {
				s.logger.Warn("stereo: stale-pair removeGroup failed on the partner", "err", err, "partnerIP", partner.IP)
			}
		}
	}
	if s.margeGroupClear != nil {
		s.margeGroupClear("stale pair heal (5510)")
	}
	// Give the firmware a moment to settle the teardown before the retry; a
	// back-to-back addGroup right after removeGroup has been seen to 500.
	select {
	case <-ctx.Done():
	case <-time.After(1200 * time.Millisecond):
	}
}

// pushGroupDocToPartner installs (doc != "") or clears (doc == "") the
// canonical pair document on the partner's marge via its agent. Best-effort:
// between series-I boxes the agent port is firewalled and the desktop app
// relays instead (the caller reports that via partnerMargeSynced/-Cleared).
func (s *Server) pushGroupDocToPartner(ctx context.Context, partnerIP, doc string) bool {
	if partnerIP == "" {
		return false
	}
	// The partner of a stereo pair is on the local network, always. Requiring
	// that keeps a caller from pointing this push at an address off the LAN:
	// the path and the two ports are fixed, so the reach was already narrow,
	// but "narrow" is not a reason to leave it open.
	if !isPrivateHost(partnerIP) {
		s.logger.Warn("stereo: refusing to push the pair document off the local network", "partnerIP", partnerIP)
		return false
	}
	// Short per-port budget: on series-I (the only stereo hardware) a blocked
	// agent port black-holes the SYN, and this push runs inside the pairing
	// response — an open LAN port answers in milliseconds.
	client := &http.Client{Timeout: 800 * time.Millisecond}
	for _, port := range []string{"8888", "17008"} {
		url := "http://" + net.JoinHostPort(partnerIP, port) + "/api/marge/group"
		var req *http.Request
		var err error
		if doc == "" {
			req, err = http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
		} else {
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(doc))
			if req != nil {
				req.Header.Set("Content-Type", "application/xml")
			}
		}
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		// An agent that predates /api/marge/group answers via its catch-all
		// index: 200 + text/html. Only the real endpoint's JSON counts, or a
		// "success" here would suppress the desktop app's relay while the
		// partner stored nothing.
		ok := resp.StatusCode == http.StatusOK &&
			strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json")
		tooOld := resp.StatusCode == http.StatusOK && !ok
		resp.Body.Close()
		if ok {
			s.logger.Info("stereo: partner marge updated directly", "partnerIP", partnerIP, "port", port, "cleared", doc == "")
			return true
		}
		if tooOld {
			s.logger.Warn("stereo: partner agent predates the pair-document relay (answered HTML), update the partner speaker", "partnerIP", partnerIP, "port", port)
			return false
		}
	}
	s.logger.Info("stereo: partner marge not reachable directly (expected between series-I speakers), the desktop app will relay", "partnerIP", partnerIP)
	return false
}

// isPrivateHost reports whether host is a literal address on this machine's own
// network. A name is refused rather than resolved: resolution here would be a
// second chance to point the push somewhere else.
func isPrivateHost(host string) bool {
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// handleSpotifyCredential moves the go-librespot Spotify login between speakers
// (#45): GET returns this box's active credential blob, POST installs a blob
// exported from another box and restarts go-librespot to log in with it. LAN-only,
// same trust model as the rest of the agent API; the blob is a reusable Spotify
// Connect credential, so the desktop app should only move it between the user's
// own speakers.
func (s *Server) handleSpotifyCredential(w http.ResponseWriter, r *http.Request) {
	// The comment above has said "LAN-only" since this was written and the
	// handler never checked. It does now. The CORS narrowing is what actually
	// closes the drive-by read (a browser on the LAN passes this check), but a
	// reusable account credential should not be one middleware away from
	// anyone who can route to the speaker.
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "spotify credential is LAN only", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if s.spotifyExportCred == nil {
			http.Error(w, "spotify not configured", http.StatusServiceUnavailable)
			return
		}
		data, err := s.spotifyExportCred()
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "no spotify login stored on this speaker", "detail": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	case http.MethodPost:
		if s.spotifyImportCred == nil {
			http.Error(w, "spotify not configured", http.StatusServiceUnavailable)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256*1024))
		if err != nil || len(data) == 0 {
			http.Error(w, "empty or oversized credential", http.StatusBadRequest)
			return
		}
		if err := s.spotifyImportCred(r.Context(), data); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": "import failed", "detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
