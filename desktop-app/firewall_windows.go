//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// firewallSupported: the firewall check and unblock exist on Windows only.
const firewallSupported = true

// powershellExe is Windows PowerShell by its full path, so a powershell.exe
// planted earlier on PATH is never the one that runs, least of all elevated.
func powershellExe() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}

// runFirewallQuery runs the read-only query script in a hidden PowerShell, with
// no console window flashing up (HideWindow plus CREATE_NO_WINDOW).
func runFirewallQuery(ctx context.Context, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, powershellExe(), "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-EncodedCommand", encodePowerShellCommand(script))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			msg := string(exitErr.Stderr)
			if len(msg) > 300 {
				msg = msg[:300]
			}
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
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

// runElevatedPowerShell starts Windows PowerShell with the "runas" verb, which
// raises the UAC prompt, runs script in it hidden, and waits for it to finish.
// A declined prompt returns errFirewallDeclined.
func runElevatedPowerShell(ctx context.Context, script string) (int, error) {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(powershellExe())
	if err != nil {
		return -1, err
	}
	params, err := windows.UTF16PtrFromString("-NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -EncodedCommand " +
		encodePowerShellCommand(script))
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
		return -1, fmt.Errorf("start elevated PowerShell: %w", callErr)
	}
	if info.hProcess == 0 {
		return -1, errors.New("start elevated PowerShell: no process handle")
	}
	defer func() { _ = windows.CloseHandle(info.hProcess) }()
	for {
		ev, werr := windows.WaitForSingleObject(info.hProcess, 500)
		if werr != nil {
			return -1, fmt.Errorf("wait for elevated PowerShell: %w", werr)
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
