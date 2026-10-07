//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
)

// firewallSupported: the firewall check and unblock exist on Windows only.
const firewallSupported = true

// Both the check and the unblock talk to Windows Firewall through its own COM
// interface (HNetCfg.FwPolicy2), in this process. v1.0.7 ran them as hidden
// PowerShell scripts started with -ExecutionPolicy Bypass -EncodedCommand, and
// that command line is exactly what Norton and Avast flag as IDP.HELU.PSE92:
// they quarantined powershell.exe or the app itself on every start. No script
// host is involved any more; the elevated step is this same executable started
// again with the "runas" verb (see firewallHelperMain).

// Values of the NET_FW_* enums the COM interface uses.
const (
	netFwRuleDirIn      = 1
	netFwActionBlock    = 0
	netFwActionAllow    = 1
	netFwIPProtocolTCP  = 6
	netFwIPProtocolUDP  = 17
	netFwProfileDomain  = 0x1
	netFwProfilePrivate = 0x2
	netFwProfilePublic  = 0x4
)

// withFwPolicy runs fn with an HNetCfg.FwPolicy2 object on a COM-initialised,
// locked OS thread. COM objects belong to the thread that created them, so
// everything happens inside fn.
func withFwPolicy(fn func(policy *ole.IDispatch) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		// S_FALSE: COM was already initialised on this thread, which still
		// has to be balanced by CoUninitialize.
		var oe *ole.OleError
		if !errors.As(err, &oe) || oe.Code() != 1 {
			return fmt.Errorf("initialise COM: %w", err)
		}
	}
	defer ole.CoUninitialize()
	unk, err := oleutil.CreateObject("HNetCfg.FwPolicy2")
	if err != nil {
		return fmt.Errorf("open the Windows Firewall policy: %w", err)
	}
	defer unk.Release()
	policy, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return fmt.Errorf("open the Windows Firewall policy: %w", err)
	}
	defer policy.Release()
	return fn(policy)
}

func fwString(d *ole.IDispatch, name string) string {
	v, err := oleutil.GetProperty(d, name)
	if err != nil {
		return ""
	}
	defer func() { _ = v.Clear() }()
	return v.ToString()
}

func fwInt(d *ole.IDispatch, name string) (int64, bool) {
	v, err := oleutil.GetProperty(d, name)
	if err != nil {
		return 0, false
	}
	defer func() { _ = v.Clear() }()
	switch x := v.Value().(type) {
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint32:
		return int64(x), true
	case int16:
		return int64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// forEachFwRule calls fn for every rule in the policy's rule collection.
func forEachFwRule(policy *ole.IDispatch, fn func(rule *ole.IDispatch) error) error {
	rv, err := oleutil.GetProperty(policy, "Rules")
	if err != nil {
		return fmt.Errorf("read the firewall rules: %w", err)
	}
	defer func() { _ = rv.Clear() }()
	return oleutil.ForEach(rv.ToIDispatch(), func(item *ole.VARIANT) error {
		defer func() { _ = item.Clear() }()
		rule := item.ToIDispatch()
		if rule == nil {
			return nil
		}
		return fn(rule)
	})
}

// queryFirewall reads the enabled inbound block rules that name a program and
// the categories of the networks the PC is on. It only reads, so it needs no
// elevation and cannot prompt.
func queryFirewall(ctx context.Context) ([]fwRule, []string, error) {
	type result struct {
		rules    []fwRule
		profiles []string
		err      error
	}
	ch := make(chan result, 1)
	go func() {
		var r result
		r.err = withFwPolicy(func(policy *ole.IDispatch) error {
			if mask, ok := fwInt(policy, "CurrentProfileTypes"); ok {
				r.profiles = fwActiveProfiles(mask)
			}
			return forEachFwRule(policy, func(rule *ole.IDispatch) error {
				if dir, _ := fwInt(rule, "Direction"); dir != netFwRuleDirIn {
					return nil
				}
				if act, _ := fwInt(rule, "Action"); act != netFwActionBlock {
					return nil
				}
				if en, _ := fwInt(rule, "Enabled"); en == 0 {
					return nil
				}
				prog := fwString(rule, "ApplicationName")
				if prog == "" {
					return nil
				}
				mask, _ := fwInt(rule, "Profiles")
				name := fwString(rule, "Name")
				r.rules = append(r.rules, fwRule{
					Name: name, DisplayName: name, Enabled: "True", Direction: "Inbound",
					Action: "Block", Profile: fwProfileString(mask), Program: prog,
				})
				return nil
			})
		})
		ch <- r
	}()
	select {
	case r := <-ch:
		return r.rules, r.profiles, r.err
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// applyFirewallUnblock is the elevated step itself: it removes every inbound
// block rule naming one of programs, replaces the app's own earlier allow
// rules, and adds inbound allow rules for TCP and UDP.
func applyFirewallUnblock(programs []string, includePublic bool) error {
	want := map[string]bool{}
	for _, p := range programs {
		want[normalizeWinPath(p)] = true
	}
	isOurs := func(p string) bool { return want[normalizeWinPath(expandWinEnv(p, os.LookupEnv))] }
	profiles := int64(netFwProfileDomain | netFwProfilePrivate)
	if includePublic {
		profiles |= netFwProfilePublic
	}
	return withFwPolicy(func(policy *ole.IDispatch) error {
		// INetFwRules.Remove works by name, and the rules Windows writes for a
		// cancelled prompt share one name with each other and possibly with
		// rules of other programs. Each rule to go is renamed to a unique
		// name first, so Remove can only ever hit that rule.
		var doomed []string
		stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
		err := forEachFwRule(policy, func(rule *ole.IDispatch) error {
			if dir, _ := fwInt(rule, "Direction"); dir != netFwRuleDirIn {
				return nil
			}
			if !isOurs(fwString(rule, "ApplicationName")) {
				return nil
			}
			act, _ := fwInt(rule, "Action")
			if act != netFwActionBlock && fwString(rule, "Grouping") != firewallRuleGroup {
				return nil
			}
			tmp := fmt.Sprintf("ST Reborn remove %s-%d", stamp, len(doomed))
			if _, err := oleutil.PutProperty(rule, "Name", tmp); err != nil {
				return fmt.Errorf("mark rule %q for removal: %w", fwString(rule, "Name"), err)
			}
			doomed = append(doomed, tmp)
			return nil
		})
		if err != nil {
			return err
		}
		rv, err := oleutil.GetProperty(policy, "Rules")
		if err != nil {
			return fmt.Errorf("read the firewall rules: %w", err)
		}
		defer func() { _ = rv.Clear() }()
		rules := rv.ToIDispatch()
		for _, n := range doomed {
			if _, err := oleutil.CallMethod(rules, "Remove", n); err != nil {
				return fmt.Errorf("remove a blocking rule: %w", err)
			}
		}
		for _, p := range programs {
			for _, proto := range []struct {
				name string
				num  int32
			}{{"TCP", netFwIPProtocolTCP}, {"UDP", netFwIPProtocolUDP}} {
				if err := addFwAllowRule(rules, p, proto.name, proto.num, int32(profiles)); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func addFwAllowRule(rules *ole.IDispatch, program, protoName string, proto, profiles int32) error {
	unk, err := oleutil.CreateObject("HNetCfg.FWRule")
	if err != nil {
		return fmt.Errorf("create a firewall rule: %w", err)
	}
	defer unk.Release()
	rule, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return fmt.Errorf("create a firewall rule: %w", err)
	}
	defer rule.Release()
	for _, kv := range []struct {
		k string
		v any
	}{
		{"Name", "ST Reborn (" + protoName + ")"},
		{"Description", "Lets ST Reborn find and control SoundTouch speakers on this network."},
		{"ApplicationName", program},
		{"Protocol", proto},
		{"Direction", int32(netFwRuleDirIn)},
		{"Action", int32(netFwActionAllow)},
		{"Profiles", profiles},
		{"Grouping", firewallRuleGroup},
		{"Enabled", true},
	} {
		if _, err := oleutil.PutProperty(rule, kv.k, kv.v); err != nil {
			return fmt.Errorf("set %s on the allow rule: %w", kv.k, err)
		}
	}
	if _, err := oleutil.CallMethod(rules, "Add", rule); err != nil {
		return fmt.Errorf("add the allow rule: %w", err)
	}
	return nil
}

// shellExecuteInfo is SHELLEXECUTEINFOW. golang.org/x/sys/windows only wraps
// plain ShellExecute, which neither hands back the process nor waits, and the
// app needs both to read the outcome of the elevated step.
type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           windows.Handle
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       windows.Handle
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      windows.Handle
	dwHotKey       uint32
	hIconOrMonitor windows.Handle
	hProcess       windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	seeMaskFlagNoUI       = 0x00000400
)

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// runElevatedFirewallHelper starts this executable again with the "runas"
// verb, which raises the UAC prompt, in its firewall helper mode (args from
// firewallHelperArgs), and waits for it to finish. A declined prompt returns
// errFirewallDeclined.
func runElevatedFirewallHelper(ctx context.Context, args []string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return -1, fmt.Errorf("locate the app: %w", err)
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = syscall.EscapeArg(a)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return -1, err
	}
	params, err := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	if err != nil {
		return -1, err
	}
	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        windows.SW_HIDE,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return -1, errFirewallDeclined
		}
		return -1, fmt.Errorf("start the elevated firewall step: %w", callErr)
	}
	if info.hProcess == 0 {
		return -1, errors.New("start the elevated firewall step: no process handle")
	}
	defer func() { _ = windows.CloseHandle(info.hProcess) }()
	for {
		ev, werr := windows.WaitForSingleObject(info.hProcess, 500)
		if werr != nil {
			return -1, fmt.Errorf("wait for the elevated firewall step: %w", werr)
		}
		if ev == windows.WAIT_OBJECT_0 {
			break
		}
		select {
		case <-ctx.Done():
			return -1, fmt.Errorf("the firewall step did not finish: %w", ctx.Err())
		default:
		}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return -1, fmt.Errorf("read the firewall step's exit code: %w", err)
	}
	return int(code), nil
}

// errFirewallUnsupported is never returned on Windows; it exists so the shared
// helper code compiles on every platform.
var errFirewallUnsupported = errors.New("the firewall check exists on Windows only")
