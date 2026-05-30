package tlsgen

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
)

// DefaultTrustStorePaths sind die System Trust Store Files die wir auf
// der Box ueber bind mount mit unserer Root CA erweitern. Beide werden
// von unterschiedlichen Bose Komponenten gelesen (libcurl vs. openssl),
// daher beide patchen.
var DefaultTrustStorePaths = []string{
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/certs/ca-certificates.crt",
}

// trustStoreMarkerBegin / End umklammern unseren Append Block, damit
// RefreshTrustStore beim Re-Apply den vorherigen Block erkennen und
// ueberspringen kann (Idempotenz). Marker bewusst auffaellig formuliert
// damit Operator beim Debuggen mit `cat` direkt sieht was passiert ist.
const (
	trustStoreMarkerBegin = "# >>> STR Root CA (refresh) >>>"
	trustStoreMarkerEnd   = "# <<< STR Root CA (refresh) <<<"
)

// RefreshTrustStore haengt die uebergebene Root CA an die bind-gemounteten
// Trust Store Overlays an. Wird aufgerufen wenn EnsureBundle eine alte
// CA durch eine frisch generierte ersetzt hat — sonst zeigt der Trust
// Store noch die alte CA und Bose verwirft unsere Server Cert mit
// `tls: unknown certificate authority` (#60 .180 and #80 .144).
//
// Wir schreiben mit O_APPEND damit der Inode erhalten bleibt und der
// bestehende bind mount den neuen Inhalt sofort sieht — kein umount,
// kein remount, keine Race mit Bose Prozessen die das File bereits
// geoeffnet haben.
//
// Idempotent: enthaelt das Overlay bereits den exakten PEM Block den
// wir anhaengen wuerden, ueberspringen wir das Ziel. Damit kostet ein
// erneuter Aufruf (z.B. nach noch einem Cold Boot) nichts.
//
// Best-effort: ein Ziel das nicht existiert oder nicht beschreibbar
// ist wird als WARN geloggt aber blockiert die anderen Ziele nicht.
// Im Dev / Test Build (kein /etc auf der Host Platte beschreibbar)
// loggt das Funktionsergebnis sauber ohne den Agent zu killen.
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

// appendRootIfNew haengt rootCAPEM mit Marker Block an target an, es sei
// denn target enthaelt bereits exakt dieses PEM zwischen den Markern.
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
	defer f.Close()
	block := buildAppendBlock(rootCAPEM)
	if _, err := f.Write(block); err != nil {
		return err
	}
	return nil
}

// containsRootBlock prueft ob current bereits einen Marker Block mit
// exakt dem uebergebenen PEM enthaelt. Vergleich ueber bytes.Contains
// statt PEM-Parsing, weil ein Marker Match plus exakter PEM Substring
// fuer Idempotenz reicht (wir schreiben den Block immer in der gleichen
// kanonischen Form).
func containsRootBlock(current, rootCAPEM []byte) bool {
	needle := buildAppendBlock(rootCAPEM)
	return bytes.Contains(current, needle)
}

// buildAppendBlock formatiert den Append Block kanonisch:
//
//	\n# >>> STR Root CA (refresh) >>>\n
//	<PEM>
//	# <<< STR Root CA (refresh) <<<\n
//
// Das fuehrende \n verhindert dass der Marker an die letzte Zeile des
// bestehenden Bundles geklebt wird (manche Bundles enden ohne Newline).
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
