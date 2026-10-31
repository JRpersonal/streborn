// Package speakername holds the ONE rule STR uses for the names it writes onto
// a speaker. The agent's rename endpoint, the setup wizard's pending name, the
// stick's name file and the desktop app's own setup-AP rename all go through
// it. It lives outside internal/ because the desktop app is its own Go module
// and Go forbids importing another module's internal packages.
//
// Why a rule is needed at all: the firmware's UPnP renderer (STSCertified on
// :8091) pastes the speaker's name into its device description WITHOUT XML
// escaping it. A name with "&" served <friendlyName>Test&B</friendlyName>, and
// after the next boot the renderer failed to come up at all: the description
// and every SOAP call answered 404, the syslog said
// "gupnp_root_device_get_ssdp_resource_group: assertion
// 'GUPNP_IS_ROOT_DEVICE (root_device)' failed". Renaming and rebooting brought
// it back. A name stored as invalid UTF-8 (a lone Latin-1 byte) broke it the
// same way. Measured on a Portable, FW 27.0.6, 2026-10-28 (#1582). Spotify and
// the music library both play through that renderer, so neither works on such
// a speaker while native radio keeps playing.
//
// The rule:
//   - "&" becomes "+" ("Tom & Jerry" reads as "Tom + Jerry"),
//   - "<" and ">" are dropped,
//   - a byte that is not valid UTF-8 is read as Latin-1, which is what the
//     firmware's own umlaut bug produces (0xFC is "ü"),
//   - control characters are dropped (XML 1.0 forbids them in text too),
//   - surrounding white space is trimmed.
package speakername

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sanitise returns name rewritten so the speaker's firmware survives it (see
// the package comment). A name that is already safe comes back unchanged.
func Sanitise(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); {
		r, size := utf8.DecodeRuneInString(name[i:])
		if r == utf8.RuneError && size == 1 {
			// Not UTF-8: read the byte as Latin-1, so "K\xfcche" is "Küche".
			r = rune(name[i])
		}
		i += size
		switch {
		case r == '&':
			b.WriteByte('+')
		case r == '<' || r == '>':
			// dropped
		case unicode.IsControl(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// Unsafe reports whether name carries something that breaks the speaker's
// UPnP renderer: "&", "<", ">", a control character, or bytes that are not
// valid UTF-8.
func Unsafe(name string) bool {
	if !utf8.ValidString(name) {
		return true
	}
	for _, r := range name {
		if r == '&' || r == '<' || r == '>' || unicode.IsControl(r) {
			return true
		}
	}
	return false
}
