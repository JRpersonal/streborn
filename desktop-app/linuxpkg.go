package main

// A Linux copy of ST Reborn can come three ways, and only one of them may
// update itself by swapping its own binary:
//
//   - the tar.gz from the release: the binary lives wherever the user put it
//     (install.sh copies it to ~/.local/bin), and the in-app swap is right.
//   - the .deb from the release: dpkg owns /usr/bin/st-reborn. Swapping it
//     would fail on the root-owned folder anyway, and if it did not, dpkg's
//     record would stay on the old version. Such a copy downloads the next
//     .deb and hands it to the system's package installer.
//   - the .rpm from the release (Fedora, openSUSE): the same, with rpm in
//     dpkg's place.

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	linuxPkgNone = ""
	linuxPkgDeb  = "deb"
	linuxPkgRpm  = "rpm"
)

// debPackageName is the package name in .github/scripts/build-deb.sh.
const debPackageName = "st-reborn"

// rpmPackageName is the package name in .github/scripts/build-rpm.sh.
const rpmPackageName = "st-reborn"

var (
	linuxPkgOnce sync.Once
	linuxPkg     string
)

// linuxPackage reports how this copy was installed on Linux: linuxPkgDeb,
// linuxPkgRpm, or linuxPkgNone for the tar.gz and every other OS.
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
		linuxPkg = detectLinuxPackage(exe, "/var/lib/dpkg/info", rpmOwnerOf)
	})
	return linuxPkg
}

// detectLinuxPackage is linuxPackage with every outside input passed in, so the
// decision can be tested anywhere. A .deb counts only when dpkg's file list of
// the package names this very binary: a tar.gz copy in ~/.local/bin on a
// machine that also has the .deb installed still updates itself. The same
// holds for the .rpm: it counts only when rpm names our package as the owner
// of this binary. rpmOwner is asked last and only when dpkg did not answer,
// so a Debian system never starts an rpm it may not have.
func detectLinuxPackage(exe, dpkgInfoDir string, rpmOwner func(path string) string) string {
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
	if rpmOwner != nil && rpmOwner(exe) == rpmPackageName {
		return linuxPkgRpm
	}
	return linuxPkgNone
}

// rpmOwnerOf returns the name of the RPM package that owns path, "" when no
// package does or this system has no rpm. The RPM database is not a text file
// like dpkg's lists (sqlite on current Fedora and openSUSE), so rpm itself is
// asked, once per start and with a short deadline.
func rpmOwnerOf(path string) string {
	rpm, err := exec.LookPath("rpm")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, rpm, "-qf", "--queryformat", "%{NAME}\n", path).Output()
	if err != nil {
		// rpm exits non-zero for a file no package owns.
		return ""
	}
	return rpmOwnerFromOutput(string(out))
}

// rpmOwnerFromOutput reads the package name out of rpm's answer: the first
// line, as one file has one owner in a healthy database.
func rpmOwnerFromOutput(out string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(line)
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
