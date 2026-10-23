// Package versioncmp holds the ONE rule STR uses to order its version strings.
// The desktop app's update check, its OTA downgrade guard and update-intent
// bookkeeping, and the speaker's self-update (internal/updatesig) all compare
// through it, so the two sides can never disagree about which build is newer.
// It lives outside internal/ because the desktop app is its own Go module and
// Go forbids importing another module's internal packages.
//
// The rule: drop the leading "v", split on ".", and read the leading digits of
// every segment; a segment that does not start with a digit ends the parse.
// Only those numbers are compared, numerically, and a missing trailing segment
// counts as zero. What that means for the strings STR actually produces, all
// of it deliberate:
//
//   - "v1.0.19-3-gabc123-dirty" (a git-describe dev build) reads as [1 0 19]
//     and equals the v1.0.19 release, so a dev build never flags the box it
//     just updated as out of date.
//   - "v1.0.19-preview.1" reads as [1 0 19 1]: previews order among
//     themselves by N, and sit above the plain v1.0.19 they are cut from, so an
//     installed preview is not offered, or OTA-pushed, the plain release as if
//     that were newer. (The website manifest never serves a preview.)
//   - "1.2.3rc1" reads as [1 2 3]; "" and "vdev" have no parts at all, which
//     Parts reports as nil so callers can tell an unparseable version apart
//     from v0.
//   - Numeric, never lexical: v1.0.2 is older than v1.0.10.
package versioncmp

import "strings"

// Less reports whether version a is strictly older than version b under the
// package rule.
func Less(a, b string) bool {
	pa, pb := Parts(a), Parts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// Parts returns the leading numeric segments of v under the package rule, or
// nil when v does not start with a digit after its optional "v".
func Parts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	var parts []int
	for _, seg := range strings.Split(v, ".") {
		n, ok := 0, false
		for _, r := range seg { // the first non-digit ends the segment (git or pre-release suffix)
			if r < '0' || r > '9' {
				break
			}
			n, ok = n*10+int(r-'0'), true
		}
		if !ok {
			break
		}
		parts = append(parts, n)
	}
	return parts
}
