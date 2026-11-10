package main

// The four predicates under the names and argument lists they had before they
// became methods of otaTarget. The table tests written against them
// (TestUpdateConfirms, TestAgentCurrentEngineMissing, TestSpeakerReachedTarget,
// the classifier matrix, the regression tests of 2026-11-09 and the truth
// table) keep calling these, unchanged, so they pin the methods.

func agentCurrentEngineMissing(v map[string]string, appVersion, appBuild, appSha string) bool {
	return otaTarget{sha: appSha, build: appBuild, version: appVersion}.onlyEngineMissing(v)
}

func speakerReachedTarget(live map[string]string, preVersion string, wantEngine bool, appSha string) (done bool, missing string) {
	return otaTarget{sha: appSha, preVersion: preVersion}.reached(live, wantEngine)
}

func updateConfirms(v map[string]string, appSha, appBuild, appVersion, preBuild, preVersion string) bool {
	return otaTarget{sha: appSha, build: appBuild, version: appVersion, preBuild: preBuild, preVersion: preVersion}.confirms(v)
}

func classifyAgentVersion(ver map[string]string, embedded, wantBuild, wantVersion string) (verdict, journal string) {
	return otaTarget{sha: embedded, build: wantBuild, version: wantVersion}.classify(ver)
}
