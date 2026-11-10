package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/sticksetup"
)

// The anonymised bundle blanked the stick's volume label and then wrote the
// mount path beside it, which on Linux is /media/<account>/<label> and on
// macOS /Volumes/<label>: the label after all, and the account name with it.
func TestStickMetaAnonymisedNamesNeitherLabelNorAccount(t *testing.T) {
	for _, path := range []string{
		"/media/annika/FAMILY-STICK",
		"/run/media/annika/FAMILY-STICK",
		"/Volumes/FAMILY-STICK",
		"/mnt/FAMILY-STICK",
		"/home/annika/FAMILY-STICK",
		`C:\Users\annika\FAMILY-STICK`,
		`\\annika-pc\FAMILY-STICK`,
	} {
		d := sticksetup.Drive{Path: path, Label: "FAMILY-STICK", Filesystem: "FAT32", TotalBytes: 8 << 30, FreeBytes: 7 << 30}
		b, err := json.Marshal(stickMeta(d, true))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"FAMILY-STICK", "annika"} {
			if strings.Contains(string(b), secret) {
				t.Errorf("%s: the anonymised _meta.json still contains %q: %s", path, secret, b)
			}
		}
		// What the reader needs survives.
		for _, want := range []string{`"filesystem":"FAT32"`, `"totalBytes":8589934592`, `"freeBytes":7516192768`, `"driveLabel":""`} {
			if !strings.Contains(string(b), want) {
				t.Errorf("%s: _meta.json lost %s: %s", path, want, b)
			}
		}
	}
}

func TestAnonymizeDrivePath(t *testing.T) {
	for in, want := range map[string]string{
		// A drive letter says nothing about anybody.
		`E:\`: `E:\`,
		`E:`:  `E:`,
		`f:/`: `f:/`,
		// The mount folders stay, so the bundle still shows how the stick was mounted.
		"/media/annika/STICK":     "/media/<removed>",
		"/run/media/annika/STICK": "/run/media/<removed>",
		"/Volumes/STICK":          "/Volumes/<removed>",
		"/Volumes/STICK/":         "/Volumes/<removed>",
		"/mnt/usb":                "/mnt/<removed>",
		// A label or account that happens to be called like a mount folder.
		"/media/media":     "/media/<removed>",
		"/media/run/STICK": "/media/run/<removed>",
		"/Volumes":         "/<removed>",
		// Anything else goes whole.
		"/home/annika/stick":     "/<removed>",
		`C:\Users\annika\stick`:  "<removed>",
		`\\annika-pc\share`:      "<removed>",
		"relative/annika/sticks": "<removed>",
		"":                       "",
	} {
		if got := anonymizeDrivePath(in); got != want {
			t.Errorf("anonymizeDrivePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// With anonymisation off the bundle is meant to be raw.
func TestStickMetaRawKeepsPathAndLabel(t *testing.T) {
	m := stickMeta(sticksetup.Drive{Path: "/media/annika/STICK", Label: "STICK"}, false)
	if m["drivePath"] != "/media/annika/STICK" || m["driveLabel"] != "STICK" {
		t.Errorf("raw meta = %v", m)
	}
}
