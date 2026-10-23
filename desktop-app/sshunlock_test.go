package main

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

// A speaker whose :17000 setup port does not answer is not touched at all:
// no unlock, no restart, no reboot memory, and the error says so, because the
// install preflight words its advice on exactly that difference ("not
// attempted" versus "unlock ran but did not open SSH").
func TestEnsureSSHOpenLeavesASpeakerWithoutSetupPortAlone(t *testing.T) {
	if c, err := net.DialTimeout("tcp", "127.0.0.1:17000", 300*time.Millisecond); err == nil {
		_ = c.Close()
		t.Skip("something listens on 127.0.0.1:17000 on this machine")
	}
	a := newTestApp()
	a.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	unlockReboots.Delete("127.0.0.1")

	opened, note, err := a.ensureSSHOpen("127.0.0.1", "SoundTouch 10", sshUnlockOptions{tag: "test", rememberReboot: true})
	if opened || note != "" || !errors.Is(err, errSetupPortClosed) {
		t.Fatalf("got opened=%v note=%q err=%v; want the closed-port verdict", opened, note, err)
	}
	if _, ok := recentUnlockReboot("127.0.0.1"); ok {
		t.Fatal("a closed :17000 must not be remembered as a restart we caused")
	}
}
