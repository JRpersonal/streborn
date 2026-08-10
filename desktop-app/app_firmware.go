package main

// This file was split out of app.go (wave-1 move-only refactor):
// Bose firmware version lookup and comparison.

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// GetPresets calls GET /api/presets of the given stick.
// latestBoseFirmware is the final firmware Bose shipped for every SoundTouch
// model (27.0.6, 2022-08-04). There is nothing newer.
//
// An older box has to be brought up to it with Bose's USB update tool, NOT with
// the SoundTouch app: the app fetched firmware from the Bose cloud, so that
// route died with the shutdown. See the fw.* strings for the guidance users
// actually get.
const latestBoseFirmware = "27.0.6"

// FirmwareInfo is a speaker's Bose firmware + model, read from its :8090/info.
// Used as an install pre-flight (and on STR boxes too): STR shows the firmware,
// flags a box that is not on the latest Bose firmware, and includes it in
// install failures so an old firmware can be ruled in or out (#114).
type FirmwareInfo struct {
	Reachable  bool   `json:"reachable"`
	Model      string `json:"model"`      // <type>, e.g. "SoundTouch 20"
	Firmware   string `json:"firmware"`   // SCM softwareVersion, first token
	Short      string `json:"short"`      // human version, e.g. "27.0.6"
	ModuleType string `json:"moduleType"` // scm / sm2 / ...
	Variant    string `json:"variant"`    // taigan / rhino / ...
	Latest     string `json:"latest"`     // the latest Bose firmware (27.0.6)
	Outdated   bool   `json:"outdated"`   // older than Latest
}

// GetBoxFirmware reads :8090/info from a speaker (the Bose REST API, bound on
// 0.0.0.0 and LAN-reachable on stock AND STR boxes) and returns its model +
// firmware. The endpoint is on every SoundTouch, so this works before STR is
// installed as well as afterwards.
func (a *App) GetBoxFirmware(host string) (FirmwareInfo, error) {
	fi := FirmwareInfo{Latest: latestBoseFirmware}
	if host == "" {
		return fi, fmt.Errorf("host is required")
	}
	ctx, cancel := context.WithTimeout(a.appCtx(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s:8090/info", host), nil)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fi, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return fi, err
	}
	var info struct {
		Type       string `xml:"type"`
		ModuleType string `xml:"moduleType"`
		Variant    string `xml:"variant"`
		Components []struct {
			Category string `xml:"componentCategory"`
			Version  string `xml:"softwareVersion"`
		} `xml:"components>component"`
	}
	if err := xml.Unmarshal(body, &info); err != nil {
		return fi, fmt.Errorf("parse /info: %w", err)
	}
	fi.Reachable = true
	fi.Model = strings.TrimSpace(info.Type)
	fi.ModuleType = strings.TrimSpace(info.ModuleType)
	fi.Variant = strings.TrimSpace(info.Variant)
	// The SCM component carries the main firmware; fall back to the first
	// component that reports a version.
	ver := ""
	for _, c := range info.Components {
		if strings.EqualFold(strings.TrimSpace(c.Category), "SCM") && strings.TrimSpace(c.Version) != "" {
			ver = c.Version
			break
		}
	}
	if ver == "" {
		for _, c := range info.Components {
			if strings.TrimSpace(c.Version) != "" {
				ver = c.Version
				break
			}
		}
	}
	if f := strings.Fields(ver); len(f) > 0 {
		fi.Firmware = f[0] // drop the "epdbuild..." build tail after the space
	}
	fi.Short = shortFirmware(fi.Firmware)
	fi.Outdated = fi.Short != "" && firmwareOlder(fi.Short, latestBoseFirmware)
	return fi, nil
}

// shortFirmware reduces a "27.0.6.46330.5043500" version to its human "27.0.6".
func shortFirmware(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) >= 3 {
		return strings.Join(parts[:3], ".")
	}
	return v
}

// firmwareOlder reports whether version a is older than b, comparing the first
// three numeric segments (major.minor.patch).
func firmwareOlder(a, b string) bool {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}
