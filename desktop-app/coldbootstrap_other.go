// Non-Windows stub for the cold-bootstrap helpers. macOS and Linux
// each need their own implementation (airport/networksetup on
// macOS, nmcli/wpa_cli on Linux) before this feature works on those
// platforms. Until then the App methods are exposed but always
// return "not implemented on this platform" so the Wails-generated
// frontend bindings still compile and the UI can render a sensible
// error.

//go:build !windows

package main

import "fmt"

type SetupAP struct {
	SSID      string `json:"ssid"`
	Interface string `json:"interface"`
	Signal    string `json:"signal"`
}

type BootstrapResult struct {
	Step    string `json:"step"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	BoxIP   string `json:"boxIP"`
}

func (a *App) ScanForSetupAPs() ([]SetupAP, error) {
	return nil, fmt.Errorf("cold-bootstrap is currently only implemented on Windows")
}

func (a *App) BootstrapBoxOnSetupAP(setupSSID, homeSSID, homePassphrase, securityType string) (BootstrapResult, error) {
	return BootstrapResult{Step: "unsupported", OK: false, Message: "cold-bootstrap is currently only implemented on Windows"},
		fmt.Errorf("cold-bootstrap is currently only implemented on Windows")
}
