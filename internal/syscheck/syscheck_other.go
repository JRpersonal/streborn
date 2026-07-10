//go:build !linux

package syscheck

import "log/slog"

// Run is a no-op off-Linux: the real system check reads /proc and Linux
// syscalls, and the agent only ever runs on the speaker's ARM Linux. The stub
// keeps cmd/agent compiling (and therefore testable) on dev hosts.
func Run(logger *slog.Logger, glrPath string) {
	logger.Info("STR system check skipped (non-linux dev host)", "goLibrespotPath", glrPath)
}
