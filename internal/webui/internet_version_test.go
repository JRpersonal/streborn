package webui

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JRpersonal/streborn/internal/internet"
)

// TestAgentVersionReportsInternet: /api/agent/version carries the passive
// internet verdict the desktop app turns into the "check the router" hint.
func TestAgentVersionReportsInternet(t *testing.T) {
	prev := internet.Default
	tr := internet.New(nil)
	internet.Default = tr
	t.Cleanup(func() { internet.Default = prev })

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

	if out := get(); out["internet"] != "unknown" || out["internetReason"] != "" {
		t.Fatalf("fresh agent: internet=%q reason=%q, want unknown", out["internet"], out["internetReason"])
	}
	servfail := &net.DNSError{Err: "server misbehaving", Name: "stream.example.org", IsTemporary: true}
	tr.Failure("streamproxy", internet.Classify(servfail, true), servfail)
	if out := get(); out["internet"] != "unreachable" || out["internetReason"] != "dns" {
		t.Fatalf("after SERVFAIL: internet=%q reason=%q, want unreachable/dns", out["internet"], out["internetReason"])
	}
	tr.Success("clocksync")
	if out := get(); out["internet"] != "ok" {
		t.Fatalf("after success: internet=%q, want ok", out["internet"])
	}
	if _, ok := get()["internetReason"]; ok {
		t.Fatal("internetReason must be omitted while ok")
	}
}
