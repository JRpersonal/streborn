// Regression tests for the in-app SSH installer. Each test is named
// after the user-visible failure mode it guards against so future
// refactors that re-introduce the same bug fail loudly in CI before
// they hit a real user. Issues referenced are on the public tracker.

package main

import (
	"errors"
	"strings"
	"testing"
)

// TestSSHFlagSetsRejectDeprecatedPubkeyOption guards against the
// "Bad configuration option: pubkeyacceptedalgorithms" regression
// (#60). PubkeyAcceptedAlgorithms was introduced in OpenSSH 8.5
// (April 2021); macOS Big Sur ships OpenSSH 8.1, which aborts ssh
// with that exact error before any negotiation if the option is
// present. v0.5.2 carried the option and was unusable on Big Sur.
// STR uses passwordless root login so the option is unnecessary
// anyway. No set in the fallback chain must carry it ever again.
func TestSSHFlagSetsRejectDeprecatedPubkeyOption(t *testing.T) {
	for i, set := range sshFlagSets {
		for _, f := range set {
			if strings.Contains(strings.ToLower(f), "pubkeyacceptedalgorithms") {
				t.Errorf("sshFlagSets[%d] contains PubkeyAcceptedAlgorithms which "+
					"breaks macOS Big Sur (OpenSSH 8.1, issue #60); flag was %q", i, f)
			}
		}
	}
}

// TestSSHFlagSetsCarryEveryLegacyAlgorithmClass ensures at least
// one set in the chain patches each algorithm class the Bose box's
// 2014-era sshd needs. Without these, modern OpenSSH refuses to
// negotiate and the installer never reaches the stick probe.
func TestSSHFlagSetsCarryEveryLegacyAlgorithmClass(t *testing.T) {
	classes := []struct {
		needle string
		why    string
	}{
		{"hostkeyalgorithms", "Bose offers only ssh-rsa host keys"},
		{"kexalgorithms", "Bose offers only diffie-hellman-group{1,14}-sha1"},
		{"ciphers", "Bose offers only CBC ciphers"},
		{"macs", "Bose offers only SHA1/MD5 MACs"},
	}
	for _, c := range classes {
		seen := false
		for _, set := range sshFlagSets {
			for _, f := range set {
				if strings.Contains(strings.ToLower(f), c.needle) {
					seen = true
					break
				}
			}
			if seen {
				break
			}
		}
		if !seen {
			t.Errorf("no sshFlagSet patches the %s algorithm class (needed because %s)", c.needle, c.why)
		}
	}
}

// TestSSHFlagSetsHaveBareFallback locks in the last-resort set in
// the chain. The "bare" fallback must be hygiene-only: if it carried
// algorithm patches and a user's ssh rejected even one of them, we
// would lose the escape hatch and bork the installer the same way
// v0.5.2 did.
func TestSSHFlagSetsHaveBareFallback(t *testing.T) {
	if len(sshFlagSets) < 2 {
		t.Fatalf("expected at least 2 fallback sets, have %d", len(sshFlagSets))
	}
	last := sshFlagSets[len(sshFlagSets)-1]
	for _, f := range last {
		low := strings.ToLower(f)
		switch {
		case strings.HasPrefix(low, "-okexalgorithms="):
			t.Errorf("bare fallback set carries KEX patch %q which defeats its purpose", f)
		case strings.HasPrefix(low, "-ociphers="):
			t.Errorf("bare fallback set carries cipher patch %q which defeats its purpose", f)
		case strings.HasPrefix(low, "-omacs="):
			t.Errorf("bare fallback set carries MAC patch %q which defeats its purpose", f)
		}
	}
}

// TestSSHFlagSetsAllSetBatchModeAndStrictHostKeyOff covers the
// connection-hygiene contract: every set in the chain must
// suppress interactive prompts and the rotating Bose host key
// must never end up in the user's known_hosts. Forgetting one of
// these on a future tweak produces silent UI hangs.
func TestSSHFlagSetsAllSetBatchModeAndStrictHostKeyOff(t *testing.T) {
	required := []string{
		"-oBatchMode=yes",
		"-oStrictHostKeyChecking=no",
	}
	for i, set := range sshFlagSets {
		joined := strings.ToLower(strings.Join(set, "\n"))
		for _, want := range required {
			if !strings.Contains(joined, strings.ToLower(want)) {
				t.Errorf("sshFlagSets[%d] missing required hygiene flag %q", i, want)
			}
		}
	}
}

// TestClassifySSHErrorRecognizesBadOption guards the user-facing
// error path that was the single most useful diagnostic in #60:
// when the local ssh refuses one of our flags with "Bad
// configuration option", the wizard must name the offending option
// instead of showing a bare "exit status 255".
func TestClassifySSHErrorRecognizesBadOption(t *testing.T) {
	out := "command-line: line 0: Bad configuration option: pubkeyacceptedalgorithms"
	msg := classifySSHError(out, errors.New("exit status 255"))
	low := strings.ToLower(msg)
	if !strings.Contains(low, "refused an option") {
		t.Errorf("classifier did not surface the 'refused an option' hint; got %q", msg)
	}
	if !strings.Contains(low, "pubkeyacceptedalgorithms") {
		t.Errorf("classifier did not include the offending option name; got %q", msg)
	}
}

// TestExtractBadOptionParsesOpenSSHFormat locks the parser against
// the literal OpenSSH stderr line shape. If a future OpenSSH
// changes the line, the diagnostic message goes back to "<unknown>"
// and the user is back to staring at "exit 255" with no clue.
func TestExtractBadOptionParsesOpenSSHFormat(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"command-line: line 0: Bad configuration option: pubkeyacceptedalgorithms", "pubkeyacceptedalgorithms"},
		{"Bad configuration option: kexalgorithms", "kexalgorithms"},
		{"prefix junk\ncommand-line: line 0: Bad configuration option: ciphers\nsuffix junk", "ciphers"},
		{"no marker here at all", "<unknown>"},
		{"", "<unknown>"},
	}
	for _, c := range cases {
		got := extractBadOption(c.in)
		if got != c.want {
			t.Errorf("extractBadOption(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDetectStickCopyFailureMatchesRunShMarkers guards the install-time
// diagnosis of the "agent never started because the binary could not be copied
// off a flaky stick" failure (CHI Wong ST30, 13.06). install.sh succeeds, the
// box reboots, run.sh's stick->NAND copy hits an I/O error, and with no prior
// NAND cache run.sh exits. Without this the desktop showed a generic "agent not
// up". The strings here MUST stay byte-identical to what usb-stick/run.sh logs;
// if run.sh's wording changes, this test fails before a release ships a silent
// regression of the specific message + the auto NAND-copy repair trigger.
func TestDetectStickCopyFailureMatchesRunShMarkers(t *testing.T) {
	positives := []string{
		// sync_stick_to_nand_always, exact run.sh wording.
		"Fri Jun 12 16:37:26: stick -> NAND cp failed (stick I/O error?), keeping previous NAND binary",
		// run.sh BIN resolution, exact wording.
		"Fri Jun 12 16:37:26: ERROR: neither NAND cache nor stick binary available",
		// Realistic multi-line tail with both markers interleaved with noise.
		"redeployed run-override.sh\nstick -> NAND cp failed (stick I/O error?), keeping previous NAND binary\nERROR: neither NAND cache nor stick binary available\n",
	}
	for _, p := range positives {
		if !detectStickCopyFailure(p) {
			t.Errorf("detectStickCopyFailure should match run.sh stick-copy-failure log:\n%q", p)
		}
	}
	negatives := []string{
		"",
		"stick binary deployed to NAND cache (10485760 bytes)",
		"STR webui :8888 listening at uptime=42s",
		"phase summary: wpa=12s boseHTTP=20s strAPI=42s",
	}
	for _, n := range negatives {
		if detectStickCopyFailure(n) {
			t.Errorf("detectStickCopyFailure should NOT match a healthy log:\n%q", n)
		}
	}
}

// TestDetectUSBPowerFailureMatchesVBUSSignature guards the discriminator that
// keeps STR from blaming the stick for an ST30 USB power dropout. Multiple
// independent users (13.06.2026) hit install.sh "Input/output error" on the
// ST30 only, with the SAME stick installing fine on their ST10/ST20: the cause
// was the speaker's USB port failing to keep VBUS up under read load, visible
// only in the kernel dmesg. If the matched signatures drift from what musb-hdrc
// actually logs, the user gets the misleading "stick likely faulty" text again,
// so this test pins them.
func TestDetectUSBPowerFailureMatchesVBUSSignature(t *testing.T) {
	positives := []string{
		// Exact musb-hdrc VBUS line from the field dmesg.
		"musb-hdrc musb-hdrc.0.auto: VBUS_ERROR in a_wait_vrise (81, <SessEnd), retry #3",
		// Enumeration timeouts (-110 = ETIMEDOUT) when VBUS sagged.
		"usb 1-1: device descriptor read/64, error -110",
		"usb 1-1: device not accepting address 2, error -110",
		// Realistic multi-line dmesg tail with the I/O error interleaved.
		"sda: sda1\nmusb-hdrc musb-hdrc.0.auto: VBUS_ERROR in a_wait_vrise (81, <SessEnd), retry #3\nend_request: I/O error, dev sda, sector 31728\nusb 1-1: USB disconnect, device number 2\n",
	}
	for _, p := range positives {
		if !detectUSBPowerFailure(p) {
			t.Errorf("detectUSBPowerFailure should match a USB VBUS/enumeration dropout dmesg:\n%q", p)
		}
	}
	negatives := []string{
		"",
		// A plain media read error with no power/enumeration signature: this is
		// the genuine unreadable/oversized-stick case that must stay stick-io-error.
		"FAT-fs (sda1): unable to read boot sector\nend_request: I/O error, dev sda, sector 0\n",
		"sd 0:0:0:0: [sda] Attached SCSI removable disk",
		"# dmesg usb/storage\nusb 1-1: new high-speed USB device number 2 using musb-hdrc",
	}
	for _, n := range negatives {
		if detectUSBPowerFailure(n) {
			t.Errorf("detectUSBPowerFailure should NOT match a non-power dmesg:\n%q", n)
		}
	}
}

// TestParseDfAvailBytes guards the free-space pre-check that decides whether the
// SSH repair stages into RAM (tmpfs) or NAND (ubifs). BusyBox df wraps a long
// device name onto a second line, shifting the column count, so the Available
// value must be read relative to the END of the last line (3rd from end), not by
// a fixed index. A regression here would silently make the chooser pick a
// too-small filesystem (or reject a fine one) before the byte-verify catches it.
func TestParseDfAvailBytes(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want int64
	}{
		{
			name: "normal single-line row",
			out:  "Filesystem           1K-blocks      Used Available Use% Mounted on\ntmpfs                    20480      4096     16384  20% /tmp\n",
			want: 16384 * 1024,
		},
		{
			name: "wrapped long device name",
			out:  "Filesystem           1K-blocks      Used Available Use% Mounted on\nubi0:rootfs_data\n                         61440     51200     10240  83% /mnt/nv\n",
			want: 10240 * 1024,
		},
		{name: "empty", out: "", want: 0},
		{name: "header only", out: "Filesystem 1K-blocks Used Available Use% Mounted on\n", want: 0},
		{name: "df error", out: "df: /nope: can't find mount point\n", want: 0},
	}
	for _, c := range cases {
		if got := parseDfAvailBytes(c.out); got != c.want {
			t.Errorf("%s: parseDfAvailBytes = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestBuildStickProbeCmdScansFallbackDirectories guards the broader
// stick mount probe: scanning /media, /mnt and /run/media for any
// install.sh fallback. Without the wide scan, a firmware variant
// that mounts USB sticks somewhere other than /media/sd[a-d]1
// breaks first-install with the cryptic "install.sh did not
// appear" UI message.
func TestBuildStickProbeCmdScansFallbackDirectories(t *testing.T) {
	cmd := buildStickProbeCmd(stickProbePaths)
	for _, root := range []string{"/media", "/mnt", "/run/media"} {
		if !strings.Contains(cmd, root) {
			t.Errorf("stick probe does not scan %s as fallback (firmware variants may mount sticks there)", root)
		}
	}
	if !strings.Contains(cmd, "STICKPATH=") {
		t.Error("stick probe does not emit STICKPATH= marker which the caller parses")
	}
	if !strings.Contains(cmd, "MISSING") {
		t.Error("stick probe does not emit MISSING marker which lets the caller distinguish 'no stick' from 'ssh died'")
	}
}
