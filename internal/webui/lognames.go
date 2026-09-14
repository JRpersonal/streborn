package webui

import (
	"fmt"
	"regexp"
)

// The speaker's own run.sh log writes the Wi-Fi network name in clear, and that
// log goes out in every diagnostic bundle.
//
// Found in a PUBLIC GitHub attachment on 2026-09-13: a reporter's household
// network name sat in setup_log_prev as
//
//	wlan.conf parsed: SSID='<their network>' password_length=37
//
// while the structured wlan_configured list beside it was properly redacted.
// One leak had been closed and this one had not, which is the shape these
// things usually have.
//
// The password was never in there: run.sh has always written password_length
// rather than the value, and several of its lines already use name_length for
// the SSID too. So this is the same idea applied to the lines that missed it,
// and applied at the reading end as well, because the logs already sitting on
// every speaker were written by the old code and are exported unchanged.
//
// The length is kept. "did the box get an SSID at all, and was it the long one
// or the short one" is a real diagnostic question, and it is answerable without
// naming anybody's network.
var ssidAssignment = regexp.MustCompile(`(?i)\b(ssid)\s*=\s*('[^']*'|"[^"]*"|[^\s,;)'"]+)`)

// redactNetworkNamesInLog masks the value of every ssid= assignment in a raw
// log tail, leaving the key and the length behind.
func redactNetworkNamesInLog(s string) string {
	if s == "" {
		return s
	}
	return ssidAssignment.ReplaceAllStringFunc(s, func(m string) string {
		g := ssidAssignment.FindStringSubmatch(m)
		if len(g) != 3 {
			return m
		}
		val := g[2]
		// Strip one layer of quotes for the length, so 'abc' counts as 3.
		if len(val) >= 2 && (val[0] == '\'' || val[0] == '"') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if val == "" || val == "<REDACTED>" {
			return m
		}
		return fmt.Sprintf("%s=<REDACTED name_length=%d>", g[1], len(val))
	})
}
