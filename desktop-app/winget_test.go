package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIsWingetManagedPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"user scope", `C:\Users\someone\AppData\Local\Microsoft\WinGet\Packages\JRpersonal.STReborn_Microsoft.Winget.Source_8wekyb3d8bbwe\STR-Windows.exe`, true},
		{"user scope command alias hardlink", `C:\Users\someone\AppData\Local\Microsoft\WinGet\Packages\JRpersonal.STReborn_Microsoft.Winget.Source_8wekyb3d8bbwe\streborn.exe`, true},
		{"machine scope", `C:\Program Files\WinGet\Packages\JRpersonal.STReborn_Microsoft.Winget.Source_8wekyb3d8bbwe\STR-Windows.exe`, true},
		{"machine scope x86", `C:\Program Files (x86)\WinGet\Packages\JRpersonal.STReborn_Microsoft.Winget.Source_8wekyb3d8bbwe\STR-Windows.exe`, true},
		{"local manifest source", `C:\Users\someone\AppData\Local\Microsoft\WinGet\Packages\JRpersonal.STReborn__DefaultSource\STR-Windows.exe`, true},
		{"moved package root", `D:\Tools\Portable\JRpersonal.STReborn_Microsoft.Winget.Source_8wekyb3d8bbwe\STR-Windows.exe`, true},
		{"different case", `c:\users\someone\appdata\local\microsoft\winget\packages\jrpersonal.streborn_microsoft.winget.source_8wekyb3d8bbwe\str-windows.exe`, true},
		{"versioned exe name", `C:\Users\someone\AppData\Local\Microsoft\WinGet\Packages\JRpersonal.STReborn_Microsoft.Winget.Source_8wekyb3d8bbwe\STR-Windows-v1.0.1.exe`, true},
		{"forward slashes", `C:/Program Files/WinGet/Packages/JRpersonal.STReborn_Microsoft.Winget.Source_8wekyb3d8bbwe/STR-Windows.exe`, true},
		{"downloads folder", `C:\Users\someone\Downloads\STR-Windows.exe`, false},
		{"versioned in downloads", `C:\Users\someone\Downloads\STR-Windows-v1.0.1.exe`, false},
		{"winget links alias", `C:\Users\someone\AppData\Local\Microsoft\WinGet\Links\streborn.exe`, false},
		{"another winget package", `C:\Users\someone\AppData\Local\Microsoft\WinGet\Packages\Other.Tool_Microsoft.Winget.Source_8wekyb3d8bbwe\tool.exe`, false},
		{"id without source suffix", `C:\Users\someone\AppData\Local\Microsoft\WinGet\Packages\JRpersonal.STReborn\STR-Windows.exe`, false},
		{"id prefix but different package", `C:\Users\someone\AppData\Local\Microsoft\WinGet\Packages\JRpersonal.STRebornBeta_x\STR-Windows.exe`, false},
		{"bare underscore folder", `C:\WinGet\Packages\JRpersonal.STReborn_\STR-Windows.exe`, false},
		{"nested below the package folder", `C:\WinGet\Packages\JRpersonal.STReborn_src\sub\STR-Windows.exe`, false},
		{"the folder itself", `C:\WinGet\Packages\JRpersonal.STReborn_src\`, false},
		{"empty", ``, false},
		{"bare file", `STR-Windows.exe`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isWingetManagedPath(c.path); got != c.want {
				t.Errorf("isWingetManagedPath(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

func TestWingetCommands(t *testing.T) {
	want := "winget upgrade --id JRpersonal.STReborn --exact --silent --accept-source-agreements --accept-package-agreements"
	if got := wingetManualCommand(); got != want {
		t.Errorf("manual command = %q, want %q", got, want)
	}
	for _, v := range []string{"v1.0.3", "1.0.3", " V1.0.3 "} {
		got := wingetShowArgs(v)
		exp := []string{"show", "--id", "JRpersonal.STReborn", "--exact", "--version", "1.0.3", "--accept-source-agreements"}
		if !reflect.DeepEqual(got, exp) {
			t.Errorf("wingetShowArgs(%q) = %v, want %v", v, got, exp)
		}
	}
	if !strings.Contains(errWingetManaged.Error(), want) {
		t.Errorf("errWingetManaged does not name the command: %v", errWingetManaged)
	}
}

func TestWingetUpgradeScript(t *testing.T) {
	s := wingetUpgradeScript(4242, `C:\Users\o'neil\AppData\Local\Microsoft\WindowsApps\winget.exe`,
		`C:\Users\o'neil\AppData\Local\Microsoft\WinGet\Packages\JRpersonal.STReborn_x\STR-Windows.exe`,
		`C:\Users\o'neil\AppData\Roaming\ST Reborn\winget-upgrade-failed`)
	for _, want := range []string{
		"Wait-Process -Id 4242",
		`& 'C:\Users\o''neil\AppData\Local\Microsoft\WindowsApps\winget.exe' 'upgrade' '--id' 'JRpersonal.STReborn' '--exact' '--silent' '--accept-source-agreements' '--accept-package-agreements'`,
		`Set-Content -LiteralPath 'C:\Users\o''neil\AppData\Roaming\ST Reborn\winget-upgrade-failed'`,
		`Start-Process -FilePath 'C:\Users\o''neil\AppData\Local\Microsoft\WinGet\Packages\JRpersonal.STReborn_x\STR-Windows.exe'`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q:\n%s", want, s)
		}
	}
	// The relaunch comes after winget, whatever winget answered.
	if strings.Index(s, "Start-Process") < strings.Index(s, "'upgrade'") {
		t.Errorf("app is started before the upgrade ran:\n%s", s)
	}
}

func TestFindWinget(t *testing.T) {
	notOnPath := func(string) (string, error) { return "", errors.New("not found") }
	onPath := func(string) (string, error) { return `C:\Tools\winget.exe`, nil }
	alias := filepath.Join(`LAD`, "Microsoft", "WindowsApps", "winget.exe")
	existsOnly := func(p string) func(string) bool { return func(q string) bool { return q == p } }

	if got := findWinget(onPath, "LAD", existsOnly(alias)); got != `C:\Tools\winget.exe` {
		t.Errorf("PATH should win, got %q", got)
	}
	if got := findWinget(notOnPath, "LAD", existsOnly(alias)); got != alias {
		t.Errorf("alias fallback: got %q, want %q", got, alias)
	}
	if got := findWinget(notOnPath, "LAD", existsOnly("")); got != "" {
		t.Errorf("nothing there should be empty, got %q", got)
	}
	if got := findWinget(notOnPath, "", func(string) bool { return true }); got != "" {
		t.Errorf("no LOCALAPPDATA should be empty, got %q", got)
	}
}

func TestWingetOutcome(t *testing.T) {
	cmd := wingetManualCommand()
	cases := map[string]WingetUpdateResult{
		wingetReasonStarted:     {Started: true, Command: cmd, Reason: wingetReasonStarted},
		wingetReasonNotOffered:  {NotYet: true, Command: cmd, Reason: wingetReasonNotOffered},
		wingetReasonNotFound:    {Command: cmd, Reason: wingetReasonNotFound},
		wingetReasonStartFailed: {Command: cmd, Reason: wingetReasonStartFailed},
		wingetReasonNotManaged:  {Command: cmd, Reason: wingetReasonNotManaged},
		wingetReasonUnsupported: {Command: cmd, Reason: wingetReasonUnsupported},
	}
	for reason, want := range cases {
		if got := wingetOutcome(reason); got != want {
			t.Errorf("wingetOutcome(%q) = %+v, want %+v", reason, got, want)
		}
	}
}

func TestDecideWingetShortcut(t *testing.T) {
	cases := []struct {
		managed, done, exists bool
		want                  shortcutDecision
	}{
		{false, false, false, shortcutSkip},         // not winget: never
		{false, false, true, shortcutSkip},          // not winget, even with an entry
		{true, false, false, shortcutCreate},        // first winget start
		{true, false, true, shortcutRecordExisting}, // entry already there: keep it, remember
		{true, true, false, shortcutSkip},           // user deleted it: stays deleted
		{true, true, true, shortcutSkip},            // done
	}
	for _, c := range cases {
		if got := decideWingetShortcut(c.managed, c.done, c.exists); got != c.want {
			t.Errorf("decideWingetShortcut(%v,%v,%v) = %v, want %v", c.managed, c.done, c.exists, got, c.want)
		}
	}
}

func TestStartMenuShortcut(t *testing.T) {
	if got := startMenuShortcutPath(""); got != "" {
		t.Errorf("no APPDATA should give no path, got %q", got)
	}
	want := filepath.Join("AD", "Microsoft", "Windows", "Start Menu", "Programs", "ST Reborn.lnk")
	if got := startMenuShortcutPath("AD"); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	s := startMenuShortcutScript(`C:\x\ST Reborn.lnk`, `C:\pkg\o'k\STR-Windows.exe`)
	for _, w := range []string{"WScript.Shell", `$l.TargetPath='C:\pkg\o''k\STR-Windows.exe'`, `$l.IconLocation='C:\pkg\o''k\STR-Windows.exe,0'`, "$l.Save()"} {
		if !strings.Contains(s, w) {
			t.Errorf("shortcut script lacks %q:\n%s", w, s)
		}
	}
}

func TestReadWingetFailMarker(t *testing.T) {
	p := filepath.Join(t.TempDir(), wingetFailMarkerName)
	if _, ok := readWingetFailMarker(p); ok {
		t.Fatal("missing marker reported as present")
	}
	if err := os.WriteFile(p, []byte("-1978335189\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, ok := readWingetFailMarker(p)
	if !ok || code != "-1978335189" {
		t.Errorf("got %q %v", code, ok)
	}
	if _, ok := readWingetFailMarker(p); ok {
		t.Error("marker reported twice")
	}
}

// Off Windows there is no winget install, so the download and install steps
// stay exactly as they were.
func TestWingetNeverManagedOffWindows(t *testing.T) {
	if _, ok := wingetManagedExe(); ok && filepath.Separator != '\\' {
		t.Error("a non-Windows build reported a winget install")
	}
}
