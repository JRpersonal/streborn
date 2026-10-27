// Agent self-administration: version, OTA update upload, NAND inventory and SSH.

package webui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/JRpersonal/streborn/internal/hosts"
	"github.com/JRpersonal/streborn/internal/internet"
	"github.com/JRpersonal/streborn/internal/oled"
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
			// two cases apart. The desktop app's update run acts on it when the
			// agent update did not complete (the engine goes back onto the old
			// agent); outside a run nothing does: a binary reaches a speaker
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
	// The speaker's effective country and where it came from ("str": the
	// wizard or app setting, "box": the firmware's countryCode). A US speaker
	// gets the firmware's own Pandora and iHeartRadio back (usservices.go).
	if ri := s.regionInfo(); ri.Country != "" {
		out["region"] = ri.Country
		out["regionSource"] = ri.Source
	}
	// Whether a USB stick is plugged in, so the app opens SSH for its stick
	// refresh only when there is a stick to refresh (usbstick.go).
	out["usbStick"] = usbStickField()
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
	// When the last round of a display game was saved, so the app, which
	// probes this anyway, fetches the scores only after a new round.
	if at := oled.LastRoundAt(); !at.IsZero() {
		out["arcadeAt"] = strconv.FormatInt(at.Unix(), 10)
	}
	// Wedged-control state (see wedge.go): the desktop app and the phone
	// remote read this to tell the user a power-cycle is needed.
	if status, since := s.BoxHealth(); status != "ok" {
		out["boxHealth"] = status
		out["boxHealthSinceSec"] = strconv.FormatInt(int64(time.Since(since).Seconds()), 10)
	} else {
		out["boxHealth"] = "ok"
	}
	// Whether the speaker reaches the internet (internal/internet): a passive
	// verdict fed by the clock sync and the stream proxy, no probe of its own.
	// "unreachable" only on positive evidence. The desktop app turns it into
	// "this speaker cannot reach the internet, check the router" where the
	// owner looks, instead of a station that "stops after a few seconds"
	// (2026-10-14: two hours of rebooting five speakers whose router had cut
	// them off the internet while the LAN kept working).
	inetStatus, inetReason := internet.Status()
	out["internet"] = inetStatus
	if inetReason != "" {
		out["internetReason"] = inetReason
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
	// A cloud URL STR's /etc/hosts redirect can never catch (#986). Two file
	// reads, no request to the speaker, and emitted only when there is
	// something wrong, so the polled response stays small. This is the flag
	// whose absence cost #986 three days: the box looked healthy from every
	// angle the app could see while it asked a dead server for everything.
	if f := foreignCloudURL(); f != "" {
		out["foreignCloudURL"] = f
	}
	// A group-key press that was refused. Until now the reason lived only in
	// the debugState snapshot, so the owner pressed the thumbs key, nothing
	// happened, and nothing anywhere said why. Two users spent days re-saving
	// a group that was never at fault (2026-09-26). Emitted only while it is
	// recent, so the polled response stays small and a long-fixed failure does
	// not haunt the speaker for ever.
	// A long press the speaker made but STR could not keep. Same problem as
	// the group key above: it happened, it was refused, and nothing the owner
	// can see said so. The commonest case is a hold while Spotify plays, which
	// STR has no preset form for, so the firmware keeps the old station and
	// the key appears to snap back on its own.
	if s.heldRefusalFn != nil {
		if at, slot, source, name, ok := s.heldRefusalFn(); ok && time.Since(at) < heldRefusalWindow {
			out["holdRefusedSlot"] = strconv.Itoa(slot)
			out["holdRefusedSource"] = source
			out["holdRefusedAgeSec"] = strconv.FormatInt(int64(time.Since(at).Seconds()), 10)
			if name != "" {
				out["holdRefusedName"] = name
			}
		}
	}
	// A folder key whose saved file addresses the media server no longer has
	// (queuebadurl.go): the server answers, every path is gone, typically a
	// USB stick swapped on a router's media server. Only re-saving the folder
	// cures it, so the app and the phone page say exactly that. Emitted until
	// the key is saved again, at most an hour.
	if slot, age, ok := s.StaleFolderKey(); ok {
		out["staleFolderKeySlot"] = strconv.Itoa(slot)
		out["staleFolderKeyAgeSec"] = strconv.FormatInt(int64(age.Seconds()), 10)
	}
	if s.groupKeys != nil {
		if a, ok := s.groupKeys.LastError(); ok && time.Since(a.At) < groupKeyErrorWindow {
			out["groupKeyError"] = a.Error
			out["groupKeyErrorKey"] = a.Key
			out["groupKeyErrorAgeSec"] = strconv.FormatInt(int64(time.Since(a.At).Seconds()), 10)
			if a.Template != "" {
				out["groupKeyErrorTemplate"] = a.Template
			}
		}
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
	// The firmware's OWN setup episodes (#873). Emitted only when there has
	// been one, same "keep the common response small" rule as conflictingMod.
	//
	// This is the field that lets the desktop app say, after the fact, that the
	// speaker was finishing its own out-of-box setup during a window in which
	// it answered the PC nothing at all: the app cannot see a setup access
	// point going up, and reported the resulting silence as a failed install.
	if state, episodes, lastSec := s.BoxSetup(); episodes > 0 {
		out["boxSetup"] = state
		out["boxSetupEpisodes"] = strconv.Itoa(episodes)
		out["boxSetupLastSec"] = strconv.Itoa(lastSec)
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
	// And the binary this process IS, which is a different question. The two
	// differ for exactly one reason and it is the reason both fields exist: a
	// push that reached the disk and then did not boot (#381). Only the running
	// hash can confirm an update, and only the pair can tell a rollback from a
	// clock: v0.9.88 stamped the app and the agent it embeds a minute apart, so a
	// stamp comparison called every successful update a failure, and reading the
	// ON-DISK hash instead would have called every rollback a success.
	if sha := runningBinaryStamp(); sha != "" {
		out["agentRunningSha256"] = sha
	}
	// The name this speaker answers for itself, so the desktop app can put a
	// name on the phone-remote QR code rather than an address that changes with
	// the next DHCP lease. Absent while no responder is running, which is the
	// only honest answer.
	if n := mdnsHostName; n != "" {
		out["mdnsName"] = n
	}
	// Which speaker this is, so the app can tell it from another one that got
	// this address while both restarted (see SetAgentIdentityFn).
	if agentIdentityFn != nil {
		if id, boxID := agentIdentityFn(); id != "" || boxID != "" {
			if id != "" {
				out["deviceID"] = id
			}
			if boxID != "" && !strings.EqualFold(boxID, id) {
				out["boxDeviceID"] = boxID
			}
		}
	}
	// A failed tier-3 RAM-staged swap leaves a marker instead of rebooting
	// into a silently-old binary; surface it so the failure is visible on a
	// stickless box where nothing else is.
	if msg, err := os.ReadFile(swapFailMarker); err == nil && len(msg) > 0 {
		out["otaSwapFailed"] = strings.TrimSpace(string(msg))
	}
	writeAgentVersion(w, out)
}

// writeAgentVersion emits the version payload with "version" and "build" as the
// FIRST keys on the wire, whatever else the map carries.
//
// writeJSON marshals the map, and encoding/json SORTS a map's keys, which put
// "version" second to last: every optional flag sorts before it and shoved it
// further right. The desktop app's play-readiness probe reads only the first
// 512 bytes of this response and decides on strings.Contains(body, `"version"`)
// (waitAgentReady in desktop-app/app_playback.go), so a box carrying enough
// flags answered a body whose version key fell outside that window, the probe
// never matched, and every play and preset copy to that speaker was refused as
// "still starting" for as long as the flags were set.
//
// Measured on live hardware 2026-09-25: a healthy speaker puts the key at
// offset 419 to 435, leaving 77 to 93 bytes. An OpenCloudTouch box adds
// conflictingMod (33 bytes) AND foreignCloudURL (66) by construction, since
// both have the same cause, so the box the flag exists to diagnose is exactly
// the one that stopped playing. The app half of this is fixed too, but the
// agent must not depend on the user having a new app.
func writeAgentVersion(w http.ResponseWriter, out map[string]string) {
	first := []string{"version", "build"}
	rest := make([]string, 0, len(out))
	for k := range out {
		if k != "version" && k != "build" {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)

	var b bytes.Buffer
	b.WriteByte('{')
	n := 0
	writePair := func(k string) {
		v, ok := out[k]
		if !ok {
			return
		}
		if n > 0 {
			b.WriteByte(',')
		}
		n++
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(v)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	for _, k := range first {
		writePair(k)
	}
	for _, k := range rest {
		writePair(k)
	}
	b.WriteByte('}')

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b.Bytes())
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
// runningBinaryStamp is the hash of the executable this process was started
// from. Computed once: the file cannot change underneath a running process in
// a way that matters here, and the agent runs on a NAND that is worth sparing.
var runningShaOnce = sync.OnceValue(func() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	sha, err := fileSHA256(path)
	if err != nil {
		return ""
	}
	return sha
})

// fileSHA256 hashes a file by streaming it through a small buffer.
//
// The three binary hashes behind /api/agent/version used to read their file
// whole with os.ReadFile: the agent on NAND, the agent this process runs from
// (usually the same 13 MB file a second time) and, when its marker is stale,
// the 16 MB Spotify engine. The first version poll after every agent start pays
// for them, and the desktop app sends that poll the moment it sees the speaker,
// often on two ports at once. On a speaker with 120 MB of RAM and no swap that
// put the agent at 42 to 44 MB resident within two minutes of every boot,
// measured on five speakers (#1083). Go hands the memory back only over the
// following minutes, and a SoundTouch 20 that was still starting its own
// processes in that window ran out of memory and rebooted, over and over.
//
// Streamed, the hash costs one 32 KB buffer whatever the file size.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 32<<10)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func runningBinaryStamp() string { return runningShaOnce() }

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
	sha, err := fileSHA256(agentBinNANDPath)
	if err != nil {
		return ""
	}
	agentBinShaCache.mtime = fi.ModTime()
	agentBinShaCache.size = fi.Size()
	agentBinShaCache.sha = sha
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
// The note is synced (file and directory): reclaimSpotifyEngine writes it
// just before the engine is removed, and the pair must survive a power loss.
func noteEngineDroppedForUpdate() {
	f, err := os.OpenFile(engineDroppedMarkerPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(time.Now().UTC().Format(time.RFC3339) + "\n")
	_ = f.Sync()
	_ = f.Close()
	if d, derr := os.Open(filepath.Dir(engineDroppedMarkerPath)); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
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
	return goLibrespotStampAt(goLibrespotBinPath)
}

// goLibrespotStampMu serialises the hash: the app probes two ports at once,
// and without it both requests hashed the same 16 MB engine side by side.
// The second one now finds the marker the first one just wrote.
var goLibrespotStampMu sync.Mutex

func goLibrespotStampAt(bin string) (present bool, sha string) {
	fi, err := os.Stat(bin)
	if err != nil || fi.Size() < 1024 {
		return false, ""
	}
	goLibrespotStampMu.Lock()
	defer goLibrespotStampMu.Unlock()
	marker := bin + ".sha256"
	if mfi, err := os.Stat(marker); err == nil && !mfi.ModTime().Before(fi.ModTime()) {
		if b, rerr := os.ReadFile(marker); rerr == nil {
			if h := strings.TrimSpace(string(b)); h != "" {
				return true, h
			}
		}
	}
	h, err := fileSHA256(bin)
	if err != nil {
		return true, "" // present but hash unknown; app re-pushes once
	}
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
// agent. Body must be the raw ARM binary. The receive itself is
// installAgentUpload (agentupload.go), shared with the self-update; this is
// the wire adapter.
//
// On success the stick still returns 200 OK and then exits. The
// rc.local bootstrap starts the new agent.
// updateInFlight is set while an agent update is writing the new binary and
// heading for its restart.
//
// It exists because the update DELETES the Spotify engine when NAND is tight:
// it stops the engine and reclaims its ~16 MB to make room for the new binary.
// An engine delivered into that window is therefore thrown away moments later,
// and the speaker comes back without one. That is not hypothetical, it cost a
// SoundTouch 30 its engine twice in one day, both times because a client
// pushed the engine while the update it had just started was still running.
//
// A client cannot see this from the outside: the old agent keeps answering
// normally right up to the restart, so every check it can make says the
// speaker is ready. So the agent has to say no.
var updateInFlight atomic.Bool

func (s *Server) handleAgentUpdate(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		s.refuseNonLAN(w, r, "agent-update")
		return
	}
	res, ref := s.installAgentUpload(upload{Body: r.Body, Size: r.ContentLength, Remote: r.RemoteAddr})
	if ref != nil {
		writeUploadRefusal(w, ref)
		return
	}
	writeJSON(w, http.StatusOK, res.body())
	go res.afterAnswer()
}

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

// validSoftfloatARMELF checks that body is a 32-bit little-endian softfloat ARM
// ELF, the only shape the speakers can run (#302). The magic alone let anything
// ELF-shaped through: softfloat is pinned at BUILD time (Makefile GOARM=5) but
// nothing validated it at WRITE time, so a mis-built hard-float or wrong-arch
// embed would flash, pass the flash verify, reboot, and then SIGILL on every
// start, crash-looping the agent on a stickless box with no way back. This is
// the last gate before flash. Real GOARM=5 binaries (the agent AND the
// go-librespot engine) are EM_ARM, 32-bit LE, with the hard-float ABI flag
// clear (e_flags 0x05000002), so a correct build always passes. Callers pass a
// body already length-checked (>= 1024), so the fixed header offsets are safe.
func validSoftfloatARMELF(body []byte) (msg string, ok bool) {
	if len(body) < 40 {
		return "binary too small to be an ELF", false
	}
	if body[0] != 0x7f || body[1] != 'E' || body[2] != 'L' || body[3] != 'F' {
		return "not an ELF binary", false
	}
	if body[4] != 1 /*ELFCLASS32*/ || body[5] != 1 /*ELFDATA2LSB*/ {
		return "not a 32-bit little-endian ARM binary", false
	}
	if eMachine := int(body[18]) | int(body[19])<<8; eMachine != 40 /*EM_ARM*/ {
		return "wrong architecture; STR speakers need an ARM (GOARM=5) build", false
	}
	eFlags := uint32(body[36]) | uint32(body[37])<<8 | uint32(body[38])<<16 | uint32(body[39])<<24
	if eFlags&0x400 != 0 /*EF_ARM_ABI_FLOAT_HARD*/ {
		return "hard-float ARM binary refused; STR speakers need a softfloat (GOARM=5) build", false
	}
	return "", true
}

// uploadProgressReader logs a large upload's progress every few MB so the
// box-side agent log shows exactly how far a doomed transfer got before its
// connection died. Wraps the size-limited body reader of both upload
// endpoints (handleAgentUpdate and streamUploadedELF).
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

// errInsufficientNAND is returned by the NAND writes when an actually
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

// nandWriteState carries what the reclaim preamble learned into the error
// classification, so a failure can still say whether the engine was dropped
// and whether statfs had predicted the shortage.
//
// enginePending is set only by the deferred variant (prepareBinaryWriteDeferred):
// statfs predicted a shortage, but the engine is still on the NAND and is
// dropped only once the incoming body has actually filled the volume to the
// margin (engineReclaimGate).
type nandWriteState struct {
	engineStopped bool
	engineReclaim string
	predictedFull bool
	enginePending bool
}

// prepareBinaryWrite is everything a binary write does BEFORE the bytes
// move: the parent directory, the stale temp, and the reclaim cascade that
// makes room. Shared with the streaming variant so the two cannot drift on the
// space handling, which is the part that took the longest to get right.
func prepareBinaryWrite(dst string, need int64) (tmp, dir string, st nandWriteState, err error) {
	tmp, dir, err = prepareBinaryWriteBase(dst)
	if err != nil {
		return "", "", st, err
	}
	if !nandHasRoom(dir, need) {
		st = reclaimEngineForWrite(dir, need)
	}
	return tmp, dir, st, nil
}

// prepareBinaryWriteDeferred is prepareBinaryWrite for the agent update: when
// statfs predicts a shortage it does NOT drop the Spotify engine up front but
// marks the drop as pending, and the receive loop runs it through
// engineReclaimGate once the body has actually used the free space up to the
// margin.
//
// Dropping the engine before a single byte had arrived is what left a speaker
// with its old agent and no Spotify (a Portable on a poor Wi-Fi link,
// 2026-10-25): the upload died twice after 2.3 to 2.5 MB of 18.3 MB, the agent
// correctly kept the old binary, but the first attempt had already deleted the
// engine to make room for bytes that never came. Deferring costs the box
// nothing: the body still streams through the same 32 KB window, so RAM use is
// unchanged, and the engine keeps running during the transfer exactly as it
// does on a roomy speaker. What it buys is that a transfer that dies before
// reaching the margin leaves the engine where it was. A transfer that dies
// AFTER the drop is still possible; the desktop app puts the engine back for
// that case (the goLibrespotDroppedForUpdate marker).
//
// It never withholds the room the update needs: the drop still runs before
// the volume gets closer to full than nandWriteMargin (plus one check window),
// so the write stays off the sub-megabyte cliff that margin exists for, and a
// real ENOSPC while the drop is pending runs it at once and retries the bytes.
func prepareBinaryWriteDeferred(dst string, need int64) (tmp, dir string, st nandWriteState, err error) {
	tmp, dir, err = prepareBinaryWriteBase(dst)
	if err != nil {
		return "", "", st, err
	}
	if !nandHasRoom(dir, need) {
		_, avail, _ := nandAvailFn(dir)
		st.enginePending = true
		slog.Info("OTA write: statfs predicts a shortage; the Spotify engine is dropped only once the body actually needs its room",
			"needKB", need/1024, "availKB", avail/1024, "marginKB", nandWriteMargin/1024)
	}
	return tmp, dir, st, nil
}

// prepareBinaryWriteBase is the preamble both variants share: the parent
// directory, this target's stale temp and the cheap reclaim of regenerable junk.
func prepareBinaryWriteBase(dst string) (tmp, dir string, err error) {
	dir = filepath.Dir(dst)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir parent: %w", err)
	}
	tmp = dst + ".new"
	// Drop this target's stale temp, then proactively reclaim regenerable junk on
	// EVERY OTA write (not only when already tight), so a previous failed
	// attempt's leftovers, a stale OTHER-binary .new, or oversized logs never eat
	// the headroom this write needs (#ST30 Daniel). reclaimNAND never touches Bose
	// files or the live binaries.
	_ = os.Remove(tmp)
	reclaimNAND()
	return tmp, dir, nil
}

// engineReclaimCheckEvery is how often (in bytes written) the deferred reclaim
// asks statfs whether the volume has reached the margin. One statfs per 256 KB
// is about 70 syscalls for an 18 MB agent, nothing next to the write itself,
// and the volume can never get more than one window closer to full than the
// margin before the drop runs.
const engineReclaimCheckEvery = 256 * 1024

// nandAvailFn is diskFree behind a seam, so the deferred reclaim can be driven
// by a scripted volume in tests.
var nandAvailFn = func(dir string) (total, avail int64, ok bool) { return diskFree(dir) }

// engineReclaimGate drives the pending engine drop of a deferred write. The
// receive loop calls due before every write; it asks statfs at most once per
// engineReclaimCheckEvery bytes. The first call always asks, so a volume that
// is already at the margin drops the engine before the first byte, exactly as
// the up-front reclaim did.
type engineReclaimGate struct {
	dir        string
	need       int64
	sinceCheck int64
	asked      bool
}

// due reports whether the pending drop must run before the next write of
// next bytes.
func (g *engineReclaimGate) due(st *nandWriteState, next int) bool {
	if !st.enginePending {
		return false
	}
	g.sinceCheck += int64(next)
	if g.asked && g.sinceCheck < engineReclaimCheckEvery {
		return false
	}
	g.asked = true
	g.sinceCheck = 0
	_, avail, ok := nandAvailFn(g.dir)
	if !ok {
		// No free figure: nandHasRoom would have predicted room, so there is
		// nothing to steer by. Keep the engine; a real ENOSPC still drops it.
		return false
	}
	return avail < nandWriteMargin+int64(next)
}

// run drops the engine now. written is what has reached the temp file so far;
// forced marks a drop the filesystem itself asked for (ENOSPC while pending).
func (g *engineReclaimGate) run(st *nandWriteState, written int64, forced bool) {
	if !st.enginePending {
		return
	}
	slog.Info("OTA write: the incoming body reached the NAND margin, dropping the Spotify engine now",
		"writtenKB", written/1024, "needKB", g.need/1024, "forcedByENOSPC", forced)
	remaining := g.need - written
	if remaining < 0 {
		remaining = 0
	}
	*st = reclaimEngineForWrite(g.dir, remaining)
}

// reclaimEngineForWrite is the second reclaim tier: stop and drop the Spotify
// engine so a write that statfs says cannot fit gets the room. need is what is
// still to be written.
func reclaimEngineForWrite(dir string, need int64) nandWriteState {
	// Second-tier reclaim: the go-librespot Spotify engine (~16 MB) is the one
	// big regenerable block left on a nearly-full NAND (ST30, ~31 MB), and the
	// cheap reclaim never touches it. Drop it so the agent .new fits rather
	// than failing the whole update (#119); the desktop app re-delivers it
	// after the reboot (EnsureSpotifyEngine, triggered by goLibrespot !=
	// "present"), and after an update that did not complete when the speaker
	// reports the engine dropped for it. Only runs when statfs predicts a
	// shortage, so a roomy box keeps its engine.
	//
	// Stop the engine FIRST: it is normally running during an OTA, and a plain
	// os.Remove of a running binary only unlinks the path while the kernel keeps
	// its blocks pinned until the process exits, so dropping it freed nothing and
	// the update still failed with "no space left" (#119). StopEngine kills it and
	// waits for exit so the ~16 MB actually frees; reclaimSpotifyEngine then drops
	// the (now-unused) binary.
	engineStopped := false
	if engineStopHook != nil {
		engineStopped = engineStopHook()
	}
	engineReclaim := reclaimSpotifyEngine()
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
	return nandWriteState{engineStopped: engineStopped, engineReclaim: engineReclaim, predictedFull: !fits}
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
//
// It STREAMS the re-read through the hasher instead of loading the file.
// os.ReadFile allocated a second full copy of the binary while the upload copy
// was still pinned by handleAgentUpdate, so the peak was two whole agent
// binaries plus the page cache we had just dropped. At 14.6 MB per copy on a
// box with 122 MB of RAM and 19-31 MB actually available after an upload, that
// is the cliff three of ten speakers went over during Juergen's 2026-08-22
// batch update: one ST20 had its agent killed here outright (its log stops
// between "upload body received" at memAvailableKB=19228 and the
// "flash-verified" line that never came, and run-override.sh respawned it 103 s
// later already running the NEW binary, so the write itself had landed), and
// two ST10s stalled inside this step long enough to blow the app's 180 s
// response-header timeout and never came back at all. A fixed 512 KB window
// takes the verify's own cost from 14.6 MB to half a megabyte.
//
// wantSum is the hex SHA256 taken while the upload streamed in (#1083: the
// bytes themselves are no longer held in memory to hash afterwards).
func verifyBinaryOnFlash(dst, wantSum string) error {
	_ = exec.Command("sync").Run()
	// Best-effort: /proc/sys/vm/drop_caches needs root (the agent is root on
	// the box) but does not exist in tests.
	_ = os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0o200)
	f, err := os.Open(dst)
	if err != nil {
		return fmt.Errorf("flash verify re-read: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.CopyBuffer(h, f, make([]byte, 512*1024))
	if err != nil {
		return fmt.Errorf("flash verify re-read: %w", err)
	}
	if have := hex.EncodeToString(h.Sum(nil)); have != wantSum {
		return fmt.Errorf("flash verify: on-flash binary (%d bytes) differs from the upload, the NAND write did not persist", n)
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
	return fmt.Errorf("%w: the write was actually attempted and the filesystem refused it (%s: %w): need %dKB, statfs free %dKB after reclaim (%s; engine stop=%v; statfs predicted full=%v) [NAND %s]",
		errInsufficientNAND, step, err, need/1024, avail/1024, engineReclaim, engineStopped, predictedFull, nandReportLine())
}

// nandWriteHTTPStatus maps a NAND write error to an HTTP status: 507
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
	if !sameAgentAuthority(raw) {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

// sameAgentAuthority reports whether a URL points at THIS agent: one of its own
// ports, or a loopback host. Factored out of selfProxySlot so every "is this our
// own stream?" test uses the same rule rather than a copy of it.
func sameAgentAuthority(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host, port := u.Hostname(), u.Port()
	return port == "8888" || port == "17008" ||
		host == "127.0.0.1" || host == "localhost" || host == "::1"
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
//
// #698 added OpenCloudTouch (SoundTouch Hybrid). If both tools somehow left
// artifacts, AfterTouch wins the single return value; the desktop app shows
// the string verbatim, so no i18n is involved.
// groupKeyErrorWindow is how recent a refused press must be to still be
// reported to the app. A press is a human action and the owner is standing
// there; an hour later it is history, not news.
const groupKeyErrorWindow = time.Hour

// heldRefusalWindow matches it: a long press is a human action and the owner
// is standing at the speaker. An hour later it is history, not news.
const heldRefusalWindow = time.Hour

func detectConflictingMod() string {
	if detectAfterTouch() {
		return "AfterTouch"
	}
	if detectOpenCloudTouch() {
		return "OpenCloudTouch"
	}
	return ""
}

func detectAfterTouch() bool {
	if fi, err := os.Stat(filepath.Join(nandRoot, "aftertouch")); err == nil && fi.IsDir() {
		return true
	}
	if _, err := os.Stat(filepath.Join(nandRoot, "aftertouch.resolv.conf")); err == nil {
		return true
	}
	// STR's own rc.local never references the rival tool, so a hook line in
	// the boot script is an unambiguous fingerprint.
	if b, err := os.ReadFile(filepath.Join(nandRoot, "rc.local")); err == nil &&
		strings.Contains(strings.ToLower(string(b)), "aftertouch") {
		return true
	}
	return false
}

// hostsLivePath / hostsOriginalPath are vars so the OpenCloudTouch detection
// test can point them at a temp tree; in production they are the real paths.
// hostsOriginalPath is run.sh's verbatim boot copy of the persistent
// /etc/hosts, taken BEFORE the OCT block is stripped from the live copy, so it
// is where the leftover stays visible for diagnostics. The mod's SDK-config
// backup is matched by octBackupGlob in sdkcloudurls.go.
var (
	hostsLivePath     = "/etc/hosts"
	hostsOriginalPath = "/tmp/hosts.original"
)

// detectOpenCloudTouch reports OpenCloudTouch (SoundTouch Hybrid) leftovers
// (#698). The mod's footprint on the reporter's migrated ST10 was its
// SDK-config backup on NAND and a "# OCT-START".."# OCT-END" redirect block
// in the persistent /etc/hosts, which kept BoseApp and STSCertified in
// SYN_SENT against the mod's long-dead LAN server. Only that one box has
// been measured; other OCT installs may differ.
//
// The hosts block counts here only while it is LIVE, i.e. still present in
// the bind-mounted /etc/hosts the resolver actually reads. run.sh strips it
// at boot and hosts.Apply filters it at agent start, so on a healthy fixed
// box the live file is clean and only the removable backup file keeps the
// warning up. Keying on the verbatim boot copy instead would pin the banner
// forever: the persistent block sits on the read-only rootfs and removing it
// would need a rw remount, firmware bending, off the table by standing rule.
// The boot copy is still surfaced in /api/debug/state for bundles.
//
// #986: the SDK-config backup is matched by PATTERN, not by one literal name.
// The literal used until now (/mnt/nv/OverrideSdkPrivateCfg.xml.oct-backup)
// was copied from a suggestion in #698 and never seen on a box; the measured
// ST30 carries /mnt/nv/SoundTouchSdkPrivateCfg.xml.oct-backup, so detection
// missed it, the cleanup button deleted nothing, and the reporter was told
// "removed" while his speaker went on asking the mod's dead server.
func detectOpenCloudTouch() bool {
	if len(octBackupFiles()) > 0 {
		return true
	}
	if b, err := os.ReadFile(hostsLivePath); err == nil && hosts.ContainsOCTBlock(b) {
		return true
	}
	return false
}

// handleRemoveConflictingMod removes the leftovers of a rival cloud-free
// SoundTouch tool that clash with STR: AfterTouch's /mnt/nv/aftertouch
// directory, its resolv.conf override, and any aftertouch hook line in
// rc.local, plus OpenCloudTouch's SDK-config backup on NAND (#698). It
// touches ONLY those artifacts, never the rest of /mnt/nv (which holds the
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
	// A foreign cloud URL is reason enough to run, even with every marker file
	// already gone: it is the part that actually keeps the speaker away from
	// STR, it outlives the files, and on #986's ST30 it outlived a factory
	// reset as well.
	if mod == "" && foreignCloudURL() == "" && foreignCloudURLFiles() == "" {
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
	// 4. OpenCloudTouch. The cloud URL comes FIRST and the files go second,
	// because the mod's SDK-config backup is the box's pristine pre-mod config
	// and therefore the best template for the healed override: deleting it
	// first would throw away the restore source. Until #986 this handler did
	// exactly that and nothing else, so a box whose margeServerUrl pointed at
	// the mod's dead server kept pointing there after a "successful" cleanup.
	heal := healSDKCloudURLs()
	if heal.Healed {
		removed = append(removed, "cloud URL -> "+stockCloudURLs[sdkMargeTag])
		s.logger.Info("conflicting-mod cleanup: healed the SDK cloud URLs back to stock",
			"wrote", heal.Path, "from", heal.From, "tags", heal.Tags, "issue", "#986")
	} else if heal.Note != "" {
		s.logger.Warn("conflicting-mod cleanup: could not heal the SDK cloud URLs",
			"note", heal.Note, "issue", "#986")
	}
	// The inert leftovers, removed only once the cloud URL is actually stock.
	// The mod's OTHER fingerprint, the redirect block in the persistent
	// /etc/hosts, is NOT removable from here: that file sits on the read-only
	// rootfs and cleaning it would mean remounting the rootfs rw, which is
	// firmware bending and off the table by standing rule (the reporter's own
	// workaround did exactly that; do not "improve" this handler that way).
	// The block is neutralized instead: run.sh strips it from the live hosts
	// copy at boot and hosts.Apply filters it at agent start, and
	// detectOpenCloudTouch only counts the LIVE file, so removing these files
	// is enough for stillDetected to converge to false on a healthy box.
	if foreignCloudURLFiles() == "" {
		for _, p := range octBackupFiles() {
			if err := os.Remove(p); err != nil {
				http.Error(w, "could not remove OCT backup: "+err.Error(), http.StatusInternalServerError)
				return
			}
			removed = append(removed, filepath.Base(p))
		}
		// Corroborated by the SDK backup or the live block above, never a
		// trigger on its own; see octHostsBackupPath.
		if _, err := os.Stat(octHostsBackupPath); err == nil {
			if err := os.Remove(octHostsBackupPath); err == nil {
				removed = append(removed, filepath.Base(octHostsBackupPath))
			}
		}
	}
	_ = exec.Command("sync").Run()
	// Two views, deliberately. stillForeign is what the FILES say, i.e. what
	// this handler could still do something about. The firmware's own live view
	// can lag it by one restart, so it must not read as a failed repair.
	stillForeign := foreignCloudURLFiles()
	liveForeign := liveForeignCloudURL()
	s.logger.Info("removed conflicting-mod leftovers", "mod", mod, "removed", removed,
		"cloudURLHealed", heal.Healed, "filesForeign", stillForeign, "liveForeign", liveForeign)
	out := map[string]any{
		"status":  "ok",
		"mod":     mod,
		"removed": removed,
		// stillDetected stays true while anything the box acts on is left, the
		// foreign cloud URL included: the app shows this verbatim, and #986
		// proved how much a cheerful "removed (0)" costs when it is wrong.
		//
		// The LIVE view belongs in it, not just the files. A rival app can set
		// the address at runtime and leave the config untouched, which is what
		// the ST Remote Pro iOS app did to a five-speaker fleet on 2026-09-26:
		// files pristine, firmware naming stremotepro.com, nothing paired. With
		// only the file view here, this handler reported everything cleaned up
		// on a speaker that was still hijacked, which is exactly the #986
		// mistake in a new shape. A restart cures it and rebootRequired below
		// already says so.
		"stillDetected":  detectConflictingMod() != "" || stillForeign != "" || liveForeign != "",
		"cloudURLHealed": heal.Healed,
	}
	if heal.Healed || heal.RestartPending {
		// The firmware reads the SDK config once, at boot.
		out["rebootRequired"] = true
	}
	if heal.Healed {
		out["cloudURLFile"] = heal.Path
	}
	if heal.RestartPending {
		out["cloudURLRestartPending"] = true
	}
	if stillForeign != "" {
		out["foreignCloudURL"] = stillForeign
	}
	if liveForeign != "" {
		out["liveCloudURL"] = liveForeign
	}
	if heal.Note != "" {
		out["cloudURLNote"] = heal.Note
	}
	writeJSON(w, http.StatusOK, out)
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
//
// The Portable (taigan-bco) keeps its own profile: the BCO coprocessor's NVRAM
// (it survived even a full `sys factorydefault`, 2026-05-24) and the slot-0
// PersistentWifiProfile in AirplayConfiguration.xml. With such a profile there
// the warning was a false alarm: a second-hand Portable joined to the home
// network through Bose's own setup page (2026-10-13) showed it, and so did the
// test Portable for months, while both reconnected after every restart and
// two firmware flashes. The scm bridge boxes keep it: their cold-boot
// association failures are what the offline rescue re-pushes STR's copy for.
func wlanCredsWarningWarranted() bool {
	// Credentials STR set aside because the owner moved the speaker to a
	// network of their own (wlansupersede.go) are not a speaker about to
	// strand: its Wi-Fi is in the owner's hands and the chip's own store.
	if hasSavedWLANCreds() || wlanCredsRetired() {
		return false
	}
	mode, err := os.ReadFile(filepath.Join(strNANDDir, "wlan-mode"))
	if err != nil {
		return false
	}
	return wlanCredsWarningFor(strings.TrimSpace(string(mode)), firmwareWifiProfileSSID)
}

// wlanCredsWarningFor is the decision for a speaker without STR's own copy,
// by its wlan-mode. firmwareSSID is only asked on a Portable.
func wlanCredsWarningFor(mode string, firmwareSSID func() string) bool {
	switch mode {
	case "bco":
		return true
	case "taigan-bco":
		return firmwareSSID() == ""
	}
	return false
}

// firmwareWifiProfileSSID is the SSID of the Wi-Fi profile the firmware keeps
// itself (NetworkProfiles.xml, else AirplayConfiguration.xml), "" when it
// keeps none.
func firmwareWifiProfileSSID() string {
	if m, _ := filepath.Glob("/mnt/nv/BoseApp-Persistence/*/NetworkProfiles.xml"); len(m) > 0 {
		if b, err := os.ReadFile(m[0]); err == nil {
			if v := ssidAfter(string(b), "<profile"); v != "" {
				return v
			}
		}
	}
	if m, _ := filepath.Glob("/mnt/nv/BoseApp-Persistence/*/AirplayConfiguration.xml"); len(m) > 0 {
		if b, err := os.ReadFile(m[0]); err == nil {
			return ssidAfter(string(b), "PersistentWifiProfile")
		}
	}
	return ""
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
	capBoseLogs(filepath.Join(nandRoot, "BoseLog"))
}

const (
	// boseLogCapAt is the size a single file in Bose's log directory may reach
	// before it is trimmed, and boseLogKeep is what survives. Generous on
	// purpose: this is somebody else's log and the point is to stop it eating
	// the volume, not to keep it tidy.
	boseLogCapAt = 1 << 20 // 1 MiB
	boseLogKeep  = 1 << 18 // 256 KiB
)

// capBoseLogs bounds the speaker's OWN log directory.
//
// /mnt/nv/BoseLog belongs to the Bose firmware, not to STR, and STR neither
// writes nor reads it. It does share the same ~32 MB volume, and it can run
// away: a diagnostic from 2026-08-23 showed BoseLog at tens of megabytes on a
// speaker whose whole volume is 31.58 MiB, and that speaker had no room left
// for the Spotify engine while four identical ones next to it had an empty
// BoseLog. Those are logs for a cloud that was switched off in February.
//
// Trimmed the same way STR's own logs are, by keeping the tail instead of
// deleting the file. The inode survives, so a Bose process holding it open goes
// on appending, and nothing has to be restarted. Only files past the cap are
// touched, so an ordinary log directory is left exactly as it is, and only
// regular files directly inside it: no recursion, no directories removed,
// nothing else of Bose's ever touched.
func capBoseLogs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, ferr := e.Info()
		if ferr != nil || !fi.Mode().IsRegular() || fi.Size() <= boseLogCapAt {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, rerr := os.ReadFile(p)
		if rerr != nil || int64(len(b)) <= boseLogKeep {
			continue
		}
		if werr := os.WriteFile(p, b[int64(len(b))-boseLogKeep:], fi.Mode().Perm()); werr == nil {
			slog.Info("nand: trimmed an oversized Bose log so it stops filling the speaker's storage",
				"file", e.Name(), "wasBytes", fi.Size(), "keptBytes", boseLogKeep)
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
	// The marker goes down, synced, BEFORE the engine goes. The desktop puts a
	// dropped engine back only on the marker's word, so a power loss right
	// after the remove must not keep the deletion and lose the marker. A
	// marker that turns out to be wrong (nothing was removed) is taken back,
	// unless one from an earlier drop was already there.
	had := engineDroppedForUpdate()
	noteEngineDroppedForUpdate()
	err := os.Remove(goLibrespotBinPath)
	if err != nil && !had {
		clearEngineDroppedMarker()
	}
	switch {
	case err == nil:
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

// handleAgentSidecar receives the go-librespot Spotify sidecar binary: the
// wire adapter of installEngineUpload (agentupload.go), which the self-update
// shares.
func (s *Server) handleAgentSidecar(w http.ResponseWriter, r *http.Request) {
	// Asked before the method and the LAN gate, as it always was: a client
	// that is about to lose its delivery must hear why, whoever it is. The
	// service asks again, which costs an atomic load.
	if ref := s.engineBlockedByUpdate(); ref != nil {
		writeUploadRefusal(w, ref)
		return
	}
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		s.refuseNonLAN(w, r, "sidecar")
		return
	}
	res, ref := s.installEngineUpload(upload{Body: r.Body, Size: r.ContentLength, Remote: r.RemoteAddr})
	if ref != nil {
		writeUploadRefusal(w, ref)
		return
	}
	writeJSON(w, http.StatusOK, res.body())
}

// boostUploadThroughput lifts the WLAN driver out of power-save for the
// duration of a large upload. While the box idles in standby the driver
// throttles an inbound transfer to ~40 KB/s; the identical 16.5 MB engine
// push runs at ~750 KB/s once the radio is awake (timed A/B on the Portable,
// 2026-08-19 - Jens' hypothesis, live-confirmed). That crawl is what made
// field engine deliveries take minutes and trip client timeouts (#597
// family).
//
// This is deliberately a DRIVER-level toggle, not a box wake: `sys power`
// wakes the box into its last source, and a radio station then audibly
// resumes playing (power-on resume, live 2026-08-19) - an engine delivery
// at night must never start music. Toggling wireless power-save is silent,
// touches nothing the user can perceive, and is restored right after the
// transfer (a reboot resets it anyway). Boxes without wlan0 (wired) and
// boxes that are awake already transfer at full speed and are left alone.
// Best-effort: without iwconfig/iw in the firmware the transfer proceeds at
// the throttled rate, exactly as before.
//
// The returned restore func is never nil and safe to defer.
func (s *Server) boostUploadThroughput(endpoint string) (restore func()) {
	restore = func() {}
	// Every branch says what it saw, at Info. Before, both "leave it alone"
	// checks returned without a word, and six uploads from standby on both
	// chassis families (2026-10-25 fleet roll) produced logs that could not say
	// which of the two had stopped the boost.
	iface, how := wirelessIface()
	if iface == "" {
		s.logger.Info("upload: wireless power-save left as is, no Linux wireless interface on this speaker (wired, or a Wi-Fi module the kernel sees as Ethernet)",
			"endpoint", endpoint, "lookup", how)
		return restore
	}
	// Read the source directly rather than through boxSourceNow, so a failed
	// read is logged with its error instead of reading as an empty source.
	// A read is all this is: nothing here may wake the speaker, because
	// power-on resume would start the music.
	src := ""
	var srcErr error
	if np, err := s.readBoxNowWithin(4 * time.Second); err != nil {
		srcErr = err
	} else {
		src = strings.TrimSpace(np.Source)
	}
	if !strings.EqualFold(src, "STANDBY") {
		if srcErr != nil {
			s.logger.Info("upload: wireless power-save left as is, could not read the speaker's source",
				"endpoint", endpoint, "iface", iface, "lookup", how, "err", srcErr)
		} else {
			s.logger.Info("upload: wireless power-save left as is, the speaker is not in standby",
				"endpoint", endpoint, "iface", iface, "lookup", how, "source", src)
		}
		return restore
	}
	type tool struct {
		name    string
		off, on []string
	}
	tools := []tool{
		{"iwconfig", []string{iface, "power", "off"}, []string{iface, "power", "on"}},
		{"iw", []string{"dev", iface, "set", "power_save", "off"}, []string{"dev", iface, "set", "power_save", "on"}},
	}
	for _, t := range tools {
		if out, err := powerSaveCmd(t.name, t.off...); err == nil {
			s.logger.Info("upload: wireless power-save off for the transfer (idle throttles it ~17x)",
				"endpoint", endpoint, "tool", t.name, "iface", iface, "source", src)
			tt := t
			return func() {
				if out, err := powerSaveCmd(tt.name, tt.on...); err != nil {
					s.logger.Warn("upload: could not re-enable wireless power-save (a reboot resets it)",
						"endpoint", endpoint, "tool", tt.name, "err", err, "out", strings.TrimSpace(string(out)))
					return
				}
				s.logger.Info("upload: wireless power-save restored", "endpoint", endpoint)
			}
		} else {
			s.logger.Info("upload: power-save toggle unavailable", "endpoint", endpoint, "tool", t.name,
				"iface", iface, "err", err, "out", strings.TrimSpace(string(out)))
		}
	}
	s.logger.Info("upload: no wireless power-save toggle in this firmware, transferring at the throttled rate",
		"endpoint", endpoint, "iface", iface, "source", src)
	return restore
}

// powerSaveCmd runs one power-save tool. A seam so the boost's branches can
// be tested without the tools on the host.
var powerSaveCmd = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// procNetWirelessPath and sysClassNetDir are the kernel views wirelessIface
// reads, as variables so tests can point them at fixtures.
var (
	procNetWirelessPath = "/proc/net/wireless"
	sysClassNetDir      = "/sys/class/net"
)

// wirelessIface names the box's wireless interface, or "" when there is none,
// and says how it got there (or why it found nothing) for the boost's log line.
//
// The name is NOT uniform across chassis, so it is never assumed. The kernel's
// wireless-extensions list (/proc/net/wireless) comes first. It only lists
// drivers that implement wireless extensions, though, so a cfg80211 driver
// built without the WEXT compat layer leaves it with nothing but its two
// header lines while wlan0 is up and associated. /sys/class/net is the second
// look: an interface with a "wireless" or "phy80211" entry is a WLAN whatever
// it is called. A Wi-Fi module that does its own networking and is handed to
// Linux as plain Ethernet (the eth0 boxes) has neither, and there is then no
// Linux-side radio whose power-save could be lifted at all.
func wirelessIface() (iface, how string) {
	procNote := ""
	b, err := os.ReadFile(procNetWirelessPath)
	switch {
	case err != nil:
		procNote = "proc unreadable: " + err.Error()
	default:
		if name := parseProcNetWirelessIface(string(b)); name != "" {
			return name, "proc"
		}
		procNote = "proc lists no interface"
	}
	ents, err := os.ReadDir(sysClassNetDir)
	if err != nil {
		return "", procNote + "; sysfs unreadable: " + err.Error()
	}
	// Several entries can carry the markers: a driver that adds a P2P or AP
	// interface (p2p0, ap0, uap0) next to its station interface. Directory
	// order puts p2p0 before wlan0, and lifting power-save on p2p0 "succeeds"
	// while wlan0 stays throttled, so the log would point the diagnosis the
	// wrong way. The station interface is the one that is up, and failing
	// that, the one with a station name; directory order breaks ties.
	best, bestMarker, bestScore := "", "", -1
	var found []string
	for _, e := range ents {
		name := e.Name()
		for _, marker := range []string{"wireless", "phy80211"} {
			if _, serr := os.Stat(filepath.Join(sysClassNetDir, name, marker)); serr != nil {
				continue
			}
			found = append(found, name)
			if sc := wirelessIfaceScore(name); sc > bestScore {
				best, bestMarker, bestScore = name, marker, sc
			}
			break
		}
	}
	if best == "" {
		return "", procNote + "; sysfs has no wireless interface"
	}
	how = procNote + "; sysfs " + bestMarker
	if len(found) > 1 {
		how += " (picked from " + strings.Join(found, ",") + ")"
	}
	return best, how
}

// wirelessIfaceScore ranks a sysfs wireless candidate: operstate "up" counts
// most, a station-style name (wlan*, mlan*) next.
func wirelessIfaceScore(name string) int {
	sc := 0
	if b, err := os.ReadFile(filepath.Join(sysClassNetDir, name, "operstate")); err == nil && strings.TrimSpace(string(b)) == "up" {
		sc += 2
	}
	if strings.HasPrefix(name, "wlan") || strings.HasPrefix(name, "mlan") {
		sc++
	}
	return sc
}

// parseProcNetWirelessIface returns the first interface listed in
// /proc/net/wireless content. The two header lines carry no colon; every data
// line is "<iface>: <status> <link> ...".
func parseProcNetWirelessIface(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if i := strings.IndexByte(line, ':'); i > 0 {
			if name := strings.TrimSpace(line[:i]); name != "" && !strings.ContainsAny(name, " |") {
				return name
			}
		}
	}
	return ""
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
// of it is shared verbatim with receiveAgentBinary, because the space handling
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
		// fsync before close, for the same reason receiveAgentBinary does it: an
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

// refuseNonLAN answers a request the LAN gate turned away, and records WHY.
//
// The refusal used to be silent: http.Error and nothing else. A user whose
// update was refused therefore sent a diagnostic bundle containing no trace of
// it at all, so the only way to work out what happened was to ask him to try
// again and describe it. One reporter went through ten attempts and a mail
// exchange before his Wave updated (2026-08-15), and his bundle could not say
// which address had been turned away or what the speaker considered local.
//
// Both figures are in the answer as well as the log, because the person who
// needs them is usually reading the app's error message rather than a bundle.
func (s *Server) refuseNonLAN(w http.ResponseWriter, r *http.Request, what string) {
	caller := r.RemoteAddr
	if host, _, err := net.SplitHostPort(caller); err == nil {
		caller = host
	}
	local := localSubnetsFn()
	// "I cannot tell" is not "you are not local", and saying the second when
	// the first is true sends the owner looking at his router for a problem
	// that is on the speaker. A speaker that cannot enumerate its own
	// interfaces refuses EVERY caller, including the one standing next to it.
	//
	// It still refuses: this endpoint writes a binary and runs it as root, so
	// the unknown case is not one to wave through. What changes is that it says
	// which of the two happened, and that a restart is the thing to try.
	if len(local) == 0 {
		s.logger.Warn("refused: this speaker cannot read its own network configuration, so it cannot tell whether the caller is local",
			"endpoint", what, "caller", caller)
		http.Error(w, fmt.Sprintf(
			"update refused: this speaker cannot read its own network configuration right now, so it cannot tell whether %s is on its network. Restart the speaker and try again.",
			caller), http.StatusForbidden)
		return
	}
	s.logger.Warn("refused: the caller is not on any network this speaker has an address in",
		"endpoint", what, "caller", caller, "speakerNetworks", strings.Join(local, ","))
	http.Error(w, fmt.Sprintf(
		"update only allowed from LAN: this speaker sees the request coming from %s, which is not on any network it has an address in (%s)",
		caller, strings.Join(local, ", ")), http.StatusForbidden)
}

// localSubnetsFn is localSubnets behind a seam, so the "cannot tell" branch
// can be tested without breaking the machine running the tests.
var localSubnetsFn = localSubnets

// localSubnets lists the networks the speaker has an address in, for the
// refusal message. Best effort: an empty list is itself informative, since a
// speaker whose interfaces cannot be read refuses everything.
func localSubnets() []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
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
			out = append(out, n.String())
		}
	}
	return out
}
