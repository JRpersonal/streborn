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
//
// Two escapes count as label text too, so the part in front of one is not
// left in clear: \uXXXX in raw JSON ("Alex's-iMac.local") and the \NNN
// decimal escape DNS-SD writes for a space ("Alex\032MacBook._raop._tcp.local").
var localHostRegex = regexp.MustCompile(`(?i)(?:[\p{L}\p{N}_'’-]|\\u[0-9a-f]{4}|\\[0-9]{3})+(?:\.(?:[\p{L}\p{N}_'’-]|\\u[0-9a-f]{4}|\\[0-9]{3})+)*\.local\.?`)

// keepLocalHosts are .local names that identify nobody: the boot script the
// box runs (a FILE, /mnt/nv/rc.local, which the bundle is read for) and the
// SoundTouch chassis code names, which a few firmware builds use as host name.
var keepLocalHosts = map[string]bool{
	"rc": true, "rhino": true, "spotty": true, "mojo": true, "taigan": true,
	"lisa": true, "ginger": true, "scm": true, "sm2": true,
}

// mediaServerNameRegex is "<Product>[<host>]", the shape MinimServer (and
// anything modelled on it) gives itself, wherever it appears. The product is a
// capitalised word right before the bracket. The bracket may only hold what a
// host name can: letters, digits, '-', '_', '.', spaces and apostrophes. That
// keeps the firmware's state traces (StdOp[Standby(ENTER), Standby(START)]),
// header maps (Content-Type[text/xml]) and Go's map[k:v] out. On its own that
// still caught syslog tags (wpa_supplicant[wlan0]), so the content also has to
// look like a machine name; see looksLikeHost. Log lines that name a media
// server under a key are handled more thoroughly by scrubMediaServerAttrs.
var mediaServerNameRegex = regexp.MustCompile(`(\p{Lu}[\p{L}\p{N}_-]*)\[([\p{L}\p{N}_ .'’-]{4,80})\]`)

// looksLikeHost is true for bracket content shaped like a machine name: a
// hyphenated or dotted name (Alexs-MacBook-Pro, nas.local), one with an
// apostrophe (Alex's Mac mini), or several words. A single plain word (wlan0,
// eth0) is an interface or a tag, not a host.
func looksLikeHost(s string) bool {
	if !strings.ContainsFunc(s, unicode.IsLetter) {
		return false
	}
	return strings.ContainsAny(s, "-.'’ ")
}

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

// instanceStart returns where a DNS-SD service instance name that ends at
// start really begins. Instance names are free text ("Alex's MacBook
// Pro._raop._tcp.local."), so the regex stops at the first space. The name is
// extended backwards over letters, digits, apostrophes, backslash escapes and
// spaces as far as an opening quote. Without a quote, a space is where the
// surrounding sentence begins ("browse hit Kitchen._soundtouch..."), so only
// the space-free part in front is taken.
func instanceStart(s string, start int) int {
	firstSpace := -1 // position after the nearest space crossed
	i := start
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:i])
		switch {
		case r == '"':
			return i
		case r == ' ':
			if firstSpace < 0 {
				firstSpace = i
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(`-_'’\`, r):
		default:
			if firstSpace >= 0 {
				return firstSpace
			}
			return i
		}
		i -= size
	}
	if firstSpace >= 0 {
		return firstSpace
	}
	return 0
}

// isFilePath is true when the match at s[start:end] is the last element of a
// file path (/etc/hosts.local), not a host: a single slash in front, and
// nothing but white space, a quote or the end of the text after it. A URL
// host has two slashes in front.
func isFilePath(s string, start, end int) bool {
	if start == 0 || s[start-1] != '/' || (start >= 2 && s[start-2] == '/') {
		return false
	}
	if end == len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[end:])
	return unicode.IsSpace(r) || r == '"' || r == '\''
}

// scrubLocalHosts hashes every personal ".local" name in s.
//
// A service type (_spotify-connect._tcp.local) stays readable; a service
// INSTANCE in front of one (Kitchen._soundtouch._tcp.local) is somebody's name
// and is hashed whole with the type kept. A name glued to a token (the
// firmware host SoundTouch-NAME#..., Bose-SM2-DEV#...) has already been masked
// by a pass that knows its shape and is left alone.
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
		last := prev
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
		if isFilePath(s, start, end) {
			b.WriteString(m)
			continue
		}
		// Quotes around the name belong to the text, not to the host.
		lead := len(m) - len(strings.TrimLeft(m, "'’"))
		b.WriteString(m[:lead])
		m = m[lead:]
		labels := strings.Split(strings.TrimSuffix(m, "."), ".")
		host := labels[:len(labels)-1] // without "local"
		instance := false
		for i, l := range host {
			if strings.HasPrefix(l, "_") {
				host = host[:i]
				instance = true
				break
			}
		}
		h := strings.Join(host, ".")
		if strings.Trim(h, "-'’.") == "" || (len(host) == 1 && keepLocalHosts[strings.ToLower(host[0])]) {
			b.WriteString(m)
			continue
		}
		rest := m[len(h):]
		if instance && lead == 0 {
			// Take back the part of a spaced instance name the regex left out;
			// it has already been written to b, so rewind b to before it.
			if from := max(instanceStart(s, start), last); from < start {
				out := b.String()
				b.Reset()
				b.WriteString(out[:len(out)-(start-from)])
				h = s[from:start] + h
			}
		}
		b.WriteString("HOST#" + hashShort(strings.ToLower(h)) + rest)
	}
	b.WriteString(s[prev:])
	return b.String()
}

// scrubMediaServerNames hashes the machine part of "<Product>[<host>]".
func scrubMediaServerNames(s string) string {
	return mediaServerNameRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := mediaServerNameRegex.FindStringSubmatch(m)
		product, host := sub[1], sub[2]
		if !looksLikeHost(host) {
			return m
		}
		return product + "[" + maskHost(host) + "]"
	})
}

// A media server's friendly name is logged under a key on the agent's and the
// desktop's library lines, and only some servers use the bracket shape above:
//
//	Universal Media Server [Alexs-MacBook-Pro]
//	Serviio (Alexs-MacBook-Pro)
//	Plex Media Server: Alexs-MacBook-Pro
//	Jellyfin - Alexs-PC
//	Alexs-PC: alex:                (Windows Media Player: host, then account)
//	DiskStation                    (Synology and many others: the host alone)
//
// On those lines the value under the server-name keys is masked by shape,
// whatever is in it. Older agents on the speakers keep writing the names in
// clear, so this has to happen here, on the way out, rather than at the source.

// mediaServerLineRegex is a whole log line written by the library, DLNA and
// media-server code.
var mediaServerLineRegex = regexp.MustCompile(`(?m)^[^\n]*\bmsg="?(dlna:|library resolve:|library:|media server:|library search:|library browse|folder card replay:|queue)[^\n]*`)

// serverNameUnderName are the line prefixes whose "name" key is a server. On
// the others (a queue preset save, a library key) it is the music.
var serverNameUnderName = map[string]bool{
	"dlna:": true, "library resolve:": true, "library:": true, "media server:": true,
}

// mediaServerAttrRegex is a key on such a line that carries a server's
// friendly name ("seen" is a comma-separated list of them), quoted or bare.
var mediaServerAttrRegex = regexp.MustCompile(`(\b(?:server|name|registered|seen)=)(?:"((?:[^"\\\n]|\\.)*)"|([^\s"]+))`)

// knownServerProducts are friendly names that name the software, not the
// machine, when they stand alone ("MinimServer", "FRITZ!Box 7590 Mediaserver").
var knownServerProducts = []string{
	"minimserver", "plex media server", "jellyfin", "emby", "serviio",
	"universal media server", "twonky", "kodi", "gerbera", "minidlna",
	"readymedia", "mediatomb", "asset upnp", "bubbleupnp", "fritz!box",
	"logitech media server", "lyrion", "foobar2000",
}

// productWords may follow a known product without making the name personal.
var productWords = map[string]bool{
	"server": true, "media": true, "mediaserver": true, "dlna": true, "upnp": true,
}

// isKnownProductName is true for a friendly name that is a known product,
// optionally followed by a model number or a generic word.
func isKnownProductName(v string) bool {
	low := strings.ToLower(v)
	for _, p := range knownServerProducts {
		if low == p {
			return true
		}
		if !strings.HasPrefix(low, p+" ") {
			continue
		}
		for _, w := range strings.Fields(low[len(p):]) {
			if r, _ := utf8.DecodeRuneInString(w); !unicode.IsDigit(r) && !productWords[w] {
				return false
			}
		}
		return true
	}
	return false
}

// maskMediaServerName masks one media server friendly name. A product in
// front of a bracket, a parenthesis, ": " or " - " is kept and what follows is
// hashed; a name with no such separator is kept only when it is a known
// product and hashed whole otherwise, since then it usually IS the host.
func maskMediaServerName(v string) string {
	t := strings.TrimSpace(v)
	if t == "" || strings.Contains(t, "#") {
		return v // empty, or already a token
	}
	// Windows Media Player: "<host>: <account>:".
	if strings.HasSuffix(t, ":") {
		if i := strings.Index(t, ": "); i > 0 && i < len(t)-2 {
			return maskHost(t[:i]) + ": ACCT#" + hashShort(strings.ToLower(strings.TrimSpace(t[i+2:len(t)-1]))) + ":"
		}
	}
	hostStart, hostEnd := -1, len(t)
	switch {
	case strings.HasSuffix(t, "]") && strings.LastIndex(t, "[") > 0:
		hostStart, hostEnd = strings.LastIndex(t, "[")+1, len(t)-1
	case strings.HasSuffix(t, ")") && strings.LastIndex(t, "(") > 0:
		hostStart, hostEnd = strings.LastIndex(t, "(")+1, len(t)-1
	case strings.Index(t, ": ") > 0:
		hostStart = strings.Index(t, ": ") + 2
	case strings.Index(t, " - ") > 0:
		hostStart = strings.Index(t, " - ") + 3
	}
	if hostStart > 0 && strings.TrimSpace(t[hostStart:hostEnd]) != "" {
		return t[:hostStart] + maskHost(strings.TrimSpace(t[hostStart:hostEnd])) + t[hostEnd:]
	}
	if isKnownProductName(t) {
		return v
	}
	return maskHost(t)
}

// scrubMediaServerAttrs masks the server friendly names on library lines.
func scrubMediaServerAttrs(s string) string {
	if !strings.Contains(s, "msg=") {
		return s
	}
	return mediaServerLineRegex.ReplaceAllStringFunc(s, func(line string) string {
		nameIsServer := serverNameUnderName[mediaServerLineRegex.FindStringSubmatch(line)[1]]
		return mediaServerAttrRegex.ReplaceAllStringFunc(line, func(m string) string {
			sub := mediaServerAttrRegex.FindStringSubmatch(m)
			key, val, quoted := sub[1], sub[3], false
			if key == "name=" && !nameIsServer {
				return m
			}
			if sub[3] == "" && strings.HasPrefix(m[len(key):], `"`) {
				val, quoted = sub[2], true
			}
			var out string
			if key == "seen=" {
				parts := strings.Split(val, ", ")
				for i, p := range parts {
					parts[i] = maskMediaServerName(p)
				}
				out = strings.Join(parts, ", ")
			} else {
				out = maskMediaServerName(val)
			}
			if quoted {
				return key + `"` + out + `"`
			}
			return key + out
		})
	})
}
