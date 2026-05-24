package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestProbeSetupAPHits exercises the happy path: an SSH-ish listener
// answers on the probe IP plus a Bose-shaped /info on :8090 → probe
// returns a populated BoxInfo with kind=stock.
func TestProbeSetupAPHits(t *testing.T) {
	// Stand up a TCP listener on a random loopback port as the "ssh"
	// surrogate. We just need DialTimeout to succeed.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	sshAddr := ln.Addr().(*net.TCPAddr)

	// /info responder. Real Bose XML shape so the parser exercises
	// the regexes the same way it does against a stock speaker.
	infoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?><info deviceID="08DF1F0C9870"><name>Jens Portable</name><type>SoundTouch Portable</type></info>`))
	}))
	defer infoSrv.Close()

	host := "127.0.0.1"
	// We cannot run with fixed ports (CI may have them busy); the
	// helper hardcodes :22 and :8090. Probe through the unexported
	// helper using the real fetchBoseInfo wired to a separate
	// addr is awkward, so we sub-test only the SSH-dial portion
	// here against the random port and assert the BoxInfo shape.
	got, ok := probeSetupAPAtPorts(host, sshAddr.Port, infoSrv.Listener.Addr().(*net.TCPAddr).Port, 800*time.Millisecond, 1500*time.Millisecond)
	if !ok {
		t.Fatal("expected probe to succeed with both listeners alive")
	}
	if got.Host != host || got.Kind != "stock" {
		t.Errorf("BoxInfo shape wrong: %+v", got)
	}
	if !strings.Contains(got.FriendlyName, "Jens Portable") {
		t.Errorf("friendlyName not enriched from /info: got %q", got.FriendlyName)
	}
	if got.Model != "SoundTouch Portable" {
		t.Errorf("model not enriched from /info: got %q", got.Model)
	}
	if got.DeviceID != "08DF1F0C9870" {
		t.Errorf("deviceID not enriched from /info: got %q", got.DeviceID)
	}
}

// TestProbeSetupAPMissReturnsSilentFalse confirms the no-box case
// returns ok=false with no error log — that's the common case (user
// not joined to a setup AP) and must not look like a failure.
func TestProbeSetupAPMissReturnsSilentFalse(t *testing.T) {
	// 192.0.2.0/24 is RFC 5737 TEST-NET-1, guaranteed non-routable.
	// DialTimeout returns quickly with a connection error.
	_, ok := probeSetupAPAtPorts("192.0.2.1", 22, 8090, 250*time.Millisecond, 250*time.Millisecond)
	if ok {
		t.Error("expected probe to miss against TEST-NET-1")
	}
}
