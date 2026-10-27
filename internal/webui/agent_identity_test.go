package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAgentVersionReportsIdentity: /api/agent/version says which speaker
// answers, so the desktop app can tell it from another one that took its
// address while both restarted after an update.
func TestAgentVersionReportsIdentity(t *testing.T) {
	prev := agentIdentityFn
	t.Cleanup(func() { agentIdentityFn = prev })

	s := quietServer("127.0.0.1")
	get := func() map[string]string {
		w := httptest.NewRecorder()
		s.handleAgentVersion(w, httptest.NewRequest(http.MethodGet, "/api/agent/version", nil))
		var out map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	agentIdentityFn = nil
	if out := get(); out["deviceID"] != "" || out["boxDeviceID"] != "" {
		t.Fatalf("unwired identity must be absent, got deviceID=%q boxDeviceID=%q", out["deviceID"], out["boxDeviceID"])
	}

	SetAgentIdentityFn(func() (string, string) { return "AABBCCDDEEFF", "" })
	if out := get(); out["deviceID"] != "AABBCCDDEEFF" {
		t.Fatalf("deviceID = %q, want the announced id", out["deviceID"])
	} else if _, ok := out["boxDeviceID"]; ok {
		t.Fatal("boxDeviceID must be omitted while the firmware has not answered")
	}

	SetAgentIdentityFn(func() (string, string) { return "AABBCCDDEEFF", "112233445566" })
	if out := get(); out["deviceID"] != "AABBCCDDEEFF" || out["boxDeviceID"] != "112233445566" {
		t.Fatalf("deviceID=%q boxDeviceID=%q, want both ids", out["deviceID"], out["boxDeviceID"])
	}

	// The same id twice is one id: no second key that only repeats the first.
	SetAgentIdentityFn(func() (string, string) { return "AABBCCDDEEFF", "aabbccddeeff" })
	if _, ok := get()["boxDeviceID"]; ok {
		t.Fatal("boxDeviceID that equals deviceID must be omitted")
	}
}
