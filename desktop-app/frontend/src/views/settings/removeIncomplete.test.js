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
