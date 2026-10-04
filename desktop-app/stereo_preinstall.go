// A stereo pair that predates STR, and the install that must not break it.
//
// Owners of two SoundTouch 10s very often paired them in the Bose app before
// the cloud shutdown. The pair lives in the firmware of the speakers, and the
// first STR install on one of them used to break it: the STR half dropped the
// pair after its first boot (marge answered the group poll wrongly), the stock
// half kept it and waited for a partner that had let go, and refused to play on
// its own (field case, 2026-10-04: "the second speaker does not come out of
// standby", a reinstall and a restart did not help).
//
// The agent now adopts a healthy pair instead of dropping it
// (internal/marge/groupadopt.go). This file is the app's half: it reads the pair
// before the install, writes it down before either speaker is touched, offers to
// keep it (install STR on both) or to dissolve it cleanly on both halves first
// (install only this one), and once both halves run STR it makes sure the pair
// is whole again.

package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PairMember is one half of a stereo pair as the firmware reports it.
type PairMember struct {
	DeviceID string `json:"deviceID"`
	Role     string `json:"role"`
	IP       string `json:"ip"`
}

// SavedPair is a pair written down before an install touched it.
type SavedPair struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	MasterDeviceID string       `json:"masterDeviceID"`
	Members        []PairMember `json:"members"`
	SavedAt        time.Time    `json:"savedAt"`
}

// StereoInstallCheck is what the setup wizard needs to decide before installing.
type StereoInstallCheck struct {
	// Known is false when the speaker's group could not be read (some chassis
	// hang on /getGroup); the install then proceeds as it always has.
	Known         bool      `json:"known"`
	Paired        bool      `json:"paired"`
	Pair          SavedPair `json:"pair"`
	Status        string    `json:"status"`
	PartnerIP     string    `json:"partnerIP"`
	PartnerOnline bool      `json:"partnerOnline"`
	PartnerHasSTR bool      `json:"partnerHasSTR"`
}

// fwGroup is the firmware's /getGroup document.
type fwGroup struct {
	ID             string `xml:"id,attr"`
	Name           string `xml:"name"`
	MasterDeviceID string `xml:"masterDeviceId"`
	Status         string `xml:"status"`
	Roles          []struct {
		DeviceID string `xml:"deviceId"`
		Role     string `xml:"role"`
		IP       string `xml:"ipAddress"`
	} `xml:"roles>groupRole"`
}

func (g fwGroup) toPair() SavedPair {
	p := SavedPair{ID: strings.TrimSpace(g.ID), Name: strings.TrimSpace(g.Name), MasterDeviceID: strings.TrimSpace(g.MasterDeviceID)}
	for _, r := range g.Roles {
		p.Members = append(p.Members, PairMember{
			DeviceID: strings.TrimSpace(r.DeviceID), Role: strings.TrimSpace(r.Role), IP: strings.TrimSpace(r.IP),
		})
	}
	return p
}

func (g fwGroup) empty() bool {
	return strings.TrimSpace(g.ID) == "" && len(g.Roles) == 0 && strings.TrimSpace(g.MasterDeviceID) == ""
}

// firmwareGet reads a path from the speaker's stock firmware API on :8090.
// Seam for tests.
var firmwareGet = func(ctx context.Context, host, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, "8090")+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<16))
}

// readFirmwareGroup reads /getGroup with a short budget. Some chassis (scm/BCO)
// hang on it; a timeout means "unknown", never "not paired".
func readFirmwareGroup(host string) (fwGroup, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	b, err := firmwareGet(ctx, host, "/getGroup")
	if err != nil {
		return fwGroup{}, err
	}
	var g fwGroup
	if err := xml.Unmarshal(b, &g); err != nil {
		return fwGroup{}, err
	}
	return g, nil
}

// partnerOf returns the member of p that is not at host.
func partnerOf(p SavedPair, host string) (PairMember, bool) {
	for _, m := range p.Members {
		if m.IP != "" && !strings.EqualFold(m.IP, host) {
			return m, true
		}
	}
	return PairMember{}, false
}

// agentAnswers reports whether STR's agent answers on the speaker. Seam.
var agentAnswers = func(a *App, host string) bool {
	_, err := a.BoxAgentVersion(host, 0)
	return err == nil
}

// firmwareAnswers reports whether the speaker's stock API answers. Seam.
var firmwareAnswers = func(host string) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, "8090"), 2*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// CheckStereoBeforeInstall reads the speaker's own group before an install and
// writes a pair down, so nothing that happens to either speaker afterwards can
// lose it.
func (a *App) CheckStereoBeforeInstall(host string) (StereoInstallCheck, error) {
	g, err := readFirmwareGroup(host)
	if err != nil {
		a.logger.Info("stereo pre-install: could not read the speaker's group, installing as usual", "host", host, "err", err)
		return StereoInstallCheck{}, nil
	}
	out := StereoInstallCheck{Known: true, Status: strings.TrimSpace(g.Status)}
	if g.empty() || len(g.Roles) < 2 {
		return out, nil
	}
	out.Paired = true
	out.Pair = g.toPair()
	out.Pair.SavedAt = time.Now()
	if p, ok := partnerOf(out.Pair, host); ok {
		out.PartnerIP = p.IP
		out.PartnerOnline = firmwareAnswers(p.IP)
		out.PartnerHasSTR = out.PartnerOnline && agentAnswers(a, p.IP)
	}
	if err := savePendingPair(out.Pair); err != nil {
		a.logger.Warn("stereo pre-install: could not write the pair down", "err", err)
	}
	a.logger.Info("stereo pre-install: the speaker is half of a stereo pair",
		"host", host, "name", out.Pair.Name, "status", out.Status, "partnerIP", out.PartnerIP,
		"partnerOnline", out.PartnerOnline, "partnerHasSTR", out.PartnerHasSTR)
	return out, nil
}

// DissolvePairBeforeInstall dissolves a pair on every given speaker that holds
// it, with the firmware's own /removeGroup (works without the Bose cloud,
// measured 2026-10-04), and reads each one back. It is for "install only this
// one": a half left holding the pair would wait for its partner for good.
func (a *App) DissolvePairBeforeInstall(hosts []string) (map[string]any, error) {
	results := map[string]string{}
	allClear := true
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		g, err := readFirmwareGroup(h)
		if err != nil {
			results[h] = "unreadable"
			allClear = false
			continue
		}
		if g.empty() {
			results[h] = "notPaired"
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		_, rerr := firmwareGet(ctx, h, "/removeGroup")
		cancel()
		if rerr != nil {
			a.logger.Warn("stereo pre-install: removeGroup failed", "host", h, "err", rerr)
		}
		if g2, err := readFirmwareGroup(h); err == nil && g2.empty() {
			results[h] = "dissolved"
		} else {
			results[h] = "stillPaired"
			allClear = false
		}
	}
	a.logger.Info("stereo pre-install: dissolve before install", "results", results, "allClear", allClear)
	return map[string]any{"ok": allClear, "results": results}, nil
}

// RestorePairAfterInstall checks, after an install, whether a pair written
// down before it now has STR on both halves, and if so makes sure the pair is
// whole: both firmwares report it, or it is formed again through STR's verified
// pairing with the name and sides it had.
//
// status: "none" (no saved pair for this speaker), "waitingForPartner" (the
// other half still needs STR), "kept" (both halves hold it), "restored"
// (formed again), "failed" (could not be formed; reason/error say why).
func (a *App) RestorePairAfterInstall(host string) (map[string]any, error) {
	pairs, _ := loadPendingPairs()
	for _, p := range pairs {
		if !pairHasHost(p, host) {
			continue
		}
		partner, _ := partnerOf(p, host)
		for _, m := range p.Members {
			if !agentAnswers(a, m.IP) {
				return map[string]any{"status": "waitingForPartner", "name": p.Name, "partnerIP": partner.IP}, nil
			}
		}
		left, right, ok := pairSides(p)
		if !ok {
			_ = dropPendingPair(p)
			return map[string]any{"status": "failed", "error": "the saved pair has no left and right side"}, nil
		}
		if bothHold(p, left.IP, right.IP) {
			a.installCanonicalPair(p, left, right)
			_ = dropPendingPair(p)
			a.logger.Info("stereo post-install: the pair survived the install on both speakers", "name", p.Name)
			return map[string]any{"status": "kept", "name": p.Name}, nil
		}
		res, err := a.FormZone(left.IP, 0, ZoneSpec{
			Master: ZoneMember{DeviceID: left.DeviceID, IP: left.IP},
			Slaves: []ZoneMember{{DeviceID: right.DeviceID, IP: right.IP}},
			Name:   p.Name, Stereo: true,
		})
		_ = dropPendingPair(p)
		if err != nil {
			return map[string]any{"status": "failed", "name": p.Name, "error": err.Error()}, nil
		}
		if okv, _ := res["ok"].(bool); !okv {
			return map[string]any{"status": "failed", "name": p.Name, "reason": res["reason"], "error": res["error"]}, nil
		}
		return map[string]any{"status": "restored", "name": p.Name}, nil
	}
	return map[string]any{"status": "none"}, nil
}

func pairHasHost(p SavedPair, host string) bool {
	for _, m := range p.Members {
		if strings.EqualFold(m.IP, host) {
			return true
		}
	}
	return false
}

// pairSides returns the LEFT (master) and RIGHT members.
func pairSides(p SavedPair) (left, right PairMember, ok bool) {
	for _, m := range p.Members {
		switch strings.ToUpper(m.Role) {
		case "LEFT":
			left = m
		case "RIGHT":
			right = m
		}
	}
	return left, right, left.IP != "" && right.IP != ""
}

// bothHold reports whether both firmwares report a healthy pair of these two.
func bothHold(p SavedPair, hosts ...string) bool {
	for _, h := range hosts {
		g, err := readFirmwareGroup(h)
		if err != nil || len(g.Roles) != 2 || strings.EqualFold(strings.TrimSpace(g.Status), "GROUP_ERROR") {
			return false
		}
		seen := 0
		for _, r := range g.Roles {
			for _, m := range p.Members {
				if strings.EqualFold(strings.TrimSpace(r.DeviceID), m.DeviceID) {
					seen++
				}
			}
		}
		if seen != 2 {
			return false
		}
	}
	return true
}

// installCanonicalPair gives both agents the same pair document, so neither
// half's marge can drift to a self-centred view of the pair.
func (a *App) installCanonicalPair(p SavedPair, left, right PairMember) {
	doc := canonicalPairXML(p, left, right)
	for _, h := range []string{left.IP, right.IP} {
		if err := postPairDoc(a, h, doc); err != nil {
			a.logger.Warn("stereo post-install: could not install the pair document", "host", h, "err", err)
		}
	}
}

// postPairDoc installs the pair document on one agent. Seam for tests.
var postPairDoc = func(a *App, host, doc string) error {
	resp, err := a.boxDo(host, 0, http.MethodPost, "/api/marge/group", "application/xml", doc)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func canonicalPairXML(p SavedPair, left, right PairMember) string {
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	id := p.ID
	if id == "" {
		id = "str-grp-" + left.DeviceID
	}
	role := func(m PairMember, r string) string {
		return `<groupRole><deviceId>` + esc(m.DeviceID) + `</deviceId><role>` + r + `</role><ipAddress>` + esc(m.IP) + `</ipAddress></groupRole>`
	}
	return `<group id="` + esc(id) + `"><name>` + esc(p.Name) + `</name><masterDeviceId>` + esc(left.DeviceID) +
		`</masterDeviceId><roles>` + role(left, "LEFT") + role(right, "RIGHT") + `</roles></group>`
}

// ---------- the written-down pairs ----------

var pendingPairsMu sync.Mutex

// pendingPairsPath is a seam for tests.
var pendingPairsPath = func() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ST Reborn", "pending-stereo-pairs.json"), nil
}

func pairKey(p SavedPair) string {
	ids := []string{}
	for _, m := range p.Members {
		ids = append(ids, strings.ToUpper(m.DeviceID))
	}
	if len(ids) == 2 && ids[0] > ids[1] {
		ids[0], ids[1] = ids[1], ids[0]
	}
	return strings.Join(ids, "+")
}

func loadPendingPairs() ([]SavedPair, error) {
	path, err := pendingPairsPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []SavedPair
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func writePendingPairs(pairs []SavedPair) error {
	path, err := pendingPairsPath()
	if err != nil {
		return err
	}
	if len(pairs) == 0 {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(pairs, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// savePendingPair writes p down, replacing an earlier record of the same two
// speakers.
func savePendingPair(p SavedPair) error {
	pendingPairsMu.Lock()
	defer pendingPairsMu.Unlock()
	pairs, _ := loadPendingPairs()
	key := pairKey(p)
	out := pairs[:0]
	for _, q := range pairs {
		if pairKey(q) != key {
			out = append(out, q)
		}
	}
	out = append(out, p)
	return writePendingPairs(out)
}

func dropPendingPair(p SavedPair) error {
	pendingPairsMu.Lock()
	defer pendingPairsMu.Unlock()
	pairs, _ := loadPendingPairs()
	key := pairKey(p)
	out := pairs[:0]
	for _, q := range pairs {
		if pairKey(q) != key {
			out = append(out, q)
		}
	}
	return writePendingPairs(out)
}
