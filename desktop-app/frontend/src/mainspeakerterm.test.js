// One word for the group's lead speaker (#1554, open decision 5): the
// Multi-room view and the speaker frames said "main speaker" almost
// everywhere, but the group name note said "master speaker", and German mixed
// "Hauptlautsprecher", "Haupt-Lautsprecher" and "Master-Lautsprecher" for the
// same role. It is "main speaker" / "Hauptlautsprecher" now, in every bundle.
// The keys keep their names (multiroom.masterLabel, groupMasterTitle): only
// the text a user reads changed.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';

const BUNDLE_DIR = new URL('./i18n/bundles/', import.meta.url);
const bundles = Object.fromEntries(readdirSync(BUNDLE_DIR)
  .filter((f) => f.endsWith('.json'))
  .map((f) => [f.slice(0, -5), JSON.parse(readFileSync(new URL(f, BUNDLE_DIR), 'utf8'))]));

// The role keys of the Multi-room view and the speaker frames.
const roleKeys = (b) => Object.keys(b).filter((k) => /^multiroom\.|^speaker\.(permanent|stereoPair|groupLabel)/.test(k));
// Placeholders such as {{master}} are code, not text.
const text = (s) => String(s).replace(/\{\{\s*\w+\s*\}\}/g, '');

describe('the lead speaker is the main speaker', () => {
  it('English calls the role "main speaker", never "master"', () => {
    for (const k of roleKeys(bundles.en)) expect(text(bundles.en[k]), k).not.toMatch(/master/i);
    expect(bundles.en['multiroom.groupNameNote']).toContain("main speaker's name");
  });

  it('German says "Hauptlautsprecher", never "Haupt-Lautsprecher" or "Master"', () => {
    for (const k of roleKeys(bundles.de)) {
      expect(text(bundles.de[k]), k).not.toMatch(/Haupt-Lautsprecher|Master/);
    }
    expect(bundles.de['multiroom.masterLabel']).toMatch(/^Hauptlautsprecher /);
    expect(bundles.de['multiroom.groupNameNote']).toContain('des Hauptlautsprechers');
  });

  it('no bundle keeps a "master" word for the role', () => {
    const MASTER = /master|maestro|maîtresse|マスター/i;
    for (const [code, b] of Object.entries(bundles)) {
      for (const k of roleKeys(b)) expect(text(b[k]), `${code} ${k}`).not.toMatch(MASTER);
    }
  });

  it('Japanese uses one word for the pair\'s main speaker too', () => {
    expect(bundles.ja['speaker.stereoPairTitle']).toContain('メインスピーカー');
    expect(bundles.ja['speaker.stereoPairTitle']).not.toContain('主スピーカー');
  });
});
