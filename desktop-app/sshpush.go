// sshPushFile puts one file onto the speaker over SSH and proves it arrived:
// stream to <dst>.new, read back the byte count, content-verify with the best
// tool the box's BusyBox has (sha256sum, else md5sum, else the size), then
// rename into place and sync. The agent binary, the Spotify engine and the
// network install's staged file set all go through here. Before this file
// the agent and the engine each carried their own copy of the upload and
// verify scripts, and the staging copy checked only the size.

package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// sshPushOptions is what differs between the three callers. The scripts
// themselves do not.
type sshPushOptions struct {
	// preamble is shell run in the upload session before the file is written:
	// the space reclaim and df gates. Every statement ends with "; " so the
	// mkdir that follows reads on from it. A preamble that refuses for space
	// prints STR_NO_SPACE and exits 1; sshPushFile reports that as
	// errInsufficientNAND instead of a failed upload.
	preamble string
	// afterRename is shell run after the verified file is renamed into place
	// and before the final sync (the engine's .sha256 stamp). No trailing "&&".
	afterRename string
	// attempts is how often the upload is tried before giving up; 0 and 1 both
	// mean once. The install staging retries the agent binary once, because a
	// transient stall mid-stream is exactly what staging-then-verify absorbs.
	attempts int
	// uploadTimeout is the stdin stream's STALL timeout (no progress for this
	// long), see boxSSHUploadStdin; 0 is 120 s, which covers a 10 MB upload
	// over slow Wi-Fi with the speaker's modest CPU spending time on crypto.
	uploadTimeout time.Duration
	// verifyTimeout bounds the hash command; 0 is 45 s. The install staging
	// uses 30 s, matching its other commands (see stageRepairFiles on why
	// those look generous).
	verifyTimeout time.Duration
}

// sshPushUploadError: the session that streams the file failed (the transport
// dropped, the stream stalled, or the preamble exited non-zero).
type sshPushUploadError struct {
	dst  string
	want int64
	err  error
	out  string
}

func (e *sshPushUploadError) Error() string {
	return fmt.Sprintf("upload of %s (%d bytes) failed: %v (%s)", e.dst, e.want, e.err, strings.TrimSpace(e.out))
}

func (e *sshPushUploadError) Unwrap() error { return e.err }

// sshPushShortError: the session ended cleanly but the byte count the box
// echoed back is not the number of bytes sent, so the box does not hold the
// file: NAND full, or the stream was cut. gotOK is false when the session
// echoed no count at all (the redirect itself failed).
type sshPushShortError struct {
	dst   string
	want  int64
	got   int64
	gotOK bool
	out   string
}

func (e *sshPushShortError) Error() string {
	return fmt.Sprintf("upload of %s did not land intact: expected %d bytes on the box, it holds %s", e.dst, e.want, e.received())
}

// received words the count for the error texts: the number, or "no byte
// count" when the session echoed none.
func (e *sshPushShortError) received() string {
	if !e.gotOK {
		return "no byte count"
	}
	return strconv.FormatInt(e.got, 10)
}

// sshPushVerifyError: the file landed with the right size and the content
// check or the rename into place failed, so the live file was left untouched.
type sshPushVerifyError struct {
	dst string
	err error
	out string
}

func (e *sshPushVerifyError) Error() string {
	return fmt.Sprintf("content-verify or rename of %s failed: %v (%s)", e.dst, e.err, strings.TrimSpace(e.out))
}

func (e *sshPushVerifyError) Unwrap() error { return e.err }

// sshPushUploadCmd is the upload session's command: the caller's preamble,
// then mkdir, cat to <dst>.new, sync, and the on-box byte count. The session
// can end "successfully" while the file never landed (stream cut, ENOSPC, or
// a reboot racing the write; live 2026-07-31: verify found no .new at all
// after a clean-looking upload), so the command syncs and echoes the count;
// anything else means the box does not hold the file.
func sshPushUploadCmd(dst, preamble string) string {
	return preamble + "mkdir -p " + path.Dir(dst) + " && cat > " + dst + ".new && " +
		"sync && echo STR_UPLOADED_$(wc -c < " + dst + ".new)"
}

// sshPushVerifyCmd content-verifies <dst>.new before the atomic rename so a
// half-uploaded OR corrupted file never becomes the live file. A size check
// alone cannot do that job: consecutive releases are byte-equal in size
// (v0.9.4/5/6 are all exactly 12517538 bytes), so a stale same-size file
// would "verify". Prefer sha256sum, fall back to md5sum (ubiquitous on
// BusyBox), and only then size. sync after the rename so the swap survives
// the reboot that follows (UBIFS write-back, #381; an unsynced engine
// vanished across exactly that reboot, deqw 2026-07-12). Sentinel "OK_<size>"
// so the caller can distinguish a successful rename from any stderr noise.
func sshPushVerifyCmd(dst string, data []byte, afterRename string) string {
	sum := sha256.Sum256(data)
	md5sum := md5.Sum(data)
	after := ""
	if afterRename != "" {
		after = afterRename + " && "
	}
	return fmt.Sprintf(
		"new=%s.new; "+
			"if command -v sha256sum >/dev/null 2>&1; then [ \"$(sha256sum $new | cut -d' ' -f1)\" = \"%s\" ] || { echo STR_HASH_FAIL; exit 1; }; "+
			"elif command -v md5sum >/dev/null 2>&1; then [ \"$(md5sum $new | cut -d' ' -f1)\" = \"%s\" ] || { echo STR_HASH_FAIL; exit 1; }; "+
			"else [ \"$(wc -c < $new)\" = \"%d\" ] || { echo STR_HASH_FAIL; exit 1; }; fi && "+
			"chmod 0755 $new && "+
			"mv $new %s && "+
			"%ssync && echo OK_%d",
		dst, hex.EncodeToString(sum[:]), hex.EncodeToString(md5sum[:]), len(data), dst, after, len(data))
}

// sshUploadedCount pulls the byte count out of the upload session's
// STR_UPLOADED_<n> echo. Parsed, not string-matched, so local SSH noise ahead
// of it never matters and a count that merely starts with the expected digits
// cannot pass.
var sshUploadedCount = regexp.MustCompile(`STR_UPLOADED_(\d+)`)

func parseUploadedCount(out string) (int64, bool) {
	m := sshUploadedCount.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// sshPushFile uploads data to dst on the box and returns nil only once the
// file is in place with its content verified. The errors are typed
// (sshPushUploadError, sshPushShortError, sshPushVerifyError, or one wrapping
// errInsufficientNAND when the preamble refused for space) so each caller can
// keep its own wording and retry policy.
func sshPushFile(host, dst string, data []byte, o sshPushOptions) error {
	if o.attempts < 1 {
		o.attempts = 1
	}
	if o.uploadTimeout == 0 {
		o.uploadTimeout = 120 * time.Second
	}
	if o.verifyTimeout == 0 {
		o.verifyTimeout = 45 * time.Second
	}
	n := int64(len(data))
	uploadCmd := sshPushUploadCmd(dst, o.preamble)
	var lastErr error
	for try := 0; try < o.attempts; try++ {
		out, err := boxSSHUploadStdin(host, uploadCmd, bytes.NewReader(data), o.uploadTimeout)
		if err != nil {
			if strings.Contains(out, "STR_NO_SPACE") {
				return fmt.Errorf("%w: the speaker refused the push of %s for lack of space", errInsufficientNAND, dst)
			}
			lastErr = &sshPushUploadError{dst: dst, want: n, err: err, out: out}
			continue
		}
		if got, ok := parseUploadedCount(out); !ok || got != n {
			lastErr = &sshPushShortError{dst: dst, want: n, got: got, gotOK: ok, out: out}
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return lastErr
	}
	sentinel := fmt.Sprintf("OK_%d", n)
	if out, err := boxSSHOutput(host, sshPushVerifyCmd(dst, data, o.afterRename), o.verifyTimeout); err != nil || !strings.Contains(out, sentinel) {
		return &sshPushVerifyError{dst: dst, err: err, out: out}
	}
	return nil
}
