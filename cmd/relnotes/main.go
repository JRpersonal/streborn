// Command relnotes generates human-facing release notes from the
// Conventional Commit subjects between two git refs.
//
// It is run by the release workflow to produce two artifacts:
//
//   - a Markdown "What's changed" block, inserted at the top of the
//     GitHub Release body, and
//   - a JSON object ({"markdown": ..., "items": [...]}) merged into
//     manifest.json as the "notes" field, so the website and the
//     desktop app can show the same change list without scraping the
//     Releases UI.
//
// Only user-facing commit types are kept (feat, fix, perf) plus any
// commit flagged breaking (a "!" before the colon). Noise like chore,
// ci, build, test, style, refactor, and docs is dropped, so a user
// reading the notes sees only what tells them whether the upgrade is
// worth it.
//
// It is intentionally dependency-free (standard library + the git CLI)
// to stay inside the project's supply-chain posture: no new third-party
// action or module in the release path.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// change is one parsed Conventional Commit that made the cut.
type change struct {
	Type     string `json:"type"`
	Scope    string `json:"scope,omitempty"`
	Summary  string `json:"summary"`
	Breaking bool   `json:"breaking"`
	Commit   string `json:"commit,omitempty"`
}

// notes is the JSON shape merged into manifest.json under "notes".
type notes struct {
	Markdown string   `json:"markdown"`
	Items    []change `json:"items"`
}

// sectionOrder is the display order and titles for the kept commit
// types. Anything not listed here (and not breaking) is dropped.
var sectionOrder = []struct{ key, title string }{
	{"feat", "New features"},
	{"fix", "Fixes"},
	{"perf", "Performance"},
}

// subjectRE parses "type(scope)!: summary". scope and "!" are optional.
var subjectRE = regexp.MustCompile(`^([a-z]+)(?:\(([^)]*)\))?(!)?:\s*(.+)$`)

func main() {
	var (
		from    = flag.String("from", "", "start ref (exclusive); auto-detected as the previous tag when empty")
		to      = flag.String("to", "HEAD", "end ref (inclusive)")
		ver     = flag.String("version", "", "version label shown in the heading, e.g. v0.6.21")
		outMD   = flag.String("out-md", "", "write the Markdown block to this file (default stdout)")
		outJSON = flag.String("out-json", "", "write the notes JSON object to this file")
	)
	flag.Parse()

	start := *from
	if start == "" {
		start = previousTag(*to) // may stay empty for the very first release
	}

	changes, err := collect(start, *to)
	if err != nil {
		fmt.Fprintln(os.Stderr, "relnotes:", err)
		os.Exit(1)
	}

	md := renderMarkdown(*ver, changes)

	if *outMD != "" {
		if err := os.WriteFile(*outMD, []byte(md), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "relnotes:", err)
			os.Exit(1)
		}
	} else {
		fmt.Print(md)
	}

	if *outJSON != "" {
		b, err := json.MarshalIndent(notes{Markdown: md, Items: changes}, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "relnotes:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*outJSON, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "relnotes:", err)
			os.Exit(1)
		}
	}
}

// previousTag returns the most recent tag reachable from before ref, or
// "" when there is none (the first release). Errors are treated as "no
// previous tag" so a fresh repo still produces notes.
func previousTag(ref string) string {
	out, err := exec.Command("git", "describe", "--tags", "--abbrev=0", ref+"^").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// collect runs git log over (start, end] and returns the kept changes
// in commit order (newest first), de-duplicated by type+scope+summary.
func collect(start, end string) ([]change, error) {
	rng := end
	if start != "" {
		rng = start + ".." + end
	}
	// %H<TAB>%s, one commit per line, no merges.
	args := []string{"log", "--no-merges", "--pretty=format:%H\t%s", rng}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("git log %s: %w", rng, err)
	}

	var changes []change
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		hash, subject, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		c, keep := parseSubject(subject)
		if !keep {
			continue
		}
		c.Commit = shortHash(hash)
		key := c.Type + "|" + c.Scope + "|" + c.Summary
		if seen[key] {
			continue
		}
		seen[key] = true
		changes = append(changes, c)
	}
	return changes, nil
}

func shortHash(h string) string {
	if len(h) > 9 {
		return h[:9]
	}
	return h
}

// parseSubject parses one commit subject. It returns keep=false when the
// subject is not a Conventional Commit or its type is not user-facing
// (and it is not flagged breaking).
func parseSubject(subject string) (change, bool) {
	m := subjectRE.FindStringSubmatch(strings.TrimSpace(subject))
	if m == nil {
		return change{}, false
	}
	c := change{
		Type:     m[1],
		Scope:    m[2],
		Breaking: m[3] == "!",
		Summary:  capitalize(strings.TrimSpace(m[4])),
	}
	if c.Breaking {
		return c, true
	}
	for _, s := range sectionOrder {
		if s.key == c.Type {
			return c, true
		}
	}
	return change{}, false
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	return string(r)
}

// renderMarkdown builds the "What's changed" block: a Breaking changes
// section first (when any), then one section per kept type in
// sectionOrder. Each line is "- Summary (scope)".
func renderMarkdown(version string, changes []change) string {
	var b strings.Builder
	if version != "" {
		fmt.Fprintf(&b, "## What's changed in %s\n\n", version)
	} else {
		b.WriteString("## What's changed\n\n")
	}

	if len(changes) == 0 {
		b.WriteString("Maintenance release: internal improvements only, no user-facing changes.\n")
		return b.String()
	}

	var breaking []change
	for _, c := range changes {
		if c.Breaking {
			breaking = append(breaking, c)
		}
	}
	if len(breaking) > 0 {
		b.WriteString("### Breaking changes\n\n")
		writeList(&b, breaking)
		b.WriteString("\n")
	}

	for _, s := range sectionOrder {
		var bucket []change
		for _, c := range changes {
			if c.Type == s.key && !c.Breaking {
				bucket = append(bucket, c)
			}
		}
		if len(bucket) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n", s.title)
		writeList(&b, bucket)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeList(b *strings.Builder, cs []change) {
	// Stable, readable order within a section: by scope, then summary.
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Scope != cs[j].Scope {
			return cs[i].Scope < cs[j].Scope
		}
		return cs[i].Summary < cs[j].Summary
	})
	for _, c := range cs {
		if c.Scope != "" {
			fmt.Fprintf(b, "- %s (%s)\n", c.Summary, c.Scope)
		} else {
			fmt.Fprintf(b, "- %s\n", c.Summary)
		}
	}
}
