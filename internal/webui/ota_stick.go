package webui

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// OTA stick refresh (discussion #381).
//
// run.sh's boot sync is deliberately unconditional: a stick that is inserted
// at boot wins, every time - its agent binary is copied over the NAND cache
// with no version check ("what I just put on the stick is what the box runs").
// That model breaks the HTTP-OTA when the install stick was simply left in the
// speaker: the OTA writes the new binary to NAND, the post-OTA reboot runs the
// stick->NAND sync, and the stick's OLD binary silently reverts the update.
// The box then keeps reporting the old version, the desktop app pushes the
// update again, and the user sees an update that never sticks (#381: three
// installs and four reboots for one v0.9.5 update).
//
// The desktop app already rewrites a still-inserted stick over SSH before the
// push (ota.go refreshStick), but SSH is stick-gated and often closed, and the
// stick can be unmountable from outside. The agent runs ON the box and needs
// neither: after the NAND write succeeds, refreshStickAgentBinary puts the
// same bytes onto the stick so the boot sync installs the NEW version. Both
// paths are best-effort and idempotent (identical content is skipped), so
// they complement rather than fight each other.

// refreshStickAgentBinary writes the freshly OTA'd agent binary onto a
// still-inserted STR stick, atomically (temp + rename) and flushed to the
// device, so the next boot's unconditional stick->NAND sync installs the same
// version instead of reverting the OTA. Called on the post-OTA reboot path,
// BEFORE the reboot command, so the write always completes first. Best-effort:
// a stickless box or a failed write only logs; the NAND update stands either
// way (it just will not survive the next boot with a stale stick inserted).
func refreshStickAgentBinary(body []byte, logger *slog.Logger) {
	mnt := stickMountDir()
	if mnt == "" {
		return
	}
	dst := filepath.Join(mnt, "streborn-armv7l")
	// Skip identical content: the desktop app's SSH stick refresh may already
	// have rewritten the stick before this OTA, and FAT flash wear is real.
	if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, body) {
		logger.Info("OTA stick refresh: stick already carries this binary, nothing to write", "path", dst)
		return
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, body, 0o755); err != nil {
		_ = os.Remove(tmp)
		logger.Warn("OTA stick refresh: write failed; the inserted stick keeps its OLD binary and the next boot's stick->NAND sync will revert this update - remove the stick or re-prepare it (#381)",
			"path", dst, "err", err)
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		logger.Warn("OTA stick refresh: rename failed; the inserted stick keeps its OLD binary and the next boot's stick->NAND sync will revert this update - remove the stick or re-prepare it (#381)",
			"path", dst, "err", err)
		return
	}
	// Flush to the device: the reboot follows within seconds and a vfat write
	// otherwise sits in the page cache and is lost (same reason ota.go's SSH
	// refresh does a durable unmount). Best-effort; busybox always has sync.
	_ = exec.Command("sync").Run()
	logger.Info("OTA stick refresh: stick binary updated so the boot sync keeps this OTA", "path", dst, "bytes", len(body))
}
