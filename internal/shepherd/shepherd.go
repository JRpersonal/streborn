// Package shepherd integrates the STR agent into the Bose
// shepherdd watchdog on the SoundTouch box.
//
// On the box there is the init script /etc/init.d/SoundTouch that starts
// shepherdd. It reads config files from /var/run/shepherd. If instead
// /mnt/nv/shepherd exists as a directory, this path is used and
// the config files there are picked up (override mode).
//
// This package maintains /mnt/nv/shepherd: it creates the standard
// symlinks (core, noncore, product, rhino, hsp) and writes our own
// Shepherd-streborn.xml. This way our agent is supervised by shepherdd
// and automatically restarted if it crashes.
//
// Phase 1 (rc.local direct start) and Phase 2 (shepherdd integration)
// must NOT be active at the same time. install.sh and possibly this
// package must ensure that when activating.
package shepherd

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// DefaultShepherdDir is the override path that /etc/init.d/SoundTouch recognizes.
const DefaultShepherdDir = "/mnt/nv/shepherd"

// DefaultBoseConfigDir is the directory of the Bose standard configs.
const DefaultBoseConfigDir = "/opt/Bose/etc"

// DefaultStickBin is the presumed path to the agent binary on the USB stick.
const DefaultStickBin = "/media/sda1/streborn-armv7l"

// DefaultPresetsPath is the presumed path to presets.json.
const DefaultPresetsPath = "/media/sda1/presets.json"

// StandardSymlinks are the five Bose configs that we must reference via
// symlink so shepherdd finds all standard daemons. Order identical to
// the original init script.
var StandardSymlinks = []string{
	"Shepherd-core.xml",
	"Shepherd-noncore.xml",
	"Shepherd-product.xml",
	"Shepherd-rhino.xml",
	"Shepherd-hsp.xml",
}

// OwnConfigName is the file name of our own shepherd config.
const OwnConfigName = "Shepherd-streborn.xml"

// Config describes how our shepherd integration is set up.
type Config struct {
	// ShepherdDir, default DefaultShepherdDir.
	ShepherdDir string
	// BoseConfigDir, default DefaultBoseConfigDir.
	BoseConfigDir string
	// AgentBinary, default DefaultStickBin.
	AgentBinary string
	// PresetsPath, default DefaultPresetsPath.
	PresetsPath string
	// AgentArgs are the complete CLI arguments the agent receives.
	// If nil, DefaultAgentArgs is used.
	AgentArgs []string
}

// DefaultAgentArgs are the flags we pass to the agent.
// Identical to run.sh so phase 1 and phase 2 are interchangeable.
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

// Manager encapsulates the operations setup, status, teardown.
type Manager struct {
	cfg    Config
	logger *slog.Logger
}

// New creates a Manager with default paths if the config fields are empty.
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

// Status describes the current state of the shepherd integration.
type Status struct {
	DirExists       bool
	MissingSymlinks []string
	HasOwnConfig    bool
	BrokenSymlinks  []string
}

// IsHealthy returns true if the integration is complete and consistent.
func (s Status) IsHealthy() bool {
	return s.DirExists && len(s.MissingSymlinks) == 0 && s.HasOwnConfig && len(s.BrokenSymlinks) == 0
}

// Check reads the current state of /mnt/nv/shepherd without changing it.
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
		return st, fmt.Errorf("%s is not a directory", m.cfg.ShepherdDir)
	}
	st.DirExists = true

	for _, name := range StandardSymlinks {
		path := filepath.Join(m.cfg.ShepherdDir, name)
		fi, err := os.Lstat(path)
		if err != nil {
			st.MissingSymlinks = append(st.MissingSymlinks, name)
			continue
		}
		// Check whether it is a symlink
		if fi.Mode()&os.ModeSymlink == 0 {
			// Exists but is not a symlink; we accept that too (Bose could
			// have set it up itself). But we log it as a hint.
			m.logger.Debug("entry is not a symlink", "name", name)
			continue
		}
		// Read the target and check whether it exists
		target, err := os.Readlink(path)
		if err != nil {
			st.BrokenSymlinks = append(st.BrokenSymlinks, name)
			continue
		}
		// If target is relative, resolve it against ShepherdDir
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

// Install sets up /mnt/nv/shepherd. Idempotent: if everything already
// matches, nothing is touched.
func (m *Manager) Install() error {
	if err := os.MkdirAll(m.cfg.ShepherdDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", m.cfg.ShepherdDir, err)
	}

	// Create the standard symlinks
	for _, name := range StandardSymlinks {
		src := filepath.Join(m.cfg.BoseConfigDir, name)
		dst := filepath.Join(m.cfg.ShepherdDir, name)

		if _, err := os.Stat(src); err != nil {
			// If the Bose config is missing, skip the symlink but do not
			// abort. shepherdd ignores missing files.
			m.logger.Warn("Bose config missing, symlink skipped",
				"src", src, "err", err)
			continue
		}

		// Does the target already exist and point to src? Then do nothing.
		if existing, err := os.Readlink(dst); err == nil && existing == src {
			m.logger.Debug("symlink already correct", "dst", dst)
			continue
		}

		// If dst exists (symlink or file), remove it first
		_ = os.Remove(dst)

		if err := os.Symlink(src, dst); err != nil {
			return fmt.Errorf("symlink %s -> %s: %w", dst, src, err)
		}
		m.logger.Info("symlink created", "dst", dst, "src", src)
	}

	// Write our own config
	xml := RenderConfig(m.cfg.AgentBinary, m.cfg.AgentArgs)
	ownPath := filepath.Join(m.cfg.ShepherdDir, OwnConfigName)
	tmpPath := ownPath + ".new"
	if err := os.WriteFile(tmpPath, []byte(xml), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, ownPath); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, ownPath, err)
	}
	m.logger.Info("own config written", "path", ownPath)

	return nil
}

// Uninstall removes /mnt/nv/shepherd completely. On the next reboot the
// init script falls back to the default path /var/run/shepherd, and our
// agent no longer starts automatically.
func (m *Manager) Uninstall() error {
	if err := os.RemoveAll(m.cfg.ShepherdDir); err != nil {
		return fmt.Errorf("remove %s: %w", m.cfg.ShepherdDir, err)
	}
	m.logger.Info("shepherd integration removed", "dir", m.cfg.ShepherdDir)
	return nil
}

// RenderConfig produces the XML representation of our shepherd config.
// We build it as a string because the XML is simple and easy to read.
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

// escapeXML escapes the five standard XML entities.
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
