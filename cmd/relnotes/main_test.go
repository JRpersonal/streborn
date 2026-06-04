package main

import (
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
