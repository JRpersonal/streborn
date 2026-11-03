package bosefw

// The probable routes for every model family docs/FIRMWARE-UPDATE.md covers
// beyond the checked stick ladder (ladder_data.go): section 7, "Other models:
// no checked ladder yet". For these the route is known in outline, but the
// file names and checksums of the in-between steps were never pulled, so the
// app can only LIST the steps (StatusRoute) and point at the guide. It never
// prepares a stick for them and never offers Bose's last file in one jump
// from below 15, which the guide says stalls (section 2).
//
// Every version here is copied from that table, and ladder_routes_test.go
// reads the table back from the doc and fails when the two drift apart. The
// only checksums are the ones the table lists under "Known values".
//
// One step is read from the "Never use" column rather than the route column:
// the archive's Wave IV 14.00.33 is the SA-5's file, so no usable copy of that
// step is known, and a Wave IV below 14.0.33 gets "no-source" like the SA-4
// below 14. The doc row says so too.

import "strings"

// The route families.
const (
	FamilyWaveScm     = "wave-scm"     // Wave SoundTouch, first generation
	FamilySA4Scm      = "sa4-scm"      // SA-4 and Stereo JC, first generation
	FamilyCinemateScm = "cinemate-scm" // CineMate adapter, first generation
	FamilyBTSm2       = "bt-sm2"       // SoundTouch 20/30 with Bluetooth, SA-5, 300, Wireless Link
	FamilyWaveSm2     = "wave-sm2"     // Wave IV
	FamilyST10Sm2     = "st10-sm2"     // SoundTouch 10
)

// innerFP is the size and CRC-32 of an Update.stu.
type innerFP struct {
	Size int64
	CRC  uint32
}

// route is one row of the doc's "Other models" table.
type route struct {
	Family     string
	Models     []string // modelKey values
	ModuleType string   // "scm" or "sm2"
	// Versions are the archive steps before Bose's file, oldest first, spelt
	// as the doc spells them.
	Versions []string
	// NoSourceBelow: a speaker older than this has no known source for its
	// first step. Empty when every start has one.
	NoSourceBelow string
	// Folder is the Internet Archive folder the doc names for this family.
	Folder string
	// Avoid lists the files in that folder that belong to another model.
	Avoid []string
	// Known holds the inner fingerprints the doc lists, by version.
	Known map[string]innerFP
}

var routes = []route{
	{
		Family: FamilyWaveScm, Models: []string{"wave soundtouch"}, ModuleType: "scm",
		Versions: []string{"06.02.01", "07.00.37", "14.00.33", "15.00.20"},
		Folder:   "Airplay-1_Wave_SoundTouch",
		Avoid:    []string{"16.00.09", "18.x", "19.x", "20.x", "21.x", "22.x", "23.x", "24.x"},
		Known:    map[string]innerFP{"15.00.20": {107842648, 0x686cad33}},
	},
	{
		Family: FamilySA4Scm, Models: []string{"soundtouch sa-4", "soundtouch stereo jc"}, ModuleType: "scm",
		Versions:      []string{"14.0.33", "15.0.20"},
		NoSourceBelow: "14.0.0",
		Folder:        "Airplay-1_SA4_or_STJC",
		Avoid:         []string{"14.0.15", "16.0.09", "17.0.08", "18.x", "19.x", "22.x", "23.x", "24.x"},
		Known:         map[string]innerFP{"15.0.20": {106491816, 0xf6ef441d}},
	},
	{
		Family: FamilyCinemateScm, Models: []string{"cinemate"}, ModuleType: "scm",
		Versions: []string{"06.00.91", "07.00.37", "14.00.33", "15.00.20"},
	},
	{
		Family: FamilyBTSm2, Models: []string{"soundtouch 20", "soundtouch 30", "soundtouch sa-5", "soundtouch 300", "wireless link"}, ModuleType: "sm2",
		Versions: []string{"14.00.33", "15.00.20"},
	},
	{
		Family: FamilyWaveSm2, Models: []string{"wave soundtouch"}, ModuleType: "sm2",
		Versions:      []string{"14.00.33", "15.00.20"},
		NoSourceBelow: "14.0.33",
		Avoid:         []string{"14.00.33"},
	},
	{
		Family: FamilyST10Sm2, Models: []string{"soundtouch 10"}, ModuleType: "sm2",
		Versions: []string{"15.00.20"},
	},
}

// sm2Only are the models Bose only ever built on the newer hardware, so a
// missing moduleType still names the generation.
var sm2Only = map[string]bool{
	"soundtouch 10":   true,
	"soundtouch sa-5": true,
	"soundtouch 300":  true,
	"wireless link":   true,
}

// modelKey folds the names a speaker may report onto one key: a Wave can say
// "Wave SoundTouch music system IV", the adapter "SoundTouch Wireless Link
// Adapter", a CineMate may carry its number.
func modelKey(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(m, "wave soundtouch"):
		return "wave soundtouch"
	case strings.Contains(m, "wireless link"):
		return "wireless link"
	case m == "cinemate" || strings.HasPrefix(m, "cinemate "):
		return "cinemate"
	}
	return m
}

func findRoute(key, moduleType string) (route, bool) {
	for _, r := range routes {
		if r.ModuleType != moduleType {
			continue
		}
		for _, m := range r.Models {
			if m == key {
				return r, true
			}
		}
	}
	return route{}, false
}

func hasAnyRoute(key string) bool {
	_, scm := findRoute(key, "scm")
	_, sm2 := findRoute(key, "sm2")
	return scm || sm2
}

// planRoute fills p for a speaker below 15 that is not on the checked ladder.
// p.ModuleType is "scm", "sm2" or "" as reported (or as the Bluetooth answer
// decided it).
func planRoute(p LadderPlan, cur [3]int) LadderPlan {
	key := modelKey(p.Model)
	mt := p.ModuleType
	if mt != "scm" && mt != "sm2" {
		switch {
		case sm2Only[key]:
			mt = "sm2"
		case cur[0] < 9:
			// No sm2 image older than 9.00.41 exists (see Plan).
			mt = "scm"
		case hasAnyRoute(key):
			// Built on both hardware generations, which need different
			// files, and the speaker does not say which one it is.
			p.Status, p.Reason = StatusUnsupported, "generation"
			return p
		default:
			p.Status, p.Reason = StatusUnsupported, "model"
			return p
		}
	}
	p.ModuleType = mt
	r, ok := findRoute(key, mt)
	if !ok {
		p.Status, p.Reason = StatusUnsupported, "model"
		return p
	}
	p.Family = r.Family
	p.Folder = r.Folder
	p.Avoid = append([]string{}, r.Avoid...)
	if r.NoSourceBelow != "" && CompareVersions(p.Current, r.NoSourceBelow) < 0 {
		p.Status, p.Reason = StatusUnsupported, "no-source"
		return p
	}
	for _, v := range r.Versions {
		if CompareVersions(v, p.Current) <= 0 {
			continue
		}
		s := Step{
			ID:         r.Family + "-" + v,
			Version:    v,
			Family:     r.Family,
			Source:     SourceArchive,
			Confidence: "low",
		}
		if fp, ok := r.Known[v]; ok {
			s.InnerSize, s.InnerCRC32 = fp.Size, fp.CRC
		}
		p.Steps = append(p.Steps, s)
	}
	p.Steps = append(p.Steps, routeFinal(r.Family, p.Model, mt))
	p.Status = StatusRoute
	return p
}

// routeFinal is Bose's own 27.0.6 for the speaker, from the built-in copy of
// Bose's catalogue. It goes on through the normal guide once the speaker is
// on 15 or newer. A model the catalogue does not name gets the step without a
// file: the guide then finds Bose's article.
func routeFinal(family, model, moduleType string) Step {
	s := Step{
		ID:         family + "-" + LatestShort,
		Version:    LatestShort,
		Family:     family,
		Source:     SourceBose,
		Confidence: "high",
		NetworkOK:  true,
	}
	if files := Match(Builtin, model, moduleType, ""); len(files) == 1 {
		s.URL, s.FileName, s.InnerSize, s.InnerCRC32 = files[0].URL, files[0].Name, files[0].Length, files[0].CRC
	}
	return s
}

// DirectJumpOK reports whether Bose's last file may go on in one step from
// this version: from 15 on, for every model (docs/FIRMWARE-UPDATE.md,
// section 2). Below that the jump stalls on an empty progress bar or ends in
// a restart loop. An unreadable version gives no evidence either way and is
// not blocked here.
func DirectJumpOK(version string) bool {
	t, ok := VersionTuple(version)
	return !ok || t[0] >= 15
}
