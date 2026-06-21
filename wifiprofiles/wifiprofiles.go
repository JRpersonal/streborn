// Package wifiprofiles reads WLAN profiles from the host operating
// system so the desktop app can show a list of known SSIDs. Used in the
// stick setup so the user does not have to type the SSID by hand.
//
// Platforms:
//
//	Windows  netsh wlan show profiles
//	Mac      networksetup -listpreferredwirelessnetworks (on the first WLAN adapter)
//	Linux    nmcli connection show
//
// Passwords are NOT read out automatically because that requires user
// consent depending on the platform. There is an optional TryPassword
// that may attempt it — the frontend only calls it on an explicit
// "prefill password" click.
package wifiprofiles

import (
	"bufio"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// run starts an OS command and hides the console window on Windows that
// would otherwise flash up briefly. Works on Mac/Linux too without extra
// setup.
func run(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	hideWindow(cmd)
	return cmd.CombinedOutput()
}

// Profile describes a known WLAN entry.
type Profile struct {
	SSID    string `json:"ssid"`
	HasPass bool   `json:"hasPass"` // true if a password is likely stored (detectable from netsh on Windows)
	Source  string `json:"source"`  // "netsh" / "networksetup" / "nmcli"
}

// List returns all stored WLAN profiles on the host.
func List() ([]Profile, error) {
	switch runtime.GOOS {
	case "windows":
		return listWindows()
	case "darwin":
		return listMac()
	default:
		return listLinux()
	}
}

// TryPassword attempts to read out the stored password for an SSID. On
// Windows: `netsh wlan show profile name=X key=clear`, which requires
// admin rights or at least user session permissions. On Mac the keychain
// must grant access. Returns ("", nil) if nothing is found, error on an
// OS failure.
func TryPassword(ssid string) (string, error) {
	switch runtime.GOOS {
	case "windows":
		return tryPasswordWindows(ssid)
	case "darwin":
		return tryPasswordMac(ssid)
	default:
		return tryPasswordLinux(ssid)
	}
}

// CurrentSSID returns the currently connected WLAN on the host. Empty
// if the host is not on a WLAN or the query fails.
func CurrentSSID() string {
	switch runtime.GOOS {
	case "windows":
		return currentSSIDWindows()
	case "darwin":
		return currentSSIDMac()
	default:
		return currentSSIDLinux()
	}
}

func currentSSIDWindows() string {
	out, err := run("netsh", "wlan", "show", "interfaces")
	if err != nil {
		return ""
	}
	// "SSID                   : MyWifi"
	// "BSSID                  : aa:bb:..." → must be excluded
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		if key != "ssid" {
			continue
		}
		val := strings.TrimSpace(parts[1])
		if val == "" {
			continue
		}
		return val
	}
	return ""
}

func currentSSIDMac() string {
	cmd := exec.Command("/System/Library/PrivateFrameworks/Apple80211.framework/Versions/A/Resources/airport", "-I")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "SSID:") || strings.HasPrefix(line, "BSSID:") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(line, "SSID:"))
		if val != "" {
			return val
		}
	}
	return ""
}

func currentSSIDLinux() string {
	cmd := exec.Command("nmcli", "-t", "-f", "active,ssid", "dev", "wifi")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[0]) == "yes" {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

// ----- Windows -----

func listWindows() ([]Profile, error) {
	out, err := run("netsh", "wlan", "show", "profiles")
	if err != nil {
		return nil, fmt.Errorf("netsh: %v", err)
	}
	// Tolerant parser: any line with `:` where the LEFT key is not
	// obviously an adapter header and the rest is not empty. Works on DE,
	// EN and very likely further localizations because we do not rely on
	// the word "Benutzerprofil" but on the pattern "<anything> : <SSID>".
	var profiles []Profile
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		raw := scanner.Text()
		// Items are indented with spaces. Without indentation they are
		// header lines ("Profile in WLAN-Adapter \"Wi-Fi\":") or section
		// headings. This is platform-independent and very robust against
		// locale variations.
		if len(raw) == 0 || (raw[0] != ' ' && raw[0] != '\t') {
			continue
		}
		line := strings.TrimSpace(raw)
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if key == "" || val == "" {
			continue
		}
		keyLower := strings.ToLower(key)
		// Exclude adapter / interface headers (defensive)
		if strings.Contains(keyLower, "schnittstell") ||
			strings.Contains(keyLower, "interface") ||
			strings.Contains(keyLower, "wlan-adapter") {
			continue
		}
		profiles = append(profiles, Profile{SSID: val, HasPass: true, Source: "netsh"})
	}
	return dedup(profiles), nil
}

func tryPasswordWindows(ssid string) (string, error) {
	out, err := run("netsh", "wlan", "show", "profile", "name="+ssid, "key=clear")
	if err != nil {
		return "", fmt.Errorf("netsh: %v", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		// Specific match on the REAL password field. "Schluesselinhalt"
		// (de new), "Schlüsselinhalt" (de with umlaut), "Key Content" (en).
		// Do NOT match: "Verschluesselung", "Schluessel index" etc.
		klower := strings.ToLower(key)
		isPassword := klower == "schluesselinhalt" ||
			klower == "schlüsselinhalt" ||
			klower == "schl¼sselinhalt" || // CP1252 ü
			klower == "key content"
		if !isPassword {
			continue
		}
		if val == "" || strings.EqualFold(val, "Nicht vorhanden") || strings.EqualFold(val, "Absent") {
			continue
		}
		return val, nil
	}
	return "", nil
}

// ----- Mac -----

func listMac() ([]Profile, error) {
	// Find the first wireless device
	cmd := exec.Command("networksetup", "-listallhardwareports")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	device := ""
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	wifiSection := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "Hardware Port:") && strings.Contains(line, "Wi-Fi") {
			wifiSection = true
			continue
		}
		if wifiSection && strings.HasPrefix(line, "Device:") {
			device = strings.TrimSpace(strings.TrimPrefix(line, "Device:"))
			break
		}
	}
	if device == "" {
		return nil, fmt.Errorf("no Wi-Fi adapter found")
	}
	cmd2 := exec.Command("networksetup", "-listpreferredwirelessnetworks", device)
	out2, err := cmd2.CombinedOutput()
	if err != nil {
		return nil, err
	}
	var profiles []Profile
	s := bufio.NewScanner(strings.NewReader(string(out2)))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "Preferred networks") {
			continue
		}
		profiles = append(profiles, Profile{SSID: line, HasPass: true, Source: "networksetup"})
	}
	return dedup(profiles), nil
}

func tryPasswordMac(ssid string) (string, error) {
	cmd := exec.Command("security", "find-generic-password", "-ga", ssid)
	// security prints the password on stderr (!) and emits a 'password:' line.
	out, _ := cmd.CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "password:") {
			pw := strings.TrimSpace(strings.TrimPrefix(line, "password:"))
			pw = strings.Trim(pw, `"`)
			return pw, nil
		}
	}
	return "", nil
}

// ----- Linux -----

func listLinux() ([]Profile, error) {
	cmd := exec.Command("nmcli", "-t", "-f", "NAME,TYPE", "connection", "show")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("nmcli: %v", err)
	}
	var profiles []Profile
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name, typ := parts[0], parts[1]
		if !strings.Contains(typ, "wireless") {
			continue
		}
		profiles = append(profiles, Profile{SSID: name, HasPass: true, Source: "nmcli"})
	}
	return dedup(profiles), nil
}

func tryPasswordLinux(ssid string) (string, error) {
	// Needs root or NetworkManager permission. Best effort.
	cmd := exec.Command("nmcli", "-s", "-g", "802-11-wireless-security.psk", "connection", "show", ssid)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func dedup(in []Profile) []Profile {
	seen := map[string]struct{}{}
	out := make([]Profile, 0, len(in))
	for _, p := range in {
		if _, ok := seen[p.SSID]; ok {
			continue
		}
		seen[p.SSID] = struct{}{}
		out = append(out, p)
	}
	return out
}
