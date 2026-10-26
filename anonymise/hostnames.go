package anonymise

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Host names of the owner's OTHER machines: the mDNS name of a laptop or a NAS,
// and the host part a media server builds into its own friendly name.
//
// Neither has a key in front of it, so none of the attribute passes above ever
// saw one. A public bundle's agent log carried
//
//	msg="library search: discovery missed a registered server, re-probed its last known address" server="MinimServer[<first name>s-MacBook-Pro.local]"
//
// because MinimServer names itself after the machine it runs on, and macOS
// names the machine after its owner. The product name in front of the bracket
// is kept, since which server software a household runs is what a library
// report is read for; only the machine part becomes a HOST# token.
//
// The token is the same whether the host is written with or without its
// ".local" suffix, so "MinimServer[HOST#x]" in one line and "HOST#x.local:9790"
// in another still read as one machine.

// localHostRegex is a dotted name ending in ".local". Labels may carry
// letters of any script, digits, hyphens, underscores and apostrophes: an
// mDNS responder that does not transliterate publishes "Jöns-iMac.local", and
// a name typed with an apostrophe keeps it. The end of the name is checked in
// Go (Go's \b is ASCII only), so ".localhost" and ".locale" do not match.
var localHostRegex = regexp.MustCompile(`(?i)[\p{L}\p{N}_'’-]+(?:\.[\p{L}\p{N}_'’-]+)*\.local\.?`)

// keepLocalHosts are .local names that identify nobody: the boot script the
// box runs (a FILE, /mnt/nv/rc.local, which the bundle is read for) and the
// SoundTouch chassis code names, which a few firmware builds use as host name.
var keepLocalHosts = map[string]bool{
	"rc": true, "rhino": true, "spotty": true, "mojo": true, "taigan": true,
	"lisa": true, "ginger": true, "scm": true, "sm2": true,
}

// mediaServerNameRegex is "<Product>[<host>]", the shape MinimServer (and
// anything modelled on it) gives itself. The product is the letter run right
// before the bracket. The host may hold spaces and apostrophes ("Alex's Mac
// mini") but nothing that marks a different construct: a colon or an equals
// sign (Go's map[k:v], key=value), braces (generic type shapes in a panic),
// quotes, a backslash, a hash (an existing token) or angle brackets (a marker).
var mediaServerNameRegex = regexp.MustCompile(`([\p{L}][\p{L}\p{N}_-]*)\[([^\[\]\n"\:=#{}<>]{4,80})\]`)

// notAProduct are words that put a bracket right after themselves for other
// reasons.
var notAProduct = map[string]bool{"map": true}

// maskHost turns one host name into its HOST# token. A trailing ".local" (and
// its root dot) is not part of what is hashed and is put back, so the token is
// the same in either spelling.
func maskHost(h string) string {
	name, suffix := h, ""
	if i := len(name) - len(".local."); i > 0 && strings.EqualFold(name[i:], ".local.") {
		name, suffix = name[:i], name[i:]
	} else if i := len(name) - len(".local"); i > 0 && strings.EqualFold(name[i:], ".local") {
		name, suffix = name[:i], name[i:]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return h
	}
	return "HOST#" + hashShort(strings.ToLower(name)) + suffix
}

// isHostRune is a character that can continue a host label.
func isHostRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_'
}

// scrubLocalHosts hashes every personal ".local" name in s.
//
// A service type (_spotify-connect._tcp.local) stays readable; a service
// INSTANCE in front of one (Kitchen._soundtouch._tcp.local) is somebody's name
// and is hashed with the type kept. A name glued to a token (the firmware
// host SoundTouch-NAME#..., Bose-SM2-DEV#...) has already been masked by a
// pass that knows its shape and is left alone.
func scrubLocalHosts(s string) string {
	locs := localHostRegex.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prev := 0
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		m := s[start:end]
		b.WriteString(s[prev:start])
		prev = end
		// The name has to end here: ".localhost" or ".locale" is not mDNS.
		if r, _ := utf8.DecodeRuneInString(s[end:]); end < len(s) && isHostRune(r) {
			b.WriteString(m)
			continue
		}
		if r, _ := utf8.DecodeLastRuneInString(s[:start]); start > 0 && r == '#' {
			b.WriteString(m)
			continue
		}
		// Quotes around the name belong to the text, not to the host.
		lead := len(m) - len(strings.TrimLeft(m, "'’"))
		b.WriteString(m[:lead])
		m = m[lead:]
		labels := strings.Split(strings.TrimSuffix(m, "."), ".")
		host := labels[:len(labels)-1] // without "local"
		for i, l := range host {
			if strings.HasPrefix(l, "_") {
				host = host[:i]
				break
			}
		}
		if len(host) == 0 || (len(host) == 1 && keepLocalHosts[strings.ToLower(host[0])]) {
			b.WriteString(m)
			continue
		}
		h := strings.Join(host, ".")
		b.WriteString("HOST#" + hashShort(strings.ToLower(h)) + m[len(h):])
	}
	b.WriteString(s[prev:])
	return b.String()
}

// scrubMediaServerNames hashes the machine part of "<Product>[<host>]".
func scrubMediaServerNames(s string) string {
	return mediaServerNameRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := mediaServerNameRegex.FindStringSubmatch(m)
		product, host := sub[1], sub[2]
		if notAProduct[strings.ToLower(product)] || !strings.ContainsFunc(host, unicode.IsLetter) {
			return m
		}
		return product + "[" + maskHost(host) + "]"
	})
}
