package main

import (
	"reflect"
	"strings"
	"testing"
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
var fwCancelledPrompt = []fwRule{
	{Name: "str-windows.exe", DisplayName: "str-windows.exe", Enabled: "True", Direction: "Inbound", Action: "Block", Profile: "Private", Program: `C:\users\someone\downloads\str-windows.exe`},
	{Name: "str-windows.exe", DisplayName: "str-windows.exe", Enabled: "True", Direction: "Inbound", Action: "Block", Profile: "Private", Program: `C:\users\someone\downloads\str-windows.exe`},
	{Name: "other", DisplayName: "Some other app", Enabled: "True", Direction: "Inbound", Action: "Block", Profile: "Any", Program: `C:\Program Files\Other\other.exe`},
}

func TestFirewallCancelledPromptIsBlocked(t *testing.T) {
	c := evaluateFirewall(fwCancelledPrompt, []string{"Private"}, []string{fwExe}, fwEnv)
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
	rules := []fwRule{{Name: "x", DisplayName: "str-windows.exe", Enabled: "True", Direction: "Inbound", Action: "Block", Profile: "Public", Program: fwExe}}
	if c := evaluateFirewall(rules, []string{"Private"}, []string{fwExe}, fwEnv); c.Blocked {
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

func TestFirewallNoRulesIsNoBlock(t *testing.T) {
	// An empty or unreadable result is never a block.
	if c := evaluateFirewall(nil, nil, []string{fwExe}, fwEnv); c.Blocked {
		t.Fatal("no rules, no block")
	}
}

func TestFwProfileString(t *testing.T) {
	cases := map[int64]string{
		0x1:        "Domain",
		0x2:        "Private",
		0x4:        "Public",
		0x3:        "Domain, Private",
		0x6:        "Private, Public",
		0x7:        "Any",
		0x7fffffff: "Any",
	}
	for in, want := range cases {
		if got := fwProfileString(in); got != want {
			t.Errorf("fwProfileString(%#x) = %q, want %q", in, got, want)
		}
		if in&0x2 != 0 && !ruleProfileActive(fwProfileString(in), []string{"Private"}) {
			t.Errorf("%#x covers Private but does not read as active on it", in)
		}
	}
}

func TestFwActiveProfiles(t *testing.T) {
	if got := fwActiveProfiles(0x2 | 0x4); !reflect.DeepEqual(got, []string{"Private", "Public"}) {
		t.Errorf("got %v", got)
	}
	if got := fwActiveProfiles(0x1); !reflect.DeepEqual(got, []string{"DomainAuthenticated"}) {
		t.Errorf("got %v", got)
	}
}

func TestFirewallHelperArgsRoundTrip(t *testing.T) {
	path := `C:\Users\Jürgen O'Brien\AppData\Local\Temp\str-firewall-1.txt`
	for _, pub := range []bool{false, true} {
		gotPath, gotPub, ok := parseFirewallHelperArgs(firewallHelperArgs(path, pub))
		if !ok || gotPath != path || gotPub != pub {
			t.Errorf("public=%v: got %q %v %v", pub, gotPath, gotPub, ok)
		}
	}
	for _, args := range [][]string{nil, {}, {"--other"}, {"-NoProfile", firewallHelperFlag}} {
		if _, _, ok := parseFirewallHelperArgs(args); ok {
			t.Errorf("%q is not a helper invocation", args)
		}
	}
	if handled, _ := firewallHelperMain([]string{"--whatever"}); handled {
		t.Error("a normal start must not be handled as the helper")
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

func TestFirewallStepError(t *testing.T) {
	if got := firewallStepError(1, "error: Access is denied."); got != "Access is denied." {
		t.Errorf("got %q", got)
	}
	if got := firewallStepError(5, ""); !strings.Contains(got, "exit code 5") {
		t.Errorf("got %q", got)
	}
}
