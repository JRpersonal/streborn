package webui

// Forgetting a speaker's Wi-Fi altogether, for a speaker on a network cable
// (#969).
//
// The network install pre-fills the Wi-Fi fields from the computer it runs
// on, so a wired owner who followed the form put the speaker on both links
// and had no way back: PUT /api/box/wlan refuses an empty network name, and
// the forget in wlanforget.go always keeps the current network (and refuses
// a speaker that is not on Wi-Fi at all). DELETE /api/box/wlan is the missing
// "forget everything" for exactly that speaker.
//
// It only ever runs on a speaker that is reachable over its cable, because on
// a speaker whose only link is Wi-Fi it is a self-inflicted lockout: the
// speaker drops off the network with nothing left to join and needs the Bose
// setup again. Two facts are required, both read on the box:
//
//   - the default route leaves through a live cable (wiredUplinkIface, the
//     same check the store-only Wi-Fi change uses, #1307), and
//   - the request itself arrived on an address of that cable interface, so
//     the caller is not talking to the speaker over the Wi-Fi it is about
//     to lose.
//
// What it clears, on the wpa chassis (sm2: ST10 rhino, ST20/ST30 sm2, ...),
// is every store the Wi-Fi change path writes, and nothing beyond them:
//
//  1. NetManager's own store, NetworkProfiles.xml: every <profile> element is
//     cut out byte for byte, the rest of the document survives verbatim (the
//     same edit the forget makes, only without a network to keep). This is
//     the store that decides the next boot; NetManager reads it at start
//     only, so the speaker restarts once to finish. An empty store on a
//     cabled speaker is the state a factory-reset speaker that only ever ran
//     on a cable is in, so the restart lands somewhere known.
//  2. /etc/wpa_supplicant.conf, rewritten in full with its global directives
//     and no network block at all (never appended to, never patched).
//  3. The running wpa_supplicant (wpa_cli remove_network), so nothing is left
//     to roam to before the restart. Nothing is restarted or reloaded: the
//     store-only arm of the Wi-Fi change keeps NetManager's control socket
//     intact for the same reason.
//  4. STR's own records: wlan-creds (and its backup and retired copy), the
//     intended-network record, the one-shot apply marker, the conf rollback
//     copy and the known-networks baseline. Without this the next boot's
//     run.sh would replay wlan-creds and put the network straight back.
//
// The BCO chassis (scm: Portable taigan, ST30 scm, ST20 spotty) is refused.
// Its Wi-Fi chip IS eth0, so a cable cannot be told apart from Wi-Fi from
// here, and its list lives in BoseApp's AirplayConfiguration.xml slots plus
// the chip's own single profile, where a removal has not been verified on
// hardware (see wlanforget.go). A wrong guess there reboots a speaker into
// nothing.
//
// A USB stick that still carries wlan.conf re-provisions the network on the
// next boot; that is the stick doing its job, and the docs say so.

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/atomicfile"
	"github.com/JRpersonal/streborn/internal/boxwrites"
)

// Refusal codes of DELETE /api/box/wlan. The app maps each to its own
// sentence; chassis-unsupported is shared with the forget.
const (
	clearNotWired       = "not-wired"
	clearReachedOverAir = "reached-over-wifi"
	clearChassis        = forgetChassis
	clearUnexpected     = "unexpected-store"
)

// wlanClearInputs is what the safety decision needs, read off the box.
type wlanClearInputs struct {
	Mechanism string // "wpa" or "bco"
	// WiredIface is the interface carrying the default route over a live
	// cable, "" when there is none.
	WiredIface string
	// RequestOnWired is true when the request arrived on an address of
	// WiredIface.
	RequestOnWired bool
}

// planWLANClear decides whether the speaker may forget its Wi-Fi. Pure, so
// the one decision that could cut a speaker off the network is covered
// without a box. Returns "" when it may.
func planWLANClear(in wlanClearInputs) (code, reason string) {
	switch {
	case in.Mechanism != "wpa":
		return clearChassis, "STR cannot forget the Wi-Fi of this speaker model yet: its Wi-Fi chip doubles as its network port, so a cable cannot be told apart from Wi-Fi, and its network list lives where a removal has not been verified"
	case in.WiredIface == "":
		return clearNotWired, "the speaker is not on a network cable; forgetting its Wi-Fi would take it off the network"
	case !in.RequestOnWired:
		return clearReachedOverAir, "this request reached the speaker over Wi-Fi, not over its cable; connect to the speaker's cable address and try again"
	}
	return "", ""
}

// wlanClearPaths are the files the clear reads and writes, injectable so the
// whole flow runs off-box in tests.
type wlanClearPaths struct {
	profilesGlob string
	wpaConf      string
	wpaConfTmp   string // the bind-mount staging copy for a read-only /etc
	wpaBackup    string
	creds        string
	credsRetired string
	target       string
	known        string
	applyMarker  string
}

func defaultWlanClearPaths() wlanClearPaths {
	return wlanClearPaths{
		profilesGlob: networkProfilesGlob,
		wpaConf:      wpaConfPath,
		wpaConfTmp:   "/tmp/wpa_supplicant.conf.str",
		wpaBackup:    wpaBackupPath,
		creds:        wlanCredsLivePath,
		credsRetired: wlanCredsRetiredPath,
		target:       wlanTargetPath,
		known:        wlanKnownPath,
		applyMarker:  wlanApplyMarkerPath,
	}
}

// Test seams. Production reads the box; the tests hand in a cabled speaker
// and catch the restart.
var (
	wlanClearPathsFn  = defaultWlanClearPaths
	wlanClearInputsFn = readWLANClearInputs
	// wlanClearRunningFn removes every network from the running supplicant
	// and reports how many went.
	wlanClearRunningFn = clearRunningNetworks
	wlanClearRebootFn  = rebootBox
)

// readWLANClearInputs gathers the decision's inputs for one request.
func readWLANClearInputs(r *http.Request) (wlanClearInputs, string) {
	iface, mech := detectWlanMechanism()
	in := wlanClearInputs{Mechanism: mech}
	if mech != "wpa" {
		return in, iface
	}
	in.WiredIface = wiredUplinkIface(iface)
	if in.WiredIface != "" {
		in.RequestOnWired = ifaceOwnsIP(in.WiredIface, requestLocalIP(r))
	}
	return in, iface
}

// requestLocalIP is the speaker-side address the request arrived on, nil
// when the server did not record it.
func requestLocalIP(r *http.Request) net.IP {
	a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || a == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		host = a.String()
	}
	return net.ParseIP(host)
}

// ifaceOwnsIP reports whether ip is one of the addresses of the named
// interface. An unknown address is never owned: the refusal is the safe side.
func ifaceOwnsIP(name string, ip net.IP) bool {
	if ip == nil {
		return false
	}
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return false
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return false
	}
	return addrsContainIP(addrs, ip)
}

// addrsContainIP is the comparison of ifaceOwnsIP, kept pure for the tests.
func addrsContainIP(addrs []net.Addr, ip net.IP) bool {
	for _, a := range addrs {
		var have net.IP
		switch v := a.(type) {
		case *net.IPNet:
			have = v.IP
		case *net.IPAddr:
			have = v.IP
		}
		if have != nil && have.Equal(ip) {
			return true
		}
	}
	return false
}

// buildWPAConfigNoNetwork is the conf a speaker without Wi-Fi gets: every
// global directive it already had, plus the two STR always guarantees, and
// no network block. The same template rule as buildWPAConfigFrom.
func buildWPAConfigNoNetwork(existing string) string {
	globals := wpaGlobalLines(existing)
	var b strings.Builder
	for _, l := range globals {
		b.WriteString(l + "\n")
	}
	if !hasWPADirective(globals, "ctrl_interface") {
		b.WriteString(wpaCtrlInterfaceLine + "\n")
	}
	if !hasWPADirective(globals, "update_config") {
		b.WriteString("update_config=1\n")
	}
	return b.String()
}

// wlanClearResult is what the clear did. By count and tag only: the response
// may be logged or pasted, so it carries no network name.
type wlanClearResult struct {
	Status          string   `json:"status"`
	Code            string   `json:"code,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	Mechanism       string   `json:"mechanism,omitempty"`
	FirmwareRemoved int      `json:"firmwareRemoved"`
	RemovedTags     []string `json:"removedTags"`
	RunningRemoved  int      `json:"runningRemoved"`
	ConfRewritten   bool     `json:"confRewritten"`
	ConfMethod      string   `json:"confMethod,omitempty"`
	STRRecords      int      `json:"strRecordsRemoved"`
	Rebooting       bool     `json:"rebooting"`
	Error           string   `json:"error,omitempty"`
}

// clearWLANStores applies the clear to the stores of a wpa chassis. The
// caller holds s.wlanMu and has already passed planWLANClear.
//
// The firmware store is edited in memory first and written only once every
// file parsed: an unexpected shape anywhere leaves every store untouched.
func (s *Server) clearWLANStores(p wlanClearPaths, iface string) (wlanClearResult, bool) {
	res := wlanClearResult{Status: "ok", Mechanism: "wpa", RemovedTags: []string{}}

	type edit struct {
		path string
		out  string
		n    int
		mode os.FileMode
	}
	var edits []edit
	files, _ := filepath.Glob(p.profilesGlob)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		doc := string(b)
		for _, e := range parseStoredProfiles(doc, "profile") {
			res.RemovedTags = append(res.RemovedTags, ssidTag(e.SSID))
		}
		out, n, ok := removeProfileElements(doc, func(string) bool { return true })
		if !ok {
			s.logger.Warn("wlan clear: unexpected NetworkProfiles shape, nothing changed", "path", f)
			return wlanClearResult{Status: "error", Code: clearUnexpected, Mechanism: "wpa", RemovedTags: []string{},
				Reason: "the speaker's own network store has a shape STR does not recognise, so nothing was changed"}, false
		}
		if n == 0 {
			continue
		}
		mode := os.FileMode(0o644)
		if fi, serr := os.Stat(f); serr == nil {
			mode = fi.Mode().Perm()
		}
		edits = append(edits, edit{f, out, n, mode})
	}

	// 1. The firmware store, which decides the next boot.
	for _, e := range edits {
		if err := atomicfile.WriteFile(e.path, []byte(e.out), e.mode); err != nil {
			s.logger.Warn("wlan clear: could not rewrite NetworkProfiles", "path", e.path, "err", err)
			res.Error = err.Error()
			continue
		}
		res.FirmwareRemoved += e.n
		boxwrites.Note("wlan-profile", "clear-firmware")
	}

	// 2. The conf, rewritten whole with no network block. An unreadable conf
	// still gets the minimal globals: a speaker without the file has nothing
	// to lose, and the file must exist for wpa_supplicant's next start.
	cur, _ := os.ReadFile(p.wpaConf)
	method, err := writeWPAConfAt(p.wpaConf, p.wpaConfTmp, buildWPAConfigNoNetwork(string(cur)))
	if err != nil {
		s.logger.Warn("wlan clear: could not rewrite wpa_supplicant.conf", "err", err)
		if res.Error == "" {
			res.Error = err.Error()
		}
	} else {
		res.ConfRewritten = true
		res.ConfMethod = method
	}

	// 3. The running supplicant.
	if iface != "" {
		res.RunningRemoved = wlanClearRunningFn(iface)
	}

	// 4. STR's own records, so no boot path puts the network back.
	for _, f := range []string{p.creds, p.creds + ".bak", p.credsRetired, p.target, p.known, p.applyMarker, p.wpaBackup} {
		if f == "" {
			continue
		}
		if err := os.Remove(f); err == nil {
			res.STRRecords++
		} else if !os.IsNotExist(err) {
			s.logger.Warn("wlan clear: could not remove an STR Wi-Fi record", "path", f, "err", err)
		}
	}
	return res, true
}

// clearRunningNetworks removes every network the running supplicant holds.
func clearRunningNetworks(iface string) int {
	if _, err := exec.LookPath("wpa_cli"); err != nil {
		return 0
	}
	n := 0
	for _, nw := range wpaListNetworks(iface) {
		if exec.Command("wpa_cli", "-i", iface, "remove_network", strconv.Itoa(nw.ID)).Run() == nil {
			n++
		}
	}
	return n
}

// wlanClearState answers what a clear would do right now, for the networks
// list: allowed or the refusal code.
func wlanClearState(r *http.Request) map[string]any {
	in, _ := wlanClearInputsFn(r)
	code, reason := planWLANClear(in)
	return map[string]any{"allowed": code == "", "code": code, "reason": reason}
}

// handleBoxWLANClear answers DELETE /api/box/wlan: forget every stored Wi-Fi
// network of a speaker that is on a network cable. Body (optional):
// {"reboot": false} to skip the restart that finishes the job. Refusals
// answer 409 with {"status":"refused","code":...}.
func (s *Server) handleBoxWLANClear(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "wlan clear only allowed from LAN", http.StatusForbidden)
		return
	}
	var req struct {
		Reboot *bool `json:"reboot"`
	}
	if r.ContentLength != 0 {
		if !decodeJSONRequest(w, r, 1024, &req) {
			return
		}
	}
	reboot := req.Reboot == nil || *req.Reboot

	s.wlanMu.Lock()
	in, iface := wlanClearInputsFn(r)
	if code, reason := planWLANClear(in); code != "" {
		s.wlanMu.Unlock()
		s.logger.Info("wlan clear: refused", "code", code, "mechanism", in.Mechanism,
			"wiredIface", in.WiredIface, "requestOnWired", in.RequestOnWired)
		writeJSON(w, http.StatusConflict, wlanClearResult{Status: "refused", Code: code, Reason: reason,
			Mechanism: in.Mechanism, RemovedTags: []string{}})
		return
	}
	res, ok := s.clearWLANStores(wlanClearPathsFn(), iface)
	s.wlanMu.Unlock()
	if !ok {
		writeJSON(w, http.StatusInternalServerError, res)
		return
	}
	s.logger.Info("wlan clear: the speaker forgot its Wi-Fi", "wiredIface", in.WiredIface,
		"firmware", res.FirmwareRemoved, "removedTags", res.RemovedTags, "running", res.RunningRemoved,
		"conf", res.ConfRewritten, "confMethod", res.ConfMethod, "strRecords", res.STRRecords, "err", res.Error)
	if res.FirmwareRemoved > 0 {
		// The same after-boot check the forget gets: did NetManager leave the
		// emptied store alone?
		writeWLANForgetRecord(wlanForgetRecord{At: time.Now().Unix(), RemovedTags: res.RemovedTags, Verdict: "pending-reboot"})
		res.Rebooting = reboot
	}
	writeJSON(w, http.StatusOK, res)
	if res.Rebooting {
		s.logger.Info("wlan clear: restarting the speaker so its network manager reloads the empty store")
		wlanClearRebootFn()
	}
}
