package main

import (
	"strings"
	"testing"
)

// TestBuildEnableSSHCommands asserts the exact stick-free unlock sequence: all
// four sys-configuration keys plus envswitch, with the remote_services/sshd
// injection present on BOTH margeServerUrl and the envswitch marge slot (the
// #519 full-config superset that covers every variant), and every value
// double-quoted so the box command parser keeps the injection as one argument.
func TestBuildEnableSSHCommands(t *testing.T) {
	base := "https://str-setup.invalid"
	cmds := buildEnableSSHCommands(base)
	if len(cmds) != 5 {
		t.Fatalf("want 5 commands, got %d: %v", len(cmds), cmds)
	}
	inj := base + remoteServicesInjection
	want := []string{
		`sys configuration bmxRegistryUrl "` + base + `/bmx/registry/v1/services"`,
		`sys configuration statsServerUrl "` + base + `"`,
		`sys configuration margeServerUrl "` + inj + `"`,
		`sys configuration swUpdateUrl "` + base + `/updates/soundtouch"`,
		`envswitch boseurls set "` + inj + `" "` + base + `/updates/soundtouch"`,
	}
	for i := range want {
		if cmds[i] != want[i] {
			t.Errorf("command %d:\n got %q\nwant %q", i, cmds[i], want[i])
		}
	}

	// The injection must ride the runtime margeServerUrl (the layer ginger/taigan
	// actually re-read in live testing) AND the envswitch layer (used by rhino).
	margeSysConfig, envswitch := cmds[2], cmds[4]
	if !strings.Contains(margeSysConfig, remoteServicesInjection) {
		t.Error("sys configuration margeServerUrl is missing the injection")
	}
	if !strings.Contains(envswitch, remoteServicesInjection) {
		t.Error("envswitch boseurls is missing the injection")
	}

	// Every command must be balanced-quoted so a value with spaces/semicolons
	// stays one argument on the box.
	for _, c := range cmds {
		if strings.Count(c, `"`)%2 != 0 {
			t.Errorf("unbalanced quotes in %q", c)
		}
	}
}

// TestBuildResetBoseURLCommands asserts the cleanup restores the stock hosts and
// carries no leftover injection.
func TestBuildResetBoseURLCommands(t *testing.T) {
	cmds := buildResetBoseURLCommands()
	joined := strings.Join(cmds, "\n")
	if strings.Contains(joined, remoteServicesInjection) {
		t.Error("reset commands must not contain the injection")
	}
	for _, want := range []string{stockBoseURLs.marge, stockBoseURLs.stats, stockBoseURLs.swUpdate, stockBoseURLs.bmx} {
		if !strings.Contains(joined, want) {
			t.Errorf("reset commands missing stock URL %q", want)
		}
	}
	if !strings.Contains(joined, `envswitch boseurls set "`+stockBoseURLs.marge+`"`) {
		t.Error("reset must also clear the envswitch layer")
	}
}
