package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// Remove on a speaker that has no STR left is recognised in two places (before
// and after the stick-free unlock). Both must end the same way: a success that
// says so, and an app that no longer remembers the speaker as an STR one.
func TestAlreadyStockOutcome(t *testing.T) {
	isolateConfigDir(t)
	const host, id = "192.0.2.70", "DEV-STOCK-AGAIN"
	a := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.discCache = map[string]discEntry{
		host: {box: BoxInfo{Host: host, DeviceID: id, Kind: "str", Port: 8888}, seen: time.Now()},
	}
	a.strKnown = map[string]discEntry{id: a.discCache[host]}

	res := UninstallSTRResult{Step: "ssh-handshake"}
	a.finishAlreadyStock(host, &res, "test: already stock")

	if !res.OK || res.Step != "already-stock" {
		t.Errorf("step = %q ok = %v, want a successful already-stock", res.Step, res.OK)
	}
	if !strings.Contains(res.Message, "STR is already removed from this speaker") {
		t.Errorf("message = %q", res.Message)
	}
	a.discMu.Lock()
	defer a.discMu.Unlock()
	if _, known := a.strKnown[id]; known {
		t.Error("the speaker is still remembered as an STR speaker")
	}
	if e, ok := a.discCache[host]; ok && e.box.Kind == "str" {
		t.Errorf("the cached record still says STR: %+v", e.box)
	}
}
