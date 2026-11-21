package webui

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
)

// fakeStick is a speaker's view of one USB stick: sysfs, /proc and /dev in a
// temp tree, the stick's files in a directory, and a mount that only edits the
// fake /proc/mounts.
type fakeStick struct {
	t        *testing.T
	sys      string
	proc     string
	dev      string
	files    string // the stick's content, wherever it is "mounted"
	mu       sync.Mutex
	mounts   []string
	mountOps []string
	// remountErr makes the read-only remount fail; remountNoop makes it
	// "succeed" while the mount stays read-write.
	remountErr  error
	remountNoop bool
}

func newFakeStick(t *testing.T, variant string) *fakeStick {
	t.Helper()
	f := &fakeStick{t: t, sys: t.TempDir(), proc: t.TempDir(), dev: t.TempDir(), files: t.TempDir()}
	saved := []string{usbMusicSysRoot, usbProcRoot, usbDevRoot, usbMusicMountDir}
	savedMount, savedUnmount, savedRemount := usbMountFn, usbUnmountFn, usbRemountROFn
	savedDelays := usbSettleDelays
	t.Cleanup(func() {
		usbMusicSysRoot, usbProcRoot, usbDevRoot, usbMusicMountDir = saved[0], saved[1], saved[2], saved[3]
		usbMountFn, usbUnmountFn, usbRemountROFn = savedMount, savedUnmount, savedRemount
		usbSettleDelays = savedDelays
	})
	usbMusicSysRoot, usbProcRoot, usbDevRoot = f.sys, f.proc, f.dev
	// The agent's own mount point holds the stick's files once "mounted".
	usbMusicMountDir = f.files
	usbMountFn = func(dev, dir string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.mountOps = append(f.mountOps, "mount "+filepath.Base(dev)+" "+dir)
		f.mounts = append(f.mounts, "/dev/"+filepath.Base(dev)+" "+dir+" vfat ro,nosuid,nodev,noexec 0 0")
		f.writeMountsLocked()
		return nil
	}
	usbUnmountFn = func(dir string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.mountOps = append(f.mountOps, "umount "+dir)
		var keep []string
		for _, m := range f.mounts {
			if !strings.Contains(m, " "+dir+" ") {
				keep = append(keep, m)
			}
		}
		f.mounts = keep
		f.writeMountsLocked()
		return nil
	}
	// busybox "mount -o remount,ro dir": the mount's options turn ro.
	usbRemountROFn = func(dir string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.mountOps = append(f.mountOps, "remount-ro "+dir)
		if f.remountErr != nil {
			return f.remountErr
		}
		if f.remountNoop {
			return nil
		}
		for i, m := range f.mounts {
			if strings.Contains(m, " "+dir+" ") {
				f.mounts[i] = strings.Replace(m, " rw,", " ro,", 1)
			}
		}
		f.writeMountsLocked()
		return nil
	}
	if variant != "" {
		if err := os.WriteFile(filepath.Join(f.proc, "variant"), []byte(variant+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.writeMountsLocked()
	return f
}

func (f *fakeStick) writeMountsLocked() {
	body := "rootfs / rootfs rw 0 0\n" + strings.Join(f.mounts, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(f.proc, "mounts"), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// plug inserts the stick: sda with partition sda1, and its boot sector.
func (f *fakeStick) plug(sector []byte) {
	if err := os.MkdirAll(filepath.Join(f.sys, "block", "sda", "sda1"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dev, "sda1"), sector, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// unplug pulls it. A mount the firmware made stays listed, as it does.
func (f *fakeStick) unplug() {
	if err := os.RemoveAll(filepath.Join(f.sys, "block", "sda")); err != nil {
		f.t.Fatal(err)
	}
}

// firmwareMount lists the stick as mounted by Bose udev at dir.
func (f *fakeStick) firmwareMount(dir, fstype string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mounts = append(f.mounts, "/dev/sda1 "+dir+" "+fstype+" rw,relatime,iocharset=utf8,errors=remount-ro 0 0")
	f.writeMountsLocked()
}

func (f *fakeStick) ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.mountOps...)
}

// remounts counts the read-only remounts asked for.
func (f *fakeStick) remounts() int {
	n := 0
	for _, op := range f.ops() {
		if strings.HasPrefix(op, "remount-ro ") {
			n++
		}
	}
	return n
}

func (f *fakeStick) put(root string, rel ...string) {
	for _, r := range rel {
		p := filepath.Join(root, filepath.FromSlash(r))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
}

func fatSector(kind string, sectorsPerCluster byte) []byte {
	b := make([]byte, 512)
	switch kind {
	case "fat32":
		copy(b[82:], "FAT32   ")
	case "exfat":
		copy(b[3:], "EXFAT   ")
	case "ntfs":
		copy(b[3:], "NTFS    ")
	}
	binary.LittleEndian.PutUint16(b[11:], 512)
	b[13] = sectorsPerCluster
	b[510], b[511] = 0x55, 0xAA
	return b
}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func usbTestServer(t *testing.T) (*Server, *logBuf) {
	t.Helper()
	prev := agentIdentityFn
	t.Cleanup(func() { agentIdentityFn = prev })
	SetAgentIdentityFn(func() (string, string) { return "AABBCCDDEEFF", "" })
	store, err := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
	if err != nil {
		t.Fatal(err)
	}
	lb := &logBuf{}
	s := &Server{logger: slog.New(slog.NewTextHandler(lb, nil)), mediaServers: store}
	s.boxNameFn = func() (string, string) { return "Kitchen", "SoundTouch 10" }
	// Close the stick before the temp dirs go: Windows cannot remove an open one.
	t.Cleanup(func() {
		s.usb.mu.Lock()
		s.usbDropLocked("test end")
		s.usb.mu.Unlock()
	})
	return s, lb
}

type serverRow struct {
	UDN      string `json:"udn"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Location string `json:"location"`
	Refusal  string `json:"refusal"`
}

func listServers(t *testing.T, s *Server) []serverRow {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/library/servers", nil)
	req.Host = "192.0.2.10:17008"
	s.handleLibraryServers(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var out []serverRow
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func usbRow(rows []serverRow) (serverRow, bool) {
	for _, r := range rows {
		if r.Kind == "usb" {
			return r, true
		}
	}
	return serverRow{}, false
}

// ST10/ST20/ST30: Bose udev mounted the stick, so the agent reads from that
// mount and never mounts anything itself. It switches that read-write mount
// to read-only, once, and never unmounts it.
func TestFirmwareMountedMusicStickIsListedAndRemountedReadOnly(t *testing.T) {
	f := newFakeStick(t, "rhino")
	media := t.TempDir()
	f.put(media, "Album/01 a.mp3", "b.flac")
	f.plug(fatSector("fat32", 64))
	f.firmwareMount(media, "vfat")
	s, logs := usbTestServer(t)

	row, ok := usbRow(listServers(t, s))
	if !ok || row.UDN != "uuid:str-usb-aabbccddeeff" || row.Refusal != "" ||
		row.Location != "http://192.0.2.10:17008/usb/device.xml" || row.Name != "USB stick (Kitchen)" {
		t.Fatalf("usb row: %+v %v", row, ok)
	}
	if ops := f.ops(); len(ops) != 1 || ops[0] != "remount-ro "+media {
		t.Fatalf("a firmware mount is only switched to read-only in place, got %v", ops)
	}
	snap := s.USBMusicDebug().(usbMusicSnapshot)
	if !snap.Served || snap.MountedBy != "firmware" || snap.Classification != "music" || snap.FATType != "FAT32" ||
		snap.ClusterBytes != 32<<10 || snap.Variant != "rhino" || !snap.RemountedRO || snap.RemountError != "" ||
		!strings.HasPrefix(snap.MountOptions, "ro,") || snap.MountDir != media {
		t.Fatalf("snapshot: %+v", snap)
	}
	// A second look changes nothing, remounts nothing and logs nothing new.
	listServers(t, s)
	if n := strings.Count(logs.String(), "usb music: serving the music stick"); n != 1 {
		t.Fatalf("the serving decision must be logged once, got %d:\n%s", n, logs.String())
	}
	if n := strings.Count(logs.String(), "switched the firmware's mount of the music stick to read-only"); n != 1 || f.remounts() != 1 {
		t.Fatalf("the remount must happen and be logged once, got %d remounts, %d lines:\n%s", f.remounts(), n, logs.String())
	}
	if _, err := json.Marshal(s.USBMusicDebug()); err != nil {
		t.Fatalf("debug section must encode: %v", err)
	}
}

// Portable: nothing mounts the stick. The agent start only detects it; the
// read-only mount happens when the Library is opened, once.
func TestPortableStickIsMountedReadOnlyOnlyWhenTheLibraryOpens(t *testing.T) {
	f := newFakeStick(t, "taigan")
	f.put(f.files, "song.mp3")
	f.plug(fatSector("fat32", 64))
	s, _ := usbTestServer(t)

	s.USBMusicStartupCheck()
	if ops := f.ops(); len(ops) != 0 {
		t.Fatalf("nothing may be mounted at agent start, got %v", ops)
	}
	if snap := s.USBMusicDebug().(usbMusicSnapshot); !snap.WaitingForLibrary || snap.Served {
		t.Fatalf("startup snapshot: %+v", snap)
	}
	if _, ok := usbRow(listServers(t, s)); !ok {
		t.Fatal("the stick must be listed once the Library opened")
	}
	listServers(t, s)
	if ops := f.ops(); len(ops) != 1 || ops[0] != "mount sda1 "+f.files {
		t.Fatalf("want exactly one mount, got %v", ops)
	}
	snap := s.USBMusicDebug().(usbMusicSnapshot)
	if !snap.Served || snap.MountedBy != "agent" || !strings.Contains(snap.MountOptions, "ro") {
		t.Fatalf("snapshot: %+v", snap)
	}

	// Pulled: the agent's own mount is taken away and the entry is gone.
	f.unplug()
	if _, ok := usbRow(listServers(t, s)); ok {
		t.Fatal("a pulled stick must not be listed")
	}
	if ops := f.ops(); len(ops) != 2 || ops[1] != "umount "+f.files {
		t.Fatalf("want the own mount taken away, got %v", ops)
	}
}

func TestSticksThatAreNotServedSayWhy(t *testing.T) {
	cases := []struct {
		name    string
		variant string
		sector  []byte
		mountFS string // "" = not mounted
		files   []string
		want    string
		mounts  int // mount calls expected
	}{
		{"install stick (firmware mount)", "mojo", fatSector("fat32", 64), "vfat", []string{"run.sh", "Music/a.mp3"}, usbRefusalInstallStick, 0},
		{"install stick (agent mount is taken back)", "taigan", fatSector("fat32", 64), "", []string{"WLAN.PASS", "a.mp3"}, usbRefusalInstallStick, 1},
		{"Bose firmware stick", "rhino", fatSector("fat32", 64), "vfat", []string{"Update.stu"}, usbRefusalInstallStick, 0},
		{"exFAT, not mounted", "rhino", fatSector("exfat", 0), "", nil, usbRefusalNotFAT32, 0},
		{"NTFS, not mounted", "spotty", fatSector("ntfs", 8), "", nil, usbRefusalNotFAT32, 0},
		{"mounted, but not as FAT", "rhino", fatSector("exfat", 0), "exfat", nil, usbRefusalNotFAT32, 0},
		{"64 KB clusters", "rhino", fatSector("fat32", 128), "", nil, usbRefusalBigClusters, 0},
		{"no readable boot sector", "rhino", []byte{1, 2}, "", nil, usbRefusalUnreadable, 0},
		{"a Wave", "lisa", fatSector("fat32", 64), "", []string{"a.mp3"}, usbRefusalUnsupportedModel, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeStick(t, c.variant)
			f.plug(c.sector)
			dir := f.files
			if c.mountFS != "" {
				dir = t.TempDir()
				f.firmwareMount(dir, c.mountFS)
			}
			f.put(dir, c.files...)
			s, _ := usbTestServer(t)
			row, ok := usbRow(listServers(t, s))
			if !ok || row.Refusal != c.want || row.UDN != "" || row.Location != "" {
				t.Fatalf("got %+v %v, want refusal %s", row, ok, c.want)
			}
			mounts := 0
			for _, op := range f.ops() {
				if strings.HasPrefix(op, "mount ") {
					mounts++
				}
			}
			if mounts != c.mounts {
				t.Fatalf("mount calls %d, want %d (%v)", mounts, c.mounts, f.ops())
			}
			if c.mounts > 0 {
				if ops := f.ops(); ops[len(ops)-1] != "umount "+f.files {
					t.Fatalf("an install stick must not stay mounted by STR: %v", ops)
				}
			}
			// Only a music stick is switched to read-only: an install or
			// recovery stick keeps its read-write mount for STR's scripts.
			if n := f.remounts(); n != 0 {
				t.Fatalf("a refused stick must never be remounted, got %v", f.ops())
			}
			if snap := s.USBMusicDebug().(usbMusicSnapshot); snap.RemountedRO {
				t.Fatalf("snapshot: %+v", snap)
			}
			// Nothing about it is browsable.
			if _, _, ok := s.usbServed(true); ok {
				t.Fatal("a refused stick must not be served")
			}
		})
	}
}

func TestNoStickNoEntry(t *testing.T) {
	newFakeStick(t, "rhino")
	s, _ := usbTestServer(t)
	if _, ok := usbRow(listServers(t, s)); ok {
		t.Fatal("no stick, no entry")
	}
	if snap := s.USBMusicDebug().(usbMusicSnapshot); snap.Present {
		t.Fatalf("snapshot: %+v", snap)
	}
}

// startUSBAgent serves the agent's /usb/ routes on a test listener and makes it
// the loopback address the agent browses itself on.
func startUSBAgent(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/usb/", s.handleUSB)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	prev := usbLoopbackBase
	usbLoopbackBase = srv.URL
	t.Cleanup(func() { usbLoopbackBase = prev })
	return srv
}

func TestTheStickBrowsesThroughTheAgentLikeAnyMediaServer(t *testing.T) {
	f := newFakeStick(t, "rhino")
	media := t.TempDir()
	f.put(media, "Album 1/01 Ärger.mp3", "Album 1/02 b.flac", "Album 1/cover.jpg", "Album 1/notes.txt", "loose.ogg")
	f.plug(fatSector("fat32", 64))
	f.firmwareMount(media, "vfat")
	s, _ := usbTestServer(t)
	srv := startUSBAgent(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The desktop app's view: the description at the address it reached.
	ds, err := dlna.DescribeServer(ctx, srv.URL+"/usb/device.xml")
	if err != nil {
		t.Fatal(err)
	}
	if ds.UDN != s.usbUDN() || ds.FriendlyName != "USB stick (Kitchen)" {
		t.Fatalf("description: %+v", ds)
	}
	res, err := dlna.Browse(ctx, ds, "Album 1", 0, 200)
	if err != nil || len(res.Items) != 2 || !strings.HasPrefix(res.Items[0].StreamURL, srv.URL+"/usb/file/Album%201/") {
		t.Fatalf("browse: %+v %v", res, err)
	}
	resp, err := http.Get(res.Items[0].StreamURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("file: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// The phone page's view: /api/library/browse resolves the stick to the
	// agent's own loopback, no discovery.
	if !s.mediaServerRegistered(s.usbUDN()) || s.registeredName(udnKey(s.usbUDN())) != "USB stick (Kitchen)" {
		t.Fatal("the stick needs no registration")
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/library/browse?udn="+s.usbUDN()+"&id=Album%201", nil)
	req.Host = "192.0.2.10:8888"
	s.handleLibraryBrowse(rr, req)
	var page struct {
		Offline bool `json:"offline"`
		Tracks  []struct {
			Title, URL, Art, Mime string
		} `json:"tracks"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil || page.Offline || len(page.Tracks) != 2 {
		t.Fatalf("phone browse: %s", rr.Body.String())
	}
	tr := page.Tracks[0]
	if tr.URL != srv.URL+"/usb/file/Album%201/01%20%C3%84rger.mp3" || tr.Mime != "audio/mpeg" ||
		tr.Art != "http://192.0.2.10:8888/usb/art/Album%201/cover.jpg" {
		t.Fatalf("phone track: %+v", tr)
	}
	snap := s.USBMusicDebug().(usbMusicSnapshot)
	if !snap.RootListed && snap.Browses == 0 {
		t.Fatalf("browses must be counted: %+v", snap)
	}

	// Pulled mid-session: the browse answers offline instead of failing.
	f.unplug()
	rr = httptest.NewRecorder()
	s.handleLibraryBrowse(rr, httptest.NewRequest("GET", "/api/library/browse?udn="+s.usbUDN(), nil))
	if !strings.Contains(rr.Body.String(), `"offline":true`) {
		t.Fatalf("a pulled stick browses offline: %s", rr.Body.String())
	}
	resp, err = http.Get(srv.URL + "/usb/device.xml")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("no stick, no description: %d", resp.StatusCode)
	}
}

func TestUSBRoutesAreLANOnly(t *testing.T) {
	s, _ := usbTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/usb/device.xml", nil)
	req.RemoteAddr = "203.0.113.9:4444"
	s.handleUSB(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("a request from outside the LAN must be refused, got %d", rr.Code)
	}
}

func TestOwnStickURLsAreFetchedOverLoopback(t *testing.T) {
	prev := usbOwnIPsFn
	usbOwnIPsFn = func() []net.IP { return []net.IP{net.ParseIP("192.0.2.10")} }
	t.Cleanup(func() { usbOwnIPsFn = prev })
	cases := map[string]string{
		// This speaker on its whitelisted-chassis port and on :8888.
		"http://192.0.2.10:17008/usb/file/Album%201/a.flac": "http://127.0.0.1:8888/usb/file/Album%201/a.flac",
		"http://192.0.2.10:8888/usb/art/cover.jpg":          "http://127.0.0.1:8888/usb/art/cover.jpg",
		// Already loopback.
		"http://127.0.0.1:8888/usb/file/a.mp3": "http://127.0.0.1:8888/usb/file/a.mp3",
		// Another speaker's stick: that speaker serves it.
		"http://192.0.2.11:8888/usb/file/a.mp3": "http://192.0.2.11:8888/usb/file/a.mp3",
		// Not the agent's port, not the stick, not http.
		"http://192.0.2.10:8200/usb/file/a.mp3": "http://192.0.2.10:8200/usb/file/a.mp3",
		"http://192.0.2.10:8888/stream/1":       "http://192.0.2.10:8888/stream/1",
		"https://192.0.2.10:8888/usb/file/a":    "https://192.0.2.10:8888/usb/file/a",
		"":                                      "",
	}
	for in, want := range cases {
		if got := usbLoopbackURL(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	p := presets.Preset{Type: "queue", Items: []presets.PresetItem{{URL: "http://192.0.2.10:17008/usb/file/a.mp3", Art: "http://192.0.2.10:17008/usb/art/cover.jpg"}}}
	usbLoopbackPreset(&p)
	if p.Items[0].URL != "http://127.0.0.1:8888/usb/file/a.mp3" || p.Items[0].Art != "http://127.0.0.1:8888/usb/art/cover.jpg" {
		t.Fatalf("folder key: %+v", p.Items[0])
	}
	if !isUSBLoopbackFile("http://127.0.0.1:8888/usb/file/a.mp3") || isUSBLoopbackFile("http://127.0.0.1:8888/stream/1") {
		t.Fatal("only stick files may be probed over loopback")
	}
	if items := toQueueItems([]queueStartItem{{URL: "http://192.0.2.10:8888/usb/file/a.mp3", Mime: "audio/mpeg"}}); items[0].URL != "http://127.0.0.1:8888/usb/file/a.mp3" {
		t.Fatalf("queue item: %+v", items[0])
	}
}

// A stick file played through POST /api/play without a MIME went the radio
// way: through the stream proxy, whose SSRF guard refuses the speaker's own
// address ("stream target ... blocked (loopback/link-local"), and the speaker
// answered AUDIO_ERROR_BAD_URL. A stick file is always played directly, with
// the MIME of its extension, whatever the caller sent.
func TestAStickFileIsPlayedDirectlyWithOrWithoutAMime(t *testing.T) {
	prev := usbOwnIPsFn
	usbOwnIPsFn = func() []net.IP { return []net.IP{net.ParseIP("192.0.2.1")} }
	t.Cleanup(func() { usbOwnIPsFn = prev })
	const lan = "http://192.0.2.1:17008/usb/file/Album%201/02%20b.flac"
	const loop = "http://127.0.0.1:8888/usb/file/Album%201/02%20b.flac"
	for name, body := range map[string]string{
		"no mime":    `{"url":"` + lan + `","title":"02 b"}`,
		"wrong mime": `{"url":"` + lan + `","title":"02 b","mime":"audio/mpeg"}`,
		"loopback":   `{"url":"` + loop + `","title":"02 b"}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newPlayTestServer(t)
			w := postPlayBody(t, s, body)
			if w.Code != http.StatusOK {
				t.Fatalf("play: %d %s", w.Code, w.Body.String())
			}
			tr := activeFileTrack(t, s)
			if tr.boxURL != loop || tr.mime != "audio/flac" {
				t.Fatalf("the speaker was handed %q as %q, want the stick file directly as audio/flac", tr.boxURL, tr.mime)
			}
		})
	}

	// The same for a folder queue and a key.
	items := toQueueItems([]queueStartItem{{URL: lan}, {URL: "http://192.0.2.50:8000/radio"}})
	if items[0].Mime != "audio/flac" || !queuePlaysDirect(items[0].URL, items[0].Mime) || items[1].Mime != "" {
		t.Fatalf("queue items: %+v", items)
	}
	if q := presetItemsToQueue([]presets.PresetItem{{URL: loop}}); q[0].Mime != "audio/flac" {
		t.Fatalf("folder key item: %+v", q[0])
	}
	s, _ := newPlayTestServer(t)
	if m := s.directFileMime(context.Background(), presets.Preset{Slot: 1, StreamURL: loop}); m != "audio/flac" {
		t.Fatalf("a key on a stick file: mime %q", m)
	}
	// Only this speaker's stick: another speaker's is not touched, and a stick
	// URL that names no playable file keeps what the caller sent.
	for _, c := range []struct{ url, mime, want string }{
		{"http://192.0.2.11:8888/usb/file/a.mp3", "", ""},
		{"http://127.0.0.1:8888/usb/art/cover.jpg", "", ""},
		{"http://127.0.0.1:8888/usb/file/notes.txt", "audio/mpeg", "audio/mpeg"},
		{"http://127.0.0.1:8888/usb/file/B%FCcher/01%20%C4rger.mp3", "", "audio/mpeg"},
	} {
		if got := usbFileMIME(usbLoopbackURL(c.url), c.mime); got != c.want {
			t.Errorf("%s: got %q, want %q", c.url, got, c.want)
		}
	}
}

// The firmware mounts the stick vfat with iocharset=iso8859-1 (ST10 and
// Portable, live), so "01 Ärger.mp3" arrives as the Latin-1 byte 0xC4. The
// listing shows the umlaut, and the URL keeps the bytes the speaker fetches.
func TestALatin1FirmwareMountShowsUmlautsAndPlays(t *testing.T) {
	f := newFakeStick(t, "taigan")
	media := t.TempDir()
	album := filepath.Join(media, "Album 1")
	if err := os.Mkdir(album, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(album, "01 \xC4rger.mp3"), []byte("x"), 0o644); err != nil {
		t.Skipf("this filesystem cannot hold a Latin-1 name: %v", err)
	}
	if des, err := os.ReadDir(album); err != nil || len(des) != 1 || des[0].Name() != "01 \xC4rger.mp3" {
		t.Skip("this filesystem does not keep a Latin-1 name byte for byte; Linux CI runs this")
	}
	f.plug(fatSector("fat32", 64))
	f.mu.Lock()
	f.mounts = append(f.mounts, "/dev/sda1 "+media+" vfat rw,relatime,fmask=0022,dmask=0022,codepage=437,iocharset=iso8859-1,shortname=mixed,errors=remount-ro 0 0")
	f.writeMountsLocked()
	f.mu.Unlock()
	s, _ := usbTestServer(t)
	srv := startUSBAgent(t, s)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/library/browse?udn="+s.usbUDN()+"&id=Album%201", nil)
	req.Host = "192.0.2.10:8888"
	s.handleLibraryBrowse(rr, req)
	var page struct {
		Tracks []struct{ Title, URL, Mime string } `json:"tracks"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil || len(page.Tracks) != 1 {
		t.Fatalf("browse: %s", rr.Body.String())
	}
	tr := page.Tracks[0]
	if tr.Title != "01 Ärger" || tr.URL != srv.URL+"/usb/file/Album%201/01%20%C4rger.mp3" || tr.Mime != "audio/mpeg" {
		t.Fatalf("track: %+v", tr)
	}
	resp, err := http.Get(tr.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the speaker could not fetch the file: %d", resp.StatusCode)
	}
	if snap := s.USBMusicDebug().(usbMusicSnapshot); snap.NameCharset != "iso8859-1" {
		t.Fatalf("snapshot: %+v", snap)
	}
}

// The stick's name carries the speaker's name as UTF-8, byte for byte, in the
// server list and in the device description ("Küche", not "KÃ¼che").
func TestTheStickNameKeepsANonASCIISpeakerName(t *testing.T) {
	f := newFakeStick(t, "rhino")
	media := t.TempDir()
	f.put(media, "a.mp3")
	f.plug(fatSector("fat32", 64))
	f.firmwareMount(media, "vfat")
	s, _ := usbTestServer(t)
	s.boxNameFn = func() (string, string) { return "Küche", "SoundTouch 10" }

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/library/servers", nil)
	req.Host = "192.0.2.10:8888"
	s.handleLibraryServers(rr, req)
	if body := rr.Body.Bytes(); !bytes.Contains(body, []byte("USB stick (K\xc3\xbcche)")) || bytes.Contains(body, []byte("\xc3\x83")) {
		t.Fatalf("server list: %q", body)
	}
	srv := startUSBAgent(t, s)
	resp, err := http.Get(srv.URL + "/usb/device.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if !bytes.Contains(buf.Bytes(), []byte("<friendlyName>USB stick (K\xc3\xbcche)</friendlyName>")) {
		t.Fatalf("description: %q", buf.String())
	}
	ds, err := dlna.DescribeServer(context.Background(), srv.URL+"/usb/device.xml")
	if err != nil || ds.FriendlyName != "USB stick (Küche)" {
		t.Fatalf("described as %q, %v", ds.FriendlyName, err)
	}
}

func TestUSBUDNIsStablePerSpeaker(t *testing.T) {
	s, _ := usbTestServer(t)
	if s.usbUDN() != "uuid:str-usb-aabbccddeeff" || !s.isUSBUDN("uuid:STR-USB-AABBCCDDEEFF") || s.isUSBUDN("uuid:other") {
		t.Fatalf("udn: %s", s.usbUDN())
	}
	if _, isUSB, ok := s.usbResolve("uuid:other"); isUSB || ok {
		t.Fatal("another UDN is not the stick")
	}
	newFakeStick(t, "rhino")
	if _, isUSB, ok := s.usbResolve(s.usbUDN()); !isUSB || ok {
		t.Fatal("the stick's UDN without a stick resolves to nothing, without discovery")
	}
}

// The phone page lists the stick in its own language and says why one is not
// offered: every phone locale carries every string, and the server list wires
// the stick entry.
func TestPhoneRemoteShowsTheStick(t *testing.T) {
	bundles := strings.Count(indexHTML, "now:\"")
	start := strings.Index(indexHTML, "var I18N6 = {")
	if bundles == 0 || start < 0 {
		t.Fatal("the USB stick dictionary I18N6 is missing")
	}
	dict := indexHTML[start : start+strings.Index(indexHTML[start:], "\n};")]
	for _, key := range []string{"usbName", "usbUnsupportedModel", "usbInstallStick", "usbNotFAT32", "usbBigClusters", "usbUnreadable"} {
		got := len(regexp.MustCompile(`[,{]`+key+`:"[^"]+"`).FindAllString(dict, -1))
		if got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
	if strings.ContainsAny(dict, "–—") {
		t.Error("the USB stick strings use an en or em dash")
	}
	de := regexp.MustCompile(`(?m)^  de:\{.*$`).FindString(dict)
	if de == "" || strings.Contains(de, " Sie ") || strings.Contains(de, "Ihr") || !strings.Contains(de, "für") {
		t.Error("the German USB stick strings must use du and real umlauts")
	}
	for _, want := range []string{
		"var base6 = I18N6.en, sel6 = I18N6[code] || base6;",
		"s.kind === 'usb'",
		"usbRefusalText(s.refusal)",
		`id="browseUsbMsg"`,
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("phone page lacks %q", want)
		}
	}
}
