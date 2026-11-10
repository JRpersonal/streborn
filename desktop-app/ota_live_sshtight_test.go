package main

// Live check of the SSH engine push on tight speaker storage. Env-gated so CI
// never touches hardware:
//
//	STR_LIVE_SSH_TIGHT_BOX=192.0.2.1 STR_LIVE_PORT=17008 \
//	  go test . -run LiveSSHEnginePushOnTightStorage -v -timeout 15m
//
// It answers one question: when the speaker's free figure sits just above
// sshPushNeedKB for the engine, does the engine really land? The need credits
// UBIFS compression against df's pessimistic free figure, so this is the case
// where an estimate that is too optimistic would show as a short write.
//
// What it does to the speaker: opens SSH on demand through the agent (until
// the next restart), deletes the Spotify engine, fills /mnt/nv with random
// (incompressible) bytes until free is a little above the need, pushes the
// engine through pushSidecarViaSSH (the function the SSH update uses),
// verifies size and checksum on the speaker, removes the filler and restarts
// the speaker. The filler is removed on every exit.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"streborn-app/agentbin"
)

func TestLiveSSHEnginePushOnTightStorage(t *testing.T) {
	host := os.Getenv("STR_LIVE_SSH_TIGHT_BOX")
	if host == "" {
		t.Skip("STR_LIVE_SSH_TIGHT_BOX not set")
	}
	port := 8888
	if p := os.Getenv("STR_LIVE_PORT"); p != "" {
		port, _ = strconv.Atoi(p)
	}
	if !agentbin.GoLibrespotAvailable() {
		t.Fatal("this build carries no Spotify engine; run make engine-embed first")
	}
	engine := agentbin.GoLibrespotBytes()
	needKB := sshPushNeedKB(int64(len(engine)))
	const slackKB = 256 // how far above the need the free figure is set
	const filler = "/mnt/nv/str-test-filler"
	const dst = "/mnt/nv/streborn/bin/go-librespot"

	a := NewApp()

	resp, err := http.Post(fmt.Sprintf("http://%s:%d/api/agent/enable-ssh", host, port), "application/json", nil)
	if err != nil {
		t.Fatalf("enable-ssh: %v", err)
	}
	resp.Body.Close()
	if hello, herr := sshHandshake(host, 4); herr != nil || !strings.Contains(hello, "STR_SSH_OK") {
		t.Fatalf("ssh did not open: %v %q", herr, hello)
	}

	run := func(cmd string) string {
		t.Helper()
		out, rerr := boxSSHOutput(host, cmd, 120*time.Second)
		if rerr != nil {
			t.Fatalf("ssh %q: %v (%s)", cmd, rerr, strings.TrimSpace(out))
		}
		return strings.TrimSpace(out)
	}
	freeKB := func() int64 {
		t.Helper()
		n, perr := strconv.ParseInt(run("df -k /mnt/nv | tail -1 | awk '{print $(NF-2)}'"), 10, 64)
		if perr != nil {
			t.Fatalf("df: %v", perr)
		}
		return n
	}
	defer func() {
		_, _ = boxSSHOutput(host, "rm -f "+filler+"; sync", 60*time.Second)
		_ = boxReboot(host)
		t.Log("filler removed, speaker restarting (closes SSH again)")
	}()

	t.Logf("free at start: %d kB; engine %d bytes; need %d kB (was %d kB before the change)",
		freeKB(), len(engine), needKB, (int64(len(engine))+2*1024*1024)/1024)

	if os.Getenv("STR_LIVE_SSH_TIGHT_RUNNING") != "" {
		// Second question: the engine is installed and RUNNING and free is
		// below the need. The push has to delete the old engine, and that
		// only frees room once the running engine has let go of its file.
		start := freeKB()
		procs := run("pidof go-librespot || echo none")
		if start >= needKB || procs == "none" {
			t.Fatalf("precondition not met: free %d kB (need below %d), engine process %q", start, needKB, procs)
		}
		a.pushSidecarViaSSH(host)
		size := run("wc -c < " + dst + " 2>/dev/null || echo 0")
		t.Logf("engine running (pid %s), free %d kB below the need: after the push the speaker holds %s bytes, free %d kB",
			procs, start, size, freeKB())
		if size != strconv.Itoa(len(engine)) {
			t.Errorf("engine size on the speaker = %s, want %d", size, len(engine))
		}
		return
	}

	run("rm -f " + dst + " " + dst + ".sha256 " + filler + "; sync")
	empty := freeKB()
	t.Logf("free without the engine: %d kB", empty)
	fillKB := empty - needKB - slackKB
	if fillKB <= 0 {
		t.Fatalf("the speaker has only %d kB free without the engine, below need+slack %d kB: nothing to fill", empty, needKB+slackKB)
	}
	run(fmt.Sprintf("dd if=/dev/urandom of=%s bs=1024 count=%d 2>/dev/null; sync", filler, fillKB))
	// UBIFS accounts lazily; trim or extend until free sits in the window.
	for i := 0; i < 6; i++ {
		f := freeKB()
		if f >= needKB && f <= needKB+2*slackKB {
			break
		}
		fillKB += f - needKB - slackKB
		run(fmt.Sprintf("dd if=/dev/urandom of=%s bs=1024 count=%d 2>/dev/null; sync", filler, fillKB))
	}
	before := freeKB()
	t.Logf("free with a %d kB filler: %d kB (need %d kB)", fillKB, before, needKB)
	if before < needKB {
		t.Fatalf("could not set free above the need: %d kB < %d kB", before, needKB)
	}

	a.pushSidecarViaSSH(host)

	sum := sha256.Sum256(engine)
	want := hex.EncodeToString(sum[:])
	size := run("wc -c < " + dst + " 2>/dev/null || echo 0")
	got := run("sha256sum " + dst + " 2>/dev/null | cut -d' ' -f1")
	after := freeKB()
	t.Logf("after the push: size on speaker %s bytes, free %d kB, the engine took %d kB of the free figure for %d kB of file",
		size, after, before-after, len(engine)/1024)
	if size != strconv.Itoa(len(engine)) {
		t.Errorf("engine size on the speaker = %s, want %d", size, len(engine))
	}
	switch {
	case got == "":
		// No sha256sum on this firmware: the push itself verified by md5 or
		// size, and the size above is the check left here.
		t.Log("no sha256sum on the speaker, size check only")
	case got != want:
		t.Errorf("engine checksum on the speaker = %q, want %q", got, want)
	}
}
