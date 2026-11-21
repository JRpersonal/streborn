package usbmedia

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func write(t *testing.T, root string, rel ...string) {
	t.Helper()
	for _, r := range rel {
		p := filepath.Join(root, filepath.FromSlash(r))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClassifyKeepsInstallAndMusicSticksApart(t *testing.T) {
	cases := []struct {
		files []string
		want  Kind
	}{
		{[]string{"Music/a.mp3"}, KindMusic},
		{[]string{"a.flac", "cover.jpg"}, KindMusic},
		{[]string{"install.sh", "Music/a.mp3"}, KindInstall}, // a combined stick is an install stick
		{[]string{"RUN.SH"}, KindInstall},                    // FAT is case-insensitive
		{[]string{"streborn-armv7l"}, KindInstall},
		{[]string{"version.txt"}, KindInstall},
		{[]string{"wlan.pass"}, KindInstall},
		{[]string{"Update.stu"}, KindInstall}, // a Bose firmware stick
		{[]string{"remote_services"}, KindInstall},
	}
	for _, c := range cases {
		root := t.TempDir()
		write(t, root, c.files...)
		got, err := Classify(root)
		if err != nil {
			t.Fatalf("%v: %v", c.files, err)
		}
		if got != c.want {
			t.Errorf("%v: got kind %d, want %d", c.files, got, c.want)
		}
	}
}

func TestClassifyUnreadableRootIsNotServed(t *testing.T) {
	got, err := Classify(filepath.Join(t.TempDir(), "gone"))
	if err == nil || got != KindInstall {
		t.Fatalf("want KindInstall + error, got %d, %v", got, err)
	}
}

func TestBrowseOneFolderFoldersFirstHiddenSkipped(t *testing.T) {
	root := t.TempDir()
	write(t, root,
		"b.mp3", "A.flac", "notes.txt", "._A.flac", ".hidden.mp3",
		"System Volume Information/x", "zeta/1.mp3", "Alpha/deep/2.mp3",
	)
	lib, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lib.Close() }()

	page, total, err := lib.Browse("0", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range page {
		ids = append(ids, e.ID)
	}
	want := []string{"Alpha", "zeta", "A.flac", "b.mp3"}
	if total != len(want) || len(ids) != len(want) {
		t.Fatalf("got %v (total %d), want %v", ids, total, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v, want %v", ids, want)
		}
	}
	if page[2].MIME != "audio/flac" || page[2].Title != "A" {
		t.Errorf("file entry: %+v", page[2])
	}

	// Paging, and a nested folder is listed by its relative id.
	p2, total2, err := lib.Browse("0", 1, 2)
	if err != nil || total2 != 4 || len(p2) != 2 || p2[0].ID != "zeta" {
		t.Fatalf("page 2: %+v total %d err %v", p2, total2, err)
	}
	sub, _, err := lib.Browse("Alpha", 0, 0)
	if err != nil || len(sub) != 1 || sub[0].ID != "Alpha/deep" || !sub[0].Folder {
		t.Fatalf("nested: %+v %v", sub, err)
	}
}

func TestNoPathLeavesTheStick(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "stick")
	write(t, outer, "secret.mp3")
	write(t, root, "ok.mp3")
	lib, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lib.Close() }()

	for _, id := range []string{"../secret.mp3", "/etc/passwd.mp3", "a/../../secret.mp3", `..\secret.mp3`, "./ok.mp3"} {
		if f, err := lib.OpenFile(id); err == nil {
			_ = f.Close()
			t.Errorf("OpenFile(%q) must fail", id)
		}
		if _, _, err := lib.Browse(id, 0, 0); err == nil {
			t.Errorf("Browse(%q) must fail", id)
		}
	}
	if f, err := lib.OpenFile("ok.mp3"); err != nil {
		t.Fatalf("a real file must open: %v", err)
	} else {
		_ = f.Close()
	}
	if _, err := lib.OpenFile("0"); !errors.Is(err, ErrBadID) {
		t.Errorf("the root is not a file: %v", err)
	}
}

func TestSymlinkOutOfTheStickIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	outer := t.TempDir()
	root := filepath.Join(outer, "stick")
	write(t, outer, "secret.mp3")
	write(t, root, "ok.mp3")
	if err := os.Symlink(filepath.Join(outer, "secret.mp3"), filepath.Join(root, "link.mp3")); err != nil {
		t.Fatal(err)
	}
	lib, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lib.Close() }()
	if f, err := lib.OpenFile("link.mp3"); err == nil {
		_ = f.Close()
		t.Fatal("a symlink pointing out of the stick must not open")
	}
	page, _, _ := lib.Browse("0", 0, 0)
	for _, e := range page {
		if e.ID == "link.mp3" {
			t.Fatal("a symlink must not be listed")
		}
	}
}

func TestFileURLKeepsTheExtensionLast(t *testing.T) {
	got := FileURL("http://127.0.0.1:8888/", "Rock & Pop/Ärzte #1.flac")
	want := "http://127.0.0.1:8888/usb/file/Rock%20&%20Pop/%C3%84rzte%20%231.flac"
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}
