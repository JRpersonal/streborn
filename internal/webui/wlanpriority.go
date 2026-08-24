// NetworkProfiles.xml priority assertion after a deliberate runtime Wi-Fi
// switch (#697).
//
// STR's live switch wins the running session but used to lose the ranking war:
// Bose NetManager keeps its own profile store at
// /mnt/nv/BoseApp-Persistence/<N>/NetworkProfiles.xml, and after a switch that
// store still ranked the OLD network highest. On the reporter's ST10 (rhino,
// v0.9.53) the file held the old SSID at priority="1" and the new one at
// priority="0", and `wpa_cli list_networks` showed the old SSID as [CURRENT]:
// every time the firmware reasserted its stored ranking, the speaker silently
// reconnected to the old Wi-Fi. The reporter proved the end state on his own
// box: raising the new profile to priority="10" and demoting the old one to
// "0" made the new network stick reliably. That repair is the specification
// this file implements.
//
// The rewrite edits ONLY priority attribute values (inserting the attribute on
// the chosen profile if it has none). Everything else is preserved byte for
// byte on purpose: the passphrase/wepKey attributes hold AES ciphertext only
// NetManager can produce, so an XML round-trip that re-escaped or reordered
// anything could corrupt the one copy of the credential the firmware trusts.
// For the same reason the old profile is demoted, never deleted: it stays the
// user's working fallback if the new network later disappears.
//
// One claim is deliberately not made: BoseApp may hold this file in memory and
// flush it later, which could overwrite the edit on some boxes. The field
// evidence (#697: the reporter's manual edit of the same file on a running box
// stuck) is the strongest support available, so the rewrite is logged and
// best-effort rather than treated as guaranteed.

package webui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JRpersonal/streborn/internal/atomicfile"
)

// networkProfilesGlob matches NetManager's persisted Wi-Fi profile store. Same
// pattern the settings and diagnostic readers already use; BCO boxes never
// create the file (their profile lives in AirplayConfiguration.xml), which is
// one more reason this assert only runs on the wpa chassis path.
const networkProfilesGlob = "/mnt/nv/BoseApp-Persistence/*/NetworkProfiles.xml"

const (
	// wlanChosenPriority is what the deliberately chosen network is raised to,
	// and wlanDemotedPriority what every other profile is set to. 10/0 are the
	// exact values the #697 reporter verified on his own ST10; the firmware's
	// own writes have only ever been observed at 0 (fresh add) and 1, so 10
	// keeps the chosen profile strictly on top of anything NetManager assigns.
	wlanChosenPriority  = 10
	wlanDemotedPriority = 0
)

// raiseFirmwareProfilePriority makes the firmware's own profile store prefer
// ssid: the chosen profile goes to wlanChosenPriority, all others to
// wlanDemotedPriority. Called only after a CONFIRMED live association (the
// wpaConfirmed arm): on an unverified password the old ranking must survive so
// the box can fall back to its previous network.
//
// When the chosen SSID is not in the store yet (first switch to a network the
// firmware never saw), only the demotions happen; the profile itself is added
// by the firmware on the apply-marker boot (/addWirelessProfile), which STR
// cannot pre-empt because the passphrase attribute must be NetManager's own
// ciphertext.
func (s *Server) raiseFirmwareProfilePriority(ssid string) {
	matches, _ := filepath.Glob(networkProfilesGlob)
	if len(matches) == 0 {
		// Nothing to assert. Normal on a box whose firmware store was wiped
		// (True Factory Reset) or never populated; the wpa-side priority in
		// the conf still protects the running session.
		s.logger.Info("WLAN: no NetworkProfiles.xml on this box, firmware ranking not asserted", "ssid", ssid)
		return
	}
	for _, p := range matches {
		found, changed, err := rewriteProfilePriorityFile(p, ssid)
		switch {
		case err != nil:
			s.logger.Warn("WLAN: NetworkProfiles priority rewrite failed", "path", p, "ssid", ssid, "err", err)
		case changed:
			s.logger.Info("WLAN: NetworkProfiles ranking asserted for new network", "path", p, "ssid", ssid, "profilePresent", found)
		default:
			s.logger.Info("WLAN: NetworkProfiles ranking already prefers new network", "path", p, "ssid", ssid, "profilePresent", found)
		}
	}
}

// rewriteProfilePriorityFile applies setProfilePriorities to one profile store
// file, atomically and durably (these boxes cut power at standby, and a torn
// profile store would cost the user every stored network). No write happens
// when the ranking is already correct, so repeated switches to the same
// network do not wear the NAND.
func rewriteProfilePriorityFile(path, ssid string) (found, changed bool, err error) {
	b, rerr := os.ReadFile(path)
	if rerr != nil {
		return false, false, rerr
	}
	out, found, changed := setProfilePriorities(string(b), ssid)
	if !changed {
		return found, false, nil
	}
	mode := os.FileMode(0o644)
	if fi, serr := os.Stat(path); serr == nil {
		mode = fi.Mode().Perm()
	}
	if werr := atomicfile.WriteFile(path, []byte(out), mode); werr != nil {
		return found, false, werr
	}
	return found, true, nil
}

// setProfilePriorities rewrites the priority attributes in a NetworkProfiles
// document so the profile whose SSID equals ssid ranks strictly highest.
// Attribute NAMES are matched case-insensitively (the firmware writes SSID
// uppercase, priority lowercase); the SSID VALUE is compared exactly, after
// undoing XML attribute escaping. Only priority values change; every other
// byte of the document survives untouched.
func setProfilePriorities(doc, ssid string) (out string, found, changed bool) {
	type edit struct {
		start, end int
		text       string
	}
	var edits []edit
	for _, tag := range scanProfileTags(doc) {
		var ssidVal string
		var prio *profileAttr
		for k := range tag.attrs {
			a := &tag.attrs[k]
			switch {
			case strings.EqualFold(a.name, "ssid"):
				ssidVal = doc[a.valStart:a.valEnd]
			case strings.EqualFold(a.name, "priority"):
				prio = a
			}
		}
		// xmlAttrUnescape (recall_verify.go): the store holds XML-escaped
		// attribute values, the switch request holds what the user typed.
		isTarget := xmlAttrUnescape(ssidVal) == ssid
		if isTarget {
			found = true
		}
		want := wlanDemotedPriority
		if isTarget {
			want = wlanChosenPriority
		}
		switch {
		case prio != nil:
			if doc[prio.valStart:prio.valEnd] != strconv.Itoa(want) {
				edits = append(edits, edit{prio.valStart, prio.valEnd, strconv.Itoa(want)})
			}
		case isTarget:
			// The chosen profile carries no priority attribute: insert one right
			// after the tag name so the rank is explicit rather than relying on
			// NetManager's default.
			edits = append(edits, edit{tag.nameEnd, tag.nameEnd, ` priority="` + strconv.Itoa(want) + `"`})
		default:
			// A non-target profile without a priority attribute already ranks at
			// the firmware default (0, per every observed store): leave its
			// bytes alone.
		}
	}
	if len(edits) == 0 {
		return doc, found, false
	}
	var b strings.Builder
	last := 0
	for _, e := range edits { // edits arrive in document order
		b.WriteString(doc[last:e.start])
		b.WriteString(e.text)
		last = e.end
	}
	b.WriteString(doc[last:])
	return b.String(), found, true
}

// profileAttr is one name="value" attribute inside a <profile> start tag, with
// the value's byte range so an edit can replace exactly those bytes.
type profileAttr struct {
	name             string
	valStart, valEnd int
}

// profileTag is one located <profile .../> start tag.
type profileTag struct {
	// nameEnd is the offset right after "<profile", the insertion point for an
	// attribute the tag lacks.
	nameEnd int
	attrs   []profileAttr
}

// scanProfileTags finds every <profile ...> start tag. A hand-rolled scanner
// instead of a regexp or an XML decoder on purpose: the tag can span lines
// (the #697 report shows attributes wrapped mid-tag), attribute values may
// legally contain '>' (an SSID is arbitrary bytes), and a decoder round-trip
// would re-serialize the ciphertext-carrying attributes this file must never
// touch. Byte offsets into the ORIGINAL document are the whole point.
func scanProfileTags(doc string) []profileTag {
	const open = "<profile"
	var tags []profileTag
	i := 0
	for i+len(open) <= len(doc) {
		if doc[i] != '<' || !strings.EqualFold(doc[i:i+len(open)], open) {
			i++
			continue
		}
		nameEnd := i + len(open)
		if nameEnd < len(doc) {
			switch doc[nameEnd] {
			case ' ', '\t', '\n', '\r', '/', '>':
				// A real <profile> tag; attributes (or the tag end) follow.
			default:
				// A longer element name that merely starts with "profile".
				i = nameEnd
				continue
			}
		}
		tag, next := parseProfileAttrs(doc, nameEnd)
		tag.nameEnd = nameEnd
		tags = append(tags, tag)
		i = next
	}
	return tags
}

// parseProfileAttrs walks the attribute list from pos to the tag-closing '>',
// honouring quotes so a quoted value can contain '>', '/', or newlines without
// ending the tag. Returns the tag and the offset just past the '>'.
func parseProfileAttrs(doc string, pos int) (profileTag, int) {
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
	var tag profileTag
	i := pos
	for i < len(doc) {
		for i < len(doc) && (isSpace(doc[i]) || doc[i] == '/') {
			i++
		}
		if i >= len(doc) {
			return tag, i
		}
		if doc[i] == '>' {
			return tag, i + 1
		}
		ns := i
		for i < len(doc) && doc[i] != '=' && doc[i] != '>' && !isSpace(doc[i]) {
			i++
		}
		name := doc[ns:i]
		for i < len(doc) && isSpace(doc[i]) {
			i++
		}
		if i >= len(doc) || doc[i] != '=' {
			continue // bare attribute without a value: keep scanning
		}
		i++
		for i < len(doc) && isSpace(doc[i]) {
			i++
		}
		if i >= len(doc) || (doc[i] != '"' && doc[i] != '\'') {
			continue // malformed value: skip rather than guess
		}
		q := doc[i]
		i++
		vs := i
		for i < len(doc) && doc[i] != q {
			i++
		}
		ve := i
		if i < len(doc) {
			i++ // consume the closing quote
		}
		tag.attrs = append(tag.attrs, profileAttr{name: name, valStart: vs, valEnd: ve})
	}
	return tag, i
}
