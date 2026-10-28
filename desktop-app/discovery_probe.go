package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/discovery"
)

// The STR agent and stock Bose probes behind discovery. Split out of app_discovery.go.

// probeSTR checks both :8888 and :17008 on the host for the STR
// agent JSON envelope. Bose's BCO wifi chipset has a different
// whitelist on each model family:
//
//   - Series-II classic boxes (ST10/20/30 verified live 2026-05-28
//     on ST10 .66 build 1944): :8888 / :9080 / :8081 are reachable
//     externally without any hijack. STR's agent answers :8888
//     directly.
//   - Series-I taigan boxes (Portable verified): :8888 SYNs are
//     dropped at the chipset level. STR's agent uses an LD_PRELOAD
//     shim inside Bose's SoftwareUpdate process to make :17008
//     forward to localhost:8888. On these boxes :17008 is the only
//     externally-reachable port.
//
// Both ports are probed in parallel. An ST10 answers on BOTH, and taking
// whichever answered first made the reported port flip between 8888 and
// 17008 from one refresh to the next, which the app read as a changed
// speaker and reset the now-playing view once a minute (#1190). The pick
// is therefore deterministic: see pickAgentPort. The BoxInfo.Port records
// the chosen reachable port so subsequent API calls hit the right entry
// point.
//
// On hit we also pull /info from :8090 on the same box — the Bose
// firmware keeps answering that endpoint even after STR is installed,
// and without it the box list shows "str-192.168.x.x" with no
// FriendlyName/DeviceID/Model, which the frontend renders as if the
// box were unprovisioned.
func probeSTR(ctx context.Context, ip string) (BoxInfo, bool) {
	return probeSTRPreferring(ctx, ip, 0)
}

// agentPorts are the two entry points the STR agent can be reached on, in
// the default order of preference: :8888 directly, :17008 via the redirect.
var agentPorts = [2]int{8888, 17008}

// portPreferGrace is how long pickAgentPort waits for the preferred port
// once the other one has already answered. Both probes run in parallel, so
// on a box that answers on both this is normally a few milliseconds; the
// cap only bounds the wait on a box whose preferred port is dropped (a
// Portable with no known port yet, where :8888 SYNs go nowhere).
const portPreferGrace = 400 * time.Millisecond

// probeSTRPreferring is probeSTR with a preferred port: the port the box was
// last verified on (prefer), so a box that answers on both keeps the port it
// already has. prefer 0, or a port that is not an agent port, means :8888.
func probeSTRPreferring(ctx context.Context, ip string, prefer int) (BoxInfo, bool) {
	port, body := pickAgentPort(ctx, prefer, func(ctx context.Context, p int) ([]byte, bool) {
		url := fmt.Sprintf("http://%s:%d/api/agent/version", ip, p)
		// The budget below covers the ANSWER; connecting has its own,
		// much shorter one (probeDialTimeout). A speaker that accepts
		// the connection is there, and under sustained box load
		// (BoseApp churning CPU, loadavg 3-4) it can take seconds to
		// reply. A missed probe relabels a flashed speaker as "needs
		// install", so the answer is worth waiting for; a host that is
		// not there still fails in about a second, at the dial.
		// 8 KB and a real decode, for the reason in agentVersionAnswered:
		// the agent's optional flags used to push "version" past a small
		// cap, and here that would relabel a flashed speaker as "needs
		// install", which is worse than the play refusal it also caused.
		body, err := httpGetSmall(ctx, url, probeAnswerBudget, 8192)
		if err != nil || !agentVersionAnswered(body) {
			return nil, false
		}
		return body, true
	})
	if port == 0 {
		return BoxInfo{}, false
	}
	return strBoxFromVersion(ctx, ip, port, body), true
}

// pickAgentPort runs fetch on both agent ports in parallel and returns the
// port to use plus its answer, or 0 when neither answered. The preferred
// port (prefer, else :8888) wins whenever it answers: returned at once when
// it answers first, and waited for up to portPreferGrace when the other port
// was faster. The other port is used only when the preferred one fails or
// stays silent past the grace, so the result no longer depends on which of
// two healthy ports happened to reply first.
func pickAgentPort(ctx context.Context, prefer int, fetch func(context.Context, int) ([]byte, bool)) (int, []byte) {
	if prefer != agentPorts[0] && prefer != agentPorts[1] {
		prefer = agentPorts[0]
	}
	type result struct {
		port int
		body []byte
		ok   bool
	}
	// Buffered for both, so a probe still running when we return early does
	// not block forever on its send.
	hits := make(chan result, len(agentPorts))
	for _, port := range agentPorts {
		p := port
		go func() {
			body, ok := fetch(ctx, p)
			hits <- result{port: p, body: body, ok: ok}
		}()
	}
	var fallback result
	var grace <-chan time.Time
	for pending := len(agentPorts); pending > 0; {
		select {
		case r := <-hits:
			pending--
			if !r.ok {
				continue
			}
			if r.port == prefer {
				return r.port, r.body
			}
			fallback = r
			if pending > 0 {
				t := time.NewTimer(portPreferGrace)
				defer t.Stop()
				grace = t.C
			}
		case <-grace:
			return fallback.port, fallback.body
		}
	}
	if fallback.ok {
		return fallback.port, fallback.body
	}
	return 0, nil
}

// strBoxFromVersion builds the BoxInfo for an agent that answered its
// /api/agent/version on port with body, enriched from the stock :8090 /info.
func strBoxFromVersion(ctx context.Context, ip string, port int, body []byte) BoxInfo {
	s := string(body)
	version := jsonStringField(s, "version")
	build := jsonStringField(s, "build")

	box := BoxInfo{
		Name:         "str-" + ip,
		Host:         ip,
		Port:         port,
		Version:      version,
		Build:        build,
		Kind:         "str",
		PortVerified: true, // port answered an actual HTTP probe
		// The agent now carries the box display name/model in its version
		// envelope (#108). Seeding them here means a flashed speaker is
		// labelled straight from this one verified probe, even when the
		// :8090 /info enrichment below fails because the box is busy right
		// after an OTA restart. Without this the box showed as "str-<ip>".
		FriendlyName:       jsonStringField(s, "friendlyName"),
		Model:              jsonStringField(s, "model"),
		BoxHealth:          jsonStringField(s, "boxHealth"),
		Internet:           jsonStringField(s, "internet"),
		InternetReason:     jsonStringField(s, "internetReason"),
		ArcadeAt:           jsonStringField(s, "arcadeAt"),
		ConflictingMod:     jsonStringField(s, "conflictingMod"),
		ForeignCloudURL:    jsonStringField(s, "foreignCloudURL"),
		GroupKeyError:      jsonStringField(s, "groupKeyError"),
		GroupKeyErrorKind:  jsonStringField(s, "groupKeyErrorKind"),
		HoldRefusedSource:  jsonStringField(s, "holdRefusedSource"),
		HoldRefusedSlot:    jsonStringField(s, "holdRefusedSlot"),
		StaleFolderKeySlot: jsonStringField(s, "staleFolderKeySlot"),
		SpeakerNameUnsafe:  jsonStringField(s, "speakerNameUnsafe") == "true",
		Storm1036:          jsonStringField(s, "preset1036Storm") == "active",
		Storm1036SinceSec: func() int {
			n, _ := strconv.Atoi(jsonStringField(s, "preset1036SinceSec"))
			return n
		}(),
		RecallRefusal: jsonStringField(s, "presetRefusal") == "active",
		RecallRefusalSinceSec: func() int {
			n, _ := strconv.Atoi(jsonStringField(s, "presetRefusalSinceSec"))
			return n
		}(),
		WLANCredsMissing: jsonStringField(s, "wlanCreds") == "missing",
		// The hash of the agent binary this speaker is actually running. It is
		// the only answer about "is this box current" that no clock can spoil,
		// and v0.9.88 needed it: the release stamped the app one minute later
		// than the agent it embeds, so every speaker looked permanently behind.
		AgentBinarySha256:  jsonStringField(s, "agentBinarySha256"),
		AgentRunningSha256: jsonStringField(s, "agentRunningSha256"),
	}
	// Best-effort enrichment from the underlying Bose firmware's
	// /info endpoint. Failure is OK: caller still gets a usable
	// box, just less labelled. Only overwrite the agent-reported
	// fields when /info actually returns a value, so a slow/dead
	// :8090 cannot blank out the name we already have.
	if info, ok := probeStock(ctx, ip); ok {
		if info.FriendlyName != "" {
			box.FriendlyName = info.FriendlyName
		}
		if info.Model != "" {
			box.Model = info.Model
		}
		// Same guard as the two fields above, which these two lacked: a
		// /info that answers but carries no deviceID blanked the one the
		// agent had just reported, and a box with an empty deviceID drops
		// out of every zone operation (the members are addressed by it).
		// The stock deviceID is still preferred whenever it HAS a value:
		// it is the SoundTouch ID the firmware's zone protocol keys on,
		// while the agent may report its wlan0 MAC, which on a two-chip
		// chassis is a different address (#544, Wave in a zone with an
		// ST20 master: the master formed a zone the Wave never joined).
		if info.DeviceID != "" {
			box.DeviceID = info.DeviceID
			// The same value under its own name, so a caller that must match
			// what a SPEAKER said (a zone document names its master by this id)
			// has it even when DeviceID later resolves to the agent's own.
			box.BoxDeviceID = info.DeviceID
		}
		if info.SerialNumber != "" {
			box.SerialNumber = info.SerialNumber
		}
	}
	return box
}

// probeSTRWithRetry probes a single host for the STR agent up to attempts
// times and returns the first success. Used to CONFIRM STR on a host that
// already answered the stock :8090 /info, where a single missed STR probe
// would wrongly classify an already-flashed speaker as stock and prompt a
// full USB-stick reinstall instead of an OTA update (#108: an ST10 .183,
// running v0.7.1, was sent to a complete stick install whenever the parallel
// STR sweep happened to miss it). STR speakers keep the Bose :8090 port alive,
// so a :8090 hit alone must never win over a present STR agent; a couple of
// targeted attempts make that check reliable even when the box is briefly busy.
func probeSTRWithRetry(ctx context.Context, ip string, attempts int) (BoxInfo, bool) {
	for i := 0; i < attempts; i++ {
		if b, ok := probeSTR(ctx, ip); ok {
			return b, true
		}
		if ctx.Err() != nil {
			break
		}
	}
	return BoxInfo{}, false
}

// jsonStringField pulls the value of a top-level string field from
// a small JSON envelope by substring scanning. Matches `"key":"val"`
// optionally separated by whitespace; returns "" on no match. Used
// for the STR /api/agent/version probe which has a known fixed
// shape and one of two short fields per call — adding encoding/json
// for that one call would bloat the desktop binary's startup graph
// for no observable benefit.
func jsonStringField(s, key string) string {
	needle := `"` + key + `"`
	i := strings.Index(s, needle)
	if i < 0 {
		return ""
	}
	rest := s[i+len(needle):]
	c := strings.IndexByte(rest, ':')
	if c < 0 {
		return ""
	}
	rest = rest[c+1:]
	for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t') {
		rest = rest[1:]
	}
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	// Find the closing quote, stepping over escapes. Stopping at the first
	// '"' cut every value that carried an escaped quote: a refused group-key
	// press whose reason quotes a URL reached the user as "... Get \" and
	// nothing after it (2026-10-27).
	e := -1
	for j := 1; j < len(rest); j++ {
		if rest[j] == '\\' {
			j++
			continue
		}
		if rest[j] == '"' {
			e = j
			break
		}
	}
	if e < 0 {
		return ""
	}
	quoted := rest[:e+1]
	// JSON escapes are Go escapes except "\/", which Go's encoder never
	// writes but another encoder may.
	if v, err := strconv.Unquote(strings.ReplaceAll(quoted, `\/`, `/`)); err == nil {
		return v
	}
	return quoted[1 : len(quoted)-1]
}

// probeStock checks ip:8090/info for the Bose SoundTouch device XML.
// Conservative timeouts so a sweep across 254 hosts stays cheap on a
// LAN where most addresses do not respond.
func probeStock(ctx context.Context, ip string) (BoxInfo, bool) {
	b, ok, _ := probeStockDetail(ctx, ip)
	return b, ok
}

// probeStockDetail is probeStock with the underlying failure preserved, so the
// manual add-by-IP path can log WHY the probe failed (timeout vs. refused vs.
// privacy denial vs. a non-SoundTouch responder). The sweep callers keep the
// cheap two-value form (#420).
func probeStockDetail(ctx context.Context, ip string) (BoxInfo, bool, error) {
	url := fmt.Sprintf("http://%s:8090/info", ip)
	body, err := httpGetSmall(ctx, url, 1200*time.Millisecond, 4096)
	if err != nil {
		return BoxInfo{}, false, err
	}
	s := string(body)
	if !strings.Contains(s, "<info ") || !strings.Contains(s, "deviceID=") {
		return BoxInfo{}, false, fmt.Errorf("%s answered but the reply is not a SoundTouch /info document", url)
	}
	deviceID := strings.ToUpper(extractAttr(s, "deviceID"))
	// The Bose /info XML labels itself UTF-8 but reports an umlaut box name as a
	// lone Latin-1 byte ("ü" = 0xFC). Left raw it JSON-marshals to U+FFFD and
	// shows as garbled "K�che" in the speaker list / multiroom UI (#70, Albrecht).
	name := toValidUTF8(extractTag(s, "name"))
	model := extractTag(s, "type")
	serial := extractPackagedProductSerial(s)
	return BoxInfo{
		Name:         "stock-" + discovery.LastN(deviceID, 6),
		Host:         ip,
		Port:         8090,
		DeviceID:     deviceID,
		BoxDeviceID:  deviceID, // straight from the firmware: by definition the box id
		FriendlyName: name,
		Model:        model,
		SerialNumber: serial,
		Kind:         "stock",
	}, true, nil
}

// extractPackagedProductSerial pulls the human-readable Bose serial
// out of the /info XML. The XML has multiple <component> blocks; the
// one that matches the physical sticker on the speaker is the one
// with <componentCategory>PackagedProduct</componentCategory> (the
// SCM block has the mainboard serial, which is different and not
// printed anywhere the user can see). Returns the first match or
// "" if no PackagedProduct component exists.
//
// We parse with substring scanning rather than encoding/xml because
// the Bose /info XML is small, well-structured, and we already use
// the same approach for other tags here. No new dependencies.
func extractPackagedProductSerial(infoXML string) string {
	const cat = "<componentCategory>PackagedProduct</componentCategory>"
	idx := strings.Index(infoXML, cat)
	if idx < 0 {
		return ""
	}
	// Walk forward to the next </component> closing tag and pull
	// the <serialNumber>...</serialNumber> inside this block.
	end := strings.Index(infoXML[idx:], "</component>")
	if end < 0 {
		return ""
	}
	block := infoXML[idx : idx+end]
	const open, close = "<serialNumber>", "</serialNumber>"
	s := strings.Index(block, open)
	if s < 0 {
		return ""
	}
	e := strings.Index(block[s+len(open):], close)
	if e < 0 {
		return ""
	}
	return strings.TrimSpace(block[s+len(open) : s+len(open)+e])
}

// enrichBoxWithStockInfo fetches /info on :8090 for an already-known
// box and copies Model + SerialNumber into the BoxInfo if they were
// missing. Used to give STR-flashed speakers the same identifying
// info as stock ones in the Setup target picker, where users with
// two identical ST20s rely on the serial sticker to tell them
// apart. Best-effort and short-timeout: a slow or missing /info
// just leaves the fields empty and the picker still renders.
func (a *App) enrichBoxWithStockInfo(ctx context.Context, b BoxInfo) BoxInfo {
	if b.Host == "" {
		return b
	}
	if b.SerialNumber != "" && b.Model != "" {
		return b
	}
	probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	url := fmt.Sprintf("http://%s:8090/info", b.Host)
	body, err := httpGetSmall(probeCtx, url, 1200*time.Millisecond, 4096)
	if err != nil {
		return b
	}
	xml := string(body)
	if b.Model == "" {
		if m := extractTag(xml, "type"); m != "" {
			b.Model = m
		}
	}
	if b.SerialNumber == "" {
		if sn := extractPackagedProductSerial(xml); sn != "" {
			b.SerialNumber = sn
		}
	}
	return b
}
