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
// The hashes are SHA256 prefixes over a salt the exporting machine keeps and
// never ships. Within one exporter the old property holds, so the same speaker
// carries the same DEV# across reports months apart. Between the two exporters
// the tokens differ, because the PC and the speaker each hold their own salt,
// and that is the price of the tokens no longer being reversible: a MAC was a
// 24-bit sweep, about 25 seconds, and a list of common room names recovered 14%
// of the speaker names in a 72-bundle corpus (#971).
package anonymise

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// === Sanitization ===

var ipv4Regex = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
var macRegex = regexp.MustCompile(`(?i)\b([0-9A-F]{2}[:-]){5}[0-9A-F]{2}\b`)

// hexRunRegex matches a whole run of hex digits and dashes rather than a bare
// 12-hex group, because the group alone cannot see what it is sitting in.
//
// The old pattern was a blind 12-hex match, and a UUID ends in exactly that:
// the last group of 5435f503-c9e0-4ac0-ac67-58d3491f4b1a was cut out and
// replaced with a DEV# token while the rest of the UUID stayed, in two field
// families across the 72-bundle corpus in #971. That destroys the one value the
// media-server analysis is read for (#733, a server that regenerates its UUID)
// and it invents device keys that merge unrelated households.
//
// Matching the run and deciding in Go fixes both directions at once, including
// the case a naive "leave anything with a dash alone" rule would have broken: a
// Bose speaker's UPnP UDN is BO5EBO5E-F00D-F00D-FEED-<the MAC>, so its last
// group must still be hashed, and hashed to the same token as the deviceID
// beside it.
var hexRunRegex = regexp.MustCompile(`(?i)\b[0-9A-F][0-9A-F-]*[0-9A-F]\b`)

// uuidShapeRegex is the 8-4-4-4-12 form.
var uuidShapeRegex = regexp.MustCompile(`(?i)^[0-9A-F]{8}(-[0-9A-F]{4}){3}-[0-9A-F]{12}$`)

// boseUDNPrefix is the fixed head of a SoundTouch speaker's UPnP UDN. What
// follows the last dash is the MAC, so that group is a device identifier and
// nothing else in the UUID is.
const boseUDNPrefix = "BO5EBO5E-F00D-F00D-FEED-"

// scrubHexRuns hashes the hardware identifiers in s and leaves the rest of
// every run it finds intact.
func scrubHexRuns(s string) string {
	return hexRunRegex.ReplaceAllStringFunc(s, func(m string) string {
		if !strings.Contains(m, "-") {
			// A bare 12-hex token is a Bose deviceID, which is the speaker MAC.
			if len(m) == 12 {
				return "DEV#" + hashShort(m)
			}
			return m
		}
		if !uuidShapeRegex.MatchString(m) {
			// Not a UUID, so nothing here says the whole run is one identifier.
			// Any 12-hex GROUP inside it still is one, and the rest of the run
			// survives: that is what the old pattern got right and what a plain
			// "leave anything dashed alone" rule would have thrown away.
			parts := strings.Split(m, "-")
			for i, g := range parts {
				if len(g) == 12 {
					parts[i] = "DEV#" + hashShort(g)
				}
			}
			return strings.Join(parts, "-")
		}
		if strings.EqualFold(m[:len(boseUDNPrefix)], boseUDNPrefix) {
			// Keep the prefix readable and hash the MAC. The token equals the
			// deviceID token elsewhere in the same bundle, which is how a reader
			// sees that the UDN and the device are one speaker.
			return m[:len(boseUDNPrefix)] + "DEV#" + hashShort(m[len(boseUDNPrefix):])
		}
		// Any other UUID belongs to the user's equipment (a media server, a
		// renderer) and is hashed WHOLE, so two bundles can still be compared on
		// it without a fragment of it being published.
		return "UUID#" + hashShort(strings.ToLower(m))
	})
}

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

// userPathSpacedRegex is the same segment when the account name contains a
// space, which the pattern above truncates at that space: 9 of the 72 bundles
// in #971 shipped a surname that way, and one household was confirmed twice
// because the same name was also its DNS search domain.
//
// A name with a space is only accepted when a path separator follows it, so a
// match cannot run off into the rest of a log line, and at most two spaces are
// allowed: enough for "First Last" or "First Middle Last", not a sentence.
var userPathSpacedRegex = regexp.MustCompile(`(?i)([/\\]+(?:Users|home)[/\\]+)([^/\\\s"',;]+(?: [^/\\\s"',;]+){1,2})([/\\])`)

// localNames are the strings this machine knows identify its own owner: the OS
// account name and the name of the home directory.
//
// They exist because every pattern in this file anchors on something the text
// has to provide, and a first name in a path with no /Users/ or /home/ in it
// provides nothing to anchor on. Six of the 72 bundles in #971 carried one that
// way, in a stick or library path on another drive. The exporting machine does
// not have to guess: it knows its own account name, so it can strike that exact
// string wherever it appears.
var localNames []string

// genericAccountNames are names too common to strike. Replacing "root", "user"
// or "bose" everywhere would gut a log rather than anonymise it, and none of
// them identifies a person.
var genericAccountNames = map[string]bool{
	"root": true, "user": true, "users": true, "admin": true, "administrator": true,
	"guest": true, "default": true, "public": true, "home": true, "media": true,
	"bose": true, "pi": true, "nobody": true, "shared": true, "owner": true,
}

// SetLocalNames installs the account names of the machine that exports. Each is
// struck from every text that leaves, wherever it appears. Names shorter than
// four characters and names on the generic list are ignored, because striking
// those does more damage to the diagnostic than the leak they prevent.
func SetLocalNames(names ...string) {
	localNames = nil
	for _, n := range names {
		n = strings.TrimSpace(n)
		if i := strings.LastIndexAny(n, "/\\"); i >= 0 {
			n = n[i+1:]
		}
		if len(n) < 4 || genericAccountNames[strings.ToLower(n)] {
			continue
		}
		if !slices.Contains(localNames, n) {
			localNames = append(localNames, n)
		}
	}
	// Longest first, so a name that contains another is struck whole.
	slices.SortFunc(localNames, func(a, b string) int { return len(b) - len(a) })
}

// scrubLocalNames strikes the exporting account name wherever it appears,
// case-insensitively, without needing a path around it.
func scrubLocalNames(s string) string {
	for _, n := range localNames {
		if !containsFold(s, n) {
			continue
		}
		s = replaceFold(s, n, "<user>")
	}
	return s
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// replaceFold replaces every case-insensitive occurrence of old in s.
func replaceFold(s, old, new string) string {
	ls, lo := strings.ToLower(s), strings.ToLower(old)
	var b strings.Builder
	for {
		i := strings.Index(ls, lo)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(new)
		s, ls = s[i+len(old):], ls[i+len(lo):]
	}
}

// emailRegex catches an address anywhere in text. A mail address is a personal
// identifier (CLAUDE.md) and it is also the commonest shape of a streaming
// account id, so it is hashed as an account rather than as a device.
var emailRegex = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)

// spotifyUserRegex catches the account inside a Spotify URI. Seven of the 72
// bundles in #971 carried a Spotify identity in clear, one of them a
// spotify:user: URI in firstname.lastname shape, because the /sources pass that
// judges account fields never runs over log text.
var spotifyUserRegex = regexp.MustCompile(`(?i)(spotify:user:)([A-Z0-9._%+#-]+)`)

// accountLogRegex catches an account id written as a log attribute, which is
// the shape the structured passes cannot see: account=..., username=...,
// "account":"...". The value ends at a quote, at the next attribute, or at the
// end of the line, so a match cannot swallow the rest of a log entry.
// Two families, because they carry different risks. account / sourceAccount
// also hold a physical socket label (AUX, TV, CBL-Sat) that the bundle is read
// for, so those are judged by LooksLikeAccountIdentity. username / login / a
// user id never label a socket, so those are masked whatever they hold.
var accountLogRegex = regexp.MustCompile(`(?im)("?\b(?:account|sourceAccount)"?\s*[:=]\s*"?)([^"\n]*?)("|\s+[A-Za-z][A-Za-z0-9_]*[:=]|$)`)

var userNameLogRegex = regexp.MustCompile(`(?im)("?\b(?:username|userName|user_name|userId|user_id|login)"?\s*[:=]\s*"?)([^"\n]*?)("|\s+[A-Za-z][A-Za-z0-9_]*[:=]|$)`)

// friendlyNameLogRegex catches the speaker name as a log attribute. The JSON and
// XML forms are covered above; this is the third, and it is how a default name
// shipped in clear: the firmware writes friendlyName=Bose SoundTouch FD438B into
// a state-change line, and those last six hex are half the MAC.
//
// The leading word boundary is load-bearing, and leaving it out was a
// regression of mine on the day this line was written: a bare `pair`
// alternative matches INSIDE `repair:`, so every log message whose prefix
// ends in those five letters had its text replaced by a hash. "trust store
// repair:", "wrong-state repair:", "resume repair:", "autopair:", "unpair:"
// -- over twenty messages across four files, and exactly the ones that say
// whether a repair worked. Caught the same evening, in a bundle whose erased
// ERROR was the line reporting that the CA store could NOT be repaired.
//
// The optional quote after the separator is load-bearing. slog quotes any
// value containing a space, so friendlyName=Kitchen was struck while
// friendlyName="Living Room" was not: the pattern matched an empty value up
// to the opening quote and left the name itself standing. Measured in a real
// bundle on 2026-09-29. pair= carries a speaker name the same way.
var friendlyNameLogRegex = regexp.MustCompile(`(?im)(\b(?:friendlyName|pair)[:=]"?)([^"\n]*?)("|\s+[A-Za-z][A-Za-z0-9_]*[:=]|$)`)

// boseHostnameRegex catches the speaker name inside the firmware's own
// hostname. The firmware builds it as SoundTouch-<the name the owner chose>,
// and it appears in syslog lines no other pattern touches, so a bundle
// attached to a public issue shipped "hostname:SoundTouch-Kitchen" in clear
// (2026-09-29). The vendor default is SoundTouch-<6 hex of the MAC>, which is
// an identifier too, so both shapes are masked.
var boseHostnameRegex = regexp.MustCompile(`(?i)(SoundTouch-)([A-Za-z0-9_][A-Za-z0-9_-]{1,40})`)

// scrubAccounts hashes the account identities that only appear as free text.
// allDigitsRegex is a numeric service id (Deezer reports one).
var allDigitsRegex = regexp.MustCompile(`^[0-9]+$`)

// LooksLikeAccountIdentity decides whether a value identifies a person rather
// than a socket. Getting it wrong has a cost in both directions, so the rule is
// written around what real boxes report:
//
//   - Names ending in "UserName" are firmware placeholders for an unlinked slot
//     (QPlay1UserName, SpotifyConnectUserName, StoredMusicUserName,
//     AirPlay2DefaultUserName). They name nobody and must survive, because the
//     input filter keys on exactly this suffix.
//   - A linked service reports the real account: a Deezer numeric id, a Spotify
//     user id, a firstname.lastname handle, or an address. Those are hashed.
//   - A physical socket's account is its own short label (AUX, AUX1, TV,
//     CBL-Sat). Those survive, and they are the reason to capture /sources at
//     all: hashing them would leave the bundle unable to answer which inputs a
//     soundbar has.
//
// It lived in the desktop app, where only the structured /sources passes could
// reach it. The same judgement is needed over log TEXT, where an account id is
// written as an attribute and nothing structured ever sees it: that is hole 4 of
// #971, 7 of 72 bundles. One predicate, two callers, for the same reason the
// rest of this package exists.
func LooksLikeAccountIdentity(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasSuffix(v, "UserName") {
		return false
	}
	// An already masked value still IS an identity. Saying otherwise would tell
	// the /sources pass that the object beside it is impersonal, and the
	// nickname next to a masked id would then survive.
	if strings.HasPrefix(v, "ACCT#") {
		return true
	}
	if strings.Contains(v, "@") || allDigitsRegex.MatchString(v) {
		return true
	}
	// A dot between words is a handle (firstname.lastname), which is the shape
	// the leaked Spotify identity in #971 had. No socket label carries one.
	if strings.Contains(v, ".") && !strings.ContainsAny(v, " \t") {
		return true
	}
	// Opaque service ids are long and unbroken; socket labels are short.
	return len(v) >= 16 && !strings.ContainsAny(v, " \t")
}

// MaskAccount hashes an account identity, and hands back anything already
// masked, so a value can pass through more than one anonymising pass.
func MaskAccount(v string) string { return maskAccountValue(v) }

// maskAccountValue hashes one attribute value, and hands back anything that is
// empty or already masked exactly as it was, so the passes stay idempotent.
// isVendorBuildAddress recognises the one address family that is not personal
// data: the build host inside the kernel banner
// ("Linux version 3.14.43+ (epdbuild@hepdswbld04.bose.com)"). Found by running
// the new pass over real bundles, where it hashed that string and took the
// firmware fingerprint with it - docs/MODEL-VARIANTS.md matches incoming
// diagnostics on the exact kernel line.
func isVendorBuildAddress(addr string) bool {
	return strings.HasSuffix(strings.ToLower(addr), ".bose.com")
}

func maskAccountValue(v string) string {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" || trimmed == ssidRedacted || trimmed == "<REDACTED>" || trimmed == "<user>" {
		return v
	}
	// A value that is already a token stays that token. Hashing it again would
	// both break idempotence and cut a link a reader needs: a media server
	// reports its UUID as its username, and masking that a second time left
	// id=UUID#... and username=ACCT#... looking like two different things
	// (seen in a real bundle while this was being written).
	for _, p := range []string{"ACCT#", "NAME#", "UUID#", "DEV#", "MAC#"} {
		if strings.HasPrefix(trimmed, p) {
			return v
		}
	}
	return "ACCT#" + hashShort(strings.ToLower(trimmed))
}

func scrubAccounts(s string) string {
	s = spotifyUserRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := spotifyUserRegex.FindStringSubmatch(m)
		return sub[1] + maskAccountValue(sub[2])
	})
	s = emailRegex.ReplaceAllStringFunc(s, func(m string) string {
		if isVendorBuildAddress(m) {
			return m
		}
		return "ACCT#" + hashShort(strings.ToLower(m))
	})
	s = accountLogRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := accountLogRegex.FindStringSubmatch(m)
		if !LooksLikeAccountIdentity(sub[2]) {
			return m
		}
		return sub[1] + maskAccountValue(sub[2]) + sub[3]
	})
	s = userNameLogRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := userNameLogRegex.FindStringSubmatch(m)
		return sub[1] + maskAccountValue(sub[2]) + sub[3]
	})
	s = boseHostnameRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := boseHostnameRegex.FindStringSubmatch(m)
		return sub[1] + "NAME#" + hashShort(sub[2])
	})
	return s
}

// scrubPII is the single sanitization pass shared by every text blob that can
// leave the host (the app log, box-side logs pulled over SSH, the /api/debug
// state, /api/status). Keeping one function means a field added to the bundle
// cannot accidentally skip a scrub the other paths already do — the exact hole
// that leaked real device IDs and friendly names through anonymizeText while
// sanitizeLog scrubbed them (see #187/#197 diagnostic bundles).
// proxyPayloadRegex finds the base64 upstream STR's stream proxy carries in its
// own URLs, /stream/raw?u=<payload>. Both encodings appear in the field, and a
// payload can itself wrap another proxy URL, so the unwrapper below loops.
// The second shape is /playback/container/<base64 spotify URI>, which has the
// same hole one level down and was not unwrapped: a container key can carry a
// spotify:user:<name> URI, so an account identity survived inside it. Third
// time an encoded value has got out this way (#971, hole 6).
var proxyPayloadRegex = regexp.MustCompile(`(/stream/raw\?u=|/playback/container/)([A-Za-z0-9+/_-]+={0,2})`)

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
			if !utf8.ValidString(s) {
				continue
			}
			// A URL, a Spotify URI, or anything carrying an address. Everything
			// else is left byte for byte: a bundle is evidence, and mangling a
			// value nobody can read is worse than leaving it.
			switch {
			case strings.HasPrefix(s, "http://"), strings.HasPrefix(s, "https://"),
				strings.HasPrefix(s, "spotify:"), ipv4Regex.MatchString(s):
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
	s = scrubHexRuns(s)
	s = nameTagRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := nameTagRegex.FindStringSubmatch(m)
		return "<" + sub[1] + ">NAME#" + hashShort(sub[2]) + "</" + sub[1] + ">"
	})
	s = friendlyNameJSONRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := friendlyNameJSONRegex.FindStringSubmatch(m)
		return sub[1] + "NAME#" + hashShort(sub[2]) + sub[3]
	})
	s = friendlyNameLogRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := friendlyNameLogRegex.FindStringSubmatch(m)
		val := strings.TrimSpace(sub[2])
		if val == "" || strings.HasPrefix(val, "NAME#") || val == ssidRedacted {
			return m
		}
		return sub[1] + "NAME#" + hashShort(val) + sub[3]
	})
	s = scrubAccounts(s)
	s = redactSSIDs(s)
	// Spaced first: the narrower pattern below would otherwise cut the name at
	// its space and leave the surname standing.
	s = userPathSpacedRegex.ReplaceAllString(s, "${1}<user>${3}")
	s = userPathRegex.ReplaceAllString(s, "${1}<user>")
	// Last, and not anchored on anything: the account name of the machine that
	// exports, wherever it sits.
	s = scrubLocalNames(s)
	return s
}

// salt is mixed into every pseudonym. It is set once by the program that
// exports a bundle and never appears in one.
//
// It exists because the unsalted form was a pseudonym in name only. A MAC is a
// 24-bit sweep once the vendor prefix is known, which is about 25 seconds of
// plain Python, and a 306-word list of common room names recovered 14% of the
// speaker names in one corpus in under a millisecond. Measured over 72 real
// bundles in #971, all of them carrying "Anonymized: true".
//
// The property the unsalted hash was chosen for is kept: the same speaker still
// carries the same token across reports months apart, because the salt belongs
// to the machine that exports, not to the moment. What goes away is a stranger
// being able to turn that token back into an address, or to join two households
// who happen to have a speaker called Kitchen.
var salt []byte

// SetSalt installs the per-installation salt. Call it once at startup with a
// value that is stored locally and never exported.
func SetSalt(b []byte) { salt = append([]byte(nil), b...) }

func hashShort(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))[:8]
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

// HashShort is the stable pseudonym for an identifier: the first 8 hex chars of
// a salted SHA256. Stable for as long as the exporting machine keeps its salt,
// so the same speaker is recognisable across two reports months apart, and
// meaningless to anybody who does not have that salt.
func HashShort(s string) string { return hashShort(s) }
