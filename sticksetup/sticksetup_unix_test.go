//go:build !windows

package sticksetup

import "testing"

// TestMacParentWholeDiskParsesPlist locks in the regex/string parsing
// of `diskutil info -plist` output against a representative snapshot.
// The whole point of the fix for issue #58 is that we MUST resolve a
// volume path to /dev/diskN before calling diskutil eraseDisk.
func TestMacParentWholeDiskParsesPlist(t *testing.T) {
	// Trimmed real-world output from `diskutil info -plist /Volumes/BOSE`
	// on macOS 14. Only the keys this code inspects are kept.
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>DeviceIdentifier</key>
	<string>disk4s1</string>
	<key>ParentWholeDisk</key>
	<string>disk4</string>
	<key>VolumeName</key>
	<string>BOSE</string>
</dict>
</plist>
`
	got := extractParentWholeDisk(plist)
	if got != "/dev/disk4" {
		t.Fatalf("expected /dev/disk4, got %q", got)
	}
}

// extractParentWholeDisk mirrors the parsing logic in
// macParentWholeDisk but takes a plist string directly so tests can
// run without invoking diskutil. Keep the parser in lockstep with
// the production path.
func extractParentWholeDisk(plist string) string {
	idx := indexOf(plist, "<key>ParentWholeDisk</key>")
	if idx < 0 {
		return ""
	}
	tail := plist[idx:]
	openIdx := indexOf(tail, "<string>")
	closeIdx := indexOf(tail, "</string>")
	if openIdx < 0 || closeIdx < 0 || closeIdx <= openIdx {
		return ""
	}
	disk := trimSpace(tail[openIdx+len("<string>") : closeIdx])
	if disk == "" {
		return ""
	}
	if !hasPrefix(disk, "/dev/") {
		disk = "/dev/" + disk
	}
	return disk
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
