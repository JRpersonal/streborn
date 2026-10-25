// Every string the UI 2.0 shell renders exists in every shipped language,
// with the same placeholders as the English one.
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { fileSource } from '../test/source.js';
import { navLabelKeys } from './nav.js';
import { CHIP_LABEL_KEYS, PILL_LABEL_KEYS } from './pill.js';

const dir = new URL('../i18n/bundles/', import.meta.url);
const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
  .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));

// Every ui2.* key the code uses, found in the code itself so a new one cannot
// be forgotten here.
const used = new Set();
for (const f of ['shell2/markup.js', 'shell2/mount.js', 'shell2/rooms.js', 'shell2/pill.js', 'main.js']) {
  for (const m of fileSource(f).matchAll(/'(ui2\.[a-zA-Z.]+)'/g)) used.add(m[1]);
}
const KEYS = [...new Set([
  ...used,
  ...navLabelKeys(),
  ...Object.values(PILL_LABEL_KEYS),
  ...Object.values(CHIP_LABEL_KEYS),
  'speaker.searching',
  'speaker.offlineTooltip',
  'nav.setupStick',
  'nav.newFeatureDot',
])].sort();

const placeholders = (s) => [...String(s).matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();

describe('UI 2.0 strings', () => {
  it('ships all thirteen bundles', () => {
    expect(Object.keys(bundles)).toHaveLength(13);
  });

  it('finds the new keys in the code', () => {
    expect(used.size).toBeGreaterThan(20);
  });

  for (const [code, b] of Object.entries(bundles)) {
    it(`${code} has every shell string with the English placeholders`, () => {
      for (const k of KEYS) {
        expect(typeof b[k] === 'string' && b[k].trim().length > 0, `${code}: ${k}`).toBe(true);
        expect(placeholders(b[k]), `${code}: ${k}`).toEqual(placeholders(bundles.en[k]));
      }
    });
  }

  it('writes German with real umlauts', () => {
    expect(bundles.de['ui2.player.empty']).toContain('Wähle');
    expect(bundles.de['ui2.preview.hint']).toContain('lädt');
  });
});
