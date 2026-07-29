package main

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

// UpdateFailureReport builds the copyable text a user sends in when an update
// could not put a speaker into the state it was supposed to reach.
//
// The point is that the user should never have to describe a failure they
// cannot see. Everything needed to place the fault is gathered at the moment it
// happens: which build the app is on, what the speaker actually reports now,
// how much room it has left, and the last thing the update journal recorded.
// Without this the maintainer's first reply is always the same request for a
// diagnostic export, and by then the speaker has usually been rebooted and the
// evidence is gone.
//
// Only data the user is already entitled to see about their own equipment, in
// plain text, so they can read it before deciding to send it.
func (a *App) UpdateFailureReport(host string, port int, phase, errMsg, targetVersion string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ST Reborn update report\n")
	fmt.Fprintf(&b, "=======================\n\n")
	fmt.Fprintf(&b, "when          : %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "app version   : %s (build %s)\n", appVersion, appBuild)
	fmt.Fprintf(&b, "app platform  : %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "speaker       : %s:%d\n", host, port)
	fmt.Fprintf(&b, "wanted version: %s\n", targetVersion)
	fmt.Fprintf(&b, "failed at     : %s\n", phase)
	fmt.Fprintf(&b, "error         : %s\n\n", strings.TrimSpace(errMsg))

	if ver, err := a.BoxAgentVersion(host, port); err == nil {
		fmt.Fprintf(&b, "speaker reports now\n-------------------\n")
		for _, k := range []string{
			"version", "build", "model", "friendlyName", "boxHealth",
			"goLibrespot", "goLibrespotDroppedForUpdate",
			"nandFreeBytes", "nandTotalBytes", "uptimeSec", "wlanCreds",
		} {
			if v, ok := ver[k]; ok && v != "" {
				fmt.Fprintf(&b, "%-27s %s\n", k+":", v)
			}
		}
		if fd := ver["foreignDirs"]; fd != "" {
			fmt.Fprintf(&b, "%-27s %s\n", "other software on speaker:", fd)
		}
		b.WriteString("\n")
	} else {
		fmt.Fprintf(&b, "speaker reports now: NOT REACHABLE (%v)\n\n", err)
	}

	if hist := a.otaHistoryTail(host, 25); hist != "" {
		fmt.Fprintf(&b, "what the update did\n-------------------\n%s\n", hist)
	}
	b.WriteString("\nPlease send this text to str@sichtbar-app.de, together with the\n")
	b.WriteString("diagnostic file if you were able to save one.\n")
	return b.String()
}
