package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// The line must say that the step ran and how it ended, and must not carry
// the network name, not even when the speaker's own answer repeats it.
func TestLogSetupWifiStepNeverWritesTheNetworkName(t *testing.T) {
	var buf bytes.Buffer
	a := &App{logger: slog.New(slog.NewTextHandler(&buf, nil))}

	a.LogSetupWifiStep("192.0.2.1", "ExampleNet", "failed", "HTTP 422: cannot see ExampleNet\nin its scan")
	line := buf.String()
	if strings.Contains(line, "ExampleNet") {
		t.Fatalf("network name in the app log: %s", line)
	}
	for _, want := range []string{"setup: wifi step", "outcome=failed", "wantTag=SSID#", "host=192.0.2.1", "in its scan"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line lacks %q: %s", want, line)
		}
	}
}

func TestSetupWifiStepDetailIsBounded(t *testing.T) {
	got := setupWifiStepDetail("ExampleNet", strings.Repeat("x", 5000))
	if n := len([]rune(got)); n != setupWifiDetailMax+3 {
		t.Errorf("detail is %d runes long", n)
	}
	if got := setupWifiStepDetail("", "status=unchanged mechanism=wpa"); got != "status=unchanged mechanism=wpa" {
		t.Errorf("plain detail was altered: %q", got)
	}
}

// A build without a logger must not panic on the call.
func TestLogSetupWifiStepWithoutLogger(t *testing.T) {
	(&App{}).LogSetupWifiStep("192.0.2.1", "ExampleNet", "changed", "")
}
