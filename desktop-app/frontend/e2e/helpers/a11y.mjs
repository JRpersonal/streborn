// helpers/a11y.mjs: axe scans against a checked-in baseline.
//
// A baseline file (baselines/a11y-*.json) maps a scan key to the WCAG rules
// that scan is known to break, with a node count and a hand-written note:
//
//   { "dark|old|box": { "color-contrast": { "count": 3, "note": "why, and the issue" } } }
//
// A new rule, or more nodes than the baseline allows, fails. So does a
// baseline that is higher than reality or lists a rule that no longer fires:
// the baseline only goes down. Scan keys a run did not visit are left alone.
// E2E_UPDATE_A11Y=1 writes the file instead (run that with --workers=1) and
// keeps the existing notes; a new entry gets a TODO note, and a TODO note
// fails the normal run until somebody writes the real reason.

import AxeBuilder from '@axe-core/playwright';
import { expect } from '@playwright/test';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

// Inject before a scan or a geometry check: transitions and animations would
// otherwise be caught half-way.
export const KILL_ANIM = '*,*::before,*::after{transition:none!important;animation:none!important}';

export const WCAG_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'];

const TODO = 'TODO: say why this is accepted, with an issue number';

function load(file) {
  try { return JSON.parse(readFileSync(file, 'utf8')); } catch { return {}; }
}

export async function scan(page, key, baselineFile, { include, exclude } = {}) {
  let builder = new AxeBuilder({ page }).withTags(WCAG_TAGS);
  if (include) builder = builder.include(include);
  if (exclude) builder = builder.exclude(exclude);
  const results = await builder.analyze();
  const actual = {};
  for (const v of results.violations) actual[v.id] = v.nodes.length;

  const file = fileURLToPath(new URL(`../${baselineFile}`, import.meta.url));
  const all = load(file);
  const base = all[key] || {};

  if (process.env.E2E_UPDATE_A11Y) {
    const next = {};
    for (const [rule, count] of Object.entries(actual).sort()) {
      next[rule] = { count, note: (base[rule] && base[rule].note) || TODO };
    }
    const fresh = load(file); // re-read: another scan may have written meanwhile
    if (Object.keys(next).length) fresh[key] = next; else delete fresh[key];
    const sorted = Object.fromEntries(Object.entries(fresh).sort(([a], [b]) => a.localeCompare(b)));
    writeFileSync(file, JSON.stringify(sorted, null, 2) + '\n');
    return results;
  }

  const problems = [];
  for (const [rule, count] of Object.entries(actual)) {
    const b = base[rule];
    if (!b) problems.push(`${rule}: ${count} node(s), not in the baseline`);
    else if (count > b.count) problems.push(`${rule}: ${count} node(s), the baseline allows ${b.count}`);
  }
  for (const [rule, b] of Object.entries(base)) {
    if (!(rule in actual)) problems.push(`${rule}: no longer fires; the baseline is stale, remove it`);
    else if (actual[rule] < b.count) problems.push(`${rule}: ${actual[rule]} node(s) now, the baseline says ${b.count}; the baseline is stale, lower it`);
    if (!b.note || b.note.startsWith('TODO')) problems.push(`${rule}: the baseline entry has no note`);
  }
  const detail = results.violations
    .filter((v) => problems.some((p) => p.startsWith(v.id + ':')))
    .map((v) => `${v.id} (${v.impact}): ${v.help}\n  ${v.nodes.slice(0, 5).map((n) => n.target.join(' ')).join('\n  ')}`)
    .join('\n');
  expect(problems, `axe ${key} against ${baselineFile}\n${detail}`).toEqual([]);
  return results;
}
