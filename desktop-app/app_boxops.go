package main

// This file was split out of app.go (wave-1 move-only refactor):
// box operations: preset re-sync, phone QR, reboot/wake, and transport actions.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// SyncBoxPresets re-sends all stick presets to the box so that
// the hardware preset buttons 1-6 work. Used by the "Repair hardware
// buttons" button in the Settings tab.
func (a *App) SyncBoxPresets(host string, port int) (map[string]any, error) {
	// boxDo so the :8888<->:17008 self-heal applies (BCO/Portable boxes only
	// answer on the REDIRECTed :17008; a baseURL+raw POST pinned to :8888 failed).
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/sync-presets", "application/json", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readHTTPError(resp)
	}
	// If the Stick Agent is too old and does not know the endpoint,
	// the fallback to the default handler returns HTML instead of JSON. Check
	// and report it nicely.
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "json") {
		return nil, fmt.Errorf("stick agent is too old for this operation; please update the stick first (update banner at the top)")
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// PhoneQR returns a QR code (a PNG data URI) encoding url, for the "Open on
// your phone" card in Speaker Settings: the user scans it with a phone camera
// to open that speaker's web remote and add it to the home screen. Generated
// locally, so the LAN address never leaves the machine. The caller builds url
// from the box's reachable host:port (probeSTR already records the right port).
func (a *App) PhoneQR(url string) (string, error) {
	if strings.TrimSpace(url) == "" {
		return "", fmt.Errorf("empty url")
	}
	png, err := qrcode.Encode(url, qrcode.Medium, 240)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

// RebootBox triggers a restart of the Bose box (via the Stick Agent
// shell `reboot`). This makes fresh setup-wizard configs on the
// USB stick take effect immediately, without continuous polling in the agent.
func (a *App) RebootBox(host string, port int) error {
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/reboot", "application/json", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	// The box is going down: drop any cached SSH connection to it so the next
	// SSH command after the reboot dials fresh instead of failing once first.
	boxSSHClients.invalidateHost(host)
	return nil
}

// WakeBox wakes a speaker from standby without starting playback, so the
// frontend can bring a zone member that a user switched off at the speaker back
// up before enrolling it in a group (#70). Best-effort: a failure is not fatal to
// the group form.
func (a *App) WakeBox(host string, port int) error {
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/wake", "application/json", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}

// RemoveConflictingMod removes the leftovers of a rival cloud-free SoundTouch
// tool (AfterTouch) from the box so they stop clashing with STR. Surfaced as a
// one-click button in the settings Actions tab and pointed to from the box-issue
// banner, so the user never needs an SSH command. Returns the agent's JSON
// result verbatim (removed entries + whether anything is still detected) for the
// frontend to show.
func (a *App) RemoveConflictingMod(host string, port int) (string, error) {
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/remove-conflicting-mod", "application/json", "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", readHTTPError(resp)
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return string(b), nil
}

// VoteStation gives a station a thumbs-up on radio-browser.
// Best effort; the error is returned but does not have to be shown.
func (a *App) VoteStation(host string, port int, uuid string) error {
	if uuid == "" {
		return nil
	}
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/radio/vote/"+uuid, "application/json", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("vote status %d", resp.StatusCode)
	}
	return nil
}

// friendlyError extracts the `detail` field from the Stick API error
// response, if present. Fallback: the raw body.
func friendlyError(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var m map[string]any
	if err := json.Unmarshal(b, &m); err == nil {
		// A grouped-follower rejection (409 {"error":"box-grouped","master":...})
		// must pass through as the RAW JSON body: the frontend's
		// parsePlayRejection parses the master field out of the error string to
		// retarget the play at the group's lead speaker, and the friendly
		// reduction below would strip it down to the bare code, losing master.
		if e, _ := m["error"].(string); e == "box-grouped" {
			return string(b)
		}
		if c, _ := m["code"].(string); c == "box-grouped" {
			return string(b)
		}
		msg := ""
		if d, ok := m["detail"].(string); ok && d != "" {
			msg = d
		} else if e, ok := m["error"].(string); ok && e != "" {
			msg = e
		}
		// Surface the stable machine `code` (e.g. spotify-not-logged-in,
		// spotify-premium-required) ahead of the human message as "code: message"
		// so the frontend can branch on the code rather than on fragile English
		// wording, and the wording stays free to change (#45). Callers that only
		// show the string still read fine.
		if c, ok := m["code"].(string); ok && c != "" {
			if msg != "" {
				return c + ": " + msg
			}
			return c
		}
		if msg != "" {
			return msg
		}
	}
	return string(b)
}

// Pause / Stop pro Box.
func (a *App) Pause(host string, port int) error  { return a.doAction(host, port, "pause") }
func (a *App) Resume(host string, port int) error { return a.doAction(host, port, "resume") }
func (a *App) Stop(host string, port int) error   { return a.doAction(host, port, "stop") }

// Next / Prev advance or rewind the current track. Source-aware on the agent:
// it skips go-librespot when Spotify is the live source, otherwise the STR play
// queue (a DLNA folder). Radio has nothing to skip, so the app only shows these
// for a Spotify playlist or a media-server queue.
func (a *App) Next(host string, port int) error { return a.doAction(host, port, "next") }
func (a *App) Prev(host string, port int) error { return a.doAction(host, port, "prev") }

func (a *App) doAction(host string, port int, action string) error {
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/"+action, "application/json", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}

// pickReachableIP selects, from the IPs the stick announces via mDNS, the one
// reachable from the current LAN. The box's USB gadget interface
// (203.0.113.x) is not routable from the Wi-Fi; the same box also announces
// its real Wi-Fi IP, which is the one we take.
//
// Prioritization:
//
//  1. Private LAN ranges (RFC 1918): 192.168/16, 10/8, 172.16/12
//  2. Link local: 169.254/16
//  3. Public IPs (unlikely)
//
// Skip: 203.0.113/24 (Documentation TEST-NET-3, box USB gadget),
// 127/8 loopback.
func pickReachableIP(ips []string) string {
	if len(ips) == 0 {
		return ""
	}
	var lan, linkLocal, public string
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil || ip.IsLoopback() {
			continue
		}
		// USB gadget TEST-NET-3 is not routable
		if strings.HasPrefix(ipStr, "203.0.113.") {
			continue
		}
		if ip.IsPrivate() {
			if lan == "" {
				lan = ipStr
			}
			continue
		}
		if ip.IsLinkLocalUnicast() {
			if linkLocal == "" {
				linkLocal = ipStr
			}
			continue
		}
		if public == "" {
			public = ipStr
		}
	}
	// No "default: return ips[0]" fallback. If the only IP we got
	// was loopback or TEST-NET-3, returning it would cause the
	// desktop app to show an entry that cannot actually be reached
	// (and dedup against the real entry would fail because the IPs
	// differ). Better to drop the unreachable record and let the
	// other discovery path or a refresh pick up the real IP.
	switch {
	case lan != "":
		return lan
	case linkLocal != "":
		return linkLocal
	case public != "":
		return public
	default:
		return ""
	}
}
