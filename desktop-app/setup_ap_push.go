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
	"net/url"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/sticksetup"
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

	// Primary path: the SMSC wifi chipset's GoAhead goform handler on port 80.
	// On BCO/taigan the :8090 addWirelessProfile is accepted but never applied
	// (cloud-dead MargeHSM); the chipset goform actually performs the join,
	// bypassing :8090 entirely (live taigan 2026-06-10). Try it first; on any
	// failure (e.g. a model without the GoAhead :80 stack -> 404) fall through
	// to the :8090 gate sequence below, which is the right path for
	// rhino/Series-II. Either way the box drops the setup-AP and the caller
	// polls the home LAN for the new IP.
	res.Step = "goform"
	if msg, err := boseGoformWLAN(a.ctx, host, ssid, password); err != nil {
		if isExpectedRSTOnSetupAP(err) {
			res.LogTail = append(res.LogTail, "goform OK (RST as expected at setup-AP teardown): "+err.Error())
			res.OK = true
			res.Step = "done"
			res.Message = "credentials sent (chipset path) — speaker is joining your Wi-Fi"
			a.logger.Info("push wlan: goform path took (RST)", "host", host)
			return res, nil
		}
		res.LogTail = append(res.LogTail, "goform unavailable, falling back to :8090 gates: "+err.Error())
		a.logger.Info("push wlan: goform unavailable, using :8090 fallback", "host", host, "err", err)
	} else {
		res.LogTail = append(res.LogTail, "goform OK: "+msg)
		res.OK = true
		res.Step = "done"
		res.Message = "credentials sent (chipset path) — speaker is joining your Wi-Fi"
		a.logger.Info("push wlan: goform path took", "host", host)
		return res, nil
	}

	// Step 1: language gate. CRITICAL: sysLanguage=0 is a no-op for
	// the gate. Live-verified 2026-05-30 on a factory-reset taigan
	// Portable: POSTing 0 returned 200 with the echo body but
	// systemstate stayed SETUP_LANG_NOT_SET, and the next box
	// reboot left the LED yellow forever. POSTing any value >= 1
	// transitions systemstate to SETUP_LANG_SET. The value also sets
	// the box's display language, so we derive it from the user's
	// active UI locale (SetAppLocale) instead of forcing one language
	// worldwide; SysLanguageForLocale falls back to English (3) for an
	// unknown/unset locale, the neutral global default. The full enum
	// is resolved in [[bose-language-enum]] (every value 1..25 known),
	// so there is no longer any risk of the v0.5.16 "radio now speaks
	// Finnish" incident from guessing.
	res.Step = "language"
	lang := sticksetup.SysLanguageForLocale(a.appLocale())
	a.logger.Info("push wlan: language gate", "locale", a.appLocale(), "sysLanguage", lang)
	if msg, err := boseSetLanguage(a.ctx, host, lang); err != nil {
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

	// Step 3: /name. ALWAYS post — live-verified 2026-05-30 on the
	// factory-reset taigan Portable that this is the difference
	// between LED-stays-yellow and LED-goes-white after the WLAN
	// switch. Without /name the box associates to STA but appears
	// to stay in setup-pending color; with /name the LED flips. The
	// iOS app posts a name unconditionally at this step. If the
	// caller did not supply a friendly name, we re-post whatever
	// /name currently returns so we still trip the gate without
	// changing the user-visible device name.
	res.Step = "name"
	chosenName := name
	if chosenName == "" {
		if existing, gerr := boseGetName(a.ctx, host); gerr == nil && existing != "" {
			chosenName = existing
			res.LogTail = append(res.LogTail, "name: using existing /name='"+existing+"' as gate trigger")
		} else {
			// Fall back to a generic value so the gate still fires
			// on a box whose default name is somehow empty.
			chosenName = "SoundTouch"
			res.LogTail = append(res.LogTail, "name: no existing name readable, falling back to 'SoundTouch'")
		}
	}
	if msg, err := boseSetName(a.ctx, host, chosenName); err != nil {
		res.LogTail = append(res.LogTail, "name FAIL: "+err.Error())
		res.Message = "name gate failed: " + err.Error()
		return res, err
	} else {
		res.LogTail = append(res.LogTail, "name OK ("+chosenName+"): "+msg)
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

// boseGoformWLAN provisions Wi-Fi via the SMSC chipset's GoAhead web server on
// port 80 (/goform/aformHandlerConfigureProfileSettings), the path the Bose
// setup webpage (ap.js) itself emits. This is a different stack from the :8090
// SoundTouch API and, on BCO/taigan, the one that actually APPLIES the profile
// (the :8090 addWirelessProfile is accepted but never associates there). Form
// fields and the Security/Cipher mapping are from ap.js, see
// [[project_smsc_goform_provisioning]]. Returns the response body on success.
// An RST/EOF/deadline is the expected setup-AP teardown as the chipset switches
// to STA; the caller treats it as success via isExpectedRSTOnSetupAP.
func boseGoformWLAN(ctx context.Context, host, ssid, password string) (string, error) {
	// Default to WPA2-AES (Security=WPA2PSK, Cipher=CCMP) when a passphrase is
	// given — it covers the overwhelming majority of home networks and is the
	// exact pair ap.js sends for a WPA2 network (live-verified taigan). An open
	// network sends no security/passphrase. WPA/WEP-only networks fall through
	// to the :8090 gate path if the chipset rejects this.
	security, cipher, passphrase := "", "", ""
	if password != "" {
		security, cipher, passphrase = "WPA2PSK", "CCMP", password
	}
	form := url.Values{}
	form.Set("ConfigManual", "1")
	form.Set("SSID", ssid)
	form.Set("Passphrase", passphrase)
	form.Set("Key0", "")
	form.Set("Security", security)
	form.Set("Cipher", cipher)
	form.Set("DHCPClient", "1")
	for _, k := range []string{"IP", "Mask", "DefGW", "DNSSrv1", "DNSSrv2", "ProxyServer", "ProxyServerPort"} {
		form.Set(k, "")
	}
	endpoint := "http://" + host + "/goform/aformHandlerConfigureProfileSettings"
	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err // RST/EOF/deadline -> caller's isExpectedRSTOnSetupAP
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("goform not present on this model (404)")
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("goform HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return fmt.Sprintf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(b))), nil
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

// boseGetName reads the current friendly name from /name. Returns
// the unwrapped XML value (no <name> tags). Used as a fallback so
// the unconditional /name POST can preserve the existing value when
// the caller has no preference.
func boseGetName(ctx context.Context, host string) (string, error) {
	url := fmt.Sprintf("http://%s:8090/name", host)
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	// Body is <?xml ...?><name>Foo Bar</name>. Pluck the inner text.
	const open, close = "<name>", "</name>"
	s := string(body)
	i := strings.Index(s, open)
	if i < 0 {
		return "", fmt.Errorf("no <name> tag in body %q", shorten(s))
	}
	j := strings.Index(s[i+len(open):], close)
	if j < 0 {
		return "", fmt.Errorf("no </name> close in body %q", shorten(s))
	}
	return strings.TrimSpace(s[i+len(open) : i+len(open)+j]), nil
}

// boseSetName POSTs the device friendly name. Always called as a
// gate step (not best-effort): the 2026-05-30 live test on taigan
// confirmed this is the difference between LED-yellow and LED-white
// after the WLAN switch.
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
