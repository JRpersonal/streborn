// The single-speaker update says beforehand that a grouped speaker leaves its
// group while it restarts (#1339). The master's firmware drops a follower that
// stops answering, and with one follower the whole zone ends; a permanent group
// takes the speaker back by itself, an ordinary one has to be formed again.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { inLiveGroup, anyInLiveGroup } from './groups.js';

const here = dirname(fileURLToPath(import.meta.url));
const bundleDir = join(here, 'i18n', 'bundles');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const KEY = 'update.groupNote';
const ALL_KEY = 'updateAll.groupNote';

const master = { deviceID: 'AAAA', host: '192.0.2.1', kind: 'str' };
const follower = { deviceID: 'BBBB', host: '192.0.2.2', kind: 'str' };
const twoChip = { deviceID: 'MDNS-ID', host: '192.0.2.3', kind: 'str' };
const solo = { deviceID: 'CCCC', host: '192.0.2.4', kind: 'str' };

describe('inLiveGroup', () => {
  const zoneLive = {
    AAAA: { master: 'AAAA', members: [{ deviceID: 'BBBB', ip: '192.0.2.2' }, { deviceID: 'FIRMWARE-ID', ip: '192.0.2.3' }] },
    BBBB: { master: 'AAAA' },
    CCCC: null,
  };

  it('is true for the master and for a follower', () => {
    expect(inLiveGroup(master, zoneLive)).toBe(true);
    expect(inLiveGroup(follower, zoneLive)).toBe(true);
  });

  it('finds a follower the master lists by IP only', () => {
    expect(inLiveGroup(twoChip, zoneLive)).toBe(true);
  });

  it('is false for a standalone speaker and for an unknown one', () => {
    expect(inLiveGroup(solo, zoneLive)).toBe(false);
    expect(inLiveGroup(solo, {})).toBe(false);
    expect(inLiveGroup(null, zoneLive)).toBe(false);
  });

  it('is false for half of a stereo pair, which survives a restart', () => {
    const pairLive = {
      AAAA: { master: 'AAAA', members: [{ deviceID: 'BBBB', ip: '192.0.2.2' }],
        stereo: { id: 'p1', masterDeviceID: 'AAAA', members: [{ deviceID: 'AAAA', ip: '192.0.2.1' }, { deviceID: 'BBBB', ip: '192.0.2.2' }] } },
      BBBB: { master: 'AAAA' },
    };
    expect(inLiveGroup(master, pairLive)).toBe(false);
    expect(inLiveGroup(follower, pairLive)).toBe(false);
  });
});

describe('anyInLiveGroup', () => {
  const zoneLive = {
    AAAA: { master: 'AAAA', members: [{ deviceID: 'BBBB', ip: '192.0.2.2' }] },
    BBBB: { master: 'AAAA' },
  };

  it('is true when any speaker of the run is grouped, master or follower', () => {
    expect(anyInLiveGroup([solo, follower], zoneLive)).toBe(true);
    expect(anyInLiveGroup([master, solo], zoneLive)).toBe(true);
  });

  it('is false for standalone speakers, an empty run and no run', () => {
    expect(anyInLiveGroup([solo, twoChip], zoneLive)).toBe(false);
    expect(anyInLiveGroup([], zoneLive)).toBe(false);
    expect(anyInLiveGroup(null, zoneLive)).toBe(false);
  });

  it('is false when the only grouping is a stereo pair', () => {
    const pairLive = {
      AAAA: { master: 'AAAA', members: [{ deviceID: 'BBBB', ip: '192.0.2.2' }],
        stereo: { id: 'p1', masterDeviceID: 'AAAA', members: [{ deviceID: 'AAAA', ip: '192.0.2.1' }, { deviceID: 'BBBB', ip: '192.0.2.2' }] } },
      BBBB: { master: 'AAAA' },
    };
    expect(anyInLiveGroup([master, follower, solo], pairLive)).toBe(false);
  });
});

describe('the update-all paths', () => {
  const status = readFileSync(join(here, 'views', 'settings', 'status.js'), 'utf8');
  const main = readFileSync(join(here, 'main.js'), 'utf8');

  it('say beforehand that grouped speakers leave their group', () => {
    // The Settings banner, when "Update all" is on offer.
    expect(status).toContain('updateAllBtn && anyInLiveGroup(updateAllTargets, state.zoneLive)');
    expect(status).toContain(`t('${ALL_KEY}')`);
    // The confirmation before the run starts.
    expect(main).toContain(`if (anyInLiveGroup(updateTargets, state.zoneLive)) notes.push(t('${ALL_KEY}'));`);
    // The speaker update card in the music view, for more than one speaker.
    expect(main).toContain('outdated.length > 1 && anyInLiveGroup(outdated, state.zoneLive)');
  });

  it('carries a real translation in every bundle', () => {
    const en = JSON.parse(readFileSync(join(bundleDir, 'en.json'), 'utf8'))[ALL_KEY];
    expect(en).toBeTruthy();
    for (const l of langs) {
      const b = JSON.parse(readFileSync(join(bundleDir, l + '.json'), 'utf8'));
      const v = b[ALL_KEY];
      expect(v, l).toBeTruthy();
      expect(v, l).not.toEqual(b[KEY]);
      if (l !== 'en') expect(v, l).not.toEqual(en);
      expect(v, l).not.toMatch(/ [-–—] /);
    }
    const de = JSON.parse(readFileSync(join(bundleDir, 'de.json'), 'utf8'))[ALL_KEY];
    expect(de).toMatch(/\bdu\b/);
    expect(de).toMatch(/[äöüß]/);
    expect(de).not.toMatch(/\bSie\b/);
  });
});

describe('the single-speaker update banner', () => {
  const src = readFileSync(join(here, 'views', 'settings', 'status.js'), 'utf8');

  it('shows the group note only next to the single-speaker update button', () => {
    expect(src).toContain('softwareBtn && inLiveGroup(box, state.zoneLive)');
    expect(src).toContain(`t('${KEY}')`);
    expect(src).toMatch(/update\.rebootNote'\)\)\}<\/small>\$\{groupNote\}/);
  });

  it('carries a real translation in every bundle', () => {
    const en = JSON.parse(readFileSync(join(bundleDir, 'en.json'), 'utf8'))[KEY];
    expect(en).toBeTruthy();
    for (const l of langs) {
      const v = JSON.parse(readFileSync(join(bundleDir, l + '.json'), 'utf8'))[KEY];
      expect(v, l).toBeTruthy();
      if (l !== 'en') expect(v, l).not.toEqual(en);
      // No dash as punctuation.
      expect(v, l).not.toMatch(/ [-–—] /);
    }
  });

  it('speaks to the owner with du in German', () => {
    const de = JSON.parse(readFileSync(join(bundleDir, 'de.json'), 'utf8'))[KEY];
    expect(de).toMatch(/\bdu\b/);
    expect(de).not.toMatch(/\bSie\b/);
  });
});

// The note used to say "an ordinary group you form again afterwards", which a
// reporter could not map to anything on screen (#1339). It now names the
// Multi-room button by its own label, so the label has to be the bundle's
// actual button text, word for word.
describe('the group note names the real button', () => {
  it('quotes multiroom.createBtn verbatim in every bundle', () => {
    for (const l of langs) {
      const b = JSON.parse(readFileSync(join(bundleDir, l + '.json'), 'utf8'));
      const btn = b['multiroom.createBtn'];
      expect(btn, l).toBeTruthy();
      expect(b[KEY], l).toContain(btn);
      expect(b[ALL_KEY], l).toContain(btn);
    }
  });
});
