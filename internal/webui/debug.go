// Debug state and probe endpoints plus stick/SSH status helpers.

package webui

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/netutil"
)

// sysBlockRoot, mediaRoot and nvRoot are the sysfs block-device root, the mount
// root, and the writable NAND root. They are vars (not consts) so the
// stick-detection test can point them at a temp tree; in production they are the
// real paths.
var (
	sysBlockRoot = "/sys/block"
	mediaRoot    = "/media"
	nvRoot       = "/mnt/nv"
)

// sshPersistentEnabled reports whether root SSH is configured to stay open across
// reboots on this box, independent of any inserted stick. Two persistent NAND
// markers count: STR's own opt-in (/mnt/nv/streborn/enable-ssh, honored by
// run.sh) and a maintainer-placed /mnt/nv/remote_services. Both live on NAND and
// survive a reboot, so when SSH is open because of one of them the "pull the
// stick and reboot to close it" advice is wrong — the box is deliberately left
// open (#381, #385). Transient stick-driven SSH (the /media/sda1 or /tmp
// remote_services marker) leaves neither file, so it still reads as "still
// inserted".
func sshPersistentEnabled() bool {
	for _, p := range []string{
		filepath.Join(nvRoot, "streborn", "enable-ssh"),
		filepath.Join(nvRoot, "remote_services"),
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// diskIsRemovableUSB reports whether the named block disk (e.g. "sda") is a
// REMOVABLE USB device, i.e. a USB stick, rather than the speaker's built-in
// storage. A disk qualifies when /sys/block/<disk>/removable reads "1", or when
// its sysfs device path sits on the USB bus. This is the fix for #105: several
// speakers (deqw's ST10 + both ST20s, no stick inserted) enumerate an INTERNAL
// disk as sda, so the old "any sd* block device exists" check raised the
// "remove the USB stick" banner permanently with nothing to remove.
func diskIsRemovableUSB(disk string) bool {
	base := filepath.Join(sysBlockRoot, disk)
	if _, err := os.Stat(base); err != nil {
		return false // no such disk
	}
	if b, err := os.ReadFile(filepath.Join(base, "removable")); err == nil &&
		strings.TrimSpace(string(b)) == "1" {
		return true
	}
	// Fallback: a USB stick's /sys/block/<disk> resolves through the USB bus, while
	// internal eMMC/SD/SATA does not. Some sticks report removable=0, so also
	// accept a USB device path.
	if real, err := filepath.EvalSymlinks(base); err == nil && strings.Contains(real, "/usb") {
		return true
	}
	return false
}

// stickReallyMounted reports whether a real STR USB stick is in the speaker right
// now, and returns its version.txt when readable. It requires POSITIVE proof: a
// readable STR marker on a mounted /media/<disk>1 filesystem. A bare removable /
// USB block device is NOT enough.
//
// #179: deqw's ST10 + both ST20s (no stick inserted) expose an internal disk as
// a removable/USB sda that is never mounted (the diagnostic showed no /media/sda1
// and no sd* mount at all), so diskIsRemovableUSB("sda") returned true and the
// old "removable USB present" check kept the "remove the USB stick" banner up
// forever with nothing to remove. Reading an STR marker off the mount instead
// keys on the one thing only a real, inserted STR stick produces.
func stickReallyMounted() (bool, string) {
	mnt := stickMountDir()
	if mnt == "" {
		return false, ""
	}
	// version.txt is the authoritative marker and carries the stick version.
	if b, err := os.ReadFile(filepath.Join(mnt, "version.txt")); err == nil {
		return true, strings.TrimSpace(string(b))
	}
	return true, ""
}

// stickMountDir returns the mount directory of a real, inserted STR stick, or
// "" when none is present. Same positive-proof contract as stickReallyMounted
// (#179): a readable STR marker on a mounted /media/<disk>1, never a bare
// removable/USB block device.
func stickMountDir() string {
	for _, disk := range []string{"sda", "sdb"} {
		if !diskIsRemovableUSB(disk) {
			continue
		}
		mnt := filepath.Join(mediaRoot, disk+"1")
		// Sticks that predate version.txt count via the STR stick layout itself.
		// All markers only exist on a real, inserted stick, so the #179 phantom
		// sda with no mount stays false.
		for _, marker := range []string{"version.txt", "install.sh", "run.sh", "streborn-armv7l"} {
			if _, err := os.Stat(filepath.Join(mnt, marker)); err == nil {
				return mnt
			}
		}
	}
	return ""
}

// handleStickStatus reports whether the USB stick is actually in the box right
// now, plus the stick version when readable. It must NOT use a bare
// os.Stat("/media/sda1")+IsDir: the box leaves the empty mountpoint directory
// behind after `umount` (run.sh cleanup), so IsDir kept reporting mounted:true
// forever after the stick was pulled, which made the "remove the USB stick and
// restart" banner stick around permanently even with the stick already out
// (#105). stickReallyMounted requires real evidence instead.
func (s *Server) handleStickStatus(w http.ResponseWriter, _ *http.Request) {
	mounted, version := stickReallyMounted()
	out := map[string]any{"mounted": mounted}
	if mounted && version != "" {
		out["version"] = version
	}
	// SSH status — check whether port 22 is currently listening. If so
	// someone on the LAN can access the box, the app shows a warning banner.
	// We try a TCP connect to localhost with a 200 ms timeout.
	if conn, dialErr := net.DialTimeout("tcp", "127.0.0.1:22", 200*time.Millisecond); dialErr == nil {
		_ = conn.Close()
		out["sshOpen"] = true
		// Distinguish transient stick-driven SSH (closes on the next stickless
		// reboot) from a persistent NAND opt-in (survives reboots). The app uses
		// this to stop telling remote_services users to "pull the stick and
		// reboot" when no stick is involved and a reboot would not close SSH.
		if sshPersistentEnabled() {
			out["sshPersistent"] = true
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDebugState returns important box state files as JSON so we can
// debug from outside without SSH when the stick is installed.
//
// Used only for interactive diagnosis — the app itself does not call
// this regularly. Limit per file: 8 KB so the response stays compact.
func (s *Server) handleDebugState(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "debug only from LAN", http.StatusForbidden)
		return
	}
	const maxRead = 8 * 1024
	readTail := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			return "ERR: " + err.Error()
		}
		if len(b) > maxRead {
			return "...(truncated)\n" + string(b[len(b)-maxRead:])
		}
		return string(b)
	}
	listDir := func(path string) []string {
		entries, err := os.ReadDir(path)
		if err != nil {
			return []string{"ERR: " + err.Error()}
		}
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			fi, _ := e.Info()
			size := int64(0)
			if fi != nil {
				size = fi.Size()
			}
			out = append(out, fmt.Sprintf("%s  %d  %s", e.Type().String(), size, e.Name()))
		}
		return out
	}

	// The NAND-mirrored agent log is the only log that survives a reboot on a
	// box without SSH and without a previous.log (taigan writes none). Every
	// mid-upload reboot investigation (#466) needs exactly the minutes BEFORE
	// the current boot, so give this one a larger tail than the 8 KB default.
	readTailN := func(path string, max int) string {
		b, err := os.ReadFile(path)
		if err != nil {
			return "ERR: " + err.Error()
		}
		if len(b) > max {
			return "...(truncated)\n" + string(b[len(b)-max:])
		}
		return string(b)
	}

	state := map[string]any{
		"agent_log_tail": readTail("/tmp/streborn-agent.log"),
		"agent_log_nand": readTailN("/mnt/nv/streborn/agent.log", 32*1024),
		"previous_log":   readTail("/mnt/nv/streborn/previous.log"),
		"setup_log":      readTail("/mnt/nv/streborn/setup.log"),
		"boot_log":       readTail("/mnt/nv/streborn/boot.log"),
		// wpaConfPath, not a second literal. This read pointed at
		// /mnt/nv/wpa_supplicant.conf while the code that WRITES the file uses
		// /etc/wpa_supplicant.conf, so every bundle ever collected carried
		// "no such file or directory" here and nobody noticed. The one question
		// it exists to answer, how many networks a speaker is configured for,
		// was therefore never answerable from a bundle (found 2026-08-05 while
		// testing exactly that theory against nine speakers).
		//
		// REDACTED AT THE SOURCE. That file holds the user's Wi-Fi password in
		// psk="...", and this endpoint answers an unauthenticated GET on the
		// LAN and is the body of every diagnostic bundle mailed in or attached
		// to an issue. Fixing the path above turned a read that had always
		// failed into a live disclosure, so the secret is removed here rather
		// than downstream: the desktop app scrubs bundles too, but a value with
		// a space in it (a perfectly ordinary passphrase) survived its pattern,
		// and nothing scrubs a plain curl against this port at all.
		"wpa_supplicant": redactWPASecrets(readTail(wpaConfPath)),
		// What the speaker is REALLY configured for. The file above only shows
		// what was persisted, and on at least one chassis that is the untouched
		// vendor template while the speaker is on Wi-Fi via a network added at
		// runtime. See wlanlist.go.
		"wlan_configured": listConfiguredWLANs(context.Background(), wpaConfPath),
		"region_txt":      readTail("/mnt/nv/streborn/region.txt"),
		"name_txt":        readTail("/mnt/nv/streborn/name.txt"),
		"stick_listing":   listDir("/media/sda1"),
		"media_listing":   listDir("/media"),
		"nv_listing":      listDir("/mnt/nv/streborn"),
		// The /mnt/nv ROOT, not just STR's own subdir: a stock or STR-only box
		// carries only Bose's persistent state and streborn/ here, so anything
		// else (e.g. an aftertouch/ dir) is a leftover from another mod that can
		// clash with STR's Wi-Fi/marge path. Surfacing it lets a bundle spot such
		// remnants without SSH (do NOT blanket-wipe /mnt/nv: it holds the box's
		// own Wi-Fi/AirPlay/account persistence).
		"nv_root_listing": listDir("/mnt/nv"),
		"proc_mounts":     readTail("/proc/mounts"),
		// Writable-volume usage: df for /mnt/nv + / and the per-entry sizes that
		// answer "is this box genuinely tighter or carrying foreign firmware
		// leftovers" without needing SSH (#ST30 OTA no-space, 2026-06-24).
		"disk_usage": nandInventory(),
	}
	// Preset store summary: one compact line per slot so a diagnostic bundle
	// shows dead presets (empty/invalid stream URL) directly. Before this the
	// store's content was invisible in bundles and a preset that saved wrong
	// could only be diagnosed by asking the user to fetch presets.json (#252).
	// Stream URLs are included in full; the app's exporter anonymizes bundles.
	if s.presets != nil {
		all := s.presets.All()
		lines := make([]string, 0, len(all))
		for _, p := range all {
			lines = append(lines, fmt.Sprintf("slot %d: type=%s name=%q codec=%q stream=%q uri=%q items=%d",
				p.Slot, p.Type, p.Name, p.Codec, p.StreamURL, p.URI, len(p.Items)))
		}
		state["presets"] = lines
	}
	// Forensic sections registered by main.go (marge request trail, clock
	// verdict, ...). Called fresh per request; a panicking provider must not
	// take the whole debug endpoint down mid-investigation.
	debugSectionsMu.Lock()
	fns := make(map[string]func() any, len(debugSections))
	for k, fn := range debugSections {
		fns[k] = fn
	}
	debugSectionsMu.Unlock()
	for k, fn := range fns {
		func() {
			defer func() {
				if r := recover(); r != nil {
					state[k] = fmt.Sprintf("ERR: provider panicked: %v", r)
				}
			}()
			state[k] = fn()
		}()
	}
	writeJSON(w, http.StatusOK, state)
}

// handleDebugProbe issues an HTTP request from inside the box to a
// caller-supplied URL and returns the raw response (status, headers,
// body) as JSON. Built as a temporary diagnostic to verify whether
// the BCO wifi-chipset HTTP responder on :80 also answers on the
// loopback interface (it is documented as not having a Linux socket,
// but the chipset may intercept lo traffic too). LAN-only, 5 s
// timeout, body capped to keep the JSON small.
//
// Query parameters:
//
//	url     full URL to probe (required)
//	method  HTTP method (default GET)
//	body    request body, sent verbatim
func (s *Server) handleDebugProbe(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "debug only from LAN", http.StatusForbidden)
		return
	}
	target := r.URL.Query().Get("url")
	if target == "" {
		http.Error(w, "missing url query parameter", http.StatusBadRequest)
		return
	}
	// Scheme gate: this is a deliberate diagnostic that DOES probe the box's own
	// loopback services (Bose :8090, STR :8888), so we do NOT use the SSRF dial
	// guard here, but we still reject non-http(s) schemes (file://, gopher://,
	// ...) so the LAN-gated debug endpoint cannot be turned into a local-file or
	// arbitrary-protocol reader.
	if err := netutil.SafeHTTPURL(target); err != nil {
		http.Error(w, "invalid url: "+err.Error(), http.StatusBadRequest)
		return
	}
	method := r.URL.Query().Get("method")
	if method == "" {
		method = http.MethodGet
	}
	bodyStr := r.URL.Query().Get("body")
	timeoutSec := 5
	if t := r.URL.Query().Get("timeout"); t != "" {
		if v, err := strconv.Atoi(t); err == nil && v >= 1 && v <= 120 {
			timeoutSec = v
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	var reqBody io.Reader
	if bodyStr != "" {
		reqBody = strings.NewReader(bodyStr)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reqBody)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"target": target,
			"phase":  "build-request",
			"error":  err.Error(),
		})
		return
	}
	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"target": target,
			"method": method,
			"phase":  "do-request",
			"error":  err.Error(),
		})
		return
	}
	defer resp.Body.Close()
	const maxBody = 8 * 1024
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	hdrs := map[string]string{}
	for k, v := range resp.Header {
		if len(v) > 0 {
			hdrs[k] = v[0]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"target":      target,
		"method":      method,
		"status_code": resp.StatusCode,
		"status":      resp.Status,
		"headers":     hdrs,
		"body":        string(respBody),
		"body_bytes":  len(respBody),
	})
}
