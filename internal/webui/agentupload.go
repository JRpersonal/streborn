package webui

// The two binary uploads, the agent itself on POST /api/agent/update and the
// go-librespot engine on POST /api/agent/sidecar, as services. The handlers
// (agent_admin.go) check the method and the LAN gate and render the answer;
// the self-update (selfupdate.go) hands the same services a verified download
// instead of building a loopback request and reading the status back out of
// a fake ResponseWriter.

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/JRpersonal/streborn/internal/oled"
)

// upload is what the receive logic knows about an upload: the bytes, the
// announced length (-1 when the caller did not say, as net/http reports it)
// and the caller's address for the log lines.
type upload struct {
	Body   io.Reader
	Size   int64
	Remote string
}

// uploadRefusal is why an upload was not installed: the status and the exact
// body the handler renders. Error is the plain-text body (http.Error) unless
// JSON is set, in which case it is the body's "error" field.
type uploadRefusal struct {
	Status int
	Error  string
	JSON   any
}

func textUploadRefusal(status int, msg string) *uploadRefusal {
	return &uploadRefusal{Status: status, Error: msg}
}

// writeUploadRefusal renders a refusal onto the wire, byte for byte what the
// handlers wrote before the services existed.
func writeUploadRefusal(w http.ResponseWriter, ref *uploadRefusal) {
	if ref.JSON == nil {
		http.Error(w, ref.Error, ref.Status)
		return
	}
	writeJSON(w, ref.Status, ref.JSON)
}

// bodyText is the refusal as the self-update logs and reports it: the
// plain-text body, or the JSON body encoded, which is the text the loopback
// request used to read back (its trailing newline trimmed by every reader).
func (r *uploadRefusal) bodyText() string {
	if r.JSON == nil {
		return r.Error
	}
	b, err := json.Marshal(r.JSON)
	if err != nil {
		return r.Error
	}
	return string(b)
}

// agentInstalled is a delivered agent update: the binary is on the NAND and
// flash-verified, or complete in the RAM stage with the swap helper armed.
type agentInstalled struct {
	RAMStaged bool
	// afterAnswer restarts the speaker, or only the agent on a coprocessor
	// Wi-Fi ST20 (otarestart.go). It runs in the background once the 200 has
	// gone out; the 1.5 s it sleeps first is what lets that answer reach the
	// app before the box drops off the LAN.
	afterAnswer func()
}

// body is the exact 200 body: {status:"ok",action:"reboot"[,mode:"ram-staged"]}.
// "reboot" stays on an agent-only restart too: the app waits for the new
// build the same way either way, and older apps key off the exact body.
func (a agentInstalled) body() map[string]string {
	b := map[string]string{"status": "ok", "action": "reboot"}
	if a.RAMStaged {
		b["mode"] = "ram-staged"
	}
	return b
}

// installAgentUpload receives a new stick agent binary, writes it atomically
// to /mnt/nv/streborn/bin/streborn-armv7l and arms the restart. The body must
// be the raw ARM binary. On success the speaker still answers 200 OK and then
// reboots (afterAnswer); the rc.local bootstrap starts the new agent. A
// coprocessor Wi-Fi ST20 restarts only the agent instead (otarestart.go).
func (s *Server) installAgentUpload(up upload) (agentInstalled, *uploadRefusal) {
	// Lifting wireless power-save first makes the transfer ~17x faster on a
	// box idling in standby (see boostUploadThroughput); the update's reboot
	// resets the toggle, the deferred restore covers the failure paths.
	defer s.boostUploadThroughput("agent-update")()
	// Quieten the speaker before the transfer starts (hushforupload.go).
	s.hushForUpload("agent-update")
	const maxSize = 30 * 1024 * 1024
	if up.Size > maxSize {
		return agentInstalled{}, textUploadRefusal(http.StatusRequestEntityTooLarge, errUploadTooBig.Error())
	}
	// Log the whole upload lifecycle (#466): a bundle from a push that died
	// must say whether the request arrived, where it stalled, and how much
	// memory the speaker had left.
	memAtStart, memTotal := uploadMemKB()
	s.logger.Info("upload started", "endpoint", "agent-update",
		"contentLength", up.Size, "remote", up.Remote,
		"memAvailableKB", memAtStart, "memTotalKB", memTotal, "streamed", true)
	upr := &uploadProgressReader{
		r: io.LimitReader(up.Body, maxSize+1), start: time.Now(), logger: s.logger, what: "agent-update",
	}
	// The ELF gate (#302) needs only the header, so it is checked on a peek
	// and a body that is not a softfloat ARM binary never reaches the flash.
	br := bufio.NewReaderSize(upr, 4096)
	hdr, perr := br.Peek(40)
	if perr != nil {
		return agentInstalled{}, textUploadRefusal(http.StatusBadRequest, "binary too small to be an ELF")
	}
	if msg, ok := validSoftfloatARMELF(hdr); !ok {
		return agentInstalled{}, textUploadRefusal(http.StatusBadRequest, msg)
	}
	// Tier 2 (#270), moved in front of the transfer: a NAND that UBIFS parked
	// read-only is remounted BEFORE the body is read, because a streamed body
	// cannot be replayed into a second write attempt afterwards.
	if !nandWritable() && !remountNANDRW(s.logger) {
		return agentInstalled{}, textUploadRefusal(http.StatusInsufficientStorage, errNANDReadOnly.Error())
	}
	// The STR logo with a running bar on the Portable's OLED while the update
	// lands (internal/oled; a no-op elsewhere). The reboot ends it with the
	// process; every path that gives up without rebooting hands the panel back.
	stopSplash := oled.StartUpdating(s.logger.With("comp", "oled"))
	rebooting := false
	defer func() {
		if !rebooting {
			stopSplash()
		}
	}()
	// From here the engine may be reclaimed at any moment and the process may
	// exit. Cleared again only on the paths that give up WITHOUT restarting,
	// so a failed update does not leave the speaker refusing engine deliveries
	// until its next reboot.
	updateInFlight.Store(true)
	const dst = agentBinNANDPath
	got, err := receiveAgentBinary(dst, br, up.Size, maxSize)
	memAfter, _ := uploadMemKB()
	if err != nil {
		updateInFlight.Store(false)
		var rerr uploadReadError
		switch {
		case errors.As(err, &rerr):
			s.logger.Warn("upload aborted mid-stream", "endpoint", "agent-update",
				"bytesRead", upr.n, "elapsedMs", time.Since(upr.start).Milliseconds(),
				"memAvailableKB", memAfter, "memAvailableAtStartKB", memAtStart,
				"agentPeakKB", selfPeakKB(), "err", rerr.err)
			return agentInstalled{}, textUploadRefusal(http.StatusBadRequest, err.Error())
		case errors.Is(err, errUploadTooBig):
			return agentInstalled{}, textUploadRefusal(http.StatusRequestEntityTooLarge, err.Error())
		case errors.Is(err, errUploadTooSmall):
			return agentInstalled{}, textUploadRefusal(http.StatusBadRequest, err.Error())
		case isReadOnlyFSErr(err) || !nandWritable():
			// Parked read-only during the write. Remount now so the app's retry
			// (it repeats every 5xx) lands on a writable volume.
			_ = remountNANDRW(s.logger)
			s.logger.Warn("upload write failed on a read-only NAND", "endpoint", "agent-update", "bytes", got.size, "err", err)
			return agentInstalled{}, textUploadRefusal(http.StatusInsufficientStorage, errNANDReadOnly.Error())
		default:
			s.logger.Warn("upload write failed", "endpoint", "agent-update", "bytes", got.size,
				"elapsedMs", time.Since(upr.start).Milliseconds(),
				"memAvailableKB", memAfter, "memAvailableAtStartKB", memAtStart, "err", err)
			return agentInstalled{}, textUploadRefusal(nandWriteHTTPStatus(err), err.Error())
		}
	}
	s.logger.Info("upload body received", "endpoint", "agent-update",
		"bytes", got.size, "elapsedMs", time.Since(upr.start).Milliseconds(),
		"memAvailableKB", memAfter, "memAvailableAtStartKB", memAtStart,
		"agentPeakKB", selfPeakKB(), "ramStaged", got.ramStaged)

	// Tier 3 (#270): the volume writes fine but genuinely cannot hold OLD + NEW
	// agent side by side even after the reclaim (small ST20 volumes). The
	// upload already went to the RAM stage when the NAND said no mid-stream; a
	// detached helper swaps it in after this process exits, then reboots (or,
	// on a coprocessor Wi-Fi ST20, starts the new agent), so peak NAND need
	// drops to a single copy. errInsufficientNAND still means a
	// REAL failed write (ENOSPC / short write), never a statfs prediction.
	if got.ramStaged {
		helperPID, restartOnly, serr := s.armRAMStagedSwap(dst, got.size, got.sum)
		if serr != nil {
			updateInFlight.Store(false)
			_ = os.Remove(ramStageTarget)
			s.logger.Warn("RAM-staged swap unavailable, reporting the space failure", "err", serr)
			return agentInstalled{}, textUploadRefusal(http.StatusInsufficientStorage, errInsufficientNAND.Error()+": "+serr.Error())
		}
		rebooting = true
		return agentInstalled{RAMStaged: true, afterAnswer: func() {
			// Give the 200 OK time to flush, then exit: the helper waits for
			// this PID before copying (the running binary is ETXTBSY and its
			// blocks are pinned until we are gone).
			time.Sleep(1500 * time.Millisecond)
			// Refresh a still-inserted stick BEFORE exiting so the boot sync
			// cannot revert this update (#381). Delaying our exit is safe: the
			// swap helper waits for this PID.
			refreshStickAgentBinary(ramStageTarget, got.sum, s.logger)
			// Same OTA-reboot marker as the normal reboot path, but fsync'd:
			// os.Exit skips buffered flushes and the swap helper reboots the
			// box right after us, so the marker must be on NAND before we go.
			if f, werr := os.OpenFile(otaRebootMarkerPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); werr == nil {
				_, _ = f.WriteString(time.Now().UTC().Format(time.RFC3339) + "\n")
				_ = f.Sync()
				_ = f.Close()
			} else {
				s.logger.Warn("could not write OTA-reboot marker (ram-staged); post-OTA resume may fire", "err", werr)
			}
			if restartOnly {
				// The helper starts the new agent itself instead of rebooting
				// (otarestart.go): it holds run.sh's PID file through the copy,
				// so the watchdog neither respawns the old binary mid-copy nor
				// starts a second agent afterwards. The helper's wait covers
				// the boot-watchdog wait and the clean shutdown.
				s.waitOutBootWatchdog()
				s.handOverAndExit(helperPID, "RAM-staged binary swap, the helper starts the new agent, no box reboot")
				return
			}
			s.logger.Info("exiting for the RAM-staged binary swap; the helper reboots the box")
			os.Exit(0)
		}}, nil
	}

	// Safe-over-fast (#381): prove the binary is ON FLASH before telling the
	// app anything and before any reboot. The fsync in the streamed write
	// should make this a formality, but UBIFS + these boxes have burned us:
	// verify by re-reading past the page cache against the hash taken while
	// streaming, and refuse with a truthful error rather than reboot into the
	// old binary. The bytes are no longer held in memory for a local rewrite,
	// so a mismatch answers 500 and the app's retry (it repeats every 5xx)
	// sends the update again.
	//
	// The exception is a RAM-first receive (ota_ramfirst.go), which kept its
	// stage for this verify: a mismatch is copied onto the NAND once more from
	// RAM instead of making the app send the whole binary again over the link
	// that was too weak for the deferred route. The stage goes either way.
	defer releaseRAMFirstStage(got.ramFirstStage)
	verifyStart := time.Now()
	verr := verifyBinaryOnFlash(dst, got.sum)
	if verr != nil && got.ramFirstStage != "" {
		s.logger.Warn("agent update: flash verify failed, copying the RAM-first stage onto the NAND again", "err", verr)
		if rerr := reinstallFromRAMFirstStage(got.ramFirstStage, dst); rerr != nil {
			s.logger.Warn("agent update: re-copy from the RAM-first stage failed", "err", rerr)
		} else {
			verr = verifyBinaryOnFlash(dst, got.sum)
		}
	}
	if verr != nil {
		s.logger.Error("agent update: flash verify failed, refusing to reboot; the app will send it again", "err", verr)
		updateInFlight.Store(false)
		return agentInstalled{}, textUploadRefusal(http.StatusInternalServerError, "update did not persist to flash: "+verr.Error())
	}

	// A verified write supersedes any earlier tier-3 swap failure.
	_ = os.Remove(swapFailMarker)
	s.logger.Info("agent update written and flash-verified, restarting for a clean post-OTA state",
		"size", got.size, "verifyMs", time.Since(verifyStart).Milliseconds())
	rebooting = true
	// Reboot after an OTA rather than just self-restarting the process, on
	// every chassis but the coprocessor Wi-Fi ST20 (wlan-mode "bco"), where a
	// soft reboot can leave the speaker dark until it is unplugged and only
	// the agent restarts (otarestart.go). Jens 2026-06-01: a self-restart left
	// the freshly-updated box with no presets visible in the app; OTA replaces
	// only the binary, so the NAND run.sh + rc.local otherwise stay at the
	// pre-OTA vintage (project_ota_only_replaces_binary). A reboot makes the
	// new binary self-deploy its matching run.sh/rc.local AND re-run the
	// preset reconcile from clean. The start-up preset push and the forced
	// full re-sync now run on every agent start, which is what lets the bco
	// restart skip the reboot. Delay so the 200 OK above flushes to the
	// desktop app before the box drops off the LAN.
	return agentInstalled{afterAnswer: func() {
		time.Sleep(1500 * time.Millisecond)
		// Refresh a still-inserted stick BEFORE the reboot: run.sh's boot sync
		// copies the stick binary over NAND unconditionally, so a stale stick
		// would silently revert the binary just written to dst (#381). The
		// desktop app's SSH stick refresh covers this only when SSH is open;
		// this on-box write needs nothing. The reboot waits for it.
		refreshStickAgentBinary(dst, got.sum, s.logger)
		// Mark this reboot as OUR maintenance OTA so the agent that comes back up
		// does not immediately blast the room with the automatic power-on resume
		// (consumed once at the next start, resume_standby.go). A genuine power
		// outage leaves no marker and resumes as before. Written before the sync
		// below so it is flushed to NAND with everything else.
		if werr := os.WriteFile(otaRebootMarkerPath, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); werr != nil {
			s.logger.Warn("could not write OTA-reboot marker; post-OTA resume may fire", "err", werr)
		}
		// Unconditional flush before the reboot. refreshStickAgentBinary syncs
		// only when a stick is mounted; on a stickless box (the #381 field
		// case) nothing else flushed this path before v0.9.7. Every other
		// reboot in the project already syncs first — this one now does too.
		_ = exec.Command("sync").Run()
		time.Sleep(2 * time.Second)
		// The new binary is in place at dst. A reboot lets the boot path run
		// it; a coprocessor Wi-Fi ST20 restarts only the agent (otarestart.go).
		s.finishAgentOTA(dst)
	}}, nil
}

// installAgentFromDownload is installAgentUpload for the self-update: there
// is no answer for anyone to read, so the restart the handler starts after
// its 200 starts here.
func (s *Server) installAgentFromDownload(up upload) *uploadRefusal {
	res, ref := s.installAgentUpload(up)
	if ref != nil {
		return ref
	}
	go res.afterAnswer()
	return nil
}

// engineInstalled is a delivered Spotify engine.
type engineInstalled struct {
	Size int64
	// Reloaded reports whether the running engine was restarted onto the
	// new binary (the engineHotSwap capability the app keys off).
	Reloaded bool
}

// body is the exact 200 body: {status:"ok",reloaded:"true"|"false"}.
func (e engineInstalled) body() map[string]string {
	return map[string]string{"status": "ok", "reloaded": strconv.FormatBool(e.Reloaded)}
}

// engineBlockedByUpdate is the 409 an engine delivery gets while an agent
// update is installing, nil otherwise. The update reclaims the engine's
// ~16 MB when NAND is tight, so an engine delivered now is deleted a moment
// later and the speaker restarts without one. The caller cannot see that
// from outside, because the old agent answers normally until the restart,
// so the answer has to come from here. Deliver it again once the new version
// reports itself, which is what the update flow already does.
func (s *Server) engineBlockedByUpdate() *uploadRefusal {
	if !updateInFlight.Load() {
		return nil
	}
	s.logger.Info("sidecar refused: an agent update is in flight, the engine would be reclaimed by it")
	const msg = "An update is installing on this speaker. Deliver the Spotify engine again once the speaker reports the new version."
	return &uploadRefusal{Status: http.StatusConflict, Error: msg, JSON: map[string]string{
		"code":  "update-in-flight",
		"error": msg,
	}}
}

// installEngineUpload receives the go-librespot Spotify sidecar binary and
// writes it atomically to /mnt/nv/streborn/bin/go-librespot. It does NOT
// reboot the box, but it DOES hot-swap the engine in place: after the write
// it restarts the supervised go-librespot (spotifyReload) so the freshly
// delivered binary is live with no reboot (#240). A first-time delivery to a
// box that had no engine is picked up by the manager's waitForBinary instead;
// either way no reboot is needed. This closes the gap where the sidecar
// shipped only via the stick->NAND boot sync, leaving an OTA-only box (e.g. a
// SoundTouch 30 whose USB stick never copied it) silently unable to play
// Spotify despite a synced login (#45/#105).
//
// Hot-swapping also frees NAND sooner: the old engine inode is held by the
// running process until it exits, so killing+relaunching it on the write
// releases that ~10 MB immediately instead of holding it until the next
// reboot.
func (s *Server) installEngineUpload(up upload) (engineInstalled, *uploadRefusal) {
	if ref := s.engineBlockedByUpdate(); ref != nil {
		return engineInstalled{}, ref
	}
	// Streamed, not buffered. This is the ~16 MB engine and the box has ~120 MB
	// of RAM: holding it in memory first was enough to reboot a speaker that
	// happened to be busy (see streamBinaryAtomic). The agent-update endpoint
	// deliberately still buffers, because its fallback tiers need the bytes
	// again after the write.
	defer s.boostUploadThroughput("sidecar")()
	sum, size, ref := s.streamELFUpload(up, s.logger, "sidecar", goLibrespotBinPath)
	if ref != nil {
		return engineInstalled{Size: size}, ref
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
	return engineInstalled{Size: size, Reloaded: reloaded}, nil
}

// streamUploadedELF is streamELFUpload on the wire: the method and LAN gates,
// then the receive, with every refusal rendered the way the handler used to.
func (s *Server) streamUploadedELF(w http.ResponseWriter, r *http.Request, logger *slog.Logger, what, dst string) (sum string, size int64, ok bool) {
	if !requireMethod(w, r, http.MethodPost) {
		return "", 0, false
	}
	if !isLocalLAN(r.RemoteAddr) {
		s.refuseNonLAN(w, r, what)
		return "", 0, false
	}
	sum, size, ref := s.streamELFUpload(upload{Body: r.Body, Size: r.ContentLength, Remote: r.RemoteAddr}, logger, what, dst)
	if ref != nil {
		writeUploadRefusal(w, ref)
		return "", size, false
	}
	return sum, size, true
}

// streamELFUpload is the engine upload's receive for a caller that does not
// need the bytes afterwards: same guards, same logging, but the body goes to
// dst as it arrives rather than into memory first.
//
// The ELF check still happens before anything is written, by peeking the
// first four bytes; a body that is not a binary never reaches the flash.
func (s *Server) streamELFUpload(up upload, logger *slog.Logger, what, dst string) (sum string, size int64, ref *uploadRefusal) {
	s.hushForUpload(what)
	const maxSize = 30 * 1024 * 1024
	memAtStart, memTotal := uploadMemKB()
	logger.Info("upload started", "endpoint", what,
		"contentLength", up.Size, "remote", up.Remote,
		"memAvailableKB", memAtStart, "memTotalKB", memTotal, "streamed", true)
	upr := &uploadProgressReader{
		r: io.LimitReader(up.Body, maxSize+1), start: time.Now(), logger: logger, what: what,
	}
	br := bufio.NewReaderSize(upr, 4096)
	magic, err := br.Peek(4)
	if err != nil || magic[0] != 0x7f || magic[1] != 'E' || magic[2] != 'L' || magic[3] != 'F' {
		return "", 0, textUploadRefusal(http.StatusBadRequest, "not an ELF binary")
	}
	sum, size, werr := streamBinaryAtomic(dst, br, up.Size)
	memAfter, _ := uploadMemKB()
	if werr != nil {
		logger.Warn("upload write failed", "endpoint", what, "bytes", size,
			"elapsedMs", time.Since(upr.start).Milliseconds(),
			"memAvailableKB", memAfter, "memAvailableAtStartKB", memAtStart, "err", werr)
		return "", size, textUploadRefusal(nandWriteHTTPStatus(werr), werr.Error())
	}
	logger.Info("upload body received", "endpoint", what,
		"bytes", size, "elapsedMs", time.Since(upr.start).Milliseconds(),
		"memAvailableKB", memAfter, "memAvailableAtStartKB", memAtStart,
		"agentPeakKB", selfPeakKB())
	if size > maxSize {
		return "", size, textUploadRefusal(http.StatusRequestEntityTooLarge, "binary too big")
	}
	if size < 1024 {
		return "", size, textUploadRefusal(http.StatusBadRequest, "binary too small")
	}
	return sum, size, nil
}
