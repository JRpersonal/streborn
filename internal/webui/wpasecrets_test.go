package webui

import (
	"strings"
	"testing"
)

// The file this redacts is served by an unauthenticated GET and travels in
// every diagnostic bundle, so a leaked passphrase reaches a mailbox or a public
// issue. Each case below is a shape a real wpa_supplicant.conf takes.
func TestRedactWPASecrets(t *testing.T) {
	cases := []struct {
		name, in, mustNotContain string
	}{
		{"plain psk", `network={
	ssid="Home"
	psk="hunter2"
}`, "hunter2"},
		{
			// The case the desktop app's pattern let through: a value that stops
			// at whitespace leaves the rest of the passphrase in the output.
			name: "passphrase with spaces",
			in: `network={
	ssid="Home"
	psk="correct horse battery staple"
}`,
			mustNotContain: "battery staple",
		},
		{"unquoted hashed psk", "\tpsk=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n", "0123456789abcdef"},
		{"wep key", "\twep_key0=\"s3cr3t\"\n", "s3cr3t"},
		{"sae password", "\tsae_password=\"my wpa3 secret\"\n", "wpa3 secret"},
		{"eap password", "\tpassword=\"enterprise pw\"\n", "enterprise pw"},
		{"private key passwd", "\tprivate_key_passwd=\"keypw\"\n", "keypw"},
		{"leading spaces instead of tab", "   psk=\"spaced out\"\n", "spaced out"},
		{"uppercase key", "\tPSK=\"ShoutedSecret\"\n", "ShoutedSecret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactWPASecrets(tc.in)
			if strings.Contains(got, tc.mustNotContain) {
				t.Errorf("the secret survived redaction:\n%s", got)
			}
			if !strings.Contains(got, "<redacted:") {
				t.Errorf("nothing was redacted:\n%s", got)
			}
		})
	}
}

// Everything the file is actually read FOR has to survive, or the redaction
// destroys the diagnosis it was protecting.
func TestRedactKeepsWhatTheFileIsReadFor(t *testing.T) {
	in := `ctrl_interface=/var/run/wpa_supplicant
update_config=1

network={
	ssid="Home"
	psk="hunter2"
	key_mgmt=WPA-PSK
	priority=5
}

network={
	ssid="Guest"
	psk="another one"
	scan_ssid=1
}`
	got := redactWPASecrets(in)
	for _, want := range []string{
		"ctrl_interface=/var/run/wpa_supplicant",
		"update_config=1",
		`ssid="Home"`,
		`ssid="Guest"`,
		"key_mgmt=WPA-PSK",
		"priority=5",
		"scan_ssid=1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("redaction destroyed %q, which is what the file is read for:\n%s", want, got)
		}
	}
	// The count of network blocks is the single question this file answers.
	if n := strings.Count(got, "network={"); n != 2 {
		t.Errorf("network block count = %d, want 2", n)
	}
	if strings.Contains(got, "hunter2") || strings.Contains(got, "another one") {
		t.Errorf("a secret survived:\n%s", got)
	}
}

// An empty password is a real diagnosis ("the speaker has no key stored"), so
// the marker carries the length and nothing else.
func TestRedactReportsLengthOnly(t *testing.T) {
	got := redactWPASecrets("\tpsk=\"\"\n")
	if !strings.Contains(got, "<redacted:0 chars>") {
		t.Errorf("an empty passphrase should still be visible AS empty: %q", got)
	}
	got = redactWPASecrets("\tpsk=\"abcdefg\"\n")
	if !strings.Contains(got, "<redacted:7 chars>") {
		t.Errorf("length not reported: %q", got)
	}
}

func TestRedactHandlesAnAbsentFile(t *testing.T) {
	// readTail returns an error string when the file is missing; it must pass
	// through untouched rather than being mangled into something confusing.
	in := "ERR: open /etc/wpa_supplicant.conf: no such file or directory"
	if got := redactWPASecrets(in); got != in {
		t.Errorf("error text was altered: %q", got)
	}
	if got := redactWPASecrets(""); got != "" {
		t.Errorf("empty input became %q", got)
	}
}
