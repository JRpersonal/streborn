package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// The scripts sshPushFile sends are pinned byte for byte: they run on a
// 2014-era BusyBox and the STR_UPLOADED_<n> / OK_<n> sentinels are what the
// Go side parses. With the agent's preamble the upload command is exactly
// what updateAgentViaSSH sent before the helper existed; with the engine's
// stamp the verify command is exactly what pushSidecarViaSSH sent.
func TestSSHPushCommandsAreTheAgentAndEngineScripts(t *testing.T) {
	data := []byte("hello")
	sha := sha256.Sum256(data)
	m := md5.Sum(data)
	shaHex, md5Hex := hex.EncodeToString(sha[:]), hex.EncodeToString(m[:])

	const agent = "/mnt/nv/streborn/bin/streborn-armv7l"
	pre := "rm -rf /mnt/nv/streborn-install /mnt/nv/streborn/streborn-install 2>/dev/null; " +
		"free=$(df -k /mnt/nv 2>/dev/null | tail -1 | awk '{print $(NF-2)}'); " +
		"if [ \"${free:-0}\" -lt \"14336\" ]; then rm -f /mnt/nv/streborn/bin/go-librespot 2>/dev/null; fi; "
	wantUpload := pre + "mkdir -p /mnt/nv/streborn/bin && cat > " + agent + ".new && " +
		"sync && echo STR_UPLOADED_$(wc -c < " + agent + ".new)"
	if got := sshPushUploadCmd(agent, pre); got != wantUpload {
		t.Errorf("agent upload command drifted:\n got %q\nwant %q", got, wantUpload)
	}

	verifyHead := "new=" + agent + ".new; " +
		"if command -v sha256sum >/dev/null 2>&1; then [ \"$(sha256sum $new | cut -d' ' -f1)\" = \"" + shaHex + "\" ] || { echo STR_HASH_FAIL; exit 1; }; " +
		"elif command -v md5sum >/dev/null 2>&1; then [ \"$(md5sum $new | cut -d' ' -f1)\" = \"" + md5Hex + "\" ] || { echo STR_HASH_FAIL; exit 1; }; " +
		"else [ \"$(wc -c < $new)\" = \"5\" ] || { echo STR_HASH_FAIL; exit 1; }; fi && " +
		"chmod 0755 $new && mv $new " + agent + " && "
	if got, want := sshPushVerifyCmd(agent, data, ""), verifyHead+"sync && echo OK_5"; got != want {
		t.Errorf("agent verify command drifted:\n got %q\nwant %q", got, want)
	}

	const engine = "/mnt/nv/streborn/bin/go-librespot"
	stamp := "printf %s " + shaHex + " > " + engine + ".sha256"
	wantEngine := strings.ReplaceAll(verifyHead, agent, engine) + stamp + " && sync && echo OK_5"
	if got := sshPushVerifyCmd(engine, data, stamp); got != wantEngine {
		t.Errorf("engine verify command drifted:\n got %q\nwant %q", got, wantEngine)
	}

	// No preamble: the staging's plain upload, mkdir of the file's own dir.
	if got, want := sshPushUploadCmd("/dev/shm/streborn-install/bin/x", ""),
		"mkdir -p /dev/shm/streborn-install/bin && cat > /dev/shm/streborn-install/bin/x.new && sync && echo STR_UPLOADED_$(wc -c < /dev/shm/streborn-install/bin/x.new)"; got != want {
		t.Errorf("staging upload command drifted:\n got %q\nwant %q", got, want)
	}
}

// The byte count is parsed out of the echo, so a count that merely starts
// with the expected digits (STR_UPLOADED_12 for 1 byte) cannot pass, and local
// SSH noise ahead of it does not matter.
func TestParseUploadedCount(t *testing.T) {
	cases := []struct {
		out    string
		want   int64
		wantOK bool
	}{
		{"STR_UPLOADED_12517538\n", 12517538, true},
		{"Warning: Permanently added 'x' (RSA) to the list of known hosts.\r\nSTR_UPLOADED_23\n", 23, true},
		{"STR_UPLOADED_0\n", 0, true},
		{"cat: write error: No space left on device\n", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		if got, ok := parseUploadedCount(c.out); got != c.want || ok != c.wantOK {
			t.Errorf("parseUploadedCount(%q) = (%d, %v), want (%d, %v)", c.out, got, ok, c.want, c.wantOK)
		}
	}
}

func testPayload(n int) []byte {
	r := rand.New(rand.NewSource(int64(n)))
	b := make([]byte, n)
	_, _ = r.Read(b)
	return b
}

// One upload, one verify, the file in place with the content checked by the
// best tool the box has: sha256sum, else md5sum, else the size.
func TestSSHPushFileVerifiesWithTheBestToolTheBoxHas(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sha, md5 bool
		wantTool string
	}{
		{"sha256sum present", true, true, "sha256sum"},
		{"md5sum only", false, true, "md5sum"},
		{"neither, size only", false, false, "wc -c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			box := newFakeSSHBox()
			box.hasSha256, box.hasMd5 = tc.sha, tc.md5
			startFakeSSHBox(t, box)
			data := testPayload(300_000)
			const dst = "/mnt/nv/streborn/bin/streborn-armv7l"
			if err := sshPushFile(fakeBoxHost, dst, data, sshPushOptions{preamble: "rm -f /mnt/nv/x 2>/dev/null; "}); err != nil {
				t.Fatalf("push: %v", err)
			}
			if got, ok := box.file(dst); !ok || !bytes.Equal(got, data) {
				t.Fatal("the file on the box is not the one sent")
			}
			if _, ok := box.file(dst + ".new"); ok {
				t.Fatal("the .new file is still on the box after the rename")
			}
			if len(box.tools()) != 1 || box.tools()[0] != tc.wantTool {
				t.Fatalf("verified with %v, want %s", box.tools(), tc.wantTool)
			}
			if box.uploadCount() != 1 {
				t.Fatalf("uploaded %d times, want 1", box.uploadCount())
			}
		})
	}
}

// A file that arrived with the right size but the wrong content never becomes
// the live file: the verify fails, the rename does not happen.
func TestSSHPushFileRefusesACorruptedUpload(t *testing.T) {
	box := newFakeSSHBox()
	box.corruptNext = true
	startFakeSSHBox(t, box)
	data := testPayload(50_000)
	const dst = "/mnt/nv/streborn/bin/go-librespot"
	err := sshPushFile(fakeBoxHost, dst, data, sshPushOptions{afterRename: "printf %s abc > " + dst + ".sha256"})
	var ver *sshPushVerifyError
	if !errors.As(err, &ver) {
		t.Fatalf("got %v, want an sshPushVerifyError", err)
	}
	if !strings.Contains(ver.out, "STR_HASH_FAIL") {
		t.Fatalf("verify output %q does not carry the box's STR_HASH_FAIL", ver.out)
	}
	if _, ok := box.file(dst); ok {
		t.Fatal("a corrupted upload was renamed into place")
	}
}

// A short upload is reported with the count the box holds, and a caller that
// allows two attempts gets the second one.
func TestSSHPushFileReportsAndRetriesAShortUpload(t *testing.T) {
	box := newFakeSSHBox()
	box.truncateNext = 4096
	startFakeSSHBox(t, box)
	data := testPayload(100_000)
	const dst = "/dev/shm/streborn-install/streborn-armv7l"

	err := sshPushFile(fakeBoxHost, dst, data, sshPushOptions{attempts: 1})
	var short *sshPushShortError
	if !errors.As(err, &short) {
		t.Fatalf("got %v, want an sshPushShortError", err)
	}
	if !short.gotOK || short.got != int64(len(data)-4096) || short.want != int64(len(data)) {
		t.Fatalf("short error carries got=%d ok=%v want=%d", short.got, short.gotOK, short.want)
	}
	if _, ok := box.file(dst); ok {
		t.Fatal("a short upload was renamed into place")
	}

	box.truncateNext = 4096
	if err := sshPushFile(fakeBoxHost, dst, data, sshPushOptions{attempts: 2}); err != nil {
		t.Fatalf("second attempt should have landed: %v", err)
	}
	if box.uploadCount() != 3 {
		t.Fatalf("uploads = %d, want 3 (one refused, then a retry pair)", box.uploadCount())
	}
	if got, _ := box.file(dst); !bytes.Equal(got, data) {
		t.Fatal("the retried file is not the one sent")
	}
}

// A preamble that gates on free space refuses with STR_NO_SPACE; that is the
// NAND-full verdict, not a failed upload, so retry loops stop on it.
func TestSSHPushFileReportsAFullNAND(t *testing.T) {
	box := newFakeSSHBox()
	box.noSpace = true
	startFakeSSHBox(t, box)
	pre := "free=$(df -k /mnt/nv 2>/dev/null | tail -1 | awk '{print $(NF-2)}'); " +
		"if [ \"${free:-0}\" -lt \"18432\" ]; then echo STR_NO_SPACE; exit 1; fi; "
	err := sshPushFile(fakeBoxHost, "/mnt/nv/streborn/bin/go-librespot", testPayload(1000), sshPushOptions{preamble: pre})
	if !errors.Is(err, errInsufficientNAND) {
		t.Fatalf("got %v, want errInsufficientNAND", err)
	}
}

// The install staging goes through the same push: every staged file is
// content-checked (it used to be size-checked only), a short transfer is the
// uploadTruncatedError the staging chooser keys its USB-stick guidance on,
// and the file names in the errors stay the staged names.
func TestStageRepairFilesVerifiesEveryFile(t *testing.T) {
	files := map[string][]byte{
		"install.sh":       []byte("#!/bin/sh\necho hi\n"),
		"streborn-armv7l":  testPayload(2 << 20),
		"bin/go-librespot": testPayload(10_000),
	}
	a := newTestApp()

	box := newFakeSSHBox()
	startFakeSSHBox(t, box)
	if err := a.stageRepairFiles(fakeBoxHost, "/dev/shm/streborn-install", files); err != nil {
		t.Fatalf("stage: %v", err)
	}
	for name, data := range files {
		if got, ok := box.file("/dev/shm/streborn-install/" + name); !ok || !bytes.Equal(got, data) {
			t.Errorf("%s did not land intact", name)
		}
	}
	if len(box.tools()) != len(files) {
		t.Fatalf("%d files verified, want %d", len(box.tools()), len(files))
	}

	// The big file is the only one retried, and a transfer that stays short
	// is the typed truncation the chooser aggregates.
	box = newFakeSSHBox()
	box.truncateNext = 1 << 20
	startFakeSSHBox(t, box)
	err := a.stageRepairFiles(fakeBoxHost, "/dev/shm/streborn-install", map[string][]byte{"streborn-armv7l": files["streborn-armv7l"]})
	if err != nil {
		t.Fatalf("one short transfer must be absorbed by the retry: %v", err)
	}
	box = newFakeSSHBox()
	box.truncateNext = 7
	startFakeSSHBox(t, box)
	err = a.stageRepairFiles(fakeBoxHost, "/dev/shm/streborn-install", map[string][]byte{"install.sh": files["install.sh"]})
	var trunc *uploadTruncatedError
	if !errors.As(err, &trunc) {
		t.Fatalf("got %v, want an uploadTruncatedError", err)
	}
	if want := fmt.Sprintf("upload install.sh truncated: speaker received %d of %d bytes", len(files["install.sh"])-7, len(files["install.sh"])); err.Error() != want {
		t.Fatalf("truncation text %q, want %q", err.Error(), want)
	}

	// Corruption with the right size: the new check, named as such.
	box = newFakeSSHBox()
	box.corruptNext = true
	startFakeSSHBox(t, box)
	err = a.stageRepairFiles(fakeBoxHost, "/dev/shm/streborn-install", map[string][]byte{"install.sh": files["install.sh"]})
	if err == nil || !strings.Contains(err.Error(), "upload install.sh arrived with the right size but failed its checksum") {
		t.Fatalf("got %v, want the checksum failure naming the file", err)
	}
	if errors.As(err, &trunc) {
		t.Fatal("a checksum failure must not count as a truncated transfer")
	}
}
