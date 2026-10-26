package main

// Firmware ladder tests. They never touch real firmware or a real drive: the
// "firmware" is a few hundred KB of fake bytes in a zip built here, the archive
// is an httptest server, and the stick is a temp folder.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- test of the archive sha1 check
	"encoding/hex"
	"errors"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/sticksetup"
	"streborn-app/bosefw"
)

func fakeStu(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*13 + i/97)
	}
	return b
}

// fakeZip builds a zip with the given entries (name -> content).
func fakeZip(t *testing.T, entries map[string][]byte, order ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range order {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(entries[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha1hex(b []byte) string {
	s := sha1.Sum(b) // #nosec G401 -- test
	return hex.EncodeToString(s[:])
}

// ladderEnv points the archive prefix at a test server and the cache at a
// temp folder, and returns a step for the given zip.
func ladderEnv(t *testing.T, zipBytes, inner []byte) (bosefw.Step, *httptest.Server, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/item/fake.zip" {
			http.NotFound(w, r)
			return
		}
		hits++
		_, _ = w.Write(zipBytes)
	}))
	t.Cleanup(srv.Close)
	oldPrefix, oldCache := fwArchivePrefix, fwCacheDir
	dir := t.TempDir()
	fwArchivePrefix = srv.URL + "/item/"
	fwCacheDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { fwArchivePrefix, fwCacheDir = oldPrefix, oldCache })
	step := bosefw.Step{
		ID: "test-01.00.00", Version: "01.00.00", Family: "test", Source: bosefw.SourceArchive,
		URL: srv.URL + "/item/fake.zip", FileName: "fake.zip",
		ZipSize: int64(len(zipBytes)), ZipSHA1: sha1hex(zipBytes),
		InnerSize: int64(len(inner)), InnerCRC32: crc32.ChecksumIEEE(inner), InnerSHA1: sha1hex(inner),
	}
	return step, srv, &hits
}

func TestArchiveURLAllowed(t *testing.T) {
	base := bosefw.ArchiveItemBase
	cases := map[string]bool{
		base + "Firmware/2013-2015_No_Bluetooth/Airplay-1_ST20-30-Portable/Airplay-1_ST20-30-Portable_07.00.37.zip": true,
		"https://example.com/download/bose-soundtouch-software-and-firmware/x.zip":                                  false,
		"https://archive.org/download/some-other-item/x.zip":                                                        false,
		"http://archive.org/download/bose-soundtouch-software-and-firmware/x.zip":                                   false,
		base + "../other-item/x.zip": false,
		base + "x.zip?redirect=evil": false,
		base:                         false,
	}
	for u, want := range cases {
		if got := archiveURLAllowed(u); got != want {
			t.Errorf("archiveURLAllowed(%q) = %v", u, got)
		}
	}
	for u, want := range map[string]bool{
		"https://ia800100.us.archive.org/7/items/x/y.zip": true,
		"https://archive.org/download/x":                  true,
		"http://ia800100.us.archive.org/x":                false,
		"https://archive.org.evil.example/x":              false,
		"https://evilarchive.org/x":                       false,
	} {
		pu, _ := url.Parse(u)
		if got := fwArchiveRedirectOK(pu); got != want {
			t.Errorf("redirect to %q allowed = %v", u, got)
		}
	}
}

func TestFetchArchiveZipChecksAndCaches(t *testing.T) {
	inner := fakeStu(100_000)
	z := fakeZip(t, map[string][]byte{"Airplay 1 [ST20-30-Portable] 6.2.1/Update.stu": inner}, "Airplay 1 [ST20-30-Portable] 6.2.1/Update.stu")
	step, _, hits := ladderEnv(t, z, inner)
	a := &App{}
	p, err := a.fetchArchiveZip(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); !bytes.Equal(got, z) {
		t.Fatal("cached zip differs")
	}
	if _, err := a.fetchArchiveZip(context.Background(), step); err != nil || *hits != 1 {
		t.Fatalf("second fetch should use the cache: err %v, hits %d", err, *hits)
	}
}

func TestFetchArchiveZipRejectsWrongSHA1(t *testing.T) {
	inner := fakeStu(1000)
	z := fakeZip(t, map[string][]byte{"Update.stu": inner}, "Update.stu")
	step, _, _ := ladderEnv(t, z, inner)
	step.ZipSHA1 = strings.Repeat("0", 40)
	_, err := (&App{}).fetchArchiveZip(context.Background(), step)
	if !errors.Is(err, errFingerprint) {
		t.Fatalf("want a fingerprint refusal, got %v", err)
	}
	dir, _ := fwArchiveCacheDir()
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Fatalf("a bad download must not stay in the cache: %v", left)
	}
}

func TestFetchArchiveZipRefusesForeignLinks(t *testing.T) {
	a := &App{}
	for _, u := range []string{"https://example.com/x.zip", "https://archive.org/download/other-item/x.zip"} {
		_, err := a.fetchArchiveZip(context.Background(), bosefw.Step{Source: bosefw.SourceArchive, URL: u, FileName: "x.zip", ZipSize: 1, ZipSHA1: strings.Repeat("0", 40)})
		if err == nil {
			t.Errorf("%s must be refused", u)
		}
	}
	// Bose's own file never goes through the archive fetcher.
	if _, err := a.fetchArchiveZip(context.Background(), bosefw.Step{Source: bosefw.SourceBose, URL: bosefw.ArchiveItemBase + "x.zip", FileName: "x.zip"}); err == nil {
		t.Error("a Bose step must be refused here")
	}
}

func TestFetchArchiveZipRedirects(t *testing.T) {
	inner := fakeStu(1000)
	z := fakeZip(t, map[string][]byte{"Update.stu": inner}, "Update.stu")
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(z) }))
	defer storage.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, storage.URL+"/7/items/fake.zip", http.StatusFound)
	}))
	defer front.Close()
	oldPrefix, oldCache, oldOK := fwArchivePrefix, fwCacheDir, fwArchiveRedirectOK
	defer func() { fwArchivePrefix, fwCacheDir, fwArchiveRedirectOK = oldPrefix, oldCache, oldOK }()
	dir := t.TempDir()
	fwArchivePrefix = front.URL + "/item/"
	fwCacheDir = func() (string, error) { return dir, nil }
	step := bosefw.Step{ID: "t", Source: bosefw.SourceArchive, URL: front.URL + "/item/fake.zip", FileName: "fake.zip", ZipSize: int64(len(z)), ZipSHA1: sha1hex(z)}

	storageHost := strings.TrimPrefix(storage.URL, "http://")
	fwArchiveRedirectOK = func(u *url.URL) bool { return u.Host == storageHost }
	if _, err := (&App{}).fetchArchiveZip(context.Background(), step); err != nil {
		t.Fatalf("a redirect to the archive's storage must work: %v", err)
	}
	_ = os.RemoveAll(dir)
	fwArchiveRedirectOK = func(*url.URL) bool { return false }
	_, err := (&App{}).fetchArchiveZip(context.Background(), step)
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("a redirect to another host must be refused, got %v", err)
	}
}

func TestExtractUpdateStu(t *testing.T) {
	inner := fakeStu(50_000)
	crc := crc32.ChecksumIEEE(inner)
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ctx := context.Background()

	nested := write("nested.zip", fakeZip(t, map[string][]byte{"a/b/readme.txt": []byte("hi"), "AirPlay [ST] (not wst) 14.0.33/Update.stu": inner},
		"a/b/readme.txt", "AirPlay [ST] (not wst) 14.0.33/Update.stu"))
	out := filepath.Join(dir, "out.stu")
	var last int64
	if err := extractUpdateStu(ctx, nested, out, int64(len(inner)), crc, sha1hex(inner), func(n int64) { last = n }); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, inner) || last != int64(len(inner)) {
		t.Fatal("extracted file differs")
	}

	two := write("two.zip", fakeZip(t, map[string][]byte{"x/Update.stu": inner, "y/Update.stu": inner}, "x/Update.stu", "y/Update.stu"))
	if err := extractUpdateStu(ctx, two, filepath.Join(dir, "two.stu"), int64(len(inner)), crc, "", nil); !errors.Is(err, errFingerprint) {
		t.Errorf("two Update.stu must be refused, got %v", err)
	}
	none := write("none.zip", fakeZip(t, map[string][]byte{"Update.bin": inner}, "Update.bin"))
	if err := extractUpdateStu(ctx, none, filepath.Join(dir, "none.stu"), int64(len(inner)), crc, "", nil); !errors.Is(err, errFingerprint) {
		t.Errorf("no Update.stu must be refused, got %v", err)
	}
	if err := extractUpdateStu(ctx, nested, filepath.Join(dir, "crc.stu"), int64(len(inner)), crc^1, "", nil); !errors.Is(err, errFingerprint) {
		t.Errorf("a CRC mismatch must be refused, got %v", err)
	}
	if err := extractUpdateStu(ctx, nested, filepath.Join(dir, "sha.stu"), int64(len(inner)), crc, strings.Repeat("0", 40), nil); !errors.Is(err, errFingerprint) {
		t.Errorf("a sha1 mismatch must be refused, got %v", err)
	}
	for _, n := range []string{"two.stu", "none.stu", "crc.stu", "sha.stu", "sha.stu.part"} {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("%s must not be left behind", n)
		}
	}
	notZip := write("plain.zip", []byte("not a zip at all"))
	if err := extractUpdateStu(ctx, notZip, filepath.Join(dir, "p.stu"), 1, 1, "", nil); !errors.Is(err, errFingerprint) {
		t.Errorf("a broken zip must be refused, got %v", err)
	}
}

// stickEnv plays a checked, roomy stick in a temp folder.
func stickEnv(t *testing.T) string {
	t.Helper()
	stick := t.TempDir()
	oldCheck, oldEject := fwStickCheck, fwStickEject
	fwStickCheck = func(p string) sticksetup.StickCheck {
		return sticksetup.StickCheck{OK: true, Path: p, IsFAT32: true, FreeBytes: 1 << 30}
	}
	fwStickEject = func(string) error { return nil }
	t.Cleanup(func() { fwStickCheck, fwStickEject = oldCheck, oldEject })
	return stick
}

func TestPrepareStickFromArchive(t *testing.T) {
	inner := fakeStu(120_000)
	z := fakeZip(t, map[string][]byte{"Update_ti_15.0.20.37272.2363762.scm.stu/Update.stu": inner}, "Update_ti_15.0.20.37272.2363762.scm.stu/Update.stu")
	step, _, _ := ladderEnv(t, z, inner)
	stick := stickEnv(t)
	res := (&App{}).runPrepareStick(context.Background(), step, stick, "")
	if !res.OK || !res.Ejected {
		t.Fatalf("result %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(stick, "Update.stu")); !bytes.Equal(got, inner) {
		t.Fatal("the stick does not hold the checked Update.stu")
	}
	dir, _ := fwArchiveCacheDir()
	if left, _ := filepath.Glob(filepath.Join(dir, "*.Update.stu")); len(left) != 0 {
		t.Errorf("the extracted file must be deleted once it is on the stick: %v", left)
	}
	if _, err := os.Stat(filepath.Join(dir, "fake.zip")); err != nil {
		t.Errorf("the zip stays in the user's cache for the next speaker: %v", err)
	}
}

func TestPrepareStickRefusesBadStick(t *testing.T) {
	inner := fakeStu(1000)
	z := fakeZip(t, map[string][]byte{"Update.stu": inner}, "Update.stu")
	step, _, hits := ladderEnv(t, z, inner)
	stick := stickEnv(t)
	for _, c := range []sticksetup.StickCheck{
		{Reason: "not-fat32"},
		{OK: true, ClusterTooBig: true, FreeBytes: 1 << 30},
		{OK: true, FreeBytes: 1000},
	} {
		fwStickCheck = func(string) sticksetup.StickCheck { return c }
		res := (&App{}).runPrepareStick(context.Background(), step, stick, "")
		if res.OK || res.Code != "stick" {
			t.Errorf("check %+v: result %+v", c, res)
		}
	}
	if *hits != 0 {
		t.Error("nothing is downloaded for a stick that cannot take the file")
	}
}

func TestPrepareStickFromLocalFile(t *testing.T) {
	inner := fakeStu(30_000)
	z := fakeZip(t, map[string][]byte{"dir/Update.stu": inner}, "dir/Update.stu")
	step, _, hits := ladderEnv(t, z, inner)
	stick := stickEnv(t)
	dl := t.TempDir()
	zipPath := filepath.Join(dl, "Airplay-1_ST20-30-Portable_07.00.37.zip")
	stuPath := filepath.Join(dl, "Update.stu")
	badPath := filepath.Join(dl, "bad.stu")
	_ = os.WriteFile(zipPath, z, 0o644)
	_ = os.WriteFile(stuPath, inner, 0o644)
	_ = os.WriteFile(badPath, append([]byte{1}, inner[1:]...), 0o644)
	a := &App{}
	for _, p := range []string{zipPath, stuPath} {
		_ = sticksetup.RemoveFirmwareFile(stick)
		if res := a.runPrepareStick(context.Background(), step, stick, p); !res.OK {
			t.Fatalf("%s: %+v", p, res)
		}
		if got, _ := os.ReadFile(filepath.Join(stick, "Update.stu")); !bytes.Equal(got, inner) {
			t.Fatalf("%s: stick content differs", p)
		}
	}
	_ = sticksetup.RemoveFirmwareFile(stick)
	res := a.runPrepareStick(context.Background(), step, stick, badPath)
	if res.OK || res.Code != "fingerprint" {
		t.Fatalf("a wrong file must fail the fingerprint check: %+v", res)
	}
	if sticksetup.ExistingFirmwareBytes(stick) != 0 {
		t.Error("nothing may be written to the stick for a wrong file")
	}
	if res := a.runPrepareStick(context.Background(), step, stick, filepath.Join(dl, "x.exe")); res.Code != "file-type" {
		t.Errorf("an .exe: %+v", res)
	}
	if *hits != 0 {
		t.Error("a local file must not trigger a download")
	}
}

func TestPrepareFirmwareStickUnknownStep(t *testing.T) {
	if res := (&App{}).PrepareFirmwareStick("no-such-step", "X:"); res.Code != "step" {
		t.Fatalf("%+v", res)
	}
}

func TestRecheckFirmwareTriesLANThenSetupAP(t *testing.T) {
	old := fwLadderRead
	defer func() { fwLadderRead = old }()
	var asked []string
	fwLadderRead = func(_ *App, host string) (FirmwareInfo, error) {
		asked = append(asked, host)
		if host == SetupAPHost {
			return FirmwareInfo{Reachable: true, Model: "SoundTouch 20", Firmware: "7.0.37.1234", Short: "7.0.37", DeviceID: "device-id-here"}, nil
		}
		return FirmwareInfo{}, errors.New("no answer")
	}
	a := &App{}
	r := a.RecheckFirmware("192.0.2.1")
	if !r.Found || r.Via != "setup-ap" || r.Version != "7.0.37" || strings.Join(asked, ",") != "192.0.2.1,"+SetupAPHost {
		t.Fatalf("%+v asked %v", r, asked)
	}
	p := a.GetFirmwareLadder("192.0.2.1")
	if p.Status != bosefw.StatusLadder || p.VersionFrom != "setup-ap" || p.DeviceID != "device-id-here" {
		t.Fatalf("plan %+v", p)
	}
	fwLadderRead = func(*App, string) (FirmwareInfo, error) { return FirmwareInfo{}, errors.New("no") }
	if r := a.RecheckFirmware(""); r.Found || r.Via != "unknown" {
		t.Fatalf("%+v", r)
	}
	if p := a.GetFirmwareLadder(""); p.Status != bosefw.StatusNeedsVersion {
		t.Fatalf("%+v", p)
	}
}

func TestFirmwareLadderNote(t *testing.T) {
	if n := firmwareLadderNote("SoundTouch 20", "", "", "5.2.0.11111.222"); !strings.Contains(n, "5 updates") {
		t.Errorf("5.x ST20: %q", n)
	}
	if n := firmwareLadderNote("SoundTouch 20", "", "", "10.0.11"); !strings.Contains(n, "one question") {
		t.Errorf("10.x ST20 without moduleType: %q", n)
	}
	for _, c := range [][2]string{{"SoundTouch 20", "27.0.3"}, {"Wave SoundTouch", "2.1.51"}, {"SoundTouch 30", "15.0.20"}} {
		if n := firmwareLadderNote(c[0], "", "", c[1]); n != "" {
			t.Errorf("%v: no ladder, no note, got %q", c, n)
		}
	}
}

func TestGetFirmwareLadderManual(t *testing.T) {
	a := &App{}
	if p := a.GetFirmwareLadderManual("SoundTouch 20", "", "10.0.11", ""); p.Status != bosefw.StatusNeedsBluetooth {
		t.Fatalf("%+v", p)
	}
	if p := a.GetFirmwareLadderManual("SoundTouch 20", "", "10.0.11", "no"); p.Status != bosefw.StatusLadder || p.VersionFrom != "manual" {
		t.Fatalf("%+v", p)
	}
	if p := a.GetFirmwareLadderManual("SoundTouch 20", "", "10.0.11", "yes"); p.Status != bosefw.StatusUnsupported {
		t.Fatalf("%+v", p)
	}
}
