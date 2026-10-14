package webui

// Forgetting the networks a speaker should no longer join.
//
// Field case, 2026-10-14: five speakers (four SoundTouch 10, one SoundTouch
// 300) each carried two to four stored Wi-Fi profiles, one of them a public
// community hotspot with a captive portal. Whenever the home network blinked,
// the firmware picked the hotspot, got no internet through it, and the radio
// stopped; the owner re-paired the speakers twice in one day. Nothing in STR
// could remove a stored network (#969), only add one.
//
// Where the profiles actually live, per chassis, and what this file does to
// each store:
//
//	wpa chassis (sm2: ST10 rhino, ST20/ST30 sm2, ST300, Wave sm2 ...)
//	  1. NetManager's own store, /mnt/nv/BoseApp-Persistence/<N>/NetworkProfiles.xml.
//	     The ONLY persistent candidate list: NetManager loads it at start and
//	     injects every entry into wpa_supplicant (its own child). The <profile>
//	     elements of the networks to forget are cut out byte for byte; the
//	     kept profile, whose passphrase is ciphertext only NetManager can
//	     produce, is never touched. NetManager reads the file at start only,
//	     so the speaker restarts once to finish (rebootBox).
//	  2. The running wpa_supplicant (wpa_cli remove_network), so the forgotten
//	     networks stop being roam candidates right away, before the restart.
//	  3. /etc/wpa_supplicant.conf, which on these boxes is a vendor template
//	     with no network block, unless an earlier STR switch wrote one. Any
//	     block naming a forgotten network is dropped and the file is rewritten
//	     whole (never appended to, see feedback_wifi_provisioning).
//	  The firmware exposes no per-profile delete: :8090 has no remove call,
//	  and the TAP CLI only offers "network wifi profiles clear" (all of them),
//	  which would need the kept network's passphrase to re-add. Measured on an
//	  ST10 2026-10-14.
//
//	bco chassis (scm: Portable taigan, ST30 scm, ST20 spotty)
//	  The radio is a separate chip with its own profile store, mirrored into
//	  AirplayConfiguration.xml. There is no runtime channel that removes ONE
//	  profile from the chip (BoseApp only knows "clear all on BCO"), so a
//	  speaker with more than one profile there gets an honest refusal instead
//	  of a half-done job: a profile left in the chip comes straight back.
//
// The rules that keep a speaker reachable, all enforced in planWLANForget:
// the network the speaker is associated with right now is never touched; the
// live association is never dropped (only non-current networks are removed
// from the running supplicant); the firmware store is never left without the
// kept network; and a speaker that is not on Wi-Fi at all, or whose live
// network disagrees with the one STR gave it, is refused with the reason.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/atomicfile"
	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxwrites"
	"github.com/JRpersonal/streborn/internal/wlanlive"
)

// airplayConfigGlob is the BCO chassis' mirror of the radio chip's profiles.
const airplayConfigGlob = "/mnt/nv/BoseApp-Persistence/*/AirplayConfiguration.xml"

// wlanForgetRecordPath remembers the last forget, by tag only, so the next
// boot can say whether the removal held (see checkWLANForgetAfterBoot). A var
// so the tests can move it.
var wlanForgetRecordPath = "/mnt/nv/streborn/wlan-forget.json"

// publicHotspotNames are network names that belong to public community
// hotspots: shared by thousands of routers, usually behind a captive portal,
// so a speaker that joins one has a link and no internet. Matched exactly,
// case-insensitively, after trimming.
//
// Deliberately short and limited to names that are public by construction
// (operator-run, the same on every router). A guest network a household runs
// itself (a FRITZ!Box guest network, say) is NOT public in this sense and is
// not listed: it is the owner's own and may well be intended.
var publicHotspotNames = []string{
	"vodafone homespot", // Vodafone DE, broadcast by customer cable routers
	"vodafone hotspot",  // Vodafone DE, public hotspots
	"telekom_fon",       // Deutsche Telekom / Fon community hotspot
	"telekom",           // Deutsche Telekom public hotspot
	"wifionice",         // Deutsche Bahn ICE on-board Wi-Fi
	"xfinitywifi",       // Comcast US community hotspot
	"btwifi-with-fon",   // BT UK / Fon community hotspot
	"btwi-fi",           // BT UK public hotspot
}

// isPublicHotspot reports whether name is one of publicHotspotNames.
func isPublicHotspot(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	for _, h := range publicHotspotNames {
		if n == h {
			return true
		}
	}
	return false
}

// storedWiFi is one network a speaker has on file, as the app shows it. Names
// are in clear here: this list goes to the owner's own app on the LAN, never
// into a bundle (the diagnostic view is wlanConfigured, redacted at source).
type storedWiFi struct {
	SSID string `json:"ssid"`
	// Current marks the network the speaker is associated with right now.
	Current bool `json:"current"`
	// Open is a network without a key: the firmware stored it with security
	// "none", or the running supplicant carries it with key_mgmt=NONE.
	Open          bool `json:"open"`
	PublicHotspot bool `json:"publicHotspot"`
	// STRTarget marks the network STR itself gave this speaker.
	STRTarget bool `json:"strTarget,omitempty"`
	// Sources names every store that holds it: "firmware" (NetManager's
	// NetworkProfiles), "running" (wpa_supplicant), "conf"
	// (wpa_supplicant.conf), "chip" (the BCO radio's mirror).
	Sources []string `json:"sources"`
}

// wlanForgetInputs is everything the decision needs, gathered from the box.
type wlanForgetInputs struct {
	Mechanism string // "wpa" or "bco"
	// LiveSSID is the network the speaker is associated with over Wi-Fi right
	// now, "" when it is not.
	LiveSSID string
	// Wired is true when the speaker's uplink is a network cable.
	Wired bool
	// Target is the network STR gave this speaker (wlan-target, else
	// wlan-creds), "" when STR never set one.
	Target string
	// The stores, as lists of names (duplicates allowed, order kept).
	Firmware []string
	Running  []string
	Conf     []string
	Chip     []string
}

// wlanForgetPlan is the decision.
type wlanForgetPlan struct {
	Keep string
	// Remove is the distinct network names to forget, in first-seen order.
	Remove []string
	// Code is empty when the plan may run, otherwise the refusal.
	Code   string
	Reason string
}

// Refusal codes. The app maps each to its own sentence.
const (
	forgetNotAssociated = "not-associated"
	forgetWired         = "ethernet"
	forgetMismatch      = "target-mismatch"
	forgetKeepNotStored = "keep-not-stored"
	forgetChassis       = "chassis-unsupported"
	forgetIsCurrent     = "is-current"
	forgetIsTarget      = "is-target"
	forgetNotFound      = "not-found"
	forgetWouldEmpty    = "would-empty"
)

// planWLANForget decides what to remove. only=="" forgets every network but
// the current one; a name forgets that one network alone (#969). Pure, so the
// one decision that could lock a speaker out of every network it knows is
// covered by tests without a box.
func planWLANForget(in wlanForgetInputs, only string) wlanForgetPlan {
	only = strings.TrimSpace(only)
	refuse := func(code, reason string) wlanForgetPlan {
		return wlanForgetPlan{Keep: in.LiveSSID, Code: code, Reason: reason}
	}
	// A BCO chassis can never act, so it is answered first and by the store
	// alone: one distinct network is simply nothing to do (and some of these
	// chips do not even report the SSID they are on, measured on a Portable
	// 2026-10-14, so a liveness refusal would be both wrong and beside the
	// point); more than one is the chassis refusal.
	if in.Mechanism != "wpa" {
		distinct := map[string]bool{}
		for _, l := range [][]string{in.Firmware, in.Running, in.Conf, in.Chip} {
			for _, n := range l {
				if n != "" {
					distinct[n] = true
				}
			}
		}
		if len(distinct) <= 1 {
			return wlanForgetPlan{Keep: in.LiveSSID}
		}
		return refuse(forgetChassis, "this speaker's Wi-Fi chip keeps its own networks and offers no way to remove a single one")
	}
	if in.LiveSSID == "" {
		if in.Wired {
			return refuse(forgetWired, "the speaker is on a network cable and not on Wi-Fi, so there is no current network to keep")
		}
		return refuse(forgetNotAssociated, "the speaker is not connected to a Wi-Fi network right now, so there is no current network to keep")
	}
	keep := in.LiveSSID
	if only == "" && in.Target != "" && in.Target != keep {
		return refuse(forgetMismatch, "the speaker is on a different network than the one STR gave it; change the Wi-Fi first or move the speaker back, then try again")
	}

	all := func(lists ...[]string) []string {
		var out []string
		seen := map[string]bool{}
		for _, l := range lists {
			for _, n := range l {
				if n == "" || seen[n] {
					continue
				}
				seen[n] = true
				out = append(out, n)
			}
		}
		return out
	}
	known := all(in.Firmware, in.Running, in.Conf, in.Chip)
	contains := func(l []string, n string) bool {
		for _, x := range l {
			if x == n {
				return true
			}
		}
		return false
	}

	var remove []string
	if only != "" {
		switch {
		case only == keep:
			return refuse(forgetIsCurrent, "this is the network the speaker is using right now")
		case in.Target != "" && only == in.Target:
			return refuse(forgetIsTarget, "this is the network STR gave the speaker")
		case !contains(known, only):
			return refuse(forgetNotFound, "the speaker has no stored network of that name")
		}
		remove = []string{only}
	} else {
		for _, n := range known {
			if n != keep {
				remove = append(remove, n)
			}
		}
	}
	if len(remove) == 0 {
		return wlanForgetPlan{Keep: keep}
	}

	// The firmware store is the one that decides the next boot. It must still
	// hold the kept network afterwards, or the speaker wakes up with nothing
	// to join and falls back to setup mode.
	if len(in.Firmware) > 0 {
		left := 0
		for _, n := range in.Firmware {
			if !contains(remove, n) {
				left++
			}
		}
		if !contains(in.Firmware, keep) {
			if only == "" || left == 0 {
				return refuse(forgetKeepNotStored, "the speaker's own network store does not hold the current network, so removing the others would leave it nothing to join after a restart")
			}
		}
		if left == 0 {
			return refuse(forgetWouldEmpty, "removing this would leave the speaker without any stored network")
		}
	}
	return wlanForgetPlan{Keep: keep, Remove: remove}
}

// removeProfileElements cuts every <profile> element whose SSID drop selects
// out of a NetworkProfiles document. Every other byte survives exactly, in
// particular the kept profile's ciphertext. A line that held nothing but the
// element goes with it, so the file does not collect blank lines.
//
// ok is false when the document holds a profile that is not self-closing and
// has no matching end tag: a shape never observed, and not one to guess at.
func removeProfileElements(doc string, drop func(ssid string) bool) (out string, removed int, ok bool) {
	type cut struct{ start, end int }
	var cuts []cut
	for _, tag := range scanProfileTags(doc) {
		var ssid string
		for _, a := range tag.attrs {
			if strings.EqualFold(a.name, "ssid") {
				ssid = xmlAttrUnescape(doc[a.valStart:a.valEnd])
			}
		}
		if !drop(ssid) {
			continue
		}
		start, end := tag.start, tag.end
		if end < 2 || doc[end-2] != '/' {
			closeTag := strings.Index(strings.ToLower(doc[end:]), "</profile>")
			if closeTag < 0 {
				return doc, 0, false
			}
			end += closeTag + len("</profile>")
		}
		// Widen to the whole line when the element sits alone on it.
		ls := strings.LastIndexByte(doc[:start], '\n') + 1
		if strings.TrimSpace(doc[ls:start]) == "" {
			le := end
			for le < len(doc) && (doc[le] == ' ' || doc[le] == '\t' || doc[le] == '\r') {
				le++
			}
			if le < len(doc) && doc[le] == '\n' {
				start, end = ls, le+1
			}
		}
		cuts = append(cuts, cut{start, end})
	}
	if len(cuts) == 0 {
		return doc, 0, true
	}
	var b strings.Builder
	last := 0
	for _, c := range cuts {
		b.WriteString(doc[last:c.start])
		last = c.end
	}
	b.WriteString(doc[last:])
	return b.String(), len(cuts), true
}

// profileEntry is one stored profile with the attributes the app shows.
type profileEntry struct {
	SSID string
	Open bool
}

// parseStoredProfiles reads the profiles of a NetworkProfiles document (the
// "profile" element, security="none" for an open network) or of an
// AirplayConfiguration document ("PersistentWifiProfile", empty slots
// skipped).
func parseStoredProfiles(doc, element string) []profileEntry {
	var out []profileEntry
	for _, tag := range scanElementTags(doc, element) {
		var e profileEntry
		var sec string
		for _, a := range tag.attrs {
			switch {
			case strings.EqualFold(a.name, "ssid"):
				e.SSID = xmlAttrUnescape(doc[a.valStart:a.valEnd])
			case strings.EqualFold(a.name, "security"):
				sec = strings.ToLower(strings.TrimSpace(doc[a.valStart:a.valEnd]))
			}
		}
		if strings.TrimSpace(e.SSID) == "" {
			continue
		}
		e.Open = sec == "none" || sec == "open"
		out = append(out, e)
	}
	return out
}

// wpaConfBlock is one network={} block of a wpa_supplicant.conf.
type wpaConfBlock struct {
	SSID string
	Open bool
}

// wpaConfDropNetworks rewrites a wpa_supplicant.conf without the network
// blocks drop selects. Global directives, comments and every other block
// survive verbatim and in order; nothing is ever added. Returns the blocks
// the input held, so the caller can list them without a second parse.
func wpaConfDropNetworks(conf string, drop func(ssid string) bool) (out string, blocks []wpaConfBlock, removed int) {
	lines := strings.SplitAfter(conf, "\n")
	var b strings.Builder
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if !(strings.HasPrefix(t, "network=") && strings.Contains(t, "{")) {
			b.WriteString(lines[i])
			continue
		}
		// Collect the block up to its closing brace.
		j := i
		if !strings.HasSuffix(t, "}") {
			for j = i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == "}" {
					break
				}
			}
		}
		if j >= len(lines) {
			j = len(lines) - 1
		}
		blk := wpaConfBlock{}
		for _, l := range lines[i : j+1] {
			lt := strings.TrimSpace(l)
			switch {
			case strings.HasPrefix(lt, "ssid="):
				blk.SSID = parseWPAConfSSID(strings.TrimPrefix(lt, "ssid="))
			case strings.EqualFold(strings.ReplaceAll(lt, " ", ""), "key_mgmt=NONE"):
				blk.Open = true
			}
		}
		blocks = append(blocks, blk)
		if drop(blk.SSID) {
			removed++
		} else {
			for _, l := range lines[i : j+1] {
				b.WriteString(l)
			}
		}
		i = j
	}
	return b.String(), blocks, removed
}

// parseWPAConfSSID decodes the two spellings wpa_supplicant writes: a quoted
// string (with the escapes escapeWPAValue produces) or bare hex.
func parseWPAConfSSID(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `"`) {
		v = strings.TrimPrefix(v, `"`)
		if i := strings.LastIndex(v, `"`); i >= 0 {
			v = v[:i]
		}
		r := strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\n`, "\n", `\r`, "\r", `\t`, "\t")
		return r.Replace(v)
	}
	if b, err := hex.DecodeString(v); err == nil {
		return string(b)
	}
	return v
}

// readWlanCredsSSIDAt returns the SSID line of STR's wlan-creds file, "" when
// there is none.
func readWlanCredsSSIDAt(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "SSID=") {
			return strings.TrimRight(strings.TrimPrefix(l, "SSID="), "\r")
		}
	}
	return ""
}

// strWlanTarget is the network STR gave this speaker: the standing intent
// record if there is one, else the credentials the boot path replays.
func strWlanTarget() string {
	if t, ok := readWlanTarget(); ok {
		return t.SSID
	}
	return readWlanCredsSSIDAt(wlanCredsPath)
}

// wlanForgetState is what was read off the box, kept together so the list and
// the forget run on one consistent picture.
type wlanForgetState struct {
	in        wlanForgetInputs
	iface     string
	open      map[string]bool
	running   []wlanNetwork
	firmFiles []string
	firmCount int // wifiProfileCount from /networkInfo, -1 unknown
}

// readWLANForgetState gathers the inputs from the box.
func (s *Server) readWLANForgetState(ctx context.Context) wlanForgetState {
	iface, mech := detectWlanMechanism()
	st := wlanForgetState{iface: iface, open: map[string]bool{}, firmCount: -1}
	st.in.Mechanism = mech
	st.in.Target = strWlanTarget()

	st.firmFiles, _ = filepath.Glob(networkProfilesGlob)
	for _, f := range st.firmFiles {
		for _, e := range parseStoredProfiles(readFileString(f), "profile") {
			st.in.Firmware = append(st.in.Firmware, e.SSID)
			st.open[e.SSID] = st.open[e.SSID] || e.Open
		}
	}
	chips, _ := filepath.Glob(airplayConfigGlob)
	for _, f := range chips {
		for _, e := range parseStoredProfiles(readFileString(f), "PersistentWifiProfile") {
			st.in.Chip = append(st.in.Chip, e.SSID)
			st.open[e.SSID] = st.open[e.SSID] || e.Open
		}
	}
	_, blocks, _ := wpaConfDropNetworks(readFileString(wpaConfPath), func(string) bool { return false })
	for _, bl := range blocks {
		if bl.SSID != "" {
			st.in.Conf = append(st.in.Conf, bl.SSID)
			st.open[bl.SSID] = st.open[bl.SSID] || bl.Open
		}
	}

	var ifaces []boxapi.NetworkInterface
	if s.boxHost != "" {
		nctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		if n, err := boxapi.New(s.boxHost).GetNetwork(nctx); err == nil {
			ifaces = n.Interfaces
		}
		cancel()
		st.firmCount = wifiProfileCount(s.boxHost)
	}

	if mech == "wpa" {
		if _, err := exec.LookPath("wpa_cli"); err == nil {
			st.running = wpaListNetworks(iface)
			for _, n := range st.running {
				st.in.Running = append(st.in.Running, n.SSID)
				if wpaNetworkIsOpen(iface, n.ID) {
					st.open[n.SSID] = true
				}
			}
		}
		if live := wlanlive.Read(ctx, iface); live.Associated {
			st.in.LiveSSID = live.SSID
		}
		if st.in.LiveSSID == "" && wiredUplinkIface(iface) != "" {
			st.in.Wired = true
		}
		return st
	}
	// BCO: the chip is presented as an Ethernet interface, so its association
	// IS the Wi-Fi association (see wlanlive.go). There is no way to tell it
	// apart from a real cable from here, which only matters for the wording.
	if name, live := liveAssociation(ifaces); live != nil {
		st.in.LiveSSID = name
	}
	return st
}

// wpaNetworkIsOpen asks the running supplicant whether a network has no key.
func wpaNetworkIsOpen(iface string, id int) bool {
	out, err := exec.Command("wpa_cli", "-i", iface, "get_network", strconv.Itoa(id), "key_mgmt").Output()
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(string(out)), "NONE")
}

// list turns the state into the app's view: one entry per distinct name.
func (st wlanForgetState) list() []storedWiFi {
	byName := map[string]*storedWiFi{}
	var order []string
	add := func(name, src string) {
		if strings.TrimSpace(name) == "" {
			return
		}
		e, ok := byName[name]
		if !ok {
			e = &storedWiFi{
				SSID:          name,
				Current:       name == st.in.LiveSSID,
				Open:          st.open[name],
				PublicHotspot: isPublicHotspot(name),
				STRTarget:     name == st.in.Target,
				Sources:       []string{},
			}
			byName[name] = e
			order = append(order, name)
		}
		for _, x := range e.Sources {
			if x == src {
				return
			}
		}
		e.Sources = append(e.Sources, src)
	}
	for _, n := range st.in.Firmware {
		add(n, "firmware")
	}
	for _, n := range st.in.Chip {
		add(n, "chip")
	}
	for _, n := range st.in.Running {
		add(n, "running")
	}
	for _, n := range st.in.Conf {
		add(n, "conf")
	}
	out := make([]storedWiFi, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out
}

// wlanForgetResult is what a forget did, by tag: the response may be logged
// or pasted, so it carries no network name.
type wlanForgetResult struct {
	Status      string   `json:"status"`
	Code        string   `json:"code,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	KeptTag     string   `json:"keptTag,omitempty"`
	Removed     int      `json:"removed"`
	RemovedTags []string `json:"removedTags"`
	// Per store, how many entries went.
	FirmwareRemoved int  `json:"firmwareRemoved"`
	RunningRemoved  int  `json:"runningRemoved"`
	ConfRemoved     int  `json:"confRemoved"`
	Rebooting       bool `json:"rebooting"`
	// FirmwareCountBefore is the firmware's own wifiProfileCount before the
	// change; -1 when it could not be read.
	FirmwareCountBefore int    `json:"firmwareCountBefore"`
	Error               string `json:"error,omitempty"`
}

// executeWLANForget applies a plan to the three stores of a wpa chassis. The
// caller holds s.wlanMu.
func (s *Server) executeWLANForget(st wlanForgetState, plan wlanForgetPlan) wlanForgetResult {
	res := wlanForgetResult{Status: "ok", KeptTag: ssidTag(plan.Keep), RemovedTags: []string{}, FirmwareCountBefore: st.firmCount}
	drop := map[string]bool{}
	for _, n := range plan.Remove {
		drop[n] = true
		res.RemovedTags = append(res.RemovedTags, ssidTag(n))
	}
	res.Removed = len(plan.Remove)
	dropFn := func(n string) bool { return drop[n] && n != plan.Keep }

	// 1. The firmware store, which decides the next boot.
	for _, f := range st.firmFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		out, n, ok := removeProfileElements(string(b), dropFn)
		if !ok {
			s.logger.Warn("wlan forget: unexpected NetworkProfiles shape, left untouched", "path", f)
			res.Error = "unexpected profile store shape"
			continue
		}
		if n == 0 {
			continue
		}
		// Belt and braces: re-read what is about to be written and refuse to
		// write a store without a single profile left.
		if len(parseStoredProfiles(out, "profile")) == 0 {
			s.logger.Warn("wlan forget: refusing to write an empty profile store", "path", f)
			res.Error = "would empty the profile store"
			continue
		}
		mode := os.FileMode(0o644)
		if fi, serr := os.Stat(f); serr == nil {
			mode = fi.Mode().Perm()
		}
		if werr := atomicfile.WriteFile(f, []byte(out), mode); werr != nil {
			s.logger.Warn("wlan forget: could not rewrite NetworkProfiles", "path", f, "err", werr)
			res.Error = werr.Error()
			continue
		}
		res.FirmwareRemoved += n
		boxwrites.Note("wlan-profile", "forget-firmware")
	}

	// 2. The running supplicant, never the current network.
	if st.iface != "" {
		for _, n := range st.running {
			if n.Current || !dropFn(n.SSID) {
				continue
			}
			if err := exec.Command("wpa_cli", "-i", st.iface, "remove_network", strconv.Itoa(n.ID)).Run(); err == nil {
				res.RunningRemoved++
			}
		}
	}

	// 3. The conf file, rewritten whole and only when it names a forgotten
	// network.
	if conf, err := os.ReadFile(wpaConfPath); err == nil {
		out, _, n := wpaConfDropNetworks(string(conf), dropFn)
		if n > 0 {
			if _, werr := writeWPAConf(out); werr != nil {
				s.logger.Warn("wlan forget: could not rewrite wpa_supplicant.conf", "err", werr)
			} else {
				res.ConfRemoved = n
			}
		}
	}
	return res
}

// wlanForgetRecord is the tag-only memory of the last forget.
type wlanForgetRecord struct {
	At          int64    `json:"at"`
	KeptTag     string   `json:"keptTag"`
	RemovedTags []string `json:"removedTags"`
	// Verdict is "pending-reboot" until the next boot checks the store, then
	// "held" or "came-back".
	Verdict string `json:"verdict"`
	// CameBack lists the tags found in the store again after the boot.
	CameBack []string `json:"cameBack,omitempty"`
}

func writeWLANForgetRecord(r wlanForgetRecord) {
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	_ = atomicfile.WriteFile(wlanForgetRecordPath, append(b, '\n'), 0o600)
}

func readWLANForgetRecord() (wlanForgetRecord, bool) {
	var r wlanForgetRecord
	b, err := os.ReadFile(wlanForgetRecordPath)
	if err != nil || json.Unmarshal(b, &r) != nil {
		return wlanForgetRecord{}, false
	}
	return r, true
}

// wlanForgetVerdict compares the store after a boot against the record. Pure.
func wlanForgetVerdict(rec wlanForgetRecord, storeTags []string) wlanForgetRecord {
	have := map[string]bool{}
	for _, t := range storeTags {
		have[t] = true
	}
	rec.CameBack = nil
	for _, t := range rec.RemovedTags {
		if have[t] {
			rec.CameBack = append(rec.CameBack, t)
		}
	}
	if len(rec.CameBack) > 0 {
		rec.Verdict = "came-back"
	} else {
		rec.Verdict = "held"
	}
	return rec
}

// checkWLANForgetAfterBoot settles a pending forget once per box boot: did the
// removed networks stay gone from the firmware store? The answer is logged and
// kept in the record (debug section wlan_forget), because whether NetManager
// ever writes its in-memory list back over the edited file is exactly the
// question a field bundle has to be able to answer.
func (s *Server) checkWLANForgetAfterBoot() {
	rec, ok := readWLANForgetRecord()
	if !ok || rec.Verdict != "pending-reboot" {
		return
	}
	var tags []string
	files, _ := filepath.Glob(networkProfilesGlob)
	for _, f := range files {
		for _, e := range parseStoredProfiles(readFileString(f), "profile") {
			tags = append(tags, ssidTag(e.SSID))
		}
	}
	rec = wlanForgetVerdict(rec, tags)
	writeWLANForgetRecord(rec)
	if rec.Verdict == "held" {
		s.logger.Info("wlan forget: the forgotten networks stayed gone after the restart",
			"keptTag", rec.KeptTag, "removedTags", rec.RemovedTags, "storeTags", tags)
		return
	}
	s.logger.Warn("wlan forget: forgotten networks are back in the speaker's own store after the restart",
		"keptTag", rec.KeptTag, "cameBack", rec.CameBack, "storeTags", tags)
}

// wlanForgetDebug is the bundle view of the last forget (tags only).
func wlanForgetDebug() map[string]any {
	rec, ok := readWLANForgetRecord()
	if !ok {
		return map[string]any{"ran": false}
	}
	return map[string]any{
		"ran":         true,
		"at":          rec.At,
		"keptTag":     rec.KeptTag,
		"removedTags": rec.RemovedTags,
		"verdict":     rec.Verdict,
		"cameBack":    rec.CameBack,
	}
}

// forgetWLAN runs a forget end to end: read, plan, apply, record, and restart
// when the firmware store changed and the caller allows it. Serialized with
// every other Wi-Fi change.
func (s *Server) forgetWLAN(ctx context.Context, only string, reboot bool) (wlanForgetResult, int) {
	s.wlanMu.Lock()
	defer s.wlanMu.Unlock()
	st := s.readWLANForgetState(ctx)
	plan := planWLANForget(st.in, only)
	if plan.Code != "" {
		s.logger.Info("wlan forget: refused", "code", plan.Code, "liveTag", ssidTag(st.in.LiveSSID),
			"targetTag", ssidTag(st.in.Target), "mechanism", st.in.Mechanism, "storeTags", ssidTagsOfNames(st.in.Firmware))
		return wlanForgetResult{Status: "refused", Code: plan.Code, Reason: plan.Reason, KeptTag: ssidTag(plan.Keep),
			RemovedTags: []string{}, FirmwareCountBefore: st.firmCount}, http.StatusConflict
	}
	if len(plan.Remove) == 0 {
		return wlanForgetResult{Status: "ok", KeptTag: ssidTag(plan.Keep), RemovedTags: []string{}, FirmwareCountBefore: st.firmCount}, http.StatusOK
	}
	res := s.executeWLANForget(st, plan)
	s.logger.Info("wlan forget: removed stored networks", "keptTag", res.KeptTag, "removedTags", res.RemovedTags,
		"firmware", res.FirmwareRemoved, "running", res.RunningRemoved, "conf", res.ConfRemoved,
		"profilesBefore", st.firmCount, "only", only != "")
	if res.FirmwareRemoved > 0 {
		writeWLANForgetRecord(wlanForgetRecord{At: time.Now().Unix(), KeptTag: res.KeptTag, RemovedTags: res.RemovedTags, Verdict: "pending-reboot"})
		if reboot {
			res.Rebooting = true
		}
	}
	return res, http.StatusOK
}

func ssidTagsOfNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, ssidTag(n))
	}
	return out
}

// handleBoxWLANNetworks answers GET /api/box/wlan/networks with the stored
// networks (names in clear, for the owner's app), and DELETE with body
// {"ssid": "..."} by forgetting that one network.
func (s *Server) handleBoxWLANNetworks(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet, http.MethodDelete) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "wlan networks only allowed from LAN", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodDelete {
		var req struct {
			SSID   string `json:"ssid"`
			Reboot *bool  `json:"reboot"`
		}
		if !decodeJSONRequest(w, r, 1024, &req) {
			return
		}
		if strings.TrimSpace(req.SSID) == "" {
			http.Error(w, "ssid must not be empty", http.StatusBadRequest)
			return
		}
		s.respondForget(w, r, req.SSID, req.Reboot == nil || *req.Reboot)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	s.wlanMu.Lock()
	st := s.readWLANForgetState(ctx)
	s.wlanMu.Unlock()
	plan := planWLANForget(st.in, "")
	rec, _ := readWLANForgetRecord()
	writeJSON(w, http.StatusOK, map[string]any{
		"mechanism":     st.in.Mechanism,
		"networks":      st.list(),
		"current":       st.in.LiveSSID,
		"wired":         st.in.Wired,
		"firmwareCount": st.firmCount,
		// What a forget-others would do right now: the count to remove, or the
		// refusal the app shows instead of the button.
		"forgetOthers": map[string]any{
			"removable": len(plan.Remove),
			"code":      plan.Code,
			"reason":    plan.Reason,
		},
		"lastForgetVerdict": rec.Verdict,
	})
}

// handleBoxWLANForgetOthers answers POST /api/box/wlan/forget-others: remove
// every stored network but the current one. Body (optional):
// {"reboot": false} to skip the restart that finishes the job.
func (s *Server) handleBoxWLANForgetOthers(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "wlan forget only allowed from LAN", http.StatusForbidden)
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
	s.respondForget(w, r, "", req.Reboot == nil || *req.Reboot)
}

func (s *Server) respondForget(w http.ResponseWriter, r *http.Request, only string, reboot bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, code := s.forgetWLAN(ctx, only, reboot)
	writeJSON(w, code, res)
	if res.Rebooting {
		s.logger.Info("wlan forget: restarting the speaker so its network manager reloads the store")
		rebootBox()
	}
}

// forgetOthersAfterSwitch is the wlan change flow's "forget all other
// networks" option, run after a CONFIRMED live switch to keep. Never reboots
// by itself; it reports whether the firmware store changed so the caller can.
func (s *Server) forgetOthersAfterSwitch(ctx context.Context, keep string) bool {
	st := s.readWLANForgetState(ctx)
	// The switch was just confirmed on keep. The live read is the authority,
	// and if it already disagrees (a roam in the last seconds) nothing is
	// removed.
	if st.in.LiveSSID != keep {
		s.logger.Warn("wlan forget after switch: the speaker is not on the new network any more, keeping every stored network",
			"wantTag", ssidTag(keep), "liveTag", ssidTag(st.in.LiveSSID))
		return false
	}
	st.in.Target = keep
	plan := planWLANForget(st.in, "")
	if plan.Code != "" || len(plan.Remove) == 0 {
		s.logger.Info("wlan forget after switch: nothing removed", "code", plan.Code, "keptTag", ssidTag(keep))
		return false
	}
	res := s.executeWLANForget(st, plan)
	s.logger.Info("wlan forget after switch: removed the other networks", "keptTag", res.KeptTag,
		"removedTags", res.RemovedTags, "firmware", res.FirmwareRemoved, "running", res.RunningRemoved, "conf", res.ConfRemoved)
	if res.FirmwareRemoved > 0 {
		writeWLANForgetRecord(wlanForgetRecord{At: time.Now().Unix(), KeptTag: res.KeptTag, RemovedTags: res.RemovedTags, Verdict: "pending-reboot"})
		return true
	}
	return false
}
