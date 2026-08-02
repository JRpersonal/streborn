package main

// This file was split out of app.go (wave-1 move-only refactor):
// USB stick setup: drives, formatting, stick files, and Wi-Fi/region/name/language configs.

import (
	"strings"

	"streborn-app/agentbin"

	"github.com/JRpersonal/streborn/sticksetup"
	"github.com/JRpersonal/streborn/wifiprofiles"
)

// ---- Stick Setup ----

// ListDrives returns all removable volumes that are suitable as a stick target.
// The frontend uses this in the setup wizard.
func (a *App) ListDrives() ([]sticksetup.Drive, error) {
	return sticksetup.ListDrives()
}

// FormatStick reformats the stick as FAT32. WARNING: all data
// is lost. Called before WriteStickFiles when the user has enabled the
// "Format stick first" checkbox.
func (a *App) FormatStick(targetPath string) error {
	// Log the prepare step so a stick-boot install (the ST10 path, which
	// never touches the SSH installer that does log) leaves a trail in the
	// diagnostic bundle. Without this a failed self-install shows only
	// discovery noise and the cause is invisible (see #195).
	a.logger.Info("FormatStick", "comp", "sticksetup", "target", targetPath)
	err := sticksetup.FormatFAT32(targetPath, "REBORN")
	if err != nil {
		a.logger.Warn("FormatStick failed", "comp", "sticksetup", "target", targetPath, "err", err)
	}
	return err
}

// WriteStickFiles populates the given volume with all the necessary
// files (templates plus the embedded Stick Agent binary). The binary
// is embedded at app build time and needs no path from the user.
// The app version PLUS build stamp is written to version.txt
// (format "1.0.0+2026-05-15-2202") so that the update detector also
// recognizes build differences when the version number is the same.
func (a *App) WriteStickFiles(targetPath string) ([]string, error) {
	v := appVersion
	if appBuild != "" && appBuild != "dev" {
		v = appVersion + "+" + appBuild
	}
	files, err := sticksetup.WriteStickFiles(targetPath, agentbin.Bytes(), agentbin.GoLibrespotBytes(), v)
	// Record what was staged onto the stick. agentBytes confirms the embedded
	// agent is present (0 on a dev build, which is itself the cause of a
	// non-starting agent), and the file count/version pins the attempt in the
	// bundle so a later stick-boot failure is traceable (see #195).
	a.logger.Info("WriteStickFiles",
		"comp", "sticksetup", "target", targetPath, "version", v,
		"agentBytes", len(agentbin.Bytes()), "fileCount", len(files), "err", err)
	return files, err
}

// WriteWLANConfig writes a Wi-Fi config onto the stick. Optional before
// the eject; the box's run.sh detects it on first boot.
func (a *App) WriteWLANConfig(targetPath, ssid, password string) error {
	return sticksetup.WriteWLANConfig(targetPath, sticksetup.WLANConfig{
		SSID: ssid, Password: password,
	})
}

// WriteRegionConfig writes a region.conf JSON file (ISO 3166-1
// alpha-2 country code) onto the stick. The stick persists it on boot
// to NAND and uses it as the default for radio search and language.
func (a *App) WriteRegionConfig(targetPath, country string) error {
	return sticksetup.WriteRegionConfig(targetPath, sticksetup.RegionConfig{Country: country})
}

// WriteNameConfig writes a name.conf JSON file with the box name
// requested by the user onto the stick. The stick applies it on first
// boot to the box via the Bose REST API, verbatim, so the user's chosen
// name stays clean (#133, #292).
func (a *App) WriteNameConfig(targetPath, name string) error {
	return sticksetup.WriteNameConfig(targetPath, sticksetup.NameConfig{Name: name})
}

// WriteLangConfig writes lang.conf onto the stick. locale + country
// are the wizard signals, sysLanguage the Bose value chosen by the user
// in the language dropdown. The box's run.sh reads the integer on first
// boot as the OOB-gate language AND display language, instead of forcing
// German worldwide. See project_bose_language_enum.
func (a *App) WriteLangConfig(targetPath, locale, country string, sysLanguage int) error {
	return sticksetup.WriteLangConfig(targetPath, locale, country, sysLanguage)
}

// SuggestBoxLanguage returns the Bose sysLanguage that the setup wizard
// should preselect in the language dropdown: derived primarily from the
// chosen country, with the active app language as a deliberate override,
// otherwise English. The frontend calls it on load and on every country change.
func (a *App) SuggestBoxLanguage(locale, country string) int {
	return sticksetup.SuggestBoxLanguage(locale, country)
}

// SetAppLocale remembers the user's UI-active language (BCP-47,
// e.g. "de"/"en"). The frontend calls it at startup and on every
// language change. Server-side provisioning paths (setup-AP push)
// derive the box display language from it.
func (a *App) SetAppLocale(locale string) {
	a.localeMu.Lock()
	a.userLocale = strings.TrimSpace(locale)
	a.localeMu.Unlock()
}

// appLocale returns the most recently reported UI locale (empty if none
// has been set yet).
func (a *App) appLocale() string {
	a.localeMu.RLock()
	defer a.localeMu.RUnlock()
	return a.userLocale
}

// ListWiFiProfiles returns the saved Wi-Fi profiles from the host OS.
// The frontend uses this as a dropdown in setup so the user does not have
// to type the SSID.
func (a *App) ListWiFiProfiles() ([]wifiprofiles.Profile, error) {
	return wifiprofiles.List()
}

// TryWiFiPassword tries to read the saved password for an SSID.
// On Windows this works for profiles the user saved themselves
// without admin rights. On Mac/Linux it may need user consent.
// Returns empty when nothing is found.
func (a *App) TryWiFiPassword(ssid string) string {
	pw, _ := wifiprofiles.TryPassword(ssid)
	return pw
}

// CurrentWiFi returns the SSID of the currently connected Wi-Fi. Used in the UI
// as the default selection in the dropdown.
func (a *App) CurrentWiFi() string {
	return wifiprofiles.CurrentSSID()
}

// IsBoseStick true when the volume already holds an STR install.
func (a *App) IsBoseStick(path string) bool {
	return sticksetup.IsBoseStick(path)
}

// StickVersion reads version.txt from the stick.
func (a *App) StickVersion(path string) string {
	return sticksetup.StickVersion(path)
}

// CheckStick technically checks whether the volume is suitable as an install stick
// (present, FAT32, large enough, writable). The setup wizard calls this
// before letting the user proceed, so that an NTFS/exFAT, too-small or
// write-protected stick is caught early with a clear message instead of
// running into a later cryptic error.
func (a *App) CheckStick(path string) sticksetup.StickCheck {
	return sticksetup.CheckStick(path)
}

// StickConfigs returns not-yet-applied setup configs from the stick
// (wlan, region, name). Used to pre-fill the wizard.
func (a *App) StickConfigs(path string) sticksetup.StickConfigs {
	return sticksetup.ReadStickConfigs(path)
}
