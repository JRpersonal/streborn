// A Wi-Fi outage on the USER'S machine, and the three wrong things STR did
// about it.
//
// Eileen Wilson pulled her Mac's Wi-Fi in the middle of a speaker update on
// 2026-09-10 and mailed in the log. What it shows, on all three of her speakers
// at once and on SSH port 22 as well, is ENETUNREACH: the operating system
// saying there is no route to that network, so nothing was ever put on the
// wire. What the app did with that:
//
//  1. blamed her firewall, or her speakers being on a different Wi-Fi,
//  2. waited out the full two-minute "let the speaker settle" window and then
//     escalated to the SSH path, which on a BCO chassis reboots the speaker to
//     open :17000,
//  3. finished by telling her to unplug a speaker that was never at fault.
//
// None of the three is defensible on an error that proves the request never
// left the machine. These tests pin that.

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The exact strings the operating systems produce. The macOS one is copied out
// of Eileen's bundle; the Windows one is WSAENETUNREACH's own wording, which is
// the same fault phrased the other way round.
const (
	macENETUNREACH = `Get "http://192.0.2.239:8888/api/agent/version": dial tcp 192.0.2.239:8888: connect: network is unreachable (also tried :17008: network is unreachable)`
	winENETUNREACH = `dial tcp 192.0.2.10:8888: connectex: A socket operation was attempted to an unreachable network.`
	sshENETUNREACH = `ssh handshake failed: exit status 255 (ssh: connect to host 192.0.2.239 port 22: Network is unreachable)`
)

func TestNoNetworkHereRecognisesEveryPlatformsWording(t *testing.T) {
	for _, s := range []string{macENETUNREACH, winENETUNREACH, sshENETUNREACH} {
		if !noNetworkHere(errors.New(s)) {
			t.Fatalf("not recognised as a dead local network: %s", s)
		}
	}
	if noNetworkHere(nil) {
		t.Fatal("a nil error is not a network outage")
	}
}

// The neighbouring errors must NOT be swept in. Each has its own, quite
// different, correct explanation, and claiming "your computer has no network"
// for a speaker that is switched off would be the same mistake pointed the
// other way.
func TestNoNetworkHereLeavesTheNeighbouringFailuresAlone(t *testing.T) {
	for _, s := range []string{
		`dial tcp 192.0.2.10:8888: connect: no route to host`,   // EHOSTUNREACH: speaker off
		`dial tcp 192.0.2.10:8888: connect: connection refused`, // nothing listening
		`context deadline exceeded (Client.Timeout exceeded)`,   // slow or filtered
		`dial tcp: can't assign requested address`,              // EADDRNOTAVAIL, has its own advice
		`Get "http://192.0.2.10:8090/info": EOF`,                // dropped mid-answer
	} {
		if noNetworkHere(errors.New(s)) {
			t.Fatalf("wrongly claimed a local network outage for: %s", s)
		}
	}
}

// The advice paragraph. It must name the fact, and it must not send the user
// after a firewall or a power plug.
func TestTheAdviceForADeadLocalNetworkBlamesNeitherFirewallNorSpeaker(t *testing.T) {
	got := reachabilityHint(errors.New(macENETUNREACH)).Error()
	if !strings.Contains(got, noNetworkAdvice) {
		t.Fatalf("the no-network paragraph is missing:\n%s", got)
	}
	if strings.Contains(got, firewallAdvice) {
		t.Fatal("the firewall paragraph came along anyway")
	}
	if strings.Contains(got, "antivirus") {
		t.Fatalf("the advice still sends the user to their antivirus:\n%s", got)
	}
	// Not the word "unplug" (the paragraph says nothing needs to be unplugged,
	// which is the whole point) but the INSTRUCTION to do it, which is what the
	// old dead-speaker panel gave her.
	for _, wrong := range []string{"Unplug the speaker", "unplug the speaker", "unplug it"} {
		if strings.Contains(got, wrong) {
			t.Fatalf("the advice still tells the user to %q:\n%s", wrong, got)
		}
	}
	if !strings.Contains(got, "nothing needs to be unplugged") {
		t.Fatalf("the advice should say plainly that nothing needs unplugging:\n%s", got)
	}
	// The underlying error survives for the log and for errors.Is.
	if !strings.Contains(got, "network is unreachable") {
		t.Fatal("the underlying error was swallowed")
	}
}

// The classification order matters: EADDRNOTAVAIL and a refused connection each
// have their own paragraph and must keep it.
func TestTheOtherDiagnosesAreUnchanged(t *testing.T) {
	got := reachabilityHint(errors.New(`dial tcp: can't assign requested address`)).Error()
	if !strings.Contains(got, noLocalRouteAdvice) {
		t.Fatalf("EADDRNOTAVAIL lost its own advice:\n%s", got)
	}
	got = reachabilityHint(errors.New(`connection refused`)).Error()
	if !strings.Contains(got, firewallAdvice) {
		t.Fatalf("a refused connection should still get the firewall advice:\n%s", got)
	}
	if err := reachabilityHint(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("a cancelled context must pass through untouched")
	}
}

// The settle window exists so a speaker that is still booting is not dragged
// through the SSH path. A machine with no network is the opposite case: there
// is nothing to wait for, and the two minutes are spent for nothing.
func TestThePreflightDoesNotWaitOutTheWindowWithNoNetwork(t *testing.T) {
	a, _ := appWithJournal(t)
	perr := reachabilityHint(errors.New(macENETUNREACH))

	done := make(chan error, 1)
	go func() { done <- a.preflightSettleRetry("192.0.2.239", 8888, perr) }()

	select {
	case got := <-done:
		if got == nil {
			t.Fatal("the preflight reported success while the machine had no network")
		}
		if !noNetworkHere(got) {
			t.Fatalf("the reason was lost on the way out: %v", got)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("preflightSettleRetry sat out the %s settle window with no network to settle on",
			otaPreflightSettleWindow)
	}
}

// The journal has to say why nothing was attempted, or the next person reading
// a bundle sees an update that simply stopped.
func TestTheJournalRecordsWhyNothingWasSent(t *testing.T) {
	a, dir := appWithJournal(t)
	_ = a.preflightSettleRetry("192.0.2.239", 8888, reachabilityHint(errors.New(macENETUNREACH)))

	raw, err := os.ReadFile(filepath.Join(dir, "ota-history.log"))
	if err != nil {
		t.Fatalf("read the journal: %v", err)
	}
	hist := string(raw)
	if !strings.Contains(hist, "no network route") {
		t.Fatalf("the journal does not say the machine had no route:\n%s", hist)
	}
	if strings.Contains(hist, "waiting up to") {
		t.Fatalf("the journal claims it waited for the speaker to settle:\n%s", hist)
	}
}

// A sanity check on the shape of what a user ends up reading, so the paragraph
// cannot quietly turn back into a wall of Go error text.
func TestTheUserFacingParagraphReadsAsProse(t *testing.T) {
	if strings.Contains(noNetworkAdvice, "dial tcp") || strings.Contains(noNetworkAdvice, `Get "`) {
		t.Fatal("the advice paragraph carries raw error text")
	}
	if n := len(strings.Fields(noNetworkAdvice)); n > 70 {
		t.Fatalf("the advice is %d words; nobody reads that in a dialog", n)
	}
	for _, want := range []string{"never left", "not the speaker"} {
		if !strings.Contains(noNetworkAdvice, want) {
			t.Fatalf("the advice no longer says %q: %s", want, noNetworkAdvice)
		}
	}
}

// appWithJournal is an App whose OTA journal goes to a temp directory, so a
// test can read back what was recorded without touching the real one.
func appWithJournal(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	prev := otaJournalDir
	otaJournalDir = dir
	t.Cleanup(func() { otaJournalDir = prev })
	return &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, dir
}
