// Package anonymise strips personal data out of text that leaves a machine:
// diagnostic bundles, copyable failure reports, and the speaker's own debug
// state when a phone asks for it.
//
// It lives at the top level rather than under internal/ because the desktop app
// is a separate Go module and Go forbids importing another module's internal/.
// discovery/, dlna/ and sticksetup/ are here for the same reason.
//
// One implementation, two callers, on purpose. It used to live in the desktop
// app alone, on the assumption that the app was the only thing that ever
// exported this text. The speaker's phone remote grew a diagnostic button that
// fetches /api/debug/state and saves it directly, that assumption stopped being
// true, and 32 of the 36 files people attached to public issues that way
// carried their real LAN addresses and MAC addresses. Copying the regex list
// into the agent would have set up the next drift; the comment above
// ScrubIdentities already warned about exactly that.
//
// The hashes are unsalted SHA256 prefixes, so the same speaker hashes to the
// same DEV# on both paths and a bundle can still be followed across reports.
package anonymise

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
)

// === Sanitization ===

var ipv4Regex = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
var macRegex = regexp.MustCompile(`(?i)\b([0-9A-F]{2}[:-]){5}[0-9A-F]{2}\b`)
var deviceIDRegex = regexp.MustCompile(`(?i)\b[0-9A-F]{12}\b`)

// ssidRedactRegex is the SINGLE pass that removes network names and Wi-Fi
// secrets from anything leaving the host. One pass, not three, because the
// marker it writes contains the word "ssid" itself: a second pattern run over
// the result matches its own output and mangles it ("<<SSID-REDACTED>").
//
// Three shapes, in order of specificity:
//
//  1. key="value" - how the firmware echoes a profile back
//     (<profile ssid="Home Network 5G" password="..." />). The bare form below
//     truncates such a value at its first space, so the tail of any network
//     name containing a space used to ship in clear.
//  2. seeding 'value' - the box's boot script logs the Wi-Fi failover seed
//     with the network name in single quotes and no "ssid" token anywhere on
//     the line, so nothing caught it. A real household network name shipped
//     inside a user's bundle that way (found 2026-08-22), against the bundle
//     README's promise that SSIDs and Wi-Fi passwords never leave the host.
//     The boot script no longer logs the name, but a speaker on an older agent
//     already has the line on its NAND and hands it to the next bundle.
//  3. the bare key form, which is what the original pattern covered.
//
// Deliberately narrow: only these shapes. A general "anything in quotes" rule
// would gut radio station names and preset labels, which are what a bundle is
// usually read for.
// The marker is the FIRST alternative on purpose. It contains the word "ssid"
// itself, so without it the bare-key alternative matches INSIDE an already
// redacted marker and grows a "<" on every pass ("<<SSID-REDACTED>"). Text does
// go through this more than once: sanitizeLog and anonymizeText both call
// scrubPII, and nested structures are walked field by field. Matching the
// marker and handing it back untouched is what makes the pass idempotent.
// Every quoted value is bounded to its own LINE, and an unterminated one is
// redacted to the end of that line. Go's negated classes match newlines, so
// `[^"]*` on an unterminated attribute ran to the next quote anywhere later in
// the blob and swallowed whole log lines in between - and the scrub sees the
// box's setup log as one 64 KB blob, where truncation is routine: the boot
// script cuts a profile dump at 300 bytes and the seed response at 200, both of
// which land mid-attribute. Falling through to the bare-key alternative then
// leaked the tail. The seeding value stops at a newline rather than at the
// LAST quote on its line rather than the first, so a network called "Bob's
// WiFi" does not ship its tail; the unterminated case is a separate
// alternative so a terminated value does not greedily eat the rest of the
// line with it.
var ssidRedactRegex = regexp.MustCompile(`(?im)<SSID-REDACTED>|\b(?:ssid|password|passphrase|psk)\s*=\s*"[^"\n]*(?:"|$)|\bseeding\s+'[^\n]*'|\bseeding\s+'[^\n]*$|\b(?:ssid|ssid_name|wpa-psk\s+\S+|psk=)[^\s]*`)

const ssidRedacted = "<SSID-REDACTED>"

// redactSSIDs applies ssidRedactRegex, keeping the "seeding" verb so the line
// still reads as an event rather than turning into a bare marker.
func redactSSIDs(s string) string {
	return ssidRedactRegex.ReplaceAllStringFunc(s, func(m string) string {
		if strings.EqualFold(m, ssidRedacted) {
			return m // already redacted, leave it exactly as it is
		}
		if len(m) >= 8 && strings.EqualFold(m[:8], "seeding ") {
			return "seeding '" + ssidRedacted + "'"
		}
		return ssidRedacted
	})
}

// nameTagRegex catches the speaker's user-chosen friendly name as it appears in
// gabbo frame bodies captured in the box debug state / agent log
// (<nameUpdated>Living Room</nameUpdated>) and in any <name>...</name> a box
// log echoes. A friendly name is a personal identifier (CLAUDE.md), so it must
// be hashed even though it is free-form text with no fixed value shape.
var nameTagRegex = regexp.MustCompile(`<(name|nameUpdated)>([^<]+)</(?:name|nameUpdated)>`)

// friendlyNameJSONRegex catches the friendly name as a JSON value, e.g. the
// /api/agent/version payload ("friendlyName":"Bose Wit") and any status JSON
// that carries it. Keyed on the field name so radio/preset display names (also
// "name") are not over-scrubbed.
var friendlyNameJSONRegex = regexp.MustCompile(`(?i)("friendlyName"\s*:\s*")([^"]*)(")`)

// userPathRegex masks the account segment of user-home paths in bundled logs:
// macOS /Users/<name>/, Windows C:\Users\<name>\ (also the JSON-escaped \\
// form), Linux /home/<name>/. The OS account name is often the user's real
// first name, and it shipped verbatim in public bundles via lines like
// "logFile=/Users/<name>/Library/..." until v0.9.7.
var userPathRegex = regexp.MustCompile(`(?i)([/\\]+(?:Users|home)[/\\]+)([^/\\\s"',;]+)`)

// scrubPII is the single sanitization pass shared by every text blob that can
// leave the host (the app log, box-side logs pulled over SSH, the /api/debug
// state, /api/status). Keeping one function means a field added to the bundle
// cannot accidentally skip a scrub the other paths already do — the exact hole
// that leaked real device IDs and friendly names through anonymizeText while
// sanitizeLog scrubbed them (see #187/#197 diagnostic bundles).
// proxyPayloadRegex finds the base64 upstream STR's stream proxy carries in its
// own URLs, /stream/raw?u=<payload>. Both encodings appear in the field, and a
// payload can itself wrap another proxy URL, so the unwrapper below loops.
var proxyPayloadRegex = regexp.MustCompile(`(/stream/raw\?u=)([A-Za-z0-9+/_-]+={0,2})`)

// scrubProxyPayloads rewrites the addresses hidden INSIDE those payloads.
//
// It has to run before the text passes, because a base64 blob is opaque to every
// regex in this file: on issue #844 the log line's proxy host was correctly
// masked to 192.0.2.1 while the payload beside it still decoded to the
// reporter's real media server, and that bundle is public. Same shape as the
// device-ID and SSID holes this file already carries comments about, one level
// further down.
//
// A payload that does not decode, or decodes to something that is not a URL, is
// left exactly as it was: a bundle is diagnostic evidence and mangling a value
// nobody can read is worse than leaving it.
func scrubProxyPayloads(s string) string {
	return proxyPayloadRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := proxyPayloadRegex.FindStringSubmatch(m)
		prefix, payload := sub[1], sub[2]
		dec, enc, ok := decodeProxyPayload(payload)
		if !ok {
			return m
		}
		// Recurse first, so a doubly wrapped upstream is reached as well.
		cleaned := scrubPII(scrubProxyPayloads(dec))
		if cleaned == dec {
			return m
		}
		return prefix + enc.EncodeToString([]byte(cleaned))
	})
}

// decodeProxyPayload tries the encodings the proxy has used, and reports which
// one worked so the value can be put back the way it was found.
func decodeProxyPayload(payload string) (string, *base64.Encoding, bool) {
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.StdEncoding,
	} {
		if dec, err := enc.DecodeString(payload); err == nil {
			s := string(dec)
			if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
				return s, enc, true
			}
		}
	}
	return "", nil, false
}

func scrubPII(s string) string {
	// Encoded first: an address inside a base64 payload is invisible to every
	// regex below it (#844, a public bundle).
	s = scrubProxyPayloads(s)
	s = ipv4Regex.ReplaceAllStringFunc(s, func(ip string) string { return maskIP(ip) })
	return scrubIdentities(s)
}

// scrubIdentities is every pass scrubPII makes EXCEPT the IP masking, in the
// same order, so the two can never drift apart.
//
// It exists for the one text blob that must keep its addresses: the copyable
// failure report (updatereport.go). That report is shown to the user about
// their OWN equipment and the real IPs are the whole point of it, so maskIP
// would blank exactly the values it was written to display. Everything else
// scrubPII removes still has to go, and until 2026-08-23 none of it did: the
// report pasted an app-log tail through redactSSIDs alone, which left the
// Windows account name (userPathRegex), the speaker's MAC and its Bose
// deviceID, and the user-chosen friendly name in a text meant to be mailed.
// The report masks the ARP hardware address down to its vendor prefix for
// exactly that reason and then carried the same address unmasked two sections
// further down. That is the hole the comment above scrubPII warns about, so
// the fix is a shared pass rather than a second copy of the regex list.
func scrubIdentities(s string) string {
	s = macRegex.ReplaceAllStringFunc(s, func(m string) string { return "MAC#" + hashShort(m) })
	s = deviceIDRegex.ReplaceAllStringFunc(s, func(m string) string { return "DEV#" + hashShort(m) })
	s = nameTagRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := nameTagRegex.FindStringSubmatch(m)
		return "<" + sub[1] + ">NAME#" + hashShort(sub[2]) + "</" + sub[1] + ">"
	})
	s = friendlyNameJSONRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := friendlyNameJSONRegex.FindStringSubmatch(m)
		return sub[1] + "NAME#" + hashShort(sub[2]) + sub[3]
	})
	s = redactSSIDs(s)
	s = userPathRegex.ReplaceAllString(s, "${1}<user>")
	return s
}

func hashShort(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}

func maskIP(ip string) string {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return ip
	}
	// Keep last octet so the same host stays recognisable across
	// references but the network identity is hidden.
	return "192.0.2." + parts[3]
}

// sensitiveValueKeyRegex names the JSON keys whose VALUE is personal even though
// the value itself carries no hint of what it is.
//
// scrubPII can only work on the string in front of it, and a bare "MyHomeNet"
// looks like nothing: the SSID hint pattern needs the word "ssid" to be IN the
// text, which is true for a config FILE and false for structured JSON, where
// "ssid" is the key and the network name is a plain value one level down. So
// debugState.wlan_configured.networks[].ssid walked straight through a scrub the
// bundle README promises ("SSIDs and Wi-Fi passwords never leave the host"), and
// a reporter's four household network names ended up in a bundle attached to a
// public issue (2026-08-11, #592). Keying the scrub on the FIELD closes that,
// and it closes it for any future field with the same shape.
var sensitiveValueKeyRegex = regexp.MustCompile(`(?i)^(ssid|ssid_name|psk|passphrase|password|passwd|pwd|wifi_?password|pre_?shared_?key)$`)

// anonymizeDebugState walks the /api/debug/state map and scrubs
// every string value. Nested maps and slices are walked
// recursively. Non-string leaves are untouched (booleans, numbers).
// A string sitting under a sensitive key is dropped entirely rather than
// scrubbed, because its value IS the secret.
func anonymizeDebugState(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if s, ok := v.(string); ok && s != "" && sensitiveValueKeyRegex.MatchString(k) {
			out[k] = "<REDACTED>"
			continue
		}
		out[k] = anonymizeAny(v)
	}
	return out
}

func anonymizeAny(v any) any {
	switch t := v.(type) {
	case string:
		return scrubPII(t)
	case []any:
		for i, item := range t {
			t[i] = anonymizeAny(item)
		}
		return t
	case map[string]any:
		return DebugState(t)
	default:
		return v
	}
}

// speakerNameKeys are the keys whose value is a speaker's own name.
// Hashed rather than kept, the same way the bundle hashes it.
var speakerNameKeys = map[string]bool{"name": true, "friendlyName": true, "friendlyname": true}

// speakerSiblingKeys mark an object as describing a SPEAKER. Only then is a
// "name" next to them a speaker name.
//
// The distinction is the whole difference between anonymising and
// vandalising: "name" is also the station on a preset, the folder in a media
// library and the section in a log. Hashing all of them would leave a
// diagnostic nobody can read, which is why the desktop bundle runs its
// blanket name pass over the zone document alone and nowhere else.
var speakerSiblingKeys = []string{"deviceID", "deviceId", "mac", "macAddress", "ip", "host", "master", "members", "slaves", "port"}

func looksLikeSpeaker(m map[string]any) bool {
	for _, k := range speakerSiblingKeys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

// DebugState anonymises a speaker's /api/debug/state in place of the flat
// text scrub, which cannot see a secret that sits as a bare value under a
// key. Nested maps and slices are walked; numbers and booleans are left
// alone.
func DebugState(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	speaker := looksLikeSpeaker(in)
	for k, v := range in {
		if str, ok := v.(string); ok && str != "" {
			switch {
			case sensitiveValueKeyRegex.MatchString(k):
				out[k] = "<REDACTED>"
				continue
			case speaker && speakerNameKeys[k]:
				out[k] = "NAME#" + hashShort(str)
				continue
			}
		}
		out[k] = anonymizeAny(v)
	}
	return out
}

// ScrubPII removes every personal identifier from s: addresses, hardware
// addresses, device ids, speaker names, network names and Wi-Fi secrets, and
// the account segment of user-home paths. Use it for anything that leaves the
// machine and is not shown back to its own owner.
func ScrubPII(s string) string { return scrubPII(s) }

// ScrubIdentities is ScrubPII without the address masking, for the one text
// that must keep its addresses: a failure report shown to the user about their
// own equipment, where the real addresses are the point.
func ScrubIdentities(s string) string { return scrubIdentities(s) }

// RedactSSIDs removes network names and Wi-Fi secrets only.
func RedactSSIDs(s string) string { return redactSSIDs(s) }

// MaskIP rewrites an address into the documentation range, keeping the last
// octet so the same host stays recognisable across references.
func MaskIP(ip string) string { return maskIP(ip) }

// MaskIPs masks every address in s and changes nothing else. For callers
// that anonymise a structured document themselves and only need this one
// pass at the end.
func MaskIPs(s string) string {
	return ipv4Regex.ReplaceAllStringFunc(s, func(ip string) string { return maskIP(ip) })
}

// HashShort is the stable pseudonym for an identifier: the first 8 hex chars
// of its SHA256. Unsalted on purpose, so the same speaker is recognisable
// across two reports months apart without anyone knowing whose it is.
func HashShort(s string) string { return hashShort(s) }
