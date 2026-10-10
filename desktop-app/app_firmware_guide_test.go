package main

import (
	"strings"
	"testing"

	"streborn-app/bosefw"
)

// The ST20 on 5.2.0 (field report 2026-10-10) was told to use "Bose's USB update
// tool" and to look in a settings section its stock speaker cannot open. The
// note must name neither, must point at the guide the setup screen renders, and
// must carry Bose's own link so a copied failure report stands on its own.
func TestFirmwareTooOldNote(t *testing.T) {
	one := []bosefw.File{{Name: "a.sm2.stu", URL: bosefw.DownloadBase + "a.sm2.stu", Generation: "sm2"}}
	two := []bosefw.File{
		{Name: "a.scm.stu", URL: bosefw.DownloadBase + "a.scm.stu", Generation: "scm"},
		{Name: "a.sm2.stu", URL: bosefw.DownloadBase + "a.sm2.stu", Generation: "sm2"},
	}
	for _, files := range [][]bosefw.File{nil, one, two} {
		note := firmwareTooOldNote("5.2.0", files)
		for _, banned := range []string{"btu.bose.com", "USB update tool", "Firmware section", "another stick", "\u2014"} {
			if strings.Contains(note, banned) {
				t.Errorf("note still says %q: %s", banned, note)
			}
		}
		for _, want := range []string{"5.2.0", latestBoseFirmware, "firmware update guide"} {
			if !strings.Contains(note, want) {
				t.Errorf("note lacks %q: %s", want, note)
			}
		}
		for _, f := range files {
			if !strings.Contains(note, f.URL) {
				t.Errorf("note lacks Bose's link %s", f.URL)
			}
		}
	}
	if n := firmwareTooOldNote("5.2.0", two); !strings.Contains(n, "refuses the first") {
		t.Errorf("two files without the sentence on which is which: %s", n)
	}
	if n := firmwareTooOldNote("5.2.0", nil); strings.Contains(n, "https://") {
		t.Errorf("a model with no file must not get a guessed link: %s", n)
	}
}

func TestFirmwareGuideForFallsBackOffline(t *testing.T) {
	saved := boseCatalogue
	defer func() { boseCatalogue = saved }()
	// An unroutable catalogue URL: the built-in table must answer.
	boseCatalogue = &bosefw.Catalogue{URL: "http://127.0.0.1:1/index.xml"}
	g := firmwareGuideFor(t.Context(), "SoundTouch 20", "", "", "5.2.0.12345.678")
	if g.Live {
		t.Error("an unreachable index reported as live")
	}
	if g.Current != "5.2.0" || g.Required != latestBoseFirmware {
		t.Errorf("versions: %+v", g)
	}
	if len(g.Files) != 2 || g.Files[0].Generation != "scm" || g.Files[1].Generation != "sm2" {
		t.Errorf("ST20 without moduleType should offer both files, older hardware first: %+v", g.Files)
	}
	g = firmwareGuideFor(t.Context(), "CineMate 520", "sm2", "lisa", "")
	if g.Files == nil || len(g.Files) != 0 {
		t.Errorf("an unknown model gets an empty list, not nil: %#v", g.Files)
	}
}
