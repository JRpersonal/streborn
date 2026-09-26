// foreigninfluence.go: one place that says what OTHER tools have done to this
// speaker, and which of it STR can take back.
//
// Why it exists, in Jens' words on 2026-09-26: users try several tools, and
// they keep trying them, and afterwards nobody can tell which tool caused which
// effect. STR should be the safe haven that shows every foreign effect and
// every leftover, and offers to undo it.
//
// The same day proved how invisible this can be. The ST Remote Pro iOS app
// pointed five speakers at its own cloud, and it did so WITHOUT TOUCHING A
// SINGLE FILE: the override on NAND still read stock, only the running firmware
// named the other host. Every file-based check STR had said the speakers were
// clean. A restart wipes that evidence completely, so a bundle taken the next
// day would have shown nothing at all.
//
// Hence two jobs here, not one:
//
//  1. Collect the findings for the app, each with an honest verdict on whether
//     STR can undo it and how.
//  2. Log a change the moment it is seen, on a STABLE, GREPPABLE prefix, so the
//     agent log on NAND carries a timeline that survives the reboot. That log
//     ships in every diagnostic bundle, which is what lets a later sweep search
//     across users for "who else has been here" instead of guessing.
//
// Deliberately NOT a new file on NAND. The log already persists and already
// travels; a second store would be more writes on hardware that is meant to be
// spared (see the standing rule about box wear).

package webui

import (
	"os"
	"sort"
	"strings"
	"sync"
)

// foreignLogPrefix is the one string a later sweep greps for across bundles.
// Every line about a foreign influence starts with it, so "what has touched
// people's speakers" is one search rather than a reading exercise. Do not
// reword it without updating the sweep that reads it.
const foreignLogPrefix = "foreign influence:"

// knownTool maps a fingerprint found in a cloud URL to the tool behind it.
// Substring match on the host, because these tools version their paths.
//
// An unrecognised foreign address is NOT an error and must not be hidden: it is
// reported as an unknown tool with the address quoted, which is exactly the
// case that teaches us about the next one.
var knownTools = []struct{ fingerprint, tool string }{
	{"stremotepro.com", "ST Remote Pro"},
	{"content.api.bose.io:7777", "OpenCloudTouch"},
	{"bose.io:7777", "OpenCloudTouch"},
	{"aftertouch", "AfterTouch"},
}

// toolFor names the tool behind a value, or "" when nothing matches.
func toolFor(value string) string {
	v := strings.ToLower(value)
	for _, k := range knownTools {
		if strings.Contains(v, k.fingerprint) {
			return k.tool
		}
	}
	return ""
}

// Undo verbs, kept as constants because the app branches on them and a typo
// would silently turn a repairable finding into an unrepairable one.
const (
	undoReboot  = "reboot"  // the config is right; the firmware must re-read it
	undoRewrite = "rewrite" // the config is wrong; STR rewrites it, then a reboot
	undoDelete  = "delete"  // inert leftover files STR can remove
	// undoModRemove routes to the existing remove-conflicting-mod endpoint
	// instead of duplicating it. That one knows the per-tool file set and,
	// importantly, heals the cloud URL BEFORE deleting anything, because the
	// backups it deletes are the template the heal reads from.
	undoModRemove = "mod-remove"
	undoNone      = "none" // STR cannot touch it, and says why
)

// ForeignFinding is one thing another tool did or left behind.
type ForeignFinding struct {
	// ID is stable across versions so the app and any later analysis can key
	// on it rather than on prose.
	ID string `json:"id"`
	// Tool is the best guess, empty when the fingerprint is not known.
	Tool string `json:"tool,omitempty"`
	// Detail is the raw evidence, quoted for the user and for a later sweep.
	Detail string `json:"detail,omitempty"`
	// Active separates something the speaker is acting on right now from an
	// inert leftover. A leftover is worth showing and not worth alarming about.
	Active bool `json:"active"`
	// Undo says what STR would have to do. undoNone carries a Note.
	Undo string `json:"undo"`
	Note string `json:"note,omitempty"`
}

// collectForeignInfluence gathers every foreign trace STR can see.
//
// Cheap by construction: a handful of file reads plus autopair's already-cached
// view of the firmware's own answer. No request is made to the speaker for
// this, so it is safe on the polled paths.
func collectForeignInfluence() []ForeignFinding {
	out := []ForeignFinding{}

	// 1. The live cloud address. The invisible one, and the only one that
	// matters while a rival app is actually running.
	if live := liveForeignCloudURL(); live != "" {
		out = append(out, ForeignFinding{
			ID:     "cloud-url-live",
			Tool:   toolFor(live),
			Detail: live,
			Active: true,
			// The file is checked separately below; when it is stock, a
			// restart is the whole cure, which is what was verified on five
			// speakers on 2026-09-26.
			Undo: undoReboot,
		})
	}

	// 2. The cloud addresses in the configuration files. Survives a reboot,
	// so this is the one that keeps coming back.
	if files := foreignCloudURLFiles(); files != "" {
		out = append(out, ForeignFinding{
			ID:     "cloud-url-file",
			Tool:   toolFor(files),
			Detail: files,
			Active: true,
			Undo:   undoRewrite,
		})
	}

	// 3. Marker files and directories of a rival tool. Often inert: the tool
	// may be long gone and only its leftovers remain, which is why this is
	// reported as not active unless a cloud URL corroborates it.
	if mod := detectConflictingMod(); mod != "" {
		out = append(out, ForeignFinding{
			ID:     "mod-files",
			Tool:   mod,
			Detail: mod + " files on the speaker",
			Active: liveForeignCloudURL() != "" || foreignCloudURLFiles() != "",
			Undo:   undoModRemove,
		})
	}

	// 4. Another tool's backups of the files it replaced. Harmless on their
	// own, but they are the fingerprint that outlives a factory reset, so a
	// bundle that carries them explains a speaker nothing else explains.
	if backups := octBackupFiles(); len(backups) > 0 {
		out = append(out, ForeignFinding{
			ID:     "mod-backups",
			Tool:   "OpenCloudTouch",
			Detail: strings.Join(backups, " "),
			Active: false,
			Undo:   undoDelete,
		})
	}
	if _, err := os.Stat(octHostsBackupPath); err == nil {
		out = append(out, ForeignFinding{
			ID:     "mod-hosts-backup",
			Tool:   "OpenCloudTouch",
			Detail: octHostsBackupPath,
			Active: false,
			Undo:   undoDelete,
		})
	}

	// 5. A redirect block another tool wrote into the PERSISTENT hosts file.
	// STR neutralises this at boot rather than removing it, because the file
	// lives on the read-only rootfs and cleaning it would mean remounting that
	// read-write, which is firmware bending and off the table by standing rule.
	// Saying so beats leaving the user to wonder why one row has no button.
	if block := foreignHostsBlock(); block != "" {
		out = append(out, ForeignFinding{
			ID:     "hosts-block",
			Tool:   toolFor(block),
			Detail: block,
			Active: false,
			Undo:   undoNone,
			Note: "left in place on purpose: this file is on the read-only system partition. " +
				"STR strips the block from the live copy at every boot, so it has no effect.",
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// foreignHostsBlock returns the foreign redirect lines in the persistent hosts
// file, or "" when there are none. STR's own block is excluded: it is not a
// foreign influence, it is the product working.
func foreignHostsBlock() string {
	b, err := os.ReadFile(hostsOriginalPath)
	if err != nil {
		return ""
	}
	hits := []string{}
	inSTR := false
	for _, line := range strings.Split(string(b), "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.Contains(l, "streborn begin"):
			inSTR = true
			continue
		case strings.Contains(l, "streborn end"):
			inSTR = false
			continue
		}
		if inSTR || l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		// A redirect of a Bose cloud host that is not ours is somebody else's.
		low := strings.ToLower(l)
		if strings.Contains(low, "bose.com") || strings.Contains(low, "bose.io") {
			hits = append(hits, l)
		}
	}
	if len(hits) == 0 {
		return ""
	}
	return strings.Join(hits, " | ")
}

// foreignSummary is the one-line form used for the log and for change
// detection. Stable ordering, so an unchanged state produces an identical
// string and nothing is logged.
func foreignSummary(findings []ForeignFinding) string {
	if len(findings) == 0 {
		return ""
	}
	parts := make([]string, 0, len(findings))
	for _, f := range findings {
		tool := f.Tool
		if tool == "" {
			tool = "unknown"
		}
		state := "leftover"
		if f.Active {
			state = "active"
		}
		parts = append(parts, f.ID+"="+tool+"/"+state)
	}
	return strings.Join(parts, " ")
}

var (
	foreignSeenMu sync.Mutex
	foreignSeen   string
	foreignFirst  = true
)

// noteForeignInfluence logs the state the first time it is seen and on every
// change afterwards, and says nothing in between.
//
// The cadence is the point. This runs on paths that are polled, and a line per
// poll would be exactly the log spam the box-wear rule forbids. A line per
// CHANGE is what turns the agent log on NAND into a timeline: it survived the
// reboot on 2026-09-26 when the firmware's own ring did not, and it is what
// ships in every diagnostic bundle.
func (s *Server) noteForeignInfluence(findings []ForeignFinding) {
	summary := foreignSummary(findings)
	foreignSeenMu.Lock()
	first := foreignFirst
	changed := summary != foreignSeen
	foreignSeen = summary
	foreignFirst = false
	foreignSeenMu.Unlock()
	if !changed && !first {
		return
	}
	if summary == "" {
		// Worth a line of its own: it is the moment a repair actually landed,
		// and without it the log shows the problem and never its end.
		if !first {
			s.logger.Info(foreignLogPrefix + " none left, this speaker is STR's own again")
		}
		return
	}
	for _, f := range findings {
		s.logger.Warn(foreignLogPrefix+" seen", "id", f.ID, "tool", f.Tool,
			"active", f.Active, "undo", f.Undo, "detail", f.Detail)
	}
}
