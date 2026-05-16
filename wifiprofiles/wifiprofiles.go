// Package wifiprofiles liest WLAN Profile vom Host Betriebssystem aus
// damit die Desktop App eine Liste bekannter SSIDs anzeigen kann. Wird
// im Stick Setup verwendet damit der User die SSID nicht abtippen muss.
//
// Plattformen:
//
//	Windows  netsh wlan show profiles
//	Mac      networksetup -listpreferredwirelessnetworks (auf erstem WLAN Adapter)
//	Linux    nmcli connection show
//
// Passwoerter werden NICHT automatisch ausgelesen weil das je nach
// Plattform User Consent benoetigt. Es gibt ein optionales TryPassword
// das es versuchen darf — wird vom Frontend nur auf expliziten Klick
// "Passwort vorfuellen" aufgerufen.
package wifiprofiles

import (
	"bufio"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// run startet ein OS Command und verbirgt auf Windows das Console Fenster
// das sonst kurz aufflackert. Funktioniert auch auf Mac/Linux ohne extra
// Setup.
func run(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	hideWindow(cmd)
	return cmd.CombinedOutput()
}

// Profile beschreibt einen bekannten WLAN Eintrag.
type Profile struct {
	SSID     string `json:"ssid"`
	HasPass  bool   `json:"hasPass"` // true wenn Passwort vermutlich gespeichert (bei Windows aus netsh erkennbar)
	Source   string `json:"source"`  // "netsh" / "networksetup" / "nmcli"
}

// List liefert alle gespeicherten WLAN Profile auf dem Host.
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

// TryPassword versucht das gespeicherte Passwort fuer eine SSID
// auszulesen. Auf Windows: `netsh wlan show profile name=X key=clear`,
// fordert Admin Rechte oder zumindest User Session permissions. Auf Mac
// muss der Keychain Zugriff geben. Returns ("", nil) wenn nichts
// gefunden, error bei OS Fehler.
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

// CurrentSSID liefert das aktuell verbundene WLAN auf dem Host. Leer
// wenn der Host nicht im WLAN ist oder die Abfrage fehlschlaegt.
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
	// "SSID                   : MeinWifi"
	// "BSSID                  : aa:bb:..." → muss ausgeschlossen werden
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
	// Toleranter Parser: jede Zeile mit `:` wo der LEFT Key nicht
	// offensichtlich ein Adapter Header ist und der Rest nicht leer.
	// Funktioniert auf DE, EN und sehr wahrscheinlich auch weiteren
	// Localizations weil wir nicht auf das Wort "Benutzerprofil" angewiesen
	// sind sondern auf das Pattern "<irgendwas> : <SSID>".
	var profiles []Profile
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		raw := scanner.Text()
		// Items sind eingerueckt mit Leerzeichen. Ohne Einrueckung sind
		// Header Zeilen ("Profile in WLAN-Adapter \"Wi-Fi\":") oder
		// Sektionsueberschriften. Das ist plattformunabhaengig und sehr
		// robust gegen Locale Variationen.
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
		// Adapter / Interface Header ausschliessen (defensive)
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
		// Spezifischer Match auf das ECHTE Passwort Feld. "Schluesselinhalt"
		// (de neu), "Schlüsselinhalt" (de mit Umlaut), "Key Content" (en).
		// NICHT matchen: "Verschluesselung", "Schluessel index" etc.
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
	// Finde erstes Wireless Device
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
		return nil, fmt.Errorf("kein Wi-Fi Adapter gefunden")
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
	// security gibt das Passwort auf stderr (!) und gibt einen 'password:' line.
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
	// Braucht root oder NetworkManager Permission. Best Effort.
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
