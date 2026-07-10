// Diagnostic-log export. Bundles the desktop app log file plus
// per-known-box snapshots (Bose /info, STR /api/status, STR
// /api/agent/version, the live multiroom zone) into a single zip
// that the user can attach to a GitHub issue.
//
// All output is anonymized for public sharing by default:
//   - Real LAN IPs in the box list and inside the log are masked to
//     192.0.2.x (the same scheme tools/Diagnose-STR.ps1 uses).
//   - MAC addresses, device IDs, serial numbers, and friendly names
//     in box snapshots are replaced with the first 8 hex chars of
//     their SHA256.
//
// SSIDs and Wi-Fi passwords never leave the host: the export does
// not include /etc/wpa_supplicant.conf, presets.json, or any other
// stick-side state, and the desktop app's slog output is filtered
// for SSID hints before being copied into the zip.

package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/JRpersonal/streborn/sticksetup"
)

// LogExportRequest is the JSON the frontend hands to the export
// method.
type LogExportRequest struct {
	// SavePath is the absolute path the resulting zip should be
	// written to. Picked by the user via the Wails SaveFile dialog.
	SavePath string `json:"savePath"`
	// BoxHosts is the list of box LAN IPs the desktop app currently
	// knows about. The exporter probes each for fresh JSON state.
	BoxHosts []string `json:"boxHosts"`
	// Anonymize masks IPs / hashes IDs / scrubs SSIDs from log
	// before writing. Default true for safe public sharing.
	Anonymize bool `json:"anonymize"`
}

// LogExportResult is the JSON the export method returns.
type LogExportResult struct {
	SavePath string `json:"savePath"`
	Bytes    int64  `json:"bytes"`
}

// ExportDiagnosticLogs collects the app log + per-box state and
// writes a zip to req.SavePath. Returns the path + size for the
// frontend to show in a "saved" toast.
func (a *App) ExportDiagnosticLogs(req LogExportRequest) (LogExportResult, error) {
	if req.SavePath == "" {
		return LogExportResult{}, fmt.Errorf("savePath is required")
	}

	f, err := os.Create(req.SavePath)
	if err != nil {
		return LogExportResult{}, fmt.Errorf("create zip: %w", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)

	// 1. README so the human opening the zip understands what is in it.
	readme := fmt.Sprintf(`STR Reborn diagnostic bundle
============================
Created:     %s
OS:          %s/%s
App version: %s
Anonymized:  %v
Boxes asked: %d

Contents:
  README.txt            this file
  app.log               desktop app log (rolling, up to 2 MB)
  box-<n>.json          per-box snapshot (Bose /info + STR /api/status + /api/agent/version + /api/box/zone)
  stick-<n>/setup.log   FAT32 setup.log if an STR stick is plugged into this PC
  stick-<n>/_meta.json  drive metadata (path, label, free space)
  manifest.json         summary

Privacy:
  When Anonymized=true (default), LAN IPs are masked to 192.0.2.x,
  MAC addresses / device IDs / serial numbers / friendly names are
  hashed (first 8 chars of SHA256), and SSID-looking strings in the
  app log are scrubbed. Even so, please skim the files before
  attaching to a public issue.
`, time.Now().UTC().Format(time.RFC3339), runtime.GOOS, runtime.GOARCH, appVersion, req.Anonymize, len(req.BoxHosts))
	if err := writeZipEntry(zw, "README.txt", []byte(readme)); err != nil {
		return LogExportResult{}, err
	}

	// 2. App log (truncated + sanitized). Flush the live writer
	// first so anything slog buffered for this session lands on
	// disk before we read it back.
	if a.logFile != nil {
		_ = a.logFile.Sync()
	}
	logBytes, _ := os.ReadFile(LogFilePath())
	if req.Anonymize {
		logBytes = sanitizeLog(logBytes)
	}
	if err := writeZipEntry(zw, "app.log", logBytes); err != nil {
		return LogExportResult{}, err
	}

	// 2b. Previous-session app log + the persistent OTA journal. str.log is
	// rotated to <name>.1 on each launch and app.log above is only the current
	// session, so an update-failure exported in a LATER session has lost the
	// attempt. The previous session (often where it happened) is in app.prev.log,
	// and every speaker-update attempt with its outcome is in ota-history.log,
	// which is never rotated away. Both best-effort, omitted when absent.
	if prev, perr := os.ReadFile(LogFilePath() + ".1"); perr == nil && len(prev) > 0 {
		if req.Anonymize {
			prev = sanitizeLog(prev)
		}
		_ = writeZipEntry(zw, "app.prev.log", prev)
	}
	if oj, oerr := os.ReadFile(otaJournalPath()); oerr == nil && len(oj) > 0 {
		if req.Anonymize {
			oj = sanitizeLog(oj)
		}
		_ = writeZipEntry(zw, "ota-history.log", oj)
	}

	// 3. Per-box snapshots.
	manifest := struct {
		Timestamp string          `json:"timestamp"`
		OS        string          `json:"os"`
		Arch      string          `json:"arch"`
		Anonymize bool            `json:"anonymize"`
		Boxes     []boxIndexEntry `json:"boxes"`
	}{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Anonymize: req.Anonymize,
	}

	// Stable ordering so subsequent runs diff cleanly.
	hosts := append([]string{}, req.BoxHosts...)
	sort.Strings(hosts)
	for i, host := range hosts {
		snap := captureBoxSnapshot(host)
		entry := boxIndexEntry{Index: i, Host: host}
		if req.Anonymize {
			entry.Host = maskIP(host)
			snap = anonymizeSnapshot(snap)
		}
		manifest.Boxes = append(manifest.Boxes, entry)
		name := fmt.Sprintf("box-%d.json", i)
		b, _ := json.MarshalIndent(snap, "", "  ")
		if err := writeZipEntry(zw, name, b); err != nil {
			return LogExportResult{}, err
		}
	}
	// 4. Host-side stick pickup. When the user pulls the stick out of
	// the box and plugs it into the PC running the desktop app, the
	// stick's FAT32 setup.log holds the full multi-boot trace of the
	// run.sh state machine. That trace is the ONLY useful signal when
	// the box is currently unreachable (Setup-AP, dead WLAN, swapped
	// to a different network) and the SSH fallback in captureBoxSnapshot
	// returns nothing. Without this pickup we kept asking users to
	// open Explorer / Finder and attach files manually. Now they just
	// leave the stick plugged in and the diagnostic ZIP grabs it.
	//
	// We enumerate every removable drive, look for a marker file that
	// proves it is an STR stick (setup.log, version.txt, or run.sh),
	// then bundle each candidate as stick-<n>/<file>. SSID-scrub runs
	// over setup.log when anonymize=true since the credentials JSON
	// the wizard wrote can leak the password otherwise.
	stickIndex := 0
	if drives, derr := sticksetup.ListDrives(); derr == nil {
		for _, d := range drives {
			if !d.Removable || d.Path == "" {
				continue
			}
			markers := []string{"setup.log", "version.txt", "run.sh"}
			isStick := false
			for _, m := range markers {
				if _, err := os.Stat(filepath.Join(d.Path, m)); err == nil {
					isStick = true
					break
				}
			}
			if !isStick {
				continue
			}
			// 1 MB cap per file — setup.log can grow with cumulative boots
			// (a stick visiting many boxes can run into many MB). The tail
			// is what matters anyway; cap from the end via stat + offset.
			pull := []string{"setup.log", "version.txt", "wlan-mode", "boot.log"}
			for _, name := range pull {
				src := filepath.Join(d.Path, name)
				info, ierr := os.Stat(src)
				if ierr != nil {
					continue
				}
				body, rerr := readTail(src, info.Size(), 1024*1024)
				if rerr != nil {
					continue
				}
				if req.Anonymize && (name == "setup.log" || name == "boot.log") {
					body = sanitizeLog(body)
				}
				zipName := fmt.Sprintf("stick-%d/%s", stickIndex, name)
				if werr := writeZipEntry(zw, zipName, body); werr != nil {
					return LogExportResult{}, werr
				}
			}
			// Drive metadata for the manifest so the receiver knows
			// which physical stick the files came from.
			driveLabel := d.Label
			if req.Anonymize {
				driveLabel = ""
			}
			meta := map[string]any{
				"drivePath":  d.Path,
				"driveLabel": driveLabel,
				"filesystem": d.Filesystem,
				"totalBytes": d.TotalBytes,
				"freeBytes":  d.FreeBytes,
			}
			mbBytes, _ := json.MarshalIndent(meta, "", "  ")
			if err := writeZipEntry(zw, fmt.Sprintf("stick-%d/_meta.json", stickIndex), mbBytes); err != nil {
				return LogExportResult{}, err
			}
			stickIndex++
		}
	} else {
		a.logger.Warn("log export: ListDrives failed, no host-side stick pickup", "err", derr)
	}

	mb, _ := json.MarshalIndent(manifest, "", "  ")
	if err := writeZipEntry(zw, "manifest.json", mb); err != nil {
		return LogExportResult{}, err
	}
	if err := zw.Close(); err != nil {
		return LogExportResult{}, err
	}

	st, _ := f.Stat()
	a.logger.Info("log export written", "path", req.SavePath, "bytes", st.Size(), "boxes", len(hosts), "sticksFromHost", stickIndex)
	return LogExportResult{SavePath: req.SavePath, Bytes: st.Size()}, nil
}

// readTail returns the last cap bytes of the file at path. When the
// file is smaller than cap, returns the whole file. Used so a stick
// that has visited many boxes does not blow the zip up with
// historical boot traces nobody is going to read.
func readTail(path string, size, cap int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if size > cap {
		if _, err := f.Seek(size-cap, 0); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(f)
}

// sshFallbackLogCap bounds each SSH-pulled box log to its last
// sshFallbackLogCap bytes. The on-box `tail -c` already trims most files, but
// this client-side cap is the durable guard: it keeps the bundle sane even if a
// field is added without a `tail -c` limit or a BusyBox `tail` ignores -c. We
// keep the TAIL because the most recent lines (listener bring-up, the failure
// itself) matter most. 64 KB is 8x the HTTP /api/debug/state per-file cap
// (internal/webui handleDebugState, maxRead = 8*1024): the SSH path exists for
// the "agent never bound 8888" case where a little more early-boot history
// helps, but 64 KB is two orders of magnitude under the previous 5 MB that
// ballooned one bundle to 609 KB.
const sshFallbackLogCap = 64 * 1024

// tailString returns the last cap bytes of s, prefixed with a truncation
// marker, matching the HTTP path's readTail closure.
func tailString(s string, cap int) string {
	if len(s) <= cap {
		return s
	}
	return "...(truncated to last " + strconv.Itoa(cap) + " bytes)\n" + s[len(s)-cap:]
}

type boxIndexEntry struct {
	Index int    `json:"index"`
	Host  string `json:"host"`
}

type boxSnapshot struct {
	Host        string         `json:"host"`
	BoseInfo    string         `json:"boseInfoXml"`
	STRStatus   string         `json:"strStatusJson"`
	STRAgentVer map[string]any `json:"strAgentVersion"`
	// STRZone is the box's live multiroom zone (GET /api/box/zone via the
	// agent), best-effort and empty when the agent is down or predates the
	// zone API. Group bugs (#70) were previously undiagnosable from a bundle
	// because nothing recorded who was master/member at export time.
	STRZone       string `json:"strZoneJson,omitempty"`
	ReachableSSH  bool   `json:"reachableSSH"`
	Reachable8090 bool   `json:"reachable8090"`
	Reachable8888 bool   `json:"reachable8888"`
	// Reachable8091 is the box's UPnP/DLNA media-renderer port, served by a
	// separate firmware process from the Bose REST API (:8090) and the STR
	// agent. A box that answers here but nowhere else is ON the network with a
	// WEDGED control stack (the Portable "renderer up, control crashed" state),
	// not off the network. Without this probe the bundle reported such a box as
	// fully unreachable, which read as "box is off / off Wi-Fi" and sent the
	// user down the wrong recovery path (Andi M., 06.07.2026).
	Reachable8091 bool `json:"reachable8091"`
	// STRDetected is the authoritative "STR is actually installed and serving
	// here" signal: true only when /api/agent/version returned a real version
	// string. Reachable8888 alone is misleading because it probes :17008, where
	// a STOCK scm box's own Bose SoftwareUpdate service also TCP-accepts, so
	// reachable8888=true even with no STR present (this is what made the Wave /
	// SA-4 / scm-ST30 look like a "broken STR agent" when they were plain stock).
	STRDetected bool `json:"strDetected"`
	// DebugState is /api/debug/state on 8888, if reachable. Contains
	// the boot-race trace (setup.log, boot.log, agent_log_tail).
	// Always present when the STR agent is up — the single most
	// useful field for diagnosing failed installs that did succeed
	// far enough for the agent to bind 8888 but then misbehave.
	DebugState map[string]any `json:"debugState,omitempty"`
	// SSHFallback holds box-side files pulled over SSH when the STR
	// agent is NOT up (port 8888 closed) but SSH is reachable.
	// Without this the user has zero visibility into why install
	// did not bring up the agent — and that is exactly the failure
	// mode we have been chasing on ST20 reports.
	SSHFallback *sshFallback `json:"sshFallback,omitempty"`
}

// sshFallback bundles the box-side text files we pull when we can
// ssh in but the STR agent never bound port 8888. Each field is a
// best-effort tail; an "ERR: ..." string surfaces if the file was
// not present or unreadable.
type sshFallback struct {
	BootLog      string `json:"bootLog"`
	SetupLog     string `json:"setupLog"`
	PreviousLog  string `json:"previousLog"`
	AgentLogTail string `json:"agentLogTail"`
	// AgentLogNAND is the NAND-persisted /mnt/nv/streborn/agent.log
	// which gets the entire slog output, mirrored from stderr. Unlike
	// AgentLogTail (8 KB tail of /tmp/streborn-agent.log on tmpfs)
	// this survives reboot and we pull a much larger tail so the
	// listener-bring-up phase logs are always in the bundle.
	AgentLogNAND   string `json:"agentLogNand"`
	StickListing   string `json:"stickListing"`
	MediaListing   string `json:"mediaListing"`
	NVListing      string `json:"nvListing"`
	ProcMounts     string `json:"procMounts"`
	UptimeSeconds  string `json:"uptimeSeconds"`
	RunningProcs   string `json:"runningProcs"`
	StickInstallSh string `json:"stickInstallShPresent"`
	// Network interface state pulled because "no wlan, only eth0"
	// turned out to be the actual cause of #60's failing ST20 and
	// only became visible after we asked the user to SSH manually.
	// Including this in the bundle by default means future reports
	// of the same shape arrive already diagnosed.
	IPLinkShow  string   `json:"ipLinkShow"`
	SysClassNet string   `json:"sysClassNet"`
	ProcNetDev  string   `json:"procNetDev"`
	DmesgWlan   string   `json:"dmesgWlan"`
	WlanMode    string   `json:"wlanMode"`
	Probed      []string `json:"probedMountPaths"`
}

func captureBoxSnapshot(host string) boxSnapshot {
	s := boxSnapshot{Host: host}
	s.Reachable8090 = portOpen(host, 8090, 1200)
	// STR's external webui port depends on the chassis: Series-II / sm2 boxes
	// (ST10 rhino, ST30 mojo, Wave lisa) serve STR DIRECTLY on :8888, made
	// LAN-reachable by the stick's iptables INPUT ACCEPT rule with no :17008
	// REDIRECT; BCO/whitelisted chassis (Portable taigan, ST20 spotty) expose
	// STR ONLY on the REDIRECTed :17008 (their own :8888 is loopback-only). Probe
	// BOTH, exactly like discovery's probeSTR, and use whichever answers. A
	// :17008-only probe here falsely reported every healthy sm2 box as
	// reachable8888=false / strDetected=false (Kai's ST10 + Wave bundle,
	// 2026-07-09, both sm2 yet both flagged STR-absent while actually running).
	// Reachable8888 keeps its name for schema continuity but now means "STR's
	// webui reachable on either external port".
	strPort := 0
	if portOpen(host, 8888, 1200) {
		strPort = 8888
	} else if portOpen(host, 17008, 1200) {
		strPort = 17008
	}
	s.Reachable8888 = strPort != 0
	s.ReachableSSH = portOpen(host, 22, 1200)
	// :8091 is the UPnP/DLNA media renderer, a separate firmware process. When
	// :8090 is dead but :8091 answers, the box is online with a wedged control
	// stack rather than off the network - the discriminator that keeps a
	// diagnostic bundle from looking like a dead box (see field comment).
	s.Reachable8091 = portOpen(host, 8091, 1200)
	if s.Reachable8090 {
		s.BoseInfo = httpGetText(fmt.Sprintf("http://%s:8090/info", host), 4096)
	}
	if s.Reachable8888 {
		base := fmt.Sprintf("http://%s:%d", host, strPort)
		s.STRStatus = httpGetText(base+"/api/status", 4096)
		// Live multiroom zone, best-effort (empty on stock boxes, agents
		// without the zone API, or a zone read the box firmware rejects).
		s.STRZone = httpGetText(base+"/api/box/zone", 4096)
		raw := httpGetText(base+"/api/agent/version", 1024)
		if raw != "" {
			_ = json.Unmarshal([]byte(raw), &s.STRAgentVer)
		}
		// Authoritative STR-present check: a real STR agent answers with a
		// non-empty "version". A stock scm box's :17008 SoftwareUpdate replies
		// with something else (or nothing parseable), so STRAgentVer stays empty.
		if v, ok := s.STRAgentVer["version"].(string); ok && strings.TrimSpace(v) != "" {
			s.STRDetected = true
		}
		// /api/debug/state holds the boot-race trace (setup.log,
		// boot.log, agent_log_tail). Single most useful payload for
		// "agent came up but is misbehaving" diagnostics.
		debugRaw := httpGetTextTimeout(base+"/api/debug/state", 256*1024, 20*time.Second)
		if debugRaw != "" {
			var ds map[string]any
			if err := json.Unmarshal([]byte(debugRaw), &ds); err == nil {
				s.DebugState = ds
			}
		}
	}
	// SSH fallback: when 8888 is NOT up but SSH is, pull the box-
	// side logs over ssh. This is the failure mode every recent
	// ST20 report has exhibited — without these tails we are
	// guessing. Best-effort: if ssh is not installed locally or
	// negotiation fails, the field stays nil and the bundle still
	// contains everything else.
	// SSH-fallback gate. We previously only triggered when :17008 was
	// TCP-closed, but a 2026-05-30 v0.5.23 bundle exposed the
	// hole: on a scm/spotty ST20 with STR installed, :17008 stays
	// TCP-open because Bose's own SoftwareUpdate listens there. If
	// the LD_PRELOAD shim has not (yet) hijacked the process, the
	// STR JSON probes return empty bodies — same evidence as "STR
	// API just down" — but the old gate skipped the SSH pull
	// entirely. Net result: zero diagnostic content for the exact
	// scenario we most need to debug. We now also pull SSH-side when
	// the box failed to surface STR JSON at /api/agent/version,
	// which is the authoritative "STR alive externally" signal.
	strAlive := len(s.STRAgentVer) > 0
	if s.ReachableSSH && (!s.Reachable8888 || !strAlive) {
		s.SSHFallback = pullSSHFallback(host)
	}
	return s
}

// sshFallbackMinFieldTimeout floors every per-field SSH timeout in
// pullSSHFallback. The per-field ms values below are sized for the command's
// own runtime, but on a LAN where the box's sshd stalls ~10.5 s on reverse DNS
// (see boxssh.go) a fresh connection alone eats a 3-15 s budget before the
// command starts, and the field came back empty even for /proc/uptime — the
// exact "sshFallback fields empty" signature of the 2026-07-10 diagnostic. The
// client cache means only the first field here pays a handshake, but the export
// runs in the background, so flooring every field at 30 s costs nothing on a
// healthy LAN and lets every field clear one slow handshake on an affected one.
const sshFallbackMinFieldTimeout = 30 * time.Second

// pullSSHFallback fetches the diagnostic tails over SSH using the
// same legacy-algorithm flags install_str.go uses. Total time
// budgeted so a misbehaving box does not stall the whole diagnostic
// export. Each command runs with its own timeout (floored at
// sshFallbackMinFieldTimeout) so a single hang does not consume the budget.
func pullSSHFallback(host string) *sshFallback {
	type field struct {
		name string
		cmd  string
		ms   int
		dest *string
	}
	out := &sshFallback{}
	// Probed path list mirrors install_str.go stickProbePaths plus
	// a free /media + /mnt scan so we always know where the stick
	// landed (or that it never mounted at all).
	out.Probed = []string{"/media/sda1", "/media/sdb1", "/media/sdc1",
		"/media/sdd1", "/mnt/sda1", "/mnt/usb",
		"/run/media/sda1"}
	fields := []field{
		// Each log field is tailed to sshFallbackLogCap (64 KB): enough
		// to cover the listener bring-up / OOB-marker phase that the SSH
		// fallback exists to diagnose, without the multi-hundred-KB
		// bundles the old 5 MB tail produced (one ST20 bundle hit
		// setupLog=277 KB + agentLogNand=264 KB = 609 KB total). The
		// matching client-side tailString cap below is the durable guard
		// if a BusyBox tail ignores -c. Files smaller than the cap
		// transfer in whole. This mirrors the HTTP /api/debug/state 8 KB
		// per-file cap (handleDebugState), scaled up 8x for the
		// failed-install case.
		{"bootLog", "tail -c 65536 /mnt/nv/streborn/boot.log 2>/dev/null", 15000, &out.BootLog},
		{"setupLog", "tail -c 65536 /mnt/nv/streborn/setup.log 2>/dev/null", 30000, &out.SetupLog},
		{"previousLog", "tail -c 65536 /mnt/nv/streborn/previous.log 2>/dev/null", 15000, &out.PreviousLog},
		{"agentLogTail", "tail -c 65536 /tmp/streborn-agent.log 2>/dev/null", 15000, &out.AgentLogTail},
		// NAND-persisted agent log (/mnt/nv/streborn/agent.log, the
		// io.MultiWriter target newLogger opens): same 64 KB tail.
		{"agentLogNand", "tail -c 65536 /mnt/nv/streborn/agent.log 2>/dev/null", 30000, &out.AgentLogNAND},
		{"stickListing", "ls -la /media/sda1 2>&1 | head -50", 5000, &out.StickListing},
		{"mediaListing", "ls -la /media /mnt /run/media 2>&1 | head -80", 5000, &out.MediaListing},
		{"nvListing", "ls -la /mnt/nv/streborn 2>&1 | head -40", 5000, &out.NVListing},
		{"procMounts", "cat /proc/mounts 2>/dev/null | head -40", 5000, &out.ProcMounts},
		{"uptimeSeconds", "awk '{print int($1)}' /proc/uptime 2>/dev/null", 4000, &out.UptimeSeconds},
		{"runningProcs", "ps 2>/dev/null | head -40", 5000, &out.RunningProcs},
		{"stickInstall", "for p in /media/sda1 /media/sdb1 /media/sdc1 /media/sdd1 /mnt/sda1 /mnt/usb /run/media/sda1; do " +
			`if [ -e "$p/install.sh" ]; then echo "INSTALL_SH_AT=$p"; fi; done`,
			5000, &out.StickInstallSh},
		// Network state. ip link show is the canonical "what
		// interfaces does the kernel know about" view; /sys/class/net
		// catches the case where ip is missing on a stripped
		// BusyBox. /proc/net/dev cross-checks that and shows packet
		// counters so we can tell whether eth0 ever saw traffic.
		// dmesg | grep wlan picks up driver load failures from the
		// 2014-era SMSC chip on older ST20s that booted without a
		// radio in #60.
		{"ipLinkShow", "ip link show 2>&1 | head -40", 4000, &out.IPLinkShow},
		{"sysClassNet", "ls /sys/class/net 2>&1", 3000, &out.SysClassNet},
		{"procNetDev", "cat /proc/net/dev 2>/dev/null | head -20", 3000, &out.ProcNetDev},
		{"dmesgWlan", "dmesg 2>/dev/null | grep -iE 'wlan|wifi|smsc|wireless|brcm|mt76|cfg80211|mac80211' | tail -30", 5000, &out.DmesgWlan},
		{"wlanMode", "cat /mnt/nv/streborn/wlan-mode 2>/dev/null", 3000, &out.WlanMode},
	}
	// Fields whose body is a log tail get a hard client-side cap; the rest
	// (directory/process/network listings) are already bounded by `| head -N`
	// on the box.
	logFields := map[string]bool{
		"bootLog": true, "setupLog": true, "previousLog": true,
		"agentLogTail": true, "agentLogNand": true,
	}
	for _, f := range fields {
		timeout := time.Duration(f.ms) * time.Millisecond
		if timeout < sshFallbackMinFieldTimeout {
			timeout = sshFallbackMinFieldTimeout
		}
		txt, _ := boxSSHOutput(host, f.cmd, timeout)
		txt = strings.TrimSpace(txt)
		if logFields[f.name] {
			txt = tailString(txt, sshFallbackLogCap)
		}
		*f.dest = txt
	}
	return out
}

// === Sanitization ===

var ipv4Regex = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
var macRegex = regexp.MustCompile(`(?i)\b([0-9A-F]{2}[:-]){5}[0-9A-F]{2}\b`)
var deviceIDRegex = regexp.MustCompile(`(?i)\b[0-9A-F]{12}\b`)
var ssidHintRegex = regexp.MustCompile(`(?i)(ssid|ssid_name|wpa-psk\s+\S+|psk=)[^\s]*`)

// nameTagRegex catches the speaker's user-chosen friendly name as it appears in
// gabbo frame bodies captured in the box debug state / agent log
// (<nameUpdated>Living Room</nameUpdated>) and in any <name>...</name> a box
// log echoes. A friendly name is a personal identifier (CLAUDE.md), so it must
// be hashed even though it is free-form text with no fixed value shape.
var nameTagRegex = regexp.MustCompile(`<(name|nameUpdated)>([^<]+)</(?:name|nameUpdated)>`)

// friendlyNameJSONRegex catches the friendly name as a JSON value, e.g. the
// /api/agent/version payload ("friendlyName":"Bose Wit") and any status JSON
// that carries it. Keyed on the field name so radio/preset display names (also
// "name") are not over-scrubbed.
var friendlyNameJSONRegex = regexp.MustCompile(`(?i)("friendlyName"\s*:\s*")([^"]*)(")`)

// scrubPII is the single sanitization pass shared by every text blob that can
// leave the host (the app log, box-side logs pulled over SSH, the /api/debug
// state, /api/status). Keeping one function means a field added to the bundle
// cannot accidentally skip a scrub the other paths already do — the exact hole
// that leaked real device IDs and friendly names through anonymizeText while
// sanitizeLog scrubbed them (see #187/#197 diagnostic bundles).
func scrubPII(s string) string {
	s = ipv4Regex.ReplaceAllStringFunc(s, func(ip string) string { return maskIP(ip) })
	s = macRegex.ReplaceAllStringFunc(s, func(m string) string { return "MAC#" + hashShort(m) })
	s = deviceIDRegex.ReplaceAllStringFunc(s, func(m string) string { return "DEV#" + hashShort(m) })
	s = nameTagRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := nameTagRegex.FindStringSubmatch(m)
		return "<" + sub[1] + ">NAME#" + hashShort(sub[2]) + "</" + sub[1] + ">"
	})
	s = friendlyNameJSONRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := friendlyNameJSONRegex.FindStringSubmatch(m)
		return sub[1] + "NAME#" + hashShort(sub[2]) + sub[3]
	})
	s = ssidHintRegex.ReplaceAllString(s, "<SSID-REDACTED>")
	return s
}

func sanitizeLog(b []byte) []byte {
	return []byte(scrubPII(string(b)))
}

func anonymizeSnapshot(s boxSnapshot) boxSnapshot {
	s.Host = maskIP(s.Host)
	s.BoseInfo = anonymizeBoseInfoXML(s.BoseInfo)
	s.STRStatus = anonymizeText(s.STRStatus)
	// The zone JSON carries member IPs and device IDs.
	s.STRZone = anonymizeText(s.STRZone)
	// /api/agent/version carries the user-chosen friendlyName ("Bose Wit"), which
	// was previously copied into the bundle verbatim. Round-trip the parsed map
	// through scrubPII so friendlyName (and any device ID it grows) is hashed like
	// everything else. Model/version/build are left intact.
	if len(s.STRAgentVer) > 0 {
		if b, err := json.Marshal(s.STRAgentVer); err == nil {
			var m map[string]any
			if json.Unmarshal([]byte(scrubPII(string(b))), &m) == nil {
				s.STRAgentVer = m
			}
		}
	}
	if s.DebugState != nil {
		s.DebugState = anonymizeDebugState(s.DebugState)
	}
	if s.SSHFallback != nil {
		s.SSHFallback.BootLog = anonymizeText(s.SSHFallback.BootLog)
		s.SSHFallback.SetupLog = anonymizeText(s.SSHFallback.SetupLog)
		s.SSHFallback.PreviousLog = anonymizeText(s.SSHFallback.PreviousLog)
		s.SSHFallback.AgentLogTail = anonymizeText(s.SSHFallback.AgentLogTail)
		s.SSHFallback.AgentLogNAND = anonymizeText(s.SSHFallback.AgentLogNAND)
		s.SSHFallback.StickListing = anonymizeText(s.SSHFallback.StickListing)
		s.SSHFallback.MediaListing = anonymizeText(s.SSHFallback.MediaListing)
		s.SSHFallback.NVListing = anonymizeText(s.SSHFallback.NVListing)
		s.SSHFallback.ProcMounts = anonymizeText(s.SSHFallback.ProcMounts)
		s.SSHFallback.RunningProcs = anonymizeText(s.SSHFallback.RunningProcs)
		s.SSHFallback.StickInstallSh = anonymizeText(s.SSHFallback.StickInstallSh)
		s.SSHFallback.IPLinkShow = anonymizeText(s.SSHFallback.IPLinkShow)
		s.SSHFallback.SysClassNet = anonymizeText(s.SSHFallback.SysClassNet)
		s.SSHFallback.ProcNetDev = anonymizeText(s.SSHFallback.ProcNetDev)
		s.SSHFallback.DmesgWlan = anonymizeText(s.SSHFallback.DmesgWlan)
		s.SSHFallback.WlanMode = anonymizeText(s.SSHFallback.WlanMode)
	}
	return s
}

// anonymizeText scrubs IPs, MACs, device IDs, friendly names, and SSID hints
// from a text blob using the same shared pass (scrubPII) as the app log. Used
// for box-side logs we pull over SSH and the /api/debug state so the user can
// safely attach them to a public GitHub issue.
func anonymizeText(s string) string {
	return scrubPII(s)
}

// anonymizeDebugState walks the /api/debug/state map and scrubs
// every string value. Nested maps and slices are walked
// recursively. Non-string leaves are untouched (booleans, numbers).
func anonymizeDebugState(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = anonymizeAny(v)
	}
	return out
}

func anonymizeAny(v any) any {
	switch t := v.(type) {
	case string:
		return anonymizeText(t)
	case []any:
		for i, item := range t {
			t[i] = anonymizeAny(item)
		}
		return t
	case map[string]any:
		return anonymizeDebugState(t)
	default:
		return v
	}
}

func anonymizeBoseInfoXML(xml string) string {
	if xml == "" {
		return ""
	}
	// Bose /info has deviceID="...", <macAddress>...</macAddress>,
	// <serialNumber>...</serialNumber>, <name>...</name>,
	// <margeAccountUUID>...</margeAccountUUID>, <ipAddress>...
	out := xml
	out = regexp.MustCompile(`deviceID="([^"]+)"`).ReplaceAllStringFunc(out, func(m string) string {
		v := strings.TrimSuffix(strings.TrimPrefix(m, `deviceID="`), `"`)
		return `deviceID="DEV#` + hashShort(v) + `"`
	})
	out = regexp.MustCompile(`<macAddress>([^<]+)</macAddress>`).ReplaceAllStringFunc(out, func(m string) string {
		v := strings.TrimSuffix(strings.TrimPrefix(m, `<macAddress>`), `</macAddress>`)
		return `<macAddress>MAC#` + hashShort(v) + `</macAddress>`
	})
	out = regexp.MustCompile(`<serialNumber>([^<]+)</serialNumber>`).ReplaceAllStringFunc(out, func(m string) string {
		v := strings.TrimSuffix(strings.TrimPrefix(m, `<serialNumber>`), `</serialNumber>`)
		return `<serialNumber>SERIAL#` + hashShort(v) + `</serialNumber>`
	})
	out = regexp.MustCompile(`<name>([^<]+)</name>`).ReplaceAllStringFunc(out, func(m string) string {
		v := strings.TrimSuffix(strings.TrimPrefix(m, `<name>`), `</name>`)
		return `<name>NAME#` + hashShort(v) + `</name>`
	})
	out = regexp.MustCompile(`<margeAccountUUID>([^<]+)</margeAccountUUID>`).ReplaceAllStringFunc(out, func(m string) string {
		v := strings.TrimSuffix(strings.TrimPrefix(m, `<margeAccountUUID>`), `</margeAccountUUID>`)
		return `<margeAccountUUID>MARGE#` + hashShort(v) + `</margeAccountUUID>`
	})
	out = ipv4Regex.ReplaceAllStringFunc(out, func(ip string) string { return maskIP(ip) })
	return out
}

func maskIP(ip string) string {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return ip
	}
	// Keep last octet so the same host stays recognisable across
	// references but the network identity is hidden.
	return "192.0.2." + parts[3]
}

func hashShort(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}

// === Small HTTP / port helpers ===

func portOpen(host string, port int, timeoutMs int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port),
		time.Duration(timeoutMs)*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func httpGetText(url string, max int64) string {
	return httpGetTextTimeout(url, max, 4*time.Second)
}

// httpGetTextTimeout is httpGetText with an explicit timeout. The 4 s default is
// right for the fast agent probes (/api/status, /api/agent/version), but the
// single most useful diagnostic payload - /api/debug/state - reads several log
// tails plus a NAND disk-usage walk, which can take much longer on a pegged box.
// That is exactly the box a trace is needed for: a live #342 bundle from a
// misbehaving scm ST20 came back with an EMPTY debugState (so we could not see
// what fired) because the box was busy in a re-select loop and the 4 s probe
// timed out, even though the fast /api/status probe on the same port succeeded.
// The debug fetch therefore gets a much longer budget so the on-box log makes it
// into the bundle even when the box is struggling.
func httpGetTextTimeout(url string, max int64, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	cli := &http.Client{Timeout: timeout}
	resp, err := cli.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, max))
	return string(b)
}

func writeZipEntry(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// SaveDiagnosticBundle is the one-call frontend entry point: pops
// the OS save-file dialog with a sensible default filename, then
// writes the zip there. Returns the resulting path or empty string
// if the user cancelled. Anonymize defaults to true.
func (a *App) SaveDiagnosticBundle(boxHosts []string, anonymize bool) (LogExportResult, error) {
	defaultName := fmt.Sprintf("str-diagnostic-%s.zip", time.Now().UTC().Format("20060102-150405"))
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		DefaultFilename: defaultName,
		Title:           "Save STR diagnostic bundle",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Zip archive (*.zip)", Pattern: "*.zip"},
		},
	})
	if err != nil {
		return LogExportResult{}, err
	}
	if path == "" {
		return LogExportResult{}, nil // user cancelled
	}
	return a.ExportDiagnosticLogs(LogExportRequest{
		SavePath:  path,
		BoxHosts:  boxHosts,
		Anonymize: anonymize,
	})
}

// GetLogFilePath returns the path of the live app log so the
// frontend can show it in an "open log folder" link.
func (a *App) GetLogFilePath() string {
	return LogFilePath()
}
