// Package hosts manipuliert /etc/hosts beim Start und räumt beim Stop wieder auf.
// Die Bose Software wird so umgelenkt, dass sie mit dem lokalen Binary
// statt mit den echten Bose Servern kommuniziert.
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

// Entry beschreibt eine Zeile, die in /etc/hosts eingefügt wird.
type Entry struct {
	IP   string
	Host string
}

// DefaultEntries liefert die Standard Umleitungen.
//
// Der TuneIn Hostname mit dem Bose Partner Hash ist seit 15.05.2026 von Bose
// abgeschaltet (NXDOMAIN). Wir leiten ihn auf 127.0.0.1 um damit unser
// Marge Stub die TuneIn API emulieren kann und STSCertified BMXTuneInClient
// glaubt die Bose TuneIn Cloud waere erreichbar.
func DefaultEntries() []Entry {
	return []Entry{
		{IP: "127.0.0.1", Host: "streaming.bose.com"},
		{IP: "127.0.0.1", Host: "content.api.bose.io"},
		{IP: "127.0.0.1", Host: "7f5055e9ff15f2a5035a488b81ec10f4.api.radiotime.com"},
		{IP: "0.0.0.0", Host: "events.api.bosecm.com"},
		{IP: "0.0.0.0", Host: "worldwide.bose.com"},
	}
}

// Manager verwaltet einen markierten Block in /etc/hosts.
type Manager struct {
	path   string
	logger *slog.Logger
}

// New erstellt einen Manager. Wenn path leer ist, wird /etc/hosts verwendet.
func New(path string, logger *slog.Logger) *Manager {
	if path == "" {
		path = defaultPath
	}
	return &Manager{path: path, logger: logger}
}

// Apply fügt den markierten Block mit den Entries in /etc/hosts ein
// und ersetzt einen eventuell vorhandenen alten Block.
func (m *Manager) Apply(entries []Entry) error {
	current, err := os.ReadFile(m.path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("hosts lesen: %w", err)
	}

	stripped := removeBlock(current)
	block := renderBlock(entries)

	merged := append(bytes.TrimRight(stripped, "\n"), '\n', '\n')
	merged = append(merged, block...)

	if err := writeAtomic(m.path, merged); err != nil {
		return fmt.Errorf("hosts schreiben: %w", err)
	}
	if m.logger != nil {
		m.logger.Info("hosts Block aktiv", "path", m.path, "entries", len(entries))
	}
	return nil
}

// Restore entfernt den markierten Block wieder aus /etc/hosts.
func (m *Manager) Restore() error {
	current, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("hosts lesen: %w", err)
	}
	stripped := removeBlock(current)
	if err := writeAtomic(m.path, stripped); err != nil {
		return fmt.Errorf("hosts schreiben: %w", err)
	}
	if m.logger != nil {
		m.logger.Info("hosts Block entfernt", "path", m.path)
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

// writeAtomic schreibt content nach path. Versucht erst die klassische
// "write to .tmp + rename" Strategie. Wenn das fehlschlaegt weil das
// Verzeichnis read only ist (z.B. /etc auf einem ubifs Rootfs der nur eine
// tmpfs Datei drueber gemountet hat), faellt es auf in-place Truncate write
// zurueck. Damit klappt es auf der Bose Box wo /etc ro ist aber /etc/hosts
// ein eigener tmpfs Mount ist.
func writeAtomic(path string, content []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err == nil {
		if err := os.Rename(tmp, path); err == nil {
			return nil
		} else {
			_ = os.Remove(tmp)
			// Rename gescheitert, faellt durch zu in-place
		}
	}
	// In-place Truncate write
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(content)
	return err
}
