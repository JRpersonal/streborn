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

// The swap helper must wait for the agent PID with kill -9 escalation, ABORT
// without rebooting when the agent will not die (cp over a live binary fails
// ETXTBSY and leaves the old version in place), copy stage->dst, verify the
// CONTENT on flash (sha256sum, else cmp against the stage; size is only the
// last resort because consecutive releases are byte-equal in size), retry
// once, and only reboot after a passing verify. Guard the script's
// load-bearing pieces so a refactor cannot silently drop one.
func TestSwapHelperScript(t *testing.T) {
	script := swapHelperScript(4242, "/dev/shm/streborn-ota.stage",
		"/mnt/nv/streborn/bin/streborn-armv7l", 12345678, "abc123", "reboot")
	for _, want := range []string{
		"/proc/4242",
		"kill -9 4242",
		"swap aborted", // agent still alive -> abort marker, no reboot
		`cp "/dev/shm/streborn-ota.stage" "/mnt/nv/streborn/bin/streborn-armv7l"`,
		"drop_caches", // verify must read flash, not page cache
		"sha256sum",
		"abc123",
		`cmp -s "/dev/shm/streborn-ota.stage" "/mnt/nv/streborn/bin/streborn-armv7l"`,
		`wc -c < "/mnt/nv/streborn/bin/streborn-armv7l"`,
		"12345678",
		"killall -9 streborn-armv7l", // retry must clear a watchdog-respawned agent
		"swap failed",                // failed verify -> marker, no reboot
		swapFailMarker,
		`rm -f "/dev/shm/streborn-ota.stage"`,
		"reboot",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("helper script is missing %q:\n%s", want, script)
		}
	}
	// The two abort paths must exit before the reboot line.
	if strings.LastIndex(script, "exit 1") > strings.Index(script, "reboot") {
		t.Errorf("abort paths must come before the reboot:\n%s", script)
	}
	// BusyBox sh only: no bashisms.
	for _, forbidden := range []string{"[[", "function ", "$((i++))", "local "} {
		if strings.Contains(script, forbidden) {
			t.Errorf("helper script contains non-POSIX construct %q", forbidden)
		}
	}
}

// On a speaker that restarts only the agent after an update, the swap helper
// starts the new binary after a passing verify and never reboots; the abort
// paths still come first, so a failed swap leaves the restart to run.sh's
// watchdog as before.
func TestSwapHelperScriptRestartOnly(t *testing.T) {
	dst := "/mnt/nv/streborn/bin/streborn-armv7l"
	finish := execNewAgentLines(dst, []string{"--listen-webui", ":8888"})
	script := swapHelperScript(4242, "/dev/shm/streborn-ota.stage", dst, 12345678, "abc123", finish)
	if strings.Contains(script, "reboot") {
		t.Errorf("restart-only helper must not reboot:\n%s", script)
	}
	start := strings.Index(script, "exec '"+dst+"' '--listen-webui' ':8888'")
	if start < 0 {
		t.Fatalf("restart-only helper does not start the new agent:\n%s", script)
	}
	if strings.LastIndex(script, "exit 1") > start {
		t.Errorf("abort paths must come before the agent start:\n%s", script)
	}
}
