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
	"strconv"
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

// recordNANDHeadroom logs the box's writable-volume headroom to the OTA journal
// before a push. The agent reports nandFreeBytes/nandTotalBytes from /api/agent/
// version (added 2026-06-24); a box on an older agent simply omits them, in which
// case we note "unknown". Best-effort and non-blocking: it never fails the OTA.
// need is the binary size, so the journal shows whether the second copy the
// atomic write requires can fit.
func (a *App) recordNANDHeadroom(host string, port int, need int64) {
	ver, err := a.BoxAgentVersion(host, port)
	if err != nil {
		a.recordOTA(host, "nand: headroom unknown (version read failed: "+err.Error()+")")
		return
	}
	free, total := ver["nandFreeBytes"], ver["nandTotalBytes"]
	if free == "" && total == "" {
		a.recordOTA(host, "nand: headroom unknown (agent predates the disk-usage report)")
		return
	}
	freeN, _ := strconv.ParseInt(free, 10, 64)
	fits := "ok"
	if freeN > 0 && freeN < need+512*1024 {
		fits = "TIGHT (a second copy for the atomic write may not fit)"
	}
	a.recordOTA(host, fmt.Sprintf("nand: free=%sB total=%sB need=%dB -> %s", free, total, need, fits))
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
	// Record the box's NAND headroom before the push so a "no space left on
	// device" failure is diagnosable from the journal (the ~31 MB writable volume
	// must hold a second ~10 MB copy during the atomic write). Older agents do not
	// report these fields; absent means "unknown", never blocks the OTA. The agent
	// also embeds a full inventory in the failure error itself (#ST30, 2026-06-24).
	a.recordNANDHeadroom(host, port, int64(len(bin)))
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

	// Deliver the go-librespot Spotify sidecar BEFORE the agent binary push, and
	// copy everything onto the box so it reboots exactly once with all files
	// already in place — no post-reboot re-install (better UX). The agent binary
	// push reboots the box ~1.5 s after its reply, so the sidecar must be on disk
	// before that. When the box already runs a sidecar-capable agent we stage AND
	// verify the sidecar here, retrying transient drops, and only then push the
	// rebooting binary. The historical reason the sidecar shipped only via the
	// stick->NAND boot sync was that an OTA-only box (e.g. a SoundTouch 30 whose
	// USB port underpowers the stick) had a synced login but no engine, so
	// Spotify silently never played (#45/#105). A box still on a pre-v0.8.22 agent
	// has no /api/agent/sidecar endpoint and cannot be staged over HTTP at all;
	// that one-time transition is the only case left to the post-OTA
	// EnsureSpotifyEngine re-delivery (#240). Best-effort: a sidecar problem must
	// never block the (more important) agent update.
	a.stageSidecarBeforeReboot(host, port)

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
			// A 507 means the speaker's storage is full and its (old) agent
			// cannot free space by itself, and the SSH recovery could not open
			// SSH either (no :17000, or the unlock failed). Tell the user the
			// one thing that always works — a one-time USB-stick update — rather
			// than a raw "status 507".
			if strings.Contains(err.Error(), "507") || strings.Contains(strings.ToLower(err.Error()), "insufficient nand") {
				return fmt.Errorf("the speaker's storage is full and it is on an older STR version that cannot free space over the network. Update this speaker once from a USB stick (that cleans up the storage and installs the current version); after that, updates manage the space themselves. Details: %v", err)
			}
			return fmt.Errorf("HTTP OTA failed (%v) and the SSH fallback also failed: %w", err, sshErr)
		}
		a.logger.Info("update agent: SSH-OTA succeeded after HTTP failure", "host", host, "bytes", len(bin))
	}
	return nil
}

// pushSidecarIfNeeded delivers the embedded go-librespot Spotify sidecar to the
// box over HTTP (POST /api/agent/sidecar) when the box is missing it or has a
// different build, and verifies it actually landed. Returns nil when the engine
// is already current or was delivered and confirmed; returns an error when a
// push was needed but did not land. The agent-OTA caller treats it as
// best-effort (the error is ignored, the OTA still proceeds); EnsureSpotifyEngine
// surfaces the outcome. The ~10 MB transfer is gated on the box's reported
// content hash so a steady state (sidecar already current) costs just one cheap
// version GET and sends zero sidecar bytes.
func (a *App) pushSidecarIfNeeded(host string, port int) (delivered bool, err error) {
	if !agentbin.GoLibrespotAvailable() {
		// Dev build (empty stub): nothing real to deliver. Never write 0 bytes.
		a.recordOTA(host, "sidecar: skipped, no embedded go-librespot in this build")
		return false, nil
	}
	bin := agentbin.GoLibrespotBytes()
	sum := sha256.Sum256(bin)
	want := hex.EncodeToString(sum[:])

	// Ask the box what it already has. boxDo self-heals across :8888/:17008 and
	// caches the working port for the agent OTA that follows.
	ver, verErr := a.BoxAgentVersion(host, port)
	if verErr == nil {
		if ver["goLibrespot"] == "present" && ver["goLibrespotSha256"] == want {
			a.recordOTA(host, "sidecar: box already has the current go-librespot, skipping ~10 MB push")
			return false, nil
		}
	} else {
		// STR's agent is not answering. Pushing blind would stream ~16 MB at
		// whatever owns the port instead - during the post-reboot window on
		// whitelisted chassis that is Bose's OWN SoftwareUpdate daemon on
		// :17008, and each bogus push repaints the speaker's update display
		// ("0% uploading", #270). The push can never land without the agent,
		// so fail fast; callers retry with a cheap version GET. Pre-sidecar
		// agents still answer /api/agent/version, so gating on the probe
		// loses nothing.
		a.recordOTA(host, "sidecar: agent version probe failed, not pushing blind: "+verErr.Error())
		return false, fmt.Errorf("sidecar: agent unreachable (version probe failed): %w", verErr)
	}

	// Space pre-flight (#270 deqw). Never stream ~16 MB the box cannot hold: the
	// agent rejects it and every retry re-uploads the whole engine, which flashes
	// the speaker ("0% uploading") and reboots it. Spotify stays enabled for
	// everyone - this only skips a doomed, thrashing push. If the box reports too
	// little free NAND, log the shortfall and stop; the full /mnt/nv breakdown is
	// in the diagnostic bundle (nv_root_listing / disk_usage) so leftovers eating
	// the space can be spotted, and the agent's own supervise loop delivers the
	// engine once space frees.
	//
	// A present-but-outdated engine counts as free space: the agent's sidecar
	// write drops the old engine before writing the new one (its reclaim
	// cascade), so gating on the raw free figure alone would refuse an engine
	// UPDATE on a tight box forever even though the swap fits.
	if freeN, perr := strconv.ParseInt(ver["nandFreeBytes"], 10, 64); perr == nil && freeN > 0 {
		reclaimable := int64(0)
		if ver["goLibrespot"] == "present" {
			if sz, e := strconv.ParseInt(ver["goLibrespotSizeBytes"], 10, 64); e == nil && sz > 0 {
				reclaimable = sz
			} else {
				// Agent predates the size report: engine builds differ only
				// marginally in size, assume the embedded one.
				reclaimable = int64(len(bin))
			}
		}
		need := int64(len(bin)) + otaSidecarSpaceMargin
		if freeN+reclaimable < need {
			totalKB := int64(0)
			if t, e := strconv.ParseInt(ver["nandTotalBytes"], 10, 64); e == nil {
				totalKB = t / 1024
			}
			a.recordOTA(host, fmt.Sprintf("sidecar: not enough NAND for the engine right now (free=%dKB reclaimable=%dKB total=%dKB, need~%dKB) - skipping the upload to avoid thrashing; the agent will deliver it once space frees. See the diagnostic bundle's /mnt/nv listing for leftovers.", freeN/1024, reclaimable/1024, totalKB, need/1024))
			a.logger.Warn("sidecar push: insufficient NAND, skipping the upload to avoid the retry thrash", "host", host, "freeBytes", freeN, "reclaimableBytes", reclaimable, "needBytes", need)
			return false, fmt.Errorf("sidecar: insufficient NAND (free=%d reclaimable=%d need=%d)", freeN, reclaimable, need)
		}
	}

	a.recordOTA(host, fmt.Sprintf("sidecar: pushing go-librespot (%d bytes, sha %s)", len(bin), want[:12]))
	if err := a.streamPostBinary(host, port, "/api/agent/sidecar", bin); err != nil {
		a.recordOTA(host, "sidecar: push failed (agent OTA still proceeds): "+err.Error())
		a.logger.Warn("sidecar push failed; agent OTA continues, Spotify may stay unavailable until next OTA", "host", host, "err", err)
		return false, fmt.Errorf("sidecar upload failed: %w", err)
	}
	// Verify the push actually landed. A pre-v0.8.22 agent has NO
	// /api/agent/sidecar route, so its ServeMux falls through to the "/" index
	// handler and answers 200 with the web UI: streamPostBinary then reports a
	// false success and the 16 MB body is discarded. That is the gap that left a
	// SoundTouch 30 upgraded from an older agent without the engine despite a
	// "delivered" log (#237). Re-read the version and require the box to now
	// report the engine present with our content hash; otherwise treat it as not
	// delivered so the caller retries once the box is on the new, capable agent.
	if ver, err := a.BoxAgentVersion(host, port); err != nil {
		a.recordOTA(host, "sidecar: delivered but could not verify (version read failed): "+err.Error())
		a.logger.Warn("sidecar push: delivered but post-push version read failed", "host", host, "err", err)
		return false, fmt.Errorf("sidecar verify failed: %w", err)
	} else if ver["goLibrespot"] != "present" || ver["goLibrespotSha256"] != want {
		a.recordOTA(host, "sidecar: push did not land (agent has no sidecar endpoint yet); will retry after the box is on the new agent")
		a.logger.Warn("sidecar push did not land; box still reports the engine missing/mismatched (old agent without the sidecar endpoint)",
			"host", host, "goLibrespot", ver["goLibrespot"])
		return false, fmt.Errorf("sidecar did not land: box reports goLibrespot=%q", ver["goLibrespot"])
	}
	a.recordOTA(host, "sidecar: go-librespot delivered")
	a.logger.Info("sidecar push: go-librespot delivered over OTA", "host", host, "bytes", len(bin))
	return true, nil
}

// stageSidecarBeforeReboot delivers the Spotify sidecar to the box during an
// agent OTA, BEFORE the agent binary push triggers the single reboot, so the box
// comes up with the engine already in place and needs no post-reboot re-install.
//
// It only blocks/retries when staging can actually succeed pre-reboot:
//   - Sidecar-capable agent (reports the goLibrespot field, has the
//     /api/agent/sidecar endpoint): stage and verify here, retrying transient
//     drops, so the file is on disk before the reboot.
//   - Pre-v0.8.22 agent (no goLibrespot field, no endpoint): the sidecar cannot
//     be staged over HTTP at all because the *running* agent has no code to
//     receive it (the new binary on disk only takes effect after the reboot). So
//     that one-time transition is left to the post-OTA EnsureSpotifyEngine
//     re-delivery (#240); blocking here would just waste the OTA window.
//
// Never fatal: a sidecar problem must not block the agent update.
func (a *App) stageSidecarBeforeReboot(host string, port int) {
	if !agentbin.GoLibrespotAvailable() {
		// Dev build (empty stub): nothing real to deliver.
		return
	}
	ver, err := a.BoxAgentVersion(host, port)
	if err != nil {
		// Can't tell whether the agent is sidecar-capable; one best-effort
		// attempt, then leave anything unresolved to the post-OTA re-delivery.
		_, _ = a.pushSidecarIfNeeded(host, port)
		return
	}
	if _, capable := ver["goLibrespot"]; !capable {
		a.recordOTA(host, "sidecar: box on a pre-sidecar agent, cannot stage over HTTP pre-reboot; will deliver after the box is on the new agent")
		return
	}
	// Space gate (#ST30 Daniel). Staging the ~10 MB sidecar pre-reboot lands it
	// on disk BEFORE the agent .new (~12 MB) is written, so on a tight box (e.g. a
	// SoundTouch 30, ~31 MB NAND) the two second copies cannot coexist and the
	// agent OTA then dies with "no space left on device". When the box cannot hold
	// the agent AND the sidecar second copy together, skip the pre-reboot staging:
	// the agent updates alone (it fits with the sidecar out of the way), and the
	// post-OTA EnsureSpotifyEngine delivers the engine after the reboot, when the
	// old agent's blocks have been reclaimed. Only a real, reported free figure
	// gates this; an unknown free (older agent) keeps the previous behaviour.
	if freeN, perr := strconv.ParseInt(ver["nandFreeBytes"], 10, 64); perr == nil && freeN > 0 {
		agentLen := int64(len(agentbin.Bytes()))
		sidecarLen := int64(len(agentbin.GoLibrespotBytes()))
		const nandStageMargin = 2 * 1024 * 1024
		// A present old engine is dropped by the sidecar write before the new
		// one lands, so count it as headroom (same rule as pushSidecarIfNeeded).
		if ver["goLibrespot"] == "present" {
			if sz, e := strconv.ParseInt(ver["goLibrespotSizeBytes"], 10, 64); e == nil && sz > 0 {
				freeN += sz
			} else {
				freeN += sidecarLen
			}
		}
		if freeN < agentLen+sidecarLen+nandStageMargin {
			a.recordOTA(host, fmt.Sprintf("sidecar: NAND tight (free=%dKB < agent %dKB + engine %dKB + margin), deferring the engine to the post-reboot delivery so the agent update fits", freeN/1024, agentLen/1024, sidecarLen/1024))
			a.logger.Info("stageSidecarBeforeReboot: NAND too tight to stage the engine before the agent; deferring to post-OTA EnsureSpotifyEngine",
				"host", host, "free", freeN, "agentLen", agentLen, "sidecarLen", sidecarLen)
			return
		}
	}
	for attempt := 1; attempt <= otaSidecarEnsureAttempts; attempt++ {
		if _, err := a.pushSidecarIfNeeded(host, port); err == nil {
			return
		} else if attempt < otaSidecarEnsureAttempts {
			a.logger.Info("stageSidecarBeforeReboot: pre-reboot sidecar push not landed yet, retrying",
				"host", host, "attempt", attempt, "err", err)
			time.Sleep(otaSidecarEnsureBackoff(attempt))
		} else {
			a.recordOTA(host, "sidecar: pre-reboot staging still failing after retries; will retry after the box is on the new agent: "+err.Error())
		}
	}
}

// EnsureSpotifyEngine makes sure the go-librespot Spotify sidecar is present on
// the box, delivering it over the air when missing. It is the post-upgrade
// reconcile for #237: the sidecar is normally pushed during the agent OTA, but a
// box upgraded FROM a pre-v0.8.22 agent received that push on an old agent with
// no sidecar endpoint, so it silently no-op'd and the box came up on the new
// agent with a synced Spotify login but no engine (Spotify then failed with the
// "tap this speaker in Spotify once" hint). The desktop calls this right after
// the post-OTA version poll confirms the box is on the new, sidecar-capable
// agent: the push now lands and the engine is activated.
//
// Activation depends on the agent's capability. A hot-swap-capable agent
// (engineHotSwap=true, #240) restarts go-librespot in place the instant the
// sidecar write lands, so the engine is live with no reboot at all. An older
// agent binds the binary only at process start, so an already-running or
// never-started go-librespot is not picked up live, which is why Pierre had to
// reboot the box by hand after the update (#240, 2026-06-25); for that case this
// reboots the box a SECOND time, as part of the same OTA, to bring the engine up
// cleanly. Idempotent and cheap when the engine is already current (one version
// GET, zero bytes, no reboot). Bound for the frontend; only called as part of an
// update, so any reboot stays inside the OTA process.
//
// It blocks until the box is back up with the engine present (bounded by
// otaEngineRebootWait) so the caller reports success only once Spotify can
// actually play; the UI shows the "finishing + restarting" step meanwhile. On a
// transient drop right after a reboot it returns the error so the caller's retry
// loop tries again while keeping the user informed.
func (a *App) EnsureSpotifyEngine(host string, port int) (string, error) {
	if !agentbin.GoLibrespotAvailable() {
		return "no embedded engine in this build", nil
	}
	delivered, err := a.pushSidecarIfNeeded(host, port)
	if err != nil {
		return "", err
	}
	if !delivered {
		// Engine already current: the running agent is supervising it, no
		// reboot. Distinct return value so callers can tell "nothing to do"
		// from "delivered now" (the UI only announces the latter).
		return "current", nil
	}
	// Hot-swap path (#240): a sidecar-capable agent that advertises engineHotSwap
	// restarts go-librespot in place the moment the sidecar write lands, so the
	// freshly delivered engine is already active and no box reboot is needed. This
	// removes the manual restart Pierre and Daniel had to do after an update. Only
	// an older agent that binds the binary at process start (no engineHotSwap)
	// falls back to the activation reboot below. The version read here is cheap and
	// uses the port boxDo already cached during the sidecar push.
	if ver, verr := a.BoxAgentVersion(host, port); verr == nil && ver["engineHotSwap"] == "true" {
		a.recordOTA(host, "engine: delivered and hot-swapped live by the agent, no reboot needed")
		a.logger.Info("ensure spotify engine: engine delivered and hot-swapped live, skipping the activation reboot", "host", host)
		return "ok", nil
	}
	// Reboot the box once more, as part of this same OTA, so the fresh agent picks
	// up the new engine at start instead of leaving the user to restart by hand.
	// notePostOTA keeps discovery pinning the box across the restart.
	a.recordOTA(host, "engine: delivered, rebooting box once more to activate it")
	a.notePostOTA(host)
	if rerr := a.RebootBox(host, port); rerr != nil {
		// The engine is staged on disk and will come up on the next restart, so do
		// not hard-fail; report it so the UI can fall back to a manual restart hint.
		a.logger.Warn("ensure spotify engine: activation reboot could not be triggered; engine staged, needs a restart", "host", host, "err", rerr)
		return "engine-staged-reboot-failed", nil
	}
	// Wait for the box to return with the engine present.
	deadline := time.Now().Add(otaEngineRebootWait)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		if ver, e := a.BoxAgentVersion(host, port); e == nil && ver["goLibrespot"] == "present" {
			a.recordOTA(host, "engine: present after the activation reboot")
			return "ok", nil
		}
	}
	// Did not confirm in time (slow reboot, or a changed IP after DHCP): the engine
	// is delivered and will be live once the box finishes; let the caller poll on.
	return "", fmt.Errorf("engine delivered and box rebooted, but it did not report the engine present within %s", otaEngineRebootWait)
}

// otaEngineRebootWait bounds how long EnsureSpotifyEngine waits for the box to
// come back with the engine present after the activation reboot. Generous
// because a BCO box reboot plus the agent coming up can take well over a minute.
const otaEngineRebootWait = 3 * time.Minute

// otaSidecarSpaceMargin is the NAND headroom required on top of the engine size
// before pushSidecarIfNeeded will stream it. Matches the pre-reboot stage gate;
// covers the atomic-write temp copy and normal UBIFS overhead so a push is only
// attempted when it can actually land (#270).
const otaSidecarSpaceMargin = 2 * 1024 * 1024

// otaSidecarEnsureAttempts bounds the pre-reboot sidecar staging retry in
// stageSidecarBeforeReboot. With otaSidecarEnsureBackoff the cumulative wait
// across attempts is 3+6+12+24+30 = 75 s, comfortably inside the 4-minute
// otaRebootGrace, so a transient drop while staging the engine before the reboot
// is retried without giving up too early.
const otaSidecarEnsureAttempts = 6

// otaSidecarEnsureBackoff is the wait before the next sidecar staging attempt:
// 3s, 6s, 12s, 24s, then capped at 30s. Exponential so a box that is nearly
// ready is retried quickly, with a cap so a slow box is not hammered.
func otaSidecarEnsureBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := 3 * time.Second << (attempt - 1)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
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
	prog := newTransferProgress(a, "box:update:progress", total, host)
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
		// SSH is closed (opt-in since v0.8.1). Before giving up, open it
		// stick-free via the box's :17000 setup port — the same unlock the
		// install path uses. This is the ONLY remote rescue for a box stuck
		// on a pre-reclaim agent (< v0.8.34, e.g. v0.8.32) whose NAND is full:
		// that old agent cannot free space during an HTTP update and SSH is
		// off, so the update otherwise dead-ends at "ssh handshake failed"
		// (Peter's ST10, 2026-07-09). Once SSH is open, the SSH upload command
		// below frees the NAND (drops the ~16 MB engine when tight) and pushes
		// the fresh agent, after which the box self-manages space. Only when
		// :17000 answers.
		opened := false
		if tcpReachable(host, 17000, 2*time.Second) {
			a.logger.Info("SSH-OTA: plain SSH closed, opening it stick-free via :17000 to recover a full/old box", "host", host)
			var tlog string
			opened, tlog = a.enableSSHViaTelnet(host, "")
			if opened {
				// Restore stock cloud URLs while :17000 is still plain Bose TAP,
				// so the injected str-setup.invalid does not leave the box
				// marge-checking a dead host (the light-bar sweep). Best-effort.
				if rerr := a.resetBoseURLsViaTelnet(host); rerr != nil {
					a.logger.Warn("SSH-OTA: could not restore cloud URLs after stick-free unlock", "host", host, "err", rerr)
				}
				a.logger.Info("SSH-OTA: SSH opened stick-free via :17000; continuing the update over SSH", "host", host)
			} else {
				// The unlock may have written a dead .invalid URL; restore stock
				// URLs + reboot so the box is never left marge-checking a dead
				// host (also protects a later stick install's interception).
				a.restoreStockBoseURLsAndReboot(host)
				a.logger.Warn("SSH-OTA: stick-free :17000 unlock did not open SSH", "host", host, "log", lastN(tlog, 200))
			}
		}
		if !opened {
			if hello, err := sshHandshake(host, 2); err != nil || !strings.Contains(hello, "STR_SSH_OK") {
				return fmt.Errorf("ssh handshake failed: %v (%s)", err, strings.TrimSpace(hello))
			}
		}
	}
	// mkdir + cat in one ssh-with-stdin session so a missing parent dir on
	// a freshly-installed box does not need a separate round-trip. The
	// 120 s timeout covers a 10 MB upload over slow Wi-Fi with the
	// speaker's modest CPU spending time on SSH crypto.
	// Pre-clean before staging the .new so the SSH path is a real rescue on a full
	// box, not a second failure: drop a stranded SSH-repair staging dir (the ~28 MB
	// streborn-install that filled a ST30, #ST30 Daniel) and the same regenerable
	// junk the agent's reclaimNAND clears, then write the fresh temp. Mirrors
	// reclaimNAND + run.sh cleanup_nand; the agent runs from streborn/bin so the
	// staging dir is always safe to drop. Best-effort, folded into the one session.
	// When the NAND is still too tight for the agent .new after the cheap reclaim,
	// drop the ~16 MB go-librespot engine too (regenerable: the post-OTA
	// EnsureSpotifyEngine re-delivers it). Gated on a df check so a roomy box keeps
	// its engine and does not re-fetch it every SSH update (#119). Mirrors the
	// on-box reclaimSpotifyEngine second tier.
	needKB := (int64(len(bin)) + 2*1024*1024) / 1024
	uploadCmd := fmt.Sprintf("rm -rf /mnt/nv/streborn-install /mnt/nv/streborn/streborn-install 2>/dev/null; "+
		"rm -f /mnt/nv/sp-oauth.out /mnt/nv/streborn/cap*.ogg /mnt/nv/streborn/bin/*.new 2>/dev/null; "+
		"free=$(df -k /mnt/nv 2>/dev/null | tail -1 | awk '{print $(NF-2)}'); "+
		"if [ \"${free:-0}\" -lt \"%d\" ]; then rm -f /mnt/nv/streborn/bin/go-librespot /mnt/nv/streborn/bin/go-librespot.sha256 2>/dev/null; fi; "+
		"mkdir -p /mnt/nv/streborn/bin && cat > /mnt/nv/streborn/bin/streborn-armv7l.new", needKB)
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
	// Space gate, mirroring updateAgentViaSSH's df check (#270): the .new copy
	// must fit NEXT TO whatever is on the NAND. When it does not, drop the old
	// engine first (regenerable - this push replaces it anyway) and re-check;
	// if the box still cannot hold it, skip instead of writing a truncated
	// binary into ENOSPC (that is what happened live on a tight ST20). The
	// post-OTA EnsureSpotifyEngine retries once space frees.
	needKB := (int64(len(bin)) + 2*1024*1024) / 1024
	uploadCmd := fmt.Sprintf("free=$(df -k /mnt/nv 2>/dev/null | tail -1 | awk '{print $(NF-2)}'); "+
		"if [ \"${free:-0}\" -lt \"%d\" ]; then rm -f %s %s.sha256 2>/dev/null; fi; "+
		"free=$(df -k /mnt/nv 2>/dev/null | tail -1 | awk '{print $(NF-2)}'); "+
		"if [ \"${free:-0}\" -lt \"%d\" ]; then echo STR_NO_SPACE; exit 1; fi; "+
		"mkdir -p /mnt/nv/streborn/bin && cat > %s.new", needKB, dst, dst, needKB, dst)
	if out, err := boxSSHUploadStdin(host, uploadCmd, bytes.NewReader(bin), 120*time.Second); err != nil {
		if strings.Contains(out, "STR_NO_SPACE") {
			a.logger.Warn("sidecar SSH push skipped: NAND cannot hold the engine (agent OTA still proceeds; delivered later once space frees)", "host", host, "needKB", needKB)
			return
		}
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
