package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	keepSSID = "ExampleNet"
	keepPass = "correct-horse-battery"
)

// keepHarness is a Server whose Wi-Fi change runs off the box: every record
// lives in a temp dir, the chassis is a wpa speaker off the cable, the
// supplicant is associated to whatever `on` names, and the background switch
// is replaced by a recorder.
type keepHarness struct {
	s       *Server
	dir     string
	on      string
	asked   int
	applied chan string // the password each background switch was started with
}

func newKeepHarness(t *testing.T) *keepHarness {
	t.Helper()
	dir := t.TempDir()
	prevTarget, prevCreds, prevKnown, prevRetired, prevRoute :=
		wlanTargetPath, wlanCredsLivePath, wlanKnownPath, wlanCredsRetiredPath, procRoutePath
	wlanTargetPath = filepath.Join(dir, "wlan-target")
	wlanCredsLivePath = filepath.Join(dir, "wlan-creds")
	wlanKnownPath = filepath.Join(dir, "wlan-known.json")
	wlanCredsRetiredPath = filepath.Join(dir, "wlan-creds.superseded")
	// No routing table: the test host's own cable must not turn the request
	// into the cabled store-only mode.
	procRoutePath = filepath.Join(dir, "no-route-table")
	t.Cleanup(func() {
		wlanTargetPath, wlanCredsLivePath, wlanKnownPath, wlanCredsRetiredPath, procRoutePath =
			prevTarget, prevCreds, prevKnown, prevRetired, prevRoute
	})
	h := &keepHarness{dir: dir, on: keepSSID, applied: make(chan string, 4)}
	h.s = &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.s.wlanMechFn = func() (string, string) { return "wlan0", "wpa" }
	h.s.wlanAssociatedFn = func(_, ssid string) bool {
		h.asked++
		return h.on != "" && ssid == h.on
	}
	h.s.wlanApplyFn = func(_, _, _, password string, _, _ bool) { h.applied <- password }
	return h
}

func (h *keepHarness) put(t *testing.T, body string) map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/box/wlan", strings.NewReader(body))
	// Loopback passes the LAN gate; the documentation range 192.0.2.0/24 is
	// not a private range and would be refused.
	req.RemoteAddr = "127.0.0.1:40000"
	h.s.handleBoxWLAN(rr, req)
	if rr.Code != 200 {
		t.Fatalf("PUT answered %d %q", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("answer is not JSON: %v (%q)", err, rr.Body.String())
	}
	// The kept path finishes on a goroutine that holds wlanMu; wait it out so
	// the temp dir is quiet before the test reads it or goes away.
	h.s.wlanMu.Lock()
	h.s.wlanMu.Unlock() //nolint:staticcheck // empty critical section is the point: a barrier
	return out
}

// switched reports the password the background switch was started with, or
// "" when none was started.
func (h *keepHarness) switched() string {
	select {
	case p := <-h.applied:
		return p
	case <-time.After(150 * time.Millisecond):
		return ""
	}
}

func (h *keepHarness) arm(t *testing.T, tgt wlanTarget) {
	t.Helper()
	if err := writeWlanTarget(tgt); err != nil {
		t.Fatal(err)
	}
}

func keepBody(ssid, password string) string {
	b, _ := json.Marshal(map[string]any{"ssid": ssid, "password": password, "force": true})
	return string(b)
}

// The case the change exists for: same network, and the password is the one a
// confirmed switch recorded. Nothing may reach the live-switch code.
func TestSameNetworkWithProvenPasswordLeavesTheConnectionAlone(t *testing.T) {
	h := newKeepHarness(t)
	h.arm(t, wlanTarget{SSID: keepSSID, PSK: keepPass, Verify: "live", Gen: 3, BootsFailed: 2, LastVerdict: "stood-down:budget"})

	out := h.put(t, keepBody(keepSSID, keepPass))
	if out["unchanged"] != true || out["status"] != "unchanged" {
		t.Errorf("answer does not say that nothing had to change: %v", out)
	}
	if out["mechanism"] != "wpa" || out["wiredUplink"] != false {
		t.Errorf("answer lost the fields the app reads: %v", out)
	}
	if p := h.switched(); p != "" {
		t.Fatal("the live switch ran for a network the speaker is already on with a proven password")
	}

	// The records end where a confirmed switch leaves them.
	creds, err := os.ReadFile(wlanCredsLivePath)
	if err != nil || string(creds) != "SSID="+keepSSID+"\nPASS="+keepPass+"\n" {
		t.Errorf("wlan-creds = %q, %v", creds, err)
	}
	if _, err := os.Stat(wlanCredsLivePath + ".bak"); err == nil {
		t.Error("a rollback backup was left behind although nothing can be rolled back")
	}
	tgt, ok := readWlanTarget()
	if !ok || tgt.SSID != keepSSID || tgt.PSK != keepPass || tgt.Verify != "live" {
		t.Fatalf("intended-network record = %+v, %v", tgt, ok)
	}
	if tgt.Gen != 4 || tgt.BootsFailed != 0 || tgt.LastVerdict != "" {
		t.Errorf("record was not re-armed like after a confirmed switch: %+v", tgt)
	}
	if _, err := os.Stat(wlanKnownPath); err != nil {
		t.Errorf("the known-networks baseline was not retaken: %v", err)
	}
}

// Every way the password can fail to be proven keeps the old path, and the
// record a wrong password could poison is not touched by the handler.
func TestSameNetworkWithUnprovenPasswordStillSwitches(t *testing.T) {
	cases := []struct {
		name string
		tgt  *wlanTarget
		pass string
	}{
		{"first install, nothing recorded", nil, keepPass},
		{"another password than the confirmed one", &wlanTarget{SSID: keepSSID, PSK: keepPass, Verify: "live", Gen: 1}, "another-password"},
		{"record never verified", &wlanTarget{SSID: keepSSID, PSK: keepPass, Verify: "weak", Gen: 1}, keepPass},
		{"record without a verdict", &wlanTarget{SSID: keepSSID, PSK: keepPass, Gen: 1}, keepPass},
		{"record is for another network", &wlanTarget{SSID: "OtherNet", PSK: keepPass, Verify: "live", Gen: 1}, keepPass},
		{"record is for a hidden network", &wlanTarget{SSID: keepSSID, PSK: keepPass, Hidden: true, Verify: "live", Gen: 1}, keepPass},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newKeepHarness(t)
			if c.tgt != nil {
				h.arm(t, *c.tgt)
			}
			out := h.put(t, keepBody(keepSSID, c.pass))
			if out["unchanged"] == true || out["status"] != "switching" {
				t.Errorf("answer claims nothing changed: %v", out)
			}
			if p := h.switched(); p != c.pass {
				t.Fatalf("the live switch was started with %q, want %q", p, c.pass)
			}
			got, ok := readWlanTarget()
			if c.tgt == nil {
				if ok {
					t.Errorf("an unproven password armed the intended-network record: %+v", got)
				}
			} else if !ok || got != *c.tgt {
				t.Errorf("the intended-network record changed before anything was confirmed: %+v", got)
			}
		})
	}
}

// Request shapes the no-op is not defined for never even ask the supplicant.
func TestOtherRequestShapesKeepTheOldPath(t *testing.T) {
	proven := wlanTarget{SSID: keepSSID, PSK: keepPass, Verify: "live", Gen: 1}

	t.Run("another network than the one the speaker is on", func(t *testing.T) {
		h := newKeepHarness(t)
		h.on = "OtherNet"
		h.arm(t, proven)
		h.put(t, keepBody(keepSSID, keepPass))
		if h.switched() == "" {
			t.Fatal("no switch for a network the speaker is not on")
		}
	})
	t.Run("not associated at all", func(t *testing.T) {
		h := newKeepHarness(t)
		h.on = ""
		h.arm(t, proven)
		h.put(t, keepBody(keepSSID, keepPass))
		if h.switched() == "" {
			t.Fatal("no switch for a speaker that is not associated")
		}
	})
	t.Run("hidden flag", func(t *testing.T) {
		h := newKeepHarness(t)
		h.arm(t, proven)
		h.put(t, `{"ssid":"`+keepSSID+`","password":"`+keepPass+`","hidden":true}`)
		if h.switched() == "" || h.asked != 0 {
			t.Fatalf("a hidden request was answered as a no-op (association asked %d times)", h.asked)
		}
	})
	t.Run("forget the other networks", func(t *testing.T) {
		h := newKeepHarness(t)
		h.arm(t, proven)
		h.put(t, `{"ssid":"`+keepSSID+`","password":"`+keepPass+`","force":true,"forgetOthers":true}`)
		if h.switched() == "" || h.asked != 0 {
			t.Fatal("a forget-others request was answered as a no-op")
		}
	})
	t.Run("reboot-to-apply chassis", func(t *testing.T) {
		h := newKeepHarness(t)
		h.s.wlanMechFn = func() (string, string) { return "eth0", "bco" }
		h.arm(t, proven)
		out := h.put(t, keepBody(keepSSID, keepPass))
		if h.switched() == "" || out["status"] != "rebooting" || h.asked != 0 {
			t.Fatalf("BCO request left the old path: %v", out)
		}
	})
	t.Run("a switch is already in flight", func(t *testing.T) {
		h := newKeepHarness(t)
		h.arm(t, proven)
		h.s.wlanMu.Lock()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", "/api/box/wlan", strings.NewReader(keepBody(keepSSID, keepPass)))
		req.RemoteAddr = "127.0.0.1:40000"
		h.s.handleBoxWLAN(rr, req)
		h.s.wlanMu.Unlock()
		if rr.Code != 200 || strings.Contains(rr.Body.String(), `"unchanged":true`) || h.switched() == "" {
			t.Fatalf("request during a running switch did not queue on the old path: %d %q", rr.Code, rr.Body.String())
		}
	})
}

func TestWlanPasswordProven(t *testing.T) {
	live := wlanTarget{SSID: keepSSID, PSK: keepPass, Verify: "live"}
	for _, c := range []struct {
		name string
		tgt  wlanTarget
		has  bool
		pass string
		want string
	}{
		{"confirmed record, same password", live, true, keepPass, wlanKeepProven},
		{"no record", wlanTarget{}, false, keepPass, wlanKeepNoRecord},
		{"prefix of the password", live, true, keepPass[:len(keepPass)-1], wlanKeepOtherPassword},
		{"case differs", live, true, strings.ToUpper(keepPass), wlanKeepOtherPassword},
		{"weak record", wlanTarget{SSID: keepSSID, PSK: keepPass, Verify: "weak"}, true, keepPass, wlanKeepWeakRecord},
		{"other network", wlanTarget{SSID: "OtherNet", PSK: keepPass, Verify: "live"}, true, keepPass, wlanKeepOtherRecord},
		{"hidden record", wlanTarget{SSID: keepSSID, PSK: keepPass, Verify: "live", Hidden: true}, true, keepPass, wlanKeepHiddenRecord},
	} {
		ok, why := wlanPasswordProven(c.tgt, c.has, keepSSID, c.pass)
		if why != c.want || ok != (c.want == wlanKeepProven) {
			t.Errorf("%s: got %v %q, want %q", c.name, ok, why, c.want)
		}
	}
}

// A repeat must not wear the NAND: the creds file is only written when it
// does not already say the same thing.
func TestEnsureWlanCredsWritesOnlyWhenNeeded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "wlan-creds")
	if wrote, err := ensureWlanCredsAt(p, keepSSID, keepPass); err != nil || !wrote {
		t.Fatalf("first call: wrote=%v err=%v", wrote, err)
	}
	if err := os.WriteFile(p+".bak", []byte("SSID=Old\nPASS=old-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if wrote, err := ensureWlanCredsAt(p, keepSSID, keepPass); err != nil || wrote {
		t.Fatalf("second call: wrote=%v err=%v", wrote, err)
	}
	if _, err := os.Stat(p + ".bak"); err == nil {
		t.Error("stale rollback backup survived")
	}
	if wrote, _ := ensureWlanCredsAt(p, keepSSID, "another-password"); !wrote {
		t.Error("a changed password was not written")
	}
}

func TestStoreHoldsSSID(t *testing.T) {
	nets := []storedNet{{SSID: "OtherNet"}, {SSID: keepSSID}}
	if !storeHoldsSSID(nets, keepSSID) || storeHoldsSSID(nets, "examplenet") || storeHoldsSSID(nil, keepSSID) {
		t.Error("store lookup is wrong")
	}
}
