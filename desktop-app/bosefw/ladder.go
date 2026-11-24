package bosefw

// The firmware ladder: the step-by-step route from very old SoundTouch
// firmware to Bose's last one.
//
// A speaker on 5.x or 6.x cannot take 27.0.6 in one jump. In the field a
// SoundTouch 20 Series I went 5.x -> 06.02.01 -> 07.00.37 by USB stick, while
// 06.02.01 -> 27.0.6 stalled on an empty progress bar. The update container
// changes at 5 -> 6, and the speaker's own update program changes three more
// times before 15.00.20 (new IPC at 12 and at 14.00.33, new bootloader and
// kernel at 15.00.20). So the ladder lands each new installer generation on a
// system from the generation before it, the hops Bose itself once shipped.
// That generation is reached with 16.00.09: 15.00.20 itself stalled on the
// one speaker that tried it, by stick and on its update page.
//
// The checked ladder knows the first generation SoundTouch 20, 30 and Portable
// (FamilySTScm) and the Bluetooth chassis of the newer hardware: the
// SoundTouch 10 (FamilyST10Sm2), the SoundTouch 20 and 30 with Bluetooth and
// the Wireless Link adapter (FamilyBTSm2), the SA-5 (FamilySA5Sm2), the
// SoundTouch 300 (FamilyST300Sm2) and the Wave IV from 14.0.33
// (FamilyWaveSm2). Every other family the firmware guide covers gets its
// documented, unchecked route as a list (StatusRoute, ladder_routes.go); a
// model with no known path gets StatusUnsupported. None is ever offered Bose's
// last file in one jump from below 15.

import (
	"strconv"
	"strings"
)

// Step sources.
const (
	SourceArchive = "archive" // a copy kept by the Internet Archive
	SourceBose    = "bose"    // Bose's own download host
)

// Plan statuses.
const (
	StatusLadder         = "ladder"          // more than one step: show the ladder
	StatusFinal          = "final"           // only Bose's last file is left: the normal guide
	StatusUpToDate       = "uptodate"        // already on Bose's last firmware
	StatusNeedsVersion   = "needs-version"   // the version is unknown: ask the user
	StatusNeedsBluetooth = "needs-bluetooth" // ST20/30 9.x-14.x without moduleType: ask
	StatusUnsupported    = "unsupported"     // no known step-by-step path for this speaker yet
	// StatusRoute is the documented, unchecked route for a family outside the
	// checked ladder: the steps are listed, the app prepares no stick for them.
	StatusRoute = "route"
)

// Step is one firmware update on the way up.
type Step struct {
	ID      string `json:"id"`      // stable, e.g. "st-scm-07.00.37"
	Version string `json:"version"` // as the source names it, e.g. "07.00.37"
	Family  string `json:"family"`
	Source  string `json:"source"` // SourceArchive or SourceBose
	URL     string `json:"url"`
	// FileName is the zip on the Internet Archive, or Bose's .stu file name.
	FileName string `json:"fileName"`
	ZipSize  int64  `json:"zipSize"` // 0 for Bose's file, which is no zip
	ZipSHA1  string `json:"zipSha1"`
	// InnerSize, InnerCRC32 and InnerSHA1 describe the Update.stu itself, the
	// file that ends up on the stick. InnerSHA1 is empty where it is unknown.
	InnerSize  int64  `json:"innerSize"`
	InnerCRC32 uint32 `json:"innerCrc32"`
	InnerSHA1  string `json:"innerSha1"`
	// Confidence is "high", "medium-high", "medium" or "low".
	Confidence   string `json:"confidence"`
	LiveVerified bool   `json:"liveVerified"` // seen working on a real speaker
	NetworkOK    bool   `json:"networkOk"`    // may go over the network (only Bose's last file)
	Fallback     bool   `json:"fallback"`     // a smaller in-between step, offered only after a stall
}

// LadderPlan is what the guide shows for one speaker.
type LadderPlan struct {
	Status     string `json:"status"`
	Family     string `json:"family"`
	Model      string `json:"model"`
	ModuleType string `json:"moduleType"` // as reported, or as the ladder decided it
	Current    string `json:"current"`    // short version, e.g. "5.2.0"
	DeviceID   string `json:"deviceId"`   // for resuming; empty when unknown
	// VersionFrom says where Current came from: "lan", "setup-ap" or "manual".
	VersionFrom string `json:"versionFrom"`
	// Steps are the updates still to do, oldest first. The last one is Bose's
	// own file.
	Steps []Step `json:"steps"`
	// OldStart is set for a speaker older than 5.x: 06.02.01 from there is
	// untested, and the guide offers 05.00.23 first if the speaker refuses it.
	OldStart bool `json:"oldStart"`
	// Reason details StatusUnsupported: "model" (no known path for this model),
	// "no-source" (a known route, but no source for the first step from this
	// version) or "generation" (the model was built on both hardware
	// generations and the speaker does not say which one it is).
	Reason string `json:"reason,omitempty"`
	// Folder is the Internet Archive folder the firmware guide names for a
	// StatusRoute family, empty when it names none.
	Folder string `json:"folder"`
	// Avoid lists the files in that folder that belong to another model, as
	// the firmware guide lists them.
	Avoid []string `json:"avoid"`
	// Fallbacks maps a step ID to the smaller steps the guide offers when that
	// step leaves the speaker on its old version (FallbackFor). Only steps
	// that have such a route are listed.
	Fallbacks map[string][]Step `json:"fallbacks"`
}

// ArchiveSteps counts the steps that go by USB stick with an archive file.
func (p LadderPlan) ArchiveSteps() int {
	n := 0
	for _, s := range p.Steps {
		if s.Source == SourceArchive {
			n++
		}
	}
	return n
}

// VersionTuple reads the first three numbers of a firmware version
// ("5.2.0.11111.222", "06.02.01", "14.0.15 epdbuild"). ok is false when the
// string does not start with a number.
func VersionTuple(v string) (t [3]int, ok bool) {
	v = strings.TrimSpace(v)
	if f := strings.Fields(v); len(f) > 0 {
		v = f[0]
	}
	parts := strings.Split(v, ".")
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			if i == 0 {
				return t, false
			}
			break
		}
		t[i] = n
		if i == 0 {
			ok = true
		}
	}
	return t, ok
}

// CompareVersions returns -1, 0 or 1 for a < b, a == b, a > b on the first
// three numbers. An unreadable version sorts first.
func CompareVersions(a, b string) int {
	ta, _ := VersionTuple(a)
	tb, _ := VersionTuple(b)
	for i := 0; i < 3; i++ {
		if ta[i] != tb[i] {
			if ta[i] < tb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func shortVersion(v string) string {
	t, ok := VersionTuple(v)
	if !ok {
		return ""
	}
	return strconv.Itoa(t[0]) + "." + strconv.Itoa(t[1]) + "." + strconv.Itoa(t[2])
}

// stScmModels are the models of phase 1, as a speaker's <type> names them.
var stScmModels = map[string]bool{
	"soundtouch 20":       true,
	"soundtouch 30":       true,
	"soundtouch portable": true,
}

// Plan works out the ladder for a speaker. model is its <type>, moduleType
// "scm", "sm2" or "" (old firmware may not report it), version as reported or
// picked by the user. bluetooth answers "Does your speaker have a Bluetooth
// button?" (nil while unasked): only the ST20 and ST30 exist on both hardware
// generations, and only sm2 has the button.
func Plan(model, moduleType, variant, version string, bluetooth *bool) LadderPlan {
	_ = variant // the phase-1 family is told apart by model and moduleType
	p := LadderPlan{
		Model:      strings.TrimSpace(model),
		ModuleType: strings.ToLower(strings.TrimSpace(moduleType)),
		Current:    shortVersion(version),
		Steps:      []Step{},
		Fallbacks:  map[string][]Step{},
		Avoid:      []string{},
	}
	cur, ok := VersionTuple(version)
	if !ok {
		p.Status = StatusNeedsVersion
		return p
	}
	if CompareVersions(p.Current, LatestShort) >= 0 {
		p.Status = StatusUpToDate
		return p
	}
	key := modelKey(p.Model)
	fam := sm2Family(key, p.ModuleType)
	if cur[0] >= 15 {
		// From 15 on, Bose's last file goes on directly, for every model: the
		// normal guide with its one-click update handles it. A checked
		// family also gets the final step itself, for the stick route.
		p.Status = StatusFinal
		var final Step
		switch {
		case fam != "":
			p.Family, p.ModuleType = fam, "sm2"
			final = mainLadders[fam][len(mainLadders[fam])-1]
		case stScmModels[key] && p.ModuleType != "sm2":
			p.Family = FamilySTScm
			final = stScmLadder[len(stScmLadder)-1]
		default:
			return p
		}
		p.Steps = []Step{final}
		if fb := FallbackFor(final.ID, p.Current); fb != nil {
			p.Fallbacks[final.ID] = fb
		}
		return p
	}
	if fam != "" {
		return walkSm2(p, fam)
	}
	if !stScmModels[key] {
		return planRoute(p, cur)
	}
	switch p.ModuleType {
	case "sm2":
		// A Portable that says sm2: Bose built none, so no path is known.
		return planRoute(p, cur)
	case "scm":
	default:
		// No moduleType. Below 9 it can only be scm: no sm2 image older than
		// 9.00.41 exists. The Portable was only ever built on scm.
		isPortable := key == "soundtouch portable"
		switch {
		case cur[0] < 9 || isPortable:
		case bluetooth == nil:
			p.Status = StatusNeedsBluetooth
			return p
		case *bluetooth:
			// The newer hardware: the Bluetooth chassis ladder.
			return walkSm2(p, FamilyBTSm2)
		}
		p.ModuleType = "scm"
	}
	p.Family = FamilySTScm
	p.OldStart = cur[0] < 5
	return walkLadder(p, stScmLadder, func(s Step) bool {
		// A 6.x speaker already has the new container format: it goes
		// straight to 07.00.37, as the archive research and the field did.
		return cur[0] == 6 && s.Version == "06.02.01"
	})
}

// sm2Ladder names the checked ladders of the newer (sm2) hardware.
var sm2Ladder = map[string]bool{
	FamilyST10Sm2: true, FamilyBTSm2: true, FamilySA5Sm2: true, FamilyST300Sm2: true, FamilyWaveSm2: true,
}

// sm2Families maps a modelKey to its checked ladder on the newer hardware.
var sm2Families = map[string]string{
	"soundtouch 10":   FamilyST10Sm2,
	"soundtouch 20":   FamilyBTSm2,
	"soundtouch 30":   FamilyBTSm2,
	"wireless link":   FamilyBTSm2,
	"soundtouch sa-5": FamilySA5Sm2,
	"soundtouch 300":  FamilyST300Sm2,
	"wave soundtouch": FamilyWaveSm2,
}

// sm2Family returns the checked newer-hardware ladder for a speaker, or "".
// A model Bose built only on that hardware (sm2Only) counts without a
// moduleType, and one that says "scm" is no speaker Bose built. The
// SoundTouch 20, 30 and the Wave exist on both generations, so the speaker
// (or the Bluetooth answer, Plan) has to say sm2.
func sm2Family(key, moduleType string) string {
	fam := sm2Families[key]
	switch {
	case fam == "":
		return ""
	case sm2Only[key]:
		if moduleType == "scm" {
			return ""
		}
		return fam
	case moduleType == "sm2":
		return fam
	}
	return ""
}

// walkSm2 fills p with a newer-hardware ladder. The Wave IV has no checked
// source for 14.00.33 (ladder_data.go), so below 14.0.33 it has no path.
func walkSm2(p LadderPlan, fam string) LadderPlan {
	p.Family, p.ModuleType = fam, "sm2"
	if fam == FamilyWaveSm2 && CompareVersions(p.Current, waveSm2From) < 0 {
		p.Status, p.Reason = StatusUnsupported, "no-source"
		return p
	}
	return walkLadder(p, mainLadders[fam], nil)
}

// walkLadder fills p with the steps of ladder above p.Current, oldest first,
// leaving out those skip names, and their fallbacks.
func walkLadder(p LadderPlan, ladder []Step, skip func(Step) bool) LadderPlan {
	for _, s := range ladder {
		if CompareVersions(s.Version, p.Current) <= 0 || (skip != nil && skip(s)) {
			continue
		}
		p.Steps = append(p.Steps, s)
		if fb := FallbackFor(s.ID, p.Current); fb != nil {
			p.Fallbacks[s.ID] = fb
		}
	}
	if len(p.Steps) > 1 {
		p.Status = StatusLadder
	} else {
		p.Status = StatusFinal
	}
	return p
}

// LatestShort is LatestRelease as major.minor.patch.
const LatestShort = "27.0.6"

// Next returns the step a speaker that now reports `reported` has to take next
// on this plan, and false when the plan is done.
func Next(plan LadderPlan, reported string) (Step, bool) {
	if _, ok := VersionTuple(reported); !ok {
		if len(plan.Steps) > 0 {
			return plan.Steps[0], true
		}
		return Step{}, false
	}
	for _, s := range plan.Steps {
		if CompareVersions(s.Version, reported) > 0 {
			return s, true
		}
	}
	return Step{}, false
}

// Reached reports whether a speaker that reports `reported` has finished step.
func Reached(step Step, reported string) bool {
	if _, ok := VersionTuple(reported); !ok {
		return false
	}
	return CompareVersions(reported, step.Version) >= 0
}

// FallbackFor returns the smaller steps to try when `stepID` left a speaker
// on `current`, ending with the step that stalled or with the step that takes
// its place. Nil when there are none.
func FallbackFor(stepID, current string) []Step {
	cur, ok := VersionTuple(current)
	if !ok {
		return nil
	}
	pickIn := func(family string, versions ...string) []Step {
		var out []Step
		for _, v := range versions {
			if s, ok := StepByID(family + "-" + v); ok && CompareVersions(s.Version, current) > 0 {
				out = append(out, s)
			}
		}
		return out
	}
	pick := func(versions ...string) []Step { return pickIn(FamilySTScm, versions...) }
	if fam, ok := strings.CutSuffix(stepID, "-15.00.20"); ok && sm2Ladder[fam] {
		// Not a smaller step in between but the swap that worked on the scm
		// ladder: 16.00.09 in place of a 15.00.20 that stalled. It ends the
		// list, and the plan goes on with 27.0.6 after it. A family whose
		// archive folder has no 16.00.09 (the SoundTouch 300) gets none.
		if cur[0] < 15 {
			return pickIn(fam, "16.00.09")
		}
		return nil
	}
	switch stepID {
	case FamilySTScm + "-06.02.01":
		if cur[0] < 5 {
			return pick("05.00.23", "06.02.01")
		}
	case FamilySTScm + "-14.00.33":
		if cur[0] >= 7 && cur[0] < 12 {
			return pick("09.00.41", "12.00.09", "14.00.33")
		}
	case FamilySTScm + "-27.0.6":
		// docs/FIRMWARE-UPDATE.md, section 7: "15.x or newer: 27.0.6 ...; if it
		// stalls, 20.00.06 first".
		if cur[0] >= 15 && cur[0] < 20 {
			return pick("20.00.06", "27.0.6")
		}
	}
	return nil
}

// mainLadders are the checked ladders, by family.
var mainLadders = map[string][]Step{
	FamilySTScm:    stScmLadder,
	FamilyST10Sm2:  st10Ladder,
	FamilyBTSm2:    btSm2Ladder,
	FamilySA5Sm2:   sa5Ladder,
	FamilyST300Sm2: st300Ladder,
	FamilyWaveSm2:  waveSm2Ladder,
}

// StepByID finds a step of any ladder, main or fallback.
func StepByID(id string) (Step, bool) {
	for _, s := range AllSteps() {
		if s.ID == id {
			return s, true
		}
	}
	return Step{}, false
}

// AllSteps returns every step of every ladder, main first. For tests and the
// maintainer.
func AllSteps() []Step {
	var out []Step
	for _, l := range [][]Step{
		stScmLadder, st10Ladder, btSm2Ladder, sa5Ladder, st300Ladder, waveSm2Ladder,
		stScmFallback, st10Fallback, btSm2Fallback, sa5Fallback, waveSm2Fallback,
	} {
		out = append(out, l...)
	}
	return out
}
