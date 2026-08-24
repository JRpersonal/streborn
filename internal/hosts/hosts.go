// Package hosts manipulates /etc/hosts at start and cleans it up again at stop.
// The Bose software is redirected so it communicates with the local binary
// instead of the real Bose servers.
package hosts

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
)

const (
	defaultPath = "/etc/hosts"
	beginMarker = "# >>> streborn begin >>>"
	endMarker   = "# <<< streborn end <<<"

	// OpenCloudTouch (SoundTouch Hybrid) marks its redirect block in the
	// persistent /etc/hosts with these two lines. On a box migrated from that
	// mod the block (13 Bose hostnames pointed at the mod's LAN server)
	// survives on the read-only rootfs and gets copied into STR's live hosts
	// file at boot, see removeForeignRedirects (#698).
	octBeginMarker = "# OCT-START"
	octEndMarker   = "# OCT-END"
)

// boseCloudDomains are the cloud domains the Bose firmware talks to. Any
// hosts line that points a hostname under one of these at a non-loopback
// address is a rival mod's redirect: STR needs the box to resolve the Bose
// hosts it emulates to its own loopback listeners, and the dead domains it
// does not emulate must fall through to real DNS (NXDOMAIN/time out is the
// benign state, see DefaultEntries). The list covers the 13 hostnames seen
// in the field OpenCloudTouch block (#698) plus the TuneIn partner domain
// STR itself redirects.
var boseCloudDomains = []string{
	"bose.com",
	"bose.io",
	"bosecm.com",
	"bosesoundtouch.com",
	"vtuner.com",
	"radiotime.com",
}

// Entry describes a line that is inserted into /etc/hosts.
type Entry struct {
	IP   string
	Host string
}

// DefaultEntries returns the default redirects.
//
// The TuneIn hostname with the Bose partner hash has been shut down by Bose
// since 2026-05-15 (NXDOMAIN). We redirect it to 127.0.0.1 so our marge stub
// can emulate the TuneIn API and STSCertified BMXTuneInClient believes the
// Bose TuneIn cloud is reachable.
func DefaultEntries() []Entry {
	// Only the three hosts marge actively emulates are redirected. We used to
	// also black-hole events.api.bosecm.com and worldwide.bose.com to 0.0.0.0,
	// but that served no STR function and hurt the BCO/scm ST20: connecting to
	// 0.0.0.0 resolves to loopback and returns an instant RST, which the box's
	// NetManager connectivity probe reads as an actively broken link and reacts
	// to by re-associating the Wi-Fi. On the ethernet-only scm path STR persists
	// no Wi-Fi profile, so that re-association drops the speaker offline
	// ("Wi-Fi Not Provided", #302/#303). Left at real DNS these dead hosts just
	// NXDOMAIN/time out, the benign way the stock post-cloud box already tolerates.
	return []Entry{
		{IP: "127.0.0.1", Host: "streaming.bose.com"},
		{IP: "127.0.0.1", Host: "content.api.bose.io"},
		{IP: "127.0.0.1", Host: "7f5055e9ff15f2a5035a488b81ec10f4.api.radiotime.com"},
	}
}

// Manager manages a marked block in /etc/hosts.
type Manager struct {
	path   string
	logger *slog.Logger
}

// New creates a Manager. If path is empty, /etc/hosts is used.
func New(path string, logger *slog.Logger) *Manager {
	if path == "" {
		path = defaultPath
	}
	return &Manager{path: path, logger: logger}
}

// Apply inserts the marked block with the entries into /etc/hosts
// and replaces any existing old block.
func (m *Manager) Apply(entries []Entry) error {
	current, err := os.ReadFile(m.path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read hosts: %w", err)
	}

	stripped := removeBlock(current)

	// Filter a rival mod's redirects out of the base BEFORE appending our own
	// block (#698). A box migrated from OpenCloudTouch still carries that mod's
	// "# OCT-START".."# OCT-END" block (Bose hostnames pointed at the mod's LAN
	// server) in the persistent /etc/hosts on the read-only rootfs, and run.sh
	// seeds the live copy from that file. libc hosts resolution is
	// first-match-wins and the leftover block sits above our appended one, so
	// its LAN IP won even for the hostnames both mods redirect: the reporter's
	// box had BoseApp and STSCertified stuck in SYN_SENT against the long-dead
	// OCT server on every boot, and removing the block from the live copy
	// stopped the connections completely. The persistent file itself is NOT
	// touched: that would need remounting the rootfs rw, which is firmware
	// bending and off the table by standing rule. Neutralizing the live copy
	// on every Apply is the whole fix.
	cleaned, foreign := removeForeignRedirects(stripped, entries)
	setForeignFiltered(foreign)
	if len(foreign) > 0 && m.logger != nil {
		m.logger.Info("foreign hosts redirects filtered", "path", m.path,
			"lines", len(foreign), "issue", "#698")
	}

	block := renderBlock(entries)

	merged := append(bytes.TrimRight(cleaned, "\n"), '\n', '\n')
	merged = append(merged, block...)

	if err := writeAtomic(m.path, merged); err != nil {
		return fmt.Errorf("hosts write: %w", err)
	}
	if m.logger != nil {
		m.logger.Info("hosts block active", "path", m.path, "entries", len(entries))
	}
	return nil
}

// Restore removes the marked block from /etc/hosts again.
func (m *Manager) Restore() error {
	current, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read hosts: %w", err)
	}
	stripped := removeBlock(current)
	if err := writeAtomic(m.path, stripped); err != nil {
		return fmt.Errorf("write hosts: %w", err)
	}
	if m.logger != nil {
		m.logger.Info("hosts block removed", "path", m.path)
	}
	return nil
}

func renderBlock(entries []Entry) []byte {
	var sb strings.Builder
	sb.WriteString(beginMarker)
	sb.WriteByte('\n')
	for _, e := range entries {
		fmt.Fprintf(&sb, "%s\t%s\n", e.IP, e.Host)
	}
	sb.WriteString(endMarker)
	sb.WriteByte('\n')
	return []byte(sb.String())
}

func removeBlock(in []byte) []byte {
	lines := strings.Split(string(in), "\n")
	out := make([]string, 0, len(lines))
	inBlock := false
	for _, l := range lines {
		switch {
		case strings.TrimSpace(l) == beginMarker:
			inBlock = true
			continue
		case strings.TrimSpace(l) == endMarker:
			inBlock = false
			continue
		case inBlock:
			continue
		default:
			out = append(out, l)
		}
	}
	return []byte(strings.Join(out, "\n"))
}

// removeForeignRedirects drops a rival mod's Bose redirects from the hosts
// base and returns the dropped lines verbatim (#698). Two layers, so the fix
// does not depend on the rival mod's exact markers:
//
//  1. The marked OpenCloudTouch block ("# OCT-START".."# OCT-END"), markers
//     included. Block mode is only honored when the end marker actually
//     exists, so a truncated block cannot eat the rest of the file; the
//     per-line layer below still neutralizes the redirects themselves.
//  2. Any remaining line that maps a Bose cloud hostname to an address that
//     is neither loopback nor one of our own entries. A rival mod's redirect
//     by definition fights ours, whatever its markers look like.
//
// Every unrelated line is kept: users may have legitimate custom entries
// (NAS names etc.) in there. Field caveat: only one migrated box has been
// measured so far (a SoundTouch 10, issue #698); other OCT versions may
// write slightly different blocks, which is what layer 2 is for.
func removeForeignRedirects(in []byte, own []Entry) ([]byte, []string) {
	lines := strings.Split(string(in), "\n")
	out := make([]string, 0, len(lines))
	var removed []string
	inOCT := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		// Guard: only enter block mode when a closing marker actually follows,
		// otherwise an unterminated block would swallow everything below the
		// opening marker, custom user entries included.
		case t == octBeginMarker && !inOCT && octEndFollows(lines[i+1:]):
			inOCT = true
			removed = append(removed, l)
		case t == octEndMarker && inOCT:
			inOCT = false
			removed = append(removed, l)
		case inOCT:
			removed = append(removed, l)
		case isForeignBoseRedirect(l, own):
			removed = append(removed, l)
		default:
			out = append(out, l)
		}
	}
	return []byte(strings.Join(out, "\n")), removed
}

func octEndFollows(rest []string) bool {
	for _, l := range rest {
		if strings.TrimSpace(l) == octEndMarker {
			return true
		}
	}
	return false
}

// isForeignBoseRedirect reports whether the hosts line points a Bose cloud
// hostname at a foreign address. Loopback stays: that is where STR's own
// listeners sit, so such a line cannot steer the box away from STR even if
// STR did not write it. Unparseable lines stay too; this filter only ever
// drops lines it positively understands.
func isForeignBoseRedirect(line string, own []Entry) bool {
	text := line
	if i := strings.IndexByte(text, '#'); i >= 0 {
		text = text[:i]
	}
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return false
	}
	ip := net.ParseIP(fields[0])
	if ip == nil || ip.IsLoopback() {
		return false
	}
	for _, h := range fields[1:] {
		if !isBoseCloudHost(h) {
			continue
		}
		if isOwnEntry(own, fields[0], h) {
			continue
		}
		return true
	}
	return false
}

func isBoseCloudHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range boseCloudDomains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

func isOwnEntry(own []Entry, ip, host string) bool {
	for _, e := range own {
		if e.IP == ip && strings.EqualFold(e.Host, host) {
			return true
		}
	}
	return false
}

// ContainsOCTBlock reports whether the hosts content carries an
// OpenCloudTouch marker block. Used by the conflicting-mod detection in the
// webui against the LIVE bind-mounted /etc/hosts, never against the verbatim
// boot copy (/tmp/hosts.original): the boot copy keeps the block forever
// because the persistent file on the read-only rootfs cannot be cleaned, so
// keying on it would pin the warning banner even after run.sh and Apply have
// neutralized the redirects (#698).
func ContainsOCTBlock(b []byte) bool {
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == octBeginMarker {
			return true
		}
	}
	return false
}

// foreignFiltered is the package-level snapshot of the lines the last Apply
// dropped, so /api/debug/state can show in a diagnostic bundle exactly which
// rival redirects a box carried and that they were neutralized (#698). One
// small slice, written once per Apply (agent start), read on demand.
var (
	foreignFilteredMu sync.Mutex
	foreignFiltered   []string
)

func setForeignFiltered(lines []string) {
	foreignFilteredMu.Lock()
	defer foreignFilteredMu.Unlock()
	foreignFiltered = append([]string(nil), lines...)
}

// ForeignFiltered returns a copy of the hosts lines the last Apply filtered
// out, empty when the base was clean.
func ForeignFiltered() []string {
	foreignFilteredMu.Lock()
	defer foreignFilteredMu.Unlock()
	return append([]string(nil), foreignFiltered...)
}

// writeAtomic writes content to path. It first tries the classic
// "write to .tmp + rename" strategy. If that fails because the directory
// is read only (e.g. /etc on a ubifs rootfs that only has a tmpfs file
// mounted over it), it falls back to an in-place truncate write. That
// way it works on the Bose box where /etc is ro but /etc/hosts is its
// own tmpfs mount.
func writeAtomic(path string, content []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err == nil {
		if err := os.Rename(tmp, path); err == nil {
			return nil
		} else {
			_ = os.Remove(tmp)
			// Rename failed, falls through to in-place
		}
	}
	// In-place Truncate write. Close error is checked explicitly:
	// on a tmpfs-over-ro-rootfs (the actual deploy target on Bose)
	// a silently-swallowed Close error after a partial write would
	// leave /etc/hosts truncated until the next boot.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(content)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
