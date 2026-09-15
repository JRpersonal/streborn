package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The leak this pins, 2026-09-15. The phone remote's diagnostic button fetches
// /api/debug/state and saves the answer straight to a file, for reporters with
// no PC. The endpoint handed out the raw payload, on a comment in this very
// file that said "the app's exporter anonymizes bundles" -- true while the
// desktop app was the only caller, and never re-read when a second one arrived.
//
// 32 of the 36 files attached to public issues that way carried their owner's
// real LAN addresses and MAC addresses, across twelve threads.
//
// So the default is masked and the raw form is something a caller names. What
// these tests hold is the DEFAULT, because the default is the whole fix: the
// next consumer of this endpoint has not been written yet.

var (
	privateIP = regexp.MustCompile(`\b(?:10\.\d{1,3}|192\.168|172\.(?:1[6-9]|2\d|3[01]))\.\d{1,3}\.\d{1,3}\b`)
	macAddr   = regexp.MustCompile(`(?i)\b(?:[0-9A-F]{2}:){5}[0-9A-F]{2}\b`)
)

// leakyState is the shape a real speaker answers with: addresses in a peer
// list, a hardware address in the mDNS section, a speaker name, a network name
// and a user's home path in a log tail.
func leakyState() map[string]any {
	return map[string]any{
		"hosts_live": "192.168.178.1 fritz.box\n192.168.178.60 speaker",
		"mdns_host":  map[string]any{"iface": "wlan0", "mac": "7C:EC:79:F9:EC:A2"},
		"agent_log_tail": strings.Join([]string{
			`time=2026-09-15T10:00:00Z level=INFO msg="peer seen" ip=192.168.178.79 deviceID=EC24B8B790CC`,
			`time=2026-09-15T10:00:01Z level=INFO msg="wifi" ssid="Familie Mustermann 5G"`,
			`time=2026-09-15T10:00:02Z level=INFO msg="stick" path=C:\Users\alfred\Downloads\str`,
		}, "\n"),
		"zone": map[string]any{"master": "EC24B8B790CC", "name": "Wohnzimmer"},
	}
}

func maskTestServer() *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestTheDebugStateIsMaskedUnlessRawIsAskedFor(t *testing.T) {
	rec := httptest.NewRecorder()
	maskTestServer().writeDebugState(rec, leakyState(), false)

	body := rec.Body.String()
	if m := privateIP.FindString(body); m != "" {
		t.Errorf("a private address survived: %s", m)
	}
	if m := macAddr.FindString(body); m != "" {
		t.Errorf("a hardware address survived: %s", m)
	}
	for _, leak := range []string{"EC24B8B790CC", "Familie Mustermann", "Wohnzimmer", "alfred"} {
		if strings.Contains(body, leak) {
			t.Errorf("%q survived the mask", leak)
		}
	}
	// Still a usable document, or the phone saves something nobody can read.
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("the masked answer is not JSON any more: %v", err)
	}
	if len(out) != len(leakyState()) {
		t.Errorf("sections = %d, want %d: masking must not drop data", len(out), len(leakyState()))
	}
	// And still evidence: the parts that are not personal have to survive, or
	// the bundle stops being worth asking for.
	if !strings.Contains(body, "wlan0") || !strings.Contains(body, "peer seen") {
		t.Error("the masking took the diagnostic content with it")
	}
}

func TestRawIsStillAvailableWhenAskedFor(t *testing.T) {
	rec := httptest.NewRecorder()
	maskTestServer().writeDebugState(rec, leakyState(), true)
	body := rec.Body.String()
	// The desktop app needs this: its failure report shows the user their own
	// addresses on purpose, and its bundle runs a wider scrub of its own.
	if !strings.Contains(body, "192.168.178.1") {
		t.Error("raw dropped the addresses the desktop report exists to show")
	}
}

// The regression that matters most is not the masking itself, it is the
// default. A caller that knows nothing about this endpoint must get the safe
// answer, because that is the caller that leaked.
func TestAnUninformedCallerGetsTheMaskedForm(t *testing.T) {
	s := maskTestServer()
	for _, q := range []string{"", "?", "?raw=0", "?raw=true", "?anonymize=0", "?RAW=1"} {
		r := httptest.NewRequest("GET", "/api/debug/state"+q, nil)
		rec := httptest.NewRecorder()
		s.writeDebugState(rec, leakyState(), r.URL.Query().Get("raw") == "1")
		if privateIP.MatchString(rec.Body.String()) {
			t.Errorf("query %q handed out real addresses", q)
		}
	}
}

// The case the first version of this test missed, and it cost a release.
//
// leakyState above carries a network name in the `ssid="..."` form, inside a
// log line, which the scrub handles. A real speaker also has "ssid" as a JSON
// KEY with the name as a bare value next to it. The first implementation
// scrubbed the ENCODED document, and the SSID pattern ends in [^\s]*: minified
// JSON has no whitespace, so one such key swallowed the rest of the document
// and the answer stopped being JSON. Every synthetic test passed; all four
// speakers it was pointed at returned a broken payload.
//
// So this one is shaped like a speaker, not like a fixture.
func TestAStructuredNetworkNameDoesNotEatTheDocument(t *testing.T) {
	state := map[string]any{
		"wlan_configured": map[string]any{
			"tool": "wpa_cli",
			"networks": []any{
				map[string]any{"ssid": "Familie Mustermann 5G", "psk": "hunter2hunter2", "id": 0},
				map[string]any{"ssid": "Gaeste", "id": 1},
			},
		},
		"after":      "this section must survive",
		"disk_usage": map[string]any{"nvFreeBytes": 12107776},
	}

	rec := httptest.NewRecorder()
	maskTestServer().writeDebugState(rec, state, false)

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("the masked answer is not JSON: %v", err)
	}
	if len(out) != len(state) {
		t.Fatalf("sections = %d, want %d: the mask ate the document", len(out), len(state))
	}
	if out["after"] != "this section must survive" {
		t.Error("everything after the network name was lost")
	}
	body := rec.Body.String()
	for _, secret := range []string{"Familie Mustermann", "hunter2", "Gaeste"} {
		if strings.Contains(body, secret) {
			t.Errorf("%q survived", secret)
		}
	}
	// The speaker still has to be diagnosable afterwards.
	if !strings.Contains(body, "wpa_cli") || !strings.Contains(body, "12107776") {
		t.Error("the mask took the diagnostic content with it")
	}
}
