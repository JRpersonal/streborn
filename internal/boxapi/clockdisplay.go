package boxapi

import (
	"context"
	"errors"
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

// SetClockConfigXML POSTs a complete <clockDisplay><clockConfig .../></clockDisplay>
// body. The firmware rejects a bare <clockConfig/> with 400 CLIENT_XML_ERROR
// (live 2026-06-01), so body must carry the wrapper.
func (c *Client) SetClockConfigXML(ctx context.Context, body string) error {
	return c.postXML(ctx, "/clockDisplay", body)
}
