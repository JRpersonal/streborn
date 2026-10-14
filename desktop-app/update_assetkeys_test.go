package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// An ARM64 Linux app updating itself from the x86_64 archive would replace
// itself with a binary it cannot run, so each architecture gets its own key and
// ARM64 has no fallback. A .deb copy updates from the .deb only.
func TestUpdateAssetKeysLinuxByArch(t *testing.T) {
	cases := []struct {
		arch, pkg string
		want      []string
	}{
		{"amd64", linuxPkgNone, []string{"desktop_linux"}},
		{"arm64", linuxPkgNone, []string{"desktop_linux_arm64"}},
		{"amd64", linuxPkgDeb, []string{"desktop_linux_deb"}},
		{"arm64", linuxPkgDeb, []string{"desktop_linux_arm64_deb"}},
	}
	for _, c := range cases {
		if got := updateAssetKeysFor("linux", c.arch, c.pkg); !slices.Equal(got, c.want) {
			t.Errorf("linux/%s/%q: got %v, want %v", c.arch, c.pkg, got, c.want)
		}
	}
	if got := updateAssetKeysFor("windows", "amd64", linuxPkgNone); !slices.Equal(got, []string{"desktop_windows"}) {
		t.Errorf("windows: got %v", got)
	}
}

func TestDetectLinuxPackage(t *testing.T) {
	dir := t.TempDir()
	info := filepath.Join(dir, "dpkg")
	if err := os.MkdirAll(info, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := detectLinuxPackage("/usr/bin/st-reborn", info); got != linuxPkgNone {
		t.Fatalf("nothing installed: got %q", got)
	}
	list := "/.\n/usr\n/usr/bin\n/usr/bin/st-reborn\n/usr/share/applications/st-reborn.desktop\n"
	if err := os.WriteFile(filepath.Join(info, "st-reborn.list"), []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectLinuxPackage("/usr/bin/st-reborn", info); got != linuxPkgDeb {
		t.Fatalf("binary dpkg owns: got %q", got)
	}
	// A tar.gz copy on a machine that also has the .deb keeps updating itself.
	if got := detectLinuxPackage("/home/u/.local/bin/st-reborn", info); got != linuxPkgNone {
		t.Fatalf("tar.gz copy beside the .deb: got %q", got)
	}
	// Another package whose name starts the same way does not count.
	_ = os.Remove(filepath.Join(info, "st-reborn.list"))
	if err := os.WriteFile(filepath.Join(info, "st-reborn-extras.list"), []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectLinuxPackage("/usr/bin/st-reborn", info); got != linuxPkgNone {
		t.Fatalf("other package: got %q", got)
	}
	// dpkg names a multi-arch list "st-reborn:arm64.list". Windows cannot
	// hold that name (the colon opens a data stream), so it is checked on the
	// systems the .deb exists for.
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(filepath.Join(info, "st-reborn:arm64.list"), []byte(list), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := detectLinuxPackage("/usr/bin/st-reborn", info); got != linuxPkgDeb {
			t.Fatalf("arch-qualified list: got %q", got)
		}
	}
}
