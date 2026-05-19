// Cold-bootstrap helpers: configure Wi-Fi on a brand-new (or factory-
// reset) Bose SoundTouch speaker that has no Wi-Fi yet by joining its
// open setup-AP from the host, POSTing addWirelessProfile to the
// Bose API on the AP gateway, then reconnecting the host to the home
// Wi-Fi so the box can find us again on the LAN.
//
// Bose has no native stick-only setup path; their own NetManager
// reads /media/sda1 only for dprint.conf (debug). So this is the
// only Wi-Fi bootstrap option that does not require extra hardware
// (USB cable, second WLAN adapter, smartphone hotspot, ...).
//
// Windows-only for now. macOS/Linux will need their own netsh
// equivalents; the high-level App methods are platform-neutral so
// the frontend will not need changes when we add those.

//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SetupAP is a candidate Bose factory-mode access point found in the
// host's Wi-Fi scan. The frontend uses this to decide whether to
// surface a "brand-new speaker detected, want to configure it?" UI.
type SetupAP struct {
	SSID      string `json:"ssid"`
	Interface string `json:"interface"`
	Signal    string `json:"signal"`
}

// ScanForSetupAPs returns visible Wi-Fi networks whose SSID matches a
// Bose factory-mode pattern ("Bose ST*", "Bose SoundTouch*",
// "SoundTouch*"). Best-effort, returns empty slice on errors so the
// caller can simply fall through to "no setup AP found".
func (a *App) ScanForSetupAPs() ([]SetupAP, error) {
	c := exec.Command("netsh", "wlan", "show", "networks", "mode=Bssid")
	hideCmdWindow(c)
	out, err := c.CombinedOutput()
	// Windows 11 24H2 silently blocks netsh wlan unless the calling
	// app has Location permission ("Allow desktop apps to access your
	// location" in Settings -> Privacy -> Location). Without it, netsh
	// prints a German/English notice and returns no networks, and the
	// cold-bootstrap path stays stuck on "no setup-AP found" forever.
	// Detect that specific failure and surface it loudly so the user
	// can fix it in two clicks instead of debugging the wizard.
	if err != nil {
		if isLocationDenied(string(out)) {
			return nil, fmt.Errorf("windows-location-denied: %s",
				"Standort-Berechtigung fehlt. Settings -> Datenschutz -> Standort -> "+
					"'Desktop-Apps der Zugriff auf Ihren Standort erlauben' aktivieren, "+
					"dann STR neu starten")
		}
		return nil, fmt.Errorf("netsh wlan show networks: %w", err)
	}
	if isLocationDenied(string(out)) {
		return nil, fmt.Errorf("windows-location-denied: %s",
			"Standort-Berechtigung fehlt. Settings -> Datenschutz -> Standort -> "+
				"'Desktop-Apps der Zugriff auf Ihren Standort erlauben' aktivieren, "+
				"dann STR neu starten")
	}
	return parseSetupAPs(string(out)), nil
}

// isLocationDenied detects the Windows 11 24H2 location-permission
// notice in netsh output. Match on stable substrings present in both
// the English and German variants so locale changes do not silently
// bypass the check.
func isLocationDenied(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, "standortberechtigung") ||
		strings.Contains(low, "location permission") ||
		strings.Contains(low, "privacy-location") ||
		strings.Contains(low, "wlanqueryinterface") ||
		strings.Contains(low, "erhöhte rechte")
}

func parseSetupAPs(netshOutput string) []SetupAP {
	var out []SetupAP
	var curSSID, curIface, curSignal string
	for _, line := range strings.Split(netshOutput, "\n") {
		l := strings.TrimSpace(line)
		// netsh prints localized strings; the "SSID N : NAME" form is
		// stable across locales (only the prefix word changes). We
		// match on " : " plus a numbered SSID prefix.
		if i := strings.Index(l, " : "); i > 0 && (strings.HasPrefix(l, "SSID ") || strings.HasPrefix(l, "Schnittstellenname")) {
			if strings.HasPrefix(l, "SSID ") {
				if curSSID != "" && isBoseSetupSSID(curSSID) {
					out = append(out, SetupAP{SSID: curSSID, Interface: curIface, Signal: curSignal})
				}
				curSSID = strings.TrimSpace(l[i+3:])
				curSignal = ""
			} else {
				curIface = strings.TrimSpace(l[i+3:])
			}
		}
		if strings.HasPrefix(l, "Signal") || strings.HasPrefix(l, "Signalqualität") {
			if i := strings.Index(l, " : "); i > 0 {
				curSignal = strings.TrimSpace(l[i+3:])
			}
		}
	}
	if curSSID != "" && isBoseSetupSSID(curSSID) {
		out = append(out, SetupAP{SSID: curSSID, Interface: curIface, Signal: curSignal})
	}
	// Dedup by SSID.
	seen := map[string]struct{}{}
	uniq := out[:0]
	for _, ap := range out {
		if _, dup := seen[ap.SSID]; dup {
			continue
		}
		seen[ap.SSID] = struct{}{}
		uniq = append(uniq, ap)
	}
	return uniq
}

func isBoseSetupSSID(ssid string) bool {
	low := strings.ToLower(ssid)
	return strings.HasPrefix(low, "bose st") ||
		strings.HasPrefix(low, "bose soundtouch") ||
		strings.HasPrefix(low, "soundtouch ")
}

// BootstrapResult is the JSON-serializable outcome handed back to the
// frontend.
type BootstrapResult struct {
	Step    string `json:"step"`    // last step reached
	OK      bool   `json:"ok"`      // overall success
	Message string `json:"message"` // user-facing string in EN (frontend localizes the Step)
	BoxIP   string `json:"boxIP"`   // home-LAN IP of the box if confirmed
}

// BootstrapBoxOnSetupAP performs the full cold-bootstrap dance:
//   1. snapshot the current home Wi-Fi profile so we can reconnect
//   2. add a transient open-Wi-Fi profile for setupSSID
//   3. join setupSSID, wait for a 192.0.2.x IP on the adapter
//   4. POST addWirelessProfile with home credentials to 192.0.2.1:8090
//   5. disconnect from setup-AP, delete the transient profile
//   6. reconnect to homeSSID, wait for IP
//   7. poll the home LAN until the box answers /info
//
// Returns step-by-step so the frontend can show live progress. Errors
// at any step are returned with the partial state for diagnostics.
func (a *App) BootstrapBoxOnSetupAP(setupSSID, homeSSID, homePassphrase, securityType string) (BootstrapResult, error) {
	res := BootstrapResult{Step: "start"}
	if setupSSID == "" || homeSSID == "" {
		return res, fmt.Errorf("setupSSID and homeSSID are both required")
	}
	if securityType == "" {
		securityType = "wpa_or_wpa2"
	}

	// Step 1: note the host's current Wi-Fi state so we can come back.
	// Best-effort: if it fails, the user just has to manually pick the
	// home network in the tray after we are done.
	prevSSID := currentWifiSSID()
	if prevSSID == "" {
		prevSSID = homeSSID
	}
	a.logger.Info("coldbootstrap: start", "setupSSID", setupSSID, "homeSSID", homeSSID, "prevSSID", prevSSID)

	// Step 2: add transient profile.
	res.Step = "add-profile"
	profilePath, cleanup, err := writeOpenWifiProfile(setupSSID)
	if err != nil {
		return res, fmt.Errorf("write profile xml: %w", err)
	}
	defer cleanup()
	if err := netshRun("wlan", "add", "profile", "filename="+profilePath, "user=current"); err != nil {
		return res, fmt.Errorf("netsh add profile: %w", err)
	}
	defer func() {
		_ = netshRun("wlan", "delete", "profile", "name="+setupSSID)
	}()

	// Step 3: connect to the setup-AP and wait for IP.
	res.Step = "join-setup-ap"
	if err := netshRun("wlan", "connect", "name="+setupSSID); err != nil {
		return res, fmt.Errorf("netsh connect setup-AP: %w", err)
	}
	if err := waitForIPOn(192_000, 12*time.Second); err != nil {
		return res, fmt.Errorf("waiting for 192.0.2.x IP after joining setup-AP: %w", err)
	}

	// Step 4: POST addWirelessProfile.
	res.Step = "send-config"
	if err := postAddWirelessProfile("192.0.2.1", homeSSID, homePassphrase, securityType); err != nil {
		// Try to recover Wi-Fi before returning so the user is not
		// stranded on an inert setup-AP.
		_ = netshRun("wlan", "disconnect")
		_ = netshRun("wlan", "connect", "name="+prevSSID)
		return res, fmt.Errorf("POST addWirelessProfile: %w", err)
	}

	// Step 5: disconnect from setup-AP. We do not wait for the box's
	// AP to actually go down (it may stay up briefly while the box
	// reassociates). Cleanup of the transient profile is the deferred
	// netsh delete profile above.
	res.Step = "leave-setup-ap"
	_ = netshRun("wlan", "disconnect")

	// Step 6: reconnect to home Wi-Fi. Windows usually does this on
	// its own once the setup-AP is gone, but we nudge it explicitly.
	res.Step = "rejoin-home"
	if err := netshRun("wlan", "connect", "name="+prevSSID); err != nil {
		// Not fatal: the OS may already have started reconnecting on
		// its own. Continue to the box-poll step.
		a.logger.Warn("coldbootstrap: netsh connect home failed, continuing", "err", err)
	}
	if err := waitForRoutable(20 * time.Second); err != nil {
		return res, fmt.Errorf("waiting for home Wi-Fi to come back: %w", err)
	}

	// Step 7: confirm the box reached the LAN.
	res.Step = "wait-for-box"
	ctx, cancel := context.WithTimeout(a.ctx, 75*time.Second)
	defer cancel()
	ip := pollBoxOnHomeLAN(ctx, a.logger)
	if ip == "" {
		res.OK = false
		res.Message = "Box did not show up on the home LAN within 75s after sending the config. Check Wi-Fi password and try again."
		return res, nil
	}
	res.Step = "done"
	res.OK = true
	res.BoxIP = ip
	res.Message = "Box is on the home Wi-Fi at " + ip
	return res, nil
}

// writeOpenWifiProfile drops a Windows WLAN profile XML for an open
// network into TEMP and returns its path plus a cleanup func.
func writeOpenWifiProfile(ssid string) (string, func(), error) {
	xml := `<?xml version="1.0"?>
<WLANProfile xmlns="http://www.microsoft.com/networking/WLAN/profile/v1">
  <name>` + xmlEscape(ssid) + `</name>
  <SSIDConfig><SSID><name>` + xmlEscape(ssid) + `</name></SSID></SSIDConfig>
  <connectionType>ESS</connectionType>
  <connectionMode>manual</connectionMode>
  <MSM><security>
    <authEncryption>
      <authentication>open</authentication>
      <encryption>none</encryption>
      <useOneX>false</useOneX>
    </authEncryption>
  </security></MSM>
</WLANProfile>
`
	stamp := time.Now().UnixNano()
	path := filepath.Join(os.TempDir(), fmt.Sprintf("str-coldboot-%d.xml", stamp))
	if err := os.WriteFile(path, []byte(xml), 0o600); err != nil {
		return "", func() {}, err
	}
	return path, func() { _ = os.Remove(path) }, nil
}

func xmlEscape(s string) string {
	r := strings.NewReplacer(`&`, `&amp;`, `<`, `&lt;`, `>`, `&gt;`, `"`, `&quot;`, `'`, `&apos;`)
	return r.Replace(s)
}

func netshRun(args ...string) error {
	cmd := exec.Command("netsh", args...)
	hideCmdWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func currentWifiSSID() string {
	c := exec.Command("netsh", "wlan", "show", "interfaces")
	hideCmdWindow(c)
	out, err := c.CombinedOutput()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "SSID") && !strings.HasPrefix(l, "SSID-BSSID") && !strings.HasPrefix(l, "BSSID") {
			if i := strings.Index(l, " : "); i > 0 {
				return strings.TrimSpace(l[i+3:])
			}
		}
	}
	return ""
}

// waitForIPOn polls local interface IPs for one in the 192.0.2.0/24
// (TEST-NET-1) range used by Bose's setup-AP. The first octet
// argument is kept for symmetry; we hard-code on 192.0.2 because
// that is what Bose hands out.
func waitForIPOn(_ int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		addrs, err := net.InterfaceAddrs()
		if err == nil {
			for _, a := range addrs {
				if ipnet, ok := a.(*net.IPNet); ok {
					ip4 := ipnet.IP.To4()
					if ip4 != nil && ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 2 {
						return nil
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("no 192.0.2.x address appeared within %s", timeout)
}

func waitForRoutable(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		addrs, err := net.InterfaceAddrs()
		if err == nil {
			for _, a := range addrs {
				if ipnet, ok := a.(*net.IPNet); ok {
					ip4 := ipnet.IP.To4()
					if ip4 != nil && ip4.IsPrivate() &&
						!(ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 2) {
						return nil
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("no routable private IP within %s", timeout)
}

func pollBoxOnHomeLAN(ctx context.Context, logger *slog.Logger) string {
	for {
		select {
		case <-ctx.Done():
			return ""
		default:
		}
		for _, subnet := range localIPv4Subnets() {
			for i := 1; i <= 254; i++ {
				select {
				case <-ctx.Done():
					return ""
				default:
				}
				ip := fmt.Sprintf("%s%d", subnet, i)
				if box, ok := probeStock(ctx, ip); ok && box.Host != "" {
					if logger != nil {
						logger.Info("coldbootstrap: box found on home LAN", "ip", box.Host)
					}
					return box.Host
				}
			}
		}
		// Give the box a moment between full /24 sweeps. The whole
		// sweep at ~1.2s/host with 32 parallel probes via probeStock
		// is fast; sleeping a bit avoids 100% CPU if it takes
		// several sweeps.
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(2 * time.Second):
		}
	}
}

// postAddWirelessProfile pushes the home Wi-Fi credentials to the
// Bose box over the setup-AP. The XML schema below was confirmed
// live against a SoundTouch 10 (variant rhino, firmware 27.0.6) and
// matches the community-documented Bose SoundTouch Web API. The box
// drops its setup-AP and starts re-associating as soon as it
// accepts the request, so a mid-response disconnect (EOF / reset)
// is treated as success rather than a hard error.
func postAddWirelessProfile(boxIP, ssid, passphrase, securityType string) error {
	body := `<AddWirelessProfile timeout="30">` +
		`<profile ssid="` + xmlEscape(ssid) + `" ` +
		`password="` + xmlEscape(passphrase) + `" ` +
		`securityType="` + xmlEscape(securityType) + `" />` +
		`</AddWirelessProfile>`
	url := fmt.Sprintf("http://%s:8090/addWirelessProfile", boxIP)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/xml")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "reset") {
			return nil
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return nil
}
