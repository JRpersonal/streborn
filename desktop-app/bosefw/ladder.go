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
// Phase 1 knows one family only (FamilySTScm). Every other model gets
// StatusUnsupported here and keeps the single-step guide.

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
	StatusUnsupported    = "unsupported"     // no step-by-step path for this model yet
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
	// Reason details StatusUnsupported: "model" (no ladder for this model yet)
	// or "sm2" (newer hardware, no ladder yet).
	Reason string `json:"reason,omitempty"`
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
	if cur[0] >= 15 {
		// From 15 on, Bose's last file goes on directly, for every model: the
		// normal guide with its one-click update handles it.
		p.Status = StatusFinal
		if stScmModels[strings.ToLower(p.Model)] && p.ModuleType != "sm2" {
			p.Family = FamilySTScm
			p.Steps = []Step{stScmLadder[len(stScmLadder)-1]}
			if fb := FallbackFor(p.Steps[0].ID, p.Current); fb != nil {
				p.Fallbacks[p.Steps[0].ID] = fb
			}
		}
		return p
	}
	if !stScmModels[strings.ToLower(p.Model)] {
		p.Status, p.Reason = StatusUnsupported, "model"
		return p
	}
	switch p.ModuleType {
	case "sm2":
		p.Status, p.Reason = StatusUnsupported, "sm2"
		return p
	case "scm":
	default:
		// No moduleType. Below 9 it can only be scm: no sm2 image older than
		// 9.00.41 exists. The Portable was only ever built on scm.
		isPortable := strings.EqualFold(p.Model, "soundtouch portable")
		switch {
		case cur[0] < 9 || isPortable:
		case bluetooth == nil:
			p.Status = StatusNeedsBluetooth
			return p
		case *bluetooth:
			p.Status, p.Reason, p.ModuleType = StatusUnsupported, "sm2", "sm2"
			return p
		}
		p.ModuleType = "scm"
	}
	p.Family = FamilySTScm
	p.OldStart = cur[0] < 5
	for _, s := range stScmLadder {
		if CompareVersions(s.Version, p.Current) <= 0 {
			continue
		}
		// A 6.x speaker already has the new container format: it goes
		// straight to 07.00.37, as the archive research and the field did.
		if cur[0] == 6 && s.Version == "06.02.01" {
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
// on `current`, ending with the step that stalled. Nil when there are none.
func FallbackFor(stepID, current string) []Step {
	cur, ok := VersionTuple(current)
	if !ok {
		return nil
	}
	pick := func(versions ...string) []Step {
		var out []Step
		for _, v := range versions {
			if s, ok := StepByID(FamilySTScm + "-" + v); ok && CompareVersions(s.Version, current) > 0 {
				out = append(out, s)
			}
		}
		return out
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
		if cur[0] >= 16 && cur[0] < 20 {
			return pick("20.00.06", "27.0.6")
		}
	}
	return nil
}

// StepByID finds a step of any ladder, main or fallback.
func StepByID(id string) (Step, bool) {
	for _, s := range stScmLadder {
		if s.ID == id {
			return s, true
		}
	}
	for _, s := range stScmFallback {
		if s.ID == id {
			return s, true
		}
	}
	return Step{}, false
}

// AllSteps returns every step of every ladder, main first. For tests and the
// maintainer.
func AllSteps() []Step {
	out := append([]Step(nil), stScmLadder...)
	return append(out, stScmFallback...)
}
