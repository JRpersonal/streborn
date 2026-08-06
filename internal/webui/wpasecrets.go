package webui

// Keep the user's Wi-Fi password out of everything that leaves the speaker.
//
// /etc/wpa_supplicant.conf holds the passphrase in plain text. The diagnostic
// state is served by an unauthenticated GET on the LAN and is the body of every
// bundle mailed in or attached to an issue, so the file cannot go out as it is.
//
// The whole file is still worth having: how many network={} blocks a speaker
// carries is exactly the question that explains a box which keeps rejoining the
// guest network, and key_mgmt / scan_ssid / priority all matter for that. Only
// the secrets are replaced, in place, so the shape of the file survives.

import (
	"regexp"
	"strings"
)

// wpaSecretKeys are the fields that carry a credential in a wpa_supplicant
// config. Deliberately a list of names rather than a guess at what looks
// secret: a new key nobody redacted is how these leaks happen.
var wpaSecretKeys = []string{
	"psk", "password", "passwd", "passphrase", "sae_password", "wep_key0",
	"wep_key1", "wep_key2", "wep_key3", "private_key_passwd", "pin", "ext_password",
}

// wpaSecretLine matches `key=value` for one of the names above.
//
// The value pattern is the point of this whole file. A pattern that stops at
// whitespace looks right and is wrong: `psk="two words"` would keep everything
// after the space, which is a perfectly ordinary passphrase and the exact case
// the desktop app's scrubber let through. So a QUOTED value is consumed to its
// closing quote, and only an unquoted one stops at whitespace.
var wpaSecretLine = regexp.MustCompile(
	`(?im)^(\s*(?:` + strings.Join(wpaSecretKeys, "|") + `)\s*=\s*)("(?:[^"\\]|\\.)*"?|\S*)`)

// redactWPASecrets replaces every credential value with a marker that says how
// long it was, which is occasionally useful ("the password is empty" is a real
// diagnosis) and reveals nothing else.
func redactWPASecrets(conf string) string {
	if conf == "" {
		return conf
	}
	return wpaSecretLine.ReplaceAllStringFunc(conf, func(m string) string {
		sub := wpaSecretLine.FindStringSubmatch(m)
		if len(sub) != 3 {
			return "<redacted>"
		}
		val := strings.Trim(sub[2], `"`)
		return sub[1] + `"<redacted:` + itoa(len(val)) + ` chars>"`
	})
}

// itoa avoids pulling strconv in for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
