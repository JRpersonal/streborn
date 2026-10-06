package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// Windows Firewall block detection and the one-click unblock.
//
// The first time ST Reborn opens a listening socket (mDNS answers on UDP 5353,
// SSDP, the factory-reset bootstrap's marge callback), Windows shows "Windows
// Defender Firewall has blocked some features of this app". Pressing Cancel
// there, or unticking the network type the PC is on, does not leave the app in
// an undecided state: Windows silently writes inbound BLOCK rules for the
// program's path, and because a rule for that path now exists it never asks
// again. From then on the speaker list stays empty and installs wait forever
// for a callback, with nothing on screen to say why.
//
// There is no API to bring the original prompt back. Windows only shows it
// while no rule names the program, and deleting the block rules needs the same
// elevation as writing allow rules. So the app does the whole step itself, in
// one UAC prompt: remove this program's inbound block rules and add inbound
// allow rules for it. Detection, by contrast, only reads the rule list, which
// needs no elevation and cannot prompt.

// FirewallCheck is what the app knows about the Windows Firewall rules for its
// own program. On macOS and Linux Supported is false and nothing else is set.
type FirewallCheck struct {
	// Supported is true on Windows, the only platform with a check.
	Supported bool `json:"supported"`
	// Checked is true once a check finished. A check that could not read the
	// rules leaves Blocked false and says why in Error: an unreadable rule list
	// is never reported as a block.
	Checked bool `json:"checked"`
	// Blocked is true when at least one enabled inbound block rule names this
	// program on a network profile that is active right now.
	Blocked bool `json:"blocked"`
	// BlockRules names the rules behind Blocked, for the log and the
	// diagnostic report.
	BlockRules []string `json:"blockRules,omitempty"`
	// PublicNetwork is true when a network the PC is connected to is marked
	// Public in Windows. The unblock then has to cover the Public profile too,
	// which the UI asks about instead of doing silently.
	PublicNetwork bool   `json:"publicNetwork"`
	Error         string `json:"error,omitempty"`
}

// FirewallUnblockResult is the outcome of AllowThroughFirewall.
type FirewallUnblockResult struct {
	// OK is true when the elevated step ran and the re-check finds no block.
	OK bool `json:"ok"`
	// Declined is true when the user said No to the Windows UAC prompt.
	Declined bool   `json:"declined"`
	Error    string `json:"error,omitempty"`
	// Status is the re-check after the step, so the UI can redraw at once.
	Status FirewallCheck `json:"status"`
}

// errFirewallDeclined is returned by runElevatedPowerShell when the user
// declines the UAC prompt.
var errFirewallDeclined = errors.New("the administrator prompt was declined")

// firewallRecheckMin is the shortest gap between two forced re-checks. Every
// check starts a PowerShell, and the frontend asks again when its indirect
// signals fire (an empty speaker list, an install waiting for a callback);
// this keeps a burst of those from turning into a burst of subprocesses.
const firewallRecheckMin = 60 * time.Second

// firewallRuleGroup groups the allow rules the app writes, so a second unblock
// replaces them instead of piling up duplicates, and so a user can find them
// in the firewall settings under a recognisable name.
const firewallRuleGroup = "ST Reborn"

// firewallState is the cached result of the last check. mu is held for the
// whole check, so a caller arriving while one runs waits for that result
// instead of starting a second PowerShell.
type firewallState struct {
	mu   sync.Mutex
	last *FirewallCheck
	at   time.Time
}

// CheckFirewall reports whether Windows Firewall blocks this app. It answers
// from the cache unless force is set, and even a forced check reuses a result
// younger than firewallRecheckMin. The app checks once at start-up; after that
// only the UI asks, on a user action or once when an indirect signal fires.
// Never called in a loop.
func (a *App) CheckFirewall(force bool) FirewallCheck {
	if !firewallSupported {
		return FirewallCheck{}
	}
	return a.checkFirewall(force, false)
}

func (a *App) checkFirewall(force, bypassMin bool) FirewallCheck {
	a.firewall.mu.Lock()
	defer a.firewall.mu.Unlock()
	if a.firewall.last != nil {
		age := time.Since(a.firewall.at)
		if !force || (!bypassMin && age < firewallRecheckMin) {
			return *a.firewall.last
		}
	}
	c := a.runFirewallCheck()
	a.firewall.last = &c
	a.firewall.at = time.Now()
	return c
}

func (a *App) runFirewallCheck() FirewallCheck {
	c := FirewallCheck{Supported: true}
	ctx, cancel := context.WithTimeout(a.appCtx(), 30*time.Second)
	defer cancel()
	started := time.Now()
	out, err := runFirewallQuery(ctx, firewallQueryScript)
	if err != nil {
		c.Error = err.Error()
		a.logger.Info("firewall: could not read the Windows Firewall rules", "err", err, "took", time.Since(started).Round(time.Millisecond))
		return c
	}
	rules, profiles, err := parseFirewallQuery(out)
	if err != nil {
		c.Error = err.Error()
		a.logger.Info("firewall: unreadable rule list", "err", err)
		return c
	}
	c = evaluateFirewall(rules, profiles, firewallPrograms(), os.LookupEnv)
	a.logger.Info("firewall: checked the Windows Firewall rules for ST Reborn",
		"blocked", c.Blocked, "blockRules", strings.Join(c.BlockRules, "; "),
		"activeProfiles", strings.Join(profiles, ","), "inboundBlockRulesTotal", len(rules),
		"took", time.Since(started).Round(time.Millisecond))
	return c
}

// AllowThroughFirewall removes this program's inbound block rules and adds
// inbound allow rules for it (TCP and UDP) in one elevated step, then checks
// again. The allow rules cover the Private and Domain profiles; includePublic
// adds Public, which the UI only offers when the PC is on a network Windows
// marks Public. See the comment at the top of this file for why the app does
// this itself rather than bringing back the Windows prompt.
func (a *App) AllowThroughFirewall(includePublic bool) FirewallUnblockResult {
	if !firewallSupported {
		return FirewallUnblockResult{Error: "not supported on this platform"}
	}
	programs := firewallPrograms()
	if len(programs) == 0 {
		return FirewallUnblockResult{Error: "could not determine the app's own path"}
	}
	resFile, err := os.CreateTemp("", "str-firewall-*.txt")
	if err != nil {
		return FirewallUnblockResult{Error: err.Error()}
	}
	resPath := resFile.Name()
	_ = resFile.Close()
	defer func() { _ = os.Remove(resPath) }()

	script := firewallUnblockScript(programs, includePublic, resPath)
	ctx, cancel := context.WithTimeout(a.appCtx(), 3*time.Minute)
	defer cancel()
	a.logger.Info("firewall: asking for administrator rights to allow ST Reborn through the Windows Firewall", "includePublic", includePublic)
	exitCode, runErr := runElevatedPowerShell(ctx, script)
	res := FirewallUnblockResult{}
	switch {
	case errors.Is(runErr, errFirewallDeclined):
		res.Declined = true
		a.logger.Info("firewall: the user declined the administrator prompt")
	case runErr != nil:
		res.Error = runErr.Error()
		a.logger.Warn("firewall: the elevated step could not run", "err", runErr)
	default:
		msg, _ := os.ReadFile(resPath)
		outcome := strings.TrimSpace(strings.TrimPrefix(string(msg), "\ufeff"))
		if exitCode != 0 || outcome != "ok" {
			res.Error = firewallStepError(exitCode, outcome)
			a.logger.Warn("firewall: the elevated step failed", "exitCode", exitCode, "outcome", outcome)
		} else {
			a.logger.Info("firewall: the elevated step finished")
		}
	}
	res.Status = a.checkFirewall(true, true)
	res.OK = !res.Declined && res.Error == "" && res.Status.Checked && !res.Status.Blocked
	if res.Error == "" && !res.Declined && res.Status.Blocked {
		// The step ran, yet a block remains: typically a rule that comes from a
		// group policy, which a local change cannot remove.
		res.Error = "a firewall rule still blocks ST Reborn (it may be set by your organisation's policy)"
	}
	return res
}

func firewallStepError(exitCode int, outcome string) string {
	if strings.HasPrefix(outcome, "error:") {
		return strings.TrimSpace(strings.TrimPrefix(outcome, "error:"))
	}
	return fmt.Sprintf("the firewall step ended with exit code %d", exitCode)
}

// firewallPrograms is the app's own executable path, plus its symlink-resolved
// form when that differs (a winget install starts the app through a link).
// Windows matches a program rule against the image path that actually runs.
func firewallPrograms() []string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return nil
	}
	out := []string{exe}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil && resolved != "" &&
		normalizeWinPath(resolved) != normalizeWinPath(exe) {
		out = append(out, resolved)
	}
	return out
}

// firewallQueryScript lists the enabled inbound block rules with the program
// each one names, and the categories of the networks the PC is on. Enum values
// are turned into strings on purpose: ConvertTo-Json would otherwise write
// them as numbers, and the enum NAMES are the same on every Windows language,
// unlike the localised text netsh prints. It reads only, so it needs no
// elevation. Program filtering happens in Go (evaluateFirewall), so no path
// has to be quoted into the script. Output is forced to UTF-8: Windows
// PowerShell otherwise writes a redirected stdout in the OEM code page, which
// would garble a program path under a user folder like C:\Users\Jürgen.
const firewallQueryScript = `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$rules = @(Get-NetFirewallRule -Direction Inbound -Action Block -Enabled True -ErrorAction SilentlyContinue | ForEach-Object {
  $f = $_ | Get-NetFirewallApplicationFilter
  [pscustomobject]@{
    Name = [string]$_.Name
    DisplayName = [string]$_.DisplayName
    Enabled = [string]$_.Enabled
    Direction = [string]$_.Direction
    Action = [string]$_.Action
    Profile = [string]$_.Profile
    Program = [string]$f.Program
  }
} | Where-Object { $_.Program -and $_.Program -ne 'Any' })
$profiles = @(Get-NetConnectionProfile -ErrorAction SilentlyContinue | ForEach-Object { [string]$_.NetworkCategory })
ConvertTo-Json -Compress -Depth 3 -InputObject @{ rules = $rules; profiles = $profiles }`

// fwRule is one rule row from firewallQueryScript.
type fwRule struct {
	Name        string `json:"Name"`
	DisplayName string `json:"DisplayName"`
	Enabled     string `json:"Enabled"`
	Direction   string `json:"Direction"`
	Action      string `json:"Action"`
	Profile     string `json:"Profile"`
	Program     string `json:"Program"`
}

// parseFirewallQuery reads firewallQueryScript's JSON. It tolerates the shapes
// Windows PowerShell 5.1 produces: a byte order mark, a lone object where a
// one-element array was meant, and null for an empty list.
func parseFirewallQuery(out []byte) ([]fwRule, []string, error) {
	out = bytes.TrimSpace(bytes.TrimPrefix(out, []byte("\xef\xbb\xbf")))
	if len(out) == 0 {
		return nil, nil, errors.New("the firewall query returned nothing")
	}
	var top struct {
		Rules    json.RawMessage `json:"rules"`
		Profiles json.RawMessage `json:"profiles"`
	}
	if err := json.Unmarshal(out, &top); err != nil {
		return nil, nil, fmt.Errorf("parse firewall query: %w", err)
	}
	var rules []fwRule
	if err := unmarshalOneOrMany(top.Rules, &rules); err != nil {
		return nil, nil, fmt.Errorf("parse firewall rules: %w", err)
	}
	var profiles []string
	if err := unmarshalOneOrMany(top.Profiles, &profiles); err != nil {
		return nil, nil, fmt.Errorf("parse network profiles: %w", err)
	}
	return rules, profiles, nil
}

// unmarshalOneOrMany decodes raw into *dst, which must be a slice pointer,
// accepting a JSON array, a single element, or null/absent.
func unmarshalOneOrMany[T any](raw json.RawMessage, dst *[]T) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		*dst = nil
		return nil
	}
	if raw[0] == '[' {
		return json.Unmarshal(raw, dst)
	}
	var one T
	if err := json.Unmarshal(raw, &one); err != nil {
		return err
	}
	*dst = []T{one}
	return nil
}

// evaluateFirewall decides whether rules block one of programs on a network
// profile that is active now. lookupEnv expands %VARIABLE% paths in the rule,
// which Windows allows in a program rule.
func evaluateFirewall(rules []fwRule, activeProfiles, programs []string, lookupEnv func(string) (string, bool)) FirewallCheck {
	c := FirewallCheck{Supported: true, Checked: true}
	for _, p := range activeProfiles {
		if strings.EqualFold(strings.TrimSpace(p), "Public") {
			c.PublicNetwork = true
		}
	}
	want := map[string]bool{}
	for _, p := range programs {
		if n := normalizeWinPath(p); n != "" {
			want[n] = true
		}
	}
	for _, r := range rules {
		if !strings.EqualFold(r.Enabled, "True") ||
			!strings.EqualFold(r.Direction, "Inbound") ||
			!strings.EqualFold(r.Action, "Block") {
			continue
		}
		if !want[normalizeWinPath(expandWinEnv(r.Program, lookupEnv))] {
			continue
		}
		if !ruleProfileActive(r.Profile, activeProfiles) {
			continue
		}
		c.Blocked = true
		name := r.DisplayName
		if name == "" {
			name = r.Name
		}
		c.BlockRules = append(c.BlockRules, fmt.Sprintf("%s [%s]", name, r.Profile))
	}
	return c
}

// ruleProfileActive reports whether a rule whose Profile is profile (as
// PowerShell prints the flags: "Any", "Private", "Domain, Public", ...) applies
// to one of the active network categories ("Public", "Private",
// "DomainAuthenticated"). With no known active network the rule is counted, so
// a failed profile read cannot hide a block.
func ruleProfileActive(profile string, active []string) bool {
	profile = strings.TrimSpace(profile)
	if profile == "" || strings.EqualFold(profile, "Any") || len(active) == 0 {
		return true
	}
	ruleSet := map[string]bool{}
	for _, p := range strings.Split(profile, ",") {
		ruleSet[strings.ToLower(strings.TrimSpace(p))] = true
	}
	for _, a := range active {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "domainauthenticated" {
			a = "domain"
		}
		if ruleSet[a] {
			return true
		}
	}
	return false
}

// normalizeWinPath makes two Windows paths comparable: case folded, forward
// slashes turned into backslashes, quotes and the \\?\ prefix dropped.
func normalizeWinPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"`)
	p = strings.ReplaceAll(p, "/", `\`)
	p = strings.TrimPrefix(p, `\\?\`)
	return strings.ToLower(p)
}

// expandWinEnv replaces %NAME% references using lookup, leaving unknown ones
// as they are.
func expandWinEnv(s string, lookup func(string) (string, bool)) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '%')
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.IndexByte(s[i+1:], '%')
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		name := s[i+1 : i+1+j]
		b.WriteString(s[:i])
		if v, ok := lookup(name); ok && name != "" {
			b.WriteString(v)
		} else {
			b.WriteString(s[i : i+j+2])
		}
		s = s[i+j+2:]
	}
}

// firewallUnblockScript is the PowerShell that runs elevated. It removes every
// inbound block rule naming one of programs, replaces the app's own earlier
// allow rules, and adds inbound allow rules for TCP and UDP. The outcome goes
// to resultPath ("ok" or "error: <message>"), since an elevated process has no
// output the app could read.
func firewallUnblockScript(programs []string, includePublic bool, resultPath string) string {
	quoted := make([]string, 0, len(programs))
	for _, p := range programs {
		quoted = append(quoted, psQuote(p))
	}
	profiles := "Private,Domain"
	if includePublic {
		profiles = "Private,Domain,Public"
	}
	return `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$out = ` + psQuote(resultPath) + `
try {
  $progs = @(` + strings.Join(quoted, ", ") + `)
  $isOurs = {
    param($p)
    $x = [Environment]::ExpandEnvironmentVariables([string]$p)
    foreach ($q in $progs) { if ($x -ieq $q) { return $true } }
    return $false
  }
  Get-NetFirewallRule -Direction Inbound -Action Block -ErrorAction SilentlyContinue | ForEach-Object {
    if (& $isOurs ($_ | Get-NetFirewallApplicationFilter).Program) { $_ | Remove-NetFirewallRule }
  }
  Get-NetFirewallRule -Group ` + psQuote(firewallRuleGroup) + ` -ErrorAction SilentlyContinue | ForEach-Object {
    if (& $isOurs ($_ | Get-NetFirewallApplicationFilter).Program) { $_ | Remove-NetFirewallRule }
  }
  foreach ($p in $progs) {
    foreach ($proto in 'TCP', 'UDP') {
      New-NetFirewallRule -DisplayName ("ST Reborn (" + $proto + ")") -Group ` + psQuote(firewallRuleGroup) + ` -Description 'Lets ST Reborn find and control SoundTouch speakers on this network.' -Direction Inbound -Action Allow -Program $p -Protocol $proto -Profile ` + profiles + ` | Out-Null
    }
  }
  Set-Content -LiteralPath $out -Value 'ok' -Encoding UTF8
  exit 0
} catch {
  try { Set-Content -LiteralPath $out -Value ('error: ' + $_.Exception.Message) -Encoding UTF8 } catch {}
  exit 1
}`
}

// encodePowerShellCommand encodes script for powershell.exe -EncodedCommand:
// base64 of its UTF-16LE bytes. Encoding sidesteps every quoting rule of the
// command line the elevated process is started with.
func encodePowerShellCommand(script string) string {
	u := utf16.Encode([]rune(script))
	buf := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(buf[2*i:], c)
	}
	return base64.StdEncoding.EncodeToString(buf)
}
