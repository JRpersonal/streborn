package webui

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
)

// EROFS must be recognized both as a wrapped syscall errno and by message,
// because it usually arrives buried in fs.PathError -> fmt.Errorf chains.
func TestIsReadOnlyFSErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain EROFS", syscall.EROFS, true},
		{"wrapped EROFS", fmt.Errorf("write tmp: %w", syscall.EROFS), true},
		{"message only", errors.New("open /mnt/nv/x: read-only file system"), true},
		{"ENOSPC is not it", syscall.ENOSPC, false},
		{"unrelated", errors.New("connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReadOnlyFSErr(tc.err); got != tc.want {
				t.Errorf("isReadOnlyFSErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The swap helper must wait for the agent PID, copy stage->dst, verify by
// size and (when available) SHA256 with one retry, clean the stage, and
// reboot. Guard the script's load-bearing pieces so a refactor cannot
// silently drop one.
func TestSwapHelperScript(t *testing.T) {
	script := swapHelperScript(4242, "/dev/shm/streborn-ota.stage",
		"/mnt/nv/streborn/bin/streborn-armv7l", 12345678, "abc123")
	for _, want := range []string{
		"/proc/4242",
		`cp "/dev/shm/streborn-ota.stage" "/mnt/nv/streborn/bin/streborn-armv7l"`,
		`wc -c < "/mnt/nv/streborn/bin/streborn-armv7l"`,
		"12345678",
		"sha256sum",
		"abc123",
		`rm -f "/dev/shm/streborn-ota.stage"`,
		"reboot",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("helper script is missing %q:\n%s", want, script)
		}
	}
	// BusyBox sh only: no bashisms.
	for _, forbidden := range []string{"[[", "function ", "$((i++))"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("helper script contains non-POSIX construct %q", forbidden)
		}
	}
}
