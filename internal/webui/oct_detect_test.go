// Tests for the OpenCloudTouch leftover detection (#698). The fingerprints
// come from the one migrated box measured so far, the reporter's ST10: the
// SDK-config backup on NAND and the "# OCT-START" redirect block in the
// persistent /etc/hosts, which kept BoseApp and STSCertified in SYN_SENT
// against the mod's long-dead LAN server.

package webui

import (
	"os"
	"path/filepath"
	"testing"
)

// redirectOCTPaths points the detection at a temp tree and restores the
// production paths afterwards.
func redirectOCTPaths(t *testing.T) (backup, live, original string) {
	t.Helper()
	dir := t.TempDir()
	backup = filepath.Join(dir, "OverrideSdkPrivateCfg.xml.oct-backup")
	live = filepath.Join(dir, "hosts.live")
	original = filepath.Join(dir, "hosts.original")
	oldB, oldL, oldO := octBackupPath, hostsLivePath, hostsOriginalPath
	octBackupPath, hostsLivePath, hostsOriginalPath = backup, live, original
	t.Cleanup(func() {
		octBackupPath, hostsLivePath, hostsOriginalPath = oldB, oldL, oldO
	})
	return backup, live, original
}

const octLiveHosts = "127.0.0.1\tlocalhost\n" +
	"# OCT-START\n" +
	"192.0.2.8\tstreaming.bose.com\t# OpenCloudTouch redirect\n" +
	"# OCT-END\n"

func TestDetectOpenCloudTouch(t *testing.T) {
	backup, live, original := redirectOCTPaths(t)

	// Nothing there: an STR-only box must stay silent.
	if detectOpenCloudTouch() {
		t.Fatal("clean box misdetected as OpenCloudTouch")
	}

	// The SDK-config backup alone is enough: it is the removable artifact the
	// one-click cleanup can act on.
	if err := os.WriteFile(backup, []byte("<x/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !detectOpenCloudTouch() {
		t.Error("oct-backup file not detected")
	}
	if err := os.Remove(backup); err != nil {
		t.Fatal(err)
	}

	// A LIVE hosts block means the boot/agent filters have not neutralized it
	// yet: a real, active conflict.
	if err := os.WriteFile(live, []byte(octLiveHosts), 0o644); err != nil {
		t.Fatal(err)
	}
	if !detectOpenCloudTouch() {
		t.Error("live OCT hosts block not detected")
	}

	// A block only in the verbatim boot copy, with a clean live file, is the
	// healthy post-fix state: the persistent block on the read-only rootfs can
	// never be removed without firmware bending, so it must NOT keep the
	// warning banner up forever. It stays visible to bundles via
	// /api/debug/state instead.
	if err := os.WriteFile(live, []byte("127.0.0.1\tlocalhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte(octLiveHosts), 0o644); err != nil {
		t.Fatal(err)
	}
	if detectOpenCloudTouch() {
		t.Error("neutralized (boot-copy-only) block must not re-raise the banner")
	}
}

func TestDetectConflictingModNamesOpenCloudTouch(t *testing.T) {
	backup, _, _ := redirectOCTPaths(t)
	if detectAfterTouch() {
		// This dev/CI host genuinely carries AfterTouch fingerprints under
		// /mnt/nv; the priority order would mask the OCT result.
		t.Skip("host machine has AfterTouch artifacts")
	}
	if err := os.WriteFile(backup, []byte("<x/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectConflictingMod(); got != "OpenCloudTouch" {
		t.Errorf("detectConflictingMod = %q, want OpenCloudTouch", got)
	}
}
