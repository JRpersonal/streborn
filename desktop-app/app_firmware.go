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

	"streborn-app/bosefw"
)

// latestBoseFirmware is the final firmware Bose shipped for every SoundTouch
// model (27.0.6, 2022-08-04). There is nothing newer.
//
// An older box has to be brought up to it with Bose's own update file, uploaded
// to the speaker's built-in update page (http://<speaker>:17008/update.html, in
// setup mode or over a USB cable), NOT with the SoundTouch app: the app fetched
// firmware from the Bose cloud, so that route died with the shutdown. The
// firmware update guide (frontend/src/fwguide.js) walks the user through it,
// and GetFirmwareGuide below picks the matching file on Bose's download host.
const latestBoseFirmware = "27.0.6"

// boseCatalogue is Bose's model index, fetched once per session on first use
// and held in memory only. STR links files on Bose's host and never downloads
// or redistributes a firmware image itself.
var boseCatalogue = &bosefw.Catalogue{
	Client:  &http.Client{Timeout: 6 * time.Second},
	Timeout: 5 * time.Second,
}

// FirmwareGuide is what the firmware update guide needs beyond the strings:
// the version gap and the exact Bose download(s) for this speaker.
type FirmwareGuide struct {
	Model    string `json:"model"`
	Current  string `json:"current"`
	Required string `json:"required"`
	// Files is one file normally, two when the model exists on both hardware
	// generations and the speaker did not say which it is (older hardware
	// first), and empty when the model is not in Bose's catalogue.
	Files []bosefw.File `json:"files"`
	// Live is false when Bose's index could not be fetched and the built-in
	// table answered instead.
	Live bool `json:"live"`
}

// GetFirmwareGuide maps a speaker (model = <type>, moduleType, variant as read
// from its :8090/info) to the matching update file in Bose's catalogue. It takes
// the identity rather than a host because the guide is shown on install-failure
// screens too, where the speaker may be rebooting and not answer.
func (a *App) GetFirmwareGuide(model, moduleType, variant, current string) FirmwareGuide {
	return firmwareGuideFor(a.appCtx(), model, moduleType, variant, current)
}

func firmwareGuideFor(ctx context.Context, model, moduleType, variant, current string) FirmwareGuide {
	entries, live := boseCatalogue.Entries(ctx)
	files := bosefw.Match(entries, model, moduleType, variant)
	if files == nil {
		files = []bosefw.File{}
	}
	return FirmwareGuide{
		Model:    strings.TrimSpace(model),
		Current:  shortFirmware(strings.TrimSpace(current)),
		Required: latestBoseFirmware,
		Files:    files,
		Live:     live,
	}
}

// firmwareTooOldNote is the sentence every install message carries when the
// speaker's firmware is behind Bose's last one. It names the route that still
// works, points at the step-by-step guide the setup screen renders under the
// message, and carries the direct Bose link so the copied failure report is
// useful on its own.
func firmwareTooOldNote(short string, files []bosefw.File) string {
	note := " The speaker firmware is " + short + ", older than Bose's last firmware " + latestBoseFirmware +
		", and STR needs it updated first. Since the Bose cloud shut down the SoundTouch app cannot deliver it any more, " +
		"so the update file is downloaded from Bose and uploaded to the speaker's own update page: " +
		"the firmware update guide below walks you through it step by step."
	switch len(files) {
	case 1:
		note += " Bose's download for this speaker: " + files[0].URL
	case 2:
		note += " Bose's downloads for this model: " + files[0].URL + " (older hardware) or " + files[1].URL +
			" (newer hardware). If the speaker's update page refuses the first file, use the second."
	}
	return note
}

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
