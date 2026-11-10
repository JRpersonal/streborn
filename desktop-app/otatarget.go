package main

// otaTarget: what a speaker's update is held against, and the questions the
// update asks of a version reading.
//
// Four steps of an update ask "is this speaker on the software it was meant to
// get", each with its own evidence and its own cost of being wrong: before the
// push (onlyEngineMissing), every two seconds after it (confirms), once when
// that wait ran out (classify), and at the top of the engine step (reached).
// They are methods of one type so their rules sit side by side and
// TestUpdateTargetTruthTable can hold one reading against all four. Where they
// answer differently, the method's comment says why.
//
// Two rules hold for all of them. The binary the speaker is RUNNING decides
// when both sides know its checksum: a local dev build has the same version
// string as the one before it, so only the checksum and the build stamp can
// tell them apart. And an update that did not take is never called installed.

import (
	"regexp"
	"strings"

	"github.com/JRpersonal/streborn/versioncmp"
)

// otaTarget is one update's target. sha, build and version are what this app
// carries (the embedded agent's checksum, the build stamp, the version);
// preBuild and preVersion are what the speaker reported before the push, empty
// when that reading failed or when the question is asked without one
// (classify).
type otaTarget struct {
	sha, build, version  string
	preBuild, preVersion string
}

// target is what an update run by these seams is held against, given what the
// speaker reported before the push.
func (s *otaSeams) target(preBuild, preVersion string) otaTarget {
	return otaTarget{sha: s.appSha, build: s.appBuild, version: s.appVersion, preBuild: preBuild, preVersion: preVersion}
}

// buildStampRe is the agent's build stamp, which sorts as text in time order.
var buildStampRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-\d{4}$`)

// older reports whether a version reading is provably older software than the
// update was meant to install: its version is below this app's, or its build
// stamp is earlier than the one the speaker reported before the push. Unknown
// or unstamped values (an app that reports the default "1.0.0", an agent
// without a build) prove nothing and never refuse.
func (t otaTarget) older(v map[string]string) bool {
	if v == nil {
		return false
	}
	have := strings.TrimSpace(v["version"])
	want := strings.TrimSpace(t.version)
	if have != "" && want != "" && strings.TrimPrefix(want, "v") != "1.0.0" &&
		len(versioncmp.Parts(have)) > 0 && len(versioncmp.Parts(want)) > 0 &&
		versioncmp.Less(have, want) {
		return true
	}
	b := strings.TrimSpace(v["build"])
	return buildStampRe.MatchString(b) && buildStampRe.MatchString(t.preBuild) && b < t.preBuild
}

// onlyEngineMissing reports whether a speaker already runs exactly the agent
// this app would push and only lacks the Spotify engine. Such a speaker needs
// the engine, not another agent upload and the reboot that comes with it.
//
// The running agent's checksum decides when both sides know it. It is the
// running one on purpose: the one on disk differs exactly when a push landed
// and did not boot, and that speaker does need the agent again. Without a
// checksum, version and build must both match.
//
// It is asked before the push, so it has no earlier reading to see a change
// against: preBuild and preVersion play no part.
func (t otaTarget) onlyEngineMissing(v map[string]string) bool {
	if v == nil || v["goLibrespot"] != "missing" {
		return false
	}
	if t.sha != "" && v["agentRunningSha256"] != "" {
		return v["agentRunningSha256"] == t.sha
	}
	return t.build != "" && t.version != "" && v["build"] == t.build && v["version"] == t.version
}

// reached answers the only question that matters at the end of an update: is
// this speaker actually where it was meant to be. It returns which half is
// outstanding ("agent", "engine", "unreachable") so the caller can say so
// rather than showing a spinner with no explanation.
//
// It exists because judging on one half is how a run lies. A speaker whose
// Spotify engine survived the reboot reports the engine present within seconds,
// while its agent is still being replaced, and anything that stops looking at
// that moment declares success on the old software. The mirror case is just as
// real: the agent lands and the engine is still missing. Both halves, always.
//
// Learned from a fleet run on 2026-07-29 where a Portable passed on the old
// build and only finished minutes later. Users hit the same on slow speakers.
//
// The binary the speaker is RUNNING first, then the version string. "the
// version string moved" cannot be satisfied by a push of the SAME version, and
// such a push is not harmless: a speaker that has the Spotify engine is by
// definition NAND-tight, so the agent drops the engine to make the update fit.
// Judging by the string then returns before the engine is put back, and a
// working Spotify install is destroyed by a re-push that was meant to change
// nothing. Four of six speakers in one household ended up that way after the
// v0.9.88 stamp skew invited re-push after re-push.
//
// The agent half reads the checksum and the version string only, never the
// build stamp: it is asked after confirms already took the agent, and a reading
// it does not recognise costs one more look at the speaker, not a wrong result.
func (t otaTarget) reached(live map[string]string, wantEngine bool) (done bool, missing string) {
	if live == nil {
		return false, "unreachable"
	}
	agentDone := (t.sha != "" && live["agentRunningSha256"] == t.sha) ||
		(live["version"] != "" && (t.preVersion == "" || live["version"] != t.preVersion))
	engineDone := !wantEngine || live["goLibrespot"] == "present"
	if !agentDone {
		return false, "agent"
	}
	if !engineDone {
		return false, "engine"
	}
	return true, ""
}

// confirms is the predicate the post-push poll runs every two seconds for up to
// six minutes: does this version reading prove the update took effect.
//
// The binary the speaker is RUNNING, first, because that is the only answer a
// clock cannot spoil. Without it the v0.9.88 stamp skew made a successful update
// wait out the whole 360 s window and then report "the update did not take
// effect", which was false and told the user to stop trying.
//
// Not agentBinarySha256: that is what lies on the box's DISK, and the one case
// where the two differ is a push that landed and then did not boot (#381).
// Reading the disk here would report that rollback as a success.
//
// After the hash: the app's own build stamp, then "no longer the pre-OTA build",
// then "no longer the pre-OTA version", which survive app/agent stamp drift
// and an agent too old to report a hash.
//
// None of them may accept a speaker that is provably on OLDER software than
// this app carries (older). "No longer the pre-OTA build" read a step BACKWARDS
// as success: a SoundTouch 10 whose reading before the push already named the
// app's build came back on the build it had before that, and the journal said
// "confirmed: box is on build <the old one>" (2026-11-09). The checksum is held
// to the same rule, because the agent hashes its own executable lazily by path
// and a path can hold another binary by then.
func (t otaTarget) confirms(v map[string]string) bool {
	if v == nil {
		return false
	}
	if t.older(v) {
		return false
	}
	if t.sha != "" && v["agentRunningSha256"] == t.sha {
		return true
	}
	if t.build != "" && v["build"] == t.build {
		return true
	}
	if t.preBuild != "" && v["build"] != "" && v["build"] != t.preBuild {
		return true
	}
	if t.preVersion != "" && v["version"] != "" && v["version"] != t.preVersion {
		return true
	}
	return false
}

// classify is the whole decision of ClassifyOTAResult for a speaker that
// answers, with no network and no embed in it, so the matrix it has to get
// right can be written down as a test. It returns the verdict and the journal
// line that belongs to it.
//
// Two hashes, and the difference between them IS the verdict.
//
// agentRunningSha256 is the binary the agent currently is. It is the one answer
// a clock cannot spoil, and v0.9.88 is why that matters: the release stamped
// the app and the agent it embeds a minute apart, so the build comparison
// called every successful update a failure. Eleven speakers in one household,
// all of them on the new build, all journalled as "the update did not take
// effect" with the loop breaker armed and the user told to stop retrying. Three
// separate users wrote in about it on 2026-09-27.
//
// agentBinarySha256 is the binary on the box's DISK, which is a different
// question and must not be mistaken for this one. They differ for exactly one
// reason: a push that reached the disk and then did not boot (#381). Taking the
// on-disk hash as confirmation would report that rollback as a success and
// disarm the loop breaker written to stop it repeating forever, so the running
// hash confirms and the on-disk hash is only ever the evidence for
// landed-not-running.
//
// The version guards the checksum arm: an agent that reports a version older
// than this app's is not this app's agent, whatever checksum it reports (the
// agent hashes its executable lazily by path, see confirms).
//
// It is asked without a reading from before the push (ClassifyOTAResult is
// also the window's own call), so "no longer the pre-OTA build" is not one of
// its rules and a build stamp is never held against an earlier one.
func (t otaTarget) classify(ver map[string]string) (verdict, journal string) {
	running, onDisk := ver["agentRunningSha256"], ver["agentBinarySha256"]
	older := t.older(ver)
	if t.sha != "" && running == t.sha && ver["otaSwapFailed"] == "" && !older {
		return "confirmed", "outcome: confirmed late - box runs the agent this build carries"
	}
	if t.build != "" && ver["build"] == t.build {
		return "confirmed", "outcome: confirmed late - box is on build " + ver["build"]
	}
	if msg := ver["otaSwapFailed"]; msg != "" {
		return "swap-failed", "outcome: NOT CONFIRMED - the box reports a failed binary swap: " + msg
	}
	// The disk carries what was pushed and the agent is running something else.
	// An agent too old to report what it runs cannot tell this apart from a stamp
	// skew, so for it the on-disk hash alone still means what it meant before.
	if t.sha != "" && onDisk == t.sha && running != t.sha {
		return "landed-not-running", "outcome: NOT CONFIRMED - the pushed binary IS on the box's disk but the running agent is still " +
			ver["version"] + " build " + ver["build"] +
			"; the update did not take effect (boot rollback / swap failure), an identical re-push cannot help"
	}
	return "not-landed", "outcome: NOT CONFIRMED - box still runs " + ver["version"] + " build " + ver["build"] +
		" and does not report the pushed binary on disk"
}
