import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';

// A view must not set state.currentBox just before handing the speaker to
// speakerPicked, and this is checked against the SOURCE because the defect is
// in the call, not in any function a unit test can reach.
//
// speakerPicked switches speakers only when the picked host DIFFERS from the
// current one. The Library view assigned the new box one line earlier, which
// made that test false, so the switch never ran: no selectBox, and therefore no
// loadPresets. "Save as preset" then offered the PREVIOUS speaker's six slots
// with the previous speaker's labels on them, which is a reliable way to
// overwrite the wrong key. Reported 2026-10-01 against v0.9.92, and invisible
// to every existing test because each half was correct on its own.
//
// setup.js assigns state.currentBox in its own right (clearing it when a
// speaker is removed, adopting one it has just found). That is fine and is not
// what this looks at; only the lines leading into a speakerPicked call are.

const VIEWS = join(import.meta.dirname, 'views');

// viewFiles lists every view module, including the ones split into a folder
// of their own (views/music/, views/settings/), as paths relative to views/.
function viewFiles(dir = '') {
  const out = [];
  for (const name of readdirSync(join(VIEWS, dir))) {
    const rel = dir ? `${dir}/${name}` : name;
    if (statSync(join(VIEWS, rel)).isDirectory()) out.push(...viewFiles(rel));
    else if (name.endsWith('.js') && !name.endsWith('.test.js')) out.push(rel);
  }
  return out;
}

// linesBefore returns the CODE lines preceding an index, comments and blanks
// dropped, newest first. Comments are dropped on purpose: the explanation of
// this very trap sits above the call and mentions the assignment.
function codeLinesBefore(lines, idx, howMany) {
  const out = [];
  for (let i = idx - 1; i >= 0 && out.length < howMany; i--) {
    const t = lines[i].trim();
    if (!t || t.startsWith('//') || t.startsWith('*') || t.startsWith('/*')) continue;
    out.push(t);
  }
  return out;
}

describe('the speakerPicked contract, checked in the source', () => {
  const files = viewFiles();

  it('finds the views, so a rename cannot turn this into a test of nothing', () => {
    expect(files.length).toBeGreaterThan(0);
    expect(files).toContain('library.js');
    expect(files).toContain('music/search.js');
  });

  for (const file of files) {
    const src = readFileSync(join(VIEWS, file), 'utf8');
    if (!src.includes('speakerPicked(')) continue;

    it(`${file} does not set state.currentBox on its way into speakerPicked`, () => {
      const lines = src.split(/\r?\n/);
      const offenders = [];
      lines.forEach((line, i) => {
        if (!/\bspeakerPicked\s*\(/.test(line)) return;
        // The declaration and the no-op default are not calls.
        if (/speakerPicked\s*\(\s*\)\s*\{/.test(line) || /speakerPicked:/.test(line)) return;
        for (const prev of codeLinesBefore(lines, i, 3)) {
          if (/state\.currentBox\s*=/.test(prev)) {
            offenders.push(`${file}:${i + 1} is preceded by "${prev}"`);
          }
        }
      });
      expect(offenders).toEqual([]);
    });
  }
});
