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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/sticksetup"
	"streborn-app/agentbin"
)

// BoxAgentVersion queries the box's Stick Agent version.
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
func (a *App) UpdateBoxAgent(host string, port int) (err error) {
	bin := agentbin.Bytes()
	if len(bin) == 0 {
		a.recordOTA(host, "start: aborted, no embedded agent binary in this build")
		return fmt.Errorf("no embedded stick binary available")
	}
	a.recordOTA(host, fmt.Sprintf("start: port=%d bytes=%d app=%s build=%s", port, len(bin), appVersion, appBuild))
	// Record the final outcome to the persistent OTA journal so an update-failure
	// report is diagnosable even if the user exports the diagnostic in a later
	// session: str.log is rotated away on the next launch, this journal is not.
	defer func() {
		if err != nil {
			a.recordOTA(host, "outcome: reported failure: "+err.Error())
		} else {
			a.recordOTA(host, "outcome: no error (applied, or deferred to the version poll)")
		}
	}()
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

	// Deliver the go-librespot Spotify sidecar over OTA too, so a box that was
	// never successfully synced from a USB stick (e.g. an OTA-only SoundTouch 30
	// whose USB port underpowers the stick) still gets Spotify. The sidecar
	// historically shipped ONLY via the stick->NAND boot sync, so an OTA-only box
	// had a synced login but no engine and Spotify silently never played
	// (#45/#105). Pushed FIRST and strictly before the agent OTA: the agent OTA
	// reboots ~1.5 s after its reply, and that reboot is what makes the fresh
	// agent's Spotify manager pick up the now-present binary. Best-effort: a
	// sidecar failure must never block the (more important) agent update.
	a.pushSidecarIfNeeded(host, port)

	if perr := a.updateAgentPreflight(host, port); perr != nil {
		a.recordOTA(host, "HTTP preflight rejected -> trying SSH: "+perr.Error())
		a.logger.Warn("update agent: HTTP preflight rejected, switching to SSH-OTA",
			"host", host, "port", port, "reason", perr)
		if sshErr := a.updateAgentViaSSH(host, bin); sshErr != nil {
			// A preflight TIMEOUT (slow link, or an HTTP-inspecting security
			// suite such as Norton stalling the probe) is not proof the box is
			// unupdatable. If SSH is also unavailable, defer to the caller's
			// post-OTA version poll rather than surfacing a raw "context
			// deadline exceeded" failure (the box may still be reachable and
			// updatable on a later, faster attempt). This is what two reporters
			// hit: the agent was healthy yet the app showed "Update failed".
			if isTimeoutLikeErr(perr) {
				a.logger.Info("update agent: preflight timed out and SSH unavailable; deferring to the post-OTA version poll", "host", host, "preflightErr", perr, "sshErr", sshErr)
				return nil
			}
			return fmt.Errorf("HTTP preflight rejected the listener at :%d and SSH fallback also failed: %w (preflight: %v)", port, sshErr, perr)
		}
		a.logger.Info("update agent: SSH-OTA succeeded", "host", host, "bytes", len(bin))
		return nil
	}
	if err := a.updateAgentViaHTTP(host, port, bin); err != nil {
		a.recordOTA(host, "HTTP upload failed -> trying SSH: "+err.Error())
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

// pushSidecarIfNeeded delivers the embedded go-librespot Spotify sidecar to the
// box over HTTP (POST /api/agent/sidecar) when the box is missing it or has a
// different build. Strictly best-effort: any failure is logged to the OTA
// journal and swallowed so it never aborts the agent OTA that follows. The
// ~10 MB transfer is gated on the box's reported content hash so a steady-state
// OTA (agent-only change, sidecar already current) costs just one cheap version
// GET and sends zero sidecar bytes.
func (a *App) pushSidecarIfNeeded(host string, port int) {
	if !agentbin.GoLibrespotAvailable() {
		// Dev build (empty stub): nothing real to deliver. Never write 0 bytes.
		a.recordOTA(host, "sidecar: skipped, no embedded go-librespot in this build")
		return
	}
	bin := agentbin.GoLibrespotBytes()
	sum := sha256.Sum256(bin)
	want := hex.EncodeToString(sum[:])

	// Ask the box what it already has. boxDo self-heals across :8888/:17008 and
	// caches the working port for the agent OTA that follows.
	if ver, err := a.BoxAgentVersion(host, port); err == nil {
		if ver["goLibrespot"] == "present" && ver["goLibrespotSha256"] == want {
			a.recordOTA(host, "sidecar: box already has the current go-librespot, skipping ~10 MB push")
			return
		}
	} else {
		// Couldn't read the version: still try the push (a missing sidecar is the
		// whole reason this exists); worst case the POST also fails and is logged.
		a.logger.Info("sidecar push: version probe failed, pushing anyway", "host", host, "err", err)
	}

	a.recordOTA(host, fmt.Sprintf("sidecar: pushing go-librespot (%d bytes, sha %s)", len(bin), want[:12]))
	if err := a.streamPostBinary(host, port, "/api/agent/sidecar", bin); err != nil {
		a.recordOTA(host, "sidecar: push failed (agent OTA still proceeds): "+err.Error())
		a.logger.Warn("sidecar push failed; agent OTA continues, Spotify may stay unavailable until next OTA", "host", host, "err", err)
		return
	}
	a.recordOTA(host, "sidecar: go-librespot delivered")
	a.logger.Info("sidecar push: go-librespot delivered over OTA", "host", host, "bytes", len(bin))
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
		// mountStick already logged the specific reason (probe/mount failure, or
		// no stick found with the block devices it did see).
		return
	}
	v := appVersion
	if appBuild != "" && appBuild != "dev" {
		v = appVersion + "+" + appBuild
	}
	files, err := sticksetup.StickFileSet(agentbin.Bytes(), agentbin.GoLibrespotBytes(), v)
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
	// A stick that has sat in the box since a previous session is sometimes not
	// re-enumerated as a block node after a reboot/standby, so the device probe
	// found nothing and we wrongly logged "no stick" (Jens, live 2026-06-17:
	// "USB stick detection is still unreliable; the inserted stick may need
	// re-mounting before the OTA"). So before probing we nudge the SCSI layer to
	// rescan, then run the device loop up to 3 times with a 1 s settle so a stick
	// that appears a beat late is still caught. The marker check matches the
	// agent's stickReallyMounted (#179): any STR program file, not just
	// install.sh, so a stick written by any sticksetup vintage is recognised. The
	// devices actually seen are echoed for diagnostics when the probe still finds
	// nothing.
	const script = `PATH=/usr/sbin:/sbin:/usr/bin:/bin:$PATH
MP=""; DEV=""; SEEN=""
mkdir -p /tmp/str-stick
# Best-effort USB/SCSI re-enumeration for a stick inserted but not yet showing
# a block node. Ignored where the scan node is absent.
for h in /sys/class/scsi_host/host*/scan; do [ -e "$h" ] && echo "- - -" > "$h" 2>/dev/null; done
has_marker() { [ -f "$1/install.sh" ] || [ -f "$1/version.txt" ] || [ -f "$1/run.sh" ] || [ -f "$1/streborn-armv7l" ]; }
attempt=1
while [ "$attempt" -le 3 ]; do
  for dev in /dev/sda1 /dev/sdb1 /dev/sdc1 /dev/sda /dev/sdb; do
    [ -b "$dev" ] || continue
    case " $SEEN " in *" $dev "*) ;; *) SEEN="$SEEN $dev" ;; esac
    cur=$(grep "^$dev " /proc/mounts | cut -d' ' -f2 | head -1)
    if [ -n "$cur" ]; then
      umount "$cur" 2>/dev/null
      # Still mounted (umount busy)? Use that mount as-is; do NOT fsck a mounted FS
      # or mount the same device twice (a double vfat mount corrupts writes).
      cur2=$(grep "^$dev " /proc/mounts | cut -d' ' -f2 | head -1)
      if [ -n "$cur2" ]; then
        if has_marker "$cur2"; then MP="$cur2"; DEV="$dev"; break; fi
        continue
      fi
    fi
    # Device is unmounted now: fsck the (possibly dirty) FAT, then mount fresh.
    umount /tmp/str-stick 2>/dev/null
    fsck.vfat -a "$dev" >/dev/null 2>&1 || dosfsck -a -w "$dev" >/dev/null 2>&1
    if mount -t vfat "$dev" /tmp/str-stick 2>/dev/null; then
      if has_marker /tmp/str-stick; then MP=/tmp/str-stick; DEV="$dev"; break; fi
      umount /tmp/str-stick 2>/dev/null
    fi
  done
  [ -n "$MP" ] && break
  attempt=$((attempt+1))
  sleep 1
done
echo "STR_STICK_SEEN=$SEEN"
if [ -n "$MP" ]; then echo "STR_STICK_MP=$MP"; echo "STR_STICK_DEV=$DEV"; else echo STR_STICK_NONE; fi`
	out, err := boxSSHOutput(host, script, 45*time.Second)
	if err != nil {
		a.logger.Info("OTA stick refresh: stick probe/mount failed", "host", host, "err", err, "out", strings.TrimSpace(out))
		return "", "", false
	}
	mp := lineValue(out, "STR_STICK_MP=")
	dev := lineValue(out, "STR_STICK_DEV=")
	if mp == "" {
		// Surface which block devices were present so a bundle shows whether the
		// stick simply was not inserted vs. inserted-but-unreadable.
		a.logger.Info("OTA stick refresh: no STR stick found on the box, nothing to refresh",
			"host", host, "devicesSeen", strings.TrimSpace(lineValue(out, "STR_STICK_SEEN=")))
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

// isTimeoutLikeErr reports whether an OTA error is a transport timeout or
// connection drop rather than a definite rejection. A timeout (including the
// "context deadline exceeded (Client.Timeout or context cancellation while
// reading body)" text Go emits, common when a slow link or an HTTP-inspecting
// security suite such as Norton stalls the transfer) does NOT mean the OTA
// failed: the binary may have landed and the box may be rebooting. Such cases
// are deferred to the caller's post-OTA /api/agent/version poll instead of
// being surfaced as a hard "Update failed". A clean ">= 400 status N" reply is
// the only HTTP outcome that proves the binary was refused, so it is excluded.
func isTimeoutLikeErr(err error) bool {
	if err == nil {
		return false
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	if strings.Contains(err.Error(), "status ") {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"deadline exceeded", "client.timeout", "while reading body", "timeout", "connection reset", "broken pipe"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
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
	return a.streamPostBinary(host, port, "/api/agent/update", bin)
}

// streamPostBinary POSTs bin to path on the agent, streaming the body with a
// live progress event ("box:update:progress") and the same abort policy the
// agent OTA settled on: no total deadline (a 10 MB push to a busy box over slow
// Wi-Fi can take minutes), a 60 s upload-stall watchdog, and a bounded
// post-upload reply window. Shared by the agent-binary update and the
// go-librespot sidecar push so they cannot drift on transfer behavior.
func (a *App) streamPostBinary(host string, port int, path string, bin []byte) error {
	url := a.baseURL(host, port) + path
	// No total-transfer deadline. Streaming the ~10 MB agent to a busy box over slow
	// Wi-Fi can legitimately take minutes, and the old fixed 240 s cap killed
	// still-progressing uploads with "context deadline exceeded (Client.Timeout ...
	// while reading body)". The abort conditions instead: a genuine upload stall (no
	// bytes for 60 s), the box failing to begin its reply after the upload
	// (ResponseHeaderTimeout, which covers the NAND write on the modest ARM CPU), and
	// app shutdown (appCtx). Matches the in-app self-update download.
	ctx, cancel := context.WithCancel(a.appCtx())
	defer cancel()

	total := int64(len(bin))
	// Stream the body through a counting reader so the UI can show an upload
	// percentage and live throughput (the box reads the body as we send it),
	// instead of a blind "uploading" spinner that looks frozen on a slow link.
	prog := newTransferProgress(a, "box:update:progress", total)
	beat := make(chan struct{}, 1)
	uploadDone := make(chan struct{})
	finished := false
	body := &countingReader{r: bytes.NewReader(bin), onProgress: func(n int64) {
		prog.report(n)
		if n >= total {
			// Body fully streamed. Stop the upload-stall watchdog so the box's
			// NAND-write + reply window (bounded by ResponseHeaderTimeout) is not
			// mistaken for a stall. onProgress runs on the single body-read
			// goroutine, so this flag needs no lock.
			if !finished {
				finished = true
				close(uploadDone)
			}
			return
		}
		select {
		case beat <- struct{}{}:
		default:
		}
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = total

	// Upload-stall watchdog: cancel only if no bytes move for 60 s while the body
	// is still being sent. Exits cleanly once the upload completes or the context
	// is done, so it never trips during the post-upload NAND write.
	go func() {
		t := time.NewTimer(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-uploadDone:
				return
			case <-beat:
				if !t.Stop() {
					select {
					case <-t.C:
					default:
					}
				}
				t.Reset(60 * time.Second)
			case <-t.C:
				cancel()
				return
			}
		}
	}()

	client := &http.Client{
		// No total Timeout (see above); only connect and post-upload-reply are bounded.
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ResponseHeaderTimeout: 180 * time.Second,
		},
	}
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
	//
	// Deliver the go-librespot sidecar over the SAME SSH session-class before
	// the reboot, so a box that fell to the SSH path (HTTP preflight rejected)
	// also gets Spotify. Best-effort: an SSH sidecar failure logs but never
	// fails the agent OTA. The reboot below brings up the fresh agent that picks
	// up the binary.
	a.pushSidecarViaSSH(host)
	_ = boxReboot(host)
	return nil
}

// pushSidecarViaSSH writes the embedded go-librespot sidecar to
// /mnt/nv/streborn/bin/go-librespot over SSH (the fallback delivery for boxes
// whose HTTP listener is Bose's, taken alongside updateAgentViaSSH). Mirrors the
// agent-binary upload: stream to .new, size-verify, chmod, atomic rename, then
// stamp the content hash. Best-effort: every failure is logged and swallowed.
func (a *App) pushSidecarViaSSH(host string) {
	if !agentbin.GoLibrespotAvailable() {
		return
	}
	bin := agentbin.GoLibrespotBytes()
	const dst = "/mnt/nv/streborn/bin/go-librespot"
	uploadCmd := "mkdir -p /mnt/nv/streborn/bin && cat > " + dst + ".new"
	if out, err := boxSSHUploadStdin(host, uploadCmd, bytes.NewReader(bin), 120*time.Second); err != nil {
		a.logger.Warn("sidecar SSH upload failed (agent OTA still proceeds)", "host", host, "err", err, "out", strings.TrimSpace(out))
		return
	}
	sum := sha256.Sum256(bin)
	sha := hex.EncodeToString(sum[:])
	verifyCmd := fmt.Sprintf(
		"size=$(wc -c < %s.new) && [ \"$size\" = \"%d\" ] && "+
			"chmod 0755 %s.new && mv %s.new %s && "+
			"printf %%s %s > %s.sha256 && echo OK_%d",
		dst, len(bin), dst, dst, dst, sha, dst, len(bin))
	sentinel := fmt.Sprintf("OK_%d", len(bin))
	if out, err := boxSSHOutput(host, verifyCmd, 20*time.Second); err != nil || !strings.Contains(out, sentinel) {
		a.logger.Warn("sidecar SSH size-verify/rename failed (agent OTA still proceeds)", "host", host, "err", err, "out", strings.TrimSpace(out))
		return
	}
	a.logger.Info("sidecar push (SSH): go-librespot delivered over OTA", "host", host, "bytes", len(bin))
}
