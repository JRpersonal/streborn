// WLAN credential change endpoints and wpa_supplicant handling.

package webui

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// handleBoxWLAN sets the box's WLAN configuration at runtime.
// Body: {"ssid":"...", "password":"..."}
//
// Robust across the two Wi-Fi stacks SoundTouch ships (see run.sh's WLAN
// section):
//   - wpa_supplicant boxes (wlan0): the new network is applied LIVE via wpa_cli
//     reconfigure, verified, and rolled back to the previous network if it does
//     not associate, so a wrong password leaves the box on its old Wi-Fi rather
//     than stranded.
//   - BCO boxes (eth0, e.g. Portable): no usable runtime channel exists
//     (wpa_supplicant is absent), so the credentials are persisted and the box
//     reboots to apply them through the proven boot-time provisioning path.
//
// In ALL cases the SSID/PASS are written to the canonical NAND wlan-creds that
// the boot path replays, so the change survives a reboot. The previous version
// only wrote a runtime wpa file at the wrong path and never updated wlan-creds,
// so a reboot reverted it AND on BCO boxes it poked a non-existent
// wpa_supplicant and silently did nothing while reporting success.
//
// The switch runs in the background and the response returns immediately: the
// box leaves the current network as it switches, so the client must rediscover
// it on the new IP rather than wait on this request. LAN-only so a stray
// internet call can never move the speaker's Wi-Fi.
func (s *Server) handleBoxWLAN(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPut) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "wlan switch only allowed from LAN", http.StatusForbidden)
		return
	}
	var req struct {
		SSID     string `json:"ssid"`
		Password string `json:"password"`
		// Force skips the site-survey pre-flight (the user chose to switch to a
		// network the speaker cannot currently see, e.g. a momentarily-missed one).
		Force bool `json:"force"`
		// Hidden marks the target as a hidden network (SSID broadcast disabled).
		// A hidden SSID never appears in the box's site survey, so it implies
		// skipping the pre-flight, and the wpa config gains scan_ssid=1 so
		// wpa_supplicant probes for the SSID directly instead of waiting for a
		// beacon that never carries it.
		Hidden bool `json:"hidden"`
	}
	if !decodeJSONRequest(w, r, 2048, &req) {
		return
	}
	req.SSID = strings.TrimSpace(req.SSID)
	if req.SSID == "" {
		http.Error(w, "ssid must not be empty", http.StatusBadRequest)
		return
	}
	// WPA requires a PSK of at least 8 characters; an empty password means an
	// open network (key_mgmt=NONE in buildWPAConfig).
	if req.Password != "" && len(req.Password) < 8 {
		http.Error(w, "password too short (at least 8 characters)", http.StatusBadRequest)
		return
	}

	// Pre-flight: confirm the box can actually SEE the target network before the
	// switch. SoundTouch speakers are 2.4 GHz only, so pointing one at a 5 GHz-only
	// network strands it (it leaves the current network, cannot join the new one,
	// and then needs a Bose-app re-pair). The box's own site survey only lists the
	// bands it supports, so an invisible SSID is the clean signal to refuse; the
	// `force` flag lets the user override a momentarily-missed but real network,
	// and `hidden` implies the same skip: a hidden SSID is invisible to the
	// survey BY DESIGN, so refusing on invisibility would refuse every hidden
	// network forever.
	if wlanPreflightApplies(req.Force, req.Hidden) {
		sctx, scancel := context.WithTimeout(r.Context(), 12*time.Second)
		ssids, serr := boxapi.New(s.boxHost).SiteSurvey(sctx)
		scancel()
		if serr != nil {
			s.logger.Warn("WLAN switch preflight: site survey failed, proceeding without it", "err", serr)
		} else {
			visible := false
			for _, sid := range ssids {
				if sid == req.SSID {
					visible = true
					break
				}
			}
			if !visible {
				s.logger.Info("WLAN switch refused: target SSID not visible to the speaker", "ssid", req.SSID, "visible", ssids)
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error":   "The speaker can't see that network. SoundTouch speakers only support 2.4 GHz Wi-Fi, so a 5 GHz network will not work. Pick a network the speaker can see, or switch anyway.",
					"code":    "ssid-not-visible",
					"ssid":    req.SSID,
					"visible": ssids,
				})
				return
			}
		}
	}

	iface, mech := detectWlanMechanism()
	// Persist to NAND (with .bak backup) BEFORE responding: the response triggers
	// the client to rediscover the box on its new network, and the actual switch
	// runs in a background goroutine, so committing the canonical creds first
	// means a crash after the response can never leave the client believing the
	// switch happened while NAND still holds the old creds.
	if err := backupAndWriteWlanCreds(req.SSID, req.Password, req.Hidden); err != nil {
		http.Error(w, "persist wlan creds: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Respond before switching: the box drops off the current network mid-switch,
	// so the client rediscovers it on its new IP instead of waiting on this socket.
	status := "switching"
	if mech == "bco" {
		status = "rebooting"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":    status,
		"ssid":      req.SSID,
		"mechanism": mech,
	})
	s.logger.Info("WLAN switch requested", "ssid", req.SSID, "mechanism", mech, "iface", iface, "hidden", req.Hidden)
	go s.applyWLANChange(iface, mech, req.SSID, req.Password, req.Hidden)
}

// wlanPreflightApplies reports whether the site-survey visibility pre-flight
// should run for a WLAN change request. `force` is the user's explicit
// override; `hidden` networks never show up in a site survey, so the
// pre-flight is meaningless for them and must be skipped.
func wlanPreflightApplies(force, hidden bool) bool {
	return !force && !hidden
}

const (
	wlanCredsPath = "/mnt/nv/streborn/wlan-creds"
	wpaConfPath   = "/etc/wpa_supplicant.conf"
	// wpaBackupPath holds the rollback copy of the live wpa_supplicant.conf on
	// writable NAND. /etc is read-only on several chassis (rhino/scm), so the
	// backup cannot live next to the conf: writing it to /etc is exactly what
	// aborted runtime Wi-Fi switches before ("read-only file system"). run.sh's
	// M3 boot path uses the same NAND location.
	wpaBackupPath = "/mnt/nv/streborn/wpa_supplicant.conf.bak"
	// wlanApplyMarkerPath is a one-shot marker the app-initiated Wi-Fi change
	// drops before a reboot so run.sh's boot path treats that boot as an active
	// "program the new SSID" provision instead of a passive replay of the current
	// network. run.sh deletes it on read, so a wrong password cannot loop (#184).
	wlanApplyMarkerPath = "/mnt/nv/streborn/.wlan-apply-pending"
)

// touchWLANApplyMarker drops the one-shot boot marker that makes run.sh actively
// provision the new Wi-Fi after an app "apply now" change, rather than replaying
// the old network and exiting hands-off (which left an app Wi-Fi change dead-
// ending when the old AP was still in range, #184).
func touchWLANApplyMarker() {
	if err := os.WriteFile(wlanApplyMarkerPath, []byte("1\n"), 0o600); err != nil {
		// Non-fatal: without the marker the boot falls back to the old replay
		// behaviour, i.e. the pre-fix behaviour, never worse.
		return
	}
}

// detectWlanMechanism mirrors run.sh's interface detection: a wlan* iface means
// a wpa_supplicant stack (live switch possible); eth0-only is the BCO pattern
// (Wi-Fi via the chip exposed as eth0, no wpa_supplicant -> reboot to apply).
func detectWlanMechanism() (iface, mech string) {
	for _, w := range []string{"wlan0", "wlan1"} {
		if _, err := os.Stat("/sys/class/net/" + w); err == nil {
			return w, "wpa"
		}
	}
	return "eth0", "bco"
}

// applyWLANChange applies the (already persisted) credentials by the box's
// mechanism. Serialized by wlanMu so two switches cannot interleave their writes
// to wlan-creds / wpa_supplicant.conf and leave the box on an unpredictable
// network. The creds were committed synchronously by the handler before this
// runs, so this only drives the live switch / reboot.
func (s *Server) applyWLANChange(iface, mech, ssid, password string, hidden bool) {
	s.wlanMu.Lock()
	defer s.wlanMu.Unlock()
	switch mech {
	case "wpa":
		switch s.applyWlanWPALive(iface, ssid, password, hidden) {
		case wpaConfirmed:
			s.logger.Info("WLAN: live switch confirmed", "ssid", ssid, "iface", iface)
			_ = os.Remove(wlanCredsPath + ".bak")
			_ = os.Remove(wpaBackupPath)
		case wpaCannotApply:
			// The conf could not be written live (read-only /etc and the
			// bind-mount overlay both failed). The new creds are already on NAND,
			// so reboot and let run.sh's boot path provision them (M3 applies the
			// same bind workaround from a clean boot). Keep the new creds: do NOT
			// roll back.
			s.logger.Warn("WLAN: cannot switch live, rebooting to apply new network via boot path", "ssid", ssid)
			// Drop the rollback backup before the reboot: we are committing the new
			// creds via the boot path, so a stale backup must not survive on NAND and
			// be used to roll a future switch back to this now-superseded conf.
			_ = os.Remove(wpaBackupPath)
			// Mark this as an active apply so run.sh programs the new SSID on boot
			// instead of replaying the old network hands-off (#184).
			touchWLANApplyMarker()
			rebootBox()
		default: // wpaNotAssociated
			// Did not associate (e.g. wrong password): roll all the way back so
			// the box stays on its previous network instead of unreachable. The
			// agent runs ON the box, so it can do this even while the box is
			// briefly off the LAN.
			s.logger.Warn("WLAN: new network did not associate, rolling back to previous", "ssid", ssid)
			restoreWlanCreds()
			s.restoreWPAConfAndReload(iface)
		}
	default:
		s.logger.Info("WLAN: BCO chassis, rebooting to apply via boot path", "ssid", ssid)
		// Mark this as an active apply so run.sh programs the new SSID on boot
		// instead of replaying the old network hands-off (#184).
		touchWLANApplyMarker()
		rebootBox()
	}
}

// backupAndWriteWlanCreds writes the canonical NAND wlan-creds (the SSID=/PASS=
// format the boot path replays, plus HIDDEN=1 for hidden networks), keeping the
// previous set as .bak for rollback.
func backupAndWriteWlanCreds(ssid, password string, hidden bool) error {
	_ = os.Rename(wlanCredsPath, wlanCredsPath+".bak") // best-effort backup
	return writeWlanCredsFile(wlanCredsPath, ssid, password, hidden)
}

// writeWlanCredsFile is the path-injectable core of backupAndWriteWlanCreds,
// kept separate so the file format stays unit-testable off-box. run.sh's boot
// replay parses these exact SSID=/PASS=/HIDDEN= lines.
func writeWlanCredsFile(path, ssid, password string, hidden bool) error {
	body := fmt.Sprintf("SSID=%s\nPASS=%s\n", ssid, password)
	if hidden {
		body += "HIDDEN=1\n"
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func restoreWlanCreds() {
	if _, err := os.Stat(wlanCredsPath + ".bak"); err == nil {
		_ = os.Rename(wlanCredsPath+".bak", wlanCredsPath)
	}
}

// wpaApplyResult reports how a live wpa switch ended so applyWLANChange can pick
// the right recovery.
type wpaApplyResult int

const (
	wpaConfirmed     wpaApplyResult = iota // box associated to the new SSID
	wpaNotAssociated                       // conf written, but no association -> roll back
	wpaCannotApply                         // conf could not be written live -> reboot to apply
)

// applyWlanWPALive writes the new wpa_supplicant.conf, reloads wpa_supplicant,
// and reports whether the box associated to the new SSID within the timeout. The
// running conf is backed up to writable NAND first (NOT next to the conf: /etc is
// read-only on rhino/scm) so a failed switch can roll back. A failed backup never
// blocks the switch — it only forfeits the rollback.
func (s *Server) applyWlanWPALive(iface, ssid, password string, hidden bool) wpaApplyResult {
	if cur, err := os.ReadFile(wpaConfPath); err == nil {
		if werr := os.WriteFile(wpaBackupPath, cur, 0o600); werr != nil {
			// Read-only NAND would be unexpected, but never let a backup failure
			// abort the switch (the old /etc backup did, breaking every switch on
			// read-only-/etc boxes). Drop any stale backup so rollback won't
			// restore an unrelated conf.
			s.logger.Warn("WLAN: could not back up wpa conf, switching anyway (rollback unavailable)", "err", werr)
			_ = os.Remove(wpaBackupPath)
		}
	} else {
		// Could not read the current conf to back it up (e.g. it does not exist
		// yet): drop any stale backup from an earlier switch so a later rollback
		// can never restore an unrelated/older conf. Symmetric with the
		// write-failure path above.
		_ = os.Remove(wpaBackupPath)
	}
	method, err := writeWPAConf(buildWPAConfig(ssid, password, hidden))
	if err != nil {
		s.logger.Warn("WLAN: write wpa conf failed, will reboot to apply via boot path", "err", err, "path", wpaConfPath)
		return wpaCannotApply
	}
	s.logger.Info("WLAN: wpa conf written", "method", method, "iface", iface)
	// Escalate the way run.sh's boot path does (M3 restart, M4 add_network)
	// instead of a single reconfigure+reassociate then rollback. A bare
	// reconfigure does not dislodge a config NetManager reverted, so a runtime
	// switch that run.sh would have completed on the next boot used to fail live
	// and roll straight back (#288). Each stage only runs if the previous one did
	// not associate, and the rollback in applyWLANChange still protects a genuine
	// failure (e.g. a wrong password), so this can only help, never strand.
	reloadWPA(iface)
	if waitWPAAssociated(iface, ssid, 12*time.Second) {
		return wpaConfirmed
	}
	s.logger.Warn("WLAN: reconfigure did not associate, restarting wpa_supplicant (M3)", "ssid", ssid)
	restartWPA(iface)
	if waitWPAAssociated(iface, ssid, 12*time.Second) {
		return wpaConfirmed
	}
	s.logger.Warn("WLAN: restart did not associate, trying wpa_cli add_network fallback (M4)", "ssid", ssid)
	if wpaAddNetwork(iface, ssid, password, hidden) && waitWPAAssociated(iface, ssid, 12*time.Second) {
		return wpaConfirmed
	}
	return wpaNotAssociated
}

// waitWPAAssociated polls wpaAssociatedTo until the box is COMPLETED on ssid or
// the window elapses.
func waitWPAAssociated(iface, ssid string, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		if wpaAssociatedTo(iface, ssid) {
			return true
		}
	}
	return false
}

// restartWPA fully restarts wpa_supplicant (run.sh M3): a reconfigure keeps a
// stale/NetManager-reverted association, a clean relaunch reads the new conf from
// scratch. Independent of wpa_cli being present.
func restartWPA(iface string) {
	_ = exec.Command("killall", "wpa_supplicant").Run()
	time.Sleep(time.Second)
	_ = exec.Command("wpa_supplicant", "-B", "-i", iface, "-s", "-c", wpaConfPath, "-D", "nl80211").Start()
}

// wpaAddNetwork is run.sh's M4 fallback: build the network directly through
// wpa_cli (add_network / set_network / enable / select / save_config) when the
// conf-file path did not take. scan_ssid=1 lets a hidden SSID be found. Returns
// false if wpa_cli is absent or add_network fails.
func wpaAddNetwork(iface, ssid, password string, hidden bool) bool {
	if _, err := exec.LookPath("wpa_cli"); err != nil {
		return false
	}
	run := func(args ...string) string {
		out, _ := exec.Command("wpa_cli", append([]string{"-i", iface}, args...)...).Output()
		return strings.TrimSpace(string(out))
	}
	id := run("add_network")
	if id == "" || strings.Contains(id, "FAIL") {
		return false
	}
	// wpa_cli set_network wants the ssid/psk quoted; %q emits the surrounding
	// double quotes wpa_cli expects, and exec passes the arg verbatim (no shell).
	run("set_network", id, "ssid", fmt.Sprintf("%q", ssid))
	if password != "" {
		run("set_network", id, "psk", fmt.Sprintf("%q", password))
	} else {
		run("set_network", id, "key_mgmt", "NONE")
	}
	if hidden {
		run("set_network", id, "scan_ssid", "1")
	}
	run("enable_network", id)
	run("select_network", id)
	run("save_config")
	return true
}

// writeWPAConf installs content as /etc/wpa_supplicant.conf, working around a
// read-only /etc the way run.sh's M3 boot path does: a direct write first, and
// if that fails, a bind mount overlaying the conf from a tmpfs copy so
// wpa_supplicant reads the new content without /etc ever being written. Returns
// the method used ("direct"/"bind") or an error if both fail.
func writeWPAConf(content string) (string, error) {
	return writeWPAConfAt(wpaConfPath, "/tmp/wpa_supplicant.conf.str", content)
}

// writeWPAConfAt is the path-injectable core of writeWPAConf, kept separate so
// the direct-write path is unit-testable off-box.
func writeWPAConfAt(confPath, tmpPath, content string) (string, error) {
	directErr := os.WriteFile(confPath, []byte(content), 0o600)
	if directErr == nil {
		return "direct", nil
	}
	// /etc is read-only (rhino/scm): stage the conf in tmpfs and bind-mount it
	// over the existing path so wpa_supplicant reads the new content.
	if terr := os.WriteFile(tmpPath, []byte(content), 0o600); terr != nil {
		return "", terr
	}
	if berr := exec.Command("mount", "--bind", tmpPath, confPath).Run(); berr != nil {
		return "", fmt.Errorf("direct write (%w) and bind-mount (%v) both failed", directErr, berr)
	}
	return "bind", nil
}

// reloadWPA reloads the new conf in place via wpa_cli (preferred, keeps the
// daemon up), or restarts wpa_supplicant if wpa_cli is absent. Same commands
// run.sh uses in its M3/M6 approaches.
func reloadWPA(iface string) {
	if _, err := exec.LookPath("wpa_cli"); err == nil {
		_ = exec.Command("wpa_cli", "-i", iface, "reconfigure").Run()
		_ = exec.Command("wpa_cli", "-i", iface, "reassociate").Run()
		return
	}
	_ = exec.Command("killall", "wpa_supplicant").Run()
	time.Sleep(time.Second)
	_ = exec.Command("wpa_supplicant", "-B", "-i", iface, "-s", "-c", wpaConfPath, "-D", "nl80211").Start()
}

// wpaAssociatedTo reports whether wpa_supplicant is COMPLETED on the given SSID.
func wpaAssociatedTo(iface, ssid string) bool {
	out, err := exec.Command("wpa_cli", "-i", iface, "status").Output()
	if err != nil {
		return false
	}
	st := string(out)
	if !strings.Contains(st, "wpa_state=COMPLETED") {
		return false
	}
	for _, line := range strings.Split(st, "\n") {
		if strings.TrimSpace(line) == "ssid="+ssid {
			return true
		}
	}
	return false
}

func (s *Server) restoreWPAConfAndReload(iface string) {
	if b, err := os.ReadFile(wpaBackupPath); err == nil {
		if _, werr := writeWPAConf(string(b)); werr != nil {
			s.logger.Warn("WLAN: rollback write failed", "err", werr)
		}
		_ = os.Remove(wpaBackupPath)
	}
	reloadWPA(iface)
}

// rebootBox triggers a detached reboot so BCO boxes apply the persisted creds
// through the boot-time provisioning path. sync flushes the NAND creds first.
func rebootBox() {
	_ = exec.Command("sh", "-c", "(sleep 1; sync; /sbin/reboot) </dev/null >/dev/null 2>&1 &").Start()
}

// buildWPAConfig generates a minimal wpa_supplicant.conf. With an empty
// password key_mgmt=NONE is set (open WLAN). hidden adds scan_ssid=1 to the
// network block so wpa_supplicant sends SSID-specific probe requests, which is
// the only way to find a network that does not broadcast its SSID.
func buildWPAConfig(ssid, psk string, hidden bool) string {
	var b strings.Builder
	b.WriteString("ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=root\n")
	b.WriteString("update_config=1\n")
	b.WriteString("network={\n")
	b.WriteString("    ssid=\"" + escapeWPAValue(ssid) + "\"\n")
	if hidden {
		b.WriteString("    scan_ssid=1\n")
	}
	if psk == "" {
		b.WriteString("    key_mgmt=NONE\n")
	} else {
		b.WriteString("    psk=\"" + escapeWPAValue(psk) + "\"\n")
		b.WriteString("    key_mgmt=WPA-PSK\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func escapeWPAValue(s string) string {
	// Escape backslash and double-quote, plus the control characters that would
	// otherwise break the single-line key="value" form and corrupt the conf (a
	// JSON body can carry a literal newline/tab in an SSID).
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return r.Replace(s)
}
