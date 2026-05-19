// In-app STR installer: runs install.sh on a stock Bose SoundTouch
// over SSH, reboots the box, and waits for the STR agent to come
// up on port 8888. Replaces the manual PowerShell wizard step for
// end users who only ever touch the desktop app.
//
// Auth: passwordless root. Bose's stock firmware ships /etc/shadow
// with an empty password hash for root and the default sshd config
// accepts it as long as the remote_services marker is present on
// /media/sda1 (which our stick provisioning writes). No key, no
// password, no UAC.

package main

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

// InstallResult is the JSON-serialisable outcome of an STR install
// attempt. The frontend uses Step to drive live progress and OK +
// Message for the final state.
type InstallResult struct {
	Step    string `json:"step"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Log     string `json:"log"`
}

// sshFlags are the OpenSSH options needed to talk to a Bose
// SoundTouch box (old ssh-rsa host key) without interactive prompts.
var sshFlags = []string{
	"-oHostKeyAlgorithms=+ssh-rsa",
	"-oPubkeyAcceptedAlgorithms=+ssh-rsa",
	"-oStrictHostKeyChecking=accept-new",
	"-oBatchMode=yes",
	"-oConnectTimeout=5",
}

// InstallSTROnBox runs the full install on a box that has a freshly
// provisioned STR stick mounted at /media/sda1. Steps:
//   1. verify install.sh is reachable on the stick
//   2. run "sh /media/sda1/install.sh install"
//   3. reboot the box
//   4. poll port 8888 for up to 150 s
//
// Caller passes the box's home-LAN IP. Returns a step-tagged result
// even on failure so the UI can show the user where it stopped.
func (a *App) InstallSTROnBox(host string) (InstallResult, error) {
	res := InstallResult{Step: "start"}
	if host == "" {
		return res, fmt.Errorf("host is required")
	}
	a.logger.Info("install_str: starting", "host", host)

	// Step 1: stick mounted, install.sh present. Retry up to ~60 s
	// because sshd answers before the USB stack has finished
	// mounting /media/sda1 on first boot.
	res.Step = "check-stick"
	var probe string
	var probeErr error
	for attempt := 0; attempt < 20; attempt++ {
		probe, probeErr = boxSSHOutput(host, "test -x /media/sda1/install.sh && echo READY || echo MISSING", 6*time.Second)
		if probeErr == nil && strings.Contains(probe, "READY") {
			break
		}
		if attempt == 19 {
			if probeErr != nil {
				return res, fmt.Errorf("ssh probe failed after retries: %w", probeErr)
			}
			res.Message = "install.sh did not appear on /media/sda1 within 60 s. Is the STR stick plugged into the speaker, and did the speaker mount it on this boot?"
			return res, nil
		}
		time.Sleep(3 * time.Second)
	}

	// Step 2: run install.sh install.
	res.Step = "run-install"
	out, err := boxSSHOutput(host, "sh /media/sda1/install.sh install 2>&1", 60*time.Second)
	res.Log = out
	if err != nil {
		return res, fmt.Errorf("install.sh failed: %w", err)
	}
	if strings.Contains(out, "FEHLER") || strings.Contains(out, "ERROR") {
		res.Message = "install.sh reported an error. See log."
		return res, nil
	}
	a.logger.Info("install_str: install.sh ran", "host", host, "outBytes", len(out))

	// Step 3: reboot. The ssh call ends with the box dropping the
	// connection, which manifests as a non-zero exit — that is the
	// expected success path here, not an error.
	res.Step = "reboot"
	_ = boxSSHFireAndForget(host, "(sleep 1; reboot) &", 4*time.Second)
	a.logger.Info("install_str: reboot signal sent", "host", host)

	// Step 4: poll port 8888 (the STR agent webui) for up to 150 s.
	res.Step = "wait-agent"
	if err := waitForTCPPort(host, 8888, 150*time.Second); err != nil {
		res.Message = "Speaker did not bring up the STR agent on port 8888 within 150 s. It may still be rebooting. Try refreshing the speaker list in a minute."
		return res, nil
	}
	res.Step = "done"
	res.OK = true
	res.Message = "STR agent is up on port 8888."
	return res, nil
}

// boxSSHOutput runs cmd on the box over SSH and returns combined
// stdout+stderr. timeout caps the whole call.
func boxSSHOutput(host, cmd string, timeout time.Duration) (string, error) {
	args := append(append([]string{}, sshFlags...), "root@"+host, cmd)
	c := exec.Command("ssh", args...)
	// CombinedOutput catches both streams which is what we want for
	// install.sh log capture.
	done := make(chan struct {
		out []byte
		err error
	}, 1)
	go func() {
		out, err := c.CombinedOutput()
		done <- struct {
			out []byte
			err error
		}{out, err}
	}()
	select {
	case r := <-done:
		return string(r.out), r.err
	case <-time.After(timeout):
		_ = c.Process.Kill()
		return "", fmt.Errorf("ssh timeout after %s", timeout)
	}
}

// boxSSHFireAndForget runs cmd but does not require it to exit
// cleanly. Used for "reboot" where the connection dropping is
// expected.
func boxSSHFireAndForget(host, cmd string, timeout time.Duration) error {
	args := append(append([]string{}, sshFlags...), "root@"+host, cmd)
	c := exec.Command("ssh", args...)
	done := make(chan error, 1)
	go func() { done <- c.Run() }()
	select {
	case <-done:
		return nil // ignore exit code, reboot is expected to disconnect
	case <-time.After(timeout):
		_ = c.Process.Kill()
		return nil
	}
}

// waitForTCPPort polls host:port until either it accepts a TCP
// connection or the timeout elapses.
func waitForTCPPort(host string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("%s:%d", host, port)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 1500*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("port %d on %s not reachable within %s", port, host, timeout)
}
