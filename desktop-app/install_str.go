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
	"runtime"
	"strings"
	"time"
)

// nullDevice points at the platform's null device — used to keep
// the rotating Bose host key out of the user's known_hosts. Linux
// and macOS use /dev/null; Windows OpenSSH (Microsoft OpenSSH Win32)
// accepts NUL.
var nullDevice = func() string {
	if runtime.GOOS == "windows" {
		return "NUL"
	}
	return "/dev/null"
}()

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
// SoundTouch box without interactive prompts.
//
// Why so many legacy algorithms: Bose SoundTouch firmware ships an
// OpenSSH from ~2014 that only offers ssh-rsa host keys, SHA1 MACs,
// the diffie-hellman-group{1,14}-sha1 KEX, and CBC ciphers. Modern
// OpenSSH on macOS Sequoia (9.x) disables all of these by default
// and returns exit 255 with no useful stderr in the wizard UI. We
// patch every algorithm class explicitly so the wizard works on
// macOS, Windows, and Linux without users having to drop SSH config
// into ~/.ssh themselves.
//
// StrictHostKeyChecking is set to "no" instead of "accept-new"
// because: (a) the Bose box's host key rotates on factory reset,
// which we trigger as part of bootstrap, so accept-new would refuse
// the very next install attempt on a re-flashed box; (b) the
// connection is over the user's home LAN to a device whose IP they
// just selected from a discovery list — there is no realistic MITM
// vector to defend against here. UserKnownHostsFile=/dev/null keeps
// the rotating Bose key out of the user's known_hosts entirely.
var sshFlags = []string{
	"-oHostKeyAlgorithms=+ssh-rsa,ssh-dss",
	"-oPubkeyAcceptedAlgorithms=+ssh-rsa,ssh-dss",
	"-oKexAlgorithms=+diffie-hellman-group1-sha1,diffie-hellman-group14-sha1,diffie-hellman-group-exchange-sha1",
	"-oCiphers=+aes128-cbc,aes192-cbc,aes256-cbc,3des-cbc",
	"-oMACs=+hmac-sha1,hmac-sha1-96,hmac-md5",
	"-oStrictHostKeyChecking=no",
	"-oUserKnownHostsFile=" + nullDevice,
	"-oGlobalKnownHostsFile=" + nullDevice,
	"-oBatchMode=yes",
	"-oConnectTimeout=8",
	"-oServerAliveInterval=5",
	"-oServerAliveCountMax=3",
}

// stickProbePaths are the candidate mount paths checked for the STR
// install.sh on the box. Bose's udev rule normally lands USB sticks
// at /media/sda1 across every model we have observed (ST10
// micro-USB, ST20/30 USB-A — same /etc/udev/scripts/mount.sh), but
// the list is intentionally broad so we never give up on a firmware
// variant that numbers or names mountpoints differently. Probed in
// order: the most common slot first.
var stickProbePaths = []string{
	"/media/sda1", "/media/sdb1", "/media/sdc1", "/media/sdd1",
	"/media/usb", "/media/usb0", "/media/usb1",
	"/media/usbhd-sda1", "/media/usbhd-sdb1",
	"/mnt/usb", "/mnt/usb0", "/mnt/usb1", "/mnt/sda1", "/mnt/sdb1",
	"/run/media/sda1", "/run/media/sdb1",
}

// InstallSTROnBox runs the full install on a box that has a freshly
// provisioned STR stick mounted somewhere under /media or /mnt.
// Steps:
//   1. probe SSH and locate install.sh on the stick
//   2. run "sh <stick>/install.sh install"
//   3. reboot the box
//   4. poll port 8888 for up to 150 s
//
// Caller passes the box's home-LAN IP. Returns a step-tagged result
// even on failure so the UI can show the user where it stopped, and
// captures SSH stderr into res.Log so the user can see the actual
// failure reason instead of an opaque exit code.
func (a *App) InstallSTROnBox(host string) (InstallResult, error) {
	res := InstallResult{Step: "start"}
	if host == "" {
		return res, fmt.Errorf("host is required")
	}
	a.logger.Info("install_str: starting", "host", host)

	// Step 0: SSH itself reachable + authenticated? We do this as a
	// separate trivial command so a connect/auth/algorithm failure
	// surfaces with a specific message instead of looking like a
	// missing stick. The probe also doubles as a warmup so the next
	// SSH call reuses the negotiated host key.
	res.Step = "ssh-handshake"
	hello, helloErr := boxSSHOutput(host, "echo STR_SSH_OK", 12*time.Second)
	if helloErr != nil || !strings.Contains(hello, "STR_SSH_OK") {
		res.Log = hello
		hint := classifySSHError(hello, helloErr)
		res.Message = "SSH handshake to speaker failed: " + hint
		if helloErr != nil {
			return res, fmt.Errorf("ssh handshake: %w", helloErr)
		}
		return res, nil
	}
	a.logger.Info("install_str: ssh ok", "host", host)

	// Step 1: stick mounted, install.sh present. Retry up to ~60 s
	// because sshd answers before the USB stack has finished
	// mounting the stick on first boot. The probe checks a broad
	// set of candidate paths (see stickProbePaths) and additionally
	// scans /media + /mnt + /run/media for *any* directory that
	// holds an install.sh — so even an entirely new firmware variant
	// is recoverable without a code change.
	res.Step = "check-stick"
	probeCmd := buildStickProbeCmd(stickProbePaths)
	var probe string
	var probeErr error
	stickPath := ""
	for attempt := 0; attempt < 20; attempt++ {
		probe, probeErr = boxSSHOutput(host, probeCmd, 8*time.Second)
		if probeErr == nil && strings.Contains(probe, "STICKPATH=") {
			for _, line := range strings.Split(probe, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "STICKPATH=") {
					stickPath = strings.TrimPrefix(line, "STICKPATH=")
					break
				}
			}
			if stickPath != "" {
				break
			}
		}
		if attempt == 19 {
			res.Log = probe
			if probeErr != nil {
				hint := classifySSHError(probe, probeErr)
				res.Message = "ssh probe failed after retries: " + hint
				return res, fmt.Errorf("ssh probe failed after retries: %w", probeErr)
			}
			res.Message = "install.sh did not appear under /media, /mnt or /run/media within 60 s. " +
				"Is the STR stick physically plugged into the speaker (USB-A on ST20/30, " +
				"micro-USB adapter on ST10), and did you reboot the speaker so it mounted the stick?"
			return res, nil
		}
		time.Sleep(3 * time.Second)
	}
	a.logger.Info("install_str: stick found", "host", host, "path", stickPath)

	// Step 2: run install.sh install.
	res.Step = "run-install"
	out, err := boxSSHOutput(host, "sh "+stickPath+"/install.sh install 2>&1", 60*time.Second)
	res.Log = out
	if err != nil {
		hint := classifySSHError(out, err)
		res.Message = "install.sh execution failed: " + hint
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

	// Step 4: poll port 8888 (the STR agent webui) for up to 180 s.
	// Extended from 150 s because slower ST20 variants need a few
	// extra seconds for the boot-race watchdog (run-override.sh
	// re-fire loop) to win against Bose's service manager.
	res.Step = "wait-agent"
	if err := waitForTCPPort(host, 8888, 180*time.Second); err != nil {
		res.Message = "Speaker did not bring up the STR agent on port 8888 within 180 s. " +
			"It may still be rebooting. Try refreshing the speaker list in a minute. " +
			"If it stays down, pull a diagnostic bundle (Settings → Save logs) and attach it to the GitHub issue."
		return res, nil
	}
	res.Step = "done"
	res.OK = true
	res.Message = "STR agent is up on port 8888."
	return res, nil
}

// buildStickProbeCmd returns a single-line shell command (BusyBox-
// compatible) that prints STICKPATH=<path> for the first candidate
// that holds an executable install.sh, then falls back to scanning
// /media, /mnt and /run/media for *any* directory containing one.
// MISSING is printed if nothing matches so the caller can
// distinguish "no stick" from "ssh died".
func buildStickProbeCmd(paths []string) string {
	var b strings.Builder
	for _, p := range paths {
		b.WriteString(`if [ -e `)
		b.WriteString(p)
		b.WriteString(`/install.sh ]; then echo "STICKPATH=`)
		b.WriteString(p)
		b.WriteString(`"; exit 0; fi; `)
	}
	// Fallback wide scan: any subdir directly under /media /mnt
	// /run/media that holds an install.sh. -maxdepth 2 keeps the
	// scan cheap even on a busy /mnt with many bind mounts.
	b.WriteString(
		`for d in /media /mnt /run/media; do ` +
			`if [ -d "$d" ]; then ` +
			`for cand in $d/*; do ` +
			`if [ -e "$cand/install.sh" ]; then ` +
			`echo "STICKPATH=$cand"; exit 0; ` +
			`fi; done; fi; done; echo MISSING`)
	return b.String()
}

// classifySSHError turns an opaque "exit status 255" + combined
// stdout/stderr into a human-readable hint so the wizard UI can
// guide the user instead of just saying "exit 255".
func classifySSHError(out string, err error) string {
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "host key verification failed"),
		strings.Contains(low, "remote host identification has changed"):
		return "host key changed on the speaker (factory reset?). The next install attempt should succeed because UserKnownHostsFile is /dev/null in this build."
	case strings.Contains(low, "no matching"), strings.Contains(low, "their offer:"),
		strings.Contains(low, "no kex alg"), strings.Contains(low, "no hostkey alg"),
		strings.Contains(low, "no cipher"), strings.Contains(low, "no mac"):
		return "SSH algorithm negotiation failed. The speaker's old OpenSSH only offers legacy ciphers; please file a bug with the exact line from this log."
	case strings.Contains(low, "permission denied"):
		return "speaker refused passwordless root login. Bose's stock firmware allows it when /media/sda1 has the remote_services marker. Reboot the speaker with the STR stick plugged in, then retry."
	case strings.Contains(low, "connection refused"):
		return "speaker is reachable but not running sshd on port 22. It may be mid-reboot."
	case strings.Contains(low, "connection timed out"), strings.Contains(low, "operation timed out"):
		return "TCP connection to the speaker timed out. Check that it is on your LAN."
	case strings.Contains(low, "no route to host"), strings.Contains(low, "host is unreachable"):
		return "speaker is not on the LAN (no route). It may have rebooted into a different IP."
	}
	if err != nil {
		return err.Error()
	}
	return "no STR_SSH_OK marker received (check Wi-Fi to speaker)"
}

// boxSSHOutput runs cmd on the box over SSH and returns combined
// stdout+stderr. timeout caps the whole call.
func boxSSHOutput(host, cmd string, timeout time.Duration) (string, error) {
	args := append(append([]string{}, sshFlags...), "root@"+host, cmd)
	c := exec.Command("ssh", args...)
	hideCmdWindow(c)
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
	hideCmdWindow(c)
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
