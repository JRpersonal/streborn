// The Radio keys heading and the Spotify screen name half of a live stereo
// pair as the pair, like the sidebar and the Library (#1587: "Keys on
// Bose (Grace left)" and "Liked Songs on Bose (Grace left)" on a pair).
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const src = (p) => readFileSync(new URL(p, import.meta.url), 'utf8');
const body = (text, start) => {
  const at = text.indexOf(start);
  expect(at, start).toBeGreaterThanOrEqual(0);
  return text.slice(at, text.indexOf('\n}', at) + 2);
};

describe('a stereo pair is named as the pair', () => {
  it('on the Radio keys heading', () => {
    const text = src('./views/music/ui2radio.js');
    const at = text.indexOf('const speakerName = ');
    expect(at).toBeGreaterThanOrEqual(0);
    expect(text.slice(at, text.indexOf(';', at))).toContain('speakerUnitName(box');
  });

  // Since #1077 issue 27: the heading said the pair, but the lines a key writes
  // (save, rename, clear, recall), the hold card and the key picker still named
  // the half ("Key 2 of Kitchen left is empty now"). They all take the
  // one helper (unitname.js) and none goes back to the bare speaker label.
  it('in everything a key reports, the hold card and the key picker', () => {
    for (const file of ['./views/music/save.js', './views/music/ui2keyedit.js', './views/music/picker.js']) {
      const text = src(file);
      expect(text, file).toContain('speakerUnitName(');
      expect(text, file).not.toContain('getBoxLabel');
    }
    const tiles = src('./views/music/presettiles.js');
    for (const fn of ['export function lostKeyNotice', 'export async function clearPresetKey', 'export async function renameOutcome', 'async function renamePresetKey', 'function renderPresetRowChrome']) {
      const part = body(tiles, fn);
      expect(part, fn).toContain('speakerUnitName(');
      expect(part, fn).not.toContain('getBoxLabel');
    }
    expect(body(src('./views/music/presets.js'), 'export async function recallBoxPreset')).toContain('speakerUnitName(box)');
  });
  it('on the Spotify screen', () => {
    const fn = body(src('./views/spotifyui2.js'), 'function speakerName(box)');
    expect(fn).toContain('pairUnitName(box, state.zoneLive, state.boxes');
    expect(fn).toContain("t('ui2.side.pairName', { name: pair })");
  });
});
