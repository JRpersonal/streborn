// Recently played in the UI 2.0 preview (#1286): its strings in every
// language, its styles scoped to the preview, and the speaker each button
// names being the speaker it acts on.
import { describe, it, expect, beforeEach } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { state } from '../state.js';
import { cardPlayBox, cardKeyBox } from './recent.js';

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
