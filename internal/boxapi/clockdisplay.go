package boxapi

import (
	"context"
	"errors"
	"math"
	"time"
)

// ClockConfig is the firmware's standby clock configuration, the one element
// inside GET /clockDisplay:
//
//	<clockDisplay><clockConfig timezoneInfo="Europe/Berlin" userEnable="true"
//	  timeFormat="TIME_FORMAT_24HOUR_ID" userOffsetMinute="0"
//	  brightnessLevel="70" userUtcTime="0" /></clockDisplay>
//
// FW 27.0.6 carries exactly these six attributes (`getpdo ClockDisplay` over
// TAP shows the same). The values are kept as the firmware wrote them; an
// attribute the firmware left out is "". The POST body is built by the caller
// (internal/standbyclock), because which attributes are safe to send is a
// decision, not a format.
type ClockConfig struct {
	UserEnable       string `xml:"userEnable,attr"`
	TimezoneInfo     string `xml:"timezoneInfo,attr"`
	UserOffsetMinute string `xml:"userOffsetMinute,attr"`
	TimeFormat       string `xml:"timeFormat,attr"`
	BrightnessLevel  string `xml:"brightnessLevel,attr"`
}

// ErrNoClockConfig is returned when /clockDisplay answers without a
// clockConfig element: a speaker without a clock, or a firmware in the middle
// of a restart.
var ErrNoClockConfig = errors.New("box /clockDisplay: no clockConfig")

// GetClockConfig reads the firmware's standby clock configuration.
func (c *Client) GetClockConfig(ctx context.Context) (ClockConfig, error) {
	var doc struct {
		Config *ClockConfig `xml:"clockConfig"`
	}
	if err := c.getXML(ctx, "/clockDisplay", &doc); err != nil {
		return ClockConfig{}, err
	}
	if doc.Config == nil || doc.Config.UserEnable == "" {
		return ClockConfig{}, ErrNoClockConfig
	}
	return *doc.Config, nil
}

// ClockTime is GET /clockTime: the firmware's UTC clock and the local wall
// time it derives from the configured zone, which is what the display shows.
//
//	<clockTime utcTime="1793942709" timeFormat="TIME_FORMAT_24HOUR_ID" ...>
//	  <localTime year="2026" month="10" dayOfMonth="6" dayOfWeek="5"
//	    hour="6" minute="25" second="9" /></clockTime>
//
// The month is zero-based (that answer was taken on 6 November), so only the
// time of day is used.
type ClockTime struct {
	UTCTime int64 `xml:"utcTime,attr"`
	Local   *struct {
		Hour   int `xml:"hour,attr"`
		Minute int `xml:"minute,attr"`
		Second int `xml:"second,attr"`
	} `xml:"localTime"`
}

// OffsetMinutes is how far the firmware's local time is from UTC, east
// positive, read from the time of day alone. ok is false when the answer has
// no usable times.
func (t ClockTime) OffsetMinutes() (int, bool) {
	if t.UTCTime <= 0 || t.Local == nil {
		return 0, false
	}
	u := time.Unix(t.UTCTime, 0).UTC()
	d := (t.Local.Hour*3600 + t.Local.Minute*60 + t.Local.Second) - (u.Hour()*3600 + u.Minute()*60 + u.Second())
	d = ((d % 86400) + 86400) % 86400
	if d > 14*3600 {
		d -= 86400
	}
	return int(math.Round(float64(d) / 60)), true
}

// GetClockTime reads the firmware's clock as the display shows it.
func (c *Client) GetClockTime(ctx context.Context) (ClockTime, error) {
	var t ClockTime
	if err := c.getXML(ctx, "/clockTime", &t); err != nil {
		return ClockTime{}, err
	}
	return t, nil
}

// SetClockConfigXML POSTs a complete <clockDisplay><clockConfig .../></clockDisplay>
// body. The firmware rejects a bare <clockConfig/> with 400 CLIENT_XML_ERROR
// (live 2026-06-01), so body must carry the wrapper.
func (c *Client) SetClockConfigXML(ctx context.Context, body string) error {
	return c.postXML(ctx, "/clockDisplay", body)
}
