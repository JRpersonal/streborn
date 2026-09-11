package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestParseSubject(t *testing.T) {
	cases := []struct {
		subject   string
		wantKeep  bool
		wantType  string
		wantScope string
		wantBreak bool
		wantSum   string
	}{
		{"feat(i18n): add Lithuanian", true, "feat", "i18n", false, "Add Lithuanian"},
		{"fix(desktop,stick): stable discovery", true, "fix", "desktop,stick", false, "Stable discovery"},
		{"perf: faster boot", true, "perf", "", false, "Faster boot"},
		{"feat!: drop legacy config", true, "feat", "", true, "Drop legacy config"},
		{"refactor(core)!: rename API", true, "refactor", "core", true, "Rename API"}, // kept only because breaking
		{"chore: bump deps", false, "", "", false, ""},
		{"docs(screenshots): regenerate", false, "", "", false, ""},
		{"ci: pin action", false, "", "", false, ""},
		{"not a conventional commit", false, "", "", false, ""},
		{"refactor(core): rename internal", false, "", "", false, ""}, // refactor without breaking is dropped
	}
	for _, c := range cases {
		got, keep := parseSubject(c.subject)
		if keep != c.wantKeep {
			t.Errorf("parseSubject(%q) keep=%v want %v", c.subject, keep, c.wantKeep)
			continue
		}
		if !keep {
			continue
		}
		if got.Type != c.wantType || got.Scope != c.wantScope || got.Breaking != c.wantBreak || got.Summary != c.wantSum {
			t.Errorf("parseSubject(%q) = %+v, want type=%s scope=%s break=%v sum=%q",
				c.subject, got, c.wantType, c.wantScope, c.wantBreak, c.wantSum)
		}
	}
}

func TestRenderMarkdownSections(t *testing.T) {
	changes := []change{
		{Type: "feat", Scope: "i18n", Summary: "Add Lithuanian"},
		{Type: "fix", Scope: "frontend", Summary: "Sort language filter"},
		{Type: "feat", Scope: "core", Summary: "Drop legacy config", Breaking: true},
	}
	md := renderMarkdown("v1.2.3", changes)

	for _, want := range []string{
		"## What's changed in v1.2.3",
		"### Breaking changes",
		"- Drop legacy config (core)",
		"### New features",
		"- Add Lithuanian (i18n)",
		"### Fixes",
		"- Sort language filter (frontend)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("rendered markdown missing %q:\n%s", want, md)
		}
	}
	// Breaking section must come before the New features section.
	if strings.Index(md, "Breaking changes") > strings.Index(md, "New features") {
		t.Error("breaking changes should be listed before new features")
	}
}

func TestRenderMarkdownEmpty(t *testing.T) {
	md := renderMarkdown("v1.0.0", nil)
	if !strings.Contains(md, "Maintenance release") {
		t.Errorf("empty changelog should note a maintenance release, got:\n%s", md)
	}
}

// A squash merge collapses a whole branch into one subject, so a pull request
// that fixed several user-visible things could only announce one of them.
func TestParseNoteTrailers(t *testing.T) {
	body := "Some prose about the change.\n\n" +
		"Release-Note: fix(webui): saving a very long playlist as a preset works\n" +
		"Release-Note: feat(app): the speaker tile shows when an update is waiting\n" +
		"Release-Note: chore(ci): bump an action\n" +
		"Refs #489.\n"
	got := parseNoteTrailers(body)
	if len(got) != 2 {
		t.Fatalf("want 2 user-facing entries (the chore trailer is dropped), got %d: %+v", len(got), got)
	}
	if got[0].Type != "fix" || got[0].Scope != "webui" ||
		got[0].Summary != "Saving a very long playlist as a preset works" {
		t.Errorf("first trailer parsed wrong: %+v", got[0])
	}
	if got[1].Type != "feat" || got[1].Scope != "app" {
		t.Errorf("second trailer parsed wrong: %+v", got[1])
	}
	if len(parseNoteTrailers("no trailers here\nRelease-Notes: not the key\n")) != 0 {
		t.Error("only the exact Release-Note key may count")
	}
}

// Trailers ADD entries next to the subject, they do not replace it. The old
// replace rule swallowed real entries three times in one day (2026-08-25): a
// fix commit carrying a Release-Note trailer for a DIFFERENT change it also
// made lost its own subject line silently. An exact restatement still folds to
// one line via the dedup, and a chore commit still contributes only its
// trailers because its subject is not a user-facing type.
func TestSubjectAndTrailersBothListed(t *testing.T) {
	run := func(t *testing.T, log string) []change {
		t.Helper()
		dir := t.TempDir()
		git := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
				"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		git("init", "-q")
		git("commit", "--allow-empty", "-q", "-m", log)
		old, _ := os.Getwd()
		_ = os.Chdir(dir)
		defer func() { _ = os.Chdir(old) }()
		got, err := collect("", "HEAD")
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		return got
	}

	t.Run("fix subject plus foreign trailer yields two lines", func(t *testing.T) {
		got := run(t, "fix(app): the thing this commit is about\n\n"+
			"Release-Note: fix(phone): the other thing it also changed\n")
		if len(got) != 2 {
			t.Fatalf("want subject AND trailer, got %d: %+v", len(got), got)
		}
	})
	t.Run("exact restatement folds to one line", func(t *testing.T) {
		got := run(t, "fix(app): the thing this commit is about\n\n"+
			"Release-Note: fix(app): the thing this commit is about\n")
		if len(got) != 1 {
			t.Fatalf("want the restatement deduped, got %d: %+v", len(got), got)
		}
	})
	t.Run("chore subject stays out, its trailers stay in", func(t *testing.T) {
		got := run(t, "chore(relnotes): bookkeeping\n\n"+
			"Release-Note: fix(app): the restored entry\n")
		if len(got) != 1 || got[0].Summary != "The restored entry" {
			t.Fatalf("want only the trailer, got %+v", got)
		}
	})
}

// Work announced and then reversed inside the same release window must not be
// announced: it sends users looking for something that is not there.
func TestParseNoteDrops(t *testing.T) {
	body := "We changed course.\n\n" +
		"Release-Note-Drop: A speaker no longer lists itself, and a Spotify engine removed by an update comes back\n"
	got := parseNoteDrops(body)
	if len(got) != 1 {
		t.Fatalf("want 1 withdrawal, got %d: %v", len(got), got)
	}
	if got[0] != "A speaker no longer lists itself, and a Spotify engine removed by an update comes back" {
		t.Errorf("withdrawal text parsed wrong: %q", got[0])
	}
	if len(parseNoteDrops("Release-Note: fix(app): something\n")) != 0 {
		t.Error("a normal entry must not be read as a withdrawal")
	}
}

// A Release-Note trailer that restates the commit's own subject in other
// words is one entry, not two (two such commits produced four lines for two
// fixes on 2026-09-06). A trailer for a DIFFERENT change under the same scope
// still counts, which is what the trailer exists for.
func TestRestatedTrailerFoldsIntoOneEntry(t *testing.T) {
	subj := change{Type: "fix", Scope: "multiroom", Summary: "a refused group form no longer shows \"Group active\""}
	restated := change{Type: "fix", Scope: "multiroom", Summary: "a refused group form no longer shows a false \"Group active\" confirmation"}
	if !restatedBy(subj, []change{restated}) {
		t.Error("the reworded repeat was not recognised")
	}
	subj2 := change{Type: "fix", Scope: "multiroom", Summary: "Ungroup with no group says so instead of a false confirmation"}
	restated2 := change{Type: "fix", Scope: "multiroom", Summary: "pressing Ungroup with no group now says there is nothing to ungroup, not a false success"}
	if !restatedBy(subj2, []change{restated2}) {
		t.Error("the second reworded repeat was not recognised")
	}
	other := change{Type: "fix", Scope: "multiroom", Summary: "the stereo pair keeps its name after a rename on the other speaker"}
	if restatedBy(subj, []change{other}) {
		t.Error("a different change under the same scope was folded away")
	}
	if restatedBy(subj, []change{{Type: "fix", Scope: "app", Summary: subj.Summary}}) {
		t.Error("a different scope must never fold")
	}
}

// A trailer that cannot be parsed is REPORTED, not silently dropped.
//
// Three commits in the v0.9.79 range wrote theirs as prose over two lines with
// no type(scope): prefix. All three parsed as nothing, the notes fell back to
// the rougher commit subject, and there was nothing anywhere to say so, so
// three of the eight lines users would have read were not the sentences written
// for them. The prefix stays required; losing one is what had to stop being
// quiet.
func TestABrokenNoteTrailerIsReportedRatherThanDropped(t *testing.T) {
	noteTrailerProblems = nil
	t.Cleanup(func() { noteTrailerProblems = nil })

	body := "Some commit body.\n\n" +
		"Release-Note: The firmware help in the speaker settings opens the Bose\n" +
		"  article for your own model instead of always the SoundTouch 20 one.\n" +
		"Release-Note: fix(app): a good one that parses\n"

	got := parseNoteTrailers(body)
	if len(got) != 1 || got[0].Summary != "A good one that parses" {
		t.Fatalf("the well-formed trailer did not survive: %+v", got)
	}
	if len(noteTrailerProblems) == 0 {
		t.Fatal("the prose trailer was dropped without a word, which is the whole bug")
	}
	joined := strings.Join(noteTrailerProblems, "\n")
	if !strings.Contains(joined, "firmware help") {
		t.Fatalf("the warning does not name the payload that was lost:\n%s", joined)
	}
}

// The quieter half: a trailer wrapped onto a second line still produces an
// entry, truncated mid-sentence, which reads as a typo in the release rather
// than as a broken trailer.
func TestAWrappedNoteTrailerIsReportedToo(t *testing.T) {
	noteTrailerProblems = nil
	t.Cleanup(func() { noteTrailerProblems = nil })

	body := "Release-Note: fix(app): this sentence runs on past the end of the\n" +
		"  first line and loses its tail\n"
	got := parseNoteTrailers(body)
	if len(got) != 1 {
		t.Fatalf("want the truncated entry, got %+v", got)
	}
	if len(noteTrailerProblems) == 0 {
		t.Fatal("a wrapped trailer was accepted silently")
	}
	if !strings.Contains(strings.Join(noteTrailerProblems, "\n"), "wrapped") {
		t.Fatalf("the warning does not say it was wrapped: %v", noteTrailerProblems)
	}
}

// A well-formed body must stay silent, or the warning becomes noise nobody
// reads. Co-Authored-By and the session trailer sit right under a Release-Note
// line in every commit this repo makes, and neither is a continuation.
func TestAWellFormedBodyWarnsAboutNothing(t *testing.T) {
	noteTrailerProblems = nil
	t.Cleanup(func() { noteTrailerProblems = nil })

	body := "Release-Note: fix(agent): one clean line\n" +
		"Release-Note: feat(app): another clean line\n" +
		"\n" +
		"Co-Authored-By: Somebody <nobody@example.com>\n"
	if got := parseNoteTrailers(body); len(got) != 2 {
		t.Fatalf("want both entries, got %+v", got)
	}
	if len(noteTrailerProblems) != 0 {
		t.Fatalf("a clean body produced warnings: %v", noteTrailerProblems)
	}
}
