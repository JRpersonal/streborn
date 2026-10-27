package main

import (
	"strings"
	"testing"
)

// The desktop-side exits for the same leak: the app's own log (its DLNA
// discovery names the servers it finds) and the box agent log the bundle pulls
// over SSH or inside the debug state. A media server named after the owner's
// laptop must leave as a HOST# token with its product name kept.
func TestBundleMasksMediaServerHostNames(t *testing.T) {
	const agentLine = `time=2026-10-25T09:12:44Z level=INFO msg="library search: discovery missed a registered server, re-probed its last known address" server=MinimServer[Alexs-MacBook-Pro.local]`
	leaks := []string{"Alex", "MacBook", "Jöns"}
	check := func(where, got string) {
		t.Helper()
		for _, leak := range leaks {
			if strings.Contains(got, leak) {
				t.Errorf("%s: %q survived:\n%s", where, leak, got)
			}
		}
		if !strings.Contains(got, "MinimServer[HOST#") {
			t.Errorf("%s: the server product or its token is missing:\n%s", where, got)
		}
	}

	appLog := `time=2026-10-25T09:12:40Z level=INFO msg="dlna: discovery matched early" server="MinimServer[Alex’s Mac mini]" described=1` + "\n" +
		`time=2026-10-25T09:12:41Z level=INFO msg="dlna: described" location=http://Jöns-iMac.local:9790/desc.xml server=MinimServer[Jöns-iMac.local]`
	check("app log", string(sanitizeLog([]byte(appLog))))

	snap := anonymizeSnapshot(boxSnapshot{
		Host:        "192.0.2.10",
		SSHFallback: &sshFallback{AgentLogTail: agentLine, AgentLogNAND: agentLine},
		DebugState:  map[string]any{"agent_log_tail": agentLine},
	})
	check("ssh agent log tail", snap.SSHFallback.AgentLogTail)
	check("ssh agent log nand", snap.SSHFallback.AgentLogNAND)
	tail, _ := snap.DebugState["agent_log_tail"].(string)
	check("debug state agent log", tail)
}
