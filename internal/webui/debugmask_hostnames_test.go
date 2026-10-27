package webui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// A media server's friendly name carries the host it runs on, and macOS names
// that host after its owner. The agent logs it when library search re-probes a
// server discovery missed, and a public bundle shipped it that way. The phone's
// diagnostic file is the box-side exit, so it is pinned here.
func TestTheDebugStateMasksMediaServerHostNames(t *testing.T) {
	state := map[string]any{
		"agent_log_tail": strings.Join([]string{
			`time=2026-10-25T09:12:44Z level=INFO msg="library search: discovery missed a registered server, re-probed its last known address" server=MinimServer[Alexs-MacBook-Pro.local]`,
			`time=2026-10-25T09:12:45Z level=INFO msg="media server: enabled as a native music source" id=uuid:x name="MinimServer[Alex’s Mac mini]"`,
			`time=2026-10-25T09:12:46Z level=INFO msg="dlna: described" location=http://Jöns-iMac.local:9790/desc.xml`,
		}, "\n"),
	}
	rec := httptest.NewRecorder()
	maskTestServer().writeDebugState(rec, state, false)
	body := rec.Body.String()
	for _, leak := range []string{"Alex", "MacBook", "Mac mini", "Jöns"} {
		if strings.Contains(body, leak) {
			t.Errorf("%q survived the mask:\n%s", leak, body)
		}
	}
	if !strings.Contains(body, "MinimServer[HOST#") || !strings.Contains(body, "discovery missed a registered server") {
		t.Errorf("the server product or the message went with the name:\n%s", body)
	}
}
