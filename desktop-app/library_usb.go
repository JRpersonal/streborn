package main

// Music sticks plugged into the speakers (Ideas #1136).
//
// A speaker's agent serves a FAT32 music stick as a small media server under
// :8888/usb/, but does not announce it over SSDP. The Library finds it the way
// the phone page does: every STR speaker lists its stick in
// /api/library/servers (kind "usb"), with the address of its device
// description, or with the reason it is not served. A served stick is then
// described and browsed like any other server; a refused one becomes one line
// in the Library ("format the stick as FAT32", "this is the install stick").
//
// Nothing here is remembered: a stick is in the list exactly while it is in a
// speaker, so a pulled stick never lingers as a "not answering" entry.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/dlna"
)

// LibraryUSBNotice is a stick in a speaker that is not served, and why.
// Code is the agent's refusal: unsupported-model, install-stick, not-fat32,
// big-clusters or unreadable. The frontend shows one translated line per code.
type LibraryUSBNotice struct {
	Speaker string `json:"speaker"`
	Code    string `json:"code"`
}

// usbStick is a served stick, described.
type usbStick struct {
	Server  dlna.Server
	Speaker string
	// Label is the stick's volume label, "" when it has none (or the agent
	// is older than the field).
	Label string
}

// usbAgentEntry is the stick's entry in an agent's /api/library/servers.
type usbAgentEntry struct {
	UDN      string `json:"udn"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Location string `json:"location"`
	Refusal  string `json:"refusal"`
	// Speaker is the name the speaker gives itself, Label the stick's volume
	// label; an agent from before these fields sends neither.
	Speaker string `json:"speaker"`
	Label   string `json:"label"`
}

// usbSpeakerName is the name a stick is listed under: the speaker's name as
// this app shows it, else the name the speaker's agent gives itself, else the
// discovery name unless it is only the "str-<ip>" placeholder, else the
// address. The placeholder is what a speaker found by probing its address is
// called until its name is read (discovery_probe.go), and an ST20 owner saw
// his stick listed as "USB stick (str-192.168.1.213)" for it (discussion
// #1136, 2026-11-22).
func usbSpeakerName(b BoxInfo, agentName string) string {
	if n := strings.TrimSpace(b.FriendlyName); n != "" {
		return n
	}
	if n := strings.TrimSpace(agentName); n != "" {
		return n
	}
	if n := strings.TrimSpace(b.Name); n != "" && !isPlaceholderBoxName(n) {
		return n
	}
	return b.Host
}

// isPlaceholderBoxName reports whether name is a stand-in rather than a
// speaker's name: "str-<ip>" from an address probe, or "STR-<hex>" (the tail of
// the device id) from an mDNS announce made before the name was known.
func isPlaceholderBoxName(name string) bool {
	if len(name) < 4 || !strings.EqualFold(name[:4], "str-") {
		return false
	}
	rest := name[4:]
	if net.ParseIP(rest) != nil {
		return true
	}
	if len(rest) < 4 {
		return false
	}
	for _, r := range rest {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// usbAskTimeout bounds one speaker's answer. The Library scan itself listens
// for at least 5 s, so a speaker that is off costs nothing extra.
const usbAskTimeout = 4 * time.Second

// speakerUSBSticks asks every STR speaker this app knows for its stick, in
// parallel.
func (a *App) speakerUSBSticks(ctx context.Context) ([]usbStick, []LibraryUSBNotice) {
	boxes := a.backupBoxes()
	var (
		mu      sync.Mutex
		sticks  []usbStick
		notices []LibraryUSBNotice
		wg      sync.WaitGroup
	)
	client := &http.Client{Timeout: usbAskTimeout}
	for _, b := range boxes {
		if b.Host == "" || b.Port == 0 {
			continue
		}
		wg.Add(1)
		go func(b BoxInfo) {
			defer wg.Done()
			s, n := fetchSpeakerUSB(ctx, client, b)
			mu.Lock()
			sticks = append(sticks, s...)
			notices = append(notices, n...)
			mu.Unlock()
		}(b)
	}
	wg.Wait()
	return sticks, notices
}

// fetchSpeakerUSB reads one speaker's stick entry and describes a served
// stick. An older agent lists no stick entry and is simply skipped.
func fetchSpeakerUSB(ctx context.Context, client *http.Client, b BoxInfo) ([]usbStick, []LibraryUSBNotice) {
	cctx, cancel := context.WithTimeout(ctx, usbAskTimeout)
	defer cancel()
	url := fmt.Sprintf("http://%s:%d/api/library/servers", b.Host, b.Port)
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	var entries []usbAgentEntry
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&entries); err != nil {
		return nil, nil
	}
	var (
		sticks  []usbStick
		notices []LibraryUSBNotice
	)
	for _, e := range entries {
		if e.Kind != "usb" {
			continue
		}
		speaker := usbSpeakerName(b, e.Speaker)
		if e.Refusal != "" {
			notices = append(notices, LibraryUSBNotice{Speaker: speaker, Code: e.Refusal})
			continue
		}
		if e.UDN == "" || e.Location == "" {
			continue
		}
		srv, err := dlna.DescribeServer(cctx, e.Location)
		if err != nil || srv.UDN == "" {
			continue
		}
		sticks = append(sticks, usbStick{Server: srv, Speaker: speaker, Label: strings.TrimSpace(e.Label)})
	}
	return sticks, notices
}

// LibraryUSBNotices lists the sticks the last Library scan found in a speaker
// but could not offer, so the Library can say what to do about each.
func (a *App) LibraryUSBNotices() []LibraryUSBNotice {
	a.libraryMu.Lock()
	defer a.libraryMu.Unlock()
	return append([]LibraryUSBNotice{}, a.usbNotices...)
}
