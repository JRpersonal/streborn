package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	piLeftIP   = "192.0.2.21"
	piRightIP  = "192.0.2.22"
	piLeftID   = "AAAAAAAAAAAA"
	piRightID  = "BBBBBBBBBBBB"
	pairedBody = `<group id="1234"><name>Wohnzimmer</name><masterDeviceId>AAAAAAAAAAAA</masterDeviceId><roles>` +
		`<groupRole><deviceId>AAAAAAAAAAAA</deviceId><role>LEFT</role><ipAddress>192.0.2.21</ipAddress></groupRole>` +
		`<groupRole><deviceId>BBBBBBBBBBBB</deviceId><role>RIGHT</role><ipAddress>192.0.2.22</ipAddress></groupRole>` +
		`</roles><status>GROUP_OK</status></group>`
)

// fakeFirmware holds a /getGroup body per host and records /removeGroup calls.
type fakeFirmware struct {
	mu      sync.Mutex
	groups  map[string]string
	removed []string
	fail    map[string]bool
}

func (f *fakeFirmware) get(_ context.Context, host, path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail[host] {
		return nil, errors.New("timeout")
	}
	switch path {
	case "/getGroup":
		return []byte(f.groups[host]), nil
	case "/removeGroup":
		f.removed = append(f.removed, host)
		f.groups[host] = `<group />`
		return []byte(`<group><status>GROUP_PEER_REQUEST_FAILED</status></group>`), nil
	}
	return nil, errors.New("unexpected path " + path)
}

func withPreinstallSeams(t *testing.T, fw *fakeFirmware, strHosts map[string]bool) {
	t.Helper()
	oldGet, oldAgent, oldFw, oldPath, oldPost := firmwareGet, agentAnswers, firmwareAnswers, pendingPairsPath, postPairDoc
	dir := t.TempDir()
	firmwareGet = fw.get
	agentAnswers = func(_ *App, host string) bool { return strHosts[host] }
	firmwareAnswers = func(host string) bool { return !fw.fail[host] }
	pendingPairsPath = func() (string, error) { return filepath.Join(dir, "pending-stereo-pairs.json"), nil }
	postPairDoc = func(*App, string, string) error { return nil }
	t.Cleanup(func() {
		firmwareGet, agentAnswers, firmwareAnswers, pendingPairsPath, postPairDoc = oldGet, oldAgent, oldFw, oldPath, oldPost
	})
}

func TestCheckFindsThePairAndWritesItDown(t *testing.T) {
	fw := &fakeFirmware{groups: map[string]string{piLeftIP: pairedBody, piRightIP: pairedBody}}
	withPreinstallSeams(t, fw, nil)
	a := NewApp()

	c, err := a.CheckStereoBeforeInstall(piLeftIP)
	if err != nil || !c.Known || !c.Paired {
		t.Fatalf("pair not detected: %+v err=%v", c, err)
	}
	if c.PartnerIP != piRightIP || !c.PartnerOnline || c.PartnerHasSTR || c.Pair.Name != "Wohnzimmer" {
		t.Fatalf("partner facts wrong: %+v", c)
	}
	saved, _ := loadPendingPairs()
	if len(saved) != 1 || saved[0].Name != "Wohnzimmer" || len(saved[0].Members) != 2 {
		t.Fatalf("pair must be written down before the install: %+v", saved)
	}
}

func TestUnpairedOrUnreadableSpeakerInstallsAsUsual(t *testing.T) {
	fw := &fakeFirmware{groups: map[string]string{piLeftIP: `<group />`}, fail: map[string]bool{piRightIP: true}}
	withPreinstallSeams(t, fw, nil)
	a := NewApp()
	if c, _ := a.CheckStereoBeforeInstall(piLeftIP); !c.Known || c.Paired {
		t.Fatalf("unpaired speaker: %+v", c)
	}
	// /getGroup hangs on some chassis: unknown, never "paired".
	if c, _ := a.CheckStereoBeforeInstall(piRightIP); c.Known || c.Paired {
		t.Fatalf("unreadable speaker must be unknown: %+v", c)
	}
}

func TestDissolveClearsEveryHalfThatHoldsThePair(t *testing.T) {
	fw := &fakeFirmware{groups: map[string]string{piLeftIP: pairedBody, piRightIP: `<group />`}}
	withPreinstallSeams(t, fw, nil)
	res, _ := NewApp().DissolvePairBeforeInstall([]string{piLeftIP, piRightIP})
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("dissolve must succeed: %+v", res)
	}
	if len(fw.removed) != 1 || fw.removed[0] != piLeftIP {
		t.Fatalf("removeGroup must hit only the half holding the pair: %v", fw.removed)
	}
}

func TestRestoreWaitsUntilBothHalvesRunSTR(t *testing.T) {
	fw := &fakeFirmware{groups: map[string]string{piLeftIP: pairedBody, piRightIP: pairedBody}}
	withPreinstallSeams(t, fw, map[string]bool{piLeftIP: true})
	a := NewApp()
	_, _ = a.CheckStereoBeforeInstall(piLeftIP)
	res, _ := a.RestorePairAfterInstall(piLeftIP)
	if res["status"] != "waitingForPartner" || res["partnerIP"] != piRightIP {
		t.Fatalf("want waitingForPartner, got %+v", res)
	}
	if saved, _ := loadPendingPairs(); len(saved) != 1 {
		t.Fatal("the saved pair must stay until both halves run STR")
	}
}

func TestRestoreKeepsAPairBothHalvesStillHold(t *testing.T) {
	fw := &fakeFirmware{groups: map[string]string{piLeftIP: pairedBody, piRightIP: pairedBody}}
	withPreinstallSeams(t, fw, map[string]bool{piLeftIP: true, piRightIP: true})
	posted := 0
	postPairDoc = func(_ *App, _ string, doc string) error {
		posted++
		if !strings.Contains(doc, "<name>Wohnzimmer</name>") || !strings.Contains(doc, "<role>RIGHT</role>") {
			t.Errorf("pair document wrong: %s", doc)
		}
		return nil
	}
	a := NewApp()
	_, _ = a.CheckStereoBeforeInstall(piLeftIP)
	res, _ := a.RestorePairAfterInstall(piRightIP)
	if res["status"] != "kept" || posted != 2 {
		t.Fatalf("want kept with the document on both agents, got %+v posted=%d", res, posted)
	}
	if saved, _ := loadPendingPairs(); len(saved) != 0 {
		t.Fatal("a restored pair must be crossed off")
	}
}

func TestRestoreWithoutASavedPairDoesNothing(t *testing.T) {
	withPreinstallSeams(t, &fakeFirmware{groups: map[string]string{}}, nil)
	if res, _ := NewApp().RestorePairAfterInstall(piLeftIP); res["status"] != "none" {
		t.Fatalf("want none, got %+v", res)
	}
}

func TestCanonicalPairXMLEscapesAndNamesBothSides(t *testing.T) {
	doc := canonicalPairXML(SavedPair{ID: "x", Name: "Bad & Küche"},
		PairMember{DeviceID: piLeftID, IP: piLeftIP}, PairMember{DeviceID: piRightID, IP: piRightIP})
	if !strings.Contains(doc, "Bad &amp; Küche") || !strings.Contains(doc, "<masterDeviceId>"+piLeftID) ||
		!strings.Contains(doc, "<role>LEFT</role>") || !strings.Contains(doc, "<role>RIGHT</role>") {
		t.Fatalf("document wrong: %s", doc)
	}
}
