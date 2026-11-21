package webui

// Copy this speaker's preset keys to other STR speakers, from the phone
// (backlog #1591, Ideas discussion #1780).
//
// Until now only the desktop app could do it: its "Transfer" button reads the
// source speaker's store, writes the set onto the target and moves the Spotify
// login along (desktop-app/app_presets.go, CopyPresetsAcrossBoxes). The phone
// remote talks to ONE speaker only. It cannot reach another speaker's agent
// (the CORS rule in server.go keeps a page from reading another origin, on
// purpose), so here the speaker the phone is on does the desktop app's part
// itself and pushes to the targets over the LAN.
//
// Nothing new happens on the targets. Each one gets exactly the requests the
// desktop app sends it, in the same order:
//
//  1. GET  /api/agent/version   is it an STR agent, how many rows does it keep
//  2. PUT  /api/presets          the whole set in one write (#882); a target
//     too old for it answers 404/405 and gets PUT /api/presets/<slot> per key
//  3. POST /api/box/sync-presets its hardware keys follow the copy
//  4. POST /spotify/credential   only when the set holds a Spotify key: this
//     speaker's login goes along, so those keys play there too
//
// The semantics are the desktop app's: all rows the target keeps go across
// and later rows are omitted when the target supports fewer, a station
// that already sits on a key the set does not rewrite makes the target refuse
// the whole set (409 already-on-slot, nothing written), and the Spotify login
// is best effort, so a failed login transfer never fails the copy.
//
// Rules of its own:
//
//   - Only speakers from the peer roster (the list the phone's speaker switcher
//     shows), only bare private LAN addresses, only the two agent ports, and
//     the fleet's guarded client (no loopback, no link-local, no redirects).
//     The phone names its targets by address; an address the roster does not
//     hold is reported back, never contacted.
//   - "All" means every roster speaker that answered the last probe sweep,
//     which is the list the phone offers.
//   - The run is detached from the phone's request: a phone that locks its
//     screen halfway does not leave a target with half a copy.
//   - One run at a time per speaker.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// presetCopyAPIPath is the phone's entry point. Outside the /api/presets/
// prefix on purpose, like /api/box/preset-move: an agent without it answers a
// plain 404 instead of reading "copy" as a slot.
const presetCopyAPIPath = "/api/box/preset-copy"

// Per-target outcome codes. The phone turns them into words.
const (
	presetCopyCodeUnreachable = "unreachable"     // no STR agent answered on either port
	presetCopyCodeConflict    = "already-on-slot" // the target refused the set, nothing was written
	presetCopyCodeRefused     = "refused"         // the target refused the set for another reason
	presetCopyCodeUnknown     = "unknown-speaker" // the address is not in the peer roster
)

// Request-level refusal codes.
const (
	presetCopyCodeInvalid   = "invalid"
	presetCopyCodeBusy      = "busy"
	presetCopyCodeNoPresets = "no-presets"
	presetCopyCodeNoTargets = "no-targets"
)

// What happened to the Spotify login on a target.
const (
	presetCopySpotifyCopied  = "copied"
	presetCopySpotifyNoLogin = "no-login" // this speaker holds no Spotify login to hand on
	presetCopySpotifyFailed  = "failed"
)

var (
	// presetCopySettleWindow bounds the wait for a target that does not answer
	// yet. The desktop app waits two minutes for a speaker that is still
	// booting after an update; the phone is a person holding a page, so the
	// wait is shorter and a target still asleep after it is reported as not
	// answering.
	presetCopySettleWindow = 30 * time.Second
	presetCopySettleStep   = 3 * time.Second
	// presetCopyWriteTimeout bounds the requests in which the target registers
	// its hardware keys before it answers: about 8 s for six keys on a
	// SoundTouch 20 (the desktop app's presetKeyWriteTimeout).
	presetCopyWriteTimeout = 30 * time.Second
	// presetCopyRunBudget bounds a whole run.
	presetCopyRunBudget = 3 * time.Minute
)

// presetCopyRequest is the phone's ask: some targets by address, or all.
type presetCopyRequest struct {
	Targets []string `json:"targets"`
	All     bool     `json:"all"`
}

// presetCopyConflict names the station that blocked the copy and the key it
// already sits on at the target.
type presetCopyConflict struct {
	Name string `json:"name"`
	Slot int    `json:"slot"`
}

// presetCopyResult is one target's outcome.
type presetCopyResult struct {
	Name   string `json:"name"`
	IP     string `json:"ip"`
	OK     bool   `json:"ok"`
	Copied int    `json:"copied"`
	// Row1Only stays as the existing response field name; it marks that one or
	// more extra rows exceeded the target's active row count. NotCopied is how many.
	Row1Only  bool                `json:"row1Only,omitempty"`
	NotCopied int                 `json:"notCopied,omitempty"`
	Spotify   string              `json:"spotify,omitempty"`
	Code      string              `json:"code,omitempty"`
	Conflict  *presetCopyConflict `json:"conflict,omitempty"`
	Error     string              `json:"error,omitempty"`
}

// presetCopyAnswer is the whole run.
type presetCopyAnswer struct {
	Keys    int                `json:"keys"`
	Rows    int                `json:"rows"`
	Done    int                `json:"done"`
	Total   int                `json:"total"`
	Results []presetCopyResult `json:"results"`
}

// presetCopyPeer is one roster speaker a copy may go to.
type presetCopyPeer struct {
	Name      string
	IP        string
	Port      int
	Reachable bool
}

// presetCopySet keeps the named keys up to maxSlot, in slot order: the same
// filter the desktop app applies (presetsUpTo).
func presetCopySet(all []presets.Preset, maxSlot int) []presets.Preset {
	var out []presets.Preset
	for _, p := range all {
		if p.Slot < 1 || p.Slot > maxSlot || strings.TrimSpace(p.Name) == "" {
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Slot < out[j].Slot })
	return out
}

func presetCopyHasSpotify(set []presets.Preset) bool {
	for _, p := range set {
		if p.Type == "spotify" {
			return true
		}
	}
	return false
}

// presetCopyRoster is the peer roster narrowed to what a copy may touch.
func (s *Server) presetCopyRoster(ctx context.Context) []presetCopyPeer {
	if s.peersFn == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []presetCopyPeer
	for _, p := range s.peersFn(ctx) {
		ip, port := fleetPeerAddr(p)
		if ip == "" || seen[ip] || !isLANPeer(ip) || (s.boxHost != "" && ip == s.boxHost) {
			continue
		}
		seen[ip] = true
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = ip
		}
		out = append(out, presetCopyPeer{Name: name, IP: ip, Port: port, Reachable: p.Reachable})
		if len(out) == fleetMaxPeers {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].IP < out[j].IP
	})
	return out
}

// pickPresetCopyTargets resolves the request against the roster. unknown holds
// the asked addresses the roster does not know; they are reported, never
// contacted.
func pickPresetCopyTargets(roster []presetCopyPeer, req presetCopyRequest) (targets []presetCopyPeer, unknown []string) {
	if req.All {
		for _, p := range roster {
			if p.Reachable {
				targets = append(targets, p)
			}
		}
		return targets, nil
	}
	byIP := make(map[string]presetCopyPeer, len(roster))
	for _, p := range roster {
		byIP[p.IP] = p
	}
	seen := map[string]bool{}
	for _, raw := range req.Targets {
		ip := strings.TrimSpace(raw)
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		if p, ok := byIP[ip]; ok {
			targets = append(targets, p)
		} else {
			unknown = append(unknown, ip)
		}
	}
	return targets, unknown
}

// presetCopyDo sends one request to a peer and returns the status, the
// content type and the start of the body.
func presetCopyDo(ctx context.Context, method, u, contentType string, body []byte, timeout time.Duration) (int, string, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, "", nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := fleetClient(timeout).Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, resp.Header.Get("Content-Type"), b, nil
}

// presetCopyFindAgent finds the target's agent and how many rows of keys it
// keeps. A target that does not answer is asked again for the settle window:
// "update the speaker, then put my stations on it" is the natural order, and a
// speaker that just rebooted answers pings before its agent listens.
func presetCopyFindAgent(ctx context.Context, p presetCopyPeer) (base string, rows int, err error) {
	deadline := time.Now().Add(presetCopySettleWindow)
	for {
		for _, port := range fleetPorts(p.Port) {
			b := fleetPeerBase(p.IP, port)
			var v struct {
				Version    string `json:"version"`
				PresetRows string `json:"presetRows"`
			}
			if _, gerr := fleetGetJSON(ctx, b+"/api/agent/version", 4*time.Second, &v); gerr != nil || v.Version == "" {
				continue
			}
			rows = 1
			if n, parseErr := strconv.Atoi(v.PresetRows); parseErr == nil && n >= 1 && n <= presets.Rows {
				rows = n
			}
			return b, rows, nil
		}
		if time.Now().Add(presetCopySettleStep).After(deadline) {
			return "", 0, errors.New("no STR agent answered")
		}
		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		case <-time.After(presetCopySettleStep):
		}
	}
}

// presetCopyCred hands out this speaker's Spotify login once per run.
type presetCopyCred struct {
	once   sync.Once
	export func() ([]byte, error)
	blob   []byte
}

func (c *presetCopyCred) get() []byte {
	c.once.Do(func() {
		if c.export == nil {
			return
		}
		if b, err := c.export(); err == nil && len(b) > 0 {
			c.blob = b
		}
	})
	return c.blob
}

// copyPresetsToPeer copies the set onto one target and says how it went.
func (s *Server) copyPresetsToPeer(ctx context.Context, p presetCopyPeer, src []presets.Preset, cred *presetCopyCred) presetCopyResult {
	res := presetCopyResult{Name: p.Name, IP: p.IP}
	base, rows, err := presetCopyFindAgent(ctx, p)
	if err != nil {
		res.Code, res.Error = presetCopyCodeUnreachable, err.Error()
		s.logger.Info("preset copy: the target did not answer, nothing was written", "peer", p.Name, "ip", p.IP, "err", err)
		return res
	}
	set := presetCopySet(src, rows*presets.BoxSlots)
	if dropped := len(src) - len(set); dropped > 0 {
		res.Row1Only, res.NotCopied = true, dropped
		s.logger.Info("preset copy: some extra rows exceed the target's active row count",
			"peer", p.Name, "notCopied", dropped)
	}
	if len(set) > 0 {
		body, err := json.Marshal(set)
		if err != nil {
			res.Code, res.Error = presetCopyCodeRefused, err.Error()
			return res
		}
		code, _, rb, err := presetCopyDo(ctx, http.MethodPut, base+"/api/presets", "application/json", body, presetCopyWriteTimeout)
		if err != nil {
			// A speaker can go away during the copy. One re-wait and one retry
			// costs nothing on the happy path, same as the desktop app.
			if b2, _, werr := presetCopyFindAgent(ctx, p); werr == nil {
				base = b2
				code, _, rb, err = presetCopyDo(ctx, http.MethodPut, base+"/api/presets", "application/json", body, presetCopyWriteTimeout)
			}
		}
		switch {
		case err != nil:
			res.Code, res.Error = presetCopyCodeUnreachable, err.Error()
			s.logger.Warn("preset copy: the target went away during the copy", "peer", p.Name, "err", err)
			return res
		case code == http.StatusNotFound || code == http.StatusMethodNotAllowed:
			s.logger.Info("preset copy: the target has no bulk preset endpoint, copying key by key", "peer", p.Name)
			copied, errs := s.presetCopyPerSlot(ctx, base, set)
			res.Copied = copied
			if len(errs) > 0 {
				res.Code, res.Error = presetCopyCodeRefused, strings.Join(errs, "; ")
			}
		case code == http.StatusConflict:
			var c struct {
				Code string `json:"code"`
				Name string `json:"name"`
				Slot int    `json:"slot"`
			}
			if json.Unmarshal(rb, &c) == nil && c.Code == presetCopyCodeConflict {
				res.Code = presetCopyCodeConflict
				res.Conflict = &presetCopyConflict{Name: c.Name, Slot: c.Slot}
			} else {
				res.Code, res.Error = presetCopyCodeRefused, shortBody(string(rb))
			}
			s.logger.Info("preset copy: the target refused the whole set, nothing was written",
				"peer", p.Name, "code", res.Code, "name", c.Name, "slot", c.Slot)
			return res
		case code >= 400:
			res.Code, res.Error = presetCopyCodeRefused, fmt.Sprintf("status %d: %s", code, shortBody(string(rb)))
			s.logger.Warn("preset copy: the target refused the set", "peer", p.Name, "status", code, "body", shortBody(string(rb)))
			return res
		default:
			res.Copied = len(set)
		}
	}
	// The target's hardware keys 1-6 follow the copy (the keys pressed twice
	// live in its agent's store only). Best effort, like the desktop app.
	if code, _, _, err := presetCopyDo(ctx, http.MethodPost, base+"/api/box/sync-presets", "application/json", nil, presetCopyWriteTimeout); err != nil || code >= 400 {
		s.logger.Warn("preset copy: the target's hardware key sync failed", "peer", p.Name, "status", code, "err", err)
	}
	if presetCopyHasSpotify(set) {
		res.Spotify = s.presetCopySpotifyLogin(ctx, p, base, cred)
	}
	res.OK = res.Code == ""
	s.logger.Info("preset copy: done", "peer", p.Name, "ip", p.IP, "copied", res.Copied,
		"row1Only", res.Row1Only, "spotify", res.Spotify, "code", res.Code)
	return res
}

// presetCopyPerSlot is the key-by-key write for a target whose agent predates
// the bulk endpoint. A refused key is reported and the others still go.
func (s *Server) presetCopyPerSlot(ctx context.Context, base string, set []presets.Preset) (int, []string) {
	copied := 0
	var errs []string
	for _, p := range set {
		body, err := json.Marshal(p)
		if err != nil {
			errs = append(errs, fmt.Sprintf("key %d: %v", p.Slot, err))
			continue
		}
		code, _, rb, err := presetCopyDo(ctx, http.MethodPut, base+"/api/presets/"+strconv.Itoa(p.Slot), "application/json", body, presetCopyWriteTimeout)
		if err != nil || code >= 400 {
			if err == nil {
				err = fmt.Errorf("status %d: %s", code, shortBody(string(rb)))
			}
			errs = append(errs, fmt.Sprintf("key %d (%s): %v", p.Slot, p.Name, err))
			continue
		}
		copied++
	}
	return copied, errs
}

// presetCopySpotifyLogin hands this speaker's Spotify login to the target, so
// the Spotify keys just copied play there. Best effort by contract.
func (s *Server) presetCopySpotifyLogin(ctx context.Context, p presetCopyPeer, base string, cred *presetCopyCred) string {
	blob := cred.get()
	if len(blob) == 0 {
		s.logger.Info("preset copy: no Spotify login on this speaker to hand on", "peer", p.Name)
		return presetCopySpotifyNoLogin
	}
	code, _, _, err := presetCopyDo(ctx, http.MethodPost, base+"/spotify/credential", "application/octet-stream", blob, presetCopyWriteTimeout)
	if err != nil || code >= 400 {
		s.logger.Warn("preset copy: the target did not take the Spotify login", "peer", p.Name, "status", code, "err", err)
		return presetCopySpotifyFailed
	}
	s.logger.Info("preset copy: Spotify login handed on with the keys", "peer", p.Name)
	return presetCopySpotifyCopied
}

// runPresetCopy copies src onto every target at once (they are different
// speakers and the payloads are small) and keeps the results in target order.
func (s *Server) runPresetCopy(ctx context.Context, targets []presetCopyPeer, src []presets.Preset) []presetCopyResult {
	cred := &presetCopyCred{export: s.spotifyExportCred}
	out := make([]presetCopyResult, len(targets))
	var wg sync.WaitGroup
	for i, p := range targets {
		wg.Add(1)
		go func(i int, p presetCopyPeer) {
			defer wg.Done()
			out[i] = s.copyPresetsToPeer(ctx, p, src, cred)
		}(i, p)
	}
	wg.Wait()
	return out
}

// POST /api/box/preset-copy {"targets":["192.0.2.51"]} or {"all":true}
//
// Answers 200 with one result per target (presetCopyAnswer) once every target
// is done, also when some or all of them failed: the phone shows each one.
func (s *Server) handlePresetCopy(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "preset copy only allowed from LAN", http.StatusForbidden)
		return
	}
	if s.presets == nil {
		http.Error(w, "presets store not initialized", http.StatusServiceUnavailable)
		return
	}
	var req presetCopyRequest
	if !decodeJSONRequest(w, r, 8<<10, &req) {
		return
	}
	if !req.All && len(req.Targets) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "name the targets or ask for all", "code": presetCopyCodeInvalid})
		return
	}
	if len(req.Targets) > fleetMaxPeers {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "too many targets", "code": presetCopyCodeInvalid})
		return
	}
	src := presetCopySet(s.presets.All(), s.PresetRows()*presets.BoxSlots)
	if len(src) == 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "this speaker has no keys to copy", "code": presetCopyCodeNoPresets})
		return
	}
	if !s.presetCopyMu.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "a copy is already running on this speaker", "code": presetCopyCodeBusy})
		return
	}
	defer s.presetCopyMu.Unlock()
	// Detached: the copy finishes even when the phone locks its screen or
	// leaves the page, so no target is left with half of it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), presetCopyRunBudget)
	defer cancel()
	targets, unknown := pickPresetCopyTargets(s.presetCopyRoster(ctx), req)
	if len(targets) == 0 && len(unknown) == 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "no other speaker to copy to", "code": presetCopyCodeNoTargets})
		return
	}
	s.logger.Info("preset copy: started from the phone", "keys", len(src), "targets", len(targets),
		"unknown", len(unknown), "all", req.All, "from", r.RemoteAddr)
	results := s.runPresetCopy(ctx, targets, src)
	for _, ip := range unknown {
		s.logger.Info("preset copy: asked for a speaker the peer list does not hold, not contacted", "ip", ip)
		results = append(results, presetCopyResult{Name: ip, IP: ip, Code: presetCopyCodeUnknown})
	}
	ans := presetCopyAnswer{Keys: len(src), Rows: s.PresetRows(), Total: len(results), Results: results}
	for _, res := range results {
		if res.OK {
			ans.Done++
		}
	}
	s.logger.Info("preset copy: finished", "done", ans.Done, "total", ans.Total)
	writeJSON(w, http.StatusOK, ans)
}
