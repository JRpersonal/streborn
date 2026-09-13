// Discussion #619: three stations were missing from radio-browser.info, the
// only directory the app searches, and the reporter never saw that he could add
// them there himself. The pointer existed, as a whole paragraph at 0.7 opacity
// below the load-more button of a 360px scroll box. These tests pin the two
// things that make it findable: a short line above the result list, and the
// full guide unfolded by itself when a search comes back empty.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { addStationGuideFold } from './searchflow.js';

const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');
const css = readFileSync(new URL('./style.css', import.meta.url), 'utf8');
const dir = fileURLToPath(new URL('./i18n/bundles/', import.meta.url));
const locales = readdirSync(dir).filter((f) => f.endsWith('.json')).map((f) => f.replace('.json', ''));
const bundle = (code) => JSON.parse(readFileSync(new URL(`./i18n/bundles/${code}.json`, import.meta.url), 'utf8'));

describe('the add-a-station line is reachable while results are on screen', () => {
  it('renders above the result list, not below it', () => {
    const hint = main.indexOf('class="search-addhint"');
    const results = main.indexOf('id="searchResults"');
    const loadMore = main.indexOf('id="loadMoreRow"');
    expect(hint).toBeGreaterThan(-1);
    expect(hint, 'the hint must come before the results box, or a full list hides it').toBeLessThan(results);
    expect(hint).toBeLessThan(loadMore);
  });

  it('shows one line and keeps the long guide behind it', () => {
    const block = main.slice(main.indexOf('class="search-addhint"'), main.indexOf('id="searchResults"'));
    expect(block).toContain("t('search.addStationCta')");
    expect(block).toContain("t('search.addStationHint')");
    // A <details> is what folds the guide away; without it the paragraph is
    // back on screen permanently.
    expect(block).toContain('</details>');
  });

  it('offers the directory itself as the action', () => {
    expect(main).toContain("BrowserOpenURL('https://www.radio-browser.info/')");
  });

  it('no longer dims the line into the background', () => {
    // The old rule was ".search-addhint { ... opacity: 0.7 ... }".
    const rule = css.slice(css.indexOf('.search-addhint {'), css.indexOf('.search-addhint-actions'));
    expect(rule).not.toContain('opacity');
  });

  it('prints the guide once, so the empty result does not repeat it', () => {
    expect(main).not.toContain('emptyAddStationHint');
  });
});

describe('addStationGuideFold', () => {
  it('unfolds the guide when a search finds nothing in the directory', () => {
    expect(addStationGuideFold(false, 'search', 0)).toEqual({ open: true, auto: true });
  });

  it('folds it again on the next search that finds something', () => {
    const after = addStationGuideFold(true, 'search', 12);
    expect(after).toEqual({ open: false, auto: false });
  });

  it('leaves a guide the user opened alone', () => {
    // auto is false: the user clicked the line, so nothing may close it.
    expect(addStationGuideFold(false, 'search', 12).open).toBe(null);
    expect(addStationGuideFold(false, 'top', 30).open).toBe(null);
  });

  it('stays folded when the station is there and only the filters hid it', () => {
    // The Bose filter dropped all 12 stations the directory returned. Telling
    // the user to add the station would be wrong: it exists.
    expect(addStationGuideFold(false, 'search', 12).open).toBe(null);
  });

  it('stays folded on an empty favorites view and an empty top list', () => {
    expect(addStationGuideFold(false, 'favorites', 0).open).toBe(null);
    expect(addStationGuideFold(false, 'top', 0).open).toBe(null);
  });
});

describe('the one-line text', () => {
  it('exists in every language the app ships', () => {
    expect(locales.length).toBeGreaterThanOrEqual(13);
    for (const code of locales) {
      const b = bundle(code);
      expect(b['search.addStationCta'], `${code} has no add-station line`).toBeTruthy();
    }
  });

  it('is one line everywhere, and far shorter than the full guide', () => {
    for (const code of locales) {
      const b = bundle(code);
      const cta = b['search.addStationCta'];
      expect(cta, `${code} wraps to more than one line`).not.toContain('\n');
      expect(cta.length, `${code} is a paragraph again`).toBeLessThan(b['search.addStationHint'].length / 2);
    }
  });

  it('is translated, not left in English', () => {
    const en = bundle('en')['search.addStationCta'];
    for (const code of locales.filter((c) => c !== 'en')) {
      expect(bundle(code)['search.addStationCta'], `${code} still shows the English line`).not.toBe(en);
    }
  });

  it('spells German with real umlauts', () => {
    expect(bundle('de')['search.addStationCta']).toMatch(/[äöüß]/);
  });
});
