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

// MinStickBytes is the lower bound the desktop app enforces on an install
// stick. The agent plus templates are tiny (a few MB), but the install/setup
// experience assumes a normal "8 GB" USB stick, so a clearly undersized stick
// is rejected up front instead of failing cryptically later.
//
// The floor is 7.0 GB (decimal), not 8 GiB: a stick marketed as 8 GB measures
// roughly 7.4 to 7.7 GiB of usable capacity, so an 8 GiB threshold would wrongly
// reject genuine 8 GB sticks, while 7.0 GB still clears every 4 GB and smaller
// stick.
const MinStickBytes int64 = 7_000_000_000

// StickCheck is the technical verdict on whether a volume can be used as an STR
// install stick. The setup wizard calls CheckStick before letting the user
// proceed so a wrong-format, too-small or write-protected stick is caught with
// a clear message and (for the format case) an offered fix, instead of running
// the user into a later cryptic failure.
type StickCheck struct {
	OK         bool   `json:"ok"`         // true only when the stick is ready as-is
	Path       string `json:"path"`       // the volume that was checked
	Filesystem string `json:"filesystem"` // FAT32 / exFAT / NTFS / ""
	TotalBytes int64  `json:"totalBytes"` // capacity in bytes
	IsFAT32    bool   `json:"isFat32"`    // the speaker only reads FAT32
	BigEnough  bool   `json:"bigEnough"`  // TotalBytes >= MinStickBytes
	Writable   bool   `json:"writable"`   // a probe write succeeded
	// Reason is a stable machine-readable code the frontend maps to a localized
	// message: "" (ok), "gone", "too-small", "not-writable", "not-fat32".
	// Order of precedence is intentional: an unfixable problem (gone, too-small,
	// not-writable) wins over the fixable "not-fat32" (which the app offers to
	// format away).
	Reason string `json:"reason"`
}

// CheckStick evaluates the volume at path against the install requirements:
// present, large enough, writable and FAT32. It re-reads the drive list so the
// verdict reflects the current state (e.g. right after a format), and probes
// writability by creating and deleting a temp file.
func CheckStick(path string) StickCheck {
	c := StickCheck{Path: path}
	drives, _ := ListDrives()
	var found *Drive
	for i := range drives {
		if drives[i].Path == path {
			found = &drives[i]
			break
		}
	}
	if found == nil {
		c.Reason = "gone"
		return c
	}
	c.Filesystem = found.Filesystem
	c.TotalBytes = found.TotalBytes
	c.IsFAT32 = strings.EqualFold(found.Filesystem, "FAT32")
	c.BigEnough = found.TotalBytes >= MinStickBytes
	c.Writable = stickWritable(path)
	switch {
	case !c.BigEnough:
		c.Reason = "too-small"
	case !c.Writable:
		c.Reason = "not-writable"
	case !c.IsFAT32:
		c.Reason = "not-fat32"
	default:
		c.OK = true
	}
	return c
}

// stickWritable reports whether a small file can be created and removed under
// path. Catches a physically write-protected stick (lock switch) or a flaky
// reader before the agent write begins. Best-effort: any error means "not
// writable".
func stickWritable(path string) bool {
	probe := filepath.Join(path, ".str-write-test.tmp")
	if err := os.WriteFile(probe, []byte("str"), 0o644); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
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

// StickFileSet returns exactly the files WriteStickFiles would write to a USB
// stick, but in memory keyed by their stick-relative path. The desktop app's
// SSH repair fallback (RepairInstallViaSSH) uses it to stage install.sh +
// run.sh + rc.local + the agent binary on NAND and install from there when the
// USB stick itself is unreadable (large-cluster/faulty stick, exit 126). Kept
// in lock-step with WriteStickFiles so the SSH path installs the identical set.
func StickFileSet(binaryBytes []byte, stickVersion string) (map[string][]byte, error) {
	files := map[string][]byte{}
	stickFS := usbstick.Files()
	err := fs.WalkDir(stickFS, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := fs.ReadFile(stickFS, path)
		if err != nil {
			return err
		}
		files[path] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(binaryBytes) > 0 {
		files["streborn-armv7l"] = binaryBytes
	}
	files["remote_services"] = []byte("")
	v := strings.TrimSpace(stickVersion)
	if v == "" {
		v = "1.0.0"
	}
	files["version.txt"] = []byte(v)
	return files, nil
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
	Locale   string `json:"locale"`
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
	if b, err := os.ReadFile(filepath.Join(path, "lang.conf")); err == nil {
		var l LangConfig
		if json.Unmarshal(b, &l) == nil {
			out.Locale = l.Locale
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

// LangConfig haelt die Signale aus dem Setup-Wizard (aktives App-UI
// Locale, z.B. "de"/"en", und das vom User gewaehlte Land als ISO
// 3166-1 alpha-2) plus den daraus abgeleiteten Bose sysLanguage
// Integer. Der Stick liest das beim ersten Boot und nutzt SysLanguage
// fuer das OOB Language Gate UND als Display-Sprache der Box, statt eine
// fest verdrahtete Sprache zu erzwingen. Siehe project_bose_language_enum:
// Englisch=3 ist der weltweite Default.
type LangConfig struct {
	Locale      string `json:"locale"`
	Country     string `json:"country"`
	SysLanguage int    `json:"sysLanguage"`
}

// SysLanguageForLocale bildet einen BCP-47 Locale (oder nur das
// Primaer-Subtag) auf den Bose sysLanguage Integer ab. Single source
// of truth fuer alle Stellen die eine Sprache an die Box schicken
// (Stick lang.conf + Setup-AP Push). Unbekannte Locales fallen auf
// Englisch (3) zurueck, den neutralen weltweiten Default. 0 (unset)
// und 14 (undefiniert) werden nie zurueckgegeben. Voller Enum in
// project_bose_language_enum.
func SysLanguageForLocale(locale string) int {
	s := strings.ToLower(strings.TrimSpace(locale))
	// Chinesisch braucht das Region-Subtag fuer simplified vs traditional.
	switch {
	case strings.HasPrefix(s, "zh-tw"), strings.HasPrefix(s, "zh-hk"),
		strings.HasPrefix(s, "zh-mo"), strings.HasPrefix(s, "zh-hant"):
		return 11 // Traditional Chinese
	case strings.HasPrefix(s, "zh"):
		return 10 // Simplified Chinese
	}
	// Primaer-Subtag (vor '-' oder '_').
	primary := s
	for i := 0; i < len(s); i++ {
		if s[i] == '-' || s[i] == '_' {
			primary = s[:i]
			break
		}
	}
	switch primary {
	case "da":
		return 1
	case "de":
		return 2
	case "en":
		return 3
	case "es":
		return 4
	case "fr":
		return 5
	case "it":
		return 6
	case "nl":
		return 7
	case "sv":
		return 8
	case "ja":
		return 9
	case "ko":
		return 12
	case "th":
		return 13
	case "cs":
		return 15
	case "fi":
		return 16
	case "el":
		return 17
	case "nb", "nn", "no":
		return 18
	case "pl":
		return 19
	case "pt":
		return 20
	case "ro":
		return 21
	case "ru":
		return 22
	case "uk":
		// The box firmware has no Ukrainian in its sysLanguage enum.
		// Russian (22) is the most readable on-screen fallback for a
		// Ukrainian user (NOT English); the app UI itself never offers
		// Russian. The user can still override in the wizard dropdown.
		return 22
	case "sl":
		return 23
	case "tr":
		return 24
	case "hu":
		return 25
	default:
		return 3 // English, neutral worldwide default
	}
}

// SysLanguageForCountry maps an ISO 3166-1 alpha-2 country code to the
// Bose sysLanguage of its dominant language. Returns 0 for unknown /
// genuinely undecidable countries so callers fall back to another
// signal. Multilingual countries are mapped to their largest-share
// language (e.g. CH -> German, CA -> English); the user can always
// override in the wizard's language dropdown. Full enum in
// project_bose_language_enum.
func SysLanguageForCountry(cc string) int {
	switch strings.ToUpper(strings.TrimSpace(cc)) {
	case "DK", "GL", "FO":
		return 1 // Danish
	case "DE", "AT", "CH", "LI":
		return 2 // German
	case "US", "GB", "IE", "AU", "NZ", "CA", "ZA", "IN", "SG", "NG", "PH", "MT":
		return 3 // English
	case "ES", "MX", "AR", "CO", "CL", "PE", "VE", "EC", "GT", "CU", "BO",
		"DO", "HN", "PY", "SV", "NI", "CR", "PA", "UY":
		return 4 // Spanish
	case "FR", "LU", "MC", "SN", "CI":
		return 5 // French
	case "IT", "SM", "VA":
		return 6 // Italian
	case "NL", "BE", "SR":
		return 7 // Dutch
	case "SE":
		return 8 // Swedish
	case "JP":
		return 9 // Japanese
	case "CN":
		return 10 // Simplified Chinese
	case "TW", "HK", "MO":
		return 11 // Traditional Chinese
	case "KR", "KP":
		return 12 // Korean
	case "TH":
		return 13 // Thai
	case "CZ":
		return 15 // Czech
	case "FI":
		return 16 // Finnish
	case "GR", "CY":
		return 17 // Greek
	case "NO":
		return 18 // Norwegian
	case "PL":
		return 19 // Polish
	case "PT", "BR", "AO", "MZ":
		return 20 // Portuguese
	case "RO", "MD":
		return 21 // Romanian
	case "RU", "BY", "KZ", "KG":
		return 22 // Russian
	case "UA":
		return 22 // Ukraine: box has no Ukrainian, Russian is the most
		// readable on-screen fallback. App UI never offers Russian.
	case "SI":
		return 23 // Slovenian
	case "TR":
		return 24 // Turkish
	case "HU":
		return 25 // Hungarian
	default:
		return 0
	}
}

// SuggestBoxLanguage picks the Bose display language to PRE-SELECT in
// the setup wizard from the two signals it has: the user's active UI
// locale and the country they picked. The box supports ~25 languages
// but the app UI ships only a few bundles, so a user whose language has
// no bundle reads the app in English (locale "en") even though their
// country names their real language. Rule: a DELIBERATE non-English UI
// locale wins (the user actively switched to it); otherwise the country
// decides (covers most languages); English (3) is the floor. The user
// can still override the pre-selected value in the wizard.
func SuggestBoxLanguage(locale, country string) int {
	if p := localePrimary(locale); p != "" && p != "en" {
		return SysLanguageForLocale(locale)
	}
	if n := SysLanguageForCountry(country); n != 0 {
		return n
	}
	return SysLanguageForLocale(locale) // floors to English (3)
}

// localePrimary returns the lowercased primary subtag of a BCP-47 tag
// ("de-CH" -> "de"), or "" for an empty input.
func localePrimary(locale string) string {
	s := strings.ToLower(strings.TrimSpace(locale))
	for i := 0; i < len(s); i++ {
		if s[i] == '-' || s[i] == '_' {
			return s[:i]
		}
	}
	return s
}

// WriteLangConfig schreibt lang.conf JSON auf den Stick. locale +
// country sind die Wizard-Signale (fuer den Record), sysLanguage ist der
// vom User im Sprach-Dropdown bestaetigte Wert. Ein ungueltiger Wert
// (<=0, 14, oder >25) wird auf SuggestBoxLanguage zurueckgesetzt, damit
// run.sh den Integer ohne eigene Mapping-Tabelle direkt lesen kann.
func WriteLangConfig(targetPath string, locale, country string, sysLanguage int) error {
	loc := strings.TrimSpace(locale)
	if loc == "" {
		loc = "en"
	}
	n := sysLanguage
	if n <= 0 || n == 14 || n > 25 {
		n = SuggestBoxLanguage(loc, country)
	}
	cfg := LangConfig{Locale: loc, Country: strings.ToUpper(strings.TrimSpace(country)), SysLanguage: n}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	dst := filepath.Join(targetPath, "lang.conf")
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
//
// Close errors on the error path are intentionally discarded with
// `_ = f.Close()`: we are about to os.Remove the tmp file anyway,
// so a Close failure cannot lose data we still care about. The
// explicit discard signals to CodeQL (and humans) that this is a
// deliberate decision, not an oversight.
func writeFile(path string, data []byte) error {
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
