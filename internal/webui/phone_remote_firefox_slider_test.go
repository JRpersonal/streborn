package webui

import (
	"strings"
	"testing"
)

// The volume and bass sliders style their thumb for WebKit and for Firefox. In
// Firefox a styled thumb stops the browser from painting its own track, so the
// slider showed only a floating dot on a desktop Firefox (#1125). The track and
// the filled part have to be styled explicitly; Chromium and Safari ignore the
// -moz rules and keep their native track.
func TestPhoneRemoteSliderHasAFirefoxTrack(t *testing.T) {
	for _, want := range []string{
		`.volslide input[type=range]::-moz-range-thumb`,
		`.volslide input[type=range]::-moz-range-track`,
		`.volslide input[type=range]::-moz-range-progress`,
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("the phone remote's slider CSS is missing %s", want)
		}
	}
}
