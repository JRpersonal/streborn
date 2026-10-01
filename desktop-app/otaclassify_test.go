package main

import (
	"strings"
	"testing"
)

// The verdict the app writes into the journal after an update, and the verdict
// that decides whether it will ever offer that update again.
//
// v0.9.88 got this wrong for everybody. Its release workflow took the build
// stamp inside a three-leg matrix, so the desktop apps were stamped
// 2026-09-26-2023 while the ARM agent they embed and push carried
// 2026-09-26-2022: one minute apart, same commit. The classifier compared those
// stamps, so a perfectly successful update was journalled as
//
//	NOT CONFIRMED - the pushed binary IS on the box's disk but the running agent
//	is still v0.9.88 build 2026-09-26-2022; the update did not take effect
//	(boot rollback / swap failure), an identical re-push cannot help
//
// which is false in every clause, and it armed the loop breaker so the user was
// told to stop trying. Three of them wrote in on 2026-09-27, one with eleven
// speakers, all eleven on the new build reporting the identical agent hash.
//
// The opposite mistake is just as expensive and is the reason this is a matrix
// rather than one assertion: #381 is a push that reaches the box's DISK and then
// does not boot. Confirming on the on-disk hash would report that as a success
// and disarm the loop breaker written to stop it repeating forever.
func TestTheUpdateVerdictReadsTheRunningBinaryNotTheClock(t *testing.T) {
	const embedded = "aaaa"
	const other = "bbbb"

	cases := []struct {
		name     string
		ver      map[string]string
		embedded string
		want     string
	}{
		{
			// The v0.9.88 case, exactly as it reached three inboxes.
			name: "same build number, stamps a minute apart, agent runs this build",
			ver: map[string]string{
				"version": "v0.9.88", "build": "2026-09-26-2022",
				"agentBinarySha256": embedded, "agentRunningSha256": embedded,
			},
			embedded: embedded,
			want:     "confirmed",
		},
		{
			// #381. The one case the two hashes exist to tell apart.
			name: "binary landed on the disk, the box boots the old one",
			ver: map[string]string{
				"version": "v0.9.87", "build": "2026-09-25-1715",
				"agentBinarySha256": embedded, "agentRunningSha256": other,
			},
			embedded: embedded,
			want:     "landed-not-running",
		},
		{
			name: "the push never arrived",
			ver: map[string]string{
				"version": "v0.9.87", "build": "2026-09-25-1715",
				"agentBinarySha256": other, "agentRunningSha256": other,
			},
			embedded: embedded,
			want:     "not-landed",
		},
		{
			// A swap the box itself reports as failed outranks a hash match: the
			// binary may be in place while the box is running from a fallback.
			name: "the box reports a failed swap",
			ver: map[string]string{
				"version": "v0.9.87", "build": "2026-09-25-1715",
				"agentBinarySha256": embedded, "agentRunningSha256": embedded,
				"otaSwapFailed": "could not replace the binary",
			},
			embedded: embedded,
			want:     "swap-failed",
		},
		{
			// An agent old enough not to report what it runs keeps the reading it
			// had before the field existed: the disk hash means landed-not-running.
			name: "old agent, binary on disk, old version running",
			ver: map[string]string{
				"version": "v0.9.87", "build": "2026-09-25-1715",
				"agentBinarySha256": embedded,
			},
			embedded: embedded,
			want:     "landed-not-running",
		},
		{
			// A dev build carries an empty embed slot, so there is no hash to
			// compare and the build stamp is all there is. It must not fall into
			// landed-not-running just because both hashes are the empty string.
			name: "dev build with no embedded agent, box on the app's build",
			ver: map[string]string{
				"version": "v0.9.88", "build": "b2",
			},
			embedded: "",
			want:     "confirmed",
		},
		{
			name: "dev build with no embedded agent, box on an older build",
			ver: map[string]string{
				"version": "v0.9.87", "build": "b1",
			},
			embedded: "",
			want:     "not-landed",
		},
	}

	old := appBuild
	appBuild = "b2"
	t.Cleanup(func() { appBuild = old })

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, journal := classifyAgentVersion(c.ver, c.embedded, appBuild)
			if got != c.want {
				t.Fatalf("verdict = %q, want %q (journal: %s)", got, c.want, journal)
			}
			if journal == "" {
				t.Error("no journal line, so the bundle cannot say what happened")
			}
			// A confirmed update must never be journalled with the sentence that
			// tells the user to stop trying.
			if got == "confirmed" && strings.Contains(journal, "NOT CONFIRMED") {
				t.Errorf("a confirmed update was journalled as a failure: %s", journal)
			}
		})
	}
}
