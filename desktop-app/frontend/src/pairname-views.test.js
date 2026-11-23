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
    const fn = body(src('./views/music/ui2radio.js'), 'const speakerName = ');
    expect(fn).toContain('pairUnitName(box, state.zoneLive, state.boxes');
    expect(fn).toContain("t('ui2.side.pairName', { name: pair })");
  });
  it('on the Spotify screen', () => {
    const fn = body(src('./views/spotifyui2.js'), 'function speakerName(box)');
    expect(fn).toContain('pairUnitName(box, state.zoneLive, state.boxes');
    expect(fn).toContain("t('ui2.side.pairName', { name: pair })");
  });
});
