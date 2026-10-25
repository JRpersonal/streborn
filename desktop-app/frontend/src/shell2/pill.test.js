import { describe, it, expect } from 'vitest';
import { PILL_KINDS, PILL_LABEL_KEYS, chipHTML, speakerChipKinds, speakerPillKind, statusPillHTML } from './pill.js';
import en from '../i18n/bundles/en.json';
import { setLocale } from '../i18n/index.js';

// The assertions compare with the English bundle, whatever the machine's language.
setLocale('en');

const tile = (o = {}) => ({
  offline: false, stock: false, strNotRunning: false, playing: false, needsUpdate: false,
  noInternet: false, isCurrent: false, standby: false, inStereoPair: false, ...o,
});

describe('speakerPillKind precedence', () => {
  it('says not reachable before anything else', () => {
    expect(speakerPillKind(tile({ offline: true, playing: true, stock: true }))).toBe('notReachable');
  });
  it('tells a first install from a repair on a stock speaker', () => {
    expect(speakerPillKind(tile({ stock: true }))).toBe('boseOriginal');
    expect(speakerPillKind(tile({ stock: true, strNotRunning: true }))).toBe('strNotRunning');
  });
  it('then playing, standby of the current speaker, stereo pair, nothing', () => {
    expect(speakerPillKind(tile({ playing: true, inStereoPair: true }))).toBe('playing');
    expect(speakerPillKind(tile({ isCurrent: true, standby: true, inStereoPair: true }))).toBe('standby');
    // Standby is only known for the speaker the status poll reads.
    expect(speakerPillKind(tile({ standby: true }))).toBe('');
    expect(speakerPillKind(tile({ inStereoPair: true }))).toBe('stereoPair');
    expect(speakerPillKind(tile())).toBe('');
  });
});

describe('pill and chips', () => {
  it('reuses the badge the picker shows for STR not running', () => {
    expect(PILL_LABEL_KEYS.strNotRunning).toBe('speaker.strNotRunningBadge');
    expect(statusPillHTML('strNotRunning')).toContain(en['speaker.strNotRunningBadge']);
  });

  it('shows a playing speaker with an update as the playing pill AND the update chip', () => {
    const t = tile({ playing: true, needsUpdate: true });
    expect(speakerPillKind(t)).toBe('playing');
    expect(speakerChipKinds(t)).toEqual(['update']);
    expect(chipHTML('update')).toContain(en['speaker.updateChip']);
  });

  it('shows a no-internet chip independently of the state', () => {
    expect(speakerChipKinds(tile({ noInternet: true, standby: true, isCurrent: true }))).toEqual(['noInternet']);
    expect(chipHTML('noInternet')).toContain(en['speaker.noInternetChip']);
    expect(chipHTML('nope')).toBe('');
  });

  it('always carries words, and the dot is decoration', () => {
    for (const kind of PILL_KINDS) {
      const html = statusPillHTML(kind);
      const words = html.replace(/<[^>]+>/g, '').trim();
      expect(words.length, kind).toBeGreaterThan(0);
      expect(html).toContain('class="u-pill-dot" aria-hidden="true"');
      expect(en[PILL_LABEL_KEYS[kind]], kind).toBeTruthy();
    }
    expect(statusPillHTML('')).toBe('');
  });
});
