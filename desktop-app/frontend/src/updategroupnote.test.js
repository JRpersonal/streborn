// The single-speaker update says beforehand that a grouped speaker leaves its
// group while it restarts (#1339). The master's firmware drops a follower that
// stops answering, and with one follower the whole zone ends; a permanent group
// takes the speaker back by itself, an ordinary one has to be formed again.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { inLiveGroup } from './groups.js';

const here = dirname(fileURLToPath(import.meta.url));
const bundleDir = join(here, 'i18n', 'bundles');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const KEY = 'update.groupNote';

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
