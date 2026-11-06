package standbyclock

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// timedBox is a firmware with a clock: it turns its configured zone into local
// time with its own rules, which can be outdated for a zone (stale), and
// otherwise agree with the agent's.
type timedBox struct {
	*fakeBox
	utc   time.Time
	stale map[string]int // zone -> offset the firmware's old rules give
}

func (b *timedBox) GetClockTime(context.Context) (boxapi.ClockTime, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var off int
	switch tz := b.cfg.TimezoneInfo; {
	case tz == "" || tz == "NOT_SET":
		off, _ = strconv.Atoi(b.cfg.UserOffsetMinute)
	case b.stale[tz] != 0:
		off = b.stale[tz]
	default:
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return boxapi.ClockTime{}, err
		}
		_, s := b.utc.In(loc).Zone()
		off = s / 60
	}
	l := b.utc.Add(time.Duration(off) * time.Minute)
	ct := boxapi.ClockTime{UTCTime: b.utc.Unix()}
	ct.Local = &struct {
		Hour   int `xml:"hour,attr"`
		Minute int `xml:"minute,attr"`
		Second int `xml:"second,attr"`
	}{l.Hour(), l.Minute(), l.Second()}
	return ct, nil
}

// The firmware's rules for a zone are outdated: it shows -480 where the zone
// is -420. That is British Columbia since November 2026 (America/Vancouver on
// FW 27.0.6, measured 2026-11-06). Phoenix stands in for it here because its
// -420 has not changed since 1968, so the tests do not depend on how new the
// Go toolchain's zone data is.
var nov6 = time.Date(2026, 11, 6, 5, 25, 9, 0, time.UTC)

func staleBox() *timedBox {
	return &timedBox{
		fakeBox: &fakeBox{cfg: boxapi.ClockConfig{UserEnable: "true", TimezoneInfo: "Europe/Berlin", TimeFormat: format12, BrightnessLevel: "70"}},
		utc:     nov6,
		stale:   map[string]int{"America/Phoenix": -480},
	}
}

func TestApplyWritesAFixedOffsetWhenTheFirmwareRulesAreOutdated(t *testing.T) {
	box := staleBox()
	k := newTestKeeper(t, box)
	k.now = func() time.Time { return nov6 }
	res, err := k.Apply(context.Background(), Config{Enabled: true, Timezone: "America/Phoenix"})
	if err != nil {
		t.Fatal(err)
	}
	if res.FixedOffset == nil || *res.FixedOffset != -420 {
		t.Fatalf("fixed offset: %+v", res)
	}
	last := box.posts[len(box.posts)-1]
	if !strings.Contains(last, `timezoneInfo="NOT_SET"`) || !strings.Contains(last, `userOffsetMinute="-420"`) || !strings.Contains(last, `brightnessLevel="70"`) {
		t.Fatalf("stand-in write: %s", last)
	}
	if st, ok := Load(k.Path()); !ok || st.Timezone != "America/Phoenix" {
		t.Fatalf("the owner's zone must stay stored: %+v", st)
	}

	// The stand-in still shows the right time: a standby check writes nothing.
	n := len(box.posts)
	if _, err := k.check(context.Background(), "standby", false); err != nil || len(box.posts) != n {
		t.Fatalf("a holding stand-in must not be rewritten: %v", box.posts[n:])
	}
}

func TestCheckReplacesAStandInThatNoLongerHolds(t *testing.T) {
	box := staleBox()
	k := newTestKeeper(t, box)
	k.now = func() time.Time { return nov6 }
	if _, err := k.Apply(context.Background(), Config{Enabled: true, Timezone: "America/Phoenix"}); err != nil {
		t.Fatal(err)
	}
	box.cfg.UserOffsetMinute = "-480" // an offset that is no longer right
	if _, err := k.check(context.Background(), "wake", false); err != nil {
		t.Fatal(err)
	}
	if box.cfg.TimezoneInfo != "NOT_SET" || box.cfg.UserOffsetMinute != "-420" {
		t.Fatalf("not measured again: %+v", box.cfg)
	}
}

func TestCheckCorrectsAZoneTheFirmwareGetsWrongByName(t *testing.T) {
	box := staleBox()
	k := newTestKeeper(t, box)
	k.now = func() time.Time { return nov6 }
	if _, err := k.Apply(context.Background(), Config{Enabled: true, Timezone: "America/Phoenix"}); err != nil {
		t.Fatal(err)
	}
	// The Bose app (or a reset) puts the zone name back.
	box.cfg.TimezoneInfo, box.cfg.UserOffsetMinute = "America/Phoenix", "0"
	if _, err := k.check(context.Background(), "standby", false); err != nil {
		t.Fatal(err)
	}
	if box.cfg.TimezoneInfo != "NOT_SET" || box.cfg.UserOffsetMinute != "-420" {
		t.Fatalf("not corrected: %+v", box.cfg)
	}
}

func TestCorrectZoneLeavesARightZoneAlone(t *testing.T) {
	box := staleBox()
	k := newTestKeeper(t, box)
	k.now = func() time.Time { return nov6 }
	res, err := k.Apply(context.Background(), Config{Enabled: true, Timezone: "Europe/Berlin", Format24: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.FixedOffset != nil || len(box.posts) != 1 || box.cfg.TimezoneInfo != "Europe/Berlin" {
		t.Fatalf("a zone the firmware gets right must stay a zone: %+v posts=%v", res, box.posts)
	}
	// Switched off, nothing is measured or rewritten either.
	box2 := staleBox()
	k2 := newTestKeeper(t, box2)
	if _, err := k2.Apply(context.Background(), Config{Enabled: false, Timezone: "America/Phoenix"}); err != nil {
		t.Fatal(err)
	}
	if len(box2.posts) != 1 || box2.cfg.TimezoneInfo != "America/Phoenix" {
		t.Fatalf("an off clock must not be touched: %v", box2.posts)
	}
}

func TestAdoptedZoneIsCheckedToo(t *testing.T) {
	box := staleBox()
	box.cfg.TimezoneInfo = "America/Phoenix"
	k := newTestKeeper(t, box)
	k.now = func() time.Time { return nov6 }
	if _, err := k.check(context.Background(), "agent start", true); err != nil {
		t.Fatal(err)
	}
	if st, ok := Load(k.Path()); !ok || st.Timezone != "America/Phoenix" {
		t.Fatalf("adopted: %+v", st)
	}
	if box.cfg.TimezoneInfo != "NOT_SET" || box.cfg.UserOffsetMinute != "-420" {
		t.Fatalf("adopted zone not corrected: %+v", box.cfg)
	}
}
