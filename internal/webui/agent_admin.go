// Agent self-administration: version, OTA update upload, NAND inventory and SSH.

package webui

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// handleAgentVersion returns the running stick agent version. Used by
// the desktop app to detect whether an update is due.
func (s *Server) handleAgentVersion(w http.ResponseWriter, _ *http.Request) {
	out := map[string]string{
		"version": agentVersion(),
		"build":   agentBuild(),
	}
	// Carry the box display name/model the agent knows so the desktop app
	// can label a flashed speaker even when its own cross-LAN /info probe
	// is momentarily slow (e.g. the busy window right after an OTA restart).
	if s.boxNameFn != nil {
		if name, model := s.boxNameFn(); name != "" {
			out["friendlyName"] = name
			if model != "" {
				out["model"] = model
			}
		}
	}
	// Report whether the go-librespot Spotify sidecar is deployed and which
	// content it is, so the desktop app can decide whether to push it over OTA
	// (it ships ~10 MB; we only want to send it when the box is missing it or
	// has a different build). The binary historically reached the box ONLY via
	// the stick->NAND boot sync, so a box that was ever only OTA-updated stayed
	// without it and Spotify silently never played (#45/#105, e.g. an OTA-only
	// SoundTouch 30). present/missing + the content hash drive that gate.
	if present, sha := goLibrespotStamp(); present {
		out["goLibrespot"] = "present"
		if sha != "" {
			out["goLibrespotSha256"] = sha
		}
		// Size of the deployed engine, so the desktop app's sidecar space
		// pre-flight can count a present (old) engine as reclaimable: the
		// sidecar write drops it before the new one lands, so an engine
		// UPDATE fits on a tight box even when the raw free figure says no.
		if fi, err := os.Stat(goLibrespotBinPath); err == nil {
			out["goLibrespotSizeBytes"] = strconv.FormatInt(fi.Size(), 10)
		}
	} else {
		out["goLibrespot"] = "missing"
		if engineDroppedForUpdate() {
			// Not "never installed": this speaker HAD the engine and it was
			// deleted to fit an agent update. Reported so the app can SAY so
			// where the user is already looking, and so a diagnostic tells the
			// two cases apart. Nothing acts on it: a binary reaches a speaker
			// only while an install or update is running (standing rule,
			// 2026-07-29), never from a background task.
			out["goLibrespotDroppedForUpdate"] = "true"
		}
	}
	// Advertise that this agent hot-swaps the Spotify engine live: it restarts
	// go-librespot in place right after a sidecar OTA write, so a freshly
	// delivered/updated engine is active without a box reboot. The desktop app
	// reads this to skip the post-delivery activation reboot it still needs for
	// older agents that bind the binary only at process start (#240).
	if s.spotifyReload != nil {
		out["engineHotSwap"] = "true"
	}
	// Box uptime, so the desktop app can sequence the post-OTA engine delivery
	// deterministically (#466): the first ~2-3 minutes after a post-OTA boot are
	// reboot-prone (Bose settling, shepherd recovery) and the first 16 MB push
	// into that window reliably died with a connection reset. The app gates
	// large pushes on "uptime past the settling window and still rising"; a
	// DROP between two probes is a reboot the reachability probe alone misses.
	if up, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(up)); len(f) > 0 {
			if secs, perr := strconv.ParseFloat(f[0], 64); perr == nil {
				out["uptimeSec"] = strconv.FormatInt(int64(secs), 10)
			}
		}
	}
	// NAND headroom on the tiny (~31 MB) writable volume, so the desktop app can
	// see before an OTA whether the ~10 MB agent + sidecar will fit and warn
	// instead of pushing into a "no space left on device" failure (the stickless
	// SoundTouch 30 case where SSH is closed and the disk state was otherwise
	// invisible). Strings to match this endpoint's map[string]string shape.
	if total, avail, ok := diskFree(nandRoot); ok {
		out["nandTotalBytes"] = strconv.FormatInt(total, 10)
		out["nandFreeBytes"] = strconv.FormatInt(avail, 10)
	}
	// Wedged-control state (see wedge.go): the desktop app and the phone
	// remote read this to tell the user a power-cycle is needed.
	if status, since := s.BoxHealth(); status != "ok" {
		out["boxHealth"] = status
		out["boxHealthSinceSec"] = strconv.FormatInt(int64(time.Since(since).Seconds()), 10)
	} else {
		out["boxHealth"] = "ok"
	}
	// Pre-latch wedge evidence (#402): the strike count and the age of the most
	// recent strike, so a diagnostic taken mid-wedge is distinguishable from a
	// healthy box even before the second strike latches boxHealth=wedged.
	if strikes, lastHit := s.BoxHealthStrikes(); strikes > 0 {
		out["boxHealthStrikes"] = strconv.Itoa(strikes)
		out["boxHealthLastStrikeSec"] = strconv.FormatInt(int64(time.Since(lastHit).Seconds()), 10)
	}
	// Two heads-up flags for the desktop app (#270), emitted only when there is
	// something to warn about so the common response stays small: a rival
	// SoundTouch tool's leftover files (they fight STR), and no STR-saved Wi-Fi
	// (the box only stays online with the stick/cable and strands the user on the
	// next cold boot).
	if mod := detectConflictingMod(); mod != "" {
		out["conflictingMod"] = mod
	}
	// Ongoing 1036 storm: the box is refusing essentially every recall, so
	// nothing the user presses will play until the state is cleared. Emitted
	// only while it lasts, and carrying the age so the app can say how long it
	// has been going.
	if s.storm1036Fn != nil {
		if active, count, since := s.storm1036Fn(); active {
			out["preset1036Storm"] = "active"
			out["preset1036Count"] = strconv.Itoa(count)
			if !since.IsZero() {
				out["preset1036SinceSec"] = strconv.FormatInt(int64(time.Since(since).Seconds()), 10)
			}
		}
	}
	// Silent-refusal latch: the 1036 storm's quiet sibling (the box drops its
	// source on its own for every recall without ever sending a 1036). Same
	// remedy, so the desktop app joins it into the storm banner.
	if active, since := s.RecallRefusal(); active {
		out["presetRefusal"] = "active"
		if !since.IsZero() {
			out["presetRefusalSinceSec"] = strconv.FormatInt(int64(time.Since(since).Seconds()), 10)
		}
	}
	// The foreign (neither STR's nor Bose's) top-level /mnt/nv dirs, names only.
	// Cheap: one readdir, no recursive sizing, so it is fine on every version
	// poll. These are leftovers of OTHER SoundTouch mods that eat the NAND the
	// Spotify engine needs; the desktop app names them when it has to tell the
	// user to free space (#270 / Spotify-engine space-fail UX). Emitted only when
	// something foreign exists, to keep the common response small.
	if fd := foreignNANDDirNames(); len(fd) > 0 {
		out["foreignDirs"] = strings.Join(fd, ",")
	}
	if wlanCredsWarningWarranted() {
		out["wlanCreds"] = "missing"
	}
	// The ON-DISK agent binary's hash (the running build is in version/build
	// above). Together they let the desktop app tell "the binary landed but
	// the box still runs the old version" (a durability rollback, #381) apart
	// from "the push never arrived", and stop re-pushing into a reboot loop.
	if sha := agentBinaryStamp(); sha != "" {
		out["agentBinarySha256"] = sha
	}
	// A failed tier-3 RAM-staged swap leaves a marker instead of rebooting
	// into a silently-old binary; surface it so the failure is visible on a
	// stickless box where nothing else is.
	if msg, err := os.ReadFile(swapFailMarker); err == nil && len(msg) > 0 {
		out["otaSwapFailed"] = strings.TrimSpace(string(msg))
	}
	writeJSON(w, http.StatusOK, out)
}

// agentBinNANDPath is the NAND path of the agent binary — the only binary a
// stickless boot ever executes, and the OTA write target.
const agentBinNANDPath = "/mnt/nv/streborn/bin/streborn-armv7l"

// agentBinShaCache memoizes the on-disk agent binary hash keyed by
// mtime+size, so the frequent version polls (every 2 s during an OTA) do not
// re-hash 12.5 MB on the weak box CPU. Re-hashes only when the file changes.
var agentBinShaCache struct {
	sync.Mutex
	mtime time.Time
	size  int64
	sha   string
}

// agentBinaryStamp returns the hex SHA256 of the agent binary on NAND, or ""
// when it cannot be read.
func agentBinaryStamp() string {
	fi, err := os.Stat(agentBinNANDPath)
	if err != nil {
		return ""
	}
	agentBinShaCache.Lock()
	defer agentBinShaCache.Unlock()
	if agentBinShaCache.sha != "" &&
		fi.ModTime().Equal(agentBinShaCache.mtime) &&
		fi.Size() == agentBinShaCache.size {
		return agentBinShaCache.sha
	}
	b, err := os.ReadFile(agentBinNANDPath)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	agentBinShaCache.mtime = fi.ModTime()
	agentBinShaCache.size = fi.Size()
	agentBinShaCache.sha = hex.EncodeToString(sum[:])
	return agentBinShaCache.sha
}

// goLibrespotBinPath is where the agent runs the Spotify sidecar from; the
// desktop OTA writes it here too. Kept in lock-step with cmd/agent's
// goLibrespotPath and usb-stick/run.sh's NAND copy.
const goLibrespotBinPath = "/mnt/nv/streborn/bin/go-librespot"

// engineDroppedMarkerPath records that the Spotify engine was deleted to make
// room for an agent update, as opposed to never having been installed.
//
// The two look identical from outside (goLibrespot="missing"), and the
// difference decides whether anything should act: an engine that was dropped
// FOR an update is expected back, and if the app is closed before its post-OTA
// re-delivery finishes, nothing ever notices and the speaker silently loses
// Spotify for good. That is the shape of repeated field reports. With the
// marker the speaker can say "mine was taken away, please send it again", and
// it survives the reboot because it lives on NAND next to the binary.
const engineDroppedMarkerPath = "/mnt/nv/streborn/engine-dropped-for-update"

// noteEngineDroppedForUpdate / clearEngineDroppedMarker maintain that marker.
// Both are best effort: the marker is a hint, never a gate.
func noteEngineDroppedForUpdate() {
	_ = os.WriteFile(engineDroppedMarkerPath, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

func clearEngineDroppedMarker() {
	_ = os.Remove(engineDroppedMarkerPath)
}

func engineDroppedForUpdate() bool {
	_, err := os.Stat(engineDroppedMarkerPath)
	return err == nil
}

// goLibrespotStamp reports whether the go-librespot sidecar is deployed
// (>1 KB, i.e. a real binary not an empty stub) and the hex SHA256 of its
// contents. The hash lets the desktop app skip re-pushing the ~10 MB binary
// when the box already has the embedded build. It is cached in a sibling
// .sha256 marker so a version poll does not re-hash 10 MB on the weak box CPU
// every few minutes: the marker is trusted while it is at least as new as the
// binary; otherwise (absent, or the binary was replaced by a stick install
// that did not write a marker) it is computed once and cached. Best-effort:
// a present binary with an unreadable hash reports present with an empty sha,
// which the app treats as "push once" (correct and idempotent).
func goLibrespotStamp() (present bool, sha string) {
	fi, err := os.Stat(goLibrespotBinPath)
	if err != nil || fi.Size() < 1024 {
		return false, ""
	}
	marker := goLibrespotBinPath + ".sha256"
	if mfi, err := os.Stat(marker); err == nil && !mfi.ModTime().Before(fi.ModTime()) {
		if b, rerr := os.ReadFile(marker); rerr == nil {
			if h := strings.TrimSpace(string(b)); h != "" {
				return true, h
			}
		}
	}
	data, err := os.ReadFile(goLibrespotBinPath)
	if err != nil {
		return true, "" // present but hash unknown; app re-pushes once
	}
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	_ = os.WriteFile(marker, []byte(h), 0o644)
	return true, h
}

// handleAgentEnableSSH opens root SSH on the box on demand, so the desktop app
// can SSH in (to uninstall STR, or run diagnostics) WITHOUT a USB stick and
// WITHOUT the fragile :17000 marge-injection dance. The agent already runs here
// as root, so it just does what run.sh's ensure_sshd_running does at boot: touch
// the remote_services marker Bose's sshd init gates on, then start sshd. This is
// the clean app-first path for a box that ALREADY runs STR (the uninstall case):
// no marge check, no reboot, no autopair fight. The marker in /tmp is tmpfs, so
// SSH closes again on the next reboot - deliberately transient, since the
// uninstall's own final reboot returns the box to a stock, SSH-closed state.
// LAN-only.
func (s *Server) handleAgentEnableSSH(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "enable-ssh only allowed from LAN", http.StatusForbidden)
		return
	}
	// Bose's /etc/init.d/sshd only starts sshd when this marker is present.
	if err := os.WriteFile("/tmp/remote_services", nil, 0o644); err != nil {
		s.logger.Warn("enable-ssh: could not write remote_services marker", "err", err)
	}
	started := ensureSSHDRunning(s.logger)
	s.logger.Info("enable-ssh: on-demand SSH open requested", "sshdRunning", started)
	writeJSON(w, http.StatusOK, map[string]any{"sshd": started})
}

// sshdRunning reports whether an sshd process is alive (pidof sshd).
func sshdRunning() bool {
	out, _ := exec.Command("pidof", "sshd").Output()
	return strings.TrimSpace(string(out)) != ""
}

// ensureSSHDRunning starts sshd if it is not already up, mirroring run.sh's
// ensure_sshd_running: try the Bose init script (which needs the remote_services
// marker), then fall back to /usr/sbin/sshd directly. The init script exit code
// is untrustworthy (it prints "Not starting sshd" and still exit-0s), so success
// is decided by a real process check, not the exit code.
func ensureSSHDRunning(logger *slog.Logger) bool {
	if sshdRunning() {
		return true
	}
	if _, err := os.Stat("/etc/init.d/sshd"); err == nil {
		_ = exec.Command("/etc/init.d/sshd", "start").Run()
		if sshdRunning() {
			return true
		}
	}
	if _, err := os.Stat("/usr/sbin/sshd"); err == nil {
		_ = exec.Command("/usr/sbin/sshd").Run()
		if sshdRunning() {
			return true
		}
		logger.Warn("enable-ssh: /usr/sbin/sshd ran but no sshd process appeared")
		return false
	}
	logger.Warn("enable-ssh: no sshd init script and no /usr/sbin/sshd found")
	return false
}

// handleAgentUpdate receives a new stick agent binary, writes it
// atomically to /mnt/nv/streborn/bin/streborn-armv7l and restarts the
// agent. Body must be the raw ARM binary.
//
// On success the stick still returns 200 OK and then exits. The
// rc.local bootstrap starts the new agent.
func (s *Server) handleAgentUpdate(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readUploadedELF(w, r, s.logger, "agent-update")
	if !ok {
		return
	}
	const dst = agentBinNANDPath
	err := writeBinaryAtomic(dst, body)
	// Tier 2 (#270): a NAND that UBIFS parked read-only fails the write (or,
	// sneakier, fails every reclaim delete so the write path reports "no
	// space"). Probe, remount rw, retry once; if the volume stays protected,
	// the truthful error beats another opaque 507.
	if err != nil && (isReadOnlyFSErr(err) || !nandWritable()) {
		if remountNANDRW(s.logger) {
			err = writeBinaryAtomic(dst, body)
		} else {
			http.Error(w, errNANDReadOnly.Error(), http.StatusInsufficientStorage)
			return
		}
	}
	// Tier 3 (#270): the volume writes fine but genuinely cannot hold OLD + NEW
	// agent side by side even after the reclaim (small ST20 volumes). Stage the
	// new binary in RAM and let a detached helper swap it in after this process
	// exits, then reboot — peak NAND need drops to a single copy. Since the
	// optimistic-write change errInsufficientNAND means a REAL failed write
	// attempt (ENOSPC / short write), never a pessimistic statfs prediction, so
	// this tier only engages when the filesystem itself said no.
	if errors.Is(err, errInsufficientNAND) {
		if serr := s.stageAndSwapViaRAM(dst, body); serr == nil {
			writeJSON(w, http.StatusOK, map[string]string{
				"status": "ok",
				"action": "reboot",
				"mode":   "ram-staged",
			})
			go func() {
				// Give the 200 OK time to flush, then exit: the helper waits
				// for this PID before copying (the running binary is ETXTBSY
				// and its blocks are pinned until we are gone).
				time.Sleep(1500 * time.Millisecond)
				// Refresh a still-inserted stick BEFORE exiting so the boot
				// sync cannot revert this update (#381). Delaying our exit is
				// safe: the swap helper waits for this PID.
				refreshStickAgentBinary(body, s.logger)
				s.logger.Info("exiting for the RAM-staged binary swap; the helper reboots the box")
				os.Exit(0)
			}()
			return
		} else {
			s.logger.Warn("RAM-staged swap unavailable, reporting the space failure", "err", serr)
		}
	}
	if err != nil {
		http.Error(w, err.Error(), nandWriteHTTPStatus(err))
		return
	}

	// Safe-over-fast (#381): prove the binary is ON FLASH before telling the
	// app anything and before any reboot. The fsync inside writeBinaryAtomic
	// should make this a formality, but UBIFS + these boxes have burned us:
	// verify by re-reading past the page cache, retry the write once on a
	// mismatch, and refuse with a truthful error rather than reboot into the
	// old binary and let the app loop-push forever.
	if verr := verifyBinaryOnFlash(dst, body); verr != nil {
		s.logger.Warn("agent update: flash verify failed, rewriting once", "err", verr)
		if err := writeBinaryAtomic(dst, body); err != nil {
			http.Error(w, err.Error(), nandWriteHTTPStatus(err))
			return
		}
		if verr = verifyBinaryOnFlash(dst, body); verr != nil {
			s.logger.Error("agent update: flash verify failed twice, refusing to reboot", "err", verr)
			http.Error(w, "update did not persist to flash: "+verr.Error(), http.StatusInternalServerError)
			return
		}
	}

	// A verified write supersedes any earlier tier-3 swap failure.
	_ = os.Remove(swapFailMarker)
	s.logger.Info("agent update written and flash-verified, rebooting box for a clean post-OTA state", "size", len(body))
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"action": "reboot",
	})

	// Always reboot after an OTA rather than just self-restarting the
	// process. Jens 2026-06-01: a self-restart left the freshly-updated
	// box with no presets visible in the app — the boot-time preset push
	// and the leave-OOB full re-sync (cmd/agent reconcileOnce forceFull)
	// only run on a real boot, not on a live process restart; and OTA
	// replaces only the binary, so the NAND run.sh + rc.local otherwise
	// stay at the pre-OTA vintage (project_ota_only_replaces_binary). A
	// reboot makes the new binary self-deploy its matching run.sh/rc.local
	// AND re-run the preset reconcile from clean. Delay so the 200 OK
	// above flushes to the desktop app before the box drops off the LAN.
	go func() {
		time.Sleep(1500 * time.Millisecond)
		// Refresh a still-inserted stick BEFORE the reboot: run.sh's boot sync
		// copies the stick binary over NAND unconditionally, so a stale stick
		// would silently revert the binary just written to dst (#381). The
		// desktop app's SSH stick refresh covers this only when SSH is open;
		// this on-box write needs nothing. The reboot waits for it.
		refreshStickAgentBinary(body, s.logger)
		// Unconditional flush before the reboot. refreshStickAgentBinary syncs
		// only when a stick is mounted; on a stickless box (the #381 field
		// case) nothing else flushed this path before v0.9.7. Every other
		// reboot in the project already syncs first — this one now does too.
		_ = exec.Command("sync").Run()
		time.Sleep(2 * time.Second)
		s.logger.Info("post-OTA reboot")
		_ = dst // the new binary is in place at dst; the boot path runs it
		if err := exec.Command("reboot").Run(); err != nil {
			// reboot binary missing or refused — fall back to the detached
			// process self-restart so we at least run the new binary. The
			// sleep 70 covers the :8081/:9080 TIME_WAIT window (60 s
			// tcp_fin_timeout on this kernel) so the new binary does not
			// crash-loop on "address already in use" (seen 2026-05-17).
			s.logger.Error("post-OTA reboot failed, falling back to process self-restart", "err", err)
			quoted := make([]string, 0, len(os.Args))
			for _, a := range os.Args {
				quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", "'\\''")+"'")
			}
			shCmd := "sleep 70 && exec " + strings.Join(quoted, " ") + " >> /tmp/streborn-agent.log 2>&1"
			cmd := exec.Command("sh", "-c", shCmd)
			cmd.SysProcAttr = sysProcAttrSetsid()
			if serr := cmd.Start(); serr != nil {
				s.logger.Error("self-restart fallback also failed", "err", serr)
				return
			}
			time.Sleep(100 * time.Millisecond)
			os.Exit(0)
		}
	}()
}

// readUploadedELF reads and validates a raw ARM ELF binary POSTed to an OTA
// endpoint: LAN-only, size-bounded, ELF-magic checked. On any problem it writes
// the HTTP error response and returns ok=false. Shared by handleAgentUpdate and
// handleAgentSidecar so the two upload endpoints cannot drift on their guards.
// uploadMemKB reports MemAvailable and MemTotal in KB, or -1 each when
// /proc/meminfo cannot be read. Logged around every upload so a bundle from a
// speaker whose push died mid-stream says whether it ran out of memory,
// instead of leaving that to be inferred from the app side.
func uploadMemKB() (avail, total int64) {
	avail, total = -1, -1
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "MemAvailable:":
			avail, _ = strconv.ParseInt(f[1], 10, 64)
		case "MemTotal:":
			total, _ = strconv.ParseInt(f[1], 10, 64)
		}
	}
	return
}

func (s *Server) readUploadedELF(w http.ResponseWriter, r *http.Request, logger *slog.Logger, what string) ([]byte, bool) {
	if !requireMethod(w, r, http.MethodPost) {
		return nil, false
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "update only allowed from LAN", http.StatusForbidden)
		return nil, false
	}
	// Quieten the speaker before the transfer starts, not after: the audio and
	// the upload otherwise compete for the same radio and the same CPU, and the
	// audio is what loses. Deliberately after the method and LAN checks, so a
	// stray request cannot silence a speaker without even being a real upload.
	// See hushforupload.go.
	s.hushForUpload(what)
	const maxSize = 30 * 1024 * 1024
	// Log the whole upload lifecycle. The #466 bundles showed the app-side
	// view of a dying post-OTA push (RST after ~107 s) while the box-side log
	// carried no trace of the upload at all, so the box-side failure mode
	// (request never arrived vs stream stalled at byte N vs read completed
	// but reply lost) was undiagnosable. These lines make the next bundle
	// answer that directly.
	memAtStart, memTotal := uploadMemKB()
	logger.Info("upload started", "endpoint", what,
		"contentLength", r.ContentLength, "remote", r.RemoteAddr,
		"memAvailableKB", memAtStart, "memTotalKB", memTotal)
	upr := &uploadProgressReader{
		r: io.LimitReader(r.Body, maxSize+1), start: time.Now(), logger: logger, what: what,
	}
	// Reserve the whole body up front instead of letting io.ReadAll grow by
	// doubling. The doubling briefly holds the old AND the new buffer, so a
	// 16 MB engine peaked near 33 MB on a box with about 120 MB of RAM in
	// total and well under half of it free. Two field reports (2026-07-30)
	// show the ~16 MB sidecar push dying five times in a row with the box
	// closing the connection about 100 s in, which is what an agent killed
	// under memory pressure looks like from the app side. One allocation of
	// the exact size removes that peak.
	var buf bytes.Buffer
	if cl := r.ContentLength; cl > 0 && cl <= maxSize {
		buf.Grow(int(cl))
	}
	_, err := buf.ReadFrom(upr)
	body := buf.Bytes()
	if err != nil {
		memNow, _ := uploadMemKB()
		logger.Warn("upload aborted mid-stream", "endpoint", what,
			"bytesRead", upr.n, "elapsedMs", time.Since(upr.start).Milliseconds(),
			"memAvailableKB", memNow, "memAvailableAtStartKB", memAtStart, "err", err)
		http.Error(w, "read: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	memAfter, _ := uploadMemKB()
	logger.Info("upload body received", "endpoint", what,
		"bytes", len(body), "elapsedMs", time.Since(upr.start).Milliseconds(),
		"memAvailableKB", memAfter, "memAvailableAtStartKB", memAtStart)
	if len(body) > maxSize {
		http.Error(w, "binary too big", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	if len(body) < 1024 {
		http.Error(w, "binary too small", http.StatusBadRequest)
		return nil, false
	}
	// ELF magic check.
	if body[0] != 0x7f || body[1] != 'E' || body[2] != 'L' || body[3] != 'F' {
		http.Error(w, "not an ELF binary", http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

// uploadProgressReader logs a large upload's progress every few MB so the
// box-side agent log shows exactly how far a doomed transfer got before its
// connection died. Wraps the size-limited body reader in readUploadedELF.
type uploadProgressReader struct {
	r       io.Reader
	n       int64
	lastLog int64
	start   time.Time
	logger  *slog.Logger
	what    string
}

func (u *uploadProgressReader) Read(p []byte) (int, error) {
	n, err := u.r.Read(p)
	u.n += int64(n)
	if u.n-u.lastLog >= 4*1024*1024 {
		u.lastLog = u.n
		u.logger.Info("upload progress", "endpoint", u.what,
			"bytes", u.n, "elapsedMs", time.Since(u.start).Milliseconds())
	}
	return n, err
}

// errInsufficientNAND is returned by writeBinaryAtomic when an actually
// attempted .new write (or its rename) failed with a real out-of-space error
// even after reclaiming regenerable junk. The OTA handlers map it to 507
// Insufficient Storage so the desktop app can tell "the box is full" apart
// from a generic write failure and show the inventory instead of a raw 500
// after a full upload (#ST30 Daniel), and handleAgentUpdate's RAM-staged
// tier 3 keys off it.
var errInsufficientNAND = errors.New("insufficient NAND space")

// nandWriteMargin is the slack required above the binary size before an atomic
// write is attempted: filesystem overhead plus a cushion so a borderline write
// does not stall or ENOSPC mid-stream.
//
// It is deliberately generous, because this figure does not refuse anything: it
// only decides whether the engine is reclaimed FIRST. Being too mean here is
// what hurt. At 512 KB, a speaker with 13.65 MB free counted as having room for
// a 12.78 MB write, so it kept its engine and wrote a second full copy of the
// binary into a volume that then had under 1 MB left. Sometimes that works and
// sometimes the write never returns: the agent logs the body as received and
// then goes silent, never answers, and the speaker reboots onto the old binary.
//
// Two of Jens' speakers proved it on 2026-07-30. Both ST10s, 13,680 KB and
// 13,652 KB free, the same 12.78 MB update: one replied in 15 s, the other
// received the whole body and was never heard from again. The same signature is
// in three field reports (#511 three times on one speaker, and two install
// reports the same day), and in every one of them the speaker that was clearly
// TIGHT succeeded, because being told "no room" is what triggers the reclaim
// that then gives the write 15 MB of air.
//
// 3 MB puts a borderline speaker on the reclaim path instead of the cliff. The
// engine it drops is re-delivered right after the reboot, which is the flow
// that already existed and is well tested.
const nandWriteMargin = 3 * 1024 * 1024

// engineStopHook stops the running go-librespot so the space-pressed OTA write can
// truly free the engine's NAND blocks before dropping it (an unlink of a running
// binary frees nothing). Set by WithSpotifyStop; nil in tests and when Spotify is
// not configured, in which case the reclaim falls back to a plain os.Remove.
var engineStopHook func() bool

// writeBinaryAtomic writes body to dst via a .new temp + rename so a partial
// write never becomes the live binary, creating the parent dir on a fresh box
// (first OTA after install has no /mnt/nv/streborn/bin yet). 0755 so the file
// is executable.
//
// The box's writable NAND (/mnt/nv) is tiny (~31 MB, shared with the Bose
// firmware), and the atomic write needs room for a SECOND full copy of the
// ~10 MB binary beside the live one. On a SoundTouch 30 that tipped /mnt/nv
// over and the OTA failed with "no space left on device" (Daniel, 2026-06-24),
// with no way to see what was eating the space because the box was stickless so
// SSH was closed. Two defences: (1) before writing, drop a stale .new from an
// earlier interrupted OTA (a half-written temp from a failed attempt otherwise
// eats the very headroom the retry needs, so every retry keeps failing until
// the next boot's run.sh cleanup_nand runs) and, if statfs predicts a shortage,
// reclaim obvious junk; (2) on failure, embed the NAND inventory (df + biggest
// entries + foreign-firmware dirs) in the error so the desktop app surfaces it
// verbatim and the user's report tells us whether the ST30 is genuinely tighter
// or is carrying leftovers from a previous custom firmware.
//
// The write itself is OPTIMISTIC: the statfs prediction steers the reclaim
// cascade but never refuses the write. UBIFS free space is deliberately
// pessimistic (it assumes incompressible data while the volume compresses
// transparently, ~1.5x on Go binaries; measured 2026-07-10), so its "no" is
// frequently wrong for this write while its "yes" is always safe. Only the
// filesystem's own verdict on the actual write (a real ENOSPC / short write)
// maps to errInsufficientNAND now.
func writeBinaryAtomic(dst string, body []byte) error {
	tmp, dir, st, err := prepareBinaryWrite(dst, int64(len(body)))
	if err != nil {
		return err
	}
	if err := writeFileSynced(tmp, body, 0o755); err != nil {
		// A mid-stream ENOSPC leaves a truncated tmp; remove it so no partial
		// .new survives for the next attempt.
		_ = os.Remove(tmp)
		return classifyNANDWriteErr("write tmp", err, dir, int64(len(body)), st.engineStopped, st.engineReclaim, st.predictedFull)
	}
	return finishBinaryWrite(tmp, dst, dir, int64(len(body)), st)
}

// nandWriteState carries what the reclaim preamble learned into the error
// classification, so a failure can still say whether the engine was dropped
// and whether statfs had predicted the shortage.
type nandWriteState struct {
	engineStopped bool
	engineReclaim string
	predictedFull bool
}

// prepareBinaryWrite is everything writeBinaryAtomic does BEFORE the bytes
// move: the parent directory, the stale temp, and the reclaim cascade that
// makes room. Shared with the streaming variant so the two cannot drift on the
// space handling, which is the part that took the longest to get right.
func prepareBinaryWrite(dst string, need int64) (tmp, dir string, st nandWriteState, err error) {
	dir = filepath.Dir(dst)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", "", st, fmt.Errorf("mkdir parent: %w", err)
	}
	tmp = dst + ".new"
	// Drop this target's stale temp, then proactively reclaim regenerable junk on
	// EVERY OTA write (not only when already tight), so a previous failed
	// attempt's leftovers, a stale OTHER-binary .new, or oversized logs never eat
	// the headroom this write needs (#ST30 Daniel). reclaimNAND never touches Bose
	// files or the live binaries.
	_ = os.Remove(tmp)
	reclaimNAND()
	engineStopped := false
	engineReclaim := ""
	predictedFull := false
	if !nandHasRoom(dir, need) {
		// Second-tier reclaim: the go-librespot Spotify engine (~16 MB) is the one
		// big regenerable block left on a nearly-full NAND (ST30, ~31 MB), and the
		// cheap reclaim above never touches it. Drop it so the agent .new fits
		// rather than failing the whole update (#119); the desktop app re-delivers
		// it after the reboot (EnsureSpotifyEngine, triggered by goLibrespot !=
		// "present"). Only runs when statfs predicts a shortage, so a roomy box
		// keeps its engine.
		//
		// Stop the engine FIRST: it is normally running during an OTA, and a plain
		// os.Remove of a running binary only unlinks the path while the kernel keeps
		// its blocks pinned until the process exits, so dropping it freed nothing and
		// the update still failed with "no space left" (#119). StopEngine kills it and
		// waits for exit so the ~16 MB actually frees; reclaimSpotifyEngine then drops
		// the (now-unused) binary.
		if engineStopHook != nil {
			engineStopped = engineStopHook()
		}
		engineReclaim = reclaimSpotifyEngine()
		// Re-check with patience: UBIFS updates its free-space accounting lazily
		// after a delete, and the engine's blocks release on process reap, so an
		// immediate statfs can still show the old figure. A field report (#270)
		// showed a 507 whose inventory still carried the engine with no way to
		// tell which step failed; the outcome of each step is now logged and
		// embedded in the error.
		fits := false
		for i := 0; i < 10; i++ {
			if nandHasRoom(dir, need) {
				fits = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		predictedFull = !fits
		slog.Info("OTA write: space-pressed reclaim ran",
			"engineStopped", engineStopped, "engineReclaim", engineReclaim, "fits", fits)
		if !fits {
			// Do NOT refuse: agent+engine measurably fit the tightest (ST20,
			// ~26.7 MB) NAND once compression is accounted for, and refusing on
			// the pessimistic figure is what kept those boxes un-updatable.
			_, avail, _ := diskFree(dir)
			slog.Info("OTA write: statfs still predicts no room after reclaim; attempting the write anyway (UBIFS under-reports free space for compressible data)",
				"needKB", need/1024, "availKB", avail/1024)
		}
	}
	return tmp, dir, nandWriteState{engineStopped, engineReclaim, predictedFull}, nil
}

// finishBinaryWrite renames the completed temp into place and journals it.
func finishBinaryWrite(tmp, dst, dir string, need int64, st nandWriteState) error {
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return classifyNANDWriteErr("rename", err, dir, need, st.engineStopped, st.engineReclaim, st.predictedFull)
	}
	// fsync the parent directory so the rename itself reaches the UBIFS
	// journal. Without this the whole write + rename can sit in the page
	// cache, and the post-OTA reboot 1.5 s later rolls the file back to the
	// pre-OTA binary — the box then boots the OLD version byte-perfect while
	// the app keeps re-pushing forever (#381 meierchen006, cgb280).
	syncDir(dir)
	return nil
}

// writeFileSynced is os.WriteFile plus an fsync before close, so the data is
// on flash (not just in the page cache) when it returns. Every binary the OTA
// path writes is followed by a reboot soon after; an unsynced write on UBIFS
// simply does not survive that (#381).
func writeFileSynced(path string, body []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// syncDir fsyncs a directory so a just-completed rename is journaled.
// Best-effort: some filesystems/platforms refuse directory handles.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// verifyBinaryOnFlash forces the just-written binary out to flash and proves
// it survived: global sync, drop the page cache, re-read dst and compare its
// hash against what was uploaded. This is the same lesson run.sh's #302
// deploy learned — "the md5 reads back the bytes we just wrote from RAM
// cache … even when the flash writeback silently failed" — applied to the
// OTA path. Only called right before a reboot, so dropping the page cache is
// free. Returns nil when the on-flash bytes match.
func verifyBinaryOnFlash(dst string, body []byte) error {
	_ = exec.Command("sync").Run()
	// Best-effort: /proc/sys/vm/drop_caches needs root (the agent is root on
	// the box) but does not exist in tests.
	_ = os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0o200)
	got, err := os.ReadFile(dst)
	if err != nil {
		return fmt.Errorf("flash verify re-read: %w", err)
	}
	want := sha256.Sum256(body)
	have := sha256.Sum256(got)
	if want != have {
		return fmt.Errorf("flash verify: on-flash binary differs from the upload (%d vs %d bytes) — the NAND write did not persist", len(got), len(body))
	}
	return nil
}

// isNoSpaceErr reports whether err is a REAL filesystem out-of-space failure:
// ENOSPC (usually wrapped in fs.PathError -> fmt.Errorf chains) or a short
// write. This filesystem verdict is what the optimistic OTA write trusts, as
// opposed to the pessimistic statfs prediction that used to refuse up front.
func isNoSpaceErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.ENOSPC) ||
		errors.Is(err, io.ErrShortWrite) ||
		strings.Contains(err.Error(), "no space left")
}

// classifyNANDWriteErr turns a failed step of the atomic OTA write into the
// error the handlers act on: a real out-of-space failure maps to
// errInsufficientNAND (507 + inventory; handleAgentUpdate's RAM-staged tier 3
// keys off it), anything else stays a plain write error (500, or the EROFS
// tier 2 when it smells read-only). The message records that the write WAS
// actually attempted, so a field report can tell a genuine ENOSPC apart from
// the pre-flight refusals older agents issued.
func classifyNANDWriteErr(step string, err error, dir string, need int64, engineStopped bool, engineReclaim string, predictedFull bool) error {
	if !isNoSpaceErr(err) {
		return fmt.Errorf("%s: %w [NAND %s]", step, err, nandReportLine())
	}
	if engineReclaim == "" {
		engineReclaim = "reclaim not needed, statfs predicted room"
	}
	_, avail, _ := diskFree(dir)
	return fmt.Errorf("%w: the write was actually attempted and the filesystem refused it (%s: %v): need %dKB, statfs free %dKB after reclaim (%s; engine stop=%v; statfs predicted full=%v) [NAND %s]",
		errInsufficientNAND, step, err, need/1024, avail/1024, engineReclaim, engineStopped, predictedFull, nandReportLine())
}

// nandWriteHTTPStatus maps a writeBinaryAtomic error to an HTTP status: 507
// Insufficient Storage when the box is out of NAND (so the desktop app can tell a
// full box apart from a generic failure and surface the inventory), else 500.
func nandWriteHTTPStatus(err error) int {
	if errors.Is(err, errInsufficientNAND) {
		return http.StatusInsufficientStorage
	}
	return http.StatusInternalServerError
}

// --- NAND disk diagnostics -------------------------------------------------
//
// These mirror run.sh's nand_inventory + cleanup_nand on the agent so the same
// picture is available over plain HTTP, not just in the SSH-gated diagnostic
// bundle: in /api/agent/version (free/total, every poll), in /api/debug/state
// (full inventory), and embedded into the OTA failure error. A stickless box
// (SSH closed since v0.8.1) can then still reveal its disk state to the app.

const nandRoot = "/mnt/nv"
const strNANDDir = "/mnt/nv/streborn"

// diskFree lives in statfs_linux.go / statfs_other.go: the Linux build does a
// real statfs, other hosts report "unknown" so the package stays testable on
// dev machines.

// selfProxyPathRe matches the agent's own per-slot proxy path (/stream/1..6),
// which is the box-visible preset location and never a valid station origin.
var selfProxyPathRe = regexp.MustCompile(`^/stream/([1-6])$`)

// selfProxySlot reports whether raw points at this agent's own /stream/<slot>
// proxy and which slot it references. Heuristic: the per-slot proxy path plus
// either a loopback host or one of STR's own ports - a real station origin
// never matches all of that (#252).
func selfProxySlot(raw string) (int, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, false
	}
	m := selfProxyPathRe.FindStringSubmatch(u.Path)
	if m == nil {
		return 0, false
	}
	host, port := u.Hostname(), u.Port()
	if port != "8888" && port != "17008" && host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

// dirBytes is a du -s in bytes for path, best-effort: unreadable entries are
// skipped, never fatal. Cheap on the tiny NAND.
func dirBytes(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, fi os.FileInfo, err error) error {
		if err != nil || fi == nil {
			return nil
		}
		if !fi.IsDir() {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// isForeignNANDDir reports whether a top-level /mnt/nv dir is neither STR's nor
// one of Bose's, i.e. a candidate leftover from a third-party post-cloud tool
// or another custom firmware the owner ran before STR. Mirrors run.sh's
// nand_inventory freshness check.
// foreignNANDDirNames lists the top-level /mnt/nv directories that are neither
// STR's nor Bose's own persistence (isForeignNANDDir), by name. Cheap: a single
// readdir with no recursive sizing, so it is safe on every version poll. Wired
// into /api/agent/version as foreignDirs so the desktop app can name the other
// SoundTouch mods a user must remove to free space for the Spotify engine.
func foreignNANDDirNames() []string {
	entries, err := os.ReadDir(nandRoot)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && isForeignNANDDir(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

func isForeignNANDDir(name string) bool {
	switch name {
	case "streborn", "nv", "lost+found":
		return false
	// Bose-native persistence STR must NOT flag as foreign: AWS IoT certs, the
	// Bluetooth Profile Manager store, the Wi-Fi supplicant state, Avahi/mDNS,
	// and the box's own logs. Before this a stock box wrongly listed these as
	// "foreign", which buried the real signal (a rival mod's marker files, see
	// detectConflictingMod) in noise.
	case "IoTCerts", "btpm", "wpa_supplicant", "avahi", "BoseLog":
		return false
	}
	l := strings.ToLower(name)
	if strings.Contains(l, "bose") || strings.Contains(l, "persistence") {
		return false
	}
	return true
}

// hasSavedWLANCreds reports whether STR persisted the box's Wi-Fi SSID+password
// to NAND (run.sh writes strNANDDir/wlan-creds so the box can rejoin Wi-Fi on a
// stick-free cold boot). False means STR has no saved Wi-Fi: the box only stays
// online while the stick or an ethernet cable is inserted, so it strands the user
// on the next cold boot. A user who set Wi-Fi up via the Bose app instead of
// STR's own Wi-Fi setup hits exactly this (#270). Best-effort.
func hasSavedWLANCreds() bool {
	fi, err := os.Stat(filepath.Join(strNANDDir, "wlan-creds"))
	return err == nil && fi.Size() > 0
}

// detectConflictingMod returns the rival cloud-free SoundTouch tool (chiefly
// AfterTouch, github.com/gesellix/Bose-SoundTouch) whose real artifacts sit on
// the box, or "" for an STR-only box. Two tools both redirecting the Bose
// cloud and both driving the OLED / Wi-Fi / presets fight each other and
// strand the box (flashing display, orange Wi-Fi, no playback, #270).
//
// v0.9.7: v0.9.6 keyed this on two marker files (test_oled_stop,
// bco_needs_factory_reset) believed to be AfterTouch's. Field diagnostics
// proved them Bose-native: healthy STR-only speakers carry them (and they
// survive every factory reset because /mnt/nv is never wiped, so the warned
// user could never clear the warning), while a box with REAL AfterTouch
// leftovers (an /mnt/nv/aftertouch directory) carried neither. Detection now
// keys on AfterTouch's actual footprint: its NAND directory, its resolv.conf
// override, and an rc.local hook mentioning it. Best-effort.
func detectConflictingMod() string {
	if fi, err := os.Stat(filepath.Join(nandRoot, "aftertouch")); err == nil && fi.IsDir() {
		return "AfterTouch"
	}
	if _, err := os.Stat(filepath.Join(nandRoot, "aftertouch.resolv.conf")); err == nil {
		return "AfterTouch"
	}
	// STR's own rc.local never references the rival tool, so a hook line in
	// the boot script is an unambiguous fingerprint.
	if b, err := os.ReadFile(filepath.Join(nandRoot, "rc.local")); err == nil &&
		strings.Contains(strings.ToLower(string(b)), "aftertouch") {
		return "AfterTouch"
	}
	return ""
}

// handleRemoveConflictingMod removes the leftovers of a rival cloud-free
// SoundTouch tool (AfterTouch) that clash with STR: its /mnt/nv/aftertouch
// directory, its resolv.conf override, and any aftertouch hook line in rc.local.
// It touches ONLY those artifacts, never the rest of /mnt/nv (which holds the
// box's own Wi-Fi/AirPlay/account persistence and STR's own streborn/ dir). The
// desktop app surfaces this as a one-click button so users never need SSH; a
// reboot afterwards fully clears the rival tool's already-running processes.
func (s *Server) handleRemoveConflictingMod(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	mod := detectConflictingMod()
	if mod == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "mod": "", "removed": []string{}})
		return
	}
	removed := []string{}
	// 1. The rival tool's NAND directory.
	dir := filepath.Join(nandRoot, "aftertouch")
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		if err := os.RemoveAll(dir); err != nil {
			http.Error(w, "could not remove aftertouch/: "+err.Error(), http.StatusInternalServerError)
			return
		}
		removed = append(removed, "aftertouch/")
	}
	// 2. Its resolv.conf override.
	resolv := filepath.Join(nandRoot, "aftertouch.resolv.conf")
	if _, err := os.Stat(resolv); err == nil {
		if err := os.Remove(resolv); err != nil {
			http.Error(w, "could not remove aftertouch.resolv.conf: "+err.Error(), http.StatusInternalServerError)
			return
		}
		removed = append(removed, "aftertouch.resolv.conf")
	}
	// 3. Any aftertouch hook line in rc.local. Rewrite it without those lines,
	// keeping every other line (STR's own boot hooks and Bose defaults) intact.
	rcl := filepath.Join(nandRoot, "rc.local")
	if b, err := os.ReadFile(rcl); err == nil && strings.Contains(strings.ToLower(string(b)), "aftertouch") {
		lines := strings.Split(string(b), "\n")
		kept := lines[:0]
		dropped := 0
		for _, ln := range lines {
			if strings.Contains(strings.ToLower(ln), "aftertouch") {
				dropped++
				continue
			}
			kept = append(kept, ln)
		}
		if dropped > 0 {
			if err := os.WriteFile(rcl, []byte(strings.Join(kept, "\n")), 0o755); err != nil {
				http.Error(w, "could not clean rc.local: "+err.Error(), http.StatusInternalServerError)
				return
			}
			removed = append(removed, fmt.Sprintf("rc.local (%d line)", dropped))
		}
	}
	_ = exec.Command("sync").Run()
	s.logger.Info("removed conflicting-mod leftovers", "mod", mod, "removed", removed)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"mod":           mod,
		"removed":       removed,
		"stillDetected": detectConflictingMod() != "",
	})
}

// wlanCredsWarningWarranted reports whether the "no Wi-Fi saved in STR"
// warning should fire: only when the creds file is missing on a chassis whose
// Wi-Fi lives in the volatile coprocessor store (wlan-mode "bco" /
// "taigan-bco") — there STR's boot-time replay really is what keeps the box
// on Wi-Fi, and a missing file does strand the user on the next cold boot.
//
// v0.9.7: v0.9.6 warned on ANY missing wlan-creds and hit healthy boxes whose
// Wi-Fi persists in the Bose firmware's own store (sm2 wpa_supplicant
// profiles) or that run on ethernet — boxes with months of clean cold boots
// suddenly told the user they would strand (#381 cgb280, #119). An unknown or
// absent wlan-mode stays quiet for the same reason.
func wlanCredsWarningWarranted() bool {
	if hasSavedWLANCreds() {
		return false
	}
	mode, err := os.ReadFile(filepath.Join(strNANDDir, "wlan-mode"))
	if err != nil {
		return false
	}
	switch strings.TrimSpace(string(mode)) {
	case "bco", "taigan-bco":
		return true
	}
	return false
}

// nandEntry is one top-level /mnt/nv entry with its recursive size.
type nandEntry struct {
	Name    string `json:"name"`
	Bytes   int64  `json:"bytes"`
	IsDir   bool   `json:"isDir"`
	Foreign bool   `json:"foreign"`
}

// nandInventory is the structured disk report used by /api/debug/state: df for
// the writable filesystems plus per-entry sizes under /mnt/nv (sorted biggest
// first) with foreign dirs flagged.
func nandInventory() map[string]any {
	rep := map[string]any{}
	if total, avail, ok := diskFree(nandRoot); ok {
		rep["nvTotalBytes"] = total
		rep["nvFreeBytes"] = avail
		rep["nvUsedBytes"] = total - avail
	}
	if total, avail, ok := diskFree("/"); ok {
		rep["rootTotalBytes"] = total
		rep["rootFreeBytes"] = avail
	}
	entries, err := os.ReadDir(nandRoot)
	if err != nil {
		rep["nvEntriesErr"] = err.Error()
		return rep
	}
	list := make([]nandEntry, 0, len(entries))
	foreign := []string{}
	for _, e := range entries {
		ne := nandEntry{
			Name:  e.Name(),
			Bytes: dirBytes(filepath.Join(nandRoot, e.Name())),
			IsDir: e.IsDir(),
		}
		if e.IsDir() && isForeignNANDDir(e.Name()) {
			ne.Foreign = true
			foreign = append(foreign, e.Name())
		}
		list = append(list, ne)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Bytes > list[j].Bytes })
	rep["nvEntries"] = list
	// STR's own dir broken down (bin/ files included), so a bundle shows
	// whether the agent binary, the Spotify engine, or logs eat the space.
	rep["strEntries"] = strDirEntries()
	rep["foreignDirs"] = foreign // empty => STR/Bose-only ("fresh")
	// Two "the box will misbehave / strand the user" signals (#270): a rival
	// SoundTouch tool's marker files, and no STR-saved Wi-Fi (stick-only online).
	rep["conflictingMod"] = detectConflictingMod()
	rep["hasWLANCreds"] = hasSavedWLANCreds()
	return rep
}

// nandReportLine is a compact one-line inventory for embedding in an OTA error
// (which the desktop app surfaces verbatim and the user pastes into a report):
// free/total, the biggest few /mnt/nv entries, and any foreign dirs.
func nandReportLine() string {
	var b strings.Builder
	if total, avail, ok := diskFree(nandRoot); ok {
		fmt.Fprintf(&b, "/mnt/nv free=%dKB total=%dKB", avail/1024, total/1024)
	} else {
		b.WriteString("/mnt/nv df unavailable")
	}
	entries, err := os.ReadDir(nandRoot)
	if err != nil {
		return b.String()
	}
	type es struct {
		name    string
		bytes   int64
		foreign bool
	}
	list := make([]es, 0, len(entries))
	foreign := []string{}
	for _, e := range entries {
		f := e.IsDir() && isForeignNANDDir(e.Name())
		list = append(list, es{e.Name(), dirBytes(filepath.Join(nandRoot, e.Name())), f})
		if f {
			foreign = append(foreign, e.Name())
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].bytes > list[j].bytes })
	b.WriteString("; top:")
	for i, e := range list {
		if i >= 6 {
			break
		}
		fmt.Fprintf(&b, " %s=%dKB", e.name, e.bytes/1024)
	}
	// Break STR's own dir down too: a bare "streborn=30071KB" cannot show
	// whether the space-pressed engine drop actually removed go-librespot
	// (#270: the engine survived a reclaim and the report could not say so).
	strList := strDirEntries()
	if len(strList) > 0 {
		b.WriteString("; str:")
		for i, e := range strList {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&b, " %s=%dKB", e.Name, e.Bytes/1024)
		}
	}
	if len(foreign) > 0 {
		b.WriteString("; foreign(non-STR/Bose): " + strings.Join(foreign, ","))
	} else {
		b.WriteString("; foreign: none")
	}
	return b.String()
}

// strDirEntries lists the entries under /mnt/nv/streborn (with bin/ broken out
// into its files, since the two big binaries live there), biggest first.
func strDirEntries() []nandEntry {
	var list []nandEntry
	// lib/ is broken out for the same reason bin/ is: it holds files of about a
	// megabyte each on a volume where a megabyte decides whether an update fits,
	// and rolled up as one directory total nobody can see WHAT is in there. A
	// field bundle (#534, two ST10s, 664 KB apart) came down to lib being 818 KB
	// on one box and 1.8 MB on the other, and the bundle could not say why.
	for _, sub := range []string{strNANDDir, filepath.Join(strNANDDir, "bin"), filepath.Join(strNANDDir, "lib")} {
		ents, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if sub == strNANDDir && (e.Name() == "bin" || e.Name() == "lib") {
				continue // broken out via the later passes
			}
			p := filepath.Join(sub, e.Name())
			rel, rerr := filepath.Rel(nandRoot, p)
			if rerr != nil {
				rel = p
			}
			list = append(list, nandEntry{Name: rel, Bytes: dirBytes(p), IsDir: e.IsDir()})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Bytes > list[j].Bytes })
	return list
}

// softwareUpdateBinPath is the Bose binary the LD_PRELOAD shim replaces by
// bind-mounting a wrapper over it (see usb-stick/run.sh, shim_stage_wrapper).
const softwareUpdateBinPath = "/opt/Bose/SoftwareUpdate"

// reclaimUnusedShimStage drops the shim's staged pair when the shim is not in
// use: lib/SoftwareUpdate-real, a full copy of the Bose binary, and the
// lib/SU-wrapper.sh that execs it.
//
// The stage is only live while a bind mount sits on /opt/Bose/SoftwareUpdate.
// Without that mount the copy is a spare nobody reads, and it is close to a
// megabyte on the one volume where a megabyte decides whether an agent update
// fits beside its own temp copy. Current run.sh skips the whole shim on every
// chassis STR supports (sm2 opens :8888 with iptables, BCO uses the :17008
// redirect), so on a box installed back when it did stage, the pair has been
// dead weight ever since. Measured on two ST10s in one household, #534: 818 KB
// of lib on one, 1.8 MB on the other, and the second was the box that kept
// warning about storage during updates.
//
// Guarded, not unconditional: with the mount active, /opt/Bose/SoftwareUpdate
// IS the wrapper and this copy is the only remaining original, so removing it
// would break what the wrapper execs. It also stays a reclaim rather than a
// startup cleanup, so it only ever runs when a write actually needs the room,
// and a stick boot re-stages it if a future box needs the shim after all.
func reclaimUnusedShimStage() {
	if shimBindMountActive() {
		return
	}
	libDir := filepath.Join(strNANDDir, "lib")
	for _, name := range []string{"SoftwareUpdate-real", "SU-wrapper.sh"} {
		p := filepath.Join(libDir, name)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if err := os.Remove(p); err == nil {
			slog.Info("nand reclaim: dropped an unused shim stage file",
				"path", p, "bytes", st.Size())
		}
	}
}

// shimBindMountActive reports whether something is mounted over the Bose
// SoftwareUpdate binary, i.e. whether the shim wrapper is live right now.
// Unreadable /proc/mounts counts as active: the safe answer is to keep the file.
func shimBindMountActive() bool {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return true
	}
	return mountActiveIn(string(b))
}

// mountActiveIn is the /proc/mounts parse, split out so the guard is testable
// off-box: the mount point is the SECOND field and must match exactly, since
// "<path>-real" is a different file that happens to share the prefix.
func mountActiveIn(procMounts string) bool {
	for _, line := range strings.Split(procMounts, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == softwareUpdateBinPath {
			return true
		}
	}
	return false
}

// reclaimNAND frees obvious, regenerable junk so a tight OTA write has room:
// stale .new temps from an interrupted OTA and oversized/rotated logs. The
// agent-side mirror of run.sh's cleanup_nand for the OTA path, which (unlike a
// stick boot) does not pass through a reboot. Best-effort throughout; never
// touches Bose files or the live binaries.
func reclaimNAND() {
	binDir := filepath.Join(strNANDDir, "bin")
	for _, pat := range []string{
		filepath.Join(binDir, "*.new"),
		// lib/*.new: interrupted shim atomic-replace temps (str-shim.so.new etc.);
		// cleaned here too so the bin/ and lib/ atomic writes are symmetric.
		filepath.Join(strNANDDir, "lib", "*.new"),
		filepath.Join(strNANDDir, "cap*.ogg"),
	} {
		if matches, _ := filepath.Glob(pat); matches != nil {
			for _, m := range matches {
				_ = os.Remove(m)
			}
		}
	}
	_ = os.Remove(filepath.Join(strNANDDir, "agent.log.1"))
	_ = os.Remove(filepath.Join(nandRoot, "sp-oauth.out"))
	reclaimUnusedShimStage()
	// Stranded SSH-repair staging dir: the desktop app stages the ~28 MB install
	// file set into <base>/streborn-install and the install copies it into
	// /mnt/nv/streborn, but an older app left the staging copy behind, filling the
	// NAND so the next OTA's .new could not fit (#ST30 Daniel). The running agent
	// never uses it (it runs from streborn/bin), so it is always safe to drop here.
	_ = os.RemoveAll(filepath.Join(nandRoot, "streborn-install"))
	_ = os.RemoveAll(filepath.Join(strNANDDir, "streborn-install"))
	// run.out (whole-session run-override.sh stdout) is bounded per boot but
	// uncapped within one long uptime, and run.sh's cleanup_nand omits it; cap it
	// here on every OTA write alongside the rotated logs.
	for _, name := range []string{"setup.log", "setup.log.prev", "agent.log", "previous.log", "boot.log", "run.out"} {
		p := filepath.Join(strNANDDir, name)
		if fi, err := os.Stat(p); err == nil && fi.Size() > 131072 {
			if b, rerr := os.ReadFile(p); rerr == nil && int64(len(b)) > 65536 {
				_ = os.WriteFile(p, b[int64(len(b))-65536:], 0o644)
			}
		}
	}
}

// ReclaimNAND frees regenerable NAND junk (stale OTA temps, the ~28 MB
// SSH-repair staging dir, oversized logs) from outside the package. The agent
// calls it once at startup so a box left tight by an interrupted OTA or an older
// app's staging leftover self-heals on the next agent (re)start, without waiting
// for a full run.sh reboot or the next OTA write. Best-effort; never touches Bose
// files or the live binaries.
func ReclaimNAND() { reclaimNAND() }

// reclaimSpotifyEngine removes the go-librespot Spotify sidecar binary (and its
// sha marker) to free the single biggest regenerable block on a tight NAND. The
// running agent does not need it to apply an update, and the desktop app
// re-delivers it after the reboot (EnsureSpotifyEngine, triggered by goLibrespot
// != "present"), so dropping it is always recoverable. Called only by the
// space-pressed OTA write, never on a roomy box (#119). The returned outcome is
// logged and embedded into a 507 error, because a silent failure here left a
// field report (#270) with a full NAND and no clue whether the drop ever
// happened.
func reclaimSpotifyEngine() string {
	_ = os.Remove(goLibrespotBinPath + ".sha256")
	err := os.Remove(goLibrespotBinPath)
	switch {
	case err == nil:
		noteEngineDroppedForUpdate()
		return "engine dropped"
	case os.IsNotExist(err):
		return "engine absent"
	default:
		return "engine drop failed: " + err.Error()
	}
}

// nandHasRoom reports whether the filesystem backing dir can hold a need-byte
// file plus the atomic-write margin. Returns true when df is unavailable, so an
// unknown free figure never blocks a write (matching the original gate). Since
// the optimistic-write change it only steers the reclaim cascade and logging;
// it no longer refuses the write. Var so the test for that optimistic attempt
// can force the pessimistic "no room" prediction on a roomy temp dir.
var nandHasRoom = func(dir string, need int64) bool {
	_, avail, ok := diskFree(dir)
	if !ok {
		return true
	}
	return avail >= need+nandWriteMargin
}

// handleAgentSidecar receives the go-librespot Spotify sidecar binary and
// writes it atomically to /mnt/nv/streborn/bin/go-librespot. It does NOT reboot
// the box, but it DOES hot-swap the engine in place: after the write it restarts
// the supervised go-librespot (spotifyReload) so the freshly delivered binary is
// live with no reboot (#240). A first-time delivery to a box that had no engine
// is picked up by the manager's waitForBinary instead; either way no reboot is
// needed. This closes the gap where the sidecar shipped only via the stick->NAND
// boot sync, leaving an OTA-only box (e.g. a SoundTouch 30 whose USB stick never
// copied it) silently unable to play Spotify despite a synced login (#45/#105).
//
// Hot-swapping also frees NAND sooner: the old engine inode is held by the
// running process until it exits, so killing+relaunching it on the write releases
// that ~10 MB immediately instead of holding it until the next reboot.
func (s *Server) handleAgentSidecar(w http.ResponseWriter, r *http.Request) {
	// Streamed, not buffered. This is the ~16 MB engine and the box has ~120 MB
	// of RAM: holding it in memory first was enough to reboot a speaker that
	// happened to be busy (see streamBinaryAtomic). The agent-update endpoint
	// deliberately still buffers, because its fallback tiers need the bytes
	// again after the write.
	sum, size, ok := s.streamUploadedELF(w, r, s.logger, "sidecar", goLibrespotBinPath)
	if !ok {
		return
	}
	// Stamp the content hash next to the binary so the next /api/agent/version
	// reports it and the desktop app skips re-pushing this ~10 MB binary when the
	// box already has the embedded build.
	if err := os.WriteFile(goLibrespotBinPath+".sha256", []byte(sum), 0o644); err != nil {
		s.logger.Warn("go-librespot sidecar: hash marker write failed (non-fatal)", "err", err)
	}
	// Flush now: an agent OTA often reboots the box right after this delivery,
	// and an unsynced 16 MB engine simply vanished across that reboot (deqw,
	// 2026-07-12: "delivered" 09:18:51, gone after the 09:19 reboot, re-pushed
	// 09:22:50 — which then wedged the box).
	_ = exec.Command("sync").Run()
	// The engine is back, so the "it was taken away for an update" marker has
	// served its purpose. Clearing it stops the app from re-delivering forever.
	clearEngineDroppedMarker()
	s.logger.Info("go-librespot sidecar written via OTA", "size", size)
	// Activate the freshly delivered engine live: restart the supervised
	// go-librespot so it re-execs the new binary, with no box reboot. A first-time
	// delivery to a box that had no engine is already picked up by the manager's
	// waitForBinary; this hot-swaps the already-running case, which previously
	// needed a manual restart after the update (#240 Pierre, #ST30 Daniel).
	// Best-effort and reported back so the desktop app's diagnostics can see it;
	// the engineHotSwap capability in /api/agent/version is what gates the app's
	// decision to skip its activation reboot.
	reloaded := false
	if s.spotifyReload != nil {
		reloaded = s.spotifyReload()
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"reloaded": strconv.FormatBool(reloaded),
	})
}

// isLocalLAN true if the request comes from a private LAN IP
// (RFC1918) or localhost. Updates from the internet are blocked.
func isLocalLAN(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return true
	}
	// Not a private address, which is NOT the same as not local.
	//
	// A field report (2026-08-11, Wave SoundTouch) came from a home network
	// numbered 192.210.1.0/24, public address space used as a LAN. The app sat
	// on that same wire, three metres from the speaker, and every update was
	// refused with "update only allowed from LAN" because the check only knew
	// the RFC1918 ranges. The same hole hits carrier-grade NAT (100.64.0.0/10),
	// which is not private either, and any network an admin numbered by hand.
	// For those owners STR simply could not be updated at all.
	//
	// So ask the real question instead of the proxy one: is the caller on a
	// network this speaker itself has an address in. That is the trust boundary
	// the message always claimed. It widens the gate to a speaker's own subnet
	// even when that subnet is public, which is a deliberate trade: a box with a
	// public address is directly exposed with or without this, and refusing its
	// owner an update does not change that.
	return sameSubnetAsAnInterface(ip)
}

// sameSubnetAsAnInterface reports whether ip falls inside one of the networks
// this machine (the speaker) has an address in. Interfaces are read per call:
// this runs on admin endpoints only, a handful of times per update, and a
// cached answer would go stale exactly when the network changed.
func sameSubnetAsAnInterface(ip net.IP) bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.IsLoopback() {
				continue
			}
			// A /32 (or /128) carries no subnet, so Contains would only ever
			// match the box itself and tells us nothing about the caller.
			if ones, bits := n.Mask.Size(); ones == bits {
				continue
			}
			if n.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// streamBinaryAtomic writes dst from a reader instead of from a buffer, and
// returns the SHA256 of what it wrote.
//
// It exists because the sidecar engine is ~16 MB and the box has ~120 MB of
// RAM in total. Holding the whole upload in memory before writing it was
// enough to kill the agent on a speaker that happened to be busy: a 2026-08-08
// bundle from a Lifestyle 535 adapter caught it exactly, with the box's own log
// showing MemAvailable falling from 27 MB to 11 MB as the body arrived, and the
// speaker rebooting about eighty seconds later. Four pushes in a row died that
// way and the owner saw his speaker restart each time. An earlier fix
// (2026-07-30) removed io.ReadAll's doubling peak, which halved the cost but
// left the 16 MB itself in memory.
//
// The bytes now go straight to the temp file the atomic write was going to use
// anyway, so peak memory is a 32 KB copy buffer. The reclaim cascade in front
// of it is shared verbatim with writeBinaryAtomic, because the space handling
// is the part of this path that was hardest to get right and must not fork.
//
// need is the expected size (Content-Length) and is used only to make room; the
// actual size written is returned.
func streamBinaryAtomic(dst string, src io.Reader, need int64) (sum string, n int64, err error) {
	tmp, dir, st, err := prepareBinaryWrite(dst, need)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return "", 0, classifyNANDWriteErr("open tmp", err, dir, need, st.engineStopped, st.engineReclaim, st.predictedFull)
	}
	n, err = io.Copy(f, io.TeeReader(src, h))
	if err == nil {
		// fsync before close, for the same reason writeFileSynced does it: an
		// unsynced write does not survive the reboot that follows an OTA.
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// A mid-stream ENOSPC leaves a truncated tmp; remove it so no partial
		// .new survives for the next attempt.
		_ = os.Remove(tmp)
		return "", n, classifyNANDWriteErr("write tmp", err, dir, need, st.engineStopped, st.engineReclaim, st.predictedFull)
	}
	if err := finishBinaryWrite(tmp, dst, dir, need, st); err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// streamUploadedELF is readUploadedELF for a caller that does not need the
// bytes afterwards: same guards, same logging, but the body goes to dst as it
// arrives rather than into memory first.
//
// The ELF check still happens before anything is written, by peeking the first
// four bytes; a body that is not a binary never reaches the flash.
func (s *Server) streamUploadedELF(w http.ResponseWriter, r *http.Request, logger *slog.Logger, what, dst string) (sum string, size int64, ok bool) {
	if !requireMethod(w, r, http.MethodPost) {
		return "", 0, false
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "update only allowed from LAN", http.StatusForbidden)
		return "", 0, false
	}
	s.hushForUpload(what)
	const maxSize = 30 * 1024 * 1024
	memAtStart, memTotal := uploadMemKB()
	logger.Info("upload started", "endpoint", what,
		"contentLength", r.ContentLength, "remote", r.RemoteAddr,
		"memAvailableKB", memAtStart, "memTotalKB", memTotal, "streamed", true)
	upr := &uploadProgressReader{
		r: io.LimitReader(r.Body, maxSize+1), start: time.Now(), logger: logger, what: what,
	}
	br := bufio.NewReaderSize(upr, 4096)
	magic, err := br.Peek(4)
	if err != nil || magic[0] != 0x7f || magic[1] != 'E' || magic[2] != 'L' || magic[3] != 'F' {
		http.Error(w, "not an ELF binary", http.StatusBadRequest)
		return "", 0, false
	}
	sum, size, werr := streamBinaryAtomic(dst, br, r.ContentLength)
	memAfter, _ := uploadMemKB()
	if werr != nil {
		logger.Warn("upload write failed", "endpoint", what, "bytes", size,
			"elapsedMs", time.Since(upr.start).Milliseconds(),
			"memAvailableKB", memAfter, "memAvailableAtStartKB", memAtStart, "err", werr)
		http.Error(w, werr.Error(), nandWriteHTTPStatus(werr))
		return "", size, false
	}
	logger.Info("upload body received", "endpoint", what,
		"bytes", size, "elapsedMs", time.Since(upr.start).Milliseconds(),
		"memAvailableKB", memAfter, "memAvailableAtStartKB", memAtStart)
	if size > maxSize {
		http.Error(w, "binary too big", http.StatusRequestEntityTooLarge)
		return "", size, false
	}
	if size < 1024 {
		http.Error(w, "binary too small", http.StatusBadRequest)
		return "", size, false
	}
	return sum, size, true
}
