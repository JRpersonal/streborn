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
