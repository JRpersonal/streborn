package main

// This file was split out of app.go (wave-1 move-only refactor):
// multiroom zones, stereo pairs, and cross-box Spotify login sync.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---- Multiroom zone (#70, BETA) ----

// ZoneMember is a speaker in a multiroom zone: its stable deviceID and LAN IP.
type ZoneMember struct {
	DeviceID string `json:"deviceID"`
	IP       string `json:"ip"`
}

// ZoneSpec is the form-a-zone request the desktop sends to the master's agent.
type ZoneSpec struct {
	Master ZoneMember   `json:"master"`
	Slaves []ZoneMember `json:"slaves"`
	Name   string       `json:"name"`
	Stereo bool         `json:"stereo"`
	// Mode is "native" (firmware sync) or "mirror" (each speaker pulls the same
	// stream). Empty defaults to native on the agent.
	Mode string `json:"mode"`
}

// GetZoneState reads the live multiroom zone the speaker reports
// (GET /api/box/zone) -> {master, senderIP, members[]}. Self-heals across
// :8888/:17008 like the other box calls.
func (a *App) GetZoneState(host string, port int) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/zone", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// memberReadiness is the result of the pre-form readiness gate: which members
// answered their STR agent (reachable + /api/agent/version) within the budget,
// and which were still mid-restart and not safe to enroll. NotReady carries the
// LAN IPs so the UI can name the speaker that is still starting (#70: a member
// that had only been up ~57s after an OTA was enrolled into a zone it then never
// joined, leaving a silently-incomplete group).
type memberReadiness struct {
	Ready    []string
	NotReady []string
}

// strInSlice reports whether v is in s.
func strInSlice(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// ensureZoneMembersReady probes the master and every slave IP for a live STR
// agent before a zone is formed. It reuses probeSTRWithRetry, the same probe
// discovery uses, so a box that is briefly busy right after an OTA reboot gets a
// few attempts to answer (a member that has only been up ~57s, its agent still
// starting). A member that answers is "ready"; one that never does is reported
// rather than enrolled, so the caller can form the group with only the ready
// members and tell the user which speaker is still starting. Empty IPs are
// skipped (cannot be probed) and left for the caller to pass through.
func (a *App) ensureZoneMembersReady(ips []string) memberReadiness {
	var res memberReadiness
	for _, ip := range ips {
		if ip == "" {
			continue
		}
		// ~8 s budget per member; a ready box answers on the first attempt in
		// well under a second, so the common case adds almost no latency.
		ctx, cancel := context.WithTimeout(a.appCtx(), 8*time.Second)
		_, ok := probeSTRWithRetry(ctx, ip, 3)
		cancel()
		if ok {
			res.Ready = append(res.Ready, ip)
		} else {
			a.logger.Warn("zone: member not STR-ready, will not enroll it (mid-restart?)", "ip", ip)
			res.NotReady = append(res.NotReady, ip)
		}
	}
	return res
}

// FormZone forms (or replaces) a multiroom zone with masterHost as the master and
// the given slaves (#70 beta). POSTed to the master's agent, which drives the
// native Bose /setZone and persists it so the zone auto-reforms after a reboot.
func (a *App) FormZone(masterHost string, masterPort int, spec ZoneSpec) (result map[string]any, err error) {
	// Log every attempt + outcome here on the app side: the agent logs the
	// firmware /setZone & /addGroup responses on the box, but a remote user's
	// diagnostic bundle ships only app.log, so without this an alpha stereo-pair
	// failure (e.g. the firmware refusing /addGroup) left no trace at all. The
	// error returned to the frontend already carries the agent's "addGroup: ..."
	// / "setZone: ..." text, so this records the real firmware reason.
	a.logger.Info("FormZone: forming (stereo=alpha, zone=beta)", "masterHost", masterHost,
		"master", spec.Master.DeviceID, "masterIP", spec.Master.IP, "slaves", len(spec.Slaves),
		"stereo", spec.Stereo, "mode", spec.Mode)
	defer func() {
		if err != nil {
			a.logger.Warn("FormZone: failed", "stereo", spec.Stereo, "master", spec.Master.DeviceID, "err", err)
		} else {
			a.logger.Info("FormZone: done", "stereo", spec.Stereo, "master", spec.Master.DeviceID, "result", result)
		}
	}()
	if spec.Master.DeviceID == "" || len(spec.Slaves) == 0 {
		return nil, fmt.Errorf("a master and at least one slave are required")
	}
	// Readiness gate (#70): never form a zone against a member whose STR agent is
	// still starting. The master must be ready to drive /setZone at all; a slave
	// that is mid-restart would be silently dropped by the firmware, leaving an
	// incomplete group the user thinks succeeded.
	ips := make([]string, 0, len(spec.Slaves)+1)
	ips = append(ips, spec.Master.IP)
	for _, sl := range spec.Slaves {
		ips = append(ips, sl.IP)
	}
	readiness := a.ensureZoneMembersReady(ips)
	if spec.Master.IP != "" && !strInSlice(readiness.Ready, spec.Master.IP) {
		return nil, fmt.Errorf("box_not_ready: master")
	}
	// Drop slaves that are not ready from this attempt but report them, so the UI
	// can name the speaker that is still starting. The agent-side zone reconcile
	// and the next discovery cycle pick them up once they answer. A slave with no
	// known IP cannot be probed and is passed through unchanged.
	notReady := make([]string, 0)
	readySlaves := make([]ZoneMember, 0, len(spec.Slaves))
	for _, sl := range spec.Slaves {
		if sl.IP == "" || strInSlice(readiness.Ready, sl.IP) {
			readySlaves = append(readySlaves, sl)
		} else {
			notReady = append(notReady, sl.IP)
		}
	}
	spec.Slaves = readySlaves
	if len(spec.Slaves) == 0 {
		return map[string]any{"ok": false, "notReady": notReady}, nil
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	// Zone forming needs its own budget: the agent wakes the speaker first, which
	// can take most of the default 6 s on its own, and a timeout here leaves the
	// user with a failure message for a group the box actually went on to form.
	resp, err := a.boxDoTimeout(masterHost, masterPort, http.MethodPost, "/api/box/zone", "application/json", string(b), zoneCallTimeout)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out == nil {
		out = map[string]any{}
	}
	if len(notReady) > 0 {
		out["notReady"] = notReady
	}
	// Stereo pairs: the master's agent installs ONE canonical pair document on
	// both members' marges so the RIGHT box cannot re-create the record with
	// itself as master (the divergence that desyncs pairs after standby). The
	// agent cannot reach the partner's agent between series-I speakers (their
	// firewall drops agent-to-agent HTTP), so when it reports the direct push
	// failed, the app relays the document — the PC reaches every agent.
	if spec.Stereo {
		a.relayStereoGroupDoc(out)
	}
	return out, nil
}

// relayStereoGroupDoc pushes the canonical stereo-pair document (returned by
// the master agent's pairing response) to the partner's agent when the master
// could not deliver it directly. Best-effort: the pair itself already formed;
// a failed relay only leaves the partner's marge record uncorrected until the
// next pairing action or app-driven repair.
func (a *App) relayStereoGroupDoc(out map[string]any) {
	synced, _ := out["partnerMargeSynced"].(bool)
	doc, _ := out["canonicalGroup"].(string)
	partnerIP, _ := out["partnerIP"].(string)
	if synced || doc == "" || partnerIP == "" {
		return
	}
	resp, err := a.boxDo(partnerIP, 0, http.MethodPost, "/api/marge/group", "application/xml", doc)
	if err != nil {
		a.logger.Warn("stereo: relaying the pair document to the partner agent failed", "partnerIP", partnerIP, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		a.logger.Warn("stereo: partner agent rejected the pair document relay", "partnerIP", partnerIP, "status", resp.StatusCode)
		return
	}
	// An agent that predates /api/marge/group answers via its catch-all index:
	// 200 + text/html. Only the endpoint's JSON reply proves the record landed
	// (same false-success class as the /spotify/credential path fix).
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		a.logger.Warn("stereo: partner agent is too old for the pair-document relay (answered non-JSON), update the partner speaker", "partnerIP", partnerIP, "contentType", ct)
		return
	}
	a.logger.Info("stereo: pair document relayed to the partner agent", "partnerIP", partnerIP)
	out["partnerMargeSynced"] = true
}

// DissolveZone tears down the multiroom zone led by masterHost (#70 beta).
// Logged with the outcome: group bugs reported from the field were previously
// undiagnosable because the app log never said which zone operations ran.
func (a *App) DissolveZone(masterHost string, masterPort int) error {
	// Same budget as forming: dissolving also wakes the members first, and a
	// timeout here is what leaves a half-dissolved group behind (#442).
	resp, err := a.boxDoTimeout(masterHost, masterPort, http.MethodDelete, "/api/box/zone", "", "", zoneCallTimeout)
	if err != nil {
		a.logger.Info("zone: dissolve failed", "master", masterHost, "err", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		herr := readHTTPError(resp)
		a.logger.Info("zone: dissolve rejected", "master", masterHost, "err", herr)
		return herr
	}
	// A dissolved stereo pair must also drop the partner's marge pair record;
	// the agent tries directly and reports whether the app needs to relay the
	// delete (series-I speakers cannot reach each other's agents).
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	a.relayStereoPairClear(out)
	a.logger.Info("zone: dissolved", "master", masterHost)
	return nil
}

// relayStereoPairClear relays the pair-record DELETE to the partner's agent
// when a stereo dissolve response says the agent could not clear it directly.
func (a *App) relayStereoPairClear(out map[string]any) {
	if stereo, _ := out["stereo"].(bool); !stereo {
		return
	}
	if cleared, _ := out["partnerMargeCleared"].(bool); cleared {
		return
	}
	partnerIP, _ := out["partnerIP"].(string)
	if partnerIP == "" {
		return
	}
	dresp, derr := a.boxDo(partnerIP, 0, http.MethodDelete, "/api/marge/group", "", "")
	switch {
	case derr != nil:
		a.logger.Warn("stereo: clearing the partner's pair record failed", "partnerIP", partnerIP, "err", derr)
	// The catch-all index of a pre-relay agent answers 200+HTML; only the
	// endpoint's JSON reply proves the clear happened.
	case dresp.StatusCode != http.StatusOK,
		!strings.HasPrefix(dresp.Header.Get("Content-Type"), "application/json"):
		a.logger.Warn("stereo: partner agent did not confirm the pair-record clear (too old, or an error)",
			"partnerIP", partnerIP, "status", dresp.StatusCode, "contentType", dresp.Header.Get("Content-Type"))
		dresp.Body.Close()
	default:
		dresp.Body.Close()
		a.logger.Info("stereo: pair record cleared on the partner agent (relay)", "partnerIP", partnerIP)
	}
}

// DissolveStereoPair undoes a stereo pair from either member. It is the
// stereo-intent variant of DissolveZone: the agent only escalates a dissolve
// to the firmware's /getGroup teardown when the caller explicitly asked for a
// pair to be undone (?stereo=1), so a plain multiroom dissolve can never
// destroy a stereo pair as a side effect.
func (a *App) DissolveStereoPair(host string, port int) error {
	resp, err := a.boxDoTimeout(host, port, http.MethodDelete, "/api/box/zone?stereo=1", "", "", zoneCallTimeout)
	if err != nil {
		a.logger.Info("stereo: dissolve failed", "host", host, "err", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		herr := readHTTPError(resp)
		a.logger.Info("stereo: dissolve rejected", "host", host, "err", herr)
		return herr
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	// The agent answers 200 {"ok":true,"nothing":true} when the speaker was not
	// in a pair at all. Reporting that as "pair undone" is how a user came to
	// press undo twice, get a success message twice, and still find the pair
	// intact in the Bose app: both calls had gone to an uninvolved speaker
	// (field, 2026-08-04). Say what actually happened.
	if nothing, _ := out["nothing"].(bool); nothing {
		a.logger.Info("stereo: nothing to dissolve, this speaker is not in a pair", "host", host)
		return fmt.Errorf("stereo-not-paired")
	}
	a.relayStereoPairClear(out)
	a.logger.Info("stereo: pair dissolved", "host", host)
	return nil
}

// SpotifySyncTarget is one speaker to copy the Spotify login TO.
type SpotifySyncTarget struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	Name string `json:"name"`
}

// SyncSpotifyLogin copies the go-librespot Spotify credential from whichever
// speaker is already logged into Spotify to all the others, so the user logs in
// ONCE and recall then works on every speaker (#45 root cause: a saved Spotify
// preset with account="" because the box was never logged in). It auto-detects
// the source (the first speaker that returns a stored credential) so the user
// only taps one button. The credential moves only between the user's own
// discovered speakers, over the LAN, never off-device.
func (a *App) SyncSpotifyLogin(boxes []SpotifySyncTarget) (map[string]any, error) {
	var cred []byte
	var sourceHost, sourceName string
	for _, b := range boxes {
		if b.Host == "" {
			continue
		}
		resp, err := a.boxDo(b.Host, b.Port, http.MethodGet, "/spotify/credential", "", "")
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		ok := resp.StatusCode == http.StatusOK
		resp.Body.Close()
		if ok && len(data) > 0 {
			cred = data
			sourceHost = b.Host
			sourceName = b.Name
			if sourceName == "" {
				sourceName = b.Host
			}
			break
		}
	}
	if len(cred) == 0 {
		return nil, fmt.Errorf("no speaker is logged into Spotify yet. Log one speaker into Spotify first (pick it in the Spotify app and play a track), then sync")
	}
	synced := make([]string, 0, len(boxes))
	failed := map[string]string{}
	for _, b := range boxes {
		if b.Host == "" || b.Host == sourceHost {
			continue
		}
		label := b.Name
		if label == "" {
			label = b.Host
		}
		r2, err := a.boxDo(b.Host, b.Port, http.MethodPost, "/spotify/credential", "application/octet-stream", string(cred))
		if err != nil {
			failed[label] = err.Error()
			continue
		}
		ok := r2.StatusCode == http.StatusOK
		r2.Body.Close()
		if ok {
			synced = append(synced, label)
		} else {
			failed[label] = r2.Status
		}
	}
	a.logger.Info("spotify: synced login to speakers", "source", sourceName, "synced", len(synced), "failed", len(failed))
	return map[string]any{"source": sourceName, "synced": synced, "failed": failed}, nil
}
