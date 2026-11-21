package webui

// Music from a USB stick plugged into the speaker (Ideas #1136, v1).
//
// The agent serves a FAT32 music stick as one more media server: a small,
// lazy UPnP ContentDirectory under :8888/usb/ (internal/usbmedia). It is not
// announced over SSDP; it is listed in /api/library/servers, where the phone
// page and the desktop app find it, and every existing library path (browse,
// play, folder queues, library keys, Recently played) uses it like any NAS.
//
// The rules (docs/streaming/usb-stick.md):
//
//   - Nothing on the stick is ever written. Where the firmware mounted it
//     (/media/sda1 on the ST10, ST20 and ST30) the agent reads from that mount;
//     where nothing did (the Portable never auto-mounts), the agent mounts it
//     itself READ-ONLY at usbMusicMountDir, and only when a user opens the
//     Library, never at boot.
//   - A stick with any STR or Bose marker (usbmedia.InstallMarkers) is an
//     install stick and is never served, music on it or not.
//   - No timer and no poll. The state is looked at when the agent starts
//     (detection only, no mount) and when a client asks: the server list, a
//     browse, a file. Each look is a sysfs glob and one read of /proc/mounts;
//     the stick itself is only read when its state changed.
//   - Each change of state is logged once at INFO. Requests are not logged.
//
// The speaker's own renderer is handed http://127.0.0.1:8888/usb/file/...
// (usbLoopbackURL): on a whitelisted chassis the box cannot reach its own LAN
// address on :17008, because the PREROUTING redirect does not apply to local
// traffic.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/usbmedia"
)

// Seams for the tests. On the speaker they are the real system paths.
var (
	usbMusicSysRoot  = "/sys"
	usbProcRoot      = "/proc"
	usbDevRoot       = "/dev"
	usbMusicMountDir = "/tmp/str-music"
	// usbMountFn mounts dev read-only at dir.
	usbMountFn = mountMusicStickReadOnly
	// usbUnmountFn undoes a mount the agent made itself.
	usbUnmountFn = func(dir string) error { return runQuiet("umount", "-l", dir) }
	// usbOwnIPsFn lists this speaker's own addresses.
	usbOwnIPsFn = ownInterfaceIPs
	// usbLoopbackBase is where the agent reaches its own /usb/ routes.
	usbLoopbackBase = "http://" + boxurl.Authority
)

// usbMusicChassis are the Bose chassis whose USB port hosts a stick at
// runtime: ST10 (rhino), ST20 (spotty), ST30 (mojo) and Portable (taigan).
// Wave, SA-4/SA-5, ST300, CineMate, Lifestyle and the Wireless Link Adapter
// have a setup or service port only (spike, section 1). A speaker this list
// does not name that still sees a USB disk says so instead of serving it.
var usbMusicChassis = map[string]bool{"rhino": true, "spotty": true, "mojo": true, "taigan": true}

// Why a stick is not served. The codes travel to the apps, which show one
// line per code in the user's language.
const (
	usbRefusalUnsupportedModel = "unsupported-model"
	usbRefusalInstallStick     = "install-stick"
	usbRefusalNotFAT32         = "not-fat32"
	usbRefusalBigClusters      = "big-clusters"
	usbRefusalUnreadable       = "unreadable"
)

// usbMusicSnapshot is the stick state, as the debug state and the server list
// see it. A JSON-encoded Go value (webui.RegisterDebugSection).
type usbMusicSnapshot struct {
	Present      bool   `json:"present"`
	Variant      string `json:"variant,omitempty"`
	Device       string `json:"device,omitempty"`
	Filesystem   string `json:"filesystem,omitempty"`
	FATType      string `json:"fat_type,omitempty"`
	ClusterBytes int    `json:"cluster_bytes,omitempty"`
	MountDir     string `json:"mount_dir,omitempty"`
	// MountedBy is "firmware" (Bose udev or the boot script) or "agent" (the
	// read-only mount STR made itself).
	MountedBy    string `json:"mounted_by,omitempty"`
	MountOptions string `json:"mount_options,omitempty"`
	// Classification is "music" or "install" once the stick was looked at.
	Classification string `json:"classification,omitempty"`
	Served         bool   `json:"served"`
	Refusal        string `json:"refusal,omitempty"`
	// WaitingForLibrary is a FAT stick nothing mounted yet: the agent mounts
	// it the first time a user opens the Library.
	WaitingForLibrary bool `json:"waiting_for_library,omitempty"`
	// RootFolders and RootFiles are known once a client listed the top level;
	// the stick is never counted on its own.
	RootListed  bool      `json:"root_listed"`
	RootFolders int       `json:"root_folders,omitempty"`
	RootFiles   int       `json:"root_files,omitempty"`
	Browses     int       `json:"browses"`
	FileReads   int       `json:"file_reads"`
	LastError   string    `json:"last_error,omitempty"`
	CheckedAt   time.Time `json:"checked_at"`
	Checks      int       `json:"checks"`
}

// usbMusicState is the one stick this agent serves, or none.
type usbMusicState struct {
	mu       sync.Mutex
	lib      *usbmedia.Library
	dev      string // "sda1" while served
	dir      string
	ownMount bool
	updateID uint32
	snap     usbMusicSnapshot
	logged   string
}

// USBMusicDebug is the "usb_music" debug-state section.
func (s *Server) USBMusicDebug() any {
	s.usb.mu.Lock()
	defer s.usb.mu.Unlock()
	return s.usb.snap
}

// USBMusicStartupCheck looks at the stick once when the agent starts, so the
// log and the debug state say what is plugged in. It never mounts: at boot the
// install scripts may still be looking at the stick themselves.
func (s *Server) USBMusicStartupCheck() {
	s.usbRefresh(false)
}

// usbRefresh brings the state up to date and returns it. allowMount lets it
// mount a FAT stick nothing mounted yet (a user is opening the Library).
func (s *Server) usbRefresh(allowMount bool) usbMusicSnapshot {
	u := &s.usb
	u.mu.Lock()
	defer u.mu.Unlock()
	prev := u.snap
	snap := usbMusicSnapshot{
		Variant:   usbVariant(),
		CheckedAt: time.Now(),
		Checks:    prev.Checks + 1,
		Browses:   prev.Browses,
		FileReads: prev.FileReads,
	}
	parts := usbmedia.Partitions(usbMusicSysRoot)
	if len(parts) == 0 {
		s.usbDropLocked("the stick was removed")
		s.usbCommitLocked(snap)
		return snap
	}
	snap.Present = true
	// One stick, one partition: the first one. Sticks practically always
	// carry exactly one, and a second partition is not worth the risk of
	// serving half of an install stick.
	part := parts[0]
	snap.Device = part
	if snap.Variant != "" && !usbMusicChassis[snap.Variant] {
		s.usbDropLocked("this speaker cannot host a stick")
		snap.Refusal = usbRefusalUnsupportedModel
		s.usbCommitLocked(snap)
		return snap
	}
	mountsRaw, _ := os.ReadFile(filepath.Join(usbProcRoot, "mounts"))
	mounts := usbmedia.ParseMounts(string(mountsRaw))
	m, mounted := usbmedia.MountOf(mounts, part)

	// Still the stick that is being served, from the same mount: nothing to
	// read again.
	if u.lib != nil && u.dev == part && mounted && m.Dir == u.dir {
		keep := prev
		keep.CheckedAt, keep.Checks, keep.Variant = snap.CheckedAt, snap.Checks, snap.Variant
		u.snap = keep
		return keep
	}
	s.usbDropLocked("the stick changed")

	vol, verr := usbmedia.SniffDevice(filepath.Join(usbDevRoot, part))
	if verr == nil {
		snap.Filesystem, snap.FATType, snap.ClusterBytes = vol.FS, vol.FATType, vol.ClusterBytes
	}
	if mounted {
		snap.MountDir, snap.MountOptions = m.Dir, m.Options
		snap.MountedBy = "firmware"
		if m.Dir == usbMusicMountDir {
			snap.MountedBy = "agent"
		}
		if !usbmedia.FATMountTypes[m.FSType] {
			snap.Filesystem = m.FSType
			snap.Refusal = usbRefusalNotFAT32
			s.usbCommitLocked(snap)
			return snap
		}
		if snap.Filesystem == "" {
			snap.Filesystem = usbmedia.FSFAT
		}
	} else {
		switch {
		case verr != nil:
			snap.Refusal, snap.LastError = usbRefusalUnreadable, verr.Error()
		case vol.FS != usbmedia.FSFAT:
			snap.Refusal = usbRefusalNotFAT32
		}
		if snap.Refusal != "" {
			s.usbCommitLocked(snap)
			return snap
		}
	}
	if verr == nil && vol.ClusterBytes > usbmedia.MaxClusterBytes {
		snap.Refusal = usbRefusalBigClusters
		s.usbCommitLocked(snap)
		return snap
	}

	dir := m.Dir
	own := mounted && m.Dir == usbMusicMountDir
	if !mounted {
		if !allowMount {
			snap.WaitingForLibrary = true
			s.usbCommitLocked(snap)
			return snap
		}
		if err := os.MkdirAll(usbMusicMountDir, 0o755); err != nil {
			snap.Refusal, snap.LastError = usbRefusalUnreadable, err.Error()
			s.usbCommitLocked(snap)
			return snap
		}
		if err := usbMountFn(filepath.Join(usbDevRoot, part), usbMusicMountDir); err != nil {
			snap.Refusal, snap.LastError = usbRefusalUnreadable, err.Error()
			s.usbCommitLocked(snap)
			return snap
		}
		dir, own = usbMusicMountDir, true
		snap.MountDir, snap.MountedBy, snap.MountOptions = dir, "agent", "ro"
	}

	kind, cerr := usbmedia.Classify(dir)
	snap.Classification = kind.String()
	if cerr != nil {
		snap.Classification = ""
		snap.Refusal, snap.LastError = usbRefusalUnreadable, cerr.Error()
	} else if kind == usbmedia.KindInstall {
		snap.Refusal = usbRefusalInstallStick
	}
	if snap.Refusal != "" {
		// An install stick belongs to the boot scripts: a mount STR made is
		// taken away again at once, so nothing of STR's sits on it.
		if own {
			_ = usbUnmountFn(dir)
			snap.MountDir, snap.MountedBy, snap.MountOptions = "", "", ""
		}
		s.usbCommitLocked(snap)
		return snap
	}
	lib, err := usbmedia.Open(dir)
	if err != nil {
		snap.Refusal, snap.LastError = usbRefusalUnreadable, err.Error()
		if own {
			_ = usbUnmountFn(dir)
		}
		s.usbCommitLocked(snap)
		return snap
	}
	u.lib, u.dev, u.dir, u.ownMount = lib, part, dir, own
	u.updateID++
	snap.Served = true
	s.usbCommitLocked(snap)
	return snap
}

// usbDropLocked stops serving, closes the stick and takes away a mount the
// agent made itself (a pulled stick leaves a stale mount behind otherwise).
func (s *Server) usbDropLocked(why string) {
	u := &s.usb
	if u.lib == nil && !u.ownMount {
		return
	}
	if u.lib != nil {
		_ = u.lib.Close()
	}
	if u.ownMount {
		if err := usbUnmountFn(u.dir); err != nil {
			s.logger.Info("usb music: could not take the read-only mount away", "dir", u.dir, "err", err)
		}
	}
	s.logger.Info("usb music: no longer serving the stick", "why", why, "device", u.dev)
	u.lib, u.dev, u.dir, u.ownMount = nil, "", "", false
}

// usbCommitLocked stores snap and logs it once if the state changed.
func (s *Server) usbCommitLocked(snap usbMusicSnapshot) {
	u := &s.usb
	u.snap = snap
	sig := fmt.Sprintf("%v|%s|%s|%s|%s|%v|%s|%v", snap.Present, snap.Device, snap.Filesystem,
		snap.MountDir, snap.Classification, snap.Served, snap.Refusal, snap.WaitingForLibrary)
	if sig == u.logged {
		return
	}
	u.logged = sig
	switch {
	case !snap.Present:
		s.logger.Info("usb music: no USB stick in the speaker")
	case snap.Served:
		s.logger.Info("usb music: serving the music stick", "device", snap.Device, "fs", snap.Filesystem,
			"fat", snap.FATType, "clusterBytes", snap.ClusterBytes, "mount", snap.MountDir,
			"mountedBy", snap.MountedBy, "options", snap.MountOptions)
	case snap.WaitingForLibrary:
		s.logger.Info("usb music: FAT stick found, not mounted yet; mounted read-only when the Library is opened",
			"device", snap.Device, "clusterBytes", snap.ClusterBytes)
	default:
		s.logger.Info("usb music: the stick is not served", "refusal", snap.Refusal, "device", snap.Device,
			"fs", snap.Filesystem, "class", snap.Classification, "mount", snap.MountDir,
			"variant", snap.Variant, "err", snap.LastError)
	}
}

// usbServed returns the served stick, refreshing the state first.
func (s *Server) usbServed(allowMount bool) (*usbmedia.Library, usbmedia.Info, bool) {
	s.usbRefresh(allowMount)
	s.usb.mu.Lock()
	lib, id := s.usb.lib, s.usb.updateID
	s.usb.mu.Unlock()
	if lib == nil {
		return nil, usbmedia.Info{}, false
	}
	return lib, usbmedia.Info{UDN: s.usbUDN(), FriendlyName: s.usbFriendlyName(), UpdateID: id}, true
}

// usbNote records what a client read, for the debug state.
func (s *Server) usbNote(fn func(*usbMusicSnapshot)) {
	s.usb.mu.Lock()
	fn(&s.usb.snap)
	s.usb.mu.Unlock()
}

// usbUDN is the stick's UDN: one per speaker, stable across restarts, so a
// key saved from it, and the desktop app's list, keep finding it.
func (s *Server) usbUDN() string {
	id := ""
	if agentIdentityFn != nil {
		a, b := agentIdentityFn()
		id = a
		if id == "" {
			id = b
		}
	}
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		id = "speaker"
	}
	return "uuid:str-usb-" + id
}

// isUSBUDN reports whether udn names this speaker's stick.
func (s *Server) isUSBUDN(udn string) bool {
	return udn != "" && udnKey(udn) == udnKey(s.usbUDN())
}

// usbFriendlyName is what a generic UPnP client calls the stick. The STR apps
// show their own translated label instead.
func (s *Server) usbFriendlyName() string {
	if s.boxNameFn != nil {
		if name, _ := s.boxNameFn(); strings.TrimSpace(name) != "" {
			return "USB stick (" + strings.TrimSpace(name) + ")"
		}
	}
	return "USB stick"
}

// usbResolve answers resolveMediaServer for the stick's UDN: the agent's own
// ContentDirectory over loopback, so every library path that browses a
// registered server browses the stick unchanged, and every URL it hands out is
// the loopback form the speaker can fetch. isUSB is false for any other UDN.
func (s *Server) usbResolve(udn string) (srv dlna.Server, isUSB, ok bool) {
	if !s.isUSBUDN(udn) {
		return dlna.Server{}, false, false
	}
	_, info, served := s.usbServed(true)
	if !served {
		return dlna.Server{}, true, false
	}
	return dlna.Server{
		UDN:           info.UDN,
		Location:      usbLoopbackBase + usbmedia.DescriptionPath,
		FriendlyName:  info.FriendlyName,
		Address:       strings.TrimPrefix(usbLoopbackBase, "http://"),
		CDSControlURL: usbLoopbackBase + usbmedia.ControlPath,
		CDSDeviceType: "urn:schemas-upnp-org:device:MediaServer:1",
	}, true, true
}

// usbServerEntry is the stick's line in /api/library/servers: a server to
// browse when one is served, or the reason it is not.
type usbServerEntry struct {
	UDN  string `json:"udn,omitempty"`
	Name string `json:"name"`
	// Kind is "usb", so a client can label it in its own language.
	Kind string `json:"kind"`
	// Location is the device description a client browses it through (the
	// desktop app). Built from the Host the client used, so it is reachable
	// from where the client sits (:8888 or :17008).
	Location string `json:"location,omitempty"`
	// Refusal is set instead of UDN when a stick is in but not served.
	Refusal string `json:"refusal,omitempty"`
}

// usbLibraryEntry returns the stick's line for a client that reached the
// agent at host, or false when no stick is in.
func (s *Server) usbLibraryEntry(host string) (usbServerEntry, bool) {
	snap := s.usbRefresh(true)
	if !snap.Present {
		return usbServerEntry{}, false
	}
	e := usbServerEntry{Name: s.usbFriendlyName(), Kind: "usb"}
	if !snap.Served {
		if snap.Refusal == "" {
			return usbServerEntry{}, false
		}
		e.Refusal = snap.Refusal
		return e, true
	}
	e.UDN = s.usbUDN()
	if host != "" {
		e.Location = "http://" + host + usbmedia.DescriptionPath
	}
	return e, true
}

// handleUSB serves everything under /usb/: the device and service
// descriptions, the Browse control endpoint, the files and the covers. LAN
// only. As with any media server, everyone on the LAN can play the music on
// the stick; nothing else on it is reachable (usbmedia, os.Root, audio and
// cover names only).
func (s *Server) handleUSB(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "LAN only", http.StatusForbidden)
		return
	}
	lib, info, ok := s.usbServed(true)
	if !ok {
		if r.URL.Path == usbmedia.ControlPath {
			usbmedia.WriteFault(w, usbmedia.ErrCodeActionFailed, "No music stick in the speaker")
			return
		}
		http.Error(w, "no music stick in this speaker", http.StatusNotFound)
		return
	}
	base := "http://" + r.Host
	switch p := r.URL.Path; {
	case p == usbmedia.DescriptionPath:
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		_, _ = w.Write(usbmedia.DeviceDescription(info))
	case p == usbmedia.SCPDPath:
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		_, _ = w.Write([]byte(usbmedia.SCPD))
	case p == usbmedia.ControlPath:
		s.usbNote(func(sn *usbMusicSnapshot) { sn.Browses++ })
		lib.Control(w, r, base, info)
		s.usbNoteRoot(lib)
	case strings.HasPrefix(p, usbmedia.FilePrefix):
		if r.Method == http.MethodGet {
			s.usbNote(func(sn *usbMusicSnapshot) { sn.FileReads++ })
		}
		lib.ServeFile(w, r, usbmedia.FilePrefix)
	case strings.HasPrefix(p, usbmedia.ArtPrefix):
		lib.ServeFile(w, r, usbmedia.ArtPrefix)
	default:
		http.NotFound(w, r)
	}
}

// usbNoteRoot fills the lazily known root counts the first time a client has
// listed the top level (one more read of that one directory, once per stick).
func (s *Server) usbNoteRoot(lib *usbmedia.Library) {
	s.usb.mu.Lock()
	listed := s.usb.snap.RootListed
	s.usb.mu.Unlock()
	if listed {
		return
	}
	page, _, err := lib.Browse("0", 0, 0)
	if err != nil {
		s.usbNote(func(sn *usbMusicSnapshot) { sn.LastError = err.Error() })
		return
	}
	folders, files := 0, 0
	for _, e := range page {
		if e.Folder {
			folders++
		} else {
			files++
		}
	}
	s.usbNote(func(sn *usbMusicSnapshot) {
		sn.RootListed, sn.RootFolders, sn.RootFiles = true, folders, files
	})
}

// usbLoopbackURL turns a URL that points at THIS speaker's stick through one
// of its LAN addresses (http://<lan ip>:8888/usb/... or :17008/usb/...) into
// the loopback form the speaker's own renderer can fetch. Anything else, the
// stick of another speaker included, is returned unchanged. Applied where a
// URL enters the agent to be played or stored on a key.
func usbLoopbackURL(raw string) string {
	if !strings.HasPrefix(strings.ToLower(raw), "http://") || !strings.Contains(raw, "/usb/") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.HasPrefix(u.Path, "/usb/") {
		return raw
	}
	if u.Host == boxurl.Authority {
		return raw
	}
	switch u.Port() {
	case "8888", "17008":
	default:
		return raw
	}
	if !usbIsOwnHost(u.Hostname()) {
		return raw
	}
	out := usbLoopbackBase + u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

// usbIsOwnHost reports whether host is one of this speaker's own addresses.
func usbIsOwnHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, own := range usbOwnIPsFn() {
		if own.Equal(ip) {
			return true
		}
	}
	return false
}

// isUSBLoopbackFile reports whether raw is a file on this speaker's own stick
// in the loopback form. Only those may be probed over loopback: the probe
// clients refuse loopback for every other URL (SSRF guard), and the stick's
// files are plain audio the agent itself serves.
func isUSBLoopbackFile(raw string) bool {
	return strings.HasPrefix(raw, usbLoopbackBase+usbmedia.FilePrefix)
}

// usbProbeClient is the client a length probe of a stick file uses: plain, no
// SSRF guard, for exactly the URLs isUSBLoopbackFile accepts.
func usbProbeClient() *http.Client { return &http.Client{Timeout: 5 * time.Second} }

func ownInterfaceIPs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			out = append(out, n.IP)
		}
	}
	return out
}

// usbVariant is the chassis codename from /proc/variant, "" off the speaker.
func usbVariant() string {
	b, err := os.ReadFile(filepath.Join(usbProcRoot, "variant"))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(b)))
}

// mountMusicStickReadOnly mounts a FAT stick read-only, with no way to run
// anything from it. UTF-8 file names first; a kernel built without that
// codepage gets the plain mount.
func mountMusicStickReadOnly(dev, dir string) error {
	const base = "ro,nosuid,nodev,noexec"
	if err := runQuiet("mount", "-t", "vfat", "-o", base+",iocharset=utf8", dev, dir); err == nil {
		return nil
	}
	return runQuiet("mount", "-t", "vfat", "-o", base, dev, dir)
}

// runQuiet runs a short system command with a deadline and returns its output
// as the error when it fails.
func runQuiet(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%s: %w: %s", name, err, msg)
	}
	return nil
}

// usbLoopbackPreset applies usbLoopbackURL to everything a preset plays.
func usbLoopbackPreset(p *presets.Preset) {
	p.StreamURL = usbLoopbackURL(p.StreamURL)
	p.Art = usbLoopbackURL(p.Art)
	for i := range p.Items {
		p.Items[i].URL = usbLoopbackURL(p.Items[i].URL)
		p.Items[i].Art = usbLoopbackURL(p.Items[i].Art)
	}
}
