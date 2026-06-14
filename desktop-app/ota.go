// Over-the-air agent update for the desktop app: read the box's current agent
// version, push the embedded ARM binary, and bring the box back cleanly. The
// HTTP path (/api/agent/update) is used where the STR webui is reachable; the
// SSH path is the fallback for Series-I boxes whose only LAN-reachable HTTP
// listener is Bose's SoftwareUpdate with its 1.5 KB POST cap. The stick is
// refreshed (mount + fsck + binary copy) before the reboot so the NAND boot
// sync does not revert the box to the pre-OTA binary.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/sticksetup"
	"streborn-app/agentbin"
)

// BoxAgentVersion fragt die Stick Agent Version der Box ab.
// Returns {version, build}. Uses boxDo so it tries BOTH agent ports (:8888 and
// the :17008 redirect) with the self-healing cache, instead of forcing one port.
// This matters on rhino ST10s where the Bose firewall blocks :8888: the version
// probe (and the box-update banner) would otherwise read the box as unreachable
// even when its agent answers on the alternate port.
func (a *App) BoxAgentVersion(host string, port int) (map[string]string, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/agent/version", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateBoxAgent ships the embedded ARM binary to the speaker. Preferred
// path is HTTP POST to /api/agent/update on host:port — but only when a
// preflight confirms STR's agent really answers there. On Series-I boxes
// (scm/spotty, taigan) where the LD_PRELOAD shim has not hijacked
// SoftwareUpdate's :17008 listener, that port belongs to Bose's own
// SoftwareUpdate HTTP service. Its work buffer is 1.5 KB, and the 10 MB
// agent binary returns "Request Too Large" (verified live 2026-05-30 on
// a scm/spotty ST20 diagnostic bundle — see [[bose-http-buffer]]).
//
// On preflight failure we fall back to SSH: stream the binary via stdin
// into /mnt/nv/streborn/bin/streborn-armv7l.new, size-verify, atomic-
// rename, SIGTERM the running agent. run.sh's boot watchdog respawns it
// from the new file within seconds.
func (a *App) UpdateBoxAgent(host string, port int) error {
	bin := agentbin.Bytes()
	if len(bin) == 0 {
		return fmt.Errorf("no embedded stick binary available")
	}
	// Refresh the USB stick BEFORE the OTA push, while the box is still fully up.
	// The push reboots the box ~1s after the binary swap (updateAgentViaSSH), and
	// doing the stick write afterwards raced that reboot: the going-down
	// filesystem failed the write mid-set with an "Input/output error" (live
	// 2026-06-11), so the stick kept its old files and the next boot's
	// stick->NAND sync reverted the freshly OTA'd binary. Done first, the stick
	// carries the new file set before the reboot, so the boot-sync keeps NAND on
	// the new version. Best-effort: a stick failure does not block the OTA. The
	// post-OTA discovery pin is set here too so it covers the whole window.
	a.notePostOTA(host)
	a.refreshStick(host)

	if perr := a.updateAgentPreflight(host, port); perr != nil {
		a.logger.Warn("update agent: HTTP preflight rejected, switching to SSH-OTA",
			"host", host, "port", port, "reason", perr)
		if sshErr := a.updateAgentViaSSH(host, bin); sshErr != nil {
			return fmt.Errorf("HTTP preflight rejected the listener at :%d and SSH fallback also failed: %w (preflight: %v)", port, sshErr, perr)
		}
		a.logger.Info("update agent: SSH-OTA succeeded", "host", host, "bytes", len(bin))
		return nil
	}
	if err := a.updateAgentViaHTTP(host, port, bin); err != nil {
		// The small preflight GET can pass while the 10 MB POST still fails: on
		// BCO the :17008 REDIRECT path closes the connection mid-upload (live
		// 2026-06-11, "connection forcibly closed"). Fall back to SSH-OTA instead
		// of surfacing the error. SSH writes .new and size-verifies before the
		// atomic rename, so a mid-upload failure never corrupts the live binary.
		a.logger.Warn("update agent: HTTP OTA failed, falling back to SSH-OTA", "host", host, "err", err)
		if sshErr := a.updateAgentViaSSH(host, bin); sshErr != nil {
			// A transport-level HTTP drop (connection forcibly closed / reset /
			// EOF) is what a SUCCESSFUL self-replacing OTA looks like: the agent
			// received the binary and restarted, closing the socket before it
			// could answer 200, and that restart reboots the box, which is why the
			// SSH fallback then raced the reboot and also failed. The box is on
			// its way to the new version (the caller polls /api/agent/version to
			// confirm, and refreshStick wrote the new files to the stick as a
			// stick->NAND safety net). Reporting a hard failure here produced the
			// false "Update failed, unplug the speaker" that appeared right before
			// the reboot that updated the box fine. Only a CLEAN HTTP rejection
			// (status >= 400, binary refused so it definitely did not apply) plus
			// an SSH failure is a real, report-worthy failure.
			if isConnDropErr(err) {
				a.logger.Info("update agent: HTTP connection dropped (agent restarting) and SSH raced the reboot; deferring to the post-OTA version poll", "host", host, "httpErr", err, "sshErr", sshErr)
				return nil
			}
			return fmt.Errorf("HTTP OTA failed (%v) and the SSH fallback also failed: %w", err, sshErr)
		}
		a.logger.Info("update agent: SSH-OTA succeeded after HTTP failure", "host", host, "bytes", len(bin))
	}
	return nil
}

// refreshStick is a best-effort step run as part of an agent OTA, BEFORE the
// binary swap reboots the box (see UpdateBoxAgent): if the STR USB stick is
// still inserted in the speaker, rewrite its program files (agent binary,
// run.sh, rc.local, shim, version.txt, ...) so the next boot's stick->NAND sync
// does not revert the freshly OTA'd binary back to the stick's older one
// (project_deploy_stick_overwrites_nand). The write is durable-flushed so it
// survives the reboot (project_durable_stick_write). Never fatal: a failure here
// is logged and the OTA still proceeds.
func (a *App) refreshStick(host string) {
	// Locate the stick and make sure it is mounted before writing. Some
	// speakers (the Portable, live 2026-06-11) do NOT auto-mount the USB stick
	// at /media/sda1 after boot: /dev/sda1 was present and carried the full STR
	// file set, but nothing was mounted there, so the old probe
	// "test -f /media/sda1/install.sh" wrongly concluded "no stick" and skipped
	// the refresh. The stick then kept its OLD program files and the next boot's
	// stick->NAND sync reverted the freshly OTA'd binary, leaving the box on the
	// old version (so the update appeared not to "stick" and the app's
	// version-poll never confirmed). Mount it ourselves so the refresh runs.
	mp, dev, ok := a.mountStick(host)
	if !ok {
		a.logger.Info("OTA stick refresh: no STR stick found on the box, nothing to refresh", "host", host)
		return
	}
	v := appVersion
	if appBuild != "" && appBuild != "dev" {
		v = appVersion + "+" + appBuild
	}
	files, err := sticksetup.StickFileSet(agentbin.Bytes(), v)
	if err != nil {
		a.logger.Warn("OTA stick refresh: could not assemble stick file set", "err", err)
		a.unmountStick(host, mp, dev)
		return
	}
	for name, data := range files {
		// File names in the stick set are flat and safe (alphanumeric, '.', '-').
		if out, werr := boxSSHUploadStdin(host, "cat > "+mp+"/"+name, bytes.NewReader(data), 120*time.Second); werr != nil {
			a.logger.Warn("OTA stick refresh: write failed, stick left as-is (OTA still applied to NAND)",
				"file", name, "err", werr, "out", strings.TrimSpace(out))
			a.unmountStick(host, mp, dev)
			return
		}
	}
	a.logger.Info("OTA stick refresh: stick rewritten", "host", host, "mount", mp, "files", len(files), "version", v)
	// Durable flush + detach so the writes survive the imminent reboot.
	a.unmountStick(host, mp, dev)
}

// mountStick locates the STR USB stick on the box and ensures it is mounted,
// returning the mount point and backing block device (e.g. /dev/sda1). It
// reuses an existing vfat mount that already carries install.sh; otherwise it
// mounts the first candidate vfat partition that does at /tmp/str-stick. ok is
// false when no STR stick is present. The mount persists across the SSH session
// so the subsequent per-file writes reach it.
func (a *App) mountStick(host string) (mountPoint, device string, ok bool) {
	// POSIX-sh (busybox) script: no awk/bashisms. For each candidate stick
	// device, unmount any existing mount, fsck it, then mount fresh at
	// /tmp/str-stick and keep the one that carries install.sh.
	//
	// The fsck is essential (live 2026-06-11, Portable): the speaker does NOT
	// cleanly unmount the stick, so its FAT is left dirty ("Volume was not
	// properly unmounted"). A fresh RW mount is fine for a small write, but the
	// 11 MB agent binary trips a FAT inconsistency partway and the default
	// errors=remount-ro flips the filesystem read-only, so the next file write
	// fails with "Input/output error" and the stick is left half-updated. The
	// box then boot-syncs its OLD files back onto NAND, reverting the OTA. A
	// fsck.vfat -a clears the dirty FAT first so the whole set writes cleanly
	// (verified live: an 11 MB write that failed before succeeded after fsck).
	const script = `PATH=/usr/sbin:/sbin:/usr/bin:/bin:$PATH
MP=""; DEV=""
mkdir -p /tmp/str-stick
for dev in /dev/sda1 /dev/sdb1 /dev/sdc1 /dev/sda /dev/sdb; do
  [ -b "$dev" ] || continue
  cur=$(grep "^$dev " /proc/mounts | cut -d' ' -f2 | head -1)
  if [ -n "$cur" ]; then
    umount "$cur" 2>/dev/null
    # Still mounted (umount busy)? Use that mount as-is; do NOT fsck a mounted FS
    # or mount the same device twice (a double vfat mount corrupts writes).
    cur2=$(grep "^$dev " /proc/mounts | cut -d' ' -f2 | head -1)
    if [ -n "$cur2" ]; then
      if [ -f "$cur2/install.sh" ]; then MP="$cur2"; DEV="$dev"; break; fi
      continue
    fi
  fi
  # Device is unmounted now: fsck the (possibly dirty) FAT, then mount fresh.
  umount /tmp/str-stick 2>/dev/null
  fsck.vfat -a "$dev" >/dev/null 2>&1 || dosfsck -a -w "$dev" >/dev/null 2>&1
  if mount -t vfat "$dev" /tmp/str-stick 2>/dev/null; then
    if [ -f /tmp/str-stick/install.sh ]; then MP=/tmp/str-stick; DEV="$dev"; break; fi
    umount /tmp/str-stick 2>/dev/null
  fi
done
if [ -n "$MP" ]; then echo "STR_STICK_MP=$MP"; echo "STR_STICK_DEV=$DEV"; else echo STR_STICK_NONE; fi`
	out, err := boxSSHOutput(host, script, 30*time.Second)
	if err != nil {
		a.logger.Info("OTA stick refresh: stick probe/mount failed", "host", host, "err", err, "out", strings.TrimSpace(out))
		return "", "", false
	}
	mp := lineValue(out, "STR_STICK_MP=")
	dev := lineValue(out, "STR_STICK_DEV=")
	if mp == "" {
		return "", "", false
	}
	a.logger.Info("OTA stick refresh: stick mounted", "host", host, "mount", mp, "device", dev)
	return mp, dev, true
}

// unmountStick flushes the stick writes to flash and detaches it so they survive
// the imminent post-OTA reboot: a plain write to a vfat stick sits in the USB
// controller cache and is otherwise lost (project_durable_stick_write). The
// SYNCHRONIZE CACHE + STOP UNIT via the block device's "delete" node commits it;
// the stick re-mounts (updated) on the next boot, when the stick->NAND sync runs.
func (a *App) unmountStick(host, mountPoint, device string) {
	cmd := "sync; umount " + mountPoint + " 2>/dev/null"
	if base := blockDeviceBase(device); base != "" {
		cmd += "; echo 1 > /sys/block/" + base + "/device/delete 2>/dev/null"
	}
	_ = boxSSHFireAndForget(host, cmd, 8*time.Second)
}

// lineValue returns the text after prefix on the first line that starts with it,
// trimmed, or "".
func lineValue(out, prefix string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// blockDeviceBase maps a partition device path to its whole-disk basename for the
// /sys/block/<name>/device/delete flush: "/dev/sda1" -> "sda". Returns "" for an
// empty or unrecognised device.
func blockDeviceBase(device string) string {
	d := strings.TrimPrefix(device, "/dev/")
	if d == "" || strings.ContainsAny(d, "/ ") {
		return ""
	}
	// Strip a trailing partition number (sda1 -> sda). USB sticks are sd*.
	return strings.TrimRight(d, "0123456789")
}

// isConnDropErr reports whether an HTTP OTA error is a transport-level
// connection drop (the agent restarting after it accepted the binary, or a BCO
// :17008 redirect closing mid-stream) rather than a clean HTTP rejection.
// updateAgentViaHTTP returns a "status N: ..." error only for a real >= 400
// response (binary received and refused, so it definitely did not apply);
// anything else came from client.Do and is a drop. On a drop the OTA may well
// have landed, so the outcome is decided by the post-OTA version poll, not by
// surfacing a hard failure.
func isConnDropErr(err error) bool {
	if err == nil {
		return false
	}
	return !strings.Contains(err.Error(), "status ")
}

// updateAgentPreflight checks that /api/agent/version on host:port really
// answers as STR (JSON envelope containing a "version" key). A success
// here is the green light for the 10 MB HTTP POST. Any other response
// shape (HTML error, plain text, missing field, non-200) means the
// listener is something else — almost certainly Bose's SoftwareUpdate
// service on a Series-I box without an active shim, where the 1.5 KB
// POST buffer guarantees failure.
func (a *App) updateAgentPreflight(host string, port int) error {
	// boxDo tries both agent ports (:8888 and the :17008 redirect) and caches the
	// one that answers, so the subsequent updateAgentViaHTTP POST (which builds its
	// URL via baseURL -> cachedPort) targets that same working port. Forcing :8888
	// here would wrongly reject a box that only answers on the alternate port.
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/agent/version", "", "")
	if err != nil {
		return fmt.Errorf("GET /api/agent/version on %s: %w", host, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	snip := string(body)
	if len(snip) > 200 {
		snip = snip[:200] + "..."
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d on %s — body=%q", resp.StatusCode, host, snip)
	}
	var probe map[string]any
	if jerr := json.Unmarshal(body, &probe); jerr != nil || probe["version"] == nil {
		return fmt.Errorf("listener on %s is not STR (ct=%q body=%q) — likely Bose SoftwareUpdate, agent OTA via HTTP would hit the 1.5 KB POST buffer",
			host, resp.Header.Get("Content-Type"), snip)
	}
	return nil
}

func (a *App) updateAgentViaHTTP(host string, port int, bin []byte) error {
	url := a.baseURL(host, port) + "/api/agent/update"
	req, err := http.NewRequestWithContext(a.appCtx(), http.MethodPost, url, strings.NewReader(string(bin)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(bin))
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// updateAgentViaSSH streams bin into /mnt/nv/streborn/bin/streborn-armv7l
// over SSH, then SIGTERMs the running agent so run.sh's watchdog respawns
// it from the new file. Each step's failure is reported with concrete
// context so the desktop's error toast tells the user what to look at
// instead of "ssh: exit 1".
func (a *App) updateAgentViaSSH(host string, bin []byte) error {
	// 4 spaced attempts (sshHandshake): the OTA path runs when the box is
	// busiest, exactly where the one-shot 8 s attempt used to flake (#114).
	if hello, err := sshHandshake(host, 4); err != nil || !strings.Contains(hello, "STR_SSH_OK") {
		return fmt.Errorf("ssh handshake failed: %v (%s)", err, strings.TrimSpace(hello))
	}
	// mkdir + cat in one ssh-with-stdin session so a missing parent dir on
	// a freshly-installed box does not need a separate round-trip. The
	// 120 s timeout covers a 10 MB upload over slow Wi-Fi with the
	// speaker's modest CPU spending time on SSH crypto.
	uploadCmd := "mkdir -p /mnt/nv/streborn/bin && cat > /mnt/nv/streborn/bin/streborn-armv7l.new"
	if out, err := boxSSHUploadStdin(host, uploadCmd, bytes.NewReader(bin), 120*time.Second); err != nil {
		return fmt.Errorf("ssh upload (%d bytes) failed: %v (%s)", len(bin), err, strings.TrimSpace(out))
	}
	// Size-verify before the atomic rename so a half-uploaded file never
	// becomes the live agent. Sentinel "OK_<size>" so the caller can
	// distinguish a successful rename from any stderr noise.
	verifyCmd := fmt.Sprintf(
		"size=$(wc -c < /mnt/nv/streborn/bin/streborn-armv7l.new) && "+
			"[ \"$size\" = \"%d\" ] && "+
			"chmod 0755 /mnt/nv/streborn/bin/streborn-armv7l.new && "+
			"mv /mnt/nv/streborn/bin/streborn-armv7l.new /mnt/nv/streborn/bin/streborn-armv7l && "+
			"echo OK_%d", len(bin), len(bin))
	sentinel := fmt.Sprintf("OK_%d", len(bin))
	if out, err := boxSSHOutput(host, verifyCmd, 15*time.Second); err != nil || !strings.Contains(out, sentinel) {
		return fmt.Errorf("ssh size-verify or rename failed: %v (%s)", err, strings.TrimSpace(out))
	}
	// Always reboot after the binary swap. Jens 2026-06-01: an OTA that
	// only SIGTERMs the agent and relies on run.sh's watchdog respawn
	// leaves a dirty post-update state. The new binary came up but the
	// app showed no presets, because the boot-time preset push and the
	// leave-OOB full re-sync (cmd/agent reconcileOnce forceFull) only run
	// on a real boot, not on a live process restart; and OTA replaces
	// only the binary, so the NAND run.sh + rc.local otherwise stay at
	// the pre-OTA vintage (project_ota_only_replaces_binary). A clean
	// reboot fixes both: the new binary self-deploys its matching
	// run.sh/rc.local on boot AND the preset reconcile runs from clean.
	// Detached so the SSH session returns before the box drops off the
	// LAN. sync first so the just-renamed binary is flushed to NAND.
	// boxReboot is the shared hardened form (this OTA path is where that
	// form originated).
	_ = boxReboot(host)
	return nil
}
