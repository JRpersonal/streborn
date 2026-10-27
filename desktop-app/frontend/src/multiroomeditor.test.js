// The classic Multi-room editors (#1554, open decision 5): the fixed-group
// editor never offers an offline speaker as a member.
// The "main speaker" wording is mainspeakerterm.test.js.
//
// renderMultiroom is DOM-bound and vitest runs in node, so the view is checked
// as source here (the same way multiroomnotes.test.js does) and through the
// pure helpers; e2e/specs/desktop-multiroom.spec.mjs drives the real page.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { dropOfflineMembers } from './groups.js';

const read = (rel) => readFileSync(new URL(rel, import.meta.url), 'utf8').replace(/\r\n/g, '\n');
const multiroomJS = read('./views/multiroom.js');
const BUNDLE_DIR = new URL('./i18n/bundles/', import.meta.url);
const bundles = Object.fromEntries(readdirSync(BUNDLE_DIR)
  .filter((f) => f.endsWith('.json'))
  .map((f) => [f.slice(0, -5), JSON.parse(readFileSync(new URL(f, BUNDLE_DIR), 'utf8'))]));

const fnSrc = (name) => {
  const start = multiroomJS.indexOf(name);
  expect(start, `${name} must still exist`).toBeGreaterThan(-1);
  return multiroomJS.slice(start, multiroomJS.indexOf('\n}\n', start));
};

describe('dropOfflineMembers', () => {
  it('drops the ticks of offline speakers and keeps the rest', () => {
    const slaves = { A: true, B: true, C: false };
    const boxes = [{ deviceID: 'A' }, { deviceID: 'B', offline: true }, { deviceID: 'C', offline: true }];
    expect(dropOfflineMembers(slaves, boxes)).toBe(slaves);
    expect(slaves).toEqual({ A: true, C: false });
  });

  it('tolerates a missing map or list', () => {
    expect(dropOfflineMembers(null, [{ deviceID: 'A', offline: true }])).toBe(null);
    expect(dropOfflineMembers({ A: true }, undefined)).toEqual({ A: true });
  });
});

describe('fixed-group editor and offline speakers (source)', () => {
  const render = fnSrc('export function renderMultiroom(');

  it('drops offline ticks on every paint', () => {
    expect(render).toContain('dropOfflineMembers(state.zoneSlaves, strBoxes)');
  });

  it('marks an offline card disabled with the reason and offers no "make main"', () => {
    expect(render).toMatch(/const offline = !isMaster && !!b\.offline;/);
    expect(render).toMatch(/const selected = !isMaster && !offline && /);
    expect(render).toContain("t('multiroom.offlineNotSelectable')");
    expect(render).toContain('aria-disabled="true"');
  });

  it('a click on a disabled card does not tick it', () => {
    expect(render).toMatch(/id === state\.zoneMaster \|\| card\.getAttribute\('aria-disabled'\) === 'true'\) return;/);
  });

  it('doFormZone never sends an offline member', () => {
    expect(fnSrc('async function doFormZone(')).toMatch(/sel\[b\.deviceID\] && !b\.offline\)/);
  });
});

describe('bundles', () => {
  const NEW = ['multiroom.offlineNotSelectable'];

  it('every bundle translates the new keys (not left in English)', () => {
    for (const [code, b] of Object.entries(bundles)) {
      for (const k of NEW) {
        expect(typeof b[k], `${code} ${k}`).toBe('string');
        expect(b[k].trim(), `${code} ${k}`).not.toBe('');
        if (code !== 'en') expect(b[k], `${code} ${k} is still English`).not.toBe(bundles.en[k]);
      }
    }
  });

  it('German uses du, umlauts and no dash as punctuation', () => {
    const de = bundles.de;
    expect(de['multiroom.offlineNotSelectable']).toMatch(/schalte ihn ein/);
    for (const k of NEW) expect(de[k]).not.toMatch(/\s[-–—]\s/);
  });
});
