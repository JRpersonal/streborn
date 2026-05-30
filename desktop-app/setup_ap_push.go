// Setup-AP WLAN push: when the user temporarily joins their PC to a
// factory-reset Bose box's setup-AP ("Bose SoundTouch Wi-Fi
// Network"), we can drive the box through the documented Bose OOB
// onboarding sequence over plain HTTP and make it associate to the
// user's home Wi-Fi without ever touching the iOS app. Live-verified
// 2026-05-30 on a taigan SoundTouch Portable in factory reset: the
// four-call sequence (language gate, marge gate, site survey,
// addWirelessProfile) lands a JJ3 join with the box reachable at its
// new DHCP-assigned IP on the home LAN within 1 to 5 minutes.
//
// The HTTP shapes are the originals the Bose iOS app and the Bose
// setup webpage emit, taken from [[bose-oob-gates]] memory and
// run.sh M1. The addWirelessProfile response is a TCP RST around
// 17 s into the call because the box's setup-AP loopback dies as
// it switches to STA mode; we treat that as a positive signal, not
// an error, and rely on the caller polling the home LAN for the
// box's new IP.

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// SetupAPPushResult is returned to the frontend after a push attempt
// so the UI can show which stage worked and what to do next. Step is
// the most-advanced step we reached; OK=true means the addWirelessProfile
// fired successfully (i.e. either got 200 or the expected RST).
type SetupAPPushResult struct {
	Step    string `json:"step"`
	Message string `json:"message"`
	OK      bool   `json:"ok"`
	// LogTail collects one line per stage so the UI's "show details"
	// disclosure can render the full trace without the caller juggling
	// state across multiple JS-Go round-trips. Each entry is a short
	// summary (verb + HTTP code or elapsed); response bodies are
	// trimmed to 200 bytes for terseness.
	LogTail []string `json:"logTail"`
}

// PushWLANToBox runs the language -> marge -> name (optional) ->
// survey -> addWirelessProfile sequence against host (typically
// SetupAPHost / 192.168.1.1). On success the box drops its setup-AP
// and starts associating to ssid; the caller is expected to switch
// the PC back to the home Wi-Fi and poll for the box's new IP via
// the existing mDNS / TCP-sweep paths in app.go.
//
// name is the friendly speaker name the iOS app also sets at this
// step. It is NOT a gate (the call is best-effort, ignored on
// failure), but the box persists it and announces it via mDNS once
// associated, so picking a meaningful value here saves the user a
// later round-trip through Settings.
func (a *App) PushWLANToBox(host, ssid, password, name string) (SetupAPPushResult, error) {
	res := SetupAPPushResult{Step: "start", LogTail: []string{}}
	if host == "" {
		host = SetupAPHost
	}
	if ssid == "" {
		res.Step = "validate"
		res.Message = "SSID is empty"
		return res, fmt.Errorf("ssid required")
	}
	a.logger.Info("push wlan: starting", "host", host, "ssid", ssid, "hasPassword", password != "", "name", name)

	// Step 1: language gate. CRITICAL: sysLanguage=0 is a no-op for
	// the gate. Live-verified 2026-05-30 on a factory-reset taigan
	// Portable: POSTing 0 returned 200 with the echo body but
	// systemstate stayed SETUP_LANG_NOT_SET, and the next box
	// reboot left the LED yellow forever. POSTing any value >= 1
	// transitions systemstate to SETUP_LANG_SET. Memory
	// [[bose-language-enum]] currently has 1=Danish, 2=German
	// confirmed. We pick 2 (German) as the default because more
	// of STR's confirmed users today are German-speaking; an
	// English-speaking user sees a one-time German display until
	// they change it from the in-app /language picker, which is
	// recoverable. Picking a Bose-untested value would be worse:
	// the v0.5.16 incident wrote sysLanguage=1 (Danish) and users
	// reported "the radio now speaks Finnish/Swedish" before we
	// understood the enum. Stay on a confirmed value.
	res.Step = "language"
	if msg, err := boseSetLanguage(a.ctx, host, 2); err != nil {
		res.Message = "language gate failed: " + err.Error()
		res.LogTail = append(res.LogTail, "language FAIL: "+err.Error())
		return res, err
	} else {
		res.LogTail = append(res.LogTail, "language OK: "+msg)
	}

	// Step 2: marge gate. We send a placeholder PairDeviceWithAccount
	// document; once the box is on the home LAN the STR autopair
	// running in this desktop app overwrites it with the real
	// stub@local credentials. The placeholder just needs to be a
	// non-empty marge for NetManager to process addWirelessProfile.
	res.Step = "marge"
	if msg, err := boseSetMargeAccount(a.ctx, host); err != nil {
		res.Message = "marge gate failed: " + err.Error()
		res.LogTail = append(res.LogTail, "marge FAIL: "+err.Error())
		return res, err
	} else {
		res.LogTail = append(res.LogTail, "marge OK: "+msg)
	}

	// Step 3 (optional): /name. The iOS app sets this after marge,
	// matching the "Where is the speaker?" prompt. It is not a gate
	// for addWirelessProfile (live-verified 2026-05-30 without it),
	// but persisting it now means the box advertises the right
	// friendlyName as soon as it joins the home LAN. Best-effort.
	if name != "" {
		res.Step = "name"
		if msg, err := boseSetName(a.ctx, host, name); err != nil {
			res.LogTail = append(res.LogTail, "name SKIP (best-effort): "+err.Error())
		} else {
			res.LogTail = append(res.LogTail, "name OK: "+msg)
		}
	}

	// Step 4: site survey. Wakes the radio for STA scan. Returns 500
	// on the first call sometimes (chipset is in AP-only mode and
	// scan times out) but the second call after marge usually works.
	// Best-effort: addWirelessProfile is the authoritative signal,
	// not the survey result.
	res.Step = "survey"
	if msg, err := bosePerformSiteSurvey(a.ctx, host); err != nil {
		res.LogTail = append(res.LogTail, "survey SKIP (best-effort): "+err.Error())
	} else {
		res.LogTail = append(res.LogTail, "survey OK: "+msg)
	}

	// Step 5: addWirelessProfile. Connection RSTs at ~17 s as the
	// setup-AP comes down. We treat both 200 OK and "connection
	// reset" / "EOF" / context deadline as success. The actual STA
	// association completes 1 to 5 minutes later inside the box.
	res.Step = "addProfile"
	if msg, err := boseAddWirelessProfile(a.ctx, host, ssid, password); err != nil {
		if isExpectedRSTOnSetupAP(err) {
			res.LogTail = append(res.LogTail, "addProfile OK (RST as expected at setup-AP teardown): "+err.Error())
			res.OK = true
			res.Step = "done"
			res.Message = "credentials sent — speaker is joining your Wi-Fi"
			return res, nil
		}
		res.Message = "addWirelessProfile failed: " + err.Error()
		res.LogTail = append(res.LogTail, "addProfile FAIL: "+err.Error())
		return res, err
	} else {
		res.LogTail = append(res.LogTail, "addProfile OK: "+msg)
	}

	res.OK = true
	res.Step = "done"
	res.Message = "credentials sent — speaker is joining your Wi-Fi"
	return res, nil
}

// boseSetLanguage POSTs the language gate. Body is the raw
// <sysLanguage>N</sysLanguage> XML stanza the firmware expects.
func boseSetLanguage(ctx context.Context, host string, n int) (string, error) {
	body := fmt.Sprintf("<sysLanguage>%d</sysLanguage>", n)
	resp, err := bosePost(ctx, host, "/language", "text/xml", body, 5*time.Second)
	if err != nil {
		return "", err
	}
	return shorten(resp), nil
}

// boseSetMargeAccount POSTs the marge-account gate. The accountId,
// userAuthToken and accountEmail are placeholders; the on-LAN
// autopair runs downstream and replaces them with stub@local.
func boseSetMargeAccount(ctx context.Context, host string) (string, error) {
	const body = `<?xml version="1.0" encoding="UTF-8" ?><PairDeviceWithAccount>` +
		`<accountId>str-bootstrap</accountId>` +
		`<userAuthToken>str-bootstrap</userAuthToken>` +
		`<accountEmail>str@local</accountEmail>` +
		`</PairDeviceWithAccount>`
	resp, err := bosePost(ctx, host, "/setMargeAccount", "application/xml", body, 10*time.Second)
	if err != nil {
		return "", err
	}
	return shorten(resp), nil
}

// boseSetName POSTs the device friendly name. Best-effort: not
// required to open addWirelessProfile, but the iOS app sets it here
// so STR matches that flow.
func boseSetName(ctx context.Context, host, name string) (string, error) {
	body := "<name>" + xmlEscape(name) + "</name>"
	resp, err := bosePost(ctx, host, "/name", "text/xml", body, 5*time.Second)
	if err != nil {
		return "", err
	}
	return shorten(resp), nil
}

// bosePerformSiteSurvey kicks the radio into a 5 s scan so
// NetManager knows the target SSID is in range.
func bosePerformSiteSurvey(ctx context.Context, host string) (string, error) {
	const body = `<PerformWirelessSiteSurvey timeout="5"/>`
	resp, err := bosePost(ctx, host, "/performWirelessSiteSurvey", "text/xml", body, 15*time.Second)
	if err != nil {
		return "", err
	}
	return shorten(resp), nil
}

// boseAddWirelessProfile is the call that actually triggers the
// STA-mode switch. Body shape follows the Bose ap.js emitting
// `></profile>` (not self-closing) and `securityType="wpa2aes"`
// (matches what /performWirelessSiteSurvey reports for protected
// networks). Both shapes have worked on sm2 and taigan in different
// captures; this is the one that survived the 2026-05-30 live test.
func boseAddWirelessProfile(ctx context.Context, host, ssid, password string) (string, error) {
	body := `<AddWirelessProfile><profile ssid="` + xmlEscapeAttr(ssid) +
		`" password="` + xmlEscapeAttr(password) +
		`" securityType="wpa2aes" ></profile></AddWirelessProfile>`
	resp, err := bosePost(ctx, host, "/addWirelessProfile", "text/xml", body, 30*time.Second)
	if err != nil {
		return "", err
	}
	return shorten(resp), nil
}

// bosePost is the single HTTP-POST helper. Each call gets its own
// http.Client because the Bose firmware's keep-alive sometimes
// hangs across requests and we want zero session-state coupling
// between gate calls.
func bosePost(ctx context.Context, host, path, contentType, body string, timeout time.Duration) (string, error) {
	url := fmt.Sprintf("http://%s:8090%s", host, path)
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = int64(len(body))
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		return string(respBytes), fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}
	return string(respBytes), nil
}

// isExpectedRSTOnSetupAP returns true for the connection-reset /
// EOF / deadline pattern that the addWirelessProfile call produces
// when the box's setup-AP loopback dies mid-call — the documented
// positive outcome per [[bose-oob-gates]].
func isExpectedRSTOnSetupAP(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, hint := range []string{
		"connection reset",
		"reset by peer",
		"forcibly closed",
		"eof",
		"context deadline exceeded",
		"i/o timeout",
		"network is unreachable",
		"broken pipe",
	} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return true
	}
	return false
}

func shorten(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func xmlEscapeAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return r.Replace(s)
}
