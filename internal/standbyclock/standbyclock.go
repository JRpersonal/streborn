// Package standbyclock keeps the speaker's own standby clock the way its owner
// set it.
//
// The clock on a SoundTouch display is a Bose firmware feature (/clockDisplay
// on :8090), separate from the Linux system clock STR repairs. Until now STR
// only wrote it when the owner pressed the switch in the app or at install, and
// never again, so a clock the firmware dropped stayed gone until a restart
// (#979). The owner's choice now lives on the speaker's NAND
// (/mnt/nv/streborn/standby-clock.json), and the agent puts it back:
//
//   - once after every agent start, as soon as :8090 answers and the system
//     clock is plausible (a forced write, the "re-apply after every boot");
//   - when the speaker goes into standby or wakes up, one read of
//     /clockDisplay, and a write only when the firmware's state differs from
//     the stored choice. At most one such check every few minutes.
//
// No timer and no polling: the start-up check has a bounded list of retries,
// everything else rides on power events the agent already gets.
//
// The same file carries the clock's brightness (#1226). The firmware reports
// brightnessLevel 0 to 100 (default 70) next to the other clock settings;
// whether every model honours a written value is untested, so after each write
// the keeper reads the clock back once and remembers whether the speaker took
// it. A speaker that rejects the attribute outright (a 4xx) is written again
// without it, so the on/off and the format are never lost to it.
//
// The firmware turns the zone into local time with its own time zone data,
// which predates 2020 and is never updated. A zone whose rules changed since
// then shows the wrong hour: British Columbia's permanent UTC-7 from November
// 2026 still falls back to -8, and Yukon, Jordan and Kazakhstan are off the
// same way (all measured on FW 27.0.6, 2026-11-06). So after writing a zone
// the keeper reads the time the display shows (/clockTime) and compares it
// with the agent's own, current zone data. When they disagree it writes the
// correct offset as a fixed one (NOT_SET plus userOffsetMinute) and keeps the
// owner's zone in the stored choice, so the next check sees the stand-in as
// matching for as long as the offset is right, and re-measures when it is not.
package standbyclock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/atomicfile"
	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/tzrules"
)

// DefaultPath is the NAND file that holds the owner's choice.
const DefaultPath = "/mnt/nv/streborn/standby-clock.json"

// DefaultBrightness is the firmware's own brightnessLevel out of the box.
const DefaultBrightness = 70

// Time formats as the firmware names them.
const (
	format12 = "TIME_FORMAT_12HOUR_ID"
	format24 = "TIME_FORMAT_24HOUR_ID"
	// tzNotSet is the firmware's "no zone": raw clock plus userOffsetMinute.
	tzNotSet = "NOT_SET"
)

// Config is one standby clock setting, as stored and as read back.
type Config struct {
	Enabled bool `json:"enabled"`
	// Timezone is an IANA zone ("Europe/Berlin"); "" is the firmware's
	// NOT_SET, where OffsetMinutes (minutes east of UTC) shifts the clock.
	Timezone      string `json:"timezone,omitempty"`
	OffsetMinutes int    `json:"offsetMinutes,omitempty"`
	Format24      bool   `json:"format24"`
	// Brightness is 0 to 100; nil leaves the speaker's own value alone.
	Brightness *int `json:"brightness,omitempty"`
}

// normalized makes two configs that mean the same compare equal: with a
// zone the firmware derives the offset itself and a stored offset would
// double-shift the clock (live 2026-06-01), so it is always 0 then.
func (c Config) normalized() Config {
	c.Timezone = strings.TrimSpace(c.Timezone)
	if strings.EqualFold(c.Timezone, tzNotSet) {
		c.Timezone = ""
	}
	if c.Timezone != "" {
		c.OffsetMinutes = 0
	}
	if c.Brightness != nil {
		b := ClampBrightness(*c.Brightness)
		c.Brightness = &b
	}
	return c
}

// ClampBrightness keeps a level inside the firmware's 0 to 100.
func ClampBrightness(b int) int {
	return min(max(b, 0), 100)
}

// XML is the POST body for /clockDisplay. withBrightness false leaves the
// brightnessLevel attribute out even when one is set.
func (c Config) XML(withBrightness bool) string {
	c = c.normalized()
	tz := c.Timezone
	if tz == "" {
		tz = tzNotSet
	}
	tf := format12
	if c.Format24 {
		tf = format24
	}
	bright := ""
	if withBrightness && c.Brightness != nil {
		bright = fmt.Sprintf(` brightnessLevel="%d"`, *c.Brightness)
	}
	return fmt.Sprintf(`<clockDisplay><clockConfig userEnable="%t" timezoneInfo="%s" userOffsetMinute="%d" timeFormat="%s"%s /></clockDisplay>`,
		c.Enabled, xmlAttr(tz), c.OffsetMinutes, tf, bright)
}

func xmlAttr(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// FromFirmware turns what /clockDisplay reported into a Config. ok is false
// when the answer has no usable userEnable.
func FromFirmware(fc boxapi.ClockConfig) (Config, bool) {
	var c Config
	switch strings.ToLower(strings.TrimSpace(fc.UserEnable)) {
	case "true":
		c.Enabled = true
	case "false":
	default:
		return Config{}, false
	}
	c.Timezone = strings.TrimSpace(fc.TimezoneInfo)
	if n, err := strconv.Atoi(strings.TrimSpace(fc.UserOffsetMinute)); err == nil {
		c.OffsetMinutes = n
	}
	// The firmware's out-of-box value is 24 hours on most units; only an
	// explicit 12-hour id counts as 12.
	c.Format24 = !strings.Contains(fc.TimeFormat, "12HOUR")
	if n, err := strconv.Atoi(strings.TrimSpace(fc.BrightnessLevel)); err == nil {
		c.Brightness = &n
	}
	return c.normalized(), true
}

// Differs names what the firmware has different from the stored choice, ""
// when nothing. Brightness only counts when withBrightness is set (the
// speaker took a written brightness before) and both sides have one.
func Differs(stored, fw Config, withBrightness bool) string {
	s, f := stored.normalized(), fw.normalized()
	var diff []string
	if s.Enabled != f.Enabled {
		diff = append(diff, "enabled")
	}
	if s.Format24 != f.Format24 {
		diff = append(diff, "format")
	}
	if s.Timezone != f.Timezone {
		diff = append(diff, "timezone")
	} else if s.Timezone == "" && s.OffsetMinutes != f.OffsetMinutes {
		diff = append(diff, "offset")
	}
	if withBrightness && s.Brightness != nil && f.Brightness != nil && *s.Brightness != *f.Brightness {
		diff = append(diff, "brightness")
	}
	return strings.Join(diff, ",")
}

// Load reads the stored choice. ok is false when there is none (or it is
// unreadable, which is treated the same: nothing to put back).
func Load(path string) (Config, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, false
	}
	var c Config
	if json.Unmarshal(b, &c) != nil {
		return Config{}, false
	}
	return c.normalized(), true
}

// Save writes the choice atomically and durably (see atomicfile): the speaker
// cuts power at standby, and a rename without an fsync can come back empty.
func Save(path string, c Config) error {
	b, err := json.Marshal(c.normalized())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return atomicfile.WriteFile(path, append(b, '\n'), 0o644)
}

// ErrStore wraps a failure to store a choice the speaker already took.
var ErrStore = errors.New("store the clock setting")

// Box is the part of the firmware API the keeper needs.
type Box interface {
	GetClockConfig(ctx context.Context) (boxapi.ClockConfig, error)
	SetClockConfigXML(ctx context.Context, body string) error
}

// clockTimer is the optional part of Box that reads the time the display
// shows. A Box without it gets no zone check.
type clockTimer interface {
	GetClockTime(ctx context.Context) (boxapi.ClockTime, error)
}

// zoneOffsetMinutes is the zone's offset from UTC at t, east positive, from
// the zone data STR ships (internal/tzrules), never the speaker's own files,
// which are the outdated rules being checked. ok is false for a zone it does
// not know.
func zoneOffsetMinutes(zone string, t time.Time) (int, bool) {
	loc, err := tzrules.Load(zone)
	if err != nil {
		return 0, false
	}
	_, off := t.In(loc).Zone()
	return off / 60, true
}

// fixedOffsetHolds reports whether fw is the fixed-offset stand-in for the
// stored zone and still shows that zone's correct time at now.
func fixedOffsetHolds(stored, fw Config, now time.Time) bool {
	if stored.Timezone == "" || fw.Timezone != "" {
		return false
	}
	want, ok := zoneOffsetMinutes(stored.Timezone, now)
	return ok && fw.OffsetMinutes == want
}

// Result is what a write left on the speaker.
type Result struct {
	Stored   Config `json:"stored"`
	Firmware Config `json:"firmware"`
	// FirmwareRead is false when the read-back failed; Firmware is then empty.
	FirmwareRead bool `json:"firmwareRead"`
	// FixedOffset is set when the firmware's own rules for the zone were
	// outdated and the clock got this fixed offset (minutes east of UTC)
	// instead.
	FixedOffset *int `json:"fixedOffset,omitempty"`
	// BrightnessHonoured is nil when nothing was said about the brightness
	// (none set, or no read-back), else whether the speaker reports the
	// written level.
	BrightnessHonoured *bool `json:"brightnessHonoured,omitempty"`
}

// Keeper holds the stored choice and puts it back. The zero value is not
// usable; use New.
type Keeper struct {
	path   string
	box    Box
	logger *slog.Logger
	// onBrightness hears every known brightness (stored or written), so STR's
	// own panel drawing can follow it. May be nil.
	onBrightness func(level int)

	// minGap spaces out the event-driven checks.
	minGap time.Duration
	now    func() time.Time
	// plausible reports whether the system clock can be trusted yet.
	plausible func(time.Time) bool
	// startDelays is the start-up schedule: one attempt after each.
	startDelays []time.Duration
	sleep       func(ctx context.Context, d time.Duration) bool
	sync        func()

	mu        sync.Mutex
	lastCheck time.Time
	// brightnessTaken: the last write's read-back showed the written level,
	// so a different level later means the firmware dropped it.
	brightnessTaken bool
}

// Options are the seams New takes; zero values pick the defaults.
type Options struct {
	Path         string
	OnBrightness func(level int)
	Plausible    func(time.Time) bool
}

// New wires a keeper to a speaker's firmware API.
func New(box Box, logger *slog.Logger, o Options) *Keeper {
	if logger == nil {
		logger = slog.Default()
	}
	path := o.Path
	if path == "" {
		path = DefaultPath
	}
	plausible := o.Plausible
	if plausible == nil {
		plausible = func(time.Time) bool { return true }
	}
	return &Keeper{
		path:         path,
		box:          box,
		logger:       logger,
		onBrightness: o.OnBrightness,
		minGap:       3 * time.Minute,
		now:          time.Now,
		plausible:    plausible,
		// About 20 minutes in total, six attempts at most.
		startDelays: []time.Duration{45 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 8 * time.Minute},
		sleep: func(ctx context.Context, d time.Duration) bool {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-t.C:
				return true
			}
		},
		sync: func() { _ = exec.Command("sync").Run() },
	}
}

// Path is where the choice is stored.
func (k *Keeper) Path() string { return k.path }

// Stored returns the stored choice.
func (k *Keeper) Stored() (Config, bool) { return Load(k.path) }

// Firmware reads the speaker's current clock setting.
func (k *Keeper) Firmware(ctx context.Context) (Config, error) {
	fc, err := k.box.GetClockConfig(ctx)
	if err != nil {
		return Config{}, err
	}
	c, ok := FromFirmware(fc)
	if !ok {
		return Config{}, boxapi.ErrNoClockConfig
	}
	return c, nil
}

// Apply is the owner's choice from the app or the phone page: write it to the
// speaker, then store it. A choice without a brightness keeps the stored one,
// or else the speaker's own.
func (k *Keeper) Apply(ctx context.Context, c Config) (Result, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	c = c.normalized()
	if c.Brightness == nil {
		if prev, ok := Load(k.path); ok && prev.Brightness != nil {
			c.Brightness = prev.Brightness
		}
	}
	res, err := k.writeLocked(ctx, c, "app")
	if err != nil {
		return res, err
	}
	if err := Save(k.path, res.Stored); err != nil {
		return res, fmt.Errorf("%w: %w", ErrStore, err)
	}
	k.sync()
	k.logger.Info("standby clock set", "enabled", c.Enabled, "timezone", c.Timezone, "format24", c.Format24,
		"brightness", brightnessAttr(res.Stored.Brightness), "brightnessHonoured", boolAttr(res.BrightnessHonoured))
	return res, nil
}

// writeLocked writes c (filling an unset brightness from the speaker's own
// value), falls back to a write without brightness when the speaker refuses
// it, and reads the result back once. k.mu is held.
func (k *Keeper) writeLocked(ctx context.Context, c Config, why string) (Result, error) {
	if c.Brightness == nil {
		// Send the speaker's own level along: whether the firmware resets an
		// omitted brightnessLevel to its default is untested, and a clock
		// switched on must not come back brighter than it was.
		if fw, err := k.Firmware(ctx); err == nil && fw.Brightness != nil {
			b := *fw.Brightness
			c.Brightness = &b
		}
	}
	withBrightness := c.Brightness != nil
	err := k.box.SetClockConfigXML(ctx, c.XML(withBrightness))
	var be *boxapi.BoxError
	if err != nil && withBrightness && errors.As(err, &be) && be.Status >= 400 && be.Status < 500 {
		k.logger.Warn("standby clock: the speaker refused the write with a brightness, writing it again without",
			"why", why, "status", be.Status, "err", err)
		withBrightness = false
		err = k.box.SetClockConfigXML(ctx, c.XML(false))
	}
	if err != nil {
		return Result{Stored: c}, err
	}
	res := Result{Stored: c}
	if fw, rerr := k.Firmware(ctx); rerr == nil {
		res.Firmware, res.FirmwareRead = fw, true
		if c.Brightness != nil {
			took := withBrightness && fw.Brightness != nil && *fw.Brightness == *c.Brightness
			res.BrightnessHonoured = &took
			k.brightnessTaken = took
		}
	}
	if fixed, ok := k.correctZoneLocked(ctx, c, withBrightness, why); ok {
		off := fixed.OffsetMinutes
		res.FixedOffset = &off
		if fw, rerr := k.Firmware(ctx); rerr == nil {
			res.Firmware, res.FirmwareRead = fw, true
		}
	}
	if c.Brightness != nil && k.onBrightness != nil {
		k.onBrightness(*c.Brightness)
	}
	return res, nil
}

// correctZoneLocked measures the offset the display shows for c's zone and,
// when the firmware's own rules for it are outdated, writes the correct offset
// as a fixed one. ok is true when it wrote. A switched-off clock, a config
// without a zone, a zone the agent does not know and an unreadable /clockTime
// are all left alone. k.mu is held.
func (k *Keeper) correctZoneLocked(ctx context.Context, c Config, withBrightness bool, why string) (Config, bool) {
	ct, isTimer := k.box.(clockTimer)
	if !isTimer || !c.Enabled || c.Timezone == "" {
		return Config{}, false
	}
	// The firmware applies a write at once on FW 27.0.6, but a read that beat
	// it would measure the previous setting and keep a wrong hour.
	if !k.sleep(ctx, time.Second) {
		return Config{}, false
	}
	t, err := ct.GetClockTime(ctx)
	if err != nil {
		k.logger.Debug("standby clock: could not read the time the speaker shows", "why", why, "err", err)
		return Config{}, false
	}
	got, ok := t.OffsetMinutes()
	if !ok {
		return Config{}, false
	}
	want, ok := zoneOffsetMinutes(c.Timezone, time.Unix(t.UTCTime, 0))
	if !ok || got == want {
		return Config{}, false
	}
	fixed := c
	fixed.Timezone, fixed.OffsetMinutes = "", want
	err = k.box.SetClockConfigXML(ctx, fixed.XML(withBrightness))
	var be *boxapi.BoxError
	if err != nil && withBrightness && errors.As(err, &be) && be.Status >= 400 && be.Status < 500 {
		err = k.box.SetClockConfigXML(ctx, fixed.XML(false))
	}
	if err != nil {
		k.logger.Warn("standby clock: the speaker shows the wrong time for its zone and the fixed offset could not be written",
			"why", why, "timezone", c.Timezone, "shownOffsetMin", got, "correctOffsetMin", want, "err", err)
		return Config{}, false
	}
	k.logger.Info("standby clock: the speaker's own time zone rules are outdated for this zone, it shows a fixed offset instead",
		"why", why, "timezone", c.Timezone, "shownOffsetMin", got, "correctOffsetMin", want)
	return fixed, true
}

// Start re-applies the stored choice once per agent start: as soon as :8090
// answers and the system clock is plausible, with a bounded list of retries.
// With nothing stored it adopts a clock the speaker has switched on, so a
// speaker whose clock was set before this version (by the app or the install)
// is kept from now on. Blocks; run it in a goroutine.
func (k *Keeper) Start(ctx context.Context) {
	if c, ok := Load(k.path); ok && c.Brightness != nil && k.onBrightness != nil {
		k.onBrightness(*c.Brightness)
	}
	for i, d := range k.startDelays {
		if !k.sleep(ctx, d) {
			return
		}
		if !k.plausible(k.now()) {
			k.logger.Debug("standby clock: system clock not plausible yet, waiting", "attempt", i+1)
			continue
		}
		done, err := k.check(ctx, "agent start", true)
		if done {
			return
		}
		k.logger.Debug("standby clock: the speaker's clock is not readable yet", "attempt", i+1, "err", err)
	}
	k.logger.Info("standby clock: the speaker's clock setting stayed unreadable after every start-up attempt, leaving it until the next standby or wake",
		"attempts", len(k.startDelays))
}

// Event is a standby or a wake: one read, a write only on a difference. At
// most one every minGap. Never blocks the caller.
func (k *Keeper) Event(ctx context.Context, reason string) {
	k.mu.Lock()
	now := k.now()
	if !k.lastCheck.IsZero() && now.Sub(k.lastCheck) < k.minGap {
		k.mu.Unlock()
		return
	}
	k.lastCheck = now
	k.mu.Unlock()
	go func() {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if _, err := k.check(cctx, reason, false); err != nil {
			k.logger.Debug("standby clock: check skipped", "reason", reason, "err", err)
		}
	}()
}

// check is one look. force writes the stored choice even when the firmware
// already matches it. done is true when the speaker answered.
func (k *Keeper) check(ctx context.Context, reason string, force bool) (done bool, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.lastCheck = k.now()
	fw, err := k.Firmware(ctx)
	if err != nil {
		return false, err
	}
	stored, ok := Load(k.path)
	if !ok {
		if !fw.Enabled {
			// Off and nothing stored: the owner's choice, or a clock that was
			// never switched on. Either way not STR's to change.
			return true, nil
		}
		if err := Save(k.path, fw); err != nil {
			k.logger.Warn("standby clock: could not store the speaker's clock setting", "err", err)
			return true, nil
		}
		k.sync()
		k.logger.Info("standby clock: kept the speaker's clock setting so STR can put it back if the speaker drops it",
			"reason", reason, "timezone", fw.Timezone, "format24", fw.Format24, "brightness", brightnessAttr(fw.Brightness))
		if fw.Brightness != nil && k.onBrightness != nil {
			k.onBrightness(*fw.Brightness)
		}
		k.correctZoneLocked(ctx, fw, fw.Brightness != nil, reason)
		return true, nil
	}
	// A fixed offset standing in for the stored zone matches it for as long
	// as the offset is still right; once it is not (a zone with daylight
	// saving the firmware gets wrong), the zone is written and measured again.
	standIn := fixedOffsetHolds(stored, fw, k.now())
	if standIn {
		fw.Timezone, fw.OffsetMinutes = stored.Timezone, 0
	}
	diff := Differs(stored, fw, k.brightnessTaken)
	if diff == "" && !force {
		if !standIn {
			// The zone matches by name, but the firmware may still turn it into
			// the wrong hour (a zone set through the Bose app, or put back by a
			// reset). One /clockTime read; a write only when it is off.
			k.correctZoneLocked(ctx, stored, stored.Brightness != nil, reason)
		}
		k.logger.Debug("standby clock: the speaker's clock matches the stored setting", "reason", reason)
		return true, nil
	}
	res, werr := k.writeLocked(ctx, stored, reason)
	if werr != nil {
		k.logger.Warn("standby clock: could not put the clock setting back", "reason", reason, "differs", diff, "err", werr)
		return true, werr
	}
	if diff != "" {
		k.logger.Info("standby clock: the speaker had dropped its clock setting, put it back",
			"reason", reason, "differs", diff, "firmwareEnabled", fw.Enabled, "storedEnabled", stored.Enabled,
			"brightnessHonoured", boolAttr(res.BrightnessHonoured))
	} else {
		k.logger.Info("standby clock: re-applied the clock setting", "reason", reason,
			"enabled", stored.Enabled, "brightnessHonoured", boolAttr(res.BrightnessHonoured))
	}
	return true, nil
}

func brightnessAttr(b *int) any {
	if b == nil {
		return "unset"
	}
	return *b
}

func boolAttr(b *bool) any {
	if b == nil {
		return "unknown"
	}
	return *b
}
