package main

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

const fwExe = `C:\Users\someone\Downloads\STR-Windows.exe`

func fwEnv(name string) (string, bool) {
	switch strings.ToUpper(name) {
	case "USERPROFILE":
		return `C:\Users\someone`, true
	case "SYSTEMROOT":
		return `C:\Windows`, true
	}
	return "", false
}

// What Windows writes when the first-run prompt is cancelled: a block rule per
// protocol, named after the file, with the path in lower case.
const fwCancelledPrompt = `{"rules":[` +
	`{"Name":"TCP Query User{A}","DisplayName":"str-windows.exe","Enabled":"True","Direction":"Inbound","Action":"Block","Profile":"Private","Program":"C:\\users\\someone\\downloads\\str-windows.exe"},` +
	`{"Name":"UDP Query User{B}","DisplayName":"str-windows.exe","Enabled":"True","Direction":"Inbound","Action":"Block","Profile":"Private","Program":"C:\\users\\someone\\downloads\\str-windows.exe"},` +
	`{"Name":"other","DisplayName":"Some other app","Enabled":"True","Direction":"Inbound","Action":"Block","Profile":"Any","Program":"C:\\Program Files\\Other\\other.exe"}` +
	`],"profiles":["Private"]}`

func TestFirewallCancelledPromptIsBlocked(t *testing.T) {
	rules, profiles, err := parseFirewallQuery([]byte(fwCancelledPrompt))
	if err != nil {
		t.Fatal(err)
	}
	c := evaluateFirewall(rules, profiles, []string{fwExe}, fwEnv)
	if !c.Checked || !c.Blocked {
		t.Fatalf("want blocked, got %+v", c)
	}
	if len(c.BlockRules) != 2 {
		t.Fatalf("want the two rules for this exe only, got %v", c.BlockRules)
	}
	if c.PublicNetwork {
		t.Fatal("a Private network is not public")
	}
}

func TestFirewallBlockOnInactiveProfileDoesNotCount(t *testing.T) {
	// The prompt answered "allow on private": Windows adds the allow rule for
	// Private and a block rule for Public. On a Private network nothing is
	// blocked; on a Public one the same rule is the block.
	q := `{"rules":{"Name":"x","DisplayName":"str-windows.exe","Enabled":"True","Direction":"Inbound","Action":"Block","Profile":"Public","Program":"` +
		strings.ReplaceAll(fwExe, `\`, `\\`) + `"},"profiles":"Private"}`
	rules, profiles, err := parseFirewallQuery([]byte(q))
	if err != nil {
		t.Fatal(err)
	}
	if c := evaluateFirewall(rules, profiles, []string{fwExe}, fwEnv); c.Blocked {
		t.Fatalf("public-only block on a private network must not count: %+v", c)
	}
	c := evaluateFirewall(rules, []string{"Public"}, []string{fwExe}, fwEnv)
	if !c.Blocked || !c.PublicNetwork {
		t.Fatalf("on a public network the rule blocks: %+v", c)
	}
}

func TestFirewallRuleFieldsMustAllMatch(t *testing.T) {
	base := fwRule{DisplayName: "r", Enabled: "True", Direction: "Inbound", Action: "Block", Profile: "Any", Program: fwExe}
	cases := map[string]func(r *fwRule){
		"disabled":      func(r *fwRule) { r.Enabled = "False" },
		"outbound":      func(r *fwRule) { r.Direction = "Outbound" },
		"allow":         func(r *fwRule) { r.Action = "Allow" },
		"other program": func(r *fwRule) { r.Program = `C:\Other\STR-Windows.exe` },
	}
	for name, mut := range cases {
		r := base
		mut(&r)
		if c := evaluateFirewall([]fwRule{r}, []string{"Private"}, []string{fwExe}, fwEnv); c.Blocked {
			t.Errorf("%s: must not count as a block", name)
		}
	}
	if c := evaluateFirewall([]fwRule{base}, []string{"Private"}, []string{fwExe}, fwEnv); !c.Blocked {
		t.Error("the unmodified rule is a block")
	}
}

func TestFirewallMatchesEnvPathsAndSecondProgram(t *testing.T) {
	r := fwRule{DisplayName: "r", Enabled: "True", Direction: "Inbound", Action: "Block", Profile: "Domain, Private",
		Program: `%USERPROFILE%/Downloads/str-windows.EXE`}
	if c := evaluateFirewall([]fwRule{r}, []string{"Private"}, []string{fwExe}, fwEnv); !c.Blocked {
		t.Fatal("an env-var path naming this exe is a block")
	}
	if c := evaluateFirewall([]fwRule{r}, []string{"DomainAuthenticated"}, []string{fwExe}, fwEnv); !c.Blocked {
		t.Fatal("DomainAuthenticated maps to the Domain profile")
	}
	// The symlink-resolved path (winget) counts as well.
	r.Program = `C:\pkg\STR-Windows.exe`
	if c := evaluateFirewall([]fwRule{r}, []string{"Private"}, []string{`C:\link\STR-Windows.exe`, `C:\pkg\STR-Windows.exe`}, fwEnv); !c.Blocked {
		t.Fatal("the resolved program path is matched too")
	}
}

func TestFirewallUnknownProfilesStillCount(t *testing.T) {
	r := fwRule{DisplayName: "r", Enabled: "True", Direction: "Inbound", Action: "Block", Profile: "Public", Program: fwExe}
	if c := evaluateFirewall([]fwRule{r}, nil, []string{fwExe}, fwEnv); !c.Blocked {
		t.Fatal("with no active profile known, a block rule must not be hidden")
	}
}

func TestParseFirewallQueryShapes(t *testing.T) {
	for name, in := range map[string]string{
		"bom and nulls":  "\xef\xbb\xbf" + `{"rules":null,"profiles":null}`,
		"empty arrays":   `{"rules":[],"profiles":[]}`,
		"missing fields": `{}`,
	} {
		rules, profiles, err := parseFirewallQuery([]byte(in))
		if err != nil || len(rules) != 0 || len(profiles) != 0 {
			t.Errorf("%s: got %v %v %v", name, rules, profiles, err)
		}
	}
	for name, in := range map[string]string{
		"empty":   "  \r\n",
		"garbage": "Get-NetFirewallRule : access denied",
	} {
		if _, _, err := parseFirewallQuery([]byte(in)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	// An unreadable result is never a block.
	if c := evaluateFirewall(nil, nil, []string{fwExe}, fwEnv); c.Blocked {
		t.Fatal("no rules, no block")
	}
}

func TestExpandWinEnv(t *testing.T) {
	cases := map[string]string{
		`%SystemRoot%\x.exe`:     `C:\Windows\x.exe`,
		`%NOPE%\x.exe`:           `%NOPE%\x.exe`,
		`50% off`:                `50% off`,
		`%%`:                     `%%`,
		`%USERPROFILE%%NOPE%\a`:  `C:\Users\someone%NOPE%\a`,
		`plain\path\no\vars.exe`: `plain\path\no\vars.exe`,
	}
	for in, want := range cases {
		if got := expandWinEnv(in, fwEnv); got != want {
			t.Errorf("expandWinEnv(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirewallUnblockScript(t *testing.T) {
	s := firewallUnblockScript([]string{`C:\Users\o'brien\STR-Windows.exe`}, false, `C:\Temp\r.txt`)
	for _, want := range []string{
		`'C:\Users\o''brien\STR-Windows.exe'`,
		`-Direction Inbound -Action Block`,
		`Remove-NetFirewallRule`,
		`-Action Allow`,
		`'TCP', 'UDP'`,
		`-Profile Private,Domain `,
		`$out = 'C:\Temp\r.txt'`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if strings.Contains(s, "Public") {
		t.Error("Public must only be allowed when asked for")
	}
	if p := firewallUnblockScript([]string{fwExe}, true, `r`); !strings.Contains(p, "-Profile Private,Domain,Public ") {
		t.Error("includePublic adds the Public profile")
	}
}

func TestEncodePowerShellCommand(t *testing.T) {
	in := "Write-Output 'ü'"
	raw, err := base64.StdEncoding.DecodeString(encodePowerShellCommand(in))
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(raw[2*i:])
	}
	if got := string(utf16.Decode(u)); got != in {
		t.Fatalf("round trip: %q", got)
	}
}

func TestFirewallStepError(t *testing.T) {
	if got := firewallStepError(1, "error: Access is denied."); got != "Access is denied." {
		t.Errorf("got %q", got)
	}
	if got := firewallStepError(5, ""); !strings.Contains(got, "exit code 5") {
		t.Errorf("got %q", got)
	}
}
