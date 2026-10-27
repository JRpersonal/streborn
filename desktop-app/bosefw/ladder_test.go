package bosefw

import (
	"regexp"
	"strings"
	"testing"
)

func versions(steps []Step) string {
	var v []string
	for _, s := range steps {
		v = append(v, s.Version)
	}
	return strings.Join(v, " ")
}

func boolp(b bool) *bool { return &b }

func TestPlanByStartingVersion(t *testing.T) {
	cases := []struct {
		name, model, mt, version string
		bt                       *bool
		status, steps            string
	}{
		{"5.x ST20, the field case", "SoundTouch 20", "", "5.2.0.11111.222", nil, StatusLadder, "06.02.01 07.00.37 14.00.33 15.00.20 27.0.6"},
		{"exactly 06.02.01 does not get it again", "SoundTouch 20", "scm", "6.2.1", nil, StatusLadder, "07.00.37 14.00.33 15.00.20 27.0.6"},
		{"6.x older than 06.02.01 skips it", "SoundTouch 30", "scm", "6.0.39", nil, StatusLadder, "07.00.37 14.00.33 15.00.20 27.0.6"},
		{"7.0.37", "SoundTouch 20", "", "7.0.37", nil, StatusLadder, "14.00.33 15.00.20 27.0.6"},
		{"14.0.15 goes via 14.00.33, never straight to 27 (#854)", "SoundTouch 20", "scm", "14.0.15", nil, StatusLadder, "14.00.33 15.00.20 27.0.6"},
		{"14.0.33", "SoundTouch Portable", "", "14.0.33", nil, StatusLadder, "15.00.20 27.0.6"},
		{"15.0.20 gets the final step only", "SoundTouch 20", "scm", "15.0.20", nil, StatusFinal, "27.0.6"},
		{"27.0.3 gets the final step only", "SoundTouch Portable", "scm", "27.0.3", nil, StatusFinal, "27.0.6"},
		{"27.0.6 is done", "SoundTouch 20", "scm", "27.0.6.46330.5043500", nil, StatusUpToDate, ""},
		{"Wave 2.1.51 without moduleType is not in phase 1", "Wave SoundTouch", "", "2.1.51", nil, StatusUnsupported, ""},
		{"ST20 10.x without moduleType asks about Bluetooth", "SoundTouch 20", "", "10.0.11", nil, StatusNeedsBluetooth, ""},
		{"... no Bluetooth button means scm", "SoundTouch 20", "", "10.0.11", boolp(false), StatusLadder, "14.00.33 15.00.20 27.0.6"},
		{"... a Bluetooth button means sm2, not in phase 1", "SoundTouch 30", "", "10.0.11", boolp(true), StatusUnsupported, ""},
		{"a Portable never asks: it is scm only", "SoundTouch Portable", "", "10.0.11", nil, StatusLadder, "14.00.33 15.00.20 27.0.6"},
		{"sm2 below 9 cannot exist, so no moduleType at 7 is scm", "SoundTouch 30", "", "7.2.21", nil, StatusLadder, "14.00.33 15.00.20 27.0.6"},
		{"sm2 reported is not in phase 1", "SoundTouch 20", "sm2", "14.0.15", nil, StatusUnsupported, ""},
		{"SoundTouch 10 is not in phase 1", "SoundTouch 10", "sm2", "14.0.15", nil, StatusUnsupported, ""},
		{"no version asks for it", "SoundTouch 20", "", "", nil, StatusNeedsVersion, ""},
		{"a 2.x ST20 gets the full ladder", "SoundTouch 20", "", "2.1.51", nil, StatusLadder, "06.02.01 07.00.37 14.00.33 15.00.20 27.0.6"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Plan(c.model, c.mt, "", c.version, c.bt)
			if p.Status != c.status {
				t.Fatalf("status %q, want %q", p.Status, c.status)
			}
			if got := versions(p.Steps); got != c.steps {
				t.Fatalf("steps %q, want %q", got, c.steps)
			}
		})
	}
}

func TestPlanFlags(t *testing.T) {
	p := Plan("SoundTouch 20", "", "", "2.1.51", nil)
	if !p.OldStart {
		t.Error("a 2.x speaker must be flagged: 06.02.01 from there is untested")
	}
	if p.ModuleType != "scm" || p.Family != FamilySTScm {
		t.Errorf("moduleType %q family %q", p.ModuleType, p.Family)
	}
	if p.ArchiveSteps() != 4 {
		t.Errorf("archive steps %d, want 4", p.ArchiveSteps())
	}
	if Plan("SoundTouch 20", "", "", "5.2.0", nil).OldStart {
		t.Error("5.x is not an old start")
	}
	if r := Plan("SoundTouch 20", "sm2", "", "10.0.1", nil).Reason; r != "sm2" {
		t.Errorf("reason %q", r)
	}
	if r := Plan("Wave SoundTouch", "", "", "2.1.51", nil).Reason; r != "model" {
		t.Errorf("reason %q", r)
	}
	fb := Plan("SoundTouch 20", "scm", "", "7.0.37", nil).Fallbacks
	if got := versions(fb[FamilySTScm+"-14.00.33"]); got != "09.00.41 12.00.09 14.00.33" || len(fb) != 1 {
		t.Errorf("fallbacks from 7.0.37: %v", fb)
	}
	if fb := Plan("SoundTouch 20", "", "", "2.1.51", nil).Fallbacks; versions(fb[FamilySTScm+"-06.02.01"]) != "05.00.23 06.02.01" {
		t.Errorf("fallbacks from 2.x: %v", fb)
	}
}

func TestNextAndReached(t *testing.T) {
	p := Plan("SoundTouch 20", "", "", "5.2.0", nil)
	cases := map[string]string{
		"5.2.0":   "06.02.01",
		"6.2.1":   "07.00.37",
		"7.0.37":  "14.00.33",
		"14.0.33": "15.00.20",
		"15.0.20": "27.0.6",
		"":        "06.02.01", // unknown: stay on the first step
	}
	for rep, want := range cases {
		s, ok := Next(p, rep)
		if !ok || s.Version != want {
			t.Errorf("Next(%q) = %q %v, want %q", rep, s.Version, ok, want)
		}
	}
	if _, ok := Next(p, "27.0.6"); ok {
		t.Error("nothing is next on 27.0.6")
	}
	s, _ := StepByID(FamilySTScm + "-07.00.37")
	if !Reached(s, "7.0.37.1234") || Reached(s, "6.2.1") || Reached(s, "") {
		t.Error("Reached is wrong")
	}
}

func TestFallbackFor(t *testing.T) {
	if got := versions(FallbackFor(FamilySTScm+"-14.00.33", "7.0.37")); got != "09.00.41 12.00.09 14.00.33" {
		t.Errorf("7.0.37 fallback %q", got)
	}
	if got := versions(FallbackFor(FamilySTScm+"-14.00.33", "10.0.11")); got != "12.00.09 14.00.33" {
		t.Errorf("10.x fallback %q", got)
	}
	if got := versions(FallbackFor(FamilySTScm+"-06.02.01", "2.1.51")); got != "05.00.23 06.02.01" {
		t.Errorf("2.x fallback %q", got)
	}
	if FallbackFor(FamilySTScm+"-06.02.01", "5.2.0") != nil {
		t.Error("5.x has no smaller step before 06.02.01")
	}
	if FallbackFor(FamilySTScm+"-15.00.20", "14.0.33") != nil {
		t.Error("15.00.20 has no fallback")
	}
	for _, s := range FallbackFor(FamilySTScm+"-14.00.33", "7.0.37") {
		if s.Version != "14.00.33" && !s.Fallback {
			t.Errorf("%s should be marked as a fallback step", s.ID)
		}
	}
}

func TestVersionTuple(t *testing.T) {
	cases := map[string][3]int{
		"5.2.0.11111.222":      {5, 2, 0},
		"06.02.01":             {6, 2, 1},
		"14.0.15 epdbuild.x":   {14, 0, 15},
		"27.0.6.46330.5043500": {27, 0, 6},
		"2.1.51":               {2, 1, 51},
		"7":                    {7, 0, 0},
	}
	for in, want := range cases {
		got, ok := VersionTuple(in)
		if !ok || got != want {
			t.Errorf("VersionTuple(%q) = %v %v", in, got, ok)
		}
	}
	if _, ok := VersionTuple("unknown"); ok {
		t.Error("a word is no version")
	}
}

// The table's invariants. A step added later has to pass them too.
func TestLadderDataInvariants(t *testing.T) {
	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	prev := ""
	for i, s := range stScmLadder {
		if prev != "" && CompareVersions(s.Version, prev) <= 0 {
			t.Errorf("%s: versions must go strictly up (after %s)", s.ID, prev)
		}
		prev = s.Version
		last := i == len(stScmLadder)-1
		if last != (s.Source == SourceBose) {
			t.Errorf("%s: only the last step comes from Bose, and it must", s.ID)
		}
		if s.NetworkOK && s.Source != SourceBose {
			t.Errorf("%s: an archive step must never go over the network", s.ID)
		}
		if s.Fallback {
			t.Errorf("%s: a main step is not a fallback", s.ID)
		}
	}
	ids := map[string]bool{}
	for _, s := range AllSteps() {
		if ids[s.ID] {
			t.Errorf("duplicate ID %s", s.ID)
		}
		ids[s.ID] = true
		if s.ID != s.Family+"-"+s.Version {
			t.Errorf("%s: ID must be family-version", s.ID)
		}
		if what, bad := foreignCRCs[s.InnerCRC32]; bad {
			t.Errorf("%s: inner CRC %08x is another family's image (%s)", s.ID, s.InnerCRC32, what)
		}
		if s.InnerSize <= 0 || s.InnerCRC32 == 0 {
			t.Errorf("%s: needs the inner size and CRC-32", s.ID)
		}
		if s.InnerSHA1 != "" && !hex40.MatchString(s.InnerSHA1) {
			t.Errorf("%s: inner sha1 %q", s.ID, s.InnerSHA1)
		}
		switch s.Source {
		case SourceArchive:
			if !strings.HasPrefix(s.URL, ArchiveItemBase) || !strings.HasSuffix(s.URL, "/"+s.FileName) {
				t.Errorf("%s: URL %q is not the archive item", s.ID, s.URL)
			}
			if s.ZipSize <= 0 || !hex40.MatchString(s.ZipSHA1) {
				t.Errorf("%s: an archive step needs the zip size and sha1", s.ID)
			}
			if !strings.HasSuffix(s.FileName, ".zip") {
				t.Errorf("%s: archive files are zips", s.ID)
			}
		case SourceBose:
			if !strings.HasPrefix(s.URL, DownloadBase) {
				t.Errorf("%s: Bose's file must come from Bose's host", s.ID)
			}
			// The last step is byte for byte the file in Bose's catalogue.
			var found bool
			for _, e := range Builtin {
				if e.FileName == s.FileName && e.Length == s.InnerSize && e.CRC == s.InnerCRC32 {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: does not match Bose's catalogue", s.ID)
			}
		default:
			t.Errorf("%s: source %q", s.ID, s.Source)
		}
	}
	for _, s := range stScmFallback {
		if !s.Fallback || s.LiveVerified {
			t.Errorf("%s: fallback steps are marked as such and untested", s.ID)
		}
	}
	// Live evidence so far: only 06.02.01, 07.00.37 and 14.00.33, by stick, on a
	// SoundTouch 20 Series I (15.00.20 stalled after it). The network flash to
	// 27.0.6 was seen from 27.0.3 only, not from 15.00.20, so the ladder's last
	// step is not "tested" yet.
	for _, s := range AllSteps() {
		want := s.Version == "06.02.01" || s.Version == "07.00.37" || s.Version == "14.00.33"
		if s.LiveVerified != want {
			t.Errorf("%s: LiveVerified %v without a field report", s.ID, s.LiveVerified)
		}
	}
}
