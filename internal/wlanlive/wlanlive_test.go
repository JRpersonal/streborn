package wlanlive

import "testing"

func TestRSSIToClass(t *testing.T) {
	cases := []struct {
		rssi int
		want string
	}{
		{-30, "EXCELLENT_SIGNAL"},
		{-55, "EXCELLENT_SIGNAL"},
		{-56, "GOOD_SIGNAL"},
		{-67, "GOOD_SIGNAL"},
		{-68, "MARGINAL_SIGNAL"},
		{-75, "MARGINAL_SIGNAL"},
		{-76, "POOR_SIGNAL"},
		{-90, "POOR_SIGNAL"},
	}
	for _, c := range cases {
		if got := RSSIToClass(c.rssi); got != c.want {
			t.Errorf("RSSIToClass(%d) = %q, want %q", c.rssi, got, c.want)
		}
	}
}

func TestParseStatus(t *testing.T) {
	// Associated 5 GHz box: ssid + freq present, wpa_state COMPLETED.
	const connected = `bssid=aa:bb:cc:dd:ee:ff
freq=5180
ssid=MyNetwork
id=0
mode=station
wpa_state=COMPLETED
ip_address=192.0.2.50
address=aa:bb:cc:dd:ee:ff
`
	ssid, freqKHz, assoc := parseStatus(connected)
	if !assoc {
		t.Fatal("expected associated")
	}
	if ssid != "MyNetwork" {
		t.Errorf("ssid = %q, want MyNetwork", ssid)
	}
	if freqKHz != 5180000 {
		t.Errorf("freqKHz = %d, want 5180000 (MHz*1000)", freqKHz)
	}

	// SSID containing '=' must survive (Cut on first separator only).
	const eqSSID = "ssid=a=b\nwpa_state=COMPLETED\nfreq=2437\n"
	if ssid, freq, ok := parseStatus(eqSSID); !ok || ssid != "a=b" || freq != 2437000 {
		t.Errorf("parseStatus(eqSSID) = %q,%d,%v; want a=b,2437000,true", ssid, freq, ok)
	}

	// Not associated (scanning): nothing reported even if a stale ssid line exists.
	const scanning = "wpa_state=SCANNING\nssid=Old\nfreq=2412\n"
	if ssid, freq, ok := parseStatus(scanning); ok || ssid != "" || freq != 0 {
		t.Errorf("parseStatus(scanning) = %q,%d,%v; want empty,false", ssid, freq, ok)
	}
}

func TestParseSignalPoll(t *testing.T) {
	const poll = `RSSI=-52
LINKSPEED=54
NOISE=9999
FREQUENCY=5180
`
	if rssi, ok := parseSignalPoll(poll); !ok || rssi != -52 {
		t.Errorf("parseSignalPoll = %d,%v; want -52,true", rssi, ok)
	}

	// Out-of-range / bogus values are rejected, including the RSSI=0 sentinel
	// (0 dBm is never a real associated-station reading and must not map to
	// EXCELLENT_SIGNAL).
	for _, bad := range []string{"RSSI=0\n", "RSSI=42\n", "RSSI=-9999\n", "RSSI=x\n", "LINKSPEED=54\n"} {
		if rssi, ok := parseSignalPoll(bad); ok {
			t.Errorf("parseSignalPoll(%q) = %d,true; want ok=false", bad, rssi)
		}
	}
}

func TestParseProcWireless(t *testing.T) {
	const content = `Inter-| sta-|   Quality        |   Discarded packets
 face | tus | link level noise |  nwid  crypt   frag
 wlan0: 0000   58.  -52.  -256        0      0      0
`
	if lvl, ok := parseProcWireless(content, "wlan0"); !ok || lvl != -52 {
		t.Errorf("parseProcWireless(wlan0) = %d,%v; want -52,true", lvl, ok)
	}
	if _, ok := parseProcWireless(content, "wlan1"); ok {
		t.Error("parseProcWireless(wlan1) should not match")
	}
}
