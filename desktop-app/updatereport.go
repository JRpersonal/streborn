package main

import (
	"errors"
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

	hist := a.otaHistoryTail(host, 25)

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
		fmt.Fprintf(&b, "speaker reports now: NOT REACHABLE (%v)\n\n", stripWrongBlame(err, errMsg, hist))
	}

	if hist != "" {
		fmt.Fprintf(&b, "what the update did\n-------------------\n%s\n", hist)
	}
	b.WriteString("\nPlease send this text to str@sichtbar-app.de, together with the\n")
	b.WriteString("diagnostic file if you were able to save one.\n")
	return b.String()
}

// stripWrongBlame rewrites the closing probe's advice when the rest of the
// report contradicts it.
//
// The probe that fills the "speaker reports now" line is a single request, and
// its advice is chosen from that request alone. When it times out, the user is
// told to go through their firewall and antivirus settings. That is the right
// advice for a speaker that never answers, and the wrong advice for the case
// this report was actually written for: field report 2026-08-07, where every
// line of the journal above it reads `status 400 ... body=""`. The speaker was
// answering all along, on the wrong port, and the closing probe simply happened
// to end in a timeout. The user chased a firewall for two days.
//
// So the whole attempt decides, not the last request: if anything in the error
// the update reported or in the journal shows the speaker returning an HTTP
// status, the firewall paragraph is replaced. A missing journal changes
// nothing, and a probe that already carries the right advice is left alone.
func stripWrongBlame(probeErr error, errMsg, history string) error {
	if probeErr == nil || !strings.Contains(probeErr.Error(), firewallAdvice) {
		return probeErr
	}
	if !answeredNotSTR(errors.New(errMsg + "\n" + history)) {
		return probeErr
	}
	return errors.New(strings.Replace(probeErr.Error(), firewallAdvice, answeredNotSTRAdvice, 1))
}
