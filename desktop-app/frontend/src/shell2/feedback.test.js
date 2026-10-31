// The verdict lines of the UI 2.0 preview's music views (phase 5): every line
// names the speaker, and "Playing" or "confirmed" is said only for what the
// speaker itself reported (docs/UI-2.0.md, rule 2).
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import {
  keyFailedVerdict, keyHolds, keySavingVerdict, keyVerdict, playVerdict, sentVerdict, startingVerdict,
  verdictFromPlayState,
} from './feedback.js';
import { setLocale } from '../i18n/index.js';

setLocale('en');

const dir = new URL('../i18n/bundles/', import.meta.url);
const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
  .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));

describe('play verdicts', () => {
  it('names the speaker while the play is on its way', () => {
    expect(startingVerdict('Kitchen')).toEqual({ kind: 'busy', text: 'Starting on Kitchen...' });
  });

  it('says "Playing" only for a play state the speaker confirmed', () => {
    expect(playVerdict('Kitchen', true)).toEqual({ kind: 'ok', text: 'Playing on Kitchen' });
    const sent = playVerdict('Kitchen', false);
    expect(sent).toEqual({ kind: 'warn', text: 'Sent to Kitchen. It has not started yet.' });
    expect(sent.text).not.toMatch(/Playing/);
    expect(sentVerdict('Bedroom').text).toBe('Sent to Bedroom. It has not started yet.');
  });

  it('maps the speaker\'s answer to waitForPlayState onto the line', () => {
    expect(verdictFromPlayState('Office', 'playing').text).toBe('Playing on Office');
    for (const v of ['timeout', 'rejected']) {
      expect(verdictFromPlayState('Office', v)).toEqual({ kind: 'warn', text: 'Sent to Office. It has not started yet.' });
    }
    // No speaker selected any more: nothing left to say.
    expect(verdictFromPlayState('Office', 'gone')).toBeNull();
  });
});

describe('key verdicts', () => {
  const presets = [{ slot: 3, name: 'Jazz Lounge' }, { slot: 7, name: 'NPR News' }];

  it('reads the key back from the speaker\'s list, by slot and by name', () => {
    expect(keyHolds(presets, 3)).toBe(true);
    expect(keyHolds(presets, 3, '  jazz lounge ')).toBe(true);
    expect(keyHolds(presets, 3, 'Rock FM')).toBe(false);
    expect(keyHolds(presets, 4)).toBe(false);
    expect(keyHolds(null, 3)).toBe(false);
  });

  it('says confirmed only after the read-back, naming key and speaker', () => {
    expect(keySavingVerdict(3, 'Kitchen')).toEqual({ kind: 'busy', text: 'Putting it on key 3 of Kitchen...' });
    expect(keyVerdict(3, 'Kitchen', true)).toEqual({ kind: 'ok', text: 'On key 3 of Kitchen. The speaker confirmed it.' });
    const not = keyVerdict(3, 'Kitchen', false);
    expect(not.kind).toBe('warn');
    expect(not.text).toBe('Kitchen did not confirm key 3. Check the key and try again.');
    expect(not.text).not.toMatch(/confirmed it/);
  });

  it('names key, speaker and reason when a key change fails', () => {
    expect(keyFailedVerdict(2, 'Office', 'The speaker is busy.')).toEqual({ kind: 'error', text: 'Key 2 of Office was not changed: The speaker is busy.' });
    expect(keyFailedVerdict(2, 'Office', 'Nothing plays.', 'warn').kind).toBe('warn');
    expect(keyFailedVerdict(2, 'Office', '').text).toContain('Key 2 of Office');
  });
});

describe('the ui2.feedback strings', () => {
  const keys = Object.keys(bundles.en).filter((k) => k.startsWith('ui2.feedback.'));
  const placeholders = (s) => [...String(s).matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();

  it('are one block right after ui2.radio.unknown', () => {
    const all = Object.keys(bundles.en);
    const at = all.indexOf('ui2.radio.unknown');
    expect(all.slice(at + 1, at + 1 + keys.length)).toEqual(keys);
    expect(keys.length).toBeGreaterThan(30);
  });

  for (const [code, b] of Object.entries(bundles)) {
    it(`${code} has every one, the English placeholders and no en or em dash`, () => {
      for (const k of keys) {
        expect(typeof b[k] === 'string' && b[k].trim().length > 0, `${code}: ${k}`).toBe(true);
        expect(placeholders(b[k]), `${code}: ${k}`).toEqual(placeholders(bundles.en[k]));
        expect(b[k], `${code}: ${k}`).not.toMatch(/[–—]/);
      }
    });
  }

  it('is translated, German with du and real umlauts, and says Use, not Tap', () => {
    for (const [code, b] of Object.entries(bundles)) {
      if (code === 'en') continue;
      expect(b['ui2.feedback.sentNotStarted'], code).not.toBe(bundles.en['ui2.feedback.sentNotStarted']);
    }
    expect(bundles.de['ui2.feedback.sentNotStarted']).toBe('An {{speaker}} gesendet. Es läuft noch nicht.');
    expect(bundles.de['ui2.feedback.noInternet']).toContain('Sieh');
    expect(bundles.de['ui2.feedback.clearFilters']).toContain('zurücksetzen');
    for (const k of keys) expect(bundles.en[k], k).not.toMatch(/\bTap\b/);
  });
});
