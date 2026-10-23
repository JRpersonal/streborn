package webui

import (
	"strings"
	"testing"
)

// The phone remote's periodic polls must stand down while the page is in the
// background. A phone back on its home screen, or a tab behind another one,
// kept asking the speaker every 5 s for a page nobody was looking at, and a
// speaker with a few such phones around it answered them instead of the one in
// use. The visibilitychange handler refreshes everything on return, so a
// hidden tick has nothing to show anyway.
func TestPhoneRemotePollsOnlyWhileVisible(t *testing.T) {
	const gate = `if (document.visibilityState !== 'visible') return;`
	for _, tick := range []struct{ name, call string }{
		{"status", `refreshStatus(); refreshWedge(); }, 5000);`},
		{"zone", `loadZone(); }, 15000);`},
		{"presets", `loadPresets().catch(function(){}); }, 15000);`},
		{"peers", `loadPeers().catch(function(){}); }, 20000);`},
	} {
		at := strings.Index(indexHTML, tick.call)
		if at < 0 {
			t.Errorf("the %s poll is missing from the phone remote", tick.name)
			continue
		}
		line := indexHTML[strings.LastIndex(indexHTML[:at], "\n")+1 : at]
		if !strings.HasPrefix(line, "setInterval(function(){ ") || !strings.Contains(line, gate) {
			t.Errorf("the %s poll runs while the page is hidden: %s", tick.name, line+tick.call)
		}
	}
	if !strings.Contains(indexHTML, "if (document.visibilityState === 'visible') refreshAll();") {
		t.Error("the page no longer refreshes when it comes back to the foreground; the gated polls rely on that")
	}
}
