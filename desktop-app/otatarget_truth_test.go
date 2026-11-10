package main

import (
	"fmt"
	"testing"
)

// Four places decide whether a speaker is on the software an update was meant
// to put there, each for its own step of the update:
//
//	skip      agentCurrentEngineMissing  before the push: skip the agent, send only the engine
//	confirms  updateConfirms             the poll after the push, every two seconds
//	classify  classifyAgentVersion       once, when that poll's window ran out
//	reached   speakerReachedTarget       the engine step's "nothing left to do"
//
// This table holds one reading against all four, so a difference between them
// is written down instead of found in the field. Every row a rule changes shows
// up here as a changed line. Where they differ on purpose, the row says why.
//
// The speaker reading of every row lacks its Spotify engine ("goLibrespot":
// "missing" is added for the skip column only, which asks nothing else of it),
// and reached is asked without the engine half, so all four columns answer the
// agent question alone.
func TestUpdateTargetTruthTable(t *testing.T) {
	const (
		shaApp   = testAppSha
		shaOther = testOtherSha

		app      = "v1.5.5"
		appBuild = "2026-11-10-1200"
		// The same commit stamped a minute earlier (the v0.9.88 release skew).
		appBuildSkew = "2026-11-10-1159"

		old      = "v1.5.4"
		oldBuild = "2026-11-08-2036"

		// A local dev build: the version string does not move between two
		// builds of the same checkout, only the stamp and the checksum do.
		dev         = "v1.5.5-8-gabc1234-dirty"
		devBuildOld = "2026-11-10-1100"
		devBuildNew = "2026-11-10-1130"
	)
	release := otaTruthTarget{sha: shaApp, build: appBuild, version: app, preBuild: oldBuild, preVersion: old}
	cases := []struct {
		name   string
		target otaTruthTarget
		v      map[string]string
		want   string
	}{
		{"on target: version, build and running checksum are this app's",
			release,
			map[string]string{"version": app, "build": appBuild, "agentRunningSha256": shaApp},
			"skip=yes confirms=yes classify=confirmed reached=yes"},
		{"same version, build stamp a minute off, running checksum is this app's (v0.9.88)",
			release,
			map[string]string{"version": app, "build": appBuildSkew, "agentRunningSha256": shaApp},
			"skip=yes confirms=yes classify=confirmed reached=yes"},
		{"same version, build stamp a minute off, an agent that reports no checksum",
			release,
			map[string]string{"version": app, "build": appBuildSkew},
			// skip and classify have no reading from before the push to see a
			// change against; confirms and reached do.
			"skip=no confirms=yes classify=not-landed reached=yes"},
		{"still on the software from before the push",
			release,
			map[string]string{"version": old, "build": oldBuild, "agentRunningSha256": shaOther},
			"skip=no confirms=no classify=not-landed reached=no:agent"},
		{"the pushed binary is on the disk, the old one runs (#381)",
			release,
			map[string]string{"version": old, "build": oldBuild, "agentBinarySha256": shaApp, "agentRunningSha256": shaOther},
			"skip=no confirms=no classify=landed-not-running reached=no:agent"},
		{"older than this app, but not what ran before the push (a step back, or sideways)",
			otaTruthTarget{sha: shaApp, build: appBuild, version: app, preBuild: "", preVersion: "v1.5.3"},
			map[string]string{"version": old, "agentRunningSha256": shaOther},
			"skip=no confirms=no classify=not-landed reached=yes"},
		{"a build stamp earlier than the one before the push",
			otaTruthTarget{sha: shaApp, build: appBuild, version: app, preBuild: appBuild, preVersion: app},
			map[string]string{"version": old, "build": oldBuild, "agentRunningSha256": shaOther},
			"skip=no confirms=no classify=not-landed reached=yes"},
		{"newer than this app (another computer updated the speaker meanwhile)",
			release,
			map[string]string{"version": "v1.6.0", "build": "2026-11-20-0900", "agentRunningSha256": shaOther},
			"skip=no confirms=yes classify=not-landed reached=yes"},
		{"this app's running checksum next to an older version string",
			release,
			map[string]string{"version": old, "build": oldBuild, "agentRunningSha256": shaApp},
			"skip=yes confirms=no classify=not-landed reached=yes"},
		{"this app's build stamp next to an older version string",
			release,
			map[string]string{"version": old, "build": appBuild},
			"skip=no confirms=no classify=confirmed reached=no:agent"},
		{"this app's agent running from RAM after a failed swap (the rescue agent)",
			release,
			map[string]string{"version": app, "build": appBuild, "agentRunningSha256": shaApp, "otaSwapFailed": "copy to NAND did not verify"},
			// reached does not ask about the marker: it is only asked after
			// confirms took the agent, and a speaker sets the marker during a
			// swap, never afterwards.
			"skip=no confirms=no classify=swap-failed reached=yes"},
		{"the same rescue agent, build stamp a minute off",
			release,
			map[string]string{"version": app, "build": appBuildSkew, "agentRunningSha256": shaApp, "otaSwapFailed": "copy to NAND did not verify"},
			"skip=no confirms=no classify=swap-failed reached=yes"},
		{"a failed swap reported by the old agent",
			release,
			map[string]string{"version": old, "build": oldBuild, "agentRunningSha256": shaOther, "otaSwapFailed": "copy to NAND did not verify"},
			"skip=no confirms=no classify=swap-failed reached=no:agent"},
		{"this app's agent, with a build stamp earlier than the speaker reported before the push",
			otaTruthTarget{sha: shaApp, build: appBuild, version: app, preBuild: "2026-11-10-1300", preVersion: app},
			map[string]string{"version": app, "build": appBuild, "agentRunningSha256": shaApp},
			// Only confirms holds a reading against the build stamp from before
			// the push; classify never sees that reading.
			"skip=yes confirms=no classify=confirmed reached=yes"},
		{"no reading before the push, the speaker answers on old software",
			otaTruthTarget{sha: shaApp, build: appBuild, version: "", preBuild: "", preVersion: ""},
			map[string]string{"version": old, "build": oldBuild, "agentRunningSha256": shaOther},
			"skip=no confirms=no classify=not-landed reached=yes"},

		// An unstamped app reports version 1.0.0 and build "dev": it cannot tell
		// which version is newer, so it never calls a reading older.
		{"unstamped app, the speaker moved to another version",
			otaTruthTarget{sha: "", build: "dev", version: "1.0.0", preBuild: "2026-11-03-2355", preVersion: "v1.5.3"},
			map[string]string{"version": old, "build": oldBuild},
			"skip=no confirms=yes classify=not-landed reached=yes"},
		{"unstamped app and an unstamped agent",
			otaTruthTarget{sha: "", build: "dev", version: "1.0.0", preBuild: "dev", preVersion: "1.0.0"},
			map[string]string{"version": "1.0.0", "build": "dev"},
			"skip=yes confirms=yes classify=confirmed reached=no:agent"},

		// Local dev builds over each other: same version string before and after.
		{"dev build over a dev build: checksum and stamp moved, version did not",
			otaTruthTarget{sha: shaApp, build: devBuildNew, version: dev, preBuild: devBuildOld, preVersion: dev},
			map[string]string{"version": dev, "build": devBuildNew, "agentRunningSha256": shaApp},
			"skip=yes confirms=yes classify=confirmed reached=yes"},
		{"dev build over a dev build: the speaker came back unchanged",
			otaTruthTarget{sha: shaApp, build: devBuildNew, version: dev, preBuild: devBuildOld, preVersion: dev},
			map[string]string{"version": dev, "build": devBuildOld, "agentRunningSha256": shaOther},
			"skip=no confirms=no classify=not-landed reached=no:agent"},
		{"dev build over a dev build: only the stamp to go on (no checksum reported)",
			otaTruthTarget{sha: shaApp, build: devBuildNew, version: dev, preBuild: devBuildOld, preVersion: dev},
			map[string]string{"version": dev, "build": devBuildNew},
			// reached knows the version string and the checksum only, so it does
			// not see this one; the engine step then asks the speaker once more.
			"skip=yes confirms=yes classify=confirmed reached=no:agent"},
		{"dev build over the release it was cut from",
			otaTruthTarget{sha: shaApp, build: devBuildNew, version: dev, preBuild: appBuild, preVersion: app},
			map[string]string{"version": dev, "build": devBuildNew, "agentRunningSha256": shaApp},
			// The stamp is earlier than the release's, and the checksum does
			// not outrank that in confirms: it waits out its window, and
			// classify, which has no earlier reading, confirms late.
			"skip=yes confirms=no classify=confirmed reached=yes"},
		{"release over a dev build of the same version",
			otaTruthTarget{sha: shaApp, build: appBuild, version: app, preBuild: devBuildOld, preVersion: dev},
			map[string]string{"version": app, "build": appBuild, "agentRunningSha256": shaApp},
			"skip=yes confirms=yes classify=confirmed reached=yes"},

		{"no answer",
			release,
			nil,
			"skip=no confirms=no classify=not-landed reached=no:unreachable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.target.answers(c.v); got != c.want {
				t.Errorf("\n got %s\nwant %s", got, c.want)
			}
		})
	}
}

// otaTruthTarget is what one row's update is held against: what the app
// carries, and what the speaker reported before the push.
type otaTruthTarget struct {
	sha, build, version, preBuild, preVersion string
}

// answers asks all four predicates about one reading.
func (tt otaTruthTarget) answers(v map[string]string) string {
	yn := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	var noEngine map[string]string
	if v != nil {
		noEngine = map[string]string{"goLibrespot": "missing"}
		for k, x := range v {
			noEngine[k] = x
		}
	}
	skip := agentCurrentEngineMissing(noEngine, tt.version, tt.build, tt.sha)
	confirms := updateConfirms(v, tt.sha, tt.build, tt.version, tt.preBuild, tt.preVersion)
	verdict, _ := classifyAgentVersion(v, tt.sha, tt.build, tt.version)
	done, missing := speakerReachedTarget(v, tt.preVersion, false, tt.sha)
	reached := yn(done)
	if !done {
		reached += ":" + missing
	}
	return fmt.Sprintf("skip=%s confirms=%s classify=%s reached=%s", yn(skip), yn(confirms), verdict, reached)
}
