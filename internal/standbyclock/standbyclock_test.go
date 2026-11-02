package standbyclock

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// fakeBox is a firmware that keeps what it is sent, optionally ignoring or
// refusing the brightness.
type fakeBox struct {
	mu             sync.Mutex
	cfg            boxapi.ClockConfig
	readErr        error
	refuseBright   bool // 400 on a body with brightnessLevel
	ignoreBright   bool // accepts it but keeps its own level
	posts          []string
	reads          int
	noClockElement bool
}

func (f *fakeBox) GetClockConfig(context.Context) (boxapi.ClockConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.readErr != nil {
		return boxapi.ClockConfig{}, f.readErr
	}
	if f.noClockElement {
		return boxapi.ClockConfig{}, boxapi.ErrNoClockConfig
	}
	return f.cfg, nil
}

func (f *fakeBox) SetClockConfigXML(_ context.Context, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, body)
	if f.refuseBright && strings.Contains(body, "brightnessLevel") {
		return &boxapi.BoxError{Path: "/clockDisplay", Status: 400, Body: "CLIENT_XML_ERROR"}
	}
	f.cfg.UserEnable = attr(body, "userEnable")
	f.cfg.TimezoneInfo = attr(body, "timezoneInfo")
	f.cfg.UserOffsetMinute = attr(body, "userOffsetMinute")
	f.cfg.TimeFormat = attr(body, "timeFormat")
	if b := attr(body, "brightnessLevel"); b != "" && !f.ignoreBright {
		f.cfg.BrightnessLevel = b
	}
	return nil
}

func attr(body, name string) string {
	i := strings.Index(body, name+`="`)
	if i < 0 {
		return ""
	}
	rest := body[i+len(name)+2:]
	return rest[:strings.IndexByte(rest, '"')]
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestKeeper(t *testing.T, box Box) *Keeper {
	t.Helper()
	k := New(box, quiet(), Options{Path: filepath.Join(t.TempDir(), "standby-clock.json")})
	k.sync = func() {}
	k.sleep = func(ctx context.Context, d time.Duration) bool { return ctx.Err() == nil }
	return k
}

func ip(n int) *int { return &n }

func TestXMLWithZoneSendsNoOffset(t *testing.T) {
	c := Config{Enabled: true, Timezone: "Europe/Berlin", OffsetMinutes: 120, Format24: true, Brightness: ip(40)}
	got := c.XML(true)
	want := `<clockDisplay><clockConfig userEnable="true" timezoneInfo="Europe/Berlin" userOffsetMinute="0" timeFormat="TIME_FORMAT_24HOUR_ID" brightnessLevel="40" /></clockDisplay>`
	if got != want {
		t.Fatalf("XML:\n got %s\nwant %s", got, want)
	}
	if strings.Contains(c.XML(false), "brightness") {
		t.Fatal("XML(false) must leave the brightness out")
	}
	noZone := Config{Enabled: false, OffsetMinutes: -300, Brightness: ip(250)}
	if got := noZone.XML(true); !strings.Contains(got, `timezoneInfo="NOT_SET" userOffsetMinute="-300" timeFormat="TIME_FORMAT_12HOUR_ID" brightnessLevel="100"`) {
		t.Fatalf("no zone: %s", got)
	}
}

func TestFromFirmware(t *testing.T) {
	c, ok := FromFirmware(boxapi.ClockConfig{UserEnable: "true", TimezoneInfo: "Europe/Berlin", UserOffsetMinute: "0", TimeFormat: "TIME_FORMAT_12HOUR_ID", BrightnessLevel: "70"})
	if !ok || !c.Enabled || c.Timezone != "Europe/Berlin" || c.Format24 || c.Brightness == nil || *c.Brightness != 70 {
		t.Fatalf("parse: %+v ok=%v", c, ok)
	}
	if _, ok := FromFirmware(boxapi.ClockConfig{}); ok {
		t.Fatal("an empty userEnable must not parse")
	}
	c, _ = FromFirmware(boxapi.ClockConfig{UserEnable: "false", TimezoneInfo: "NOT_SET", UserOffsetMinute: "60"})
	if c.Timezone != "" || c.OffsetMinutes != 60 || c.Brightness != nil || !c.Format24 {
		t.Fatalf("NOT_SET parse: %+v", c)
	}
}

func TestDiffers(t *testing.T) {
	s := Config{Enabled: true, Timezone: "Europe/Berlin", Format24: true, Brightness: ip(30)}
	if d := Differs(s, Config{Enabled: true, Timezone: "Europe/Berlin", OffsetMinutes: 0, Format24: true, Brightness: ip(70)}, false); d != "" {
		t.Fatalf("brightness must not count before the speaker took one: %q", d)
	}
	if d := Differs(s, Config{Enabled: true, Timezone: "Europe/Berlin", Format24: true, Brightness: ip(70)}, true); d != "brightness" {
		t.Fatalf("got %q", d)
	}
	if d := Differs(s, Config{Enabled: false, Timezone: "Europe/Berlin", Format24: false}, true); d != "enabled,format" {
		t.Fatalf("got %q", d)
	}
	if d := Differs(Config{OffsetMinutes: 60}, Config{OffsetMinutes: 120, Timezone: "NOT_SET"}, false); d != "offset" {
		t.Fatalf("got %q", d)
	}
}

func TestApplyStoresAndReportsBrightness(t *testing.T) {
	box := &fakeBox{cfg: boxapi.ClockConfig{UserEnable: "false", TimezoneInfo: "NOT_SET", BrightnessLevel: "70"}}
	k := newTestKeeper(t, box)
	var heard []int
	k.onBrightness = func(l int) { heard = append(heard, l) }
	res, err := k.Apply(context.Background(), Config{Enabled: true, Timezone: "Europe/Berlin", Format24: true, Brightness: ip(25)})
	if err != nil {
		t.Fatal(err)
	}
	if res.BrightnessHonoured == nil || !*res.BrightnessHonoured {
		t.Fatalf("honoured: %+v", res)
	}
	st, ok := Load(k.Path())
	if !ok || !st.Enabled || st.Brightness == nil || *st.Brightness != 25 {
		t.Fatalf("stored: %+v ok=%v", st, ok)
	}
	if len(heard) != 1 || heard[0] != 25 {
		t.Fatalf("onBrightness: %v", heard)
	}
	// A later switch without a brightness keeps the stored level.
	if _, err := k.Apply(context.Background(), Config{Enabled: false, Timezone: "Europe/Berlin", Format24: true}); err != nil {
		t.Fatal(err)
	}
	if last := box.posts[len(box.posts)-1]; !strings.Contains(last, `brightnessLevel="25"`) || !strings.Contains(last, `userEnable="false"`) {
		t.Fatalf("second write: %s", last)
	}
}

func TestApplyWithoutBrightnessPassesTheSpeakersOwn(t *testing.T) {
	box := &fakeBox{cfg: boxapi.ClockConfig{UserEnable: "false", BrightnessLevel: "55"}}
	k := newTestKeeper(t, box)
	if _, err := k.Apply(context.Background(), Config{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(box.posts[0], `brightnessLevel="55"`) {
		t.Fatalf("write: %s", box.posts[0])
	}
}

func TestApplyFallsBackWithoutBrightnessOnRefusal(t *testing.T) {
	box := &fakeBox{cfg: boxapi.ClockConfig{UserEnable: "false"}, refuseBright: true}
	k := newTestKeeper(t, box)
	res, err := k.Apply(context.Background(), Config{Enabled: true, Brightness: ip(10)})
	if err != nil {
		t.Fatal(err)
	}
	if len(box.posts) != 2 || strings.Contains(box.posts[1], "brightness") {
		t.Fatalf("posts: %v", box.posts)
	}
	if box.cfg.UserEnable != "true" {
		t.Fatal("the on/off must survive a refused brightness")
	}
	if res.BrightnessHonoured == nil || *res.BrightnessHonoured {
		t.Fatalf("honoured must be false: %+v", res)
	}
}

func TestIgnoredBrightnessIsReportedAndNotFought(t *testing.T) {
	box := &fakeBox{cfg: boxapi.ClockConfig{UserEnable: "true", TimezoneInfo: "Europe/Berlin", TimeFormat: format24, BrightnessLevel: "70"}, ignoreBright: true}
	k := newTestKeeper(t, box)
	res, err := k.Apply(context.Background(), Config{Enabled: true, Timezone: "Europe/Berlin", Format24: true, Brightness: ip(20)})
	if err != nil || res.BrightnessHonoured == nil || *res.BrightnessHonoured {
		t.Fatalf("res %+v err %v", res, err)
	}
	n := len(box.posts)
	// An event must not write again and again for a level the speaker never takes.
	if _, err := k.check(context.Background(), "standby", false); err != nil {
		t.Fatal(err)
	}
	if len(box.posts) != n {
		t.Fatalf("wrote again for an ignored brightness: %v", box.posts[n:])
	}
}

func TestCheckPutsBackADroppedClock(t *testing.T) {
	box := &fakeBox{cfg: boxapi.ClockConfig{UserEnable: "true", TimezoneInfo: "Europe/Berlin", TimeFormat: format24, BrightnessLevel: "70"}}
	k := newTestKeeper(t, box)
	if _, err := k.Apply(context.Background(), Config{Enabled: true, Timezone: "Europe/Berlin", Format24: true}); err != nil {
		t.Fatal(err)
	}
	n := len(box.posts)
	if _, err := k.check(context.Background(), "standby", false); err != nil || len(box.posts) != n {
		t.Fatalf("a matching clock must not be written: %v", box.posts[n:])
	}
	box.cfg.UserEnable = "false" // the firmware drops it
	if _, err := k.check(context.Background(), "wake", false); err != nil {
		t.Fatal(err)
	}
	if len(box.posts) != n+1 || box.cfg.UserEnable != "true" {
		t.Fatalf("not put back: %+v", box.cfg)
	}
}

func TestStartAdoptsAnEnabledClockAndNotAnOffOne(t *testing.T) {
	box := &fakeBox{cfg: boxapi.ClockConfig{UserEnable: "false"}}
	k := newTestKeeper(t, box)
	k.Start(context.Background())
	if _, ok := Load(k.Path()); ok || len(box.posts) != 0 {
		t.Fatal("an off clock with nothing stored must be left alone")
	}
	box.cfg = boxapi.ClockConfig{UserEnable: "true", TimezoneInfo: "Europe/Berlin", TimeFormat: format12, BrightnessLevel: "70"}
	k.Start(context.Background())
	st, ok := Load(k.Path())
	if !ok || !st.Enabled || st.Format24 || st.Timezone != "Europe/Berlin" {
		t.Fatalf("adopted: %+v ok=%v", st, ok)
	}
	if len(box.posts) != 0 {
		t.Fatal("adopting needs no write")
	}
	// The next start re-applies it.
	k.Start(context.Background())
	if len(box.posts) != 1 {
		t.Fatalf("start must re-apply once: %v", box.posts)
	}
}

func TestStartWaitsForAPlausibleClockAndARead(t *testing.T) {
	box := &fakeBox{cfg: boxapi.ClockConfig{UserEnable: "true"}, readErr: io.ErrUnexpectedEOF}
	k := newTestKeeper(t, box)
	if err := Save(k.Path(), Config{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	k.plausible = func(time.Time) bool { attempts++; return attempts > 2 }
	k.Start(context.Background())
	if len(box.posts) != 0 {
		t.Fatal("no write without a readable clock")
	}
	if box.reads != len(k.startDelays)-2 {
		t.Fatalf("reads %d, want %d (two attempts waited for the clock)", box.reads, len(k.startDelays)-2)
	}
}

func TestEventIsRateLimited(t *testing.T) {
	box := &fakeBox{cfg: boxapi.ClockConfig{UserEnable: "false"}}
	k := newTestKeeper(t, box)
	now := time.Unix(1_800_000_000, 0)
	k.now = func() time.Time { return now }
	k.Event(context.Background(), "standby")
	k.Event(context.Background(), "wake")
	time.Sleep(50 * time.Millisecond)
	box.mu.Lock()
	reads := box.reads
	box.mu.Unlock()
	if reads != 1 {
		t.Fatalf("reads %d, want 1", reads)
	}
}
