// Box settings endpoints proxied to the Bose API.

package webui

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/wlanlive"
)

// guessErrorReason converts the technical UPnP / network error into a
// human-readable hint. The box's SOAP responses are heavily wrapped in
// XML and not directly understandable.
func guessErrorReason(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "402") || strings.Contains(s, "No URI"):
		return "The stream could not be loaded. Some stations serve playlist files (.pls/.m3u) or HTTPS streams that the speaker cannot play directly. Try a different station."
	case strings.Contains(s, "no such host") || strings.Contains(s, "lookup"):
		return "Could not reach the stream URL server."
	case strings.Contains(s, "timeout"):
		return "Speaker did not respond. It may be in standby — try again."
	case strings.Contains(s, "connection refused"):
		return "Speaker refused the connection."
	default:
		return s
	}
}

// isGroupedRejection reports whether a UPnP play failure is the box refusing
// transport control because it is currently a FOLLOWER in a multiroom zone /
// stereo group: the firmware answers SetAVTransportURI with UPnP error 501
// "Can't control member of group" (#70). Matched on the fault description
// (the code 501 alone is the generic "Action Failed").
func isGroupedRejection(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "member of group")
}

// isWrongStateRejection reports whether a play failure is the box refusing
// SetAVTransportURI because its OWN transport is in the wrong state: the
// firmware answers UPnP 501 "Action request came in wrong state" (also seen as
// UpnpRcvdContentItemInWrongState) while it still holds a ContentItem it cannot
// activate - a dead-cloud item, or the teardown of a recall that has not
// finished yet.
//
// Matched on the description, never on the code: this firmware answers 501 for
// several unrelated conditions, and the group refusal above is the other one.
//
// Both spellings count. The SOAP fault says "Action request came in wrong
// state" while the box's own gabbo frames call the same condition
// UpnpRcvdContentItemInWrongState, and an error can reach here carrying either.
func isWrongStateRejection(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "wrong state") || strings.Contains(s, "wrongstate")
}

// clearTransportForReplay forces the box's UPnP transport out of a stuck
// wrong-state: a Stop followed by an empty SetAVTransportURI so the firmware
// releases the ContentItem it keeps trying to self-activate. Best-effort, both
// calls are advisory and a wedged renderer may ACK them without acting, so
// neither error is fatal. Mirrors the hardware-recall repair in cmd/agent
// (clearTransportForRePush).
func (s *Server) clearTransportForReplay(ctx context.Context) {
	if s.renderer == nil {
		return
	}
	if err := s.renderer.Stop(ctx); err != nil {
		s.logger.Debug("wrong-state repair: transport stop returned (expected when nothing is playing)", "err", err)
	}
	if err := s.renderer.ClearURI(ctx); err != nil {
		s.logger.Debug("wrong-state repair: clear transport URI returned", "err", err)
	}
}

// playWithWrongStateRepair pushes a stream to the box and, when the box refuses
// it because its transport sits in the wrong state, empties that transport and
// pushes once more.
//
// Why this exists: the identical repair already ran for HARDWARE key presses
// (cmd/agent verifyPlayURL), but a play started from the app went straight out
// as SetAVTransportURI + Play and the raw SOAP fault was handed to the user.
// Field report 2026-08-05 (DLF Nova, v0.9.34): the first Play answered 501
// "Action request came in wrong state" and the speaker stayed silent, the
// second attempt played. That is exactly the state a Stop + ClearURI clears, so
// the user should not have to be the retry loop.
//
// One retry, not a loop: if an emptied transport still refuses, the cause is
// not the stuck ContentItem and hammering it would only delay the error the
// caller needs to show.
func (s *Server) playWithWrongStateRepair(ctx context.Context, url, title, art, mime string) error {
	push := func() error {
		if mime != "" {
			return s.renderer.PlayURLMime(ctx, url, title, art, mime)
		}
		return s.renderer.PlayURL(ctx, url, title, art)
	}
	err := push()
	if !isWrongStateRejection(err) {
		return err
	}
	s.logger.Warn("play: the speaker refused the stream in a wrong transport state, clearing the transport and pushing again",
		"title", title, "err", err)
	s.clearTransportForReplay(ctx)
	if err := push(); err != nil {
		return err
	}
	s.logger.Info("play: the clean-slate retry started the stream the box had just refused", "title", title)
	return nil
}

// writeGroupedPlayError answers a play request the box rejected as a group
// follower (#70) with a structured 409 instead of the raw SOAP fault, so the
// app can tell the user to drive the group's lead speaker (and offer to jump
// there) rather than showing an inscrutable UPnP error. The master hint is
// best-effort and omitted when unknown.
func (s *Server) writeGroupedPlayError(w http.ResponseWriter, err error) {
	resp := map[string]string{"error": "box-grouped"}
	if master := s.groupMasterHint(); master != "" {
		resp["master"] = master
	}
	s.logger.Info("play rejected: box is a grouped follower, answering 409 box-grouped",
		"master", resp["master"], "err", err)
	writeJSON(w, http.StatusConflict, resp)
}

// groupMasterHint resolves the current zone master (deviceID, falling back to
// the master's LAN IP) from the box's own /getZone, with a short budget so a
// slow firmware cannot stall the error response. Empty when unknown.
func (s *Server) groupMasterHint() string {
	if s.boxHost == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	z, err := boxapi.New(s.boxHost).GetZone(ctx)
	if err != nil {
		return ""
	}
	if z.Master != "" {
		return z.Master
	}
	return z.SenderIP
}

// ---- Box Settings (Bose API Proxy) ----

// handleBoxSettings returns info + volume + bass + network + sources
// combined as JSON.
func (s *Server) handleBoxSettings(w http.ResponseWriter, r *http.Request) {
	if s.boxHost == "" {
		http.Error(w, "box host not configured", http.StatusServiceUnavailable)
		return
	}
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	settings, err := c.LoadSettings(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// wlan0/wpa boxes: STR changes Wi-Fi by rewriting wpa_supplicant directly,
	// bypassing Bose, so Bose /networkInfo keeps reporting the OLD profile (stale
	// SSID / frequency / signal even though the box is really associated
	// elsewhere). Read the LIVE association from wpa_supplicant and let it win;
	// /networkInfo is only the fallback when the live read is unavailable or the
	// field is empty. BCO/eth0 boxes have no wpa_supplicant and keep the
	// gabbo-signal + provisionedSSID path below untouched.
	if iface, mech := detectWlanMechanism(); mech == "wpa" {
		if live := wlanlive.Read(ctx, iface); live.Associated {
			for i := range settings.Network.Interfaces {
				ni := &settings.Network.Interfaces[i]
				if ni.Type != "WIFI_INTERFACE" {
					continue
				}
				if live.SSID != "" {
					ni.SSID = live.SSID
				}
				if live.FrequencyKHz != 0 {
					ni.Frequency = live.FrequencyKHz
				}
				if live.Signal != "" {
					ni.Signal = live.Signal
				}
			}
		}
	}
	// BCO speakers (Portable, scm ST20) report the connected interface as
	// ethernet with no signal in /networkInfo, but the box does emit the
	// Wi-Fi signal class over the gabbo WebSocket. Fill the connected
	// interface's empty signal from there so the settings UI shows it on
	// those models too.
	if s.wifiSignalFn != nil {
		if sig := s.wifiSignalFn(); sig != "" {
			for i := range settings.Network.Interfaces {
				ni := &settings.Network.Interfaces[i]
				if ni.Signal == "" && (ni.State == "NETWORK_ETHERNET_CONNECTED" || ni.State == "NETWORK_WIFI_CONNECTED") {
					ni.Signal = sig
				}
			}
		}
	}
	// Same for the SSID: BCO /networkInfo carries none, but STR knows the
	// network it provisioned (its wlan-creds, or the AirplayConfiguration
	// profile). Fill it on the connected interface when empty.
	if ssid := provisionedSSID(); ssid != "" {
		for i := range settings.Network.Interfaces {
			ni := &settings.Network.Interfaces[i]
			if ni.SSID == "" && (ni.State == "NETWORK_ETHERNET_CONNECTED" || ni.State == "NETWORK_WIFI_CONNECTED") {
				ni.SSID = ssid
			}
		}
	}
	writeJSON(w, http.StatusOK, settings)
}

// provisionedSSID returns the Wi-Fi SSID the speaker is on, from the most
// reliable source available. Used to fill the SSID on BCO boxes (scm ST20,
// Portable), whose /networkInfo reports only an ethernet coprocessor with
// no ssid field.
//
// Sources, in order:
//  1. STR's own wlan-creds: what STR last provisioned. Subject to the
//     stick-mount race at cold boot, so it can be empty even on a box that
//     is associated (issue #90).
//  2. Bose NetManager's NetworkProfiles.xml: the box's own ground truth for
//     the profile it associates to. Present before BoseApp's HTTP server is
//     up, so it survives the boot race that leaves wlan-creds empty.
//  3. The slot-0 PersistentWifiProfile in AirplayConfiguration.xml (WAC
//     onboarding path).
func provisionedSSID() string {
	if b, err := os.ReadFile("/mnt/nv/streborn/wlan-creds"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "SSID=") {
				if v := strings.TrimSpace(strings.TrimPrefix(line, "SSID=")); v != "" {
					return v
				}
			}
		}
	}
	if m, _ := filepath.Glob("/mnt/nv/BoseApp-Persistence/*/NetworkProfiles.xml"); len(m) > 0 {
		if b, err := os.ReadFile(m[0]); err == nil {
			if v := ssidAfter(string(b), "<profile"); v != "" {
				return v
			}
		}
	}
	if m, _ := filepath.Glob("/mnt/nv/BoseApp-Persistence/*/AirplayConfiguration.xml"); len(m) > 0 {
		if b, err := os.ReadFile(m[0]); err == nil {
			if v := ssidAfter(string(b), "PersistentWifiProfile"); v != "" {
				return v
			}
		}
	}
	return ""
}

// ssidAfter extracts the first ssid="..." attribute value that appears at
// or after the given anchor substring in s, or "" if none.
func ssidAfter(s, anchor string) string {
	i := strings.Index(s, anchor)
	if i < 0 {
		return ""
	}
	r := s[i:]
	j := strings.Index(r, `ssid="`)
	if j < 0 {
		return ""
	}
	r = r[j+len(`ssid="`):]
	if k := strings.IndexByte(r, '"'); k >= 0 {
		return r[:k]
	}
	return ""
}

// handleBoxName PUT sets the box name. Body {"name":"..."}.
func (s *Server) handleBoxName(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPut) {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSONRequest(w, r, 1024, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "name empty", http.StatusBadRequest)
		return
	}
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	if err := c.SetName(ctx, req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// On /name POST Bose also resets the margeURL back to default —
	// trigger AutoPair so the pair state is immediately re-established.
	if s.autoPair != nil {
		go s.autoPair.TriggerNow(context.Background())
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "name": req.Name})
}

// handleBoxVolume PUT sets the volume. Body {"value":N}.
func (s *Server) handleBoxVolume(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet, http.MethodPut) {
		return
	}
	c := boxapi.New(s.boxHost)
	// GET returns the current volume so home automation / a status display can
	// read the level (and do relative up/down) without parsing the heavier
	// /api/box/settings blob.
	if r.Method == http.MethodGet {
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		v, err := c.GetVolume(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"value": v.Actual, "target": v.Target, "muted": v.Muted})
		return
	}
	// PUT sets the absolute volume (0-100).
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	var req struct {
		Value int `json:"value"`
	}
	if !decodeJSONRequest(w, r, 256, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	if err := c.SetVolume(ctx, req.Value); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"value": req.Value})
}

// handleBoxSource PUT switches the box to another source: AUX,
// BLUETOOTH or STANDBY. Body {"source":"AUX"}.
//
// Bose /select expects a ContentItem XML. We build it depending on the
// source. STANDBY has its own ContentItem without sourceAccount.
func (s *Server) handleBoxSource(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPut) {
		return
	}
	var req struct {
		Source        string `json:"source"`
		SourceAccount string `json:"sourceAccount"`
	}
	if !decodeJSONRequest(w, r, 256, &req) {
		return
	}
	src := strings.ToUpper(strings.TrimSpace(req.Source))
	if src == "" {
		http.Error(w, "source missing", http.StatusBadRequest)
		return
	}
	client := &http.Client{Timeout: 6 * time.Second}

	// Special case STANDBY: no ContentItem source at Bose. /key POWER
	// only triggers the LED animation, /standby is the real endpoint —
	// and Bose expects **GET**, not POST (POST returns 400).
	if src == "STANDBY" {
		// An app-initiated standby is a deliberate stop: arm the latch BEFORE
		// the call so the UPNP->STANDBY flip it causes is not classified as a
		// spontaneous firmware drop and auto-resumed (#419).
		s.NoteUserStop()
		u := fmt.Sprintf("http://%s:8090/standby", s.boxHost)
		resp, err := client.Get(u)
		if err != nil {
			http.Error(w, "box unreachable: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			http.Error(w, "box error: "+string(respBody), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"source": "STANDBY"})
		return
	}

	body, ok := s.contentItemForSource(r.Context(), src, req.SourceAccount)
	if !ok {
		http.Error(w, "unsupported source: "+src, http.StatusBadRequest)
		return
	}
	url := fmt.Sprintf("http://%s:8090/select", s.boxHost)
	httpReq, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, url, strings.NewReader(body))
	httpReq.Header.Set("Content-Type", "text/xml")
	httpReq.Header.Set("User-Agent", "STR/1.0")
	resp, err := client.Do(httpReq)
	if err != nil {
		http.Error(w, "box unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		// A speaker that lacks the requested hardware source (e.g. the
		// ST20 variants without Bluetooth) rejects /select with a 1005
		// UNKNOWN_SOURCE_ERROR. Surface that as a machine-readable reason
		// so the client can show a friendly localized message instead of
		// the raw Bose error XML.
		if strings.Contains(string(respBody), "UNKNOWN_SOURCE_ERROR") {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "source_unavailable",
				"source": src,
			})
			return
		}
		http.Error(w, "box error: "+string(respBody), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"source": src})
}

// handleBoxPower is the mobile remote's single on/off control. Body {"on":bool}.
//
// "off" puts the box into Bose standby (GET /standby) - the real power-off. Users
// reported that Stop only pauses the stream and the speaker stays on (the box has
// no concept of "off" for a stream, Stop just halts the transport); standby is
// what actually switches it off, so the remote needs its own power control.
//
// "on" wakes the box from standby and brings the last station back, the same
// power-on resume a hardware power press gives. Because this is an explicit user
// press, it skips the ResumeLastPlay self-wake/zone guards and pushes the last
// stream directly; if nothing is remembered the bare wake still powers the box up.
func (s *Server) handleBoxPower(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if !decodeJSONRequest(w, r, 256, &req) {
		return
	}
	if s.boxHost == "" {
		http.Error(w, "box host not configured", http.StatusServiceUnavailable)
		return
	}
	if !req.On {
		// Power off: Bose /standby expects GET (POST returns 400). Same call the
		// Standby input uses; the real power-off, unlike Stop/Pause. Arm the
		// deliberate-stop latch BEFORE the call so the UPNP->STANDBY flip it
		// causes is not classified as a spontaneous firmware drop (#419).
		s.NoteUserStop()
		client := &http.Client{Timeout: 6 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://%s:8090/standby", s.boxHost))
		if err != nil {
			http.Error(w, "box unreachable: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			http.Error(w, "box error: "+string(body), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"on": false})
		return
	}
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
	// Power on: explicit user "play it again", so clear any deliberate-stop intent
	// (and anchor the standby-flip discriminator, #419), wake the box, then
	// re-push the last station from under the lock.
	s.NoteUserPlay()
	s.ensureBoxReady(r.Context())
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	var boxURL, title, art, mime string
	if lp != nil {
		lp.failed = false
		lp.rePushes = 0
		boxURL, title, art, mime = lp.boxURL, lp.title, lp.art, lp.mime
	}
	s.lastPlayMu.Unlock()
	if boxURL != "" {
		if err := s.renderer.PlayURLMime(r.Context(), boxURL, title, art, mime); err != nil {
			s.logger.Warn("power on: resume of last station failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"on": true})
}

// handleBoxBass PUT sets the bass value. Body {"value":N}.
func (s *Server) handleBoxBass(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPut) {
		return
	}
	var req struct {
		Value int `json:"value"`
	}
	if !decodeJSONRequest(w, r, 256, &req) {
		return
	}
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	if err := c.SetBass(ctx, req.Value); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"value": req.Value})
}

// handleBoxReboot restarts the box via shell `reboot`. Used so that
// conf files from the stick (wlan / region / name) are applied on the
// run.sh boot path — this avoids a permanently running USB watcher
// polling loop.
func (s *Server) handleBoxReboot(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "reboot only allowed from LAN", http.StatusForbidden)
		return
	}
	s.logger.Info("Box reboot requested by user")
	writeJSON(w, http.StatusOK, map[string]string{"status": "rebooting"})
	// Execute 1s later so our HTTP response still gets out.
	go func() {
		time.Sleep(1 * time.Second)
		_ = exec.Command("reboot").Run()
	}()
}

// handleBoxAirplayOpt reads or sets the "AirPlay optimization" setting,
// which is the iOS app's advanced toggle stored as the
// BCOResetTimerEnabled attribute on the <AirplayConfiguration> root in
// /mnt/nv/BoseApp-Persistence/<N>/AirplayConfiguration.xml (a BCO
// coprocessor keepalive). It exists only on BCO speakers (Portable,
// ST20-spotty); on other models the file is absent and GET reports
// supported=false. The value is read by BoseApp at boot, so a POST
// rewrites the attribute and reboots, exactly like the iOS app.
func (s *Server) handleBoxAirplayOpt(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	var path string
	if m, _ := filepath.Glob("/mnt/nv/BoseApp-Persistence/*/AirplayConfiguration.xml"); len(m) > 0 {
		path = m[0]
	}
	switch r.Method {
	case http.MethodGet:
		if path == "" {
			writeJSON(w, http.StatusOK, map[string]any{"supported": false})
			return
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"supported": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"supported": true,
			"enabled":   strings.Contains(string(raw), `BCOResetTimerEnabled="true"`),
		})
	case http.MethodPost:
		if path == "" {
			http.Error(w, "no AirplayConfiguration.xml (not a BCO speaker)", http.StatusBadRequest)
			return
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		val := "false"
		if body.Enabled {
			val = "true"
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "read: "+err.Error(), http.StatusInternalServerError)
			return
		}
		out := string(raw)
		switch {
		case strings.Contains(out, `BCOResetTimerEnabled="true"`):
			out = strings.ReplaceAll(out, `BCOResetTimerEnabled="true"`, `BCOResetTimerEnabled="`+val+`"`)
		case strings.Contains(out, `BCOResetTimerEnabled="false"`):
			out = strings.ReplaceAll(out, `BCOResetTimerEnabled="false"`, `BCOResetTimerEnabled="`+val+`"`)
		default:
			// Attribute absent (older file): add it to the root element.
			out = strings.Replace(out, "<AirplayConfiguration ", `<AirplayConfiguration BCOResetTimerEnabled="`+val+`" `, 1)
		}
		tmp := path + ".str-new"
		if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
			http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			http.Error(w, "rename: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = exec.Command("sync").Run()
		s.logger.Info("airplay-opt set, rebooting to apply", "enabled", body.Enabled)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": body.Enabled, "rebooting": true})
		// BoseApp reads BCOResetTimerEnabled at boot, so reboot to apply
		// (the iOS app does the same). Delay so the response flushes.
		go func() {
			time.Sleep(1500 * time.Millisecond)
			_ = exec.Command("reboot").Run()
		}()
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleResumeOnPowerOn reads or sets the per-box "resume the last station when
// the speaker is switched on" preference (default ON). Stored as a plain flag
// file on NAND ("1" / "0"), like region.txt, so it survives reboots. GET returns
// {supported, enabled}; POST {enabled} persists it. This is the opt-out for the
// power-on resume: a real power press brings back the last stream unless the user
// turns it off here.
func (s *Server) handleResumeOnPowerOn(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"supported": true, "enabled": s.resumeOnPowerOnEnabled()})
	case http.MethodPost:
		var body struct {
			Enabled bool `json:"enabled"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		path := s.resumeOnPowerOnPath
		if path == "" {
			path = defaultResumeOnPowerOnPath
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
			return
		}
		val := "1"
		if !body.Enabled {
			val = "0"
		}
		tmp := path + ".str-new"
		if err := os.WriteFile(tmp, []byte(val+"\n"), 0o644); err != nil {
			http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			http.Error(w, "rename: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = exec.Command("sync").Run()
		s.logger.Info("resume-on-power-on set", "enabled", body.Enabled)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": body.Enabled})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleDisplayTrack reads or sets the per-box "show the live radio track on the
// speaker's display" opt-in (default OFF). Stored as a plain NAND flag file
// ("1"/"0"). GET returns {supported, enabled}; POST {enabled} persists it.
// Enabling it makes STR re-push the now-playing metadata on each ICY title
// change, which briefly re-buffers the box, so it is the user's explicit choice.
func (s *Server) handleDisplayTrack(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"supported": true, "enabled": s.displayTrackEnabled(), "mode": s.displayTrackMode()})
	case http.MethodPost:
		var body struct {
			Enabled bool   `json:"enabled"`
			Mode    string `json:"mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		path := s.displayTrackPath
		if path == "" {
			path = defaultDisplayTrackPath
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
			return
		}
		val := "0"
		if body.Enabled {
			val = "1"
		}
		if err := writeFlagFile(path, val); err != nil {
			http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Persist the display mode too, when a valid one is supplied.
		mode := strings.ToLower(strings.TrimSpace(body.Mode))
		if mode == "title" || mode == "artist" || mode == "both" {
			if err := writeFlagFile(modePathFor(path), mode); err != nil {
				http.Error(w, "write mode: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		_ = exec.Command("sync").Run()
		s.logger.Info("display-track set", "enabled", body.Enabled, "mode", s.displayTrackMode())
		// Update the speaker display right away instead of waiting for the next
		// song: enable / mode change pushes the current title; disable reverts to
		// the box's default text. Async so the POST returns before the box I/O.
		if body.Enabled {
			go s.pushDisplayNow()
		} else {
			go s.pushDisplayDefault()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": body.Enabled, "mode": s.displayTrackMode()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// writeFlagFile atomically writes a one-line value to a NAND flag file.
func writeFlagFile(path, val string) error {
	tmp := path + ".str-new"
	if err := os.WriteFile(tmp, []byte(val+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// contentItemForSource builds the /select ContentItem for a source the user
// picked, and reports false for anything this speaker does not actually offer.
//
// It used to be a switch over AUX, LOCAL and BLUETOOTH, which is every input a
// speaker has and no input a soundbar has. A CineMate 130 owner (2026-08-08)
// has TV, CBL-Sat, BD-DVD, Game and Aux on the back of his box and could reach
// none of them from STR: once he switched to radio he had to get up and use the
// infrared remote to get his television sound back. The SoundTouch 300 has the
// same shape of inputs.
//
// The names cannot be hardcoded, and that is the whole lesson of the source
// handling elsewhere in this codebase: a CineMate calls its analogue input
// LOCAL where a SoundTouch 10 says AUX, an SA-5 answers AUX with sourceAccount
// AUX1 to AUX3, and a soundbar's HDMI inputs arrive as PRODUCT with the socket
// in sourceAccount. So the box's own /sources list decides.
//
// Validating against that list is also what makes this safe to accept from a
// client at all: source and sourceAccount end up inside an XML attribute, and
// echoing an arbitrary string there would let a caller write the ContentItem.
// Only values the speaker itself reported are ever used, and they are taken
// from the speaker's copy rather than from the request.
func (s *Server) contentItemForSource(ctx context.Context, src, account string) (string, bool) {
	item := func(source, acct string) string {
		if acct == "" {
			// No sourceAccount at all, which is how a CineMate describes its
			// own analogue input and the reason the AUX form cannot simply be
			// reused for it (#491).
			return `<ContentItem source="` + xmlAttr(source) + `"></ContentItem>`
		}
		return `<ContentItem source="` + xmlAttr(source) + `" sourceAccount="` + xmlAttr(acct) + `"></ContentItem>`
	}

	sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if settings, err := boxapi.New(s.boxHost).LoadSettings(sctx); err == nil && len(settings.Sources) > 0 {
		for _, have := range settings.Sources {
			if !strings.EqualFold(have.Source, src) {
				continue
			}
			// An empty account in the request matches the box's entry for this
			// source, whatever it is: the phone sends what it was given, and a
			// source with exactly one account needs no disambiguation.
			if account != "" && !strings.EqualFold(have.SourceAccount, account) {
				continue
			}
			return item(have.Source, have.SourceAccount), true
		}
		// The box answered with a list and this source was not in it.
		return "", false
	}

	// The box's list could not be read. Fall back to the three forms that were
	// hardcoded before, so a speaker that is slow to answer /sources keeps the
	// inputs it has always had rather than losing them to a timeout.
	switch strings.ToUpper(src) {
	case "AUX":
		return `<ContentItem source="AUX" sourceAccount="AUX"></ContentItem>`, true
	case "LOCAL":
		return `<ContentItem source="LOCAL"></ContentItem>`, true
	case "BLUETOOTH", "BT":
		return `<ContentItem source="BLUETOOTH" sourceAccount=""></ContentItem>`, true
	}
	return "", false
}

// xmlAttr escapes a value for use inside a double-quoted XML attribute.
func xmlAttr(v string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(v))
	return strings.ReplaceAll(b.String(), `"`, "&#34;")
}
