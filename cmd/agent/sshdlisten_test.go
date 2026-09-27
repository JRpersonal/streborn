package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Whether sshd is really listening, as opposed to having exited zero.
//
// Bose's /etc/init.d/sshd gates on a remote_services marker and exits 0 while
// declining to start. The agent believed the exit status and logged "sshd
// started" on three speakers whose listener table had no port 22 on it at all.
// A log saying the opposite of the truth is worse than no log, and the desktop
// app's "Reset speaker setup (STR stays)" walks straight into an SSH handshake
// on the strength of it.
//
// tcpPortListening reads procfs, so the test feeds it procfs.

func withProcNet(t *testing.T, tcp, tcp6 string) {
	t.Helper()
	dir := t.TempDir()
	p4 := filepath.Join(dir, "tcp")
	p6 := filepath.Join(dir, "tcp6")
	if err := os.WriteFile(p4, []byte(tcp), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p6, []byte(tcp6), 0o644); err != nil {
		t.Fatal(err)
	}
	old := procNetTCPPaths
	procNetTCPPaths = []string{p4, p6}
	t.Cleanup(func() { procNetTCPPaths = old })
}

// procLine builds one /proc/net/tcp row: the columns this code reads are the
// local address (field 1) and the connection state (field 3).
func procLine(idx, port int, state string) string {
	return "  " + strconv.Itoa(idx) + ": 00000000:" + strings.ToUpper(strconv.FormatInt(int64(port), 16)) +
		" 00000000:0000 " + state + " 00000000:00000000 00:00000000 00000000     0        0 1234 1 00000000 100 0 0 10 0"
}

const header = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func TestAListeningPortIsSeen(t *testing.T) {
	withProcNet(t, header+procLine(0, 22, "0A")+"\n", header)
	if !tcpPortListening(22) {
		t.Error("a listening sshd was reported as absent")
	}
}

func TestAPortThatIsOnlyCONNECTEDIsNotListening(t *testing.T) {
	// 01 = ESTABLISHED. An outgoing connection from port 22 is not a daemon
	// accepting on it, and treating it as one is how the old claim went wrong.
	withProcNet(t, header+procLine(0, 22, "01")+"\n", header)
	if tcpPortListening(22) {
		t.Error("an established socket was mistaken for a listener")
	}
}

func TestTheSpeakersFromTheReportReadAsNotListening(t *testing.T) {
	// The listener set those three speakers actually had: plenty of ports, no
	// 22. This is the case the agent used to log as "sshd started".
	var b strings.Builder
	b.WriteString(header)
	for i, port := range []int{80, 443, 3678, 4140, 8080, 8081, 8090, 8091, 8200, 8888, 9080, 17000, 17008} {
		b.WriteString(procLine(i, port, "0A") + "\n")
	}
	withProcNet(t, b.String(), header)
	if tcpPortListening(22) {
		t.Error("port 22 was reported as listening on a speaker that had no sshd")
	}
	// Sanity: the ports that ARE there must be found, or the test proves nothing.
	if !tcpPortListening(8888) {
		t.Error("a port that is in the table was not found, so the negative above is meaningless")
	}
}

func TestMissingProcfsIsNotAListener(t *testing.T) {
	old := procNetTCPPaths
	procNetTCPPaths = []string{filepath.Join(t.TempDir(), "absent")}
	t.Cleanup(func() { procNetTCPPaths = old })
	if tcpPortListening(22) {
		t.Error("an unreadable procfs was treated as proof of a listener")
	}
}
