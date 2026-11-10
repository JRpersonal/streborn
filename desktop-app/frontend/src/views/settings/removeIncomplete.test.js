// Remove STR that leaves files behind on the speaker is not a success.
//
// The backend used to answer ok:true with "STR removed (1 entries)" while the
// whole STR folder was still on the speaker (2026-11-10, a SoundTouch 10). It
// now answers ok:false, incomplete:true and the paths that are left. STR no
// longer starts on that speaker and it reboots into plain Bose, so the app
// follows it like after a full removal, but it must say what is left instead
// of the success toast.
//
// Read from the source like the other settings-flow guards: the handler needs
// the Wails runtime and a DOM, and this suite runs in plain node on purpose.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const read = (rel) => readFileSync(join(here, rel), 'utf8').replace(/\r\n/g, '\n');

describe('a removal that left files behind', () => {
  const src = read('actions.js');
  const start = src.indexOf('await UninstallSTR(box.host)');
  const handler = src.slice(start, src.indexOf('finally', start));

  it('is not shown with the success toast', () => {
    expect(start).toBeGreaterThan(0);
    expect(handler).toContain('if (r && (r.ok || r.incomplete)) {');
    expect(handler).toMatch(
      /if \(r\.incomplete\) showToast\(t\('settingsView\.removeSTRIncomplete', \{ paths: state\.strRemovedLeftover\.paths \}\)\);\s*else showToast\(t\('settingsView\.removeSTRDoneToast'/,
    );
  });

  it('remembers the leftover paths for this speaker, and forgets them after a clean removal', () => {
    expect(handler).toMatch(
      /state\.strRemovedLeftover = r\.incomplete\s*\? \{ host: box\.host, paths: \(r\.leftoverFiles \|\| \[\]\)\.join\(', '\) \}\s*: null;/,
    );
  });

  it('still lands on the removed panel, since the speaker is a stock one now', () => {
    const branch = handler.slice(handler.indexOf('if (r && (r.ok || r.incomplete)) {'), handler.indexOf('} else if (r && r.stickPresent)'));
    expect(branch).toContain('purgeSpeakerLocalState(box, state)');
    expect(branch).toContain("renderSTRRemovedPanel($('settingsBody'), state.settingsBox)");
  });

  it('keeps the note on the removed panel, only for the speaker it belongs to', () => {
    const shared = read('shared.js');
    const fn = shared.slice(shared.indexOf('export function renderSTRRemovedPanel'), shared.indexOf('export function wireBackToList'));
    expect(fn).toContain('left && box && left.host === box.host');
    expect(fn).toContain("escapeHtml(t('settingsView.removeSTRIncomplete', { paths: left.paths }))");
    expect(fn).toMatch(/\$\{leftover\}/);
  });

  // The headline above that note said "STR was removed from this speaker",
  // which is the one thing that did not fully happen.
  it('gets its own headline on the removed panel instead of "STR was removed"', () => {
    const shared = read('shared.js');
    const fn = shared.slice(shared.indexOf('export function renderSTRRemovedPanel'), shared.indexOf('export function wireBackToList'));
    expect(fn).toContain("const title = incomplete ? t('settingsView.strRemovedIncompleteTitle') : t('settingsView.strRemovedTitle');");
    expect(fn).toMatch(/<div class="empty-state-title">\$\{escapeHtml\(title\)\}<\/div>/);
    // The title and the note hang on the same condition.
    expect(fn).toContain('const incomplete = !!(left && box && left.host === box.host);');
    expect(fn).toMatch(/const leftover = incomplete\s*\?/);
  });
});

describe('a removal after which STR is still installed', () => {
  const src = read('actions.js');
  const start = src.indexOf('await UninstallSTR(box.host)');
  const handler = src.slice(start, src.indexOf('finally', start));

  it('is told in its own translated sentence, with the paths', () => {
    expect(handler).toMatch(
      /\} else if \(r && r\.stillInstalled\) \{[\s\S]*?reportError\(rmBtn, t\('settingsView\.removeSTRStillInstalled', \{ paths: \(r\.leftoverFiles \|\| \[\]\)\.join\(', '\) \}\)\);/,
    );
  });

  it('is an error on the settings page, never the removed panel', () => {
    const still = handler.indexOf('} else if (r && r.stillInstalled) {');
    const removed = handler.indexOf('if (r && (r.ok || r.incomplete)) {');
    const generic = handler.indexOf("t('settingsView.removeSTRFailed'");
    expect(still).toBeGreaterThan(removed);
    expect(still).toBeLessThan(generic);
    const branch = handler.slice(still, handler.indexOf('} else if', still + 10));
    expect(branch).not.toContain('renderSTRRemovedPanel');
    expect(branch).not.toContain('purgeSpeakerLocalState');
  });
});

describe('the headline and the still-installed sentence', () => {
  const dir = join(here, '../../i18n/bundles');
  const bundles = readdirSync(dir).filter((f) => f.endsWith('.json'));
  const load = (f) => JSON.parse(readFileSync(join(dir, f), 'utf8'));

  it('exist in every bundle the app ships', () => {
    expect(bundles.length).toBeGreaterThanOrEqual(13);
    for (const f of bundles) {
      const b = load(f);
      const title = b['settingsView.strRemovedIncompleteTitle'];
      expect(title, f).toBeTruthy();
      expect(title, f).toContain('STR');
      expect(title, f).not.toBe(b['settingsView.strRemovedTitle']);
      const still = b['settingsView.removeSTRStillInstalled'];
      expect(still, f).toBeTruthy();
      expect(still, f).toContain('{{paths}}');
      expect(still, f).toContain('STR');
      expect(still, f).toContain('60');
      expect(still, f).not.toBe(b['settingsView.removeSTRIncomplete']);
    }
  });

  it('are translated, not the English text copied into the other bundles', () => {
    const en = load('en.json');
    for (const f of bundles.filter((x) => x !== 'en.json')) {
      const b = load(f);
      expect(b['settingsView.strRemovedIncompleteTitle'], f).not.toBe(en['settingsView.strRemovedIncompleteTitle']);
      expect(b['settingsView.removeSTRStillInstalled'], f).not.toBe(en['settingsView.removeSTRStillInstalled']);
    }
  });

  it('say what happened in English', () => {
    const en = load('en.json');
    expect(en['settingsView.strRemovedIncompleteTitle']).toContain('only partly removed');
    expect(en['settingsView.removeSTRStillInstalled']).toContain('could not be removed');
    expect(en['settingsView.removeSTRStillInstalled']).toContain('run Remove again');
  });
});

describe('the leftover sentence', () => {
  const dir = join(here, '../../i18n/bundles');
  const bundles = readdirSync(dir).filter((f) => f.endsWith('.json'));

  it('exists in every bundle the app ships, with the paths in it', () => {
    expect(bundles.length).toBeGreaterThanOrEqual(13);
    for (const f of bundles) {
      const text = JSON.parse(readFileSync(join(dir, f), 'utf8'))['settingsView.removeSTRIncomplete'];
      expect(text, f).toBeTruthy();
      expect(text, f).toContain('{{paths}}');
      expect(text, f).toContain('STR');
      expect(text, f).toContain('Bose');
    }
  });

  it('does not read like a success and says how to clear the files', () => {
    const en = JSON.parse(readFileSync(join(dir, 'en.json'), 'utf8'))['settingsView.removeSTRIncomplete'];
    expect(en).toContain('could not be deleted');
    expect(en).toContain('install STR on it again');
    expect(en).not.toMatch(/^STR removed/);
  });
});
