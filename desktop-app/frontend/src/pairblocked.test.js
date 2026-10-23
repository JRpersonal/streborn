// #938: "Braucht zwei SoundTouch 10" shown to an owner of six of them.
//
// The exclusion is deliberate and documented in multiroom.js: a speaker keeps
// one zone document, so pairing a member of a saved permanent group would
// replace the group with the pair and the group would be gone. His six ST10s
// were all in his two permanent groups, so every candidate was fenced off.
//
// The rule stays. The message was the bug: it named a condition he plainly met,
// leaving him no conclusion except that STR was broken.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { allSource, srcOf } from './test/source.js';

const view = allSource();
// The Multi-Room renderer, where the stereo candidates are picked.
const stereo = srcOf('renderMultiroom');
const dir = fileURLToPath(new URL('./i18n/bundles/', import.meta.url));
const langs = readdirSync(dir).filter((f) => f.endsWith('.json')).map((f) => f.replace('.json', ''));
const bundle = (l) => JSON.parse(readFileSync(new URL(`./i18n/bundles/${l}.json`, import.meta.url), 'utf8'));

describe('the stereo pair refusal', () => {
  it('tells the two reasons apart', () => {
    expect(view).toContain('fencedByGroup');
    expect(view).toContain('multiroom.stereoBlockedByGroup');
    // It may only claim "needs two" when there really are fewer than two.
    const note = stereo.slice(stereo.indexOf('stereoBlockedByGroup') - 200, stereo.indexOf('stereoBlockedByGroup') + 200);
    expect(note).toContain('stereoNeedTwo');
  });

  it('counts the ST10s that exist before the fence is applied', () => {
    const at = stereo.indexOf('const fencedByGroup');
    expect(at, 'the reason flag is gone').toBeGreaterThan(-1);
    const decl = stereo.slice(stereo.indexOf('const st10s'), at + 120);
    // Counted from all known speakers, not from the already-filtered candidates,
    // or the flag can never become true.
    expect(decl).toContain('strBoxes.filter');
    expect(decl).toContain('pairCands.length < 2');
    expect(decl).toContain('st10s.length >= 2');
  });

  it('keeps the fence itself, which is what protects the saved group', () => {
    expect(view).toContain('pairBlockedHosts');
    expect(view).toContain('!storedGroupHosts.has(b.host)');
  });

  it('has the explanation in every language, and says what to do', () => {
    for (const l of langs) {
      const v = bundle(l)['multiroom.stereoBlockedByGroup'];
      expect(v, l).toBeTruthy();
      expect(v.length, `${l} is too short to explain and advise`).toBeGreaterThan(60);
    }
    // The English and German ones must name the way out, or it is only a
    // politer dead end.
    expect(bundle('en')['multiroom.stereoBlockedByGroup']).toMatch(/remove/i);
    expect(bundle('de')['multiroom.stereoBlockedByGroup']).toMatch(/Nimm|entferne/i);
  });
});
