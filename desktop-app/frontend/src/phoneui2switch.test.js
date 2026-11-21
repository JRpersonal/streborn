import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// The phone remote's UI 2.0 preview switch (internal/webui/assets/index.html,
// Aa > Layout). The display-preferences script in the page's <head> is lifted
// off the served page, the way phonepairfollower.test.js lifts its functions,
// and run against a fake window, so these check the code the speaker serves.
//
// UI 2.0 is the default since phase 6 (#1290): classic only when this phone
// stored exactly '0' under str.ui2Preview, a page without storage gets the
// default and never offers the choice, and a speaker opened from another
// speaker's list (#ui2 or #classic) keeps the layout it was opened from.

const here = dirname(fileURLToPath(import.meta.url));
const page = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8')
  .replace(/\r\n/g, '\n');

function liftHead() {
  const start = page.indexOf("(function(){\n  var SCALE_KEY='a11yScale'");
  expect(start, 'the display-preferences script is missing from the phone page').toBeGreaterThan(-1);
  const end = page.indexOf('\n})();', start);
  return page.slice(start, end + 6);
}
const head = liftHead();

function fakeStorage(init = {}, { throwGet = false, throwSet = false, missing = false } = {}) {
  const data = { ...init };
  if (missing) return null;
  return {
    data,
    getItem(k) { if (throwGet) throw new Error('denied'); return Object.prototype.hasOwnProperty.call(data, k) ? data[k] : null; },
    setItem(k, v) { if (throwSet) throw new Error('quota'); data[k] = String(v); },
    removeItem(k) { delete data[k]; },
  };
}

// run executes the head script the way the browser does before first paint.
function run({ store = fakeStorage(), hash = '', storageThrows = false } = {}) {
  const classes = new Set();
  const replaced = [];
  const meta = { attrs: {}, setAttribute(k, v) { this.attrs[k] = v; } };
  const document = {
    documentElement: {
      classList: {
        add: (...c) => c.forEach((x) => classes.add(x)),
        remove: (...c) => c.forEach((x) => classes.delete(x)),
        contains: (c) => classes.has(c),
      },
    },
    querySelector: () => meta,
  };
  const location = { hash, pathname: '/', search: '' };
  const history = { replaceState: (s, t, u) => { replaced.push(u); location.hash = ''; } };
  const window = { matchMedia: () => ({ matches: false }) };
  if (storageThrows) {
    Object.defineProperty(window, 'localStorage', { get() { throw new Error('SecurityError'); } });
  } else {
    window.localStorage = store;
  }
  // The script reads the bare global localStorage; in a page that is the
  // window's, so the fake hands out the same thing (or throws the same way).
  const scope = {};
  Object.defineProperty(scope, 'localStorage', { get: () => window.localStorage });
  new Function('window', 'document', 'location', 'history', 'scope', `with (scope) { ${head} }`)(
    window, document, location, history, scope,
  );
  return { window, classes, store, location, replaced };
}

describe('phone remote UI 2.0 switch', () => {
  it('is on by default: no stored key means the new layout', () => {
    const r = run();
    expect(r.window.ui2Get()).toBe(true);
    expect(r.classes.has('ui2')).toBe(true);
  });

  it('is classic only for exactly "0", which somebody who picked it has stored', () => {
    const classic = run({ store: fakeStorage({ 'str.ui2Preview': '0' }) });
    expect(classic.window.ui2Get()).toBe(false);
    expect(classic.classes.has('ui2')).toBe(false);
    for (const v of ['1', 'true', 'off', '', ' 0', '2']) {
      const r = run({ store: fakeStorage({ 'str.ui2Preview': v }) });
      expect(r.window.ui2Get(), JSON.stringify(v)).toBe(true);
      expect(r.classes.has('ui2'), JSON.stringify(v)).toBe(true);
    }
  });

  it('is on, before first paint, when the phone stored "1"', () => {
    const r = run({ store: fakeStorage({ 'str.ui2Preview': '1' }) });
    expect(r.window.ui2Get()).toBe(true);
    expect(r.classes.has('ui2')).toBe(true);
  });

  it('gets the default when reading storage throws, and the page still applies its theme', () => {
    const r = run({ store: fakeStorage({ 'str.ui2Preview': '0' }, { throwGet: true }) });
    expect(r.window.ui2Get()).toBe(true);
    expect(r.classes.has('ui2')).toBe(true);
    expect(typeof r.window.a11yApply).toBe('function');
    const gone = run({ storageThrows: true });
    expect(gone.window.ui2Get()).toBe(true);
    expect(gone.classes.has('ui2')).toBe(true);
  });

  it('ui2Set reports whether the choice was really kept', () => {
    const ok = run();
    expect(ok.window.ui2Set(true)).toBe(true);
    expect(ok.store.data['str.ui2Preview']).toBe('1');
    expect(ok.window.ui2Set(false)).toBe(true);
    expect(ok.store.data['str.ui2Preview']).toBe('0');
    const full = run({ store: fakeStorage({}, { throwSet: true }) });
    expect(full.window.ui2Set(true)).toBe(false);
    expect(run({ storageThrows: true }).window.ui2Set(true)).toBe(false);
  });

  it('ui2CanKeep is false without usable storage, so the Layout group hides', () => {
    expect(run().window.ui2CanKeep()).toBe(true);
    expect(run({ store: fakeStorage({}, { throwGet: true }) }).window.ui2CanKeep()).toBe(false);
    expect(run({ storageThrows: true }).window.ui2CanKeep()).toBe(false);
    expect(run({ store: fakeStorage({}, { missing: true }) }).window.ui2CanKeep()).toBe(false);
    // The menu wiring hides the group on exactly that answer.
    expect(page).toContain("if (lyGroup && !(window.ui2CanKeep && window.ui2CanKeep())) lyGroup.hidden = true;");
    // Probing must not change what the phone stored.
    const r = run({ store: fakeStorage({ 'str.ui2Preview': '0' }) });
    r.window.ui2CanKeep();
    expect(r.store.data).toEqual({ 'str.ui2Preview': '0' });
  });

  it('ui2CanKeep is false when storage reads but refuses writes, so the Layout group hides', () => {
    // Full storage, or a private mode that throws on setItem only: a tap on
    // the preview could never be kept there.
    const ro = run({ store: fakeStorage({}, { throwSet: true }) });
    expect(ro.window.ui2Get()).toBe(true);
    expect(ro.window.ui2CanKeep()).toBe(false);
    // And a write refused after the menu was shown takes the group away.
    expect(page).toContain('if (window.ui2Set && window.ui2Set(on)) { location.reload(); return; }');
    expect(page).toContain('if (lyGroup) lyGroup.hidden = true;');
  });

  it('a page opened as #ui2 turns the preview on, keeps it and drops the hash', () => {
    const r = run({ hash: '#ui2' });
    expect(r.classes.has('ui2')).toBe(true);
    expect(r.store.data['str.ui2Preview']).toBe('1');
    expect(r.replaced).toEqual(['/']);
    expect(r.location.hash).toBe('');
  });

  it('a page opened as #classic keeps the classic layout and drops the hash', () => {
    const r = run({ hash: '#classic' });
    expect(r.classes.has('ui2')).toBe(false);
    expect(r.store.data['str.ui2Preview']).toBe('0');
    expect(r.replaced).toEqual(['/']);
    expect(r.location.hash).toBe('');
    // The classic peer list is where #classic comes from.
    expect(page).toContain("if (online) { if (p.url) a.href = String(p.url).split('#')[0] + '#classic'; }");
  });

  it('any other hash changes nothing', () => {
    const r = run({ hash: '#play' });
    expect(r.classes.has('ui2')).toBe(true);
    expect(r.store.data).toEqual({});
    expect(r.replaced).toEqual([]);
  });

  it('a #classic page on a phone without storage gets the default', () => {
    const r = run({ hash: '#classic', storageThrows: true });
    expect(r.classes.has('ui2')).toBe(true);
  });

  it('carries the Layout labels in every language of the Aa menu', () => {
    const start = page.indexOf('  var A11Y_I18N = {');
    const block = page.slice(start, page.indexOf('\n  };', start));
    const locales = [...block.matchAll(/^ {4}([a-z]{2}):\{/gm)].map((m) => m[1]);
    expect(locales.length).toBe(12);
    for (const k of ['ly', 'lc', 'ln']) {
      expect([...block.matchAll(new RegExp(`[,{]${k}:"[^"]+"`, 'g'))].length, k).toBe(locales.length);
    }
  });
});
