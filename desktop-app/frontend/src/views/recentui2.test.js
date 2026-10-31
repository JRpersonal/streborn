// Recently played in the UI 2.0 preview (#1286): its strings in every
// language, its styles scoped to the preview, and the speaker each button
// names being the speaker it acts on.
import { describe, it, expect, beforeEach } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { state } from '../state.js';
import { cardPlayBox, cardKeyBox, recentFocusMark, recentFocusTarget } from './recent.js';

const dir = new URL('../i18n/bundles/', import.meta.url);
const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
  .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));
const src = readFileSync(new URL('./recent.js', import.meta.url), 'utf8');
const css = readFileSync(new URL('../shell2/recent.css', import.meta.url), 'utf8');

const used = [...new Set([...src.matchAll(/'(ui2\.recent\.[a-zA-Z]+)'/g)].map((m) => m[1]))].sort();
const placeholders = (s) => [...String(s).matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();

describe('Recently played preview strings', () => {
  it('finds the keys in the view', () => {
    expect(used.length).toBeGreaterThanOrEqual(12);
  });

  for (const [code, b] of Object.entries(bundles)) {
    it(`${code} has every key, the English placeholders and no em dash`, () => {
      for (const k of used) {
        expect(typeof b[k] === 'string' && b[k].trim().length > 0, `${code}: ${k}`).toBe(true);
        expect(placeholders(b[k]), `${code}: ${k}`).toEqual(placeholders(bundles.en[k]));
        expect(b[k], `${code}: ${k}`).not.toContain('—');
      }
    });
  }

  it('is translated, not copied from English', () => {
    for (const [code, b] of Object.entries(bundles)) {
      if (code === 'en') continue;
      expect(b['ui2.recent.emptyAll'], code).not.toBe(bundles.en['ui2.recent.emptyAll']);
    }
    expect(bundles.de['ui2.recent.earlier']).toBe('Früher');
    expect(bundles.de['ui2.recent.emptyAll']).toContain('deinen');
  });
});

describe('Recently played preview styles', () => {
  it('scopes every rule to the preview view and reads only the shell tokens', () => {
    const noComments = css.replace(/\/\*[\s\S]*?\*\//g, '');
    const selectors = [];
    for (const m of noComments.matchAll(/([^{}]+)\{/g)) {
      for (const s of m[1].trim().split(/,(?![^(]*\))/)) selectors.push(s.trim());
    }
    expect(selectors.length).toBeGreaterThan(10);
    for (const s of selectors) expect(s, s).toMatch(/^html\.ui2 #view-recent($|[\s.:])/);
    expect(noComments).not.toMatch(/var\(--(?!u-)/);
  });

  it('uses only tokens that shell2.css defines', () => {
    const shell = readFileSync(new URL('../shell2/shell2.css', import.meta.url), 'utf8');
    const defined = new Set([...shell.matchAll(/(--u-[a-z0-9-]+):/g)].map((m) => m[1]));
    const read = [...new Set([...css.matchAll(/var\((--u-[a-z0-9-]+)\)/g)].map((m) => m[1]))];
    expect(read.length).toBeGreaterThan(5);
    for (const v of read) expect(defined.has(v), v).toBe(true);
  });
});

describe('the speaker a card button acts on', () => {
  const current = { host: '192.0.2.1', port: 8888, friendlyName: 'Living room' };
  const other = { host: '192.0.2.2', port: 8888, friendlyName: 'Bedroom' };
  beforeEach(() => { state.currentBox = current; });

  it('plays radio on the speaker you control, everything else where it was played', () => {
    expect(cardPlayBox({ source: 'radio', box: other })).toBe(current);
    expect(cardPlayBox({ source: 'upnp', box: other })).toBe(other);
    expect(cardPlayBox({ source: 'spotify', box: other })).toBe(other);
    expect(cardPlayBox({ source: 'pandora', box: other })).toBe(other);
    expect(cardPlayBox({ source: 'upnp' })).toBe(current);
  });

  it('puts Spotify on a key of its own speaker, the rest on the speaker you control', () => {
    expect(cardKeyBox({ source: 'spotify', box: other })).toBe(other);
    expect(cardKeyBox({ source: 'radio', box: other })).toBe(current);
    expect(cardKeyBox({ source: 'upnp', box: other })).toBe(current);
    expect(cardKeyBox({ source: 'pandora', box: other })).toBe(current);
  });
});

// A stand-in for the painted list: cards with their controls in card order,
// enough of the DOM for the focus bookkeeping (the suite runs without one).
function fakeList(cards) {
  const els = cards.map(([ck, controls]) => {
    const card = { dataset: { ck }, controls: [] };
    card.controls = controls.map((cls) => ({ card, cls, classList: { contains: (x) => x === cls } }));
    card.querySelector = (sel) => {
      const names = sel.split(',').map((s) => s.trim().replace(/^\./, ''));
      return card.controls.find((c) => names.includes(c.cls)) || null;
    };
    return card;
  });
  for (const card of els) for (const c of card.controls) c.closest = () => card;
  return {
    cards: els,
    contains: (n) => els.some((c) => c === n || c.controls.includes(n)),
    querySelectorAll: () => els,
  };
}

describe('the keyboard across a repaint of the list', () => {
  const before = fakeList([['a', ['rc-play', 'rc-pick', 'rc-del']], ['b', ['rc-play', 'rc-pick', 'rc-fav', 'rc-del']], ['c', ['rc-pick', 'rc-del']]]);

  it('notes the card by identity, the control and the place', () => {
    expect(recentFocusMark(before, before.cards[1].controls[3])).toEqual({ ck: 'b', cls: 'rc-del', pos: 1 });
    expect(recentFocusMark(before, { classList: { contains: () => false } })).toBeNull();
  });

  it('goes back to the same control of the same card when every index moved', () => {
    // A new card was put on top by the 30 s refresh: card b is third now.
    const after = fakeList([['n', ['rc-play', 'rc-del']], ['a', ['rc-play', 'rc-del']], ['b', ['rc-play', 'rc-pick', 'rc-fav', 'rc-del']]]);
    const hit = recentFocusTarget(after, { ck: 'b', cls: 'rc-fav', pos: 1 });
    expect(hit.card.dataset.ck).toBe('b');
    expect(hit.cls).toBe('rc-fav');
  });

  it('lands on the card that took the place of a removed one, or the last card', () => {
    const after = fakeList([['a', ['rc-play', 'rc-del']], ['c', ['rc-pick', 'rc-del']]]);
    const hit = recentFocusTarget(after, { ck: 'b', cls: 'rc-del', pos: 1 });
    expect(hit.card.dataset.ck).toBe('c');
    expect(hit.cls).toBe('rc-pick');
    expect(recentFocusTarget(after, { ck: 'z', cls: 'rc-del', pos: 9 }).card.dataset.ck).toBe('c');
  });

  it('falls back to the Clear button when the list went empty', () => {
    const clear = { id: 'recentClear' };
    expect(recentFocusTarget(fakeList([]), { ck: 'b', cls: 'rc-del', pos: 0 }, clear)).toBe(clear);
    expect(recentFocusTarget(fakeList([]), null, clear)).toBeNull();
  });
});

describe('a card action in the preview', () => {
  it('finds its card by identity, not by the index it was painted at', () => {
    expect(src).not.toMatch(/getElementById\('recErr' \+ i\)/);
    expect(src).toContain('data-ck=');
  });

  it('keeps the focused button focusable while it works', () => {
    // disabled on the focused button sends the keyboard to the page.
    expect(src).not.toContain('playBtn.disabled = true');
    expect(src).toContain('playsInFlight');
  });
});
