// Package hosts manipulates /etc/hosts at start and cleans it up again at stop.
// The Bose software is redirected so it communicates with the local binary
// instead of the real Bose servers.
package hosts

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

const (
	defaultPath = "/etc/hosts"
	beginMarker = "# >>> streborn begin >>>"
	endMarker   = "# <<< streborn end <<<"
)

// Entry describes a line that is inserted into /etc/hosts.
type Entry struct {
	IP   string
	Host string
}

// DefaultEntries returns the default redirects.
//
// The TuneIn hostname with the Bose partner hash has been shut down by Bose
// since 2026-05-15 (NXDOMAIN). We redirect it to 127.0.0.1 so our marge stub
// can emulate the TuneIn API and STSCertified BMXTuneInClient believes the
// Bose TuneIn cloud is reachable.
func DefaultEntries() []Entry {
	return []Entry{
		{IP: "127.0.0.1", Host: "streaming.bose.com"},
		{IP: "127.0.0.1", Host: "content.api.bose.io"},
		{IP: "127.0.0.1", Host: "7f5055e9ff15f2a5035a488b81ec10f4.api.radiotime.com"},
		{IP: "0.0.0.0", Host: "events.api.bosecm.com"},
		{IP: "0.0.0.0", Host: "worldwide.bose.com"},
	}
}

// Manager manages a marked block in /etc/hosts.
type Manager struct {
	path   string
	logger *slog.Logger
}

// New creates a Manager. If path is empty, /etc/hosts is used.
func New(path string, logger *slog.Logger) *Manager {
	if path == "" {
		path = defaultPath
	}
	return &Manager{path: path, logger: logger}
}

// Apply inserts the marked block with the entries into /etc/hosts
// and replaces any existing old block.
func (m *Manager) Apply(entries []Entry) error {
	current, err := os.ReadFile(m.path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read hosts: %w", err)
	}

	stripped := removeBlock(current)
	block := renderBlock(entries)

	merged := append(bytes.TrimRight(stripped, "\n"), '\n', '\n')
	merged = append(merged, block...)

	if err := writeAtomic(m.path, merged); err != nil {
		return fmt.Errorf("hosts write: %w", err)
	}
	if m.logger != nil {
		m.logger.Info("hosts block active", "path", m.path, "entries", len(entries))
	}
	return nil
}

// Restore removes the marked block from /etc/hosts again.
func (m *Manager) Restore() error {
	current, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read hosts: %w", err)
	}
	stripped := removeBlock(current)
	if err := writeAtomic(m.path, stripped); err != nil {
		return fmt.Errorf("write hosts: %w", err)
	}
	if m.logger != nil {
		m.logger.Info("hosts block removed", "path", m.path)
	}
	return nil
}

func renderBlock(entries []Entry) []byte {
	var sb strings.Builder
	sb.WriteString(beginMarker)
	sb.WriteByte('\n')
	for _, e := range entries {
		fmt.Fprintf(&sb, "%s\t%s\n", e.IP, e.Host)
	}
	sb.WriteString(endMarker)
	sb.WriteByte('\n')
	return []byte(sb.String())
}

func removeBlock(in []byte) []byte {
	lines := strings.Split(string(in), "\n")
	out := make([]string, 0, len(lines))
	inBlock := false
	for _, l := range lines {
		switch {
		case strings.TrimSpace(l) == beginMarker:
			inBlock = true
			continue
		case strings.TrimSpace(l) == endMarker:
			inBlock = false
			continue
		case inBlock:
			continue
		default:
			out = append(out, l)
		}
	}
	return []byte(strings.Join(out, "\n"))
}

// writeAtomic writes content to path. It first tries the classic
// "write to .tmp + rename" strategy. If that fails because the directory
// is read only (e.g. /etc on a ubifs rootfs that only has a tmpfs file
// mounted over it), it falls back to an in-place truncate write. That
// way it works on the Bose box where /etc is ro but /etc/hosts is its
// own tmpfs mount.
func writeAtomic(path string, content []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err == nil {
		if err := os.Rename(tmp, path); err == nil {
			return nil
		} else {
			_ = os.Remove(tmp)
			// Rename failed, falls through to in-place
		}
	}
	// In-place Truncate write. Close error is checked explicitly:
	// on a tmpfs-over-ro-rootfs (the actual deploy target on Bose)
	// a silently-swallowed Close error after a partial write would
	// leave /etc/hosts truncated until the next boot.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(content)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
