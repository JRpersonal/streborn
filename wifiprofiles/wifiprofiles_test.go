package wifiprofiles

import (
	"reflect"
	"testing"
)

// The fixtures below mirror the shape of real tool output (netsh on an
// English and a German Windows, nmcli terse mode, networksetup, airport,
// security) but use placeholder SSIDs, placeholder MACs and made-up
// passphrases throughout.

func netshProfiles(ssids ...string) []Profile {
	out := make([]Profile, 0, len(ssids))
	for _, s := range ssids {
		out = append(out, Profile{SSID: s, HasPass: true, Source: "netsh"})
	}
	return out
}

func TestParseNetshProfiles(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []Profile
	}{
		{
			name: "english output",
			out: "\n" +
				"Profiles on interface Wi-Fi:\n" +
				"\n" +
				"Group policy profiles (read only)\n" +
				"---------------------------------\n" +
				"    <None>\n" +
				"\n" +
				"User profiles\n" +
				"-------------\n" +
				"    All User Profile     : TestNet-A\n" +
				"    All User Profile     : TestNet-B\n",
			want: netshProfiles("TestNet-A", "TestNet-B"),
		},
		{
			name: "german output with CRLF line endings",
			out: "\r\n" +
				"Profile auf Schnittstelle \"WLAN\":\r\n" +
				"\r\n" +
				"Gruppenrichtlinienprofile (schreibgeschützt)\r\n" +
				"--------------------------------------------\r\n" +
				"    <Keine>\r\n" +
				"\r\n" +
				"Benutzerprofile\r\n" +
				"---------------\r\n" +
				"    Profil für alle Benutzer: PlaceholderWLAN\r\n" +
				"    Profil für alle Benutzer: TestNet-Gast\r\n",
			want: netshProfiles("PlaceholderWLAN", "TestNet-Gast"),
		},
		{
			name: "indented interface header line is excluded",
			out: "    Interface name       : Wi-Fi\n" +
				"    All User Profile     : TestNet-A\n",
			want: netshProfiles("TestNet-A"),
		},
		{
			name: "ssid containing a colon survives",
			out:  "    All User Profile     : Cafe: Guest\n",
			want: netshProfiles("Cafe: Guest"),
		},
		{
			name: "duplicate ssids are deduped",
			out: "    All User Profile     : TestNet-A\n" +
				"    All User Profile     : TestNet-A\n",
			want: netshProfiles("TestNet-A"),
		},
		{
			name: "no wlan adapter",
			out:  "There is no wireless interface on the system.\n",
			want: []Profile{},
		},
		{
			name: "empty output",
			out:  "",
			want: []Profile{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNetshProfiles(tt.out)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseNetshProfiles() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseNetshPasswordClear(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "english key content",
			out: "Profile TestNet-A on interface Wi-Fi:\n" +
				"\n" +
				"Security settings\n" +
				"-----------------\n" +
				"    Authentication         : WPA2-Personal\n" +
				"    Cipher                 : CCMP\n" +
				"    Security key           : Present\n" +
				"    Key Content            : test-passphrase-1234\n",
			want: "test-passphrase-1234",
		},
		{
			name: "german key content with umlaut, decoy fields not matched",
			out: "Profil \"PlaceholderWLAN\" auf Schnittstelle \"WLAN\":\n" +
				"\n" +
				"Sicherheitseinstellungen\n" +
				"------------------------\n" +
				"    Authentifizierung      : WPA2-Personal\n" +
				"    Verschlüsselung        : CCMP\n" +
				"    Sicherheitsschlüssel   : Vorhanden\n" +
				"    Schlüsselinhalt        : platzhalter-passwort\n",
			want: "platzhalter-passwort",
		},
		{
			name: "german key content without umlaut",
			out:  "    Schluesselinhalt       : platzhalter-passwort\n",
			want: "platzhalter-passwort",
		},
		{
			name: "german key content in mangled codepage",
			out:  "    Schl¼sselinhalt        : platzhalter-passwort\n",
			want: "platzhalter-passwort",
		},
		{
			name: "absent key english",
			out: "    Security key           : Absent\n" +
				"    Key Content            : Absent\n",
			want: "",
		},
		{
			name: "absent key german",
			out:  "    Schlüsselinhalt        : Nicht vorhanden\n",
			want: "",
		},
		{
			name: "open network without key line",
			out: "    Authentication         : Open\n" +
				"    Cipher                 : None\n",
			want: "",
		},
		{
			name: "empty output",
			out:  "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseNetshPasswordClear(tt.out); got != tt.want {
				t.Errorf("parseNetshPasswordClear() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseNetshInterfacesSSID(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "connected interface, bssid line not matched",
			out: "There is 1 interface on the system:\n" +
				"\n" +
				"    Name                   : Wi-Fi\n" +
				"    Description            : Example Wireless Adapter\n" +
				"    Physical address       : aa:bb:cc:dd:ee:ff\n" +
				"    State                  : connected\n" +
				"    SSID                   : TestNet-A\n" +
				"    BSSID                  : aa:bb:cc:dd:ee:ff\n",
			want: "TestNet-A",
		},
		{
			name: "bssid before ssid is still not matched",
			out: "    BSSID                  : aa:bb:cc:dd:ee:ff\n" +
				"    SSID                   : TestNet-B\n",
			want: "TestNet-B",
		},
		{
			name: "disconnected interface without ssid line",
			out: "There is 1 interface on the system:\n" +
				"\n" +
				"    Name                   : Wi-Fi\n" +
				"    State                  : disconnected\n",
			want: "",
		},
		{
			name: "empty ssid value is skipped",
			out: "    SSID                   :\n" +
				"    SSID                   : TestNet-A\n",
			want: "TestNet-A",
		},
		{
			name: "empty output",
			out:  "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseNetshInterfacesSSID(tt.out); got != tt.want {
				t.Errorf("parseNetshInterfacesSSID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseNmcliConnections(t *testing.T) {
	nm := func(ssids ...string) []Profile {
		out := make([]Profile, 0, len(ssids))
		for _, s := range ssids {
			out = append(out, Profile{SSID: s, HasPass: true, Source: "nmcli"})
		}
		return out
	}
	tests := []struct {
		name string
		out  string
		want []Profile
	}{
		{
			name: "wireless connections only",
			out: "TestNet-A:802-11-wireless\n" +
				"Wired connection 1:802-3-ethernet\n" +
				"PlaceholderWLAN:802-11-wireless\n" +
				"docker0:bridge\n" +
				"lo:loopback\n",
			want: nm("TestNet-A", "PlaceholderWLAN"),
		},
		{
			name: "duplicate names are deduped",
			out: "TestNet-A:802-11-wireless\n" +
				"TestNet-A:802-11-wireless\n",
			want: nm("TestNet-A"),
		},
		{
			name: "line without separator is ignored",
			out: "garbage\n" +
				"TestNet-A:802-11-wireless\n",
			want: nm("TestNet-A"),
		},
		{
			name: "empty output",
			out:  "",
			want: []Profile{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNmcliConnections(tt.out)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseNmcliConnections() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseNmcliActiveSSID(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "one active network",
			out: "no:TestNet-B\n" +
				"yes:TestNet-A\n" +
				"no:PlaceholderWLAN\n",
			want: "TestNet-A",
		},
		{
			name: "no active network",
			out: "no:TestNet-B\n" +
				"no:PlaceholderWLAN\n",
			want: "",
		},
		{
			name: "empty output",
			out:  "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseNmcliActiveSSID(tt.out); got != tt.want {
				t.Errorf("parseNmcliActiveSSID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseHardwarePortsWifiDevice(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "wifi port after ethernet port",
			out: "Hardware Port: Ethernet\n" +
				"Device: en1\n" +
				"Ethernet Address: aa:bb:cc:dd:ee:ff\n" +
				"\n" +
				"Hardware Port: Wi-Fi\n" +
				"Device: en0\n" +
				"Ethernet Address: aa:bb:cc:dd:ee:ff\n",
			want: "en0",
		},
		{
			name: "no wifi port",
			out: "Hardware Port: Ethernet\n" +
				"Device: en1\n" +
				"Ethernet Address: aa:bb:cc:dd:ee:ff\n",
			want: "",
		},
		{
			name: "empty output",
			out:  "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseHardwarePortsWifiDevice(tt.out); got != tt.want {
				t.Errorf("parseHardwarePortsWifiDevice() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParsePreferredNetworks(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []Profile
	}{
		{
			name: "header line skipped, networks listed",
			out: "Preferred networks on en0:\n" +
				"\tTestNet-A\n" +
				"\tPlaceholderWLAN\n",
			want: []Profile{
				{SSID: "TestNet-A", HasPass: true, Source: "networksetup"},
				{SSID: "PlaceholderWLAN", HasPass: true, Source: "networksetup"},
			},
		},
		{
			name: "empty output",
			out:  "",
			want: []Profile{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePreferredNetworks(tt.out)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parsePreferredNetworks() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseSecurityPassword(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "quoted password line",
			out: "keychain: \"/Users/someone/Library/Keychains/login.keychain-db\"\n" +
				"class: \"genp\"\n" +
				"password: \"test-passphrase-1234\"\n",
			want: "test-passphrase-1234",
		},
		{
			name: "access denied without password line",
			out:  "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain.\n",
			want: "",
		},
		{
			name: "empty output",
			out:  "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSecurityPassword(tt.out); got != tt.want {
				t.Errorf("parseSecurityPassword() = %q, want %q", got, tt.want)
			}
		})
	}
}
