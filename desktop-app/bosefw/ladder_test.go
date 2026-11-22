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
		{"5.x ST20, the field case", "SoundTouch 20", "", "5.2.0.11111.222", nil, StatusLadder, "06.02.01 07.00.37 14.00.33 16.00.09 27.0.6"},
		{"exactly 06.02.01 does not get it again", "SoundTouch 20", "scm", "6.2.1", nil, StatusLadder, "07.00.37 14.00.33 16.00.09 27.0.6"},
		{"6.x older than 06.02.01 skips it", "SoundTouch 30", "scm", "6.0.39", nil, StatusLadder, "07.00.37 14.00.33 16.00.09 27.0.6"},
		{"7.0.37", "SoundTouch 20", "", "7.0.37", nil, StatusLadder, "14.00.33 16.00.09 27.0.6"},
		{"14.0.15 goes via 14.00.33, never straight to 27 (#854)", "SoundTouch 20", "scm", "14.0.15", nil, StatusLadder, "14.00.33 16.00.09 27.0.6"},
		{"14.0.33", "SoundTouch Portable", "", "14.0.33", nil, StatusLadder, "16.00.09 27.0.6"},
		{"15.0.20 gets the final step only", "SoundTouch 20", "scm", "15.0.20", nil, StatusFinal, "27.0.6"},
		{"27.0.3 gets the final step only", "SoundTouch Portable", "scm", "27.0.3", nil, StatusFinal, "27.0.6"},
		{"27.0.6 is done", "SoundTouch 20", "scm", "27.0.6.46330.5043500", nil, StatusUpToDate, ""},
		{"Wave 2.1.51 without moduleType gets the documented Wave route", "Wave SoundTouch", "", "2.1.51", nil, StatusRoute, "06.02.01 07.00.37 14.00.33 15.00.20 27.0.6"},
		{"ST20 10.x without moduleType asks about Bluetooth", "SoundTouch 20", "", "10.0.11", nil, StatusNeedsBluetooth, ""},
		{"... no Bluetooth button means scm", "SoundTouch 20", "", "10.0.11", boolp(false), StatusLadder, "14.00.33 16.00.09 27.0.6"},
		{"... a Bluetooth button means sm2: the Bluetooth chassis ladder", "SoundTouch 30", "", "10.0.11", boolp(true), StatusLadder, "14.00.33 15.00.20 27.0.6"},
		{"a Portable never asks: it is scm only", "SoundTouch Portable", "", "10.0.11", nil, StatusLadder, "14.00.33 16.00.09 27.0.6"},
		{"sm2 below 9 cannot exist, so no moduleType at 7 is scm", "SoundTouch 30", "", "7.2.21", nil, StatusLadder, "14.00.33 16.00.09 27.0.6"},
		{"sm2 reported gets the Bluetooth chassis ladder", "SoundTouch 20", "sm2", "14.0.15", nil, StatusLadder, "14.00.33 15.00.20 27.0.6"},
		{"SoundTouch 10 gets its own checked ladder", "SoundTouch 10", "sm2", "14.0.15", nil, StatusLadder, "14.00.33 15.00.20 27.0.6"},
		{"no version asks for it", "SoundTouch 20", "", "", nil, StatusNeedsVersion, ""},
		{"a 2.x ST20 gets the full ladder", "SoundTouch 20", "", "2.1.51", nil, StatusLadder, "06.02.01 07.00.37 14.00.33 16.00.09 27.0.6"},
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
	if p := Plan("Lifestyle", "sm2", "", "10.0.1", nil); p.Status != StatusUnsupported || p.Reason != "model" {
		t.Errorf("Lifestyle: %s %q", p.Status, p.Reason)
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
		"14.0.33": "16.00.09",
		"16.0.9":  "27.0.6",
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
	if got := versions(FallbackFor(FamilySTScm+"-27.0.6", "16.0.9")); got != "20.00.06 27.0.6" {
		t.Errorf("16.x fallback before 27.0.6 %q", got)
	}
	// docs/FIRMWARE-UPDATE.md, section 7, row "15.x or newer": 20.00.06 first
	// if 27.0.6 stalls, from 15 as from 16.
	if got := versions(FallbackFor(FamilySTScm+"-27.0.6", "15.0.20")); got != "20.00.06 27.0.6" {
		t.Errorf("15.x fallback before 27.0.6 %q", got)
	}
	if FallbackFor(FamilySTScm+"-27.0.6", "20.0.6") != nil {
		t.Error("20.0.6 has no smaller step before 27.0.6")
	}
	if fb := Plan("SoundTouch 20", "scm", "", "16.0.9", nil).Fallbacks; versions(fb[FamilySTScm+"-27.0.6"]) != "20.00.06 27.0.6" {
		t.Error("a 16.x speaker is offered 20.00.06 when 27.0.6 stalls")
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
	if len(mainLadders) != 6 {
		t.Errorf("%d main ladders, the tests know 6", len(mainLadders))
	}
	for fam, ladder := range mainLadders {
		prev := ""
		for i, s := range ladder {
			if s.Family != fam {
				t.Errorf("%s: in the %s ladder", s.ID, fam)
			}
			if prev != "" && CompareVersions(s.Version, prev) <= 0 {
				t.Errorf("%s: versions must go strictly up (after %s)", s.ID, prev)
			}
			prev = s.Version
			last := i == len(ladder)-1
			if last != (s.Source == SourceBose) {
				t.Errorf("%s: only the last step comes from Bose, and it must", s.ID)
			}
			if last && (s.Version != LatestShort || !s.NetworkOK) {
				t.Errorf("%s: the last step is Bose's %s, which may go over the network", s.ID, LatestShort)
			}
			if s.NetworkOK && s.Source != SourceBose {
				t.Errorf("%s: an archive step must never go over the network", s.ID)
			}
			if s.Fallback {
				t.Errorf("%s: a main step is not a fallback", s.ID)
			}
			// Below 15 Bose's last file never goes on in one jump: an archive
			// step no newer than 15.x must come before it (the Wave IV ladder
			// starts at 15.00.20 itself, for a speaker already on 14.0.33).
			if last && (i == 0 || ladder[0].Source != SourceArchive || CompareVersions(ladder[0].Version, "16.0.0") >= 0) {
				t.Errorf("%s: the ladder must start with an in-between step no newer than 15", fam)
			}
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
		if img, known := knownImages[s.InnerCRC32]; known && !img.carriedBy(s.Family) {
			t.Errorf("%s: inner CRC %08x is another family's image (%s)", s.ID, s.InnerCRC32, img.What)
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
	var fallbacks []Step
	for _, l := range [][]Step{stScmFallback, st10Fallback, btSm2Fallback, sa5Fallback, waveSm2Fallback} {
		fallbacks = append(fallbacks, l...)
	}
	for _, s := range fallbacks {
		if !s.Fallback || s.LiveVerified {
			t.Errorf("%s: fallback steps are marked as such and untested", s.ID)
		}
	}
	// Live evidence so far: only 06.02.01, 07.00.37, 14.00.33 and 16.00.09, by
	// stick, on a SoundTouch 20 Series I (15.00.20 stalled there). The network
	// flash to 27.0.6 was seen from 27.0.3 only, so no ladder's last step is
	// "tested" yet, and nothing of the SoundTouch 10 ladder is.
	live := map[string]bool{
		FamilySTScm + "-06.02.01": true, FamilySTScm + "-07.00.37": true,
		FamilySTScm + "-14.00.33": true, FamilySTScm + "-16.00.09": true,
	}
	for _, s := range AllSteps() {
		if s.LiveVerified != live[s.ID] {
			t.Errorf("%s: LiveVerified %v without a field report", s.ID, s.LiveVerified)
		}
	}
}

// The SoundTouch 10 values, as derived on 2026-11-22 (ladder_data.go). Each
// archive step's inner size and CRC-32 is the one the zip's central directory
// lists, and the two this test can tie to a Bose catalogue must match it.
func TestST10LadderValues(t *testing.T) {
	// Bose's 14.0.33 catalogue (mr1_2017), DEVICE 0x0939 "SoundTouch 10".
	const bose14033Size, bose14033CRC = 79892339, 0xb5300c87
	s, ok := StepByID(FamilyST10Sm2 + "-14.00.33")
	if !ok || s.InnerSize != bose14033Size || s.InnerCRC32 != bose14033CRC {
		t.Errorf("14.00.33 is not Bose's file for the SoundTouch 10: %+v", s)
	}
	if !strings.Contains(s.URL, "/Bluetooth_ST20-30_or_Wireless_Link_Adapter/") || !strings.Contains(s.FileName, "_14.00.33.34225.2189603.sm2.") {
		t.Errorf("14.00.33 must be the shared sm2 image: %s", s.URL)
	}
	// Every later ST10 archive step is the rhino image from the ST10 folder,
	// never the shared ST20/30 one.
	for _, s := range AllSteps() {
		if s.Family != FamilyST10Sm2 || s.Source != SourceArchive || s.Version == "14.00.33" {
			continue
		}
		if !strings.Contains(s.URL, "/Bluetooth_ST10/") || !strings.HasSuffix(s.FileName, ".rhino.sm2.zip") {
			t.Errorf("%s: not the SoundTouch 10's own image: %s", s.ID, s.URL)
		}
	}
	// The final step is Bose's current catalogue entry for the SoundTouch 10.
	fin := st10Ladder[len(st10Ladder)-1]
	files := Match(Builtin, "SoundTouch 10", "sm2", "")
	if len(files) != 1 || files[0].Name != fin.FileName || files[0].Length != fin.InnerSize || files[0].CRC != fin.InnerCRC32 {
		t.Errorf("the ST10's last step %+v is not Bose's file %+v", fin, files)
	}
}

func TestPlanST10(t *testing.T) {
	cases := []struct {
		name, mt, version string
		status, steps     string
	}{
		{"9.0.41, the field case", "sm2", "9.0.41.23338.1383271", StatusLadder, "14.00.33 15.00.20 27.0.6"},
		{"9.0.41 without moduleType", "", "9.0.41", StatusLadder, "14.00.33 15.00.20 27.0.6"},
		{"14.0.15 goes via 14.00.33 too (#854)", "sm2", "14.0.15", StatusLadder, "14.00.33 15.00.20 27.0.6"},
		{"14.0.33", "sm2", "14.0.33", StatusLadder, "15.00.20 27.0.6"},
		{"15.0.20 gets the final step only", "sm2", "15.0.20", StatusFinal, "27.0.6"},
		{"27.0.6 is done", "sm2", "27.0.6", StatusUpToDate, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Plan("SoundTouch 10", c.mt, "", c.version, nil)
			if p.Status != c.status {
				t.Fatalf("status %q, want %q", p.Status, c.status)
			}
			if got := versions(p.Steps); got != c.steps {
				t.Fatalf("steps %q, want %q", got, c.steps)
			}
			if c.steps != "" && (p.Family != FamilyST10Sm2 || p.ModuleType != "sm2") {
				t.Errorf("family %q moduleType %q", p.Family, p.ModuleType)
			}
			for _, s := range p.Steps {
				if s.Family != FamilyST10Sm2 || s.LiveVerified {
					t.Errorf("%s: an ST10 step, not tested on a speaker yet", s.ID)
				}
				// The app prepares the stick from the step ID alone.
				if got, ok := StepByID(s.ID); !ok || got != s {
					t.Errorf("%s: no stick could be prepared for it", s.ID)
				}
			}
		})
	}

	p := Plan("SoundTouch 10", "sm2", "", "9.0.41", nil)
	if p.ArchiveSteps() != 2 || p.Steps[0].Source != SourceArchive || p.Steps[len(p.Steps)-1].Source != SourceBose {
		t.Errorf("9.0.41: %+v", p.Steps)
	}
	if fb := versions(p.Fallbacks[FamilyST10Sm2+"-15.00.20"]); fb != "16.00.09" || len(p.Fallbacks) != 1 {
		t.Errorf("if 15.00.20 stalls, 16.00.09 takes its place: %v", p.Fallbacks)
	}
	// Walking the plan as the speaker reports each step.
	for rep, want := range map[string]string{"9.0.41": "14.00.33", "14.0.33": "15.00.20", "15.0.20": "27.0.6", "16.0.9": "27.0.6"} {
		if s, ok := Next(p, rep); !ok || s.Version != want {
			t.Errorf("Next(%q) = %q, want %q", rep, s.Version, want)
		}
	}
	if FallbackFor(FamilyST10Sm2+"-15.00.20", "15.0.20") != nil {
		t.Error("from 15 on there is nothing to swap")
	}
	// An ST10 that says scm is no speaker Bose built: no path, never the
	// first generation ladder.
	if p := Plan("SoundTouch 10", "scm", "", "9.0.41", nil); p.Status != StatusUnsupported || p.Family != "" {
		t.Errorf("ST10 scm: %s %s", p.Status, p.Family)
	}
}

// The rest of the Bluetooth chassis, as derived on 2026-11-22
// (ladder_data.go): per family the archive folder and line its steps come
// from, and the 14.0.33 file Bose's own catalogue gives the model.
func TestSm2LadderValues(t *testing.T) {
	type want struct {
		model, folder, line string // a model name, its archive folder, the file name's line
		bose14033Size       int64  // 0: no checked 14.00.33 (Wave IV)
		bose14033CRC        uint32
		fallback            bool // a 16.00.09 exists in the folder
	}
	fams := map[string]want{
		FamilyBTSm2:    {"SoundTouch 20", "/Bluetooth_ST20-30_or_Wireless_Link_Adapter/", ".sm2.zip", 79892339, 0xb5300c87, true},
		FamilySA5Sm2:   {"SoundTouch SA-5", "/Bluetooth_SA5/", ".burns.sm2.zip", 81409611, 0x85f253e2, true},
		FamilyST300Sm2: {"SoundTouch 300", "/Bluetooth_ST300/", ".ginger.sm2.zip", 82612595, 0x7647c0be, false},
		FamilyWaveSm2:  {"Wave SoundTouch music system IV", "/Bluetooth_Wave_SoundTouch_IV/", ".nelson.sm2.zip", 0, 0, true},
	}
	for fam, w := range fams {
		ladder := mainLadders[fam]
		if len(ladder) == 0 {
			t.Fatalf("%s: no ladder", fam)
		}
		for _, s := range AllSteps() {
			if s.Family != fam {
				continue
			}
			// Every image is one whose model is known, and it is this
			// family's: the archive misfiles images across models.
			img, known := knownImages[s.InnerCRC32]
			if !known || !img.carriedBy(fam) {
				t.Errorf("%s: inner CRC %08x is not a known image of this family (%+v)", s.ID, s.InnerCRC32, img)
			}
			if s.LiveVerified {
				t.Errorf("%s: not tested on a speaker yet", s.ID)
			}
			if s.Source != SourceArchive {
				continue
			}
			if !strings.Contains(s.URL, w.folder) {
				t.Errorf("%s: not from %s: %s", s.ID, w.folder, s.URL)
			}
			// 14.00.33 zips are named by the archive's own scheme; the later
			// ones carry the model's line in the name.
			if s.Version != "14.00.33" && !strings.HasSuffix(s.FileName, w.line) {
				t.Errorf("%s: %s is not the %s line", s.ID, s.FileName, w.line)
			}
		}
		s, ok := StepByID(fam + "-14.00.33")
		switch {
		case w.bose14033Size == 0 && ok:
			t.Errorf("%s: no checked 14.00.33 exists for it, the archive's is the SA-5's", fam)
		case w.bose14033Size != 0 && (!ok || s.InnerSize != w.bose14033Size || s.InnerCRC32 != w.bose14033CRC):
			t.Errorf("%s: 14.00.33 is not Bose's file for it: %+v", fam, s)
		}
		if _, ok := StepByID(fam + "-16.00.09"); ok != w.fallback {
			t.Errorf("%s: a 16.00.09 fallback %v, want %v", fam, ok, w.fallback)
		}
		// The final step is Bose's current catalogue entry for the model.
		fin := ladder[len(ladder)-1]
		files := Match(Builtin, w.model, "sm2", "")
		if len(files) != 1 || files[0].Name != fin.FileName || files[0].Length != fin.InnerSize || files[0].CRC != fin.InnerCRC32 {
			t.Errorf("%s: the last step %+v is not Bose's file %+v", fam, fin, files)
		}
	}
	// The Wireless Link adapter takes the SoundTouch 20/30 file, as Bose's
	// catalogue gives it at 14.0.33 and 27.0.6.
	files := Match(Builtin, "SoundTouch Wireless Link adapter", "sm2", "")
	if fin := btSm2Ladder[len(btSm2Ladder)-1]; len(files) != 1 || files[0].CRC != fin.InnerCRC32 {
		t.Errorf("Wireless Link: %+v", files)
	}
	// The SA-5's 14.00.33 must never reach a Wave IV.
	for _, s := range AllSteps() {
		if s.Family == FamilyWaveSm2 && s.InnerCRC32 == 0x85f253e2 {
			t.Errorf("%s: the SA-5's file on the Wave IV ladder", s.ID)
		}
	}
}

func TestPlanSm2(t *testing.T) {
	cases := []struct {
		name, model, mt, version string
		bt                       *bool
		status, family, steps    string
		fallback                 string // the fallback for 15.00.20, "" for none
	}{
		{"ST20 sm2 on 9.0.41", "SoundTouch 20", "sm2", "9.0.41.23338.1383271", nil, StatusLadder, FamilyBTSm2, "14.00.33 15.00.20 27.0.6", "16.00.09"},
		{"ST30 with a Bluetooth button, no moduleType", "SoundTouch 30", "", "12.0.1", boolp(true), StatusLadder, FamilyBTSm2, "14.00.33 15.00.20 27.0.6", "16.00.09"},
		{"ST20 sm2 on 14.0.33", "SoundTouch 20", "sm2", "14.0.33", nil, StatusLadder, FamilyBTSm2, "15.00.20 27.0.6", "16.00.09"},
		{"Wireless Link without moduleType", "SoundTouch Wireless Link Adapter", "", "13.0.5", nil, StatusLadder, FamilyBTSm2, "14.00.33 15.00.20 27.0.6", "16.00.09"},
		{"SA-5 without moduleType", "SoundTouch SA-5", "", "12.0.1", nil, StatusLadder, FamilySA5Sm2, "14.00.33 15.00.20 27.0.6", "16.00.09"},
		{"SA-5 on 14.0.33", "SoundTouch SA-5", "sm2", "14.0.33", nil, StatusLadder, FamilySA5Sm2, "15.00.20 27.0.6", "16.00.09"},
		{"SoundTouch 300 on 14.0.15 (#854 shape)", "SoundTouch 300", "sm2", "14.0.15", nil, StatusLadder, FamilyST300Sm2, "14.00.33 15.00.20 27.0.6", ""},
		{"SoundTouch 300 on 14.0.33", "SoundTouch 300", "", "14.0.33", nil, StatusLadder, FamilyST300Sm2, "15.00.20 27.0.6", ""},
		{"Wave IV on 14.0.33", "Wave SoundTouch music system IV", "sm2", "14.0.33", nil, StatusLadder, FamilyWaveSm2, "15.00.20 27.0.6", "16.00.09"},
		{"Wave IV below 14.0.33: no source", "Wave SoundTouch", "sm2", "14.0.15", nil, StatusUnsupported, FamilyWaveSm2, "", ""},
		{"SA-5 on 15.0.20: the final step", "SoundTouch SA-5", "sm2", "15.0.20", nil, StatusFinal, FamilySA5Sm2, "27.0.6", ""},
		{"ST20 sm2 on 16.0.9: the final step", "SoundTouch 20", "sm2", "16.0.9", nil, StatusFinal, FamilyBTSm2, "27.0.6", ""},
		{"Wave IV on 15.0.20: the final step", "Wave SoundTouch", "sm2", "15.0.20", nil, StatusFinal, FamilyWaveSm2, "27.0.6", ""},
		{"SoundTouch 300 on 27.0.6 is done", "SoundTouch 300", "sm2", "27.0.6", nil, StatusUpToDate, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Plan(c.model, c.mt, "", c.version, c.bt)
			if p.Status != c.status || p.Family != c.family {
				t.Fatalf("status %q family %q, want %q %q", p.Status, p.Family, c.status, c.family)
			}
			if got := versions(p.Steps); got != c.steps {
				t.Fatalf("steps %q, want %q", got, c.steps)
			}
			if c.family != "" && p.ModuleType != "sm2" {
				t.Errorf("moduleType %q", p.ModuleType)
			}
			if got := versions(p.Fallbacks[c.family+"-15.00.20"]); got != c.fallback {
				t.Errorf("fallback for 15.00.20 %q, want %q", got, c.fallback)
			}
			for _, s := range p.Steps {
				if s.Family != c.family || s.LiveVerified {
					t.Errorf("%s: a %s step, not tested on a speaker yet", s.ID, c.family)
				}
				// The app prepares the stick from the step ID alone.
				if got, ok := StepByID(s.ID); !ok || got != s {
					t.Errorf("%s: no stick could be prepared for it", s.ID)
				}
			}
			if c.status == StatusUnsupported && p.Reason != "no-source" {
				t.Errorf("reason %q", p.Reason)
			}
		})
	}
	// Speakers Bose never built: no path, never another family's files.
	for _, c := range [][3]string{
		{"SoundTouch SA-5", "scm", "12.0.1"},
		{"SoundTouch 300", "scm", "12.0.1"},
		{"SoundTouch Portable", "sm2", "12.0.1"},
	} {
		if p := Plan(c[0], c[1], "", c[2], nil); p.Status != StatusUnsupported || p.Family != "" {
			t.Errorf("%v: %s %s", c, p.Status, p.Family)
		}
	}
	// A Wave without moduleType from 9 on cannot be told apart.
	if p := Plan("Wave SoundTouch", "", "", "14.0.33", nil); p.Status != StatusUnsupported || p.Reason != "generation" {
		t.Errorf("Wave without moduleType: %s %q", p.Status, p.Reason)
	}
	// From 15 the SoundTouch 300 has nothing to swap, the others 16.00.09
	// only below 15.
	if FallbackFor(FamilyST300Sm2+"-15.00.20", "14.0.33") != nil || FallbackFor(FamilyBTSm2+"-15.00.20", "15.0.20") != nil {
		t.Error("no swap for the 300, none from 15 on")
	}
}
