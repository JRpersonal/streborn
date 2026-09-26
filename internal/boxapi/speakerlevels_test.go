package boxapi

import (
	"context"
	"strings"
	"testing"
)

const (
	levelCapsWith = `<capabilities deviceID="AABBCCDDEEFF">
  <capability name="audioproducttonecontrols" url="/audioproducttonecontrols"/>
  <capability name="audioproductlevelcontrols" url="/audioproductlevelcontrols"/>
</capabilities>`
	levelCapsWithout = `<capabilities deviceID="AABBCCDDEEFF">
  <capability name="audioproducttonecontrols" url="/audioproducttonecontrols"/>
</capabilities>`
	levelsBoth = `<audioproductlevelcontrols>
  <frontCenterSpeakerLevel value="3" minValue="-10" maxValue="10" step="1"/>
  <rearSurroundSpeakersLevel value="-2" minValue="-10" maxValue="10" step="1"/>
</audioproductlevelcontrols>`
	levelsRearOnly = `<audioproductlevelcontrols>
  <rearSurroundSpeakersLevel value="0" minValue="-10" maxValue="10" step="1"/>
</audioproductlevelcontrols>`
	levelsCollapsed = `<audioproductlevelcontrols>
  <frontCenterSpeakerLevel value="0" minValue="0" maxValue="0" step="1"/>
  <rearSurroundSpeakersLevel value="0" minValue="0" maxValue="0" step="1"/>
</audioproductlevelcontrols>`
)

// newLevelBox is newRecordingBox plus the per-host capability cache cleanup
// that belongs to this route.
func newLevelBox(t *testing.T, host, caps, levels string) (*Client, *recordingBox) {
	t.Helper()
	c, rec := newRecordingBox(t, host, map[string]string{
		"/capabilities":              caps,
		"/audioproductlevelcontrols": levels,
	})
	t.Cleanup(func() { levelControlsHosts.Delete(host) })
	return c, rec
}

// A soundbar with surrounds reports both levels with their own ranges.
func TestGetSpeakerLevelsReadsBothLevels(t *testing.T) {
	c, _ := newLevelBox(t, "levels-both", levelCapsWith, levelsBoth)
	lv, err := c.GetSpeakerLevels(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !lv.Supported {
		t.Fatal("a box advertising the capability must report supported")
	}
	if lv.FrontCenter.Value != 3 || !lv.FrontCenter.Avail {
		t.Errorf("front centre = %+v", lv.FrontCenter)
	}
	if lv.RearSurrounds.Value != -2 || lv.RearSurrounds.Min != -10 || lv.RearSurrounds.Max != 10 {
		t.Errorf("rear surrounds = %+v", lv.RearSurrounds)
	}
}

// An ordinary speaker does not advertise the route. That is the common case
// and must be a quiet "no", never an error: the app hides the section.
func TestGetSpeakerLevelsUnsupportedIsNotAnError(t *testing.T) {
	c, rec := newLevelBox(t, "levels-none", levelCapsWithout, levelsBoth)
	lv, err := c.GetSpeakerLevels(context.Background())
	if err != nil {
		t.Fatalf("an unsupported box must not error: %v", err)
	}
	if lv.Supported {
		t.Fatal("capability absent, yet reported as supported")
	}
	// And the gated route is never touched: the doc's contract for these URLs
	// is to use them only when /capabilities lists them.
	for _, p := range rec.paths {
		if p == "/audioproductlevelcontrols" {
			t.Error("the gated route was called although the capability is absent")
		}
	}
}

// A bar with surrounds but no separately driven centre reports only the rear
// element. Showing a centre slider there would be a control that cannot move.
func TestGetSpeakerLevelsSkipsAMissingElement(t *testing.T) {
	c, _ := newLevelBox(t, "levels-rear", levelCapsWith, levelsRearOnly)
	lv, err := c.GetSpeakerLevels(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if lv.FrontCenter.Avail {
		t.Error("a missing element must not be reported as available")
	}
	if !lv.RearSurrounds.Avail {
		t.Error("the element that IS there must be available")
	}
	if !lv.Supported {
		t.Error("one usable level is still support")
	}
}

// Advertised, but with nothing that can actually move.
func TestGetSpeakerLevelsRejectsACollapsedRange(t *testing.T) {
	c, _ := newLevelBox(t, "levels-flat", levelCapsWith, levelsCollapsed)
	lv, err := c.GetSpeakerLevels(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if lv.Supported {
		t.Error("advertised but with nothing adjustable is not supported")
	}
}

// The contract that matters: a write carries ONLY the level it names. Sending
// both would push a value the caller had on screen over one the user changed
// meanwhile on the Bose remote.
func TestSetSpeakerLevelWritesOnlyTheNamedLevel(t *testing.T) {
	c, rec := newLevelBox(t, "levels-write", levelCapsWith, levelsBoth)
	if err := c.SetSpeakerLevel(context.Background(), LevelRearSurrounds, 4); err != nil {
		t.Fatalf("write: %v", err)
	}
	body := rec.bodies["/audioproductlevelcontrols"]
	if !strings.Contains(body, `rearSurroundSpeakersLevel value="4"`) {
		t.Errorf("the named level is not in the body: %s", body)
	}
	if strings.Contains(body, "frontCenterSpeakerLevel") {
		t.Errorf("the other level must not be written: %s", body)
	}
}

func TestSetSpeakerLevelRejectsAnUnknownName(t *testing.T) {
	c, rec := newLevelBox(t, "levels-bogus", levelCapsWith, levelsBoth)
	if err := c.SetSpeakerLevel(context.Background(), "subwoofer", 1); err == nil {
		t.Fatal("an unknown level name must be refused, not guessed at")
	}
	if len(rec.paths) != 0 {
		t.Errorf("nothing may go on the wire for an unknown level, got %v", rec.paths)
	}
}

// The capability verdict is cached per host, so the 30 s settings poll does not
// re-probe a box that does not have the route. A failed probe caches nothing,
// because the box web server answers errors while the firmware is still booting.
func TestSpeakerLevelCapabilityIsProbedOnce(t *testing.T) {
	c, rec := newLevelBox(t, "levels-cache", levelCapsWith, levelsBoth)
	for i := 0; i < 3; i++ {
		if _, err := c.GetSpeakerLevels(context.Background()); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	probes := 0
	for _, p := range rec.paths {
		if p == "/capabilities" {
			probes++
		}
	}
	if probes != 1 {
		t.Errorf("expected one capability probe across three reads, got %d", probes)
	}
}
