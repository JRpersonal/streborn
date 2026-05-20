// Package sticksetup enthaelt die Windows/Mac Logik fuer das Bestuecken
// eines frischen SD Sticks: Drive Discovery, File Copy, optional Eject.
// Wird von der Desktop App genutzt; ist nicht Teil des Stick Agent.
//
// Top-level Package (nicht internal/) damit Wails App importieren kann.
package sticksetup

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	usbstick "github.com/JRpersonal/streborn/usb-stick"
)

// Drive beschreibt ein potenzielles Ziellaufwerk fuer den Stick Setup.
type Drive struct {
	Path        string `json:"path"`        // z.B. "D:\" oder "/Volumes/UNTITLED"
	Label       string `json:"label"`       // Volume Label oder ""
	TotalBytes  int64  `json:"totalBytes"`  // Groesse in Bytes
	FreeBytes   int64  `json:"freeBytes"`   // verfuegbar in Bytes
	Filesystem  string `json:"filesystem"`  // FAT32 / exFAT / NTFS
	Removable   bool   `json:"removable"`   // Wechseldatentraeger
	HasStick    bool   `json:"hasStick"`    // wurde schon mal als STR bestueckt?
	Description string `json:"description"` // Anzeige Hint fuer den User
}

// ListDrives findet alle entfernbaren Volumes die als Stick Ziel taugen.
// Auf Windows: alle Drive Letters die nicht Fixed sind. Auf Mac/Linux:
// alle Mounts unter /Volumes bzw /media die fat32 sind.
func ListDrives() ([]Drive, error) {
	switch runtime.GOOS {
	case "windows":
		return listDrivesWindows()
	case "darwin":
		return listDrivesMac()
	default:
		return listDrivesLinux()
	}
}

// WLANConfig beschreibt eine WLAN Konfiguration die auf den Stick
// geschrieben wird. Box's run.sh erkennt die Datei und provisioniert
// wpa_supplicant beim ersten Boot.
type WLANConfig struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
}

// WriteStickFiles bestueckt das angegebene Ziel mit den eingebetteten
// usb-stick Templates plus optional dem Stick Agent Binary.
//
// binaryBytes ist das Stick Agent Binary (ARM build). Wenn nil, wird
// nichts geschrieben — sinnvoll fuer "nur Templates aktualisieren" Flow.
// Setzt die Files mit dem korrekten Mode fuer FAT32 (Mode wird auf FAT32
// ignoriert, ist aber sauber).
//
// Returns Liste der geschriebenen Files.
func WriteStickFiles(targetPath string, binaryBytes []byte, stickVersion string) ([]string, error) {
	if targetPath == "" {
		return nil, fmt.Errorf("targetPath is empty")
	}
	st, err := os.Stat(targetPath)
	if err != nil {
		return nil, fmt.Errorf("target not reachable: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("target is not a directory: %s", targetPath)
	}

	written := []string{}

	// Embed Files
	stickFS := usbstick.Files()
	err = fs.WalkDir(stickFS, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		// files.go ist Go Code, nicht fuer den Stick gedacht.
		if strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := fs.ReadFile(stickFS, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(targetPath, path)
		if err := writeFile(dst, data); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		written = append(written, path)
		return nil
	})
	if err != nil {
		return written, err
	}

	// Binary schreiben falls vorhanden
	if len(binaryBytes) > 0 {
		dstName := "streborn-armv7l"
		dstPath := filepath.Join(targetPath, dstName)
		if err := writeFile(dstPath, binaryBytes); err != nil {
			return written, fmt.Errorf("write agent binary: %w", err)
		}
		written = append(written, dstName)
	}

	// remote_services Marker fuer Dauer-SSH — IMMER neu schreiben damit
	// auch alte Sticks einen aktuellen Zeitstempel bekommen und der
	// User sofort sieht dass das Setup wirklich gelaufen ist.
	markerPath := filepath.Join(targetPath, "remote_services")
	_ = writeFile(markerPath, []byte(""))
	written = append(written, "remote_services")

	// version.txt mit echter App Version (z.B. "1.0.0"). Wird IMMER neu
	// geschrieben damit nach einem Update Stick der Vergleich app == stick
	// passt und kein falsches "Update verfuegbar" Banner mehr erscheint.
	versionPath := filepath.Join(targetPath, "version.txt")
	v := strings.TrimSpace(stickVersion)
	if v == "" {
		v = "1.0.0"
	}
	_ = writeFile(versionPath, []byte(v))
	written = append(written, "version.txt")

	return written, nil
}

// IsBoseStick prueft ob auf dem Stick schon STR Files liegen
// (also der Stick bereits bestueckt wurde).
func IsBoseStick(path string) bool {
	marker := filepath.Join(path, "run.sh")
	if _, err := os.Stat(marker); err == nil {
		return true
	}
	return false
}

// StickVersion liest die version.txt vom Stick. Leer wenn die Datei
// nicht da oder leer ist.
func StickVersion(path string) string {
	b, err := os.ReadFile(filepath.Join(path, "version.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// StickConfigs liefert die noch nicht angewendeten Setup Konfigs vom
// Stick. Wenn der User den Stick noch nicht in die Box gesteckt hat,
// liegen wlan.conf / region.conf / name.conf darauf — die App nutzt
// die Werte zum Vorbefuellen der Wizard Felder.
type StickConfigs struct {
	WLANSSID string `json:"wlanSSID"`
	WLANPass string `json:"wlanPass"`
	Region   string `json:"region"`
	Name     string `json:"name"`
}

// ReadStickConfigs liest die conf Files vom Stick. Felder die nicht
// existieren oder leer sind bleiben leer im Ergebnis.
func ReadStickConfigs(path string) StickConfigs {
	out := StickConfigs{}
	if b, err := os.ReadFile(filepath.Join(path, "wlan.conf")); err == nil {
		var w WLANConfig
		if json.Unmarshal(b, &w) == nil {
			out.WLANSSID = w.SSID
			out.WLANPass = w.Password
		}
	}
	if b, err := os.ReadFile(filepath.Join(path, "region.conf")); err == nil {
		var r RegionConfig
		if json.Unmarshal(b, &r) == nil {
			out.Region = r.Country
		}
	}
	if b, err := os.ReadFile(filepath.Join(path, "name.conf")); err == nil {
		var n NameConfig
		if json.Unmarshal(b, &n) == nil {
			out.Name = n.Name
		}
	}
	return out
}

// WriteWLANConfig schreibt eine wlan.conf JSON Datei auf den Stick.
// Box's run.sh erkennt das beim ersten Boot und provisioniert
// wpa_supplicant entsprechend.
func WriteWLANConfig(targetPath string, cfg WLANConfig) error {
	if cfg.SSID == "" {
		return fmt.Errorf("SSID must not be empty")
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	dst := filepath.Join(targetPath, "wlan.conf")
	return writeFile(dst, data)
}

// RegionConfig haelt den vom User beim Setup gewaehlten Country Code
// (ISO 3166-1 alpha-2). Wird vom Stick beim Start gelesen und u.a. als
// Default fuer die Radio Suche Land und die Sprach Auswahl benutzt.
type RegionConfig struct {
	Country string `json:"country"`
}

// WriteRegionConfig schreibt region.conf JSON auf den Stick. Stick's
// run.sh persistiert es beim Bootstrap nach NAND und loescht die Datei
// vom Stick.
func WriteRegionConfig(targetPath string, cfg RegionConfig) error {
	if cfg.Country == "" {
		return fmt.Errorf("country must not be empty")
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	dst := filepath.Join(targetPath, "region.conf")
	return writeFile(dst, data)
}

// NameConfig haelt den vom User beim Setup gewaehlten Box Namen (z.B.
// "Wohnzimmer"). Wird beim ersten Boot vom Stick auf die Box via Bose
// REST API angewendet. Stick haengt die UID der Box noch automatisch an.
type NameConfig struct {
	Name string `json:"name"`
}

// WriteNameConfig schreibt eine name.conf JSON Datei auf den Stick.
func WriteNameConfig(targetPath string, cfg NameConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("name must not be empty")
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	dst := filepath.Join(targetPath, "name.conf")
	return writeFile(dst, data)
}

// Eject wirft das Volume aus. Plattformspezifisch implementiert.
func Eject(path string) error {
	return ejectImpl(path)
}

// FormatFAT32 formatiert den Stick neu als FAT32. ACHTUNG: alle Daten
// gehen verloren. Wird vom Setup Wizard vor dem Beschreiben angeboten
// damit der User nicht ausserhalb der App formatieren muss (Windows
// Dialog zeigt FAT32 fuer Sticks ab 32 GB nicht an).
func FormatFAT32(path, label string) error {
	return formatFAT32Impl(path, label)
}

// writeFile schreibt eine Datei atomar mit fsync. Wichtig: f.Sync()
// erzwingt FlushFileBuffers vor Close, sonst koennte Windows die Daten
// im Lazy Write Cache halten und beim Stick Ziehen ohne sauberen Eject
// gehen sie verloren — genau das hat dazu gefuehrt dass conf Files vom
// Setup nicht auf der Box ankamen.
func writeFile(path string, data []byte) error {
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
