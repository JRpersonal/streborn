// copyreport.js — pure summary of a box-to-box preset-copy rejection.
//
// The Go side (CopyPresetsAcrossBoxes) copies every slot it can and reports
// the rejected ones as ONE combined error of the form
//   "preset 2 (Name): reason; preset 5 (Other): reason"
// A Wails promise rejects with only that message, so the copied count never
// reaches the frontend on a partial failure. This module reconstructs it:
// slots named in the message failed, the rest of the source's valid slots
// copied. Pure and vitest-covered (no DOM, no api.js import).

// summarizePresetCopyError parses the combined per-slot message. Returns
// null when the message does not look like a per-slot report (a transport
// error, "read source presets: ...", etc. — those really are all-or-nothing).
// totalValidSlots may be null/undefined when the source could not be counted;
// copied is then null and the caller must not claim a number.
export function summarizePresetCopyError(message, totalValidSlots) {
  // Wails rejects with the plain message string, but some call paths wrap it
  // in an Error whose coercion prepends "Error: "; strip that so the
  // start-of-message anchor below still sees the first slot entry.
  const msg = String(message == null ? '' : message).replace(/^error:\s*/i, '');
  const failedSlots = [];
  // Anchored on the separator so a station name containing "preset N ("
  // mid-sentence cannot fake an entry; the Go format is stable in-repo.
  const re = /(?:^|;\s*)preset (\d+) \(/g;
  let m;
  while ((m = re.exec(msg)) !== null) {
    const n = parseInt(m[1], 10);
    if (!failedSlots.includes(n)) failedSlots.push(n);
  }
  if (failedSlots.length === 0) return null;
  const total = Number.isInteger(totalValidSlots) ? totalValidSlots : null;
  const copied = total === null ? null : Math.max(0, total - failedSlots.length);
  return { failedSlots, copied, detail: msg };
}

// presetCopyConflict recognises the ONE transfer failure that has a friendly
// explanation: the target speaker already holds one of the stations on a key
// the transfer does not rewrite, so the whole set was refused and nothing was
// written (the agent's already-on-slot guard, #836, applied to the finished
// set, #882). The Go side formats it as
//   already-on-slot: "Station Name" is already on key 6
// Returns {name, slot} or null when the message is something else.
export function presetCopyConflict(message) {
  const s = String(message == null ? '' : message);
  const m = s.match(/already-on-slot:\s*"([\s\S]*)" is already on key (\d+)/);
  if (!m) return null;
  // The name is Go-quoted (%q), so a quote or backslash inside it arrives
  // escaped; hand the caller back the name the user actually sees.
  return { name: m[1].replace(/\\(["\\])/g, '$1'), slot: parseInt(m[2], 10) };
}

// countValidPresetSlots mirrors the Go-side copy filter (slot 1..6 with a
// non-empty name) so "total - failed" equals what the backend attempted.
export function countValidPresetSlots(presets) {
  return (presets || []).filter(
    (p) => p && p.slot >= 1 && p.slot <= 6 && p.name,
  ).length;
}
