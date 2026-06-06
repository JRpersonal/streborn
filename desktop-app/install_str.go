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
	"io"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync"
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

// sshFlagSets is a fallback chain of OpenSSH option lists, tried
// in order until one negotiates successfully. Whatever set wins
// the very first call is cached in sshFlagsActive and reused for
// every subsequent call in the same session — we only pay the
// trial-and-error cost on the first probe.
//
// Why a chain instead of one set: OpenSSH option names have churned
// over the years and STR ships to users on every OpenSSH from
// macOS Big Sur (8.1, 2020) to current Linux/Windows (10.x).
// v0.5.2 used PubkeyAcceptedAlgorithms (8.5+ only) and was rejected
// on Big Sur with "Bad configuration option" before negotiation
// even started. A static single set will keep tripping on someone
// somewhere; a fallback chain self-heals against future renames.
//
// Set order (most aggressive first, simplest last):
//
//  1. "full-legacy" — every legacy class explicitly patched: host
//     key, KEX, ciphers, MACs. Needed for the 2014-era Bose sshd
//     that only offers ssh-rsa / SHA1 / CBC. Works on every OpenSSH
//     from 6.x through current 10.x.
//
//  2. "host-key-only" — only -oHostKeyAlgorithms=+ssh-rsa
//     plus the connection-hygiene flags. If the user's ssh barfs
//     on any one of the algo-class options (some BSD-derived ssh
//     forks have spelled them differently), this strict subset
//     still lets the connection start: the box's ssh-rsa host key
//     will be accepted, the rest of the negotiation falls back to
//     whatever the user's ssh and the box can agree on out of the
//     box. Slightly less reliable against very modern OpenSSH that
//     disabled SHA1 KEX entirely, but works in more places.
//
//  3. "bare" — only connection hygiene (StrictHostKeyChecking=no,
//     known-hosts → /dev/null, BatchMode, ConnectTimeout). Last
//     resort: if the user's ssh rejects EVERY algo option,
//     including HostKeyAlgorithms, there's nothing else for us to
//     touch — we hand off to whatever the user's ssh defaults are
//     and hope the user's ~/.ssh/config covers the gap. On Bose's
//     2014 sshd this will usually fail at host-key verification,
//     but the resulting error ("Host key verification failed") is
//     actionable: the user can manually accept by adjusting their
//     ssh config. Better than a hard wall.
//
// StrictHostKeyChecking=no + UserKnownHostsFile=/dev/null are
// present in EVERY set because: (a) the Bose box's host key rotates
// on factory reset, which we trigger as part of bootstrap, so
// accept-new would refuse the very next install attempt on a
// re-flashed box; (b) the connection is over the user's home LAN
// to a device whose IP they just selected from a discovery list —
// there is no realistic MITM vector to defend against.
var sshFlagSets = [][]string{
	// Set 1: full-legacy
	{
		"-oHostKeyAlgorithms=+ssh-rsa",
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
	},
	// Set 2: host-key-only
	{
		"-oHostKeyAlgorithms=+ssh-rsa",
		"-oStrictHostKeyChecking=no",
		"-oUserKnownHostsFile=" + nullDevice,
		"-oGlobalKnownHostsFile=" + nullDevice,
		"-oBatchMode=yes",
		"-oConnectTimeout=8",
	},
	// Set 3: bare
	{
		"-oStrictHostKeyChecking=no",
		"-oUserKnownHostsFile=" + nullDevice,
		"-oBatchMode=yes",
		"-oConnectTimeout=8",
	},
}

// sshFlagsActive caches the index of the flag set that worked on
// the first successful call. Subsequent calls skip the fallback
// trial and use this set directly. -1 means "not chosen yet".
//
// Guarded by sshFlagsMu so concurrent install attempts (the user
// can fire several speakers in a row before the first one
// finishes) cannot wedge a half-written value.
var (
	sshFlagsMu     sync.Mutex
	sshFlagsActive = -1
)

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

	// Step 0a: preflight TCP reachability on the SSH port. SSH failing with a
	// bare "exit status 255" and no stderr is the opaque error users hit when
	// the box is simply not reachable (wrong/disconnected network, mid-reboot,
	// not yet onboarded to Wi-Fi). Reported by glehner and Max T (#, ST10).
	// Checking :22 first lets us return a human instruction instead of the
	// raw SSH exit code.
	res.Step = "preflight"
	if !tcpReachable(host, 22, 4*time.Second) {
		// :22 closed does NOT necessarily mean the box is off the network.
		// Bose only opens sshd while the box boots with the stick inserted
		// (the remote_services marker), so a fully-onboarded box that is
		// reachable on its Bose REST port (:8090) but has :22 closed is the
		// "install window closed" case, not a network problem. Gerald's
		// ST10 diagnostic (#, 06.06.) showed exactly this: 8090 reachable,
		// SSH not. Probe the Bose port so we can tell the two apart and give
		// an instruction the user can actually act on, instead of wrongly
		// blaming the network.
		if tcpReachable(host, 8888, 3*time.Second) {
			res.Message = "The speaker at " + host + " already answers on the STR agent port (8888), " +
				"so it looks like STR is installed already. Refresh the speaker list. " +
				"If you meant to reinstall, reboot the speaker with the STR stick plugged in first."
			a.logger.Warn("install_str: preflight, :22 closed but :8888 up (already installed?)", "host", host)
			return res, nil
		}
		if tcpReachable(host, 8090, 3*time.Second) {
			res.Message = "The speaker at " + host + " is on the network, but the install access (SSH) is closed. " +
				"Bose only opens it while the speaker boots with the STR stick plugged in. " +
				"Power the speaker off, insert the STR stick, power it back on, then install."
			a.logger.Warn("install_str: preflight, box reachable on :8090 but :22 closed (install window shut)", "host", host)
			return res, nil
		}
		res.Message = "The speaker is not reachable on the network (no answer on SSH port 22 or the Bose port 8090 at " + host + "). " +
			"First bring it onto your Wi-Fi with the Bose SoundTouch app and make sure this PC and the speaker are on the same network. " +
			"Then reboot the speaker with the STR stick plugged in and try again."
		a.logger.Warn("install_str: preflight failed, box not reachable on :22 or :8090", "host", host)
		return res, nil
	}

	// Step 0b: SSH itself reachable + authenticated? We do this as a
	// separate trivial command so a connect/auth/algorithm failure
	// surfaces with a specific message instead of looking like a
	// missing stick. The probe also doubles as a warmup so the next
	// SSH call reuses the negotiated host key.
	//
	// On failure we return (res, nil), NOT a wrapped error: Wails delivers
	// only the error to the frontend when the error is non-nil and drops the
	// res value, so the carefully classified res.Message would be lost and the
	// user would see the raw "ssh handshake: exit status 255" again. The
	// frontend renders res.Message + res.Log on res.OK == false.
	res.Step = "ssh-handshake"
	hello, helloErr := boxSSHOutput(host, "echo STR_SSH_OK", 12*time.Second)
	if helloErr != nil || !strings.Contains(hello, "STR_SSH_OK") {
		res.Log = hello
		hint := classifySSHError(hello, helloErr)
		res.Message = "SSH handshake to speaker failed: " + hint
		a.logger.Warn("install_str: ssh handshake failed", "host", host, "err", helloErr, "hint", hint)
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
				a.logger.Warn("install_str: ssh probe failed after retries", "host", host, "err", probeErr, "hint", hint)
				// (res, nil): keep res.Message reaching the frontend, see ssh-handshake.
				return res, nil
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
		a.logger.Warn("install_str: install.sh execution failed", "host", host, "err", err, "hint", hint)
		// (res, nil): keep res.Message reaching the frontend, see ssh-handshake.
		return res, nil
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
	case strings.Contains(low, "bad configuration option"):
		// Local OpenSSH refused one of our -o flags before connecting
		// at all. Almost always an option-name skew between OpenSSH
		// versions (the v0.5.2 release used PubkeyAcceptedAlgorithms
		// which only exists in OpenSSH 8.5+; older macOS shipped 8.1).
		// Surface the exact unknown option so the user can paste it
		// into a bug report and we can ship the rename quickly.
		return "your local SSH client refused an option: " + extractBadOption(out) +
			". Please file a bug with this exact line and your `ssh -V` output."
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

// extractBadOption pulls the offending option name out of OpenSSH's
// "command-line: line 0: Bad configuration option: <name>" error
// line, lower-cased exactly as OpenSSH emits it. Used by
// classifySSHError so the user-facing message names the actual
// option that needs renaming for our next release.
func extractBadOption(out string) string {
	const marker = "bad configuration option:"
	low := strings.ToLower(out)
	i := strings.Index(low, marker)
	if i < 0 {
		return "<unknown>"
	}
	tail := strings.TrimSpace(out[i+len(marker):])
	if nl := strings.IndexAny(tail, "\r\n"); nl >= 0 {
		tail = tail[:nl]
	}
	if tail == "" {
		return "<unknown>"
	}
	return tail
}

// boxSSHOutput runs cmd on the box over SSH and returns combined
// stdout+stderr. timeout caps the whole call.
//
// Walks sshFlagSets in order: if a set is rejected with "Bad
// configuration option" (the user's local OpenSSH does not know
// one of our -o flags), the next set is tried automatically. The
// first set that returns ANYTHING other than "Bad configuration
// option" wins and is cached for the rest of the session.
//
// timeout applies to each set individually so a transient stall
// on set 1 does not eat the whole budget. Total worst-case is
// len(sshFlagSets) * timeout.
func boxSSHOutput(host, cmd string, timeout time.Duration) (string, error) {
	start, _ := getCachedFlagSetIndex()
	var lastOut string
	var lastErr error
	for i := start; i < len(sshFlagSets); i++ {
		out, err := runSSHWithFlags(sshFlagSets[i], host, cmd, timeout)
		if isBadOptionError(out) {
			// Local ssh refuses this set's flags. Move on without
			// caching — the next call may still want to try this
			// index again with a different command (and the cache
			// also gets updated below when a non-bad-option result
			// arrives).
			lastOut, lastErr = out, err
			continue
		}
		// Anything else (success OR real network/auth/algo failure)
		// counts as "this set's flags were at least accepted by the
		// local ssh". Cache so the rest of the session skips the
		// trial-and-error.
		cacheFlagSetIndex(i)
		return out, err
	}
	return lastOut, lastErr
}

// boxSSHUploadStdin is boxSSHOutput plus an stdin stream. Same flag-set
// fallback chain. Used by UpdateBoxAgent's SSH-OTA path to pipe the 10 MB
// ARM binary into a remote `cat > file` — the HTTP-OTA route is unusable
// on Series-I boxes where the LD_PRELOAD shim is not active (the listener
// reachable from the LAN is Bose's own SoftwareUpdate HTTP service, which
// has a 1.5 KB POST buffer — see [[bose-http-buffer]] / #90).
func boxSSHUploadStdin(host, cmd string, in io.Reader, timeout time.Duration) (string, error) {
	start, _ := getCachedFlagSetIndex()
	var lastOut string
	var lastErr error
	for i := start; i < len(sshFlagSets); i++ {
		out, err := runSSHWithFlagsStdin(sshFlagSets[i], host, cmd, in, timeout)
		if isBadOptionError(out) {
			lastOut, lastErr = out, err
			continue
		}
		cacheFlagSetIndex(i)
		return out, err
	}
	return lastOut, lastErr
}

func runSSHWithFlagsStdin(flags []string, host, cmd string, in io.Reader, timeout time.Duration) (string, error) {
	args := append(append([]string{}, flags...), "root@"+host, cmd)
	c := exec.Command("ssh", args...)
	hideCmdWindow(c)
	c.Stdin = in
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
		return "", fmt.Errorf("ssh upload timeout after %s", timeout)
	}
}

// boxSSHFireAndForget runs cmd but does not require it to exit
// cleanly. Used for "reboot" where the connection dropping is
// expected. Same fallback-chain semantics as boxSSHOutput.
func boxSSHFireAndForget(host, cmd string, timeout time.Duration) error {
	start, _ := getCachedFlagSetIndex()
	for i := start; i < len(sshFlagSets); i++ {
		out, _ := runSSHWithFlags(sshFlagSets[i], host, cmd, timeout)
		if isBadOptionError(out) {
			continue
		}
		cacheFlagSetIndex(i)
		return nil
	}
	return nil
}

// runSSHWithFlags is the single subprocess invocation used by both
// boxSSHOutput and boxSSHFireAndForget. Returns combined stdout +
// stderr so the fallback-chain caller can scan for "Bad
// configuration option" markers.
func runSSHWithFlags(flags []string, host, cmd string, timeout time.Duration) (string, error) {
	args := append(append([]string{}, flags...), "root@"+host, cmd)
	c := exec.Command("ssh", args...)
	hideCmdWindow(c)
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

// isBadOptionError reports whether the combined ssh output starts
// with OpenSSH's "Bad configuration option" stanza. That happens
// before any network I/O, so it's safe to retry the same logical
// SSH call with a different flag set.
func isBadOptionError(out string) bool {
	return strings.Contains(strings.ToLower(out), "bad configuration option")
}

// getCachedFlagSetIndex returns the cached active-set index, or 0
// (try from the start) if not yet chosen. The bool is the
// "was-set" flag — currently unused by callers but handy for
// log breadcrumbs later.
func getCachedFlagSetIndex() (int, bool) {
	sshFlagsMu.Lock()
	defer sshFlagsMu.Unlock()
	if sshFlagsActive < 0 {
		return 0, false
	}
	return sshFlagsActive, true
}

func cacheFlagSetIndex(i int) {
	sshFlagsMu.Lock()
	defer sshFlagsMu.Unlock()
	if sshFlagsActive < 0 {
		sshFlagsActive = i
	}
}

// tcpReachable reports whether a single TCP connection to host:port succeeds
// within timeout. Used as an install preflight so an unreachable speaker
// produces a clear instruction instead of SSH's opaque "exit status 255".
func tcpReachable(host string, port int, timeout time.Duration) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), timeout)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
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
