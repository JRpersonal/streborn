package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// The field case of 2026-10-21: the update's verify window ended "NOT
// CONFIRMED - box unreachable", and 21 minutes later the stick install
// started on the same speaker while its new agent was still coming up. The
// install must refuse that before it touches the speaker, and say why.
func TestInstallRefusedWhileAnUpdateIsStillFinishing(t *testing.T) {
	journal := useTempJournal(t)
	dead := deadPort(t)
	old := installAgentProbePort
	installAgentProbePort = dead
	t.Cleanup(func() { installAgentProbePort = old })
	pairPorts(t, dead, deadPort(t))

	a := fastTestApp()
	const host = "127.0.0.1"
	a.rememberOTAPort(host, 17008)
	// The update started 27 minutes ago; the verify window ended 21 minutes ago.
	a.otaVerifyMu.Lock()
	m := a.otaVerify[host]
	m.at = time.Now().Add(-27 * time.Minute)
	m.unconfirmedAt = time.Now().Add(-21 * time.Minute)
	m.verdict = "unreachable"
	a.otaVerify[host] = m
	a.otaVerifyMu.Unlock()

	res, err := a.InstallSTROnBox(host, "SoundTouch 20")
	if err != nil {
		t.Fatalf("a refusal must reach the frontend as a result, not an error: %v", err)
	}
	if res.OK || res.Code != installCodeUpdateFinishing || res.Step != "guard" {
		t.Fatalf("got ok=%v code=%q step=%q, want a refusal with code %q at step guard", res.OK, res.Code, res.Step, installCodeUpdateFinishing)
	}
	for _, want := range []string{"still finishing", "Nothing was changed on the speaker", "Refresh"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("message lacks %q:\n%s", want, res.Message)
		}
	}
	if strings.Contains(strings.ToLower(res.Message), "prepare a usb stick") {
		t.Errorf("the refusal must not send the owner to the stick:\n%s", res.Message)
	}
	j := readJournal(t, journal)
	if !strings.Contains(j, "install: REFUSED code="+installCodeUpdateFinishing) {
		t.Errorf("journal lacks the refusal line:\n%s", j)
	}
	if strings.Contains(j, "install: FAILED") {
		t.Errorf("a refusal was journalled as a failed attempt:\n%s", j)
	}
}

// The hold covers the speaker, not just the address: a router that re-leases
// on every reboot moves it, and the install must still see the update.
func TestOTAHoldFollowsTheSpeakerToANewAddress(t *testing.T) {
	a := fastTestApp()
	a.discCache = map[string]discEntry{
		"192.0.2.40": {box: BoxInfo{Host: "192.0.2.40", DeviceID: "device-id-here"}},
		"192.0.2.41": {box: BoxInfo{Host: "192.0.2.41", DeviceID: "device-id-here"}},
		"192.0.2.42": {box: BoxInfo{Host: "192.0.2.42", DeviceID: "other-device"}},
	}
	a.rememberOTAPort("192.0.2.40", 8888)
	a.noteOTAUnconfirmed("192.0.2.40", "unreachable")

	if _, _, ok := a.recentOTAHold("192.0.2.40"); !ok {
		t.Error("no hold at the address the update ran on")
	}
	if _, _, ok := a.recentOTAHold("192.0.2.41"); !ok {
		t.Error("no hold for the same speaker at its new address")
	}
	if _, _, ok := a.recentOTAHold("192.0.2.42"); ok {
		t.Error("another speaker was held by this speaker's update")
	}
	if _, _, ok := a.recentOTAHold("192.0.2.43"); ok {
		t.Error("an unrelated address was held")
	}
}

// The hold is bounded: an update long past its memo window no longer blocks
// the stick, which is the recovery path for a speaker an update really broke.
// The post-OTA pin holds on its own while it is live.
func TestOTAHoldExpiresAndThePinHolds(t *testing.T) {
	a := fastTestApp()
	a.rememberOTAPort("192.0.2.50", 8888)
	a.otaVerifyMu.Lock()
	m := a.otaVerify["192.0.2.50"]
	m.at = time.Now().Add(-2 * otaVerifyMemoTTL)
	m.unconfirmedAt = time.Now().Add(-otaVerifyMemoTTL - time.Minute)
	a.otaVerify["192.0.2.50"] = m
	a.otaVerifyMu.Unlock()
	if _, _, ok := a.recentOTAHold("192.0.2.50"); ok {
		t.Error("an expired update memo still holds the install")
	}

	a.notePostOTA("192.0.2.51")
	if _, left, ok := a.recentOTAHold("192.0.2.51"); !ok || left <= 0 || left > otaRebootGrace {
		t.Errorf("live post-OTA pin: ok=%v left=%v, want a hold of at most %v", ok, left, otaRebootGrace)
	}
	a.discMu.Lock()
	a.otaPinned["192.0.2.51"] = time.Now().Add(-otaRebootGrace - time.Second)
	a.discMu.Unlock()
	if _, _, ok := a.recentOTAHold("192.0.2.51"); ok {
		t.Error("an expired post-OTA pin still holds the install")
	}
}

// An STR agent that answers its version means STR is running: the install
// refuses before it changes anything, with no update memo needed (an app
// restarted since the update has none).
func TestInstallRefusedWhenTheAgentAnswers(t *testing.T) {
	journal := useTempJournal(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/version" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"version":"v1.0.18","build":"2026-10-21-0510"}`)
	}))
	t.Cleanup(srv.Close)
	agent := listenPort(t, srv)
	old := installAgentProbePort
	installAgentProbePort = agent
	t.Cleanup(func() { installAgentProbePort = old })
	pairPorts(t, agent, deadPort(t))

	a := fastTestApp()
	res, err := a.InstallSTROnBox("127.0.0.1", "SoundTouch 20")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.OK || res.Code != installCodeSTRRunning || res.Step != "guard" {
		t.Fatalf("got ok=%v code=%q step=%q, want a refusal with code %q", res.OK, res.Code, res.Step, installCodeSTRRunning)
	}
	if !strings.Contains(res.Message, "v1.0.18") || !strings.Contains(res.Message, "already running") {
		t.Errorf("message does not say STR is already running:\n%s", res.Message)
	}
	if !strings.Contains(readJournal(t, journal), "install: REFUSED code="+installCodeSTRRunning) {
		t.Errorf("journal lacks the refusal line:\n%s", readJournal(t, journal))
	}
}

// Only the agent counts. A port that answers something else (a stock 404, an
// empty body, JSON without a version) must not block a genuine first install.
func TestRunningAgentVersionIgnoresStrangers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"404", http.StatusNotFound, "not found"},
		{"empty", http.StatusOK, ""},
		{"no version", http.StatusOK, `{"build":"x"}`},
		{"not json", http.StatusOK, "<html></html>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			p := listenPort(t, srv)
			old := installAgentProbePort
			installAgentProbePort = p
			t.Cleanup(func() { installAgentProbePort = old })
			pairPorts(t, p, deadPort(t))
			if v, ok := fastTestApp().runningAgentVersion("127.0.0.1"); ok {
				t.Errorf("took %q as a running agent", v)
			}
		})
	}
}

// The guard must run before every step that touches the speaker, and the agent
// check again right before each of the two writers (install.sh from the
// stick, the network install when there is no stick). Source-level, because
// the steps need a real speaker.
func TestInstallGuardOrderInSource(t *testing.T) {
	src := readSource(t, "install_str.go")
	body := funcBody(t, src, "func (a *App) installSTROnBox(")
	guard := strings.Index(body, "a.installRefusedSTRRunning(r, \"start\") || a.installHeldByOTA(r)")
	if guard < 0 {
		t.Fatal("installSTROnBox no longer starts with the guard")
	}
	for _, later := range []string{"a.ClearUpdateIntent(", "a.installReadFirmware(r)", "a.installPreflight(r)", "a.installProbeStick(r)"} {
		if i := strings.Index(body, later); i < 0 || i < guard {
			t.Errorf("%s runs before the guard (or is gone)", later)
		}
	}
	again := strings.Index(body, "a.installRefusedSTRRunning(r, \"before install.sh\")")
	run := strings.Index(body, "a.installRunScript(r)")
	if again < 0 || run < 0 || again > run {
		t.Error("the agent is not asked again right before install.sh")
	}
	probe := funcBody(t, src, "func (a *App) installProbeStick(")
	g := strings.Index(probe, "a.installRefusedSTRRunning(r, \"before the network install\")")
	w := strings.Index(probe, "a.repairAndAwaitAgent(")
	if g < 0 || w < 0 || g > w {
		t.Error("the no-stick network install is not guarded by the agent check")
	}
}

func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
