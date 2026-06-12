// SSH transport for the desktop app. The installer, OTA, uninstall, true
// factory reset and the diagnostic log export all run shell commands on the
// stock Bose box over passwordless root SSH, so the transport (flag-set
// fallback chain, subprocess runners, error classification, the shared
// handshake/reboot policies and the TCP reachability probe) lives here rather
// than inside any one feature file.
//
// Auth: Bose's stock firmware ships /etc/shadow with an empty root password and
// accepts it while the remote_services marker is present on /media/sda1 (our
// stick provisioning writes it). No key, no password, no UAC.

package main

import (
	"context"
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
	case strings.Contains(low, "input/output error"), strings.Contains(low, "i/o error"):
		// The speaker found install.sh but the read failed at the media layer.
		// Almost always the USB stick: a large stick force-formatted to FAT32
		// with a 64 KB cluster size the speaker's old kernel cannot read (the
		// classic 64 GB case), an unclean write, or failing flash. Re-preparing
		// with our formatter (32 KB clusters, capped) or a smaller stick fixes it.
		return "the speaker could not read the stick (I/O error). The USB stick is likely faulty, or a large stick was formatted with a block size the speaker cannot read. Re-prepare it with the Format option, or use a smaller stick (4 to 32 GB)."
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
// trySSHFlagSets runs an SSH operation against the OpenSSH flag-set fallback
// chain: start from the cached working set and, on a local "bad configuration
// option" rejection, move to the next set. The first set the local ssh accepts
// (a success OR a real network/auth/algo failure, as opposed to a flag
// rejection) is cached, so the rest of the session skips the trial-and-error.
// run performs the actual ssh subprocess for one flag set; the three callers
// differ only in that closure.
func trySSHFlagSets(run func(flags []string) (string, error)) (string, error) {
	start := getCachedFlagSetIndex()
	var lastOut string
	var lastErr error
	for i := start; i < len(sshFlagSets); i++ {
		out, err := run(sshFlagSets[i])
		if isBadOptionError(out) {
			lastOut, lastErr = out, err
			continue
		}
		cacheFlagSetIndex(i)
		return out, err
	}
	return lastOut, lastErr
}

func boxSSHOutput(host, cmd string, timeout time.Duration) (string, error) {
	return trySSHFlagSets(func(flags []string) (string, error) {
		return runSSHWithFlags(flags, host, cmd, timeout)
	})
}

// boxSSHUploadStdin is boxSSHOutput plus an stdin stream. Same flag-set
// fallback chain. Used by UpdateBoxAgent's SSH-OTA path to pipe the 10 MB
// ARM binary into a remote `cat > file` — the HTTP-OTA route is unusable
// on Series-I boxes where the LD_PRELOAD shim is not active (the listener
// reachable from the LAN is Bose's own SoftwareUpdate HTTP service, which
// has a 1.5 KB POST buffer — see [[bose-http-buffer]] / #90).
func boxSSHUploadStdin(host, cmd string, in io.Reader, timeout time.Duration) (string, error) {
	return trySSHFlagSets(func(flags []string) (string, error) {
		return runSSHWithFlagsStdin(flags, host, cmd, in, timeout)
	})
}

func runSSHWithFlagsStdin(flags []string, host, cmd string, in io.Reader, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := append(append([]string{}, flags...), "root@"+host, cmd)
	c := exec.CommandContext(ctx, "ssh", args...)
	hideCmdWindow(c)
	c.Stdin = in
	out, err := c.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("ssh upload timeout after %s", timeout)
	}
	return string(out), err
}

// boxSSHFireAndForget runs cmd but does not require it to exit
// cleanly. Used for "reboot" where the connection dropping is
// expected. Same fallback-chain semantics as boxSSHOutput.
func boxSSHFireAndForget(host, cmd string, timeout time.Duration) error {
	// Fire-and-forget: the connection dropping (e.g. on reboot) is expected, so
	// the flag-set result is ignored; we only care that a set was accepted.
	_, _ = trySSHFlagSets(func(flags []string) (string, error) {
		return runSSHWithFlags(flags, host, cmd, timeout)
	})
	return nil
}

// rebootCmd is the one hardened detached reboot used after every install, OTA,
// uninstall and factory reset. `sync` flushes the just-written NAND files
// before the box goes down, `/sbin/reboot` is the absolute path (some call
// paths run with a thin PATH), and stdio is fully detached so the SSH session
// returns instead of blocking on the closing socket as the box drops off the
// LAN. Earlier paths hand-rolled weaker forms ("(sleep 1; reboot) &") that
// skipped both the sync and the detach; the OTA path's comment explains why
// both matter, and that lesson now applies everywhere.
const rebootCmd = "(sleep 1; sync; /sbin/reboot) </dev/null >/dev/null 2>&1 &"

// boxReboot triggers the hardened detached reboot on the box. The dropped
// connection is the expected outcome, so the fire-and-forget result is ignored.
func boxReboot(host string) error {
	return boxSSHFireAndForget(host, rebootCmd, 6*time.Second)
}

// sshHandshake verifies the SSH channel is usable by running a trivial echo,
// retrying with a short backoff. On slower boxes (ST10 especially) sshd accepts
// the TCP connection but the crypto handshake is not ready within a few seconds
// right after boot, so a single short attempt failed and the user had to retry
// the whole operation 2-3 times ("ssh timeout" while :8090 was already
// reachable, #114). The box answering at all means it is up, so a few spaced
// retries cover the sshd warmup window. Every SSH-gated feature (install,
// repair, uninstall, factory reset, OTA) goes through this so they all inherit
// the proven policy instead of each picking its own one-shot timeout; the OTA
// path runs exactly when boxes are busiest, so it needs the retries most.
// Returns the last combined output and error; the caller still confirms the
// STR_SSH_OK marker so a stray success line cannot be mistaken for a handshake.
func sshHandshake(host string, attempts int) (string, error) {
	if attempts < 1 {
		attempts = 1
	}
	var out string
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second) // 2s, 4s, 6s, ...
		}
		out, err = boxSSHOutput(host, "echo STR_SSH_OK", 18*time.Second)
		if err == nil && strings.Contains(out, "STR_SSH_OK") {
			return out, nil
		}
	}
	return out, err
}

// runSSHWithFlags is the single subprocess invocation used by both
// boxSSHOutput and boxSSHFireAndForget. Returns combined stdout +
// stderr so the fallback-chain caller can scan for "Bad
// configuration option" markers.
func runSSHWithFlags(flags []string, host, cmd string, timeout time.Duration) (string, error) {
	// exec.CommandContext owns the timeout: it kills the process safely after
	// Start, so there is no race on c.Process and no nil-deref when the timeout
	// fires before Start completes (the old goroutine+select+Process.Kill form
	// could panic under load with the 4-8 s timeouts in use).
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := append(append([]string{}, flags...), "root@"+host, cmd)
	c := exec.CommandContext(ctx, "ssh", args...)
	hideCmdWindow(c)
	out, err := c.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("ssh timeout after %s", timeout)
	}
	return string(out), err
}

// isBadOptionError reports whether the combined ssh output starts
// with OpenSSH's "Bad configuration option" stanza. That happens
// before any network I/O, so it's safe to retry the same logical
// SSH call with a different flag set.
func isBadOptionError(out string) bool {
	return strings.Contains(strings.ToLower(out), "bad configuration option")
}

// getCachedFlagSetIndex returns the cached active-set index, or 0
// (try from the start) if not yet chosen.
func getCachedFlagSetIndex() int {
	sshFlagsMu.Lock()
	defer sshFlagsMu.Unlock()
	if sshFlagsActive < 0 {
		return 0
	}
	return sshFlagsActive
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

// BoxInstallReachable reports whether the speaker accepts a TCP connection on
// SSH (:22), the precondition for the in-app installer. The setup wizard polls
// this so the install button only unlocks once the speaker is actually
// reachable, instead of letting the user trigger an install that then times out.
func (a *App) BoxInstallReachable(host string) bool {
	if host == "" {
		return false
	}
	return tcpReachable(host, 22, 3*time.Second)
}
