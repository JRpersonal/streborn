package webui

import (
	"os"
	"path/filepath"
	"testing"
)

// One box boot must run the guard once. bootReason comes from the uptime and
// calls anything under ten minutes a boot, while run.sh respawns the agent up
// to six times in the first two minutes, so the five-BOOT budget was really a
// five-process budget and one power cycle could spend all of it.
func TestTheGuardRunsOncePerBoxBoot(t *testing.T) {
	dir := t.TempDir()
	old := wlanGuardBootMarker
	wlanGuardBootMarker = filepath.Join(dir, "marker")
	t.Cleanup(func() { wlanGuardBootMarker = old })

	if !claimWlanGuardBootRun() {
		t.Fatal("the first run after a boot was refused")
	}
	for i := 0; i < 5; i++ {
		if claimWlanGuardBootRun() {
			t.Fatalf("respawn %d ran the guard again and spent another boot of the budget", i+1)
		}
	}
}

// tmpfs clears the marker on a real reboot, which is the whole distinction.
func TestARealRebootLetsItRunAgain(t *testing.T) {
	dir := t.TempDir()
	old := wlanGuardBootMarker
	wlanGuardBootMarker = filepath.Join(dir, "marker")
	t.Cleanup(func() { wlanGuardBootMarker = old })

	claimWlanGuardBootRun()
	_ = os.Remove(wlanGuardBootMarker) // what a reboot does to tmpfs
	if !claimWlanGuardBootRun() {
		t.Error("after a reboot the guard refused to run, so a speaker that landed wrong is never corrected")
	}
}

// Never running is worse than running twice, so an unwritable marker must not
// disable the guard.
func TestItFailsOpenWhenTheMarkerCannotBeWritten(t *testing.T) {
	old := wlanGuardBootMarker
	wlanGuardBootMarker = filepath.Join(t.TempDir(), "no-such-dir", "marker")
	t.Cleanup(func() { wlanGuardBootMarker = old })

	if !claimWlanGuardBootRun() {
		t.Error("a box that cannot write the marker lost its guard entirely")
	}
}

// The owner putting the speaker back, twice, is a decision. The record is
// cleared and the guard stops arguing.
func TestTwoOwnerOverridesClearTheIntent(t *testing.T) {
	dir := t.TempDir()
	old := wlanTargetPath
	wlanTargetPath = filepath.Join(dir, "wlan-target")
	t.Cleanup(func() { wlanTargetPath = old })

	if err := writeWlanTarget(wlanTarget{SSID: "Home", PSK: "x", LastVerdict: "moved-back"}); err != nil {
		t.Fatal(err)
	}

	tgt, ok := readWlanTarget()
	if !ok {
		t.Fatal("no record")
	}
	// First override: counted, record kept, because one could be a change of mind.
	tgt.Overridden++
	if tgt.Overridden >= maxOwnerOverrides {
		t.Fatal("a single override already gives up; one is not a decision")
	}
	if err := writeWlanTarget(tgt); err != nil {
		t.Fatal(err)
	}
	back, _ := readWlanTarget()
	if back.Overridden != 1 {
		t.Errorf("the override count did not survive the write: %d", back.Overridden)
	}

	// Second: the owner means it.
	back.Overridden++
	if back.Overridden < maxOwnerOverrides {
		t.Fatal("two overrides still do not reach the limit")
	}
	clearWlanTarget()
	if _, ok := readWlanTarget(); ok {
		t.Error("the intent survived the owner saying twice where the speaker belongs")
	}
}

// The limit has to leave room for one honest change of mind and must not sit so
// high that the owner keeps losing the argument.
func TestTheOverrideLimitIsSane(t *testing.T) {
	if maxOwnerOverrides < 2 {
		t.Errorf("maxOwnerOverrides=%d gives up on a single change of mind", maxOwnerOverrides)
	}
	if maxOwnerOverrides > 3 {
		t.Errorf("maxOwnerOverrides=%d makes the owner fight the speaker too long", maxOwnerOverrides)
	}
}
