package main

// STR keeps its own copy of a speaker's Wi-Fi, by itself.
//
// On the coprocessor chassis (the scm SoundTouch 20 and 30, and a Portable
// whose firmware keeps no profile) the agent reports wlanCreds "missing" when
// STR holds no copy of the speaker's Wi-Fi: its offline rescue re-pushes that
// copy at the radio chip when the speaker comes up from a cold boot without a
// lease. A speaker set up through the Bose app never hands STR a copy. The app
// used to turn that into a banner with a "Save Wi-Fi" button that ran the full
// Wi-Fi change, which on these chassis restarts the speaker to apply it, and
// the owner of a working speaker could not tell what it was for (maintainer's
// own fresh SoundTouch 20, 2026-11-15).
//
// Now the app writes the copy itself through the agent's store-only endpoint
// (PUT /api/box/wlan/copy, internal/webui/wlancopy.go), which writes the copy
// and nothing else: no switch, no reconnect, no restart. Only when the app has
// evidence for the copy:
//
//   - the computer is on Wi-Fi right now, and the copy is that network with
//     the password this computer saved for it (the one it is connected with);
//   - the speaker's address lies in a subnet of that very Wi-Fi interface.
//     A coprocessor speaker does not report the name of its network, so being
//     reachable inside the subnet the computer's Wi-Fi is on is the best
//     evidence available that both are on the same network. A speaker reached
//     through another interface (the computer on a cable, a VPN, a second
//     network) is not guessed at: the copy is skipped.
//   - never on macOS: reading the saved password there prompts for the
//     keychain, which is not something to spring on the owner unasked.
//
// The agent adds its own check on top: the network must not contradict what
// the speaker itself names or stores. Every outcome is logged (never the name
// or the password, the network only as its tag), and an install writes it to
// the update journal.
//
// When the copy cannot be written the owner gets a quiet note in that
// speaker's Network settings, not a banner (frontend wificopy.js).

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/anonymise"
	"github.com/JRpersonal/streborn/wifiprofiles"
)

// WiFiCopyResult is what the frontend learns about one attempt. Reason is a
// stable code (wifiCopy* below, or "refused:<agent code>"); Note says whether
// the speaker's settings should offer to save the copy by hand.
type WiFiCopyResult struct {
	Stored bool   `json:"stored"`
	Reason string `json:"reason"`
	Note   bool   `json:"note"`
}

// The reasons an attempt ends with.
const (
	wifiCopyStored          = "stored"
	wifiCopyMacOS           = "macos"              // the keychain would prompt
	wifiCopyNoWiFi          = "pc-not-on-wifi"     // nothing to copy from
	wifiCopyNoSSID          = "pc-ssid-hidden"     // the system will not name the network (Windows: location services off) and the speaker named none to fall back on
	wifiCopyNoInterface     = "wifi-iface-unknown" // on Wi-Fi, but its addresses are not readable
	wifiCopyOtherSubnet     = "other-subnet"       // the speaker is not in the Wi-Fi's subnet
	wifiCopyNoSpeakerAddr   = "speaker-addr-unknown"
	wifiCopyNoPassword      = "no-password"       // this computer has no readable saved password
	wifiCopyBadPassword     = "password-unusable" // shorter than 8, longer than 64, or with line breaks
	wifiCopyOldAgent        = "agent-too-old"     // the agent predates the store-only endpoint
	wifiCopyUnreachable     = "unreachable"
	wifiCopyNotMissing      = "not-missing" // the speaker does not report a missing copy
	wifiCopyRefusedPrefix   = "refused:"
	wifiCopyAgentHasCopy    = "already-has-copy"
	wifiCopyAgentNotNeeded  = "not-needed"
	wifiCopyAgentRetired    = "retired"
	wifiCopyAgentStoredJSON = "stored-copy"
)

// wifiCopyEnv is what the decision reads off this computer, gathered once.
type wifiCopyEnv struct {
	GOOS      string
	SSID      string       // the Wi-Fi this computer is connected to, "" when none
	WiFiNets  []*net.IPNet // the IPv4 subnets of that Wi-Fi interface
	SpeakerIP net.IP
}

// wifiCopyPrecheck is the decision before any password is read. Pure. ""
// means the copy is plausible.
func wifiCopyPrecheck(env wifiCopyEnv) string {
	switch {
	case env.GOOS == "darwin":
		return wifiCopyMacOS
	case strings.TrimSpace(env.SSID) == "":
		return wifiCopyNoWiFi
	case len(env.WiFiNets) == 0:
		return wifiCopyNoInterface
	case env.SpeakerIP == nil:
		return wifiCopyNoSpeakerAddr
	case !ipInNets(env.SpeakerIP, env.WiFiNets):
		return wifiCopyOtherSubnet
	}
	return ""
}

// wifiCopyPasswordProblem rejects a password the agent would refuse or that
// cannot be what a WPA network uses. Pure.
func wifiCopyPasswordProblem(pw string) string {
	switch {
	case pw == "":
		return wifiCopyNoPassword
	case len(pw) < 8 || len(pw) > 64 || strings.ContainsAny(pw, "\r\n\x00"):
		return wifiCopyBadPassword
	}
	return ""
}

func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// wifiCopyNeedsNote says whether an outcome leaves the speaker without a copy
// that matters. An agent that has one, keeps the Wi-Fi itself, or set its
// copy aside because the owner moved the speaker does not need a note.
func wifiCopyNeedsNote(reason string) bool {
	switch reason {
	case wifiCopyStored, wifiCopyNotMissing,
		wifiCopyRefusedPrefix + wifiCopyAgentHasCopy,
		wifiCopyRefusedPrefix + wifiCopyAgentNotNeeded,
		wifiCopyRefusedPrefix + wifiCopyAgentRetired:
		return false
	}
	return true
}

// interfaceNets finds the IPv4 subnets of the computer's Wi-Fi interface
// among ifaces, by hardware address first, then by name.
func interfaceNets(it wifiprofiles.Interface, ifaces []net.Interface, addrs func(net.Interface) ([]net.Addr, error)) []*net.IPNet {
	var match *net.Interface
	for i := range ifaces {
		if it.MAC != "" && strings.EqualFold(ifaces[i].HardwareAddr.String(), it.MAC) {
			match = &ifaces[i]
			break
		}
	}
	if match == nil && it.Name != "" {
		for i := range ifaces {
			if ifaces[i].Name == it.Name {
				match = &ifaces[i]
				break
			}
		}
	}
	if match == nil {
		return nil
	}
	as, err := addrs(*match)
	if err != nil {
		return nil
	}
	var out []*net.IPNet
	for _, a := range as {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			out = append(out, n)
		}
	}
	return out
}

// gatherWiFiCopyEnv reads the computer's Wi-Fi and the speaker's address.
func gatherWiFiCopyEnv(host string) wifiCopyEnv {
	env := wifiCopyEnv{GOOS: runtime.GOOS}
	if env.GOOS == "darwin" {
		return env
	}
	it, ok := wifiprofiles.CurrentInterface()
	if !ok {
		return env
	}
	// The network name comes from the same query the rest of the app uses. On
	// Linux the interface's CONNECTION column is the NetworkManager profile
	// name, which need not be the SSID, so it is never used as one.
	env.SSID = wifiprofiles.CurrentSSID()
	if env.SSID == "" && env.GOOS == "windows" {
		env.SSID = it.SSID
	}
	if ifaces, err := net.Interfaces(); err == nil {
		env.WiFiNets = interfaceNets(it, ifaces, func(i net.Interface) ([]net.Addr, error) { return i.Addrs() })
	}
	env.SpeakerIP = speakerIPv4(host)
	return env
}

// speakerIPv4 is the speaker's IPv4 address, resolving a name if it has to.
func speakerIPv4(host string) net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return ip.To4()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if v4 := a.IP.To4(); v4 != nil {
			return v4
		}
	}
	return nil
}

// KeepSpeakerWiFiCopy lets STR keep its own copy of the speaker's Wi-Fi
// without touching the speaker's Wi-Fi: the computer's current network and
// its saved password, written store-only, when the evidence above holds.
// trigger says who asked ("install", "seen" for a speaker the app saw without
// a copy, "button" for the owner pressing the settings note's button), for the
// log and the journal.
func (a *App) KeepSpeakerWiFiCopy(host string, port int, trigger string) WiFiCopyResult {
	res := a.keepSpeakerWiFiCopy(host, port, trigger)
	a.logWiFiCopy(host, trigger, res)
	return res
}

func (a *App) keepSpeakerWiFiCopy(host string, port int, trigger string) WiFiCopyResult {
	if strings.TrimSpace(host) == "" {
		return WiFiCopyResult{Reason: wifiCopyNoSpeakerAddr, Note: true}
	}
	// Ask the speaker first whether it is missing a copy at all. An install
	// calls this on every model, and the password must not be read, let alone
	// travel, for a speaker that keeps its Wi-Fi by itself (a SoundTouch 10,
	// say) only to be refused there.
	if missing, known := a.agentReportsWiFiCopyMissing(host, port); !known {
		return WiFiCopyResult{Reason: wifiCopyUnreachable, Note: true}
	} else if !missing {
		return WiFiCopyResult{Reason: wifiCopyNotMissing}
	}
	env := gatherWiFiCopyEnv(host)
	if why := wifiCopyPrecheck(env); why != "" {
		// Windows 11 hides the connected network's name from programs while
		// location services are off ("netsh wlan show interfaces" then answers
		// with a location-permission notice, 2026-11-16 on the maintainer's
		// PC), so a computer that is on Wi-Fi read as one that is not. The
		// speaker names the network it keeps itself; with exactly one such
		// network, the speaker in one of this computer's subnets and a saved
		// password for that very network here, the copy is as plausible as
		// the computer-side route.
		if why == wifiCopyNoWiFi || why == wifiCopyNoInterface {
			if res, ok := a.copyFromSpeakerSSID(host, port, env); ok {
				return res
			}
			if why == wifiCopyNoWiFi && env.GOOS != "darwin" {
				why = wifiCopyNoSSID
			}
		}
		return WiFiCopyResult{Reason: why, Note: true}
	}
	pw, _ := wifiprofiles.TryPassword(env.SSID)
	if why := wifiCopyPasswordProblem(pw); why != "" {
		return WiFiCopyResult{Reason: why, Note: true}
	}
	return a.putWiFiCopy(host, port, env.SSID, pw, "pc-wifi")
}

// StoreSpeakerWiFiCopy writes the copy the owner typed into the speaker's
// settings, store-only, without the computer's checks: the owner names the
// network. The agent's own check (the speaker must not name or store another
// network) still applies. An agent too old for the endpoint answers
// agent-too-old, and the frontend then falls back to the ordinary Wi-Fi save.
func (a *App) StoreSpeakerWiFiCopy(host string, port int, ssid, password string) WiFiCopyResult {
	ssid = strings.TrimSpace(ssid)
	var res WiFiCopyResult
	switch {
	case ssid == "":
		res = WiFiCopyResult{Reason: wifiCopyNoWiFi, Note: true}
	case wifiCopyPasswordProblem(password) != "":
		res = WiFiCopyResult{Reason: wifiCopyPasswordProblem(password), Note: true}
	default:
		res = a.putWiFiCopy(host, port, ssid, password, "typed")
	}
	a.logWiFiCopy(host, "typed", res)
	if a.logger != nil {
		a.logger.Info("wifi copy: typed network", "host", host, "wantTag", anonymise.SSIDTag(ssid))
	}
	return res
}

// agentReportsWiFiCopyMissing reads wlanCreds from /api/agent/version, the
// field discovery turns into wlanCredsMissing. known is false when the agent
// did not answer with its JSON.
func (a *App) agentReportsWiFiCopyMissing(host string, port int) (missing, known bool) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/agent/version", "", "")
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return false, false
	}
	var v struct {
		Version   string `json:"version"`
		WLANCreds string `json:"wlanCreds"`
	}
	if json.Unmarshal(b, &v) != nil || v.Version == "" {
		return false, false
	}
	return v.WLANCreds == "missing", true
}

// speakerStoredSSID picks the one network the speaker keeps for itself out of
// GET /api/box/wlan/networks. Pure. It names a network only when that is
// unambiguous: one marked current, or else exactly one stored network that is
// not open. Anything else (none, several, only open ones) is "" so nothing is
// guessed.
func speakerStoredSSID(body []byte) string {
	var v struct {
		Current  string `json:"current"`
		Networks []struct {
			SSID    string `json:"ssid"`
			Current bool   `json:"current"`
			Open    bool   `json:"open"`
		} `json:"networks"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	if c := strings.TrimSpace(v.Current); c != "" {
		return c
	}
	var closed []string
	for _, n := range v.Networks {
		s := strings.TrimSpace(n.SSID)
		if s == "" || n.Open {
			continue
		}
		if n.Current {
			return s
		}
		closed = append(closed, s)
	}
	if len(closed) == 1 {
		return closed[0]
	}
	return ""
}

// localNetsContain reports whether ip lies in the IPv4 subnet of one of this
// computer's own interfaces, the computer-side half of the plausibility check
// when the Wi-Fi interface itself cannot be identified.
func localNetsContain(ip net.IP) bool {
	if ip == nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	var nets []*net.IPNet
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			nets = append(nets, n)
		}
	}
	return ipInNets(ip, nets)
}

// copyFromSpeakerSSID is the route for a computer that will not name its own
// network: the speaker names it, and this computer must hold a saved password
// for exactly that network. ok is false when the route does not apply, so the
// caller reports its own reason.
func (a *App) copyFromSpeakerSSID(host string, port int, env wifiCopyEnv) (WiFiCopyResult, bool) {
	if env.GOOS == "darwin" {
		return WiFiCopyResult{}, false
	}
	ip := env.SpeakerIP
	if ip == nil {
		ip = speakerIPv4(host)
	}
	if !localNetsContain(ip) {
		return WiFiCopyResult{}, false
	}
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/wlan/networks", "", "")
	if err != nil {
		return WiFiCopyResult{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return WiFiCopyResult{}, false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return WiFiCopyResult{}, false
	}
	ssid := speakerStoredSSID(b)
	if ssid == "" {
		return WiFiCopyResult{}, false
	}
	pw, _ := wifiprofiles.TryPassword(ssid)
	if why := wifiCopyPasswordProblem(pw); why != "" {
		return WiFiCopyResult{Reason: why, Note: true}, true
	}
	return a.putWiFiCopy(host, port, ssid, pw, "speaker-ssid"), true
}

// putWiFiCopy sends the copy to the agent and reads the answer. Only a JSON
// answer carrying the stored-copy status counts: an agent older than the
// endpoint answers the unknown path with its phone page (200, HTML).
func (a *App) putWiFiCopy(host string, port int, ssid, password, source string) WiFiCopyResult {
	body, _ := json.Marshal(map[string]string{"ssid": ssid, "password": password, "source": source})
	resp, err := a.boxDo(host, port, http.MethodPut, "/api/box/wlan/copy", "application/json", string(body))
	if err != nil {
		return WiFiCopyResult{Reason: wifiCopyUnreachable, Note: true}
	}
	defer resp.Body.Close()
	return wifiCopyFromAnswer(resp.StatusCode, resp.Header.Get("Content-Type"), resp.Body)
}

// wifiCopyFromAnswer interprets the agent's answer. Pure apart from reading
// the body.
func wifiCopyFromAnswer(status int, contentType string, body io.Reader) WiFiCopyResult {
	var out struct {
		Status string `json:"status"`
		Code   string `json:"code"`
	}
	isJSON := strings.HasPrefix(strings.ToLower(contentType), "application/json")
	if isJSON {
		_ = json.NewDecoder(io.LimitReader(body, 4096)).Decode(&out)
	}
	switch {
	case status == http.StatusOK && isJSON && out.Status == wifiCopyAgentStoredJSON:
		return WiFiCopyResult{Stored: true, Reason: wifiCopyStored}
	case (status == http.StatusConflict || status == http.StatusBadRequest) && isJSON && out.Code != "":
		r := wifiCopyRefusedPrefix + out.Code
		return WiFiCopyResult{Reason: r, Note: wifiCopyNeedsNote(r)}
	}
	// Anything else is an agent that does not know the endpoint: its phone
	// page, a 404 or a 405.
	return WiFiCopyResult{Reason: wifiCopyOldAgent, Note: true}
}

// logWiFiCopy records the decision in str.log, and for an install in the
// update journal too, which is where a failure report reads what an install
// did.
func (a *App) logWiFiCopy(host, trigger string, res WiFiCopyResult) {
	if a == nil {
		return
	}
	if a.logger != nil {
		if res.Stored {
			a.logger.Info("wifi copy: STR now keeps a copy of the speaker's Wi-Fi (store-only, the speaker's Wi-Fi was not touched)",
				"host", host, "trigger", trigger)
		} else {
			a.logger.Info("wifi copy: not stored", "host", host, "trigger", trigger, "reason", res.Reason, "note", res.Note)
		}
	}
	if trigger == "install" {
		switch {
		case res.Stored:
			a.recordOTA(host, "install: STR keeps a copy of the speaker's Wi-Fi now (store-only)")
		case res.Reason == wifiCopyNotMissing:
			a.recordOTA(host, "install: the speaker needs no Wi-Fi copy from STR")
		default:
			a.recordOTA(host, "install: no copy of the speaker's Wi-Fi kept ("+res.Reason+")")
		}
	}
}
