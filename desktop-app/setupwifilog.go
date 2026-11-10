package main

import (
	"strings"

	"github.com/JRpersonal/streborn/anonymise"
)

// setupWifiDetailMax bounds the free-text tail of the log line: a failed step
// hands over whatever the speaker answered.
const setupWifiDetailMax = 200

// setupWifiStepDetail makes the frontend's detail safe to write: the network
// name is replaced by its tag wherever the speaker's answer repeats it, line
// breaks are flattened, and the text is cut to a bounded length.
func setupWifiStepDetail(ssid, detail string) string {
	d := strings.Join(strings.Fields(detail), " ")
	if ssid != "" {
		d = strings.ReplaceAll(d, ssid, anonymise.SSIDTag(ssid))
	}
	if r := []rune(d); len(r) > setupWifiDetailMax {
		d = string(r[:setupWifiDetailMax]) + "..."
	}
	return d
}

// LogSetupWifiStep records in str.log that the setup's Wi-Fi step ran against
// a speaker and how it ended: "changed" (the speaker switches or restarts to
// apply), "unchanged" (it already was on that network with that password and
// the agent left the connection alone), "stored" (cabled speaker) or "failed".
//
// The step can move the speaker to another address, and without this line a
// diagnostic bundle could not tell whether it ran at all. The network appears
// only as its SSID# tag, under a field name the bundle scrub leaves alone
// (not one starting with "ssid"), and the password is never handed over.
// Best-effort, never throws back into JS.
func (a *App) LogSetupWifiStep(host, ssid, outcome, detail string) {
	if a.logger == nil {
		return
	}
	a.logger.Info("setup: wifi step", "host", host, "outcome", outcome,
		"wantTag", anonymise.SSIDTag(ssid), "detail", setupWifiStepDetail(ssid, detail))
}
