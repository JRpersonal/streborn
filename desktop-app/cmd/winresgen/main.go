// Command winresgen writes the Windows version resource for the desktop app.
//
// Why this exists. The released Windows binary carries no version information
// at all: reading its resources shows an icon, an icon group and a manifest,
// and no version block. Explorer therefore shows blank fields, the first-run
// warning names no publisher, and an antivirus heuristic sees a large
// executable that talks to the network, updates itself, and says nothing about
// who made it. v0.9.48 was flagged by two scanners, and one user could not
// install it because Windows quarantined the download.
//
// Wails is supposed to generate this from build/windows/info.json and does not:
// it skips the block without complaint when its template comes back empty, and
// three plausible causes were tested and eliminated (a missing product version,
// a three part version where Windows wants four, and the unusual language
// identifier in the default template). Rather than keep guessing at somebody
// else's build step, STR writes the block itself.
//
// The output is the COMPLETE resource: icon, manifest and version, taken from
// the same build/windows files Wails reads. It has to be complete because the
// Go linker refuses a second resource section ("too many .rsrc sections"), so
// this replaces the one Wails writes rather than adding to it. The build calls
// wails with -nopackage for exactly that reason.
//
// The file is a build artefact and is not committed, in the same way as the
// embedded helper binaries.
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

// Windows wants four numbers. A three part version leaves the fixed part of the
// block malformed, and a malformed block reads as no block at all.
var fourPartRE = regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`)

func main() {
	out := flag.String("out", "desktop-app/rsrc_windows_amd64.syso", "resource object to write")
	ver := flag.String("version", "0.0.0.0", "product version, four numbers")
	company := flag.String("company", "STR (SoundTouch Reborn)", "company name")
	product := flag.String("product", "ST Reborn", "product name")
	copyright := flag.String("copyright", "Copyright (c) 2026", "legal copyright")
	comments := flag.String("comments", "", "comments")
	icon := flag.String("icon", "build/windows/icon.ico", "application icon")
	manifest := flag.String("manifest", "build/windows/wails.exe.manifest", "application manifest")
	flag.Parse()

	v := normalise(*ver)
	if !fourPartRE.MatchString(v) {
		fmt.Fprintf(os.Stderr, "winresgen: %q is not a four part version\n", *ver)
		os.Exit(1)
	}

	var vi version.Info
	vi.SetProductVersion(v)
	vi.SetFileVersion(v)
	// 0x0409 is US English and the translation Windows looks for first. The
	// language neutral identifier in Wails' own template is legal but is not
	// what the common readers resolve.
	const enUS = 0x0409
	for key, val := range map[string]string{
		version.CompanyName:     *company,
		version.ProductName:     *product,
		version.FileDescription: *product,
		version.LegalCopyright:  *copyright,
		version.ProductVersion:  v,
		version.FileVersion:     v,
		version.Comments:        *comments,
	} {
		if val == "" {
			continue
		}
		if err := vi.Set(enUS, key, val); err != nil {
			fmt.Fprintf(os.Stderr, "winresgen: %s: %v\n", key, err)
			os.Exit(1)
		}
	}

	rs := winres.ResourceSet{}
	rs.SetVersionInfo(vi)

	// The icon and the manifest are passed through unchanged from the files
	// Wails uses, so nothing about the window, the DPI behaviour or the
	// requested privileges changes with this.
	ico, err := os.Open(*icon)
	if err != nil {
		fail("icon", err)
	}
	loaded, err := winres.LoadICO(ico)
	ico.Close()
	if err != nil {
		fail("icon", err)
	}
	if err := rs.SetIcon(winres.RT_ICON, loaded); err != nil {
		fail("icon", err)
	}
	manifestXML, err := os.ReadFile(*manifest)
	if err != nil {
		fail("manifest", err)
	}
	am, err := winres.AppManifestFromXML(manifestXML)
	if err != nil {
		fail("manifest", err)
	}
	rs.SetManifest(am)

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "winresgen: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()
	if err := rs.WriteObject(f, winres.ArchAMD64); err != nil {
		fmt.Fprintf(os.Stderr, "winresgen: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s with version %s\n", *out, v)
}

// normalise turns the version strings this project actually produces into the
// four numbers Windows wants: a leading v is dropped, a git describe suffix
// such as -38-gd1b3d47-dirty is cut off, and a missing fourth part is filled in.
func normalise(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	for len(parts) < 4 {
		parts = append(parts, "0")
	}
	return strings.Join(parts[:4], ".")
}

func fail(what string, err error) {
	fmt.Fprintf(os.Stderr, "winresgen: %s: %v"+string(rune(10)), what, err)
	os.Exit(1)
}
