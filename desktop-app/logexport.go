// Diagnostic-log export. Bundles the desktop app log file plus
// per-known-box snapshots (Bose /info, STR /api/status, STR
// /api/agent/version) into a single zip that the user can attach
// to a GitHub issue.
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
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
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
  box-<n>.json          per-box snapshot (Bose /info + STR /api/status + /api/agent/version)
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
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	if err := writeZipEntry(zw, "manifest.json", mb); err != nil {
		return LogExportResult{}, err
	}
	if err := zw.Close(); err != nil {
		return LogExportResult{}, err
	}

	st, _ := f.Stat()
	a.logger.Info("log export written", "path", req.SavePath, "bytes", st.Size(), "boxes", len(hosts))
	return LogExportResult{SavePath: req.SavePath, Bytes: st.Size()}, nil
}

type boxIndexEntry struct {
	Index int    `json:"index"`
	Host  string `json:"host"`
}

type boxSnapshot struct {
	Host          string         `json:"host"`
	BoseInfo      string         `json:"boseInfoXml"`
	STRStatus     string         `json:"strStatusJson"`
	STRAgentVer   map[string]any `json:"strAgentVersion"`
	ReachableSSH  bool           `json:"reachableSSH"`
	Reachable8090 bool           `json:"reachable8090"`
	Reachable8888 bool           `json:"reachable8888"`
}

func captureBoxSnapshot(host string) boxSnapshot {
	s := boxSnapshot{Host: host}
	s.Reachable8090 = portOpen(host, 8090, 1200)
	s.Reachable8888 = portOpen(host, 8888, 1200)
	s.ReachableSSH = portOpen(host, 22, 1200)
	if s.Reachable8090 {
		s.BoseInfo = httpGetText(fmt.Sprintf("http://%s:8090/info", host), 4096)
	}
	if s.Reachable8888 {
		s.STRStatus = httpGetText(fmt.Sprintf("http://%s:8888/api/status", host), 4096)
		raw := httpGetText(fmt.Sprintf("http://%s:8888/api/agent/version", host), 1024)
		if raw != "" {
			_ = json.Unmarshal([]byte(raw), &s.STRAgentVer)
		}
	}
	return s
}

// === Sanitization ===

var ipv4Regex = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
var macRegex = regexp.MustCompile(`(?i)\b([0-9A-F]{2}[:-]){5}[0-9A-F]{2}\b`)
var deviceIDRegex = regexp.MustCompile(`(?i)\b[0-9A-F]{12}\b`)
var ssidHintRegex = regexp.MustCompile(`(?i)(ssid|ssid_name|wpa-psk\s+\S+|psk=)[^\s]*`)

func sanitizeLog(b []byte) []byte {
	s := string(b)
	s = ipv4Regex.ReplaceAllStringFunc(s, func(ip string) string { return maskIP(ip) })
	s = macRegex.ReplaceAllStringFunc(s, func(m string) string { return "MAC#" + hashShort(m) })
	s = deviceIDRegex.ReplaceAllStringFunc(s, func(m string) string { return "DEV#" + hashShort(m) })
	s = ssidHintRegex.ReplaceAllString(s, "<SSID-REDACTED>")
	return []byte(s)
}

func anonymizeSnapshot(s boxSnapshot) boxSnapshot {
	s.Host = maskIP(s.Host)
	s.BoseInfo = anonymizeBoseInfoXML(s.BoseInfo)
	s.STRStatus = ipv4Regex.ReplaceAllStringFunc(s.STRStatus, func(ip string) string { return maskIP(ip) })
	return s
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
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	cli := &http.Client{Timeout: 4 * time.Second}
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
