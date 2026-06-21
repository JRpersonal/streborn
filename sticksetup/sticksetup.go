// Package sticksetup contains the Windows/Mac logic for provisioning a
// fresh SD stick: drive discovery, file copy, optional eject. Used by
// the desktop app; not part of the stick agent.
//
// Top-level package (not internal/) so the Wails app can import it.
package sticksetup

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	usbstick "github.com/JRpersonal/streborn/usb-stick"
)

// Logger is the package logger. The desktop app points it at its file
// logger (see NewApp) so drive-discovery timing lands in str.log; until
// then it falls back to the default logger. Mirrors dlna.Logger.
var Logger = slog.Default()

// Drive describes a potential target drive for the stick setup.
type Drive struct {
	Path        string `json:"path"`        // e.g. "D:\" or "/Volumes/UNTITLED"
	Label       string `json:"label"`       // volume label or ""
	TotalBytes  int64  `json:"totalBytes"`  // size in bytes
	FreeBytes   int64  `json:"freeBytes"`   // available in bytes
	Filesystem  string `json:"filesystem"`  // FAT32 / exFAT / NTFS
	Removable   bool   `json:"removable"`   // removable medium
	HasStick    bool   `json:"hasStick"`    // already provisioned as STR before?
	Description string `json:"description"` // display hint for the user
}

// ListDrives finds all removable volumes that qualify as a stick target.
// On Windows: all drive letters that are not fixed. On Mac/Linux: all
// mounts under /Volumes or /media that are fat32.
func ListDrives() ([]Drive, error) {
	start := time.Now()
	var drives []Drive
	var err error
	switch runtime.GOOS {
	case "windows":
		drives, err = listDrivesWindows()
	case "darwin":
		drives, err = listDrivesMac()
	default:
		drives, err = listDrivesLinux()
	}
	// Timing matters: a freshly inserted stick can leave Windows still
	// mounting it, so the per-drive volume queries block for seconds
	// (user-reported 10-20s with nothing showing). Logged so a slow
	// search is measurable instead of an invisible hang.
	ms := time.Since(start).Milliseconds()
	if err != nil {
		Logger.Warn("ListDrives failed", "ms", ms, "err", err)
	} else {
		Logger.Info("ListDrives done", "ms", ms, "count", len(drives))
	}
	return drives, err
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

// WLANConfig describes a WLAN configuration written to the stick. The
// box's run.sh detects the file and provisions wpa_supplicant on the
// first boot.
type WLANConfig struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
}

// WriteStickFiles provisions the given target with the embedded usb-stick
// templates plus, optionally, the stick agent binary.
//
// binaryBytes is the stick agent binary (ARM build). If nil, nothing is
// written — useful for the "update templates only" flow. Sets the files
// with the correct mode for FAT32 (mode is ignored on FAT32, but it is
// clean).
//
// Returns the list of written files.
func WriteStickFiles(targetPath string, binaryBytes, goLibrespotBytes []byte, stickVersion string) ([]string, error) {
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

	// Embed files
	stickFS := usbstick.Files()
	err = fs.WalkDir(stickFS, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		// files.go is Go code, not meant for the stick.
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

	// Write the binary if present
	if len(binaryBytes) > 0 {
		dstName := "streborn-armv7l"
		dstPath := filepath.Join(targetPath, dstName)
		if err := writeFile(dstPath, binaryBytes); err != nil {
			return written, fmt.Errorf("write agent binary: %w", err)
		}
		written = append(written, dstName)
	}

	// go-librespot Spotify sidecar (#45/#78): write it to the stick so the
	// boot-time stick->NAND sync (usb-stick/run.sh) installs it to
	// /mnt/nv/streborn/bin/go-librespot, where the agent runs it as the Spotify
	// Connect receiver. Without this the binary never reaches a normally-installed
	// box and Spotify presets fail. Empty on a dev app build (the go:embed stub was
	// not replaced by CI); skip rather than write 0 bytes over a good one.
	if len(goLibrespotBytes) > 0 {
		const glrName = "go-librespot"
		if err := writeFile(filepath.Join(targetPath, glrName), goLibrespotBytes); err != nil {
			return written, fmt.Errorf("write go-librespot: %w", err)
		}
		written = append(written, glrName)
	}

	// remote_services marker for persistent SSH — ALWAYS rewrite so that
	// even old sticks get a current timestamp and the user immediately
	// sees that the setup really ran.
	markerPath := filepath.Join(targetPath, "remote_services")
	_ = writeFile(markerPath, []byte(""))
	written = append(written, "remote_services")

	// version.txt with the real app version (e.g. "1.0.0"). ALWAYS
	// rewritten so that after an update stick the comparison app == stick
	// holds and no false "update available" banner appears anymore.
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
func StickFileSet(binaryBytes, goLibrespotBytes []byte, stickVersion string) (map[string][]byte, error) {
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
	if len(goLibrespotBytes) > 0 {
		files["go-librespot"] = goLibrespotBytes
	}
	files["remote_services"] = []byte("")
	v := strings.TrimSpace(stickVersion)
	if v == "" {
		v = "1.0.0"
	}
	files["version.txt"] = []byte(v)
	return files, nil
}

// IsBoseStick checks whether STR files are already on the stick (i.e.
// the stick has already been provisioned).
func IsBoseStick(path string) bool {
	marker := filepath.Join(path, "run.sh")
	if _, err := os.Stat(marker); err == nil {
		return true
	}
	return false
}

// StickVersion reads version.txt from the stick. Empty if the file is
// not there or empty.
func StickVersion(path string) string {
	b, err := os.ReadFile(filepath.Join(path, "version.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// StickConfigs returns the not-yet-applied setup configs from the stick.
// If the user has not yet inserted the stick into the box, wlan.conf /
// region.conf / name.conf are on it — the app uses the values to prefill
// the wizard fields.
type StickConfigs struct {
	WLANSSID string `json:"wlanSSID"`
	WLANPass string `json:"wlanPass"`
	Region   string `json:"region"`
	Name     string `json:"name"`
	Locale   string `json:"locale"`
}

// ReadStickConfigs reads the conf files from the stick. Fields that do
// not exist or are empty stay empty in the result.
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

// WriteWLANConfig writes a wlan.conf JSON file to the stick. The box's
// run.sh detects it on the first boot and provisions wpa_supplicant
// accordingly.
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

// RegionConfig holds the country code (ISO 3166-1 alpha-2) the user
// chose during setup. Read by the stick at startup and used, among other
// things, as the default for the radio search country and the language
// selection.
type RegionConfig struct {
	Country string `json:"country"`
}

// WriteRegionConfig writes region.conf JSON to the stick. The stick's
// run.sh persists it to NAND during bootstrap and deletes the file from
// the stick.
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

// NameConfig holds the box name the user chose during setup (e.g.
// "Living Room"). Applied to the box from the stick on the first boot via
// the Bose REST API. The stick automatically appends the box's UID.
type NameConfig struct {
	Name string `json:"name"`
}

// WriteNameConfig writes a name.conf JSON file to the stick.
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

// LangConfig holds the signals from the setup wizard (active app UI
// locale, e.g. "de"/"en", and the country the user chose as ISO
// 3166-1 alpha-2) plus the Bose sysLanguage integer derived from them.
// The stick reads this on the first boot and uses SysLanguage for the
// OOB language gate AND as the box's display language, instead of
// forcing a hard-wired language. See project_bose_language_enum:
// English=3 is the worldwide default.
type LangConfig struct {
	Locale      string `json:"locale"`
	Country     string `json:"country"`
	SysLanguage int    `json:"sysLanguage"`
}

// SysLanguageForLocale maps a BCP-47 locale (or just the primary subtag)
// to the Bose sysLanguage integer. Single source of truth for everywhere
// that sends a language to the box (stick lang.conf + setup-AP push).
// Unknown locales fall back to English (3), the neutral worldwide
// default. 0 (unset) and 14 (undefined) are never returned. Full enum in
// project_bose_language_enum.
func SysLanguageForLocale(locale string) int {
	s := strings.ToLower(strings.TrimSpace(locale))
	// Chinese needs the region subtag for simplified vs traditional.
	switch {
	case strings.HasPrefix(s, "zh-tw"), strings.HasPrefix(s, "zh-hk"),
		strings.HasPrefix(s, "zh-mo"), strings.HasPrefix(s, "zh-hant"):
		return 11 // Traditional Chinese
	case strings.HasPrefix(s, "zh"):
		return 10 // Simplified Chinese
	}
	// Primary subtag (before '-' or '_').
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

// WriteLangConfig writes lang.conf JSON to the stick. locale + country
// are the wizard signals (for the record), sysLanguage is the value the
// user confirmed in the language dropdown. An invalid value (<=0, 14, or
// >25) is reset to SuggestBoxLanguage so run.sh can read the integer
// directly without its own mapping table.
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

// Eject ejects the volume. Implemented per platform.
func Eject(path string) error {
	return ejectImpl(path)
}

// FormatFAT32 reformats the stick as FAT32. WARNING: all data is lost.
// Offered by the setup wizard before writing so the user does not have to
// format outside the app (the Windows dialog does not show FAT32 for
// sticks of 32 GB and up).
func FormatFAT32(path, label string) error {
	return formatFAT32Impl(path, label)
}

// writeFile writes a file atomically with fsync. Important: f.Sync()
// forces FlushFileBuffers before Close, otherwise Windows could keep the
// data in the lazy write cache and it would be lost when the stick is
// pulled without a clean eject — exactly what caused conf files from the
// setup not to reach the box.
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
