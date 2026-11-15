package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	copySSID = "ExampleNet"
	copyPass = "correct-horse-battery"
)

// copyHarness is a Server whose Wi-Fi records all live in a temp dir, whose
// speaker facts are whatever the test says, and whose every Wi-Fi switching
// seam fails the test when it is reached: the copy must never get near them.
type copyHarness struct {
	s     *Server
	dir   string
	facts wlanCopyFacts
}

func newCopyHarness(t *testing.T) *copyHarness {
	t.Helper()
	dir := t.TempDir()
	prevTarget, prevCreds, prevKnown, prevRetired, prevSnap :=
		wlanTargetPath, wlanCredsLivePath, wlanKnownPath, wlanCredsRetiredPath, wlanSetupSnapshotPath
	wlanTargetPath = filepath.Join(dir, "wlan-target")
	wlanCredsLivePath = filepath.Join(dir, "wlan-creds")
	wlanKnownPath = filepath.Join(dir, "wlan-known.json")
	wlanCredsRetiredPath = filepath.Join(dir, "wlan-creds.superseded")
	wlanSetupSnapshotPath = filepath.Join(dir, "wlan-setup-snapshot.json")
	t.Cleanup(func() {
		wlanTargetPath, wlanCredsLivePath, wlanKnownPath, wlanCredsRetiredPath, wlanSetupSnapshotPath =
			prevTarget, prevCreds, prevKnown, prevRetired, prevSnap
	})
	h := &copyHarness{dir: dir, facts: wlanCopyFacts{Mode: "bco"}}
	h.s = &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.s.wlanCopyFactsFn = func() wlanCopyFacts { return h.facts }
	h.s.wlanMechFn = func() (string, string) { t.Error("the copy asked for the Wi-Fi mechanism"); return "eth0", "bco" }
	h.s.wlanAssociatedFn = func(_, _ string) bool { t.Error("the copy asked the supplicant"); return false }
	h.s.wlanApplyFn = func(_, _, _, _ string, _, _ bool) { t.Error("the copy started a Wi-Fi switch") }
	return h
}

func (h *copyHarness) put(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/box/wlan/copy", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:40000"
	h.s.handleBoxWLANCopy(rr, req)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func copyBody(ssid, password string) string {
	b, _ := json.Marshal(map[string]any{"ssid": ssid, "password": password, "source": "pc-wifi"})
	return string(b)
}

func (h *copyHarness) files(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// The case it exists for: a coprocessor speaker without STR's copy. The copy
// is written in the exact format run.sh replays, the baseline is taken, and
// nothing else appears: no target (the boot guard's steering record), no
// backup, no apply marker, no switch.
func TestWifiCopyWritesTheCredsAndNothingElse(t *testing.T) {
	h := newCopyHarness(t)
	// The marker path is fixed (other tests on this host may have left one),
	// so the check is that the copy did not write it.
	markerBefore, markerErrBefore := os.Stat(wlanApplyMarkerPath)
	code, out := h.put(t, copyBody(copySSID, copyPass))
	if code != 200 || out["status"] != wlanCopyStored || out["stored"] != true {
		t.Fatalf("answer %d %v, want 200 stored-copy", code, out)
	}
	b, err := os.ReadFile(wlanCredsLivePath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "SSID=" + copySSID + "\nPASS=" + copyPass + "\n"; string(b) != want {
		t.Errorf("creds file = %q, want %q", b, want)
	}
	if got := strings.Join(h.files(t), ","); got != "wlan-creds,wlan-known.json" {
		t.Errorf("files after the copy: %s, want only wlan-creds and wlan-known.json", got)
	}
	k, ok := readWlanKnownAt(wlanKnownPath)
	if !ok || k.CredsTag != ssidTag(copySSID) {
		t.Errorf("baseline not keyed on the copy: %+v %v", k, ok)
	}
	markerAfter, markerErrAfter := os.Stat(wlanApplyMarkerPath)
	switch {
	case markerErrBefore != nil && markerErrAfter == nil:
		t.Error("the copy dropped the apply marker")
	case markerErrBefore == nil && markerErrAfter == nil && !markerAfter.ModTime().Equal(markerBefore.ModTime()):
		t.Error("the copy rewrote the apply marker")
	}
	if out["evidence"] != wlanCopyByNone {
		t.Errorf("evidence = %v, want %s (the speaker named nothing)", out["evidence"], wlanCopyByNone)
	}
}

// A copy that exists is never replaced, whatever the request says.
func TestWifiCopyNeverOverwritesAnExistingCopy(t *testing.T) {
	h := newCopyHarness(t)
	if err := writeWlanCredsFile(wlanCredsLivePath, "OtherNet", "other-password", false); err != nil {
		t.Fatal(err)
	}
	h.facts.HaveCreds = true
	code, out := h.put(t, copyBody(copySSID, copyPass))
	if code != 409 || out["code"] != wlanCopyRefusedHasCopy {
		t.Fatalf("answer %d %v, want 409 %s", code, out, wlanCopyRefusedHasCopy)
	}
	if got := readWlanCredsSSIDAt(wlanCredsLivePath); got != "OtherNet" {
		t.Errorf("the existing copy changed to %q", got)
	}
}

func TestWifiCopyValidatesTheRequest(t *testing.T) {
	cases := map[string]string{
		"empty ssid":         copyBody("  ", copyPass),
		"empty password":     copyBody(copySSID, ""),
		"short password":     copyBody(copySSID, "1234567"),
		"long password":      copyBody(copySSID, strings.Repeat("a", 65)),
		"long ssid":          copyBody(strings.Repeat("s", 33), copyPass),
		"newline in pass":    copyBody(copySSID, "abcdefgh\nHIDDEN=1"),
		"newline in ssid":    copyBody("Net\nPASS=x", copyPass),
		"carriage in pass":   copyBody(copySSID, "abcdefgh\r"),
		"nul in the ssid":    copyBody("Net\x00", copyPass),
		"not even json":      "{",
		"password is number": `{"ssid":"ExampleNet","password":12345678}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			h := newCopyHarness(t)
			if code, _ := h.put(t, body); code != 400 {
				t.Errorf("answered %d, want 400", code)
			}
			if len(h.files(t)) != 0 {
				t.Errorf("a refused request left files: %v", h.files(t))
			}
		})
	}
}

func TestWifiCopyOnlyFromTheLAN(t *testing.T) {
	h := newCopyHarness(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/box/wlan/copy", strings.NewReader(copyBody(copySSID, copyPass)))
	req.RemoteAddr = "8.8.8.8:40000"
	h.s.handleBoxWLANCopy(rr, req)
	if rr.Code != 403 {
		t.Errorf("a request from the internet answered %d, want 403", rr.Code)
	}
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/box/wlan/copy", strings.NewReader(copyBody(copySSID, copyPass)))
	req.RemoteAddr = "127.0.0.1:40000"
	h.s.handleBoxWLANCopy(rr, req)
	if rr.Code != 405 {
		t.Errorf("POST answered %d, want 405", rr.Code)
	}
	if len(h.files(t)) != 0 {
		t.Errorf("files written: %v", h.files(t))
	}
}

// A Wi-Fi change holding the records gets no copy written beside it, and the
// request does not wait for it.
func TestWifiCopyStepsAsideForARunningChange(t *testing.T) {
	h := newCopyHarness(t)
	h.s.wlanMu.Lock()
	code, out := h.put(t, copyBody(copySSID, copyPass))
	h.s.wlanMu.Unlock()
	if code != 409 || out["code"] != wlanCopyRefusedBusy {
		t.Fatalf("answer %d %v, want 409 busy", code, out)
	}
	if len(h.files(t)) != 0 {
		t.Errorf("files written while a change ran: %v", h.files(t))
	}
}

func TestWifiCopyVerdict(t *testing.T) {
	home := []storedNet{{SSID: copySSID}}
	cases := []struct {
		name     string
		f        wlanCopyFacts
		code     string
		evidence string
	}{
		{"scm bridge, nothing known", wlanCopyFacts{Mode: "bco"}, "", wlanCopyByNone},
		{"scm bridge, store has it", wlanCopyFacts{Mode: "bco", Stored: home}, "", wlanCopyByStore},
		{"scm bridge, live names it", wlanCopyFacts{Mode: "bco", Live: copySSID}, "", wlanCopyByLive},
		{"live names another network", wlanCopyFacts{Mode: "bco", Live: "Home-5G", Stored: home}, wlanCopyRefusedOtherNet, ""},
		{"store holds only another network", wlanCopyFacts{Mode: "bco", Stored: []storedNet{{SSID: "Home-5G"}}}, wlanCopyRefusedOtherNet, ""},
		{"store holds another network and this one", wlanCopyFacts{Mode: "bco", Stored: []storedNet{{SSID: "Home-5G"}, {SSID: copySSID}}}, "", wlanCopyByStore},
		{"open, setup AP and hotspots do not count", wlanCopyFacts{Mode: "bco", Stored: []storedNet{
			{SSID: "Cafe", Open: true}, {SSID: "Bose SoundTouch 1A2B"}, {SSID: "xfinitywifi"},
		}}, "", wlanCopyByNone},
		{"STR already has a copy", wlanCopyFacts{Mode: "bco", HaveCreds: true}, wlanCopyRefusedHasCopy, ""},
		{"copy retired by the owner", wlanCopyFacts{Mode: "bco", Retired: true}, wlanCopyRefusedRetired, ""},
		{"setup episode open", wlanCopyFacts{Mode: "bco", SetupOpen: true}, wlanCopyRefusedInSetup, ""},
		{"firmware says it stores no profile", wlanCopyFacts{Mode: "bco", NoFirmwareProfile: true, Stored: []storedNet{{SSID: copySSID}}}, wlanCopyRefusedNoProfile, ""},
		{"wpa chassis keeps its own", wlanCopyFacts{Mode: "wlan0"}, wlanCopyRefusedNotNeed, ""},
		{"unknown chassis stays quiet", wlanCopyFacts{Mode: ""}, wlanCopyRefusedNotNeed, ""},
		{"Portable with its own profile", wlanCopyFacts{Mode: "taigan-bco", FirmwareSSID: copySSID}, wlanCopyRefusedNotNeed, ""},
		{"Portable without a profile", wlanCopyFacts{Mode: "taigan-bco"}, "", wlanCopyByNone},
	}
	for _, c := range cases {
		code, ev := wlanCopyVerdict(c.f, copySSID)
		if code != c.code || ev != c.evidence {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, code, ev, c.code, c.evidence)
		}
	}
}

// Every refusal leaves the records exactly as they were.
func TestWifiCopyRefusalsWriteNothing(t *testing.T) {
	for _, f := range []wlanCopyFacts{
		{Mode: "wlan0"},
		{Mode: "bco", SetupOpen: true},
		{Mode: "bco", Live: "Home-5G"},
		{Mode: "bco", Retired: true},
		{Mode: "bco", NoFirmwareProfile: true},
	} {
		h := newCopyHarness(t)
		h.facts = f
		if code, _ := h.put(t, copyBody(copySSID, copyPass)); code != 409 {
			t.Errorf("%+v answered %d, want 409", f, code)
		}
		if len(h.files(t)) != 0 {
			t.Errorf("%+v left files: %v", f, h.files(t))
		}
	}
}
