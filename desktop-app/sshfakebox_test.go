package main

import (
	"crypto/ed25519"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeSSHBox is an in-process stand-in for a speaker's sshd plus the few BusyBox
// commands the upload-and-verify scripts run. It is reached through the real
// transport (boxSSHOutput / boxSSHUploadStdin, the native client, the per-host
// cache) by pointing the cache's dial seam at a local x/crypto/ssh server, so
// a test exercises the whole round trip short of the wire to a real speaker.
//
// It does not interpret shell. It recognises the exact command shapes
// sshPushUploadCmd and sshPushVerifyCmd generate (which a separate test pins
// byte for byte) and answers the way a speaker would: the byte count it holds,
// STR_HASH_FAIL when the hash the command carries does not match the file,
// OK_<n> after the rename.
type fakeSSHBox struct {
	mu    sync.Mutex
	files map[string][]byte
	// Which hash tools the box's BusyBox carries. A real speaker has at least
	// md5sum; the size-only branch exists for the one that has neither.
	hasSha256, hasMd5 bool
	// truncateNext drops that many trailing bytes from the next upload, once;
	// corruptNext flips one byte of the next upload, once. Both model what a
	// weak Wi-Fi link or a sick flash does to a transfer.
	truncateNext int
	corruptNext  bool
	// noSpace makes a preamble that gates on df refuse with STR_NO_SPACE.
	noSpace bool
	// Observations for the assertions.
	uploads      int
	verifiedWith []string
	cmds         []string
}

func newFakeSSHBox() *fakeSSHBox {
	return &fakeSSHBox{files: map[string][]byte{}, hasSha256: true, hasMd5: true}
}

var (
	fakeUploadRe  = regexp.MustCompile(`cat > (\S+)\.new && sync && echo STR_UPLOADED_`)
	fakeVerifyRe  = regexp.MustCompile(`^new=(\S+)\.new; `)
	fakeShaRe     = regexp.MustCompile(`sha256sum \$new \| cut -d' ' -f1\)" = "([0-9a-f]{64})"`)
	fakeMd5Re     = regexp.MustCompile(`md5sum \$new \| cut -d' ' -f1\)" = "([0-9a-f]{32})"`)
	fakeSizeRe    = regexp.MustCompile(`wc -c < \$new\)" = "(\d+)"`)
	fakeRenameRe  = regexp.MustCompile(`mv \$new (\S+) && `)
	fakeSentinel  = regexp.MustCompile(`echo OK_(\d+)$`)
	fakeShaStamp  = regexp.MustCompile(`printf %s ([0-9a-f]{64}) > (\S+) && `)
	fakeNoSpaceRe = regexp.MustCompile(`echo STR_NO_SPACE; exit 1; fi; `)
)

// exec answers one command. stdin is what the session streamed.
func (b *fakeSSHBox) exec(cmd string, stdin []byte) (out string, code int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cmds = append(b.cmds, cmd)
	switch {
	case cmd == "echo STR_SSH_OK":
		return "STR_SSH_OK\n", 0
	case strings.HasPrefix(cmd, "rm -rf ") || strings.HasPrefix(cmd, "chmod +x "):
		return "", 0
	}
	if m := fakeUploadRe.FindStringSubmatch(cmd); m != nil {
		b.uploads++
		if b.noSpace && fakeNoSpaceRe.MatchString(cmd) {
			return "STR_NO_SPACE\n", 1
		}
		data := append([]byte(nil), stdin...)
		if b.truncateNext > 0 && b.truncateNext < len(data) {
			data = data[:len(data)-b.truncateNext]
			b.truncateNext = 0
		}
		if b.corruptNext && len(data) > 0 {
			data[len(data)/2] ^= 0x5a
			b.corruptNext = false
		}
		b.files[m[1]+".new"] = data
		return fmt.Sprintf("STR_UPLOADED_%d\n", len(data)), 0
	}
	if m := fakeVerifyRe.FindStringSubmatch(cmd); m != nil {
		dst := m[1]
		data, ok := b.files[dst+".new"]
		if !ok {
			return "STR_HASH_FAIL\n", 1
		}
		var pass bool
		switch {
		case b.hasSha256:
			sum := sha256.Sum256(data)
			pass = fakeShaRe.FindStringSubmatch(cmd)[1] == hex.EncodeToString(sum[:])
			b.verifiedWith = append(b.verifiedWith, "sha256sum")
		case b.hasMd5:
			sum := md5.Sum(data)
			pass = fakeMd5Re.FindStringSubmatch(cmd)[1] == hex.EncodeToString(sum[:])
			b.verifiedWith = append(b.verifiedWith, "md5sum")
		default:
			pass = fakeSizeRe.FindStringSubmatch(cmd)[1] == strconv.Itoa(len(data))
			b.verifiedWith = append(b.verifiedWith, "wc -c")
		}
		if !pass {
			return "STR_HASH_FAIL\n", 1
		}
		if r := fakeRenameRe.FindStringSubmatch(cmd); r == nil || r[1] != dst {
			return "mv: bad target\n", 1
		}
		delete(b.files, dst+".new")
		b.files[dst] = data
		if s := fakeShaStamp.FindStringSubmatch(cmd); s != nil {
			b.files[s[2]] = []byte(s[1])
		}
		return "OK_" + fakeSentinel.FindStringSubmatch(cmd)[1] + "\n", 0
	}
	return "sh: " + cmd + ": not found\n", 127
}

func (b *fakeSSHBox) uploadCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.uploads
}

func (b *fakeSSHBox) tools() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.verifiedWith...)
}

func (b *fakeSSHBox) file(name string) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.files[name]
	return d, ok
}

// startFakeSSHBox serves box over a local SSH server and routes the app's
// cached native transport to it for the rest of the test. The dial seam is
// sshClientCache.dial: the host name callers pass is ignored and every dial
// lands on the fake server.
func startFakeSSHBox(t *testing.T, box *fakeSSHBox) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeSSHConn(c, cfg, box)
		}
	}()
	addr := ln.Addr().String()
	prev := boxSSHClients
	boxSSHClients = newSSHClientCache(func(string) (sshTransport, error) {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			return nil, err
		}
		cc, chans, reqs, err := ssh.NewClientConn(conn, addr, nativeSSHConfig(10*time.Second))
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return ssh.NewClient(cc, chans, reqs), nil
	})
	t.Cleanup(func() {
		boxSSHClients.forgetHost(fakeBoxHost)
		boxSSHClients = prev
		_ = ln.Close()
	})
}

// fakeBoxHost is the host name the tests pass; the dial seam ignores it.
const fakeBoxHost = "fake-box"

func serveFakeSSHConn(c net.Conn, cfg *ssh.ServerConfig, box *fakeSSHBox) {
	sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range chReqs {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				var p struct{ Command string }
				_ = ssh.Unmarshal(req.Payload, &p)
				_ = req.Reply(true, nil)
				// The client streams stdin and half-closes; a plain command
				// sends an empty stdin and the same EOF.
				stdin, _ := io.ReadAll(ch)
				out, code := box.exec(p.Command, stdin)
				_, _ = io.WriteString(ch, out)
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
				return
			}
		}()
	}
}
