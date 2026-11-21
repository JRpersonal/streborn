// The "unplug it" banner: a wedged speaker and one whose Bose software stopped
// answering (boxHealth firmware-unresponsive) both need a power cycle, but say
// why in their own words, and a Portable, whose battery cannot be taken out,
// is told its own restart instead of being unplugged.
import { describe, it, expect } from 'vitest';
import { setLocale, t } from './i18n/index.js';
import { POWER_CYCLE_HEALTH, needsPowerCycle } from './boxes.js';
import { wedgeBannerLines } from './powercycle.js';

setLocale('en');
const label = (b) => b.friendlyName;
const st30 = { host: '192.0.2.20', friendlyName: 'Living room', model: 'SoundTouch 30', boxHealth: 'firmware-unresponsive' };
const st10 = { host: '192.0.2.22', friendlyName: 'Kitchen', model: 'SoundTouch 10', boxHealth: 'firmware-unresponsive' };
const portable = { host: '192.0.2.21', friendlyName: 'Garden', variant: 'taigan', boxHealth: 'firmware-unresponsive' };
const wedged = { host: '192.0.2.23', friendlyName: 'Bath', boxHealth: 'wedged' };

describe('needsPowerCycle', () => {
  it('is the two power-cycle states and nothing else', () => {
    expect(POWER_CYCLE_HEALTH).toEqual(['wedged', 'firmware-unresponsive']);
    expect(needsPowerCycle(wedged)).toBe(true);
    expect(needsPowerCycle(st30)).toBe(true);
    expect(needsPowerCycle({ boxHealth: 'ok' })).toBe(false);
    expect(needsPowerCycle({})).toBe(false);
    expect(needsPowerCycle(null)).toBe(false);
  });
});

describe('wedgeBannerLines', () => {
  it('is empty when no speaker needs a power cycle', () => {
    expect(wedgeBannerLines([], [], label)).toEqual([]);
    expect(wedgeBannerLines(undefined, undefined, label)).toEqual([]);
  });

  it('names the speakers whose Bose software stopped answering, and tells them to unplug', () => {
    const lines = wedgeBannerLines([], [st30, st10], label);
    expect(lines).toEqual([{ title: t('speaker.fwDeadTitle'), text: t('speaker.fwDeadBanner', { name: 'Living room, Kitchen' }) }]);
    expect(lines[0].text).toContain('30 seconds');
  });

  it('gives a Portable its AUX restart instead of the plug', () => {
    const lines = wedgeBannerLines([], [st30, portable], label);
    expect(lines.map((l) => l.text)).toEqual([
      t('speaker.fwDeadBanner', { name: 'Living room' }),
      t('speaker.fwDeadBannerPortable', { name: 'Garden' }),
    ]);
    expect(lines[1].text).toContain('AUX');
    expect(lines[1].text).not.toMatch(/unplug/i);
  });

  it('keeps the wedged sentence first and separate', () => {
    const lines = wedgeBannerLines([wedged], [st10], label);
    expect(lines.map((l) => l.title)).toEqual([t('speaker.wedgedTitle'), t('speaker.fwDeadTitle')]);
    expect(lines[0].text).toBe(t('speaker.wedgedBanner', { name: 'Bath' }));
  });
});

describe('the words in every bundle', () => {
  const bundles = import.meta.glob('./i18n/bundles/*.json', { eager: true });
  const keys = ['speaker.fwDeadTitle', 'speaker.fwDeadBanner', 'speaker.fwDeadBannerPortable'];
  it('exist, keep the name, name Bose and carry no dash', () => {
    const files = Object.keys(bundles);
    expect(files.length).toBeGreaterThanOrEqual(14);
    for (const f of files) {
      const b = bundles[f].default || bundles[f];
      for (const k of keys) {
        expect(b[k], `${f} ${k}`).toBeTruthy();
        expect(b[k], `${f} ${k}`).toContain('Bose');
        expect(b[k], `${f} ${k}`).not.toMatch(/[\u2013\u2014]| - /);
      }
      expect(b['speaker.fwDeadBanner'], f).toContain('{{name}}');
      expect(b['speaker.fwDeadBannerPortable'], f).toContain('{{name}}');
      expect(b['speaker.fwDeadBannerPortable'], f).toContain('AUX');
    }
  });

  it('says it in German with du and real umlauts', () => {
    setLocale('de');
    expect(t('speaker.fwDeadBanner', { name: 'Küche' })).toBe('Die Bose-Software von "Küche" reagiert nicht mehr. Zieh den Stecker für etwa 30 Sekunden und steck ihn wieder ein.');
    expect(t('speaker.fwDeadBannerPortable', { name: 'Garten' })).toContain('Ladegerät');
    setLocale('en');
  });
});
