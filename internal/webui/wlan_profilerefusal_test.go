package webui

import (
	"context"
	"errors"
	"testing"
)

// The 1035 refusal every field speaker answers is expected and logged as such;
// anything else (a timeout, another error) stays a warning.
func TestIsWifiProfileSetupRefusal(t *testing.T) {
	refusal := errors.New(`box /addWirelessProfile: 500: <?xml version="1.0" encoding="UTF-8" ?><errors deviceID="device-id-here"><error value="1035" name="WIFI_PROFILE_SETUP_FAILED" severity="Unknown">UNKNOWN_ERROR</error></errors>`)
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"field refusal", refusal, true},
		{"timeout", context.DeadlineExceeded, false},
		{"other 500", errors.New("box /addWirelessProfile: 500: <errors><error value=\"1046\" name=\"ALLEGROWEBSERVER_TIMEOUT\"/></errors>"), false},
		{"nil", nil, false},
	} {
		if got := isWifiProfileSetupRefusal(tc.err); got != tc.want {
			t.Errorf("%s: isWifiProfileSetupRefusal = %v, want %v", tc.name, got, tc.want)
		}
	}
}
