// Discussion #1874: in a folder of the music library the row of the track
// that plays right now is marked clearly, in the playing colour with moving
// bars where its icon was, in the desktop app (both layouts) and on the phone
// remote. The mark follows the music while the list stays open, and a row is
// matched by the track's address, never by its title.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { trackRowHTML } from './shell2/library.js';
import { setLocale } from './i18n/index.js';

setLocale('en');

const read = (rel) => readFileSync(new URL(rel, import.meta.url), 'utf8').replace(/\r\n/g, '\n');
const view = read('./views/library.js');
const phone = read('../../../internal/webui/assets/index.html');

function phoneFn(name) {
  const start = phone.indexOf(`function ${name}(`);
  if (start < 0) throw new Error(`${name} not found on the phone page`);
  return phone.slice(start, phone.indexOf('\n}\n', start) + 2);
}

describe('the playing track in the desktop Library', () => {
  const item = { id: 't1', title: 'Blue Hour', streamURL: 'http://192.0.2.60/1.flac' };

  it('marks the UI 2.0 row that plays and leaves the others plain', () => {
    const on = trackRowHTML(item, { key: 'u:http://192.0.2.60/1.flac', attr: 'iid', value: 't1', playing: true });
    expect(on).toContain('class="lib2-item lib2-track library-row-track is-now"');
    expect(on).toContain('data-skey="u:http://192.0.2.60/1.flac"');
    expect(on).toContain('aria-current="true"');
    const off = trackRowHTML(item, { key: 'u:http://192.0.2.60/1.flac', attr: 'iid', value: 't1' });
    expect(off).not.toContain('is-now');
    expect(off).not.toContain('aria-current');
    // The key is on the row either way: the mark is moved onto it later.
    expect(off).toContain('<li class="lib2-item lib2-track library-row-track" data-skey="u:http://192.0.2.60/1.flac">');
  });

  it('matches a row by its stream address, not by its title', () => {
    expect(view).toMatch(/function rowPlays\(key\) \{\n\s+return key\.startsWith\('u:'\) && key\.length > 2 && libNowURLs\.includes\(key\.slice\(2\)\);/);
    expect(view).toMatch(/return `u:\$\{\(item && item\.streamURL\)/);
  });

  it('draws the mark into the classic rows and the search hits too', () => {
    const marked = view.match(/<li class="library-row library-row-track\$\{nowRowClass\(statusKey\(it\)\)\}"\$\{nowRowAttrs\(statusKey\(it\)\)\}/g) || [];
    expect(marked.length).toBe(2);
    const drawn = view.match(/playing: rowPlays\(statusKey\(it\)\),/g) || [];
    expect(drawn.length).toBe(2);
  });

  it('moves the mark in place in both layouts when the track changes', () => {
    const at = view.indexOf('export function libraryNowPlaying(');
    const body = view.slice(at, view.indexOf('\n}\n', at));
    expect(body).toContain('paintLibNow(key);');
    // The classic layout used to return here before anything was painted.
    expect(body).not.toContain('if (!ui2()) return;');
    expect(body).toContain('if (ui2()) paintLibStatus(key);');
  });

  for (const [name, file, sel, colour] of [
    ['classic', './style.css', '.library-row-track.is-now .library-icon', 'var(--signal-green)'],
    ['UI 2.0', './shell2/library.css', 'html.ui2 #view-library .lib2-track.is-now .lib2-icon', 'var(--u-ok)'],
  ]) {
    it(`styles it in the ${name} layout from the existing playing colour, with no asset`, () => {
      const css = read(file);
      expect(css).toContain(`${sel} { color: ${colour};`);
      const at = css.indexOf(`${sel}::after {`);
      expect(at).toBeGreaterThan(-1);
      const rule = css.slice(at, css.indexOf('}', at));
      expect(rule).toContain('linear-gradient(currentColor, currentColor)');
      expect(rule).not.toContain('url(');
      // The bars only move for a viewer who did not ask for less motion.
      expect(css).toMatch(new RegExp(`@media \\(prefers-reduced-motion: no-preference\\) \\{\\n\\s+${sel.replace(/[.#]/g, '\\$&')}::after \\{ animation:`));
    });
  }
});

describe('the playing track on the phone remote', () => {
  const browseNowURLs = new Function(`${phoneFn('browseLocPlain')}\n${phoneFn('browseNowURLs')}\nreturn browseNowURLs;`)();

  it('takes the track the agent queue is on, which leads the speaker', () => {
    expect(browseNowURLs(true, { url: 'http://192.0.2.60/2.flac' }, 'http://192.0.2.60/1.flac')).toEqual(['http://192.0.2.60/2.flac']);
  });

  it('falls back to the speaker location, with its XML escapes undone', () => {
    expect(browseNowURLs(true, null, 'http://192.0.2.60/get?a=1&amp;b=2')).toEqual(['http://192.0.2.60/get?a=1&b=2']);
    expect(browseNowURLs(true, { url: '' }, 'http://192.0.2.60/1.flac')).toEqual(['http://192.0.2.60/1.flac']);
  });

  it('marks nothing while the speaker does not play', () => {
    expect(browseNowURLs(false, { url: 'http://192.0.2.60/2.flac' }, 'http://192.0.2.60/2.flac')).toEqual([]);
    expect(browseNowURLs(true, null, '')).toEqual([]);
  });

  it('keys the folder rows and the search hits by the track address and repaints on every status poll', () => {
    const keyed = phone.match(/if \(it\.url\) \{ row\.setAttribute\('data-url', it\.url\); browseMarkRow\(row\); \}/g) || [];
    expect(keyed.length).toBe(2);
    expect(phone).toContain('browseMarkNow(browseNowURLs(playing && upnpPlay, queue, nowLoc));');
    expect(phoneFn('browseMarkRow')).toContain("row.classList.toggle('now', on);");
  });

  it('styles the row from the accent colour, with no asset', () => {
    expect(phone).toContain('.station.now .sname { color:var(--accent); font-weight:600; }');
    const at = phone.indexOf('.station.now .sname::before {');
    const rule = phone.slice(at, phone.indexOf('}', at));
    expect(rule).toContain('linear-gradient(currentColor,currentColor)');
    expect(rule).not.toContain('url(');
  });
});
