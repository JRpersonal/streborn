package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/boxmem"
	"github.com/JRpersonal/streborn/sticksetup"
	"golang.org/x/crypto/ssh"
	"streborn-app/agentbin"
)

// The install fallback that bypasses the USB stick: RepairInstallViaSSH, the staging of STR's files onto the box (targets, RAM and free-space checks, stopping a running agent to make room), and the SDK cloud URL heal. Split out of install_str.go.

// RepairInstallViaSSH is the install fallback for when the USB stick itself is
// unreadable: install.sh dies with exit 126 / an I/O error because a large or
// faulty stick was force-formatted FAT32 with a block size the speaker's old
// kernel cannot read (ST30 #119). Instead of reading from the stick it stages
// STR's embedded files (install.sh, run.sh, rc.local, the agent binary, ...)
// into a NAND directory over SSH and runs install.sh from there with STR_STICK
// pointing at that directory, bypassing the stick entirely. /mnt/nv is a
// writable ubifs even when the USB mount is dead. Requires SSH up (Bose stock
// root SSH) and a real embedded agent (release build, not a dev stub).
//
// This is the entry the window's "repair over SSH" button calls, so it takes
// the one-writer-per-speaker claim itself (boxbusy.go): the repair stops the
// speaker's agent, rewrites its NAND folder and restarts it, and none of that
// may start under a running install, update, removal or firmware flash, nor
// may one of those start under it. The install calls repairInstallViaSSH
// directly, under the claim it already holds.
func (a *App) RepairInstallViaSSH(host, model string) (InstallResult, error) {
	release, busyErr := writesInFlight.claim(host, "a repair")
	if busyErr != nil {
		a.logger.Info("repair_ssh: refused, the speaker is busy", "host", host, "err", busyErr)
		return InstallResult{Step: "busy", Message: busyErr.Error()}, busyErr
	}
	defer release()
	return a.repairInstallViaSSH(host, model)
}

// repairInstallViaSSH is the repair itself, for a caller that already holds
// the speaker's write claim (repairAndAwaitAgent, inside a running install).
func (a *App) repairInstallViaSSH(host, model string) (InstallResult, error) {
	res := InstallResult{Step: "repair-ssh"}

	if len(agentbin.Bytes()) == 0 {
		res.Code = "no-embedded-agent"
		res.Message = "this build has no embedded agent binary; SSH repair needs a release build"
		return res, nil
	}

	hello, helloErr := sshHandshake(host, 4)
	if !strings.Contains(hello, "STR_SSH_OK") {
		res.Code = "ssh-failed"
		res.Message = "SSH to speaker failed: " + classifySSHError(hello, helloErr)
		a.logger.Warn("repair_ssh: ssh handshake failed", "host", host, "err", helloErr)
		return res, nil
	}

	v := appVersion
	if appBuild != "" && appBuild != "dev" {
		v = appVersion + "+" + appBuild
	}
	files, err := repairFileSet(agentbin.Bytes(), v)
	if err != nil {
		res.Message = "could not assemble install files: " + err.Error()
		return res, err
	}

	// Stage the files in the box's RAM (tmpfs) first, not straight onto NAND.
	// Two field-driven reasons: (1) the ~11 MB agent binary travels over SSH on
	// what is often weak Wi-Fi, and a stall or truncation mid-stream then only
	// dirties a throwaway RAM copy we byte-verify before trusting it, instead of
	// leaving a half-written file on flash; (2) install.sh's NAND-cache commit
	// (the Layer-1 seed) is then a fast LOCAL cp from RAM, not a flash write held
	// open at network pace. install.sh runs immediately after, so the staged
	// files never need to survive a reboot, which is why tmpfs is safe.
	//
	// Both targets are tight on these boxes (a small tmpfs vs a nearly-full NAND),
	// so before committing the upload we df each candidate and prefer one that
	// actually has room, instead of filling a too-small filesystem and only
	// learning that from the post-write byte-verify. NAND is ubifs, which
	// transparently compresses, so its requirement is discounted (see
	// stageRepairFilesBestEffort). A candidate df says is too small is still tried
	// last rather than skipped (fallback-first).
	res.Step = "repair-stage"
	var rawNeed int64
	for _, data := range files {
		rawNeed += int64(len(data))
	}
	stage, stoppedSTR, serr := a.stageRepairFilesBestEffort(host, rawNeed, files)
	// restartIfStopped brings the old agent back when this repair stopped it
	// to free RAM and then ends before its own reboot. The NAND still carries
	// the old agent (install.sh's mv is the last step of its copy), and boot
	// cleans any half-written .new files. The SDK cloud URLs are healed first,
	// exactly as before the success reboot: a :17000-unlocked speaker whose
	// install did not get as far as run.sh would otherwise boot into
	// str-setup.invalid and sweep its light bar forever (ST300, 2026-07-07).
	// Both calls are best-effort, so a speaker that is already down costs
	// nothing.
	restartIfStopped := func() {
		if !stoppedSTR {
			return
		}
		a.logger.Info("repair_ssh: restarting the speaker so the STR agent stopped for the install comes back", "host", host)
		if herr := a.healSDKCloudURLs(host); herr != nil {
			a.logger.Warn("repair_ssh: could not heal SDK cloud URLs before the restart", "host", host, "err", herr)
		}
		_ = boxReboot(host)
	}
	if serr != nil {
		restartIfStopped()
		res.Code = "repair-upload-failed"
		res.Message = "could not copy STR to the speaker: " + serr.Error()
		// When EVERY target failed with a short upload, the network path itself
		// cannot carry the transfer (the classic reverse-DNS-stall LAN, where the
		// speaker's sshd adds ~10 s to every connection). Point the user at the
		// USB-stick install, which does not touch the network at all, instead of
		// leaving them to retry a path that will keep failing the same way.
		var allTrunc *allTargetsTruncatedError
		if errors.As(serr, &allTrunc) {
			res.Message += ". The network install could not transfer STR to this speaker " +
				"(this can happen on networks that slow every connection to the speaker). " +
				"The most reliable alternative is the USB-stick install: prepare a stick in the app, " +
				"plug it into the speaker, and reboot it."
		}
		// The speaker was too short on memory to hold the install files in RAM
		// even with STR's own programs on it stopped, and its storage could not
		// take them either. The USB-stick install copies straight from the stick
		// and never holds the files in RAM, so it is the way through, on the
		// models that read a stick at all.
		var ramShort *repairRAMShortError
		if errors.As(serr, &ramShort) {
			res.Message += ". The speaker does not have enough free memory for the network install right now."
			if !neverReadsStickAtBoot(model, "") {
				res.Message += " The USB-stick install does not need that memory: prepare a stick in the app, " +
					"plug it into the speaker, and reboot it."
			}
		}
		a.logger.Warn("repair_ssh: staging failed on all targets", "host", host, "err", serr)
		return res, nil
	}

	res.Step = "repair-run"
	out, err := boxSSHOutput(host, "STR_STICK="+stage+" sh "+stage+"/install.sh install 2>&1", installRunBudget(model))
	res.Log = out
	if err != nil && boxRestartedMidCommand(err) {
		// The SSH channel closed without an exit status: the speaker went down
		// under the running install.sh (a reboot or an OOM-driven restart, the
		// ST20 of 2026-10-26 twice in a row), not a script that failed. It
		// comes back by itself within minutes with whatever agent its NAND
		// holds (the old one, or the new one when install.sh's mv already
		// ran), so the advice is wait, do not retry the same install, send the
		// logs. Never the post-install "did not come back after the install
		// rebooted it" text, and no stick advice, which is model-specific and
		// does not help here.
		//
		// The same missing exit status also appears when only the speaker's
		// sshd child was OOM-killed and the speaker itself stayed up. If this
		// repair stopped the old agent, it would then stay down for good (the
		// pidfile is gone, so both run.sh watchdogs have ended), so the
		// speaker is restarted here as on every other failure.
		res.Code = "repair-box-restarted"
		res.Message = "The speaker restarted in the middle of the network install, while ST Reborn was being copied onto it." +
			"\n\n" + repairBoxRestartedAdvice
		a.logger.Warn("repair_ssh: speaker restarted during the install", "host", host, "model", model, "err", err,
			"installOutput", truncForLog(out, 4000), "boxDiag", truncForLog(boxInstallDiag(host), 8000))
		restartIfStopped()
		return res, nil
	}
	if err != nil {
		res.Code = "repair-install-error"
		hint := classifySSHError(out, err)
		res.Message = "SSH repair install failed: " + hint
		a.logger.Warn("repair_ssh: install failed", "host", host, "model", model, "err", err,
			"hint", hint, "installOutput", truncForLog(out, 4000), "boxDiag", truncForLog(boxInstallDiag(host), 8000))
		restartIfStopped()
		return res, nil
	}
	if strings.Contains(out, "FEHLER") || strings.Contains(out, "ERROR") {
		res.Code = "repair-install-script-error"
		res.Message = "install.sh reported an error during SSH repair. See log."
		restartIfStopped()
		return res, nil
	}

	// install.sh copied everything into /mnt/nv/streborn, so the ~20 MB staging
	// dir is now pure leftover. Remove it before the reboot, otherwise it sits on
	// the NAND forever and starves later OTA updates of the room they need for the
	// atomic .new write: a stranded streborn-install filled a ST30 to 80% and
	// every agent update then failed with "no space left on device" (#ST30
	// Daniel). Best-effort: a cleanup failure must not fail an otherwise-good
	// install (the boot-time cleanup_nand is the backstop).
	if _, cerr := boxSSHOutput(host, "rm -rf "+stage, 20*time.Second); cerr != nil {
		a.logger.Warn("repair_ssh: could not remove install staging dir; boot cleanup is the backstop", "host", host, "dir", stage, "err", cerr)
	} else {
		a.logger.Info("repair_ssh: removed install staging dir", "host", host, "dir", stage)
	}

	res.OK = true
	res.Step = "reboot"
	res.Message = "STR installed over SSH (USB stick bypassed). The speaker will reboot and join your Wi-Fi."
	a.logger.Info("repair_ssh: install ran", "host", host, "outBytes", len(out))

	// Restore any str-setup.invalid placeholder a stick-free :17000 unlock left in
	// the persistent Bose SDK config back to the stock cloud hosts, BEFORE the
	// reboot so the box reads healed URLs on its very next boot. This SSH-open
	// install path (auto-repair: no stick, SSH already open) previously never
	// restored them, so a box that had been :17000-unlocked booted with
	// bmxRegistryUrl=https://str-setup.invalid, which NXDOMAINs, and its light bar
	// swept forever (live ST300, 2026-07-07). The sys-configuration/envswitch
	// restore over :17000 does NOT reliably persist to OverrideSdkPrivateCfg.xml
	// across a hard reboot, so the file is edited directly here. Best-effort and
	// idempotent (a no-op when no placeholder is present, e.g. a plain stick
	// install), so it never fails an otherwise-good install.
	if herr := a.healSDKCloudURLs(host); herr != nil {
		a.logger.Warn("repair_ssh: could not heal SDK cloud URLs before reboot; a :17000-unlocked box may keep sweeping", "host", host, "err", herr)
	}

	a.emitPhase("restart")
	_ = boxReboot(host)
	return res, nil
}

// repairFileSet is the file set the SSH repair stages on the speaker: the stick
// template, the agent binary and the version stamp, WITHOUT the go-librespot
// Spotify engine. install.sh never uses a staged engine (it only ever deletes
// the NAND one to make room), and ensureEngineAfterInstall pushes it once
// the agent is up. Staging it anyway put ~16 MB of unreclaimable tmpfs into a
// 122 MB, swapless ST20's RAM on top of BoseApp's boot and the old agent, and
// the speaker restarted under install.sh's agent copy twice (2026-10-26).
func repairFileSet(agent []byte, version string) (map[string][]byte, error) {
	files, err := sticksetup.StickFileSet(agent, nil, version)
	if err != nil {
		return nil, err
	}
	delete(files, "go-librespot")
	return files, nil
}

// boxRestartedMidCommand reports whether an SSH command ended because the
// speaker went away under it rather than because the command failed: the
// channel closed with no exit status and no exit signal, which is what a box
// that reboots (or is restarted by its supervisor after an OOM kill) leaves
// behind. A normal failure carries an exit status (*ssh.ExitError) and a
// timeout is our own error, so neither matches.
func boxRestartedMidCommand(err error) bool {
	if err == nil {
		return false
	}
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) {
		return true
	}
	return strings.Contains(err.Error(), "exited without exit status or exit signal")
}

// healSDKCloudURLsScript rewrites the bmx/stats/marge URL tags in the box's
// persistent Bose SDK config, replacing any str-setup.invalid placeholder value
// with the stock cloud hosts. Mirrors the on-box heal_sdk_cloud_urls in
// usb-stick/install.sh line for line. It exits early (no-op) when the file is
// absent or carries no placeholder, and prints STR_HEALED_SDK_URLS only when it
// actually rewrote something. Runs over BusyBox sh via boxSSHOutput.
const healSDKCloudURLsScript = `f=/mnt/nv/OverrideSdkPrivateCfg.xml
[ -f "$f" ] || exit 0
grep -q 'str-setup\.invalid' "$f" 2>/dev/null || exit 0
sed -e 's#\(<bmxRegistryUrl>\)[^<]*str-setup\.invalid[^<]*\(</bmxRegistryUrl>\)#\1https://content.api.bose.io/bmx/registry/v1/services\2#g' -e 's#\(<statsServerUrl>\)[^<]*str-setup\.invalid[^<]*\(</statsServerUrl>\)#\1https://events.api.bosecm.com\2#g' -e 's#\(<margeServerUrl>\)[^<]*str-setup\.invalid[^<]*\(</margeServerUrl>\)#\1https://streaming.bose.com\2#g' "$f" > "$f.new" 2>/dev/null && mv "$f.new" "$f" 2>/dev/null && echo STR_HEALED_SDK_URLS`

// healSDKCloudURLs restores any str-setup.invalid placeholder host that a
// stick-free :17000 unlock (telnet_enable_ssh.go) left in the box's persistent
// Bose SDK config back to the stock cloud hostnames, over SSH, editing
// /mnt/nv/OverrideSdkPrivateCfg.xml in place. That file is the durable source of
// the box's bmx/stats/marge URLs: the unlock points them at str-setup.invalid to
// fire the SSH injection, and although the sys-configuration/envswitch layer is
// normally restored afterwards (resetBoseURLsViaTelnet), that restore does NOT
// reliably persist to this file across a hard reboot. STR only redirects the
// STOCK hosts (streaming.bose.com, content.api.bose.io) via /etc/hosts, so the
// box must carry the stock hostnames for the redirect to catch them. Idempotent
// and best-effort: a no-op when no placeholder is present, and any SSH error is
// returned so the caller can log it rather than fail the install. Must run BEFORE
// the post-install reboot so the SDK reads the healed URLs on its next boot.
func (a *App) healSDKCloudURLs(host string) error {
	out, err := boxSSHOutput(host, healSDKCloudURLsScript, 15*time.Second)
	if err != nil {
		return fmt.Errorf("heal SDK cloud URLs: %s", classifySSHError(out, err))
	}
	if strings.Contains(out, "STR_HEALED_SDK_URLS") {
		a.logger.Info("install_str: healed str-setup.invalid cloud URLs in OverrideSdkPrivateCfg.xml back to stock", "host", host)
	} else {
		a.logger.Info("install_str: no str-setup.invalid placeholder in the SDK config to heal (any other non-stock host is healed by install.sh, effective at the next boot)", "host", host)
	}
	return nil
}

// uploadTruncatedError is the staging failure where the file landed on the box
// but the byte-count verify read fewer bytes than were sent. One of these can
// be weak Wi-Fi mid-transfer; ALL targets failing this way is the signature of
// a network path that cannot carry the transfer at all, which is what the
// caller keys the USB-stick guidance on (errors.As through the staging chain).
type uploadTruncatedError struct{ msg string }

func (e *uploadTruncatedError) Error() string { return e.msg }

// stageRepairFiles uploads the embedded install file set into stageDir on the
// box over SSH and verifies every file arrived intact, so a truncated
// weak-Wi-Fi transfer is caught before install.sh trusts it. The agent binary
// is the only large file and gets one upload retry, since a transient stall
// mid-stream is exactly what staging-then-verify is meant to absorb. Returns a
// human-readable error naming the file and where it failed; nil on success.
//
// The 30 s command timeouts here (and on the df probe below) look generous for
// what are millisecond commands on the box, but they must survive the
// system-ssh fallback path paying a full handshake per command on networks
// where the box's sshd stalls ~10.5 s on reverse DNS (see boxssh.go): the old
// 8-10 s budgets died mid-handshake there, and every staging target then
// failed as "truncated"/"no space" even though the box was healthy.
//
// Each file goes through sshPushFile, the same upload-and-verify the agent
// update uses: the on-box byte count is parsed out of the session's echo,
// not string-matched against the whole blob (OpenSSH used to fold its
// "Warning: Permanently added ... to the list of known hosts." banner in
// ahead of the number via CombinedOutput, and an exact-match compare then
// read a byte-perfect upload as "truncated" and falsely failed the whole SSH
// NAND-install fallback for ST30 stick-power users, 13.06; LogLevel=ERROR
// suppresses the banner at the source now, parsing keeps the check correct
// regardless), and the content is checked with sha256sum, md5sum or, on a
// BusyBox with neither, the size. Until this check the staging verified the
// size alone.
func (a *App) stageRepairFiles(host, stageDir string, files map[string][]byte) error {
	if out, err := boxSSHOutput(host, "rm -rf "+stageDir+" && mkdir -p "+stageDir+"/bin", 30*time.Second); err != nil {
		return fmt.Errorf("could not create staging dir %s: %s", stageDir, classifySSHError(out, err))
	}
	for name, data := range files {
		attempts := 1
		if len(data) > 1<<20 { // only the agent binary; everything else is a few KB
			attempts = 2
		}
		err := sshPushFile(host, stageDir+"/"+name, data, sshPushOptions{attempts: attempts, verifyTimeout: 30 * time.Second})
		if err != nil {
			return stagingPushError(name, len(data), err)
		}
	}
	_, _ = boxSSHOutput(host, "chmod +x "+stageDir+"/install.sh "+stageDir+"/run.sh "+stageDir+"/streborn-armv7l 2>/dev/null", 30*time.Second)
	return nil
}

// stagingPushError words an sshPushFile failure for the install staging: the
// file is named, a short upload is the uploadTruncatedError the staging
// chooser keys its USB-stick guidance on (allTargetsTruncatedError), and a
// failed content check names the check.
func stagingPushError(name string, size int, err error) error {
	var up *sshPushUploadError
	var short *sshPushShortError
	var ver *sshPushVerifyError
	switch {
	case errors.As(err, &up):
		return fmt.Errorf("upload %s failed: %s", name, classifySSHError(up.out, up.err))
	case errors.As(err, &short):
		return &uploadTruncatedError{msg: fmt.Sprintf("upload %s truncated: speaker received %s of %d bytes", name, short.received(), size)}
	case errors.As(err, &ver):
		return fmt.Errorf("upload %s arrived with the right size but failed its checksum on the speaker: %s", name, classifySSHError(ver.out, ver.err))
	}
	return err
}

// lastIntField returns the LAST whitespace-separated token in out that parses as
// a non-negative integer. The box prints exactly one integer for the commands
// that use this (`wc -c` for the upload byte-count verify, `grep -c` for the CPU
// core count), so the last such token is that number even when local SSH noise
// is folded in ahead of it by CombinedOutput: with UserKnownHostsFile=/dev/null
// OpenSSH prints "Warning: Permanently added '<host>' (<key>) to the list of
// known hosts." on stderr at connect time, which lands before the remote stdout.
// Scanning for the last integer (rather than string-matching the whole blob or
// trusting a fixed field index) keeps these checks correct against that banner
// and any other pre-stdout noise; the banner's own tokens never parse as
// integers (the host is quoted and dotted), so they are ignored regardless of
// ordering. Returns (0, false) when no integer token is present, e.g. the remote
// command produced nothing (a missing file, so the redirect failed), which
// callers treat as "unreadable" / a failed transfer.
func lastIntField(out string) (int64, bool) {
	var n int64
	var ok bool
	for _, f := range strings.Fields(out) {
		if v, err := strconv.ParseInt(f, 10, 64); err == nil && v >= 0 {
			n = v
			ok = true
		}
	}
	return n, ok
}

// repairStageCandidates are the on-box staging targets for the SSH repair, in
// preference order: RAM (tmpfs) first, NAND (ubifs) as the fallback. compress is
// the fraction of the raw upload size we expect to actually need on that
// filesystem. tmpfs is 1.0 (RAM stores data verbatim). /mnt/nv is discounted
// because it is ubifs, which transparently compresses on write, and on top of
// that ubifs df deliberately UNDER-reports free space (its budgeting assumes
// worst-case incompressible data). A stripped ARM ELF plus a few text scripts
// compress comfortably, so 0.6 is a safe discount that stops us needlessly
// rejecting NAND when it really has room. The post-write byte-verify in
// stageRepairFiles is the backstop if the estimate is too optimistic.
//
// ram marks the tmpfs candidates: their df figure is the tmpfs SIZE CAP (about
// half the RAM), not memory that is actually free, so they are also held to the
// MemAvailable guard in stageRepairFilesBestEffort.
var repairStageCandidates = []struct {
	base     string
	compress float64
	ram      bool
}{
	// Prefer large RAM tmpfs so staging never lands on the tiny NAND we are about
	// to install INTO. Staging into /mnt/nv and then copying within the same volume
	// keeps TWO full copies of every file on a ~31 MB NAND at once, which ENOSPCs
	// mid-install ("cp: write error: No space left on device", Klaus' ST30 #119,
	// 2026-06-27): a factory-reset box has only ~22 MB free, enough for one install
	// (~14 MB compressed) but not for staging + copy. /dev/shm and /run are ~60 MB
	// RAM on these boxes (compress 1.0, RAM does not compress), so staging there
	// and copying RAM -> NAND leaves only ONE copy on the NAND. /tmp is often only
	// ~16 MB; /mnt/nv stays the last resort for a RAM-starved box, where
	// install.sh's drop-the-engine reclaim is the backstop. A candidate that does
	// not exist reads as 0 free and falls to the back, so this never blocks.
	{"/dev/shm", 1.0, true},
	{"/run", 1.0, true},
	{"/tmp", 1.0, true},
	{"/mnt/nv", 0.6, false},
}

// repairRAMShortError is the staging failure on a speaker whose RAM could not
// hold the install files even after STR's own programs on it were stopped, and
// whose NAND (the only other target) could not take them either.
type repairRAMShortError struct {
	memAvailable int64
	err          error
}

func (e *repairRAMShortError) Error() string {
	return fmt.Sprintf("the speaker has only %d MB of free memory, too little to hold the install files in RAM, and its storage could not take them: %v",
		e.memAvailable>>20, e.err)
}
func (e *repairRAMShortError) Unwrap() error { return e.err }

// repairRAMRoomFor reports whether staging rawNeed bytes in RAM still leaves
// boxmem.RAMStageFloor of MemAvailable, the same floor the agent's own RAM-first
// OTA keeps. An unknown MemAvailable (an old kernel without the field, or a
// read that failed) does not block: the df checks and the post-write verify
// still apply, as they always did.
func repairRAMRoomFor(memAvailable int64, known bool, rawNeed int64) bool {
	return !known || memAvailable-rawNeed >= boxmem.RAMStageFloor
}

// boxMemAvailable reads MemAvailable from the speaker's /proc/meminfo over SSH,
// in bytes. ok is false when it cannot be read or parsed.
func boxMemAvailable(host string) (int64, bool) {
	out, err := boxSSHOutput(host, "grep MemAvailable: /proc/meminfo 2>/dev/null", 30*time.Second)
	if err != nil {
		return 0, false
	}
	return parseMemAvailable(out)
}

// parseMemAvailable pulls the MemAvailable figure (kB in /proc/meminfo) out of
// command output, in bytes. Tolerates SSH noise lines around it.
func parseMemAvailable(out string) (int64, bool) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "MemAvailable:" {
			kb, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil || kb < 0 {
				return 0, false
			}
			return kb * 1024, true
		}
	}
	return 0, false
}

// stopSTRScript stops the speaker's running STR agent, its Spotify engine and
// the run.sh watchdogs, so their RAM is free for the install. Removing the
// pidfile first is what ends the watchdog loops (both break when it is gone),
// so they do not respawn the agent; the second pass catches a respawn that a
// watchdog had already decided on in the instant before.
//
// A watchdog that read the pidfile just before the rm, while the agent was
// already dead (likely on exactly the speakers this runs on, whose agent is
// not staying up), still respawns after wait_ports_clear, and the new agent
// writes a new pidfile that keeps both loops going. The sweep after the
// second pass catches that: it keeps removing the pidfile and killing for up
// to 30 s, and ends early once nothing has run for 8 s in a row (the respawn
// follows the kill within a few seconds, as soon as the ports are free). The
// install replaces this agent anyway and the reboot after it starts
// everything again.
//
// Prints STR_FOUND when STR was running before the stop (a stock speaker on
// its first install runs none and must not be restarted for it),
// STR_RESPAWN_KILLED when the sweep caught a respawn, STR_STILL_RUNNING when
// one survived it, and STR_STOPPED last so the caller can tell the script ran.
const stopSTRScript = `found=0
pidof streborn-armv7l go-librespot >/dev/null 2>&1 && found=1
rm -f /mnt/nv/streborn/agent.pid
killall streborn-armv7l go-librespot 2>/dev/null
sleep 6
rm -f /mnt/nv/streborn/agent.pid
killall -9 streborn-armv7l go-librespot 2>/dev/null
sleep 1
i=0
quiet=0
while [ $i -lt 30 ] && [ $quiet -lt 8 ]; do
if pidof streborn-armv7l go-librespot >/dev/null 2>&1; then
echo STR_RESPAWN_KILLED
rm -f /mnt/nv/streborn/agent.pid
killall -9 streborn-armv7l go-librespot 2>/dev/null
quiet=0
else
quiet=$((quiet+1))
fi
sleep 1
i=$((i+1))
done
pidof streborn-armv7l go-librespot >/dev/null 2>&1 && echo STR_STILL_RUNNING
[ $found = 1 ] && echo STR_FOUND
echo STR_STOPPED`

// strStopResult is what stopSTRScript reported.
type strStopResult struct {
	// found: STR was running on the speaker before the stop.
	found bool
	// respawned: the sweep caught a watchdog respawn and killed it.
	respawned bool
	// stillRunning: a process survived the sweep, so the memory re-read
	// after the stop may not see all the RAM the stop was meant to free.
	stillRunning bool
}

// parseSTRStop reads stopSTRScript's markers.
func parseSTRStop(out string) strStopResult {
	return strStopResult{
		found:        strings.Contains(out, "STR_FOUND"),
		respawned:    strings.Contains(out, "STR_RESPAWN_KILLED"),
		stillRunning: strings.Contains(out, "STR_STILL_RUNNING"),
	}
}

// stopSTROnBox runs stopSTRScript over SSH. The budget covers the script's
// worst case (7 s of passes plus the 30 s sweep) with room for a slow SSH
// connect.
func stopSTROnBox(host string) (strStopResult, error) {
	out, err := boxSSHOutput(host, stopSTRScript, 75*time.Second)
	r := parseSTRStop(out)
	if err != nil {
		return r, fmt.Errorf("stop STR on speaker: %s", classifySSHError(out, err))
	}
	if !strings.Contains(out, "STR_STOPPED") {
		return r, fmt.Errorf("stop STR on speaker: script did not complete")
	}
	return r, nil
}

// repairMemForStage reads the speaker's MemAvailable and, when it is too low to
// stage rawNeed bytes in RAM, stops the speaker's own STR agent, engine and
// watchdogs (the install replaces them anyway, and that frees 20-40 MB) and
// reads it again. It does NOT fall back to the NAND for room: on a 122 MB ST20
// the staged agent plus install.sh's own .new copy of it do not fit next to the
// old binary, so /mnt/nv stays the last resort it always was.
//
// stopped reports that the agent was stopped, so a caller whose install then
// does not run to its reboot restarts the speaker itself: otherwise the old
// agent would stay down until somebody pulls the plug. It stays false on a
// speaker that ran no STR at all (a first install on a stock speaker), which
// has nothing to bring back and must not be restarted for it. A stop whose
// outcome is unknown (the SSH call failed) counts as stopped: a needless
// restart is cheap, a stranded speaker is not.
func (a *App) repairMemForStage(host string, rawNeed int64) (mem int64, known, stopped bool) {
	seams := a.installSeams()
	mem, known = seams.repairMemAvailable(host)
	if repairRAMRoomFor(mem, known, rawNeed) {
		return mem, known, false
	}
	a.logger.Warn("repair_ssh: speaker RAM too short to stage the install, stopping STR's own agent and engine first",
		"host", host, "memAvailableBytes", mem, "rawNeed", rawNeed, "floorBytes", boxmem.RAMStageFloor)
	sr, err := seams.repairStopSTR(host)
	stopped = sr.found || err != nil
	if err != nil {
		a.logger.Warn("repair_ssh: could not stop STR on the speaker", "host", host, "err", err)
	}
	a.logger.Info("repair_ssh: STR stop on the speaker", "host", host,
		"strWasRunning", sr.found, "respawnKilled", sr.respawned, "stillRunning", sr.stillRunning)
	mem2, known2 := seams.repairMemAvailable(host)
	a.logger.Info("repair_ssh: speaker RAM after stopping STR", "host", host,
		"memAvailableBytes", mem2, "known", known2, "before", mem)
	if !known2 {
		// The first read worked, so keep it rather than lose the guard.
		return mem, known, stopped
	}
	return mem2, known2, stopped
}

// stageRepairFilesBestEffort stages files into the first usable on-box directory,
// preferring RAM over NAND. For each candidate it checks free space with df
// (rawNeed scaled by the candidate's compression factor, plus a small headroom):
// candidates with enough room are tried first, the rest last (fallback-first, so
// an unreadable df never blocks an attempt). The first directory that stages and
// byte-verifies wins and is returned. Errors only if every candidate failed.
//
// The RAM (tmpfs) candidates are also held to the speaker's real free memory:
// their df reports the tmpfs size cap, not what is free, and staging past
// MemAvailable restarted a 122 MB ST20 under install.sh twice (2026-10-26).
// When MemAvailable is short, STR's own agent on the speaker is stopped first
// (repairMemForStage); when it is still short, the RAM candidates are not
// tried at all and only the NAND is. stoppedSTR reports that the agent was
// stopped on the way (see repairMemForStage).
func (a *App) stageRepairFilesBestEffort(host string, rawNeed int64, files map[string][]byte) (stage string, stoppedSTR bool, err error) {
	seams := a.installSeams()
	mem, memKnown, stoppedSTR := a.repairMemForStage(host, rawNeed)
	ramOK := repairRAMRoomFor(mem, memKnown, rawNeed)
	var fits, tooSmall []string
	ramRejected := false
	for _, c := range repairStageCandidates {
		need := repairStageNeed(rawNeed, c.compress)
		free := seams.repairFreeBytes(host, c.base)
		a.logger.Info("repair_ssh: staging candidate space",
			"host", host, "dir", c.base, "freeBytes", free, "needBytes", need, "rawNeed", rawNeed,
			"memAvailableBytes", mem, "memKnown", memKnown)
		if c.ram && !ramOK {
			ramRejected = true
			a.logger.Warn("repair_ssh: skipping RAM staging candidate, speaker memory too short",
				"host", host, "dir", c.base, "memAvailableBytes", mem, "rawNeed", rawNeed, "floorBytes", boxmem.RAMStageFloor)
			continue
		}
		if free >= need {
			fits = append(fits, c.base)
		} else {
			tooSmall = append(tooSmall, c.base)
		}
	}
	var lastErr error
	allTruncated := true
	attempts := 0
	for _, base := range append(fits, tooSmall...) {
		stage := base + "/streborn-install"
		if err := seams.repairStageUpload(host, stage, files); err != nil {
			lastErr = err
			attempts++
			var trunc *uploadTruncatedError
			if !errors.As(err, &trunc) {
				allTruncated = false
			}
			a.logger.Warn("repair_ssh: staging attempt failed", "host", host, "dir", stage, "err", err)
			continue
		}
		a.logger.Info("repair_ssh: staged install files", "host", host, "dir", stage)
		return stage, stoppedSTR, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no staging directory available on the speaker")
	}
	// The RAM targets were ruled out by memory, not tried, so this is not a
	// network path that truncates everything: say what it really was.
	if ramRejected {
		return "", stoppedSTR, &repairRAMShortError{memAvailable: mem, err: lastErr}
	}
	// Every target failed the SAME way — the upload arrived short on all of them.
	// That is not a per-directory space problem (each is a different filesystem);
	// it is a network path that cannot carry the transfer, the exact signature of
	// the reverse-DNS-stall LAN this fix targets. Flag it so the caller steers the
	// user to the USB-stick install, which does not depend on the network at all.
	if allTruncated && attempts > 0 {
		return "", stoppedSTR, &allTargetsTruncatedError{err: lastErr}
	}
	return "", stoppedSTR, lastErr
}

// repairStageNeed is the free space a staging candidate must report to count
// as having room for rawNeed bytes of install files: the raw size scaled by
// the candidate's compression factor (repairStageCandidates), plus ~2 MB of
// headroom for the directory and filesystem overhead.
//
// It is one of three app-side "how much room does this need" formulas, and
// they do not agree: nandNeedCompressed (ota.go) credits 0.7 and adds 1 MB,
// sshPushNeedKB (ota.go) credits nothing and adds 2 MB. nandneed_test.go pins
// what each returns; do not change one without reading it.
func repairStageNeed(rawNeed int64, compress float64) int64 {
	return int64(float64(rawNeed)*compress) + (2 << 20)
}

// allTargetsTruncatedError wraps the last staging error when EVERY on-box target
// failed with a truncated upload. RepairInstallViaSSH classifies it into
// guidance that names the USB-stick install as the reliable alternative.
type allTargetsTruncatedError struct{ err error }

func (e *allTargetsTruncatedError) Error() string { return e.err.Error() }
func (e *allTargetsTruncatedError) Unwrap() error { return e.err }

// freeBytesAtPath returns the free space in bytes on the filesystem backing path
// on the box via BusyBox `df -k`, or 0 if it cannot be read or parsed (treated as
// "unknown, do not assume room"). Parsing lives in parseDfAvailBytes so it is
// unit-testable without a box.
func freeBytesAtPath(host, path string) int64 {
	// 30 s, not a snappier 8 s: on a LAN where the box's sshd stalls ~10.5 s on
	// reverse DNS (see boxssh.go), an 8 s df died mid-handshake for every target
	// and read as 0 free, so staging wrongly rejected a healthy NAND and the
	// install failed 100% (user diagnostic 2026-07-10). With the client cache the
	// probe is fast; the generous budget only matters on the first command or the
	// system-ssh fallback, where it must clear one slow handshake.
	out, err := boxSSHOutput(host, "df -k "+path+" 2>/dev/null", 30*time.Second)
	if err != nil {
		return 0
	}
	return parseDfAvailBytes(out)
}

// parseDfAvailBytes pulls the Available column (returned in bytes) out of
// `df -k` output. It reads Available as the 3rd field from the end of the last
// line, which is correct both for the normal one-line row
// ("fs blocks used avail use% mount") and for BusyBox's wrapped form, where a
// long device name pushes the numbers onto the next line
// ("blocks used avail use% mount"): in both, Available is followed by exactly
// Use% and the mountpoint. Returns 0 on any parse failure.
func parseDfAvailBytes(out string) int64 {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0 // header only, or nothing
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 5 {
		return 0
	}
	availKB, err := strconv.ParseInt(fields[len(fields)-3], 10, 64)
	if err != nil || availKB < 0 {
		return 0
	}
	return availKB * 1024
}
