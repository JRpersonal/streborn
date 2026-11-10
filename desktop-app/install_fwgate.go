package main

// The setup wizard's firmware gate for the USB stick.
//
// The install over the network stops at "firmware-too-old" before it touches a
// speaker whose Bose firmware is far behind 27.0.6 (installPreflight). The
// USB stick section of the same wizard used to take no notice: in a support
// case (2026-11) the preflight said firmware-too-old for a SoundTouch 20 on
// 9.0.41, and two minutes later the wizard wrote its 14 install files to the
// stick anyway. The owner then put Bose's Update.stu beside them for the
// firmware ladder, and the speaker never started that update. A speaker that
// needs the firmware update first gets the firmware update guide instead of
// STR's files.

import (
	"strings"
	"sync"
	"time"
)

// fwTooOldVerdictTTL bounds how long an install's firmware-too-old verdict
// stands in for a speaker that does not answer: long enough for an owner who
// goes from the failed install to the stick, short enough that a speaker
// updated by other means is not held back for days.
const fwTooOldVerdictTTL = 12 * time.Hour

// fwTooOldVerdict is what the install read off the speaker when it stopped at
// firmware-too-old.
type fwTooOldVerdict struct {
	at                                   time.Time
	firmware, model, moduleType, variant string
}

var fwTooOldVerdicts = struct {
	mu sync.Mutex
	m  map[string]fwTooOldVerdict
}{m: map[string]fwTooOldVerdict{}}

// The clock and the /info read are installSeams.now and readFirmware
// (install_seams.go).

func (a *App) rememberFirmwareTooOld(host string, v fwTooOldVerdict) {
	host = strings.TrimSpace(host)
	if host == "" {
		return
	}
	v.at = a.installSeams().now()
	fwTooOldVerdicts.mu.Lock()
	fwTooOldVerdicts.m[host] = v
	fwTooOldVerdicts.mu.Unlock()
}

func forgetFirmwareTooOld(host string) {
	fwTooOldVerdicts.mu.Lock()
	delete(fwTooOldVerdicts.m, strings.TrimSpace(host))
	fwTooOldVerdicts.mu.Unlock()
}

func (a *App) recentFirmwareTooOld(host string) (fwTooOldVerdict, bool) {
	fwTooOldVerdicts.mu.Lock()
	defer fwTooOldVerdicts.mu.Unlock()
	v, ok := fwTooOldVerdicts.m[strings.TrimSpace(host)]
	if !ok {
		return fwTooOldVerdict{}, false
	}
	if a.installSeams().now().Sub(v.at) > fwTooOldVerdictTTL {
		delete(fwTooOldVerdicts.m, strings.TrimSpace(host))
		return fwTooOldVerdict{}, false
	}
	return v, true
}

// firmwareTooOldForInstall is the install preflight's own rule: outdated, and
// below the oldest firmware the install has ever opened.
func firmwareTooOldForInstall(fi FirmwareInfo) bool {
	return fi.Outdated && unlockNeedsNewerFirmware(fi.Short)
}

// StickFirmwareGate is the wizard's answer before it writes the install stick.
type StickFirmwareGate struct {
	// Blocked: the speaker needs the firmware update first; the wizard shows
	// the firmware update guide and writes nothing.
	Blocked    bool   `json:"blocked"`
	Firmware   string `json:"firmware"` // short, e.g. "9.0.41"
	Model      string `json:"model"`
	ModuleType string `json:"moduleType"`
	Variant    string `json:"variant"`
	// Source says what the verdict rests on: "speaker" (read just now) or
	// "install" (the install's firmware-too-old verdict, the speaker did not
	// answer now). Empty when not blocked.
	Source string `json:"source,omitempty"`
}

// StickInstallFirmwareGate says whether the setup wizard may write STR's
// install files to a stick for the speaker at host. The speaker's own answer
// decides when it gives one; otherwise a firmware-too-old verdict of the
// install from the last 12 hours does. No host (a factory-reset speaker the
// wizard does not know yet), no answer and no verdict: not blocked.
func (a *App) StickInstallFirmwareGate(host string) StickFirmwareGate {
	host = strings.TrimSpace(host)
	if host == "" {
		return StickFirmwareGate{}
	}
	seams := a.installSeams()
	fi, err := seams.readFirmware(host)
	if err == nil && fi.Reachable && fi.Short != "" {
		if !firmwareTooOldForInstall(fi) {
			forgetFirmwareTooOld(host)
			return StickFirmwareGate{}
		}
		a.rememberFirmwareTooOld(host, fwTooOldVerdict{firmware: fi.Short, model: fi.Model, moduleType: fi.ModuleType, variant: fi.Variant})
		a.fwLog().Warn("setup: firmware too old, not writing the STR install stick; showing the firmware update instead",
			"host", host, "firmware", fi.Short, "model", fi.Model, "source", "speaker")
		return StickFirmwareGate{Blocked: true, Firmware: fi.Short, Model: fi.Model, ModuleType: fi.ModuleType, Variant: fi.Variant, Source: "speaker"}
	}
	if v, ok := a.recentFirmwareTooOld(host); ok {
		a.fwLog().Warn("setup: firmware too old, not writing the STR install stick; showing the firmware update instead",
			"host", host, "firmware", v.firmware, "model", v.model, "source", "install", "verdictAge", seams.now().Sub(v.at).Round(time.Second))
		return StickFirmwareGate{Blocked: true, Firmware: v.firmware, Model: v.model, ModuleType: v.moduleType, Variant: v.variant, Source: "install"}
	}
	return StickFirmwareGate{}
}
