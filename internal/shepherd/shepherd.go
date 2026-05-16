// Package shepherd integriert den STR Agent in den Bose
// shepherdd Watchdog auf der SoundTouch Box.
//
// Auf der Box gibt es das Init Skript /etc/init.d/SoundTouch das shepherdd
// startet. Es liest Config Files aus /var/run/shepherd. Wenn statt dessen
// /mnt/nv/shepherd als Verzeichnis existiert, wird dieser Pfad genutzt und
// die Config Files dort werden uebernommen (Override Modus).
//
// Dieses Paket pflegt /mnt/nv/shepherd: legt die Standard Symlinks an
// (core, noncore, product, rhino, hsp) und schreibt unsere eigene
// Shepherd-streborn.xml. Damit wird unser Agent von shepherdd
// supervisiert und automatisch neu gestartet wenn er crasht.
//
// Phase 1 (rc.local direct start) und Phase 2 (shepherdd Integration)
// duerfen NICHT gleichzeitig aktiv sein. install.sh und ggf. dieses Paket
// muessen das beim Aktivieren sicherstellen.
package shepherd

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// DefaultShepherdDir ist der Override Pfad, den /etc/init.d/SoundTouch erkennt.
const DefaultShepherdDir = "/mnt/nv/shepherd"

// DefaultBoseConfigDir ist das Verzeichnis der Bose Standard Configs.
const DefaultBoseConfigDir = "/opt/Bose/etc"

// DefaultStickBin ist der vermutete Pfad zum Agent Binary auf dem USB Stick.
const DefaultStickBin = "/media/sda1/streborn-armv7l"

// DefaultPresetsPath ist der vermutete Pfad zur presets.json.
const DefaultPresetsPath = "/media/sda1/presets.json"

// StandardSymlinks sind die fuenf Bose Configs die wir per Symlink referenzieren
// muessen damit shepherdd alle Standard Daemons findet. Reihenfolge identisch
// zum Original Init Skript.
var StandardSymlinks = []string{
	"Shepherd-core.xml",
	"Shepherd-noncore.xml",
	"Shepherd-product.xml",
	"Shepherd-rhino.xml",
	"Shepherd-hsp.xml",
}

// OwnConfigName ist der Dateiname unserer eigenen Shepherd Config.
const OwnConfigName = "Shepherd-streborn.xml"

// Config beschreibt wie unsere Shepherd Integration aufgesetzt wird.
type Config struct {
	// ShepherdDir, default DefaultShepherdDir.
	ShepherdDir string
	// BoseConfigDir, default DefaultBoseConfigDir.
	BoseConfigDir string
	// AgentBinary, default DefaultStickBin.
	AgentBinary string
	// PresetsPath, default DefaultPresetsPath.
	PresetsPath string
	// AgentArgs sind die kompletten CLI Argumente die der Agent bekommt.
	// Wenn nil, wird DefaultAgentArgs verwendet.
	AgentArgs []string
}

// DefaultAgentArgs sind die Flags die wir dem Agent mitgeben.
// Identisch zu run.sh damit Phase 1 und Phase 2 austauschbar sind.
func DefaultAgentArgs(presetsPath string) []string {
	return []string{
		"--presets", presetsPath,
		"--listen-webui", ":8888",
		"--listen-marge", ":8080",
		"--listen-bmx", ":8081",
		"--hosts", "/etc/hosts",
		"--apply-hosts=true",
		"--log-level", "info",
	}
}

// Manager kapselt die Operationen Setup, Status, Teardown.
type Manager struct {
	cfg    Config
	logger *slog.Logger
}

// New erstellt einen Manager mit Default Pfaden wenn die Config Felder leer sind.
func New(cfg Config, logger *slog.Logger) *Manager {
	if cfg.ShepherdDir == "" {
		cfg.ShepherdDir = DefaultShepherdDir
	}
	if cfg.BoseConfigDir == "" {
		cfg.BoseConfigDir = DefaultBoseConfigDir
	}
	if cfg.AgentBinary == "" {
		cfg.AgentBinary = DefaultStickBin
	}
	if cfg.PresetsPath == "" {
		cfg.PresetsPath = DefaultPresetsPath
	}
	if cfg.AgentArgs == nil {
		cfg.AgentArgs = DefaultAgentArgs(cfg.PresetsPath)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{cfg: cfg, logger: logger}
}

// Status beschreibt den aktuellen Zustand der Shepherd Integration.
type Status struct {
	DirExists       bool
	MissingSymlinks []string
	HasOwnConfig    bool
	BrokenSymlinks  []string
}

// IsHealthy gibt true zurueck wenn die Integration komplett und konsistent ist.
func (s Status) IsHealthy() bool {
	return s.DirExists && len(s.MissingSymlinks) == 0 && s.HasOwnConfig && len(s.BrokenSymlinks) == 0
}

// Check liest den aktuellen Zustand von /mnt/nv/shepherd ohne ihn zu veraendern.
func (m *Manager) Check() (Status, error) {
	var st Status

	fi, err := os.Stat(m.cfg.ShepherdDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			st.MissingSymlinks = append([]string{}, StandardSymlinks...)
			return st, nil
		}
		return st, fmt.Errorf("stat %s: %w", m.cfg.ShepherdDir, err)
	}
	if !fi.IsDir() {
		return st, fmt.Errorf("%s ist keine Directory", m.cfg.ShepherdDir)
	}
	st.DirExists = true

	for _, name := range StandardSymlinks {
		path := filepath.Join(m.cfg.ShepherdDir, name)
		fi, err := os.Lstat(path)
		if err != nil {
			st.MissingSymlinks = append(st.MissingSymlinks, name)
			continue
		}
		// Pruefen ob es ein Symlink ist
		if fi.Mode()&os.ModeSymlink == 0 {
			// Existiert aber kein Symlink, akzeptieren wir auch (Bose koennte
			// das selbst eingerichtet haben). Aber wir loggen es als hint.
			m.logger.Debug("Eintrag ist kein Symlink", "name", name)
			continue
		}
		// Target lesen und pruefen ob es existiert
		target, err := os.Readlink(path)
		if err != nil {
			st.BrokenSymlinks = append(st.BrokenSymlinks, name)
			continue
		}
		// Wenn target relativ, gegen ShepherdDir aufloesen
		if !filepath.IsAbs(target) {
			target = filepath.Join(m.cfg.ShepherdDir, target)
		}
		if _, err := os.Stat(target); err != nil {
			st.BrokenSymlinks = append(st.BrokenSymlinks, name)
		}
	}

	ownPath := filepath.Join(m.cfg.ShepherdDir, OwnConfigName)
	if _, err := os.Stat(ownPath); err == nil {
		st.HasOwnConfig = true
	}

	return st, nil
}

// Install richtet /mnt/nv/shepherd ein. Idempotent: wenn alles schon passt,
// wird nichts angefasst.
func (m *Manager) Install() error {
	if err := os.MkdirAll(m.cfg.ShepherdDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", m.cfg.ShepherdDir, err)
	}

	// Standard Symlinks anlegen
	for _, name := range StandardSymlinks {
		src := filepath.Join(m.cfg.BoseConfigDir, name)
		dst := filepath.Join(m.cfg.ShepherdDir, name)

		if _, err := os.Stat(src); err != nil {
			// Wenn die Bose Config fehlt, Symlink ueberspringen aber nicht
			// abbrechen. shepherdd ignoriert fehlende Files.
			m.logger.Warn("Bose Config fehlt, Symlink uebersprungen",
				"src", src, "err", err)
			continue
		}

		// Existiert das Ziel schon und zeigt auf src? Dann nichts tun.
		if existing, err := os.Readlink(dst); err == nil && existing == src {
			m.logger.Debug("Symlink bereits korrekt", "dst", dst)
			continue
		}

		// Falls dst existiert (Symlink oder Datei), erst entfernen
		_ = os.Remove(dst)

		if err := os.Symlink(src, dst); err != nil {
			return fmt.Errorf("symlink %s -> %s: %w", dst, src, err)
		}
		m.logger.Info("Symlink erstellt", "dst", dst, "src", src)
	}

	// Eigene Config schreiben
	xml := RenderConfig(m.cfg.AgentBinary, m.cfg.AgentArgs)
	ownPath := filepath.Join(m.cfg.ShepherdDir, OwnConfigName)
	tmpPath := ownPath + ".new"
	if err := os.WriteFile(tmpPath, []byte(xml), 0o644); err != nil {
		return fmt.Errorf("schreibe %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, ownPath); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, ownPath, err)
	}
	m.logger.Info("Eigene Config geschrieben", "path", ownPath)

	return nil
}

// Uninstall entfernt /mnt/nv/shepherd komplett. Beim naechsten Reboot faellt
// das Init Skript auf den Default Pfad /var/run/shepherd zurueck, unser
// Agent startet dann nicht mehr automatisch.
func (m *Manager) Uninstall() error {
	if err := os.RemoveAll(m.cfg.ShepherdDir); err != nil {
		return fmt.Errorf("entferne %s: %w", m.cfg.ShepherdDir, err)
	}
	m.logger.Info("Shepherd Integration entfernt", "dir", m.cfg.ShepherdDir)
	return nil
}

// RenderConfig produziert die XML Repraesentation unserer Shepherd Config.
// Wir bauen es als String weil das XML simpel und uebersichtlich ist.
func RenderConfig(binary string, args []string) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	sb.WriteString("<ShepherdConfig>\n")
	fmt.Fprintf(&sb, "  <daemon name=\"streborn\" exe=%q>\n", binary)
	for _, a := range args {
		fmt.Fprintf(&sb, "    <arg>%s</arg>\n", escapeXML(a))
	}
	sb.WriteString("  </daemon>\n")
	sb.WriteString("</ShepherdConfig>\n")
	return sb.String()
}

// escapeXML escaped die fuenf Standard XML Entitaeten.
func escapeXML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}
