package tlsgen

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
)

// DefaultTrustStorePaths are the system trust store files that we
// extend on the box with our root CA via bind mount. Both are read
// by different Bose components (libcurl vs. openssl), so we patch both.
var DefaultTrustStorePaths = []string{
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/certs/ca-certificates.crt",
}

// trustStoreMarkerBegin / End bracket our append block so that
// RefreshTrustStore can detect and skip the previous block on re-apply
// (idempotency). The markers are deliberately conspicuous so an
// operator debugging with `cat` immediately sees what happened.
const (
	trustStoreMarkerBegin = "# >>> STR Root CA (refresh) >>>"
	trustStoreMarkerEnd   = "# <<< STR Root CA (refresh) <<<"
)

// RefreshTrustStore appends the given root CA to the bind-mounted
// trust store overlays. It is called when EnsureBundle has replaced an
// old CA with a freshly generated one — otherwise the trust store
// still shows the old CA and Bose rejects our server cert with
// `tls: unknown certificate authority` (#60 .180 and #80 .144).
//
// We write with O_APPEND so the inode is preserved and the existing
// bind mount sees the new content immediately — no umount, no remount,
// no race with Bose processes that already have the file open.
//
// Idempotent: if the overlay already contains the exact PEM block we
// would append, we skip the target. So a repeated call (e.g. after yet
// another cold boot) costs nothing.
//
// Best-effort: a target that does not exist or is not writable is
// logged as WARN but does not block the other targets. In the
// dev / test build (no /etc writable on the host disk) the function
// result is logged cleanly without killing the agent.
func RefreshTrustStore(rootCAPEM []byte, logger *slog.Logger) error {
	return refreshTrustStorePaths(rootCAPEM, DefaultTrustStorePaths, logger)
}

func refreshTrustStorePaths(rootCAPEM []byte, paths []string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if len(bytes.TrimSpace(rootCAPEM)) == 0 {
		return fmt.Errorf("refresh trust store: empty root CA PEM")
	}

	patched := 0
	for _, target := range paths {
		if err := appendRootIfNew(target, rootCAPEM); err != nil {
			logger.Warn("trust store refresh: target skipped",
				"target", target, "err", err)
			continue
		}
		logger.Info("trust store refresh: appended new root CA",
			"target", target)
		patched++
	}
	if patched == 0 {
		return fmt.Errorf("refresh trust store: no targets updated (none writable on this host)")
	}
	return nil
}

// appendRootIfNew appends rootCAPEM with a marker block to target,
// unless target already contains exactly this PEM between the markers.
func appendRootIfNew(target string, rootCAPEM []byte) error {
	current, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	if containsRootBlock(current, rootCAPEM) {
		return nil
	}
	f, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	block := buildAppendBlock(rootCAPEM)
	// Close once and surface its error: a discarded close on this writable trust
	// store can hide a flush failure that leaves a truncated CA appended. The write
	// error dominates when both fail.
	_, writeErr := f.Write(block)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// containsRootBlock checks whether current already contains a marker
// block with exactly the given PEM. Comparison via bytes.Contains
// instead of PEM parsing, because a marker match plus an exact PEM
// substring is enough for idempotency (we always write the block in
// the same canonical form).
func containsRootBlock(current, rootCAPEM []byte) bool {
	needle := buildAppendBlock(rootCAPEM)
	return bytes.Contains(current, needle)
}

// buildAppendBlock formats the append block canonically:
//
//	\n# >>> STR Root CA (refresh) >>>\n
//	<PEM>
//	# <<< STR Root CA (refresh) <<<\n
//
// The leading \n prevents the marker from being glued to the last line
// of the existing bundle (some bundles end without a newline).
func buildAppendBlock(rootCAPEM []byte) []byte {
	var b bytes.Buffer
	b.WriteByte('\n')
	b.WriteString(trustStoreMarkerBegin)
	b.WriteByte('\n')
	b.Write(rootCAPEM)
	if len(rootCAPEM) > 0 && rootCAPEM[len(rootCAPEM)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString(trustStoreMarkerEnd)
	b.WriteByte('\n')
	return b.Bytes()
}
