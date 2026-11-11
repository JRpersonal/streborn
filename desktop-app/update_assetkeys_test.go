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
// ARM64 has no fallback. A .deb copy updates from the .deb only, an .rpm copy
// from the .rpm only.
func TestUpdateAssetKeysLinuxByArch(t *testing.T) {
	cases := []struct {
		arch, pkg string
		want      []string
	}{
		{"amd64", linuxPkgNone, []string{"desktop_linux"}},
		{"arm64", linuxPkgNone, []string{"desktop_linux_arm64"}},
		{"amd64", linuxPkgDeb, []string{"desktop_linux_deb"}},
		{"arm64", linuxPkgDeb, []string{"desktop_linux_arm64_deb"}},
		{"amd64", linuxPkgRpm, []string{"desktop_linux_rpm"}},
		{"arm64", linuxPkgRpm, []string{"desktop_linux_arm64_rpm"}},
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
	if got := detectLinuxPackage("/usr/bin/st-reborn", info, nil); got != linuxPkgNone {
		t.Fatalf("nothing installed: got %q", got)
	}
	list := "/.\n/usr\n/usr/bin\n/usr/bin/st-reborn\n/usr/share/applications/st-reborn.desktop\n"
	if err := os.WriteFile(filepath.Join(info, "st-reborn.list"), []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectLinuxPackage("/usr/bin/st-reborn", info, nil); got != linuxPkgDeb {
		t.Fatalf("binary dpkg owns: got %q", got)
	}
	// A tar.gz copy on a machine that also has the .deb keeps updating itself.
	if got := detectLinuxPackage("/home/u/.local/bin/st-reborn", info, nil); got != linuxPkgNone {
		t.Fatalf("tar.gz copy beside the .deb: got %q", got)
	}
	// Another package whose name starts the same way does not count.
	_ = os.Remove(filepath.Join(info, "st-reborn.list"))
	if err := os.WriteFile(filepath.Join(info, "st-reborn-extras.list"), []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectLinuxPackage("/usr/bin/st-reborn", info, nil); got != linuxPkgNone {
		t.Fatalf("other package: got %q", got)
	}
	// dpkg names a multi-arch list "st-reborn:arm64.list". Windows cannot
	// hold that name (the colon opens a data stream), so it is checked on the
	// systems the .deb exists for.
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(filepath.Join(info, "st-reborn:arm64.list"), []byte(list), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := detectLinuxPackage("/usr/bin/st-reborn", info, nil); got != linuxPkgDeb {
			t.Fatalf("arch-qualified list: got %q", got)
		}
	}
}

// A copy the .rpm installed (Fedora, openSUSE) is recognised by asking rpm who
// owns the running binary. Swapping /usr/bin/st-reborn in place would fail on
// the root-owned folder, so such a copy must take the package path.
func TestDetectLinuxPackageRpm(t *testing.T) {
	noDpkg := filepath.Join(t.TempDir(), "dpkg")
	owner := func(name string) func(string) string {
		return func(path string) string {
			if path == "/usr/bin/st-reborn" {
				return name
			}
			return ""
		}
	}
	if got := detectLinuxPackage("/usr/bin/st-reborn", noDpkg, owner("st-reborn")); got != linuxPkgRpm {
		t.Fatalf("binary rpm owns: got %q", got)
	}
	// A tar.gz copy on a machine that also has the .rpm keeps updating itself.
	if got := detectLinuxPackage("/home/u/.local/bin/st-reborn", noDpkg, owner("st-reborn")); got != linuxPkgNone {
		t.Fatalf("tar.gz copy beside the .rpm: got %q", got)
	}
	// Somebody else's package of the binary (a distribution's own build, a
	// repack) is not ours to update with our .rpm.
	if got := detectLinuxPackage("/usr/bin/st-reborn", noDpkg, owner("st-reborn-git")); got != linuxPkgNone {
		t.Fatalf("other package: got %q", got)
	}
	// dpkg answers first: on a Debian system that also carries rpm, the list
	// decides and rpm is not asked at all.
	info := filepath.Join(t.TempDir(), "dpkg")
	if err := os.MkdirAll(info, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info, "st-reborn.list"), []byte("/usr/bin/st-reborn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	asked := false
	got := detectLinuxPackage("/usr/bin/st-reborn", info, func(string) string { asked = true; return "st-reborn" })
	if got != linuxPkgDeb || asked {
		t.Fatalf("dpkg-owned binary: got %q, rpm asked %v", got, asked)
	}
}

func TestRpmOwnerFromOutput(t *testing.T) {
	cases := map[string]string{
		"st-reborn\n":            "st-reborn",
		"  st-reborn  \n":        "st-reborn",
		"st-reborn\nst-reborn\n": "st-reborn",
		"":                       "",
	}
	for in, want := range cases {
		if got := rpmOwnerFromOutput(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// A package manager is only ever handed its own format: a manifest that named
// the wrong file for this installation stops before anything is opened.
func TestCheckLinuxPackageFile(t *testing.T) {
	ok := []struct{ file, pkg string }{
		{"/tmp/STR-Linux-x64.deb", linuxPkgDeb},
		{"/tmp/STR-Linux-arm64.rpm", linuxPkgRpm},
		{"/tmp/STR-Linux-x64.RPM", linuxPkgRpm},
	}
	for _, c := range ok {
		if err := checkLinuxPackageFile(c.file, c.pkg); err != nil {
			t.Errorf("%s as %s: %v", c.file, c.pkg, err)
		}
	}
	bad := []struct{ file, pkg string }{
		{"/tmp/STR-Linux-x64.rpm", linuxPkgDeb},
		{"/tmp/STR-Linux-x64.deb", linuxPkgRpm},
		{"/tmp/STR-Linux-x64.tar.gz", linuxPkgRpm},
		{"/tmp/STR-Linux-x64.deb", linuxPkgNone},
	}
	for _, c := range bad {
		if err := checkLinuxPackageFile(c.file, c.pkg); err == nil {
			t.Errorf("%s as %q was accepted", c.file, c.pkg)
		}
	}
}
