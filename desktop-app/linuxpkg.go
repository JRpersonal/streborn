package main

// A Linux copy of ST Reborn can come two ways, and only one of them may
// update itself by swapping its own binary:
//
//   - the tar.gz from the release: the binary lives wherever the user put it
//     (install.sh copies it to ~/.local/bin), and the in-app swap is right.
//   - the .deb from the release: dpkg owns /usr/bin/st-reborn. Swapping it
//     would fail on the root-owned folder anyway, and if it did not, dpkg's
//     record would stay on the old version. Such a copy downloads the next
//     .deb and hands it to the system's package installer.

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	linuxPkgNone = ""
	linuxPkgDeb  = "deb"
)

// debPackageName is the package name in .github/scripts/build-deb.sh.
const debPackageName = "st-reborn"

var (
	linuxPkgOnce sync.Once
	linuxPkg     string
)

// linuxPackage reports how this copy was installed on Linux: linuxPkgDeb, or
// linuxPkgNone for the tar.gz and every other OS.
func linuxPackage() string {
	linuxPkgOnce.Do(func() {
		if runtime.GOOS != "linux" {
			return
		}
		exe, err := os.Executable()
		if err != nil {
			return
		}
		if r, e := filepath.EvalSymlinks(exe); e == nil {
			exe = r
		}
		linuxPkg = detectLinuxPackage(exe, "/var/lib/dpkg/info")
	})
	return linuxPkg
}

// detectLinuxPackage is linuxPackage with every outside input passed in, so the
// decision can be tested anywhere. A .deb counts only when dpkg's file list of
// the package names this very binary: a tar.gz copy in ~/.local/bin on a
// machine that also has the .deb installed still updates itself.
func detectLinuxPackage(exe, dpkgInfoDir string) string {
	lists, _ := filepath.Glob(filepath.Join(dpkgInfoDir, debPackageName+"*.list"))
	for _, l := range lists {
		base := strings.TrimSuffix(filepath.Base(l), ".list")
		if base != debPackageName && !strings.HasPrefix(base, debPackageName+":") {
			continue
		}
		if dpkgListHas(l, exe) {
			return linuxPkgDeb
		}
	}
	return linuxPkgNone
}

func dpkgListHas(list, path string) bool {
	f, err := os.Open(list)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == path {
			return true
		}
	}
	return false
}
