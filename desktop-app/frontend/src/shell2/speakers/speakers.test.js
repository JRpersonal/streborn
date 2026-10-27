// The UI 2.0 Speakers area skeleton (#1287, phase 3): the routes, the list's
// rows, one speaker's frame with its five tabs, and the rules every part
// keeps (import without a DOM, CSS only under html.ui2, only known tokens).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  ROUTE_VIEW, SPEAKER_ROUTES, SPEAKER_TABS, SPEAKER_TAB_LABEL_KEYS, SPEAKERS_AREA_VIEWS, SUBNAV,
  inSpeakersArea, normalizeTab, routeOfView, tabIndexFor,
} from './routes.js';
import { speakerFrameHTML, speakerHead, tabsLabel } from './speaker.js';
import { mountGroups } from './groups.js';
import * as router from './index.js';
import { setLocale } from '../../i18n/index.js';
import { shellMarkup } from '../markup.js';
import en from '../../i18n/bundles/en.json';

setLocale('en');

const css = Object.fromEntries(['speakers', 'list', 'speaker', 'groups']
  .map((n) => [n, readFileSync(new URL(`./${n}.css`, import.meta.url), 'utf8')]));
const shellCss = readFileSync(new URL('../shell2.css', import.meta.url), 'utf8');

describe('routes', () => {
  it('maps every route to the view the classic tab opens, the list to its own', () => {
    expect(SPEAKER_ROUTES).toEqual(['list', 'speaker', 'groups', 'add']);
    expect(ROUTE_VIEW).toEqual({ list: 'speakers', speaker: 'settings', groups: 'multiroom', add: 'setup' });
    expect(SPEAKERS_AREA_VIEWS).toEqual(['speakers', 'settings', 'multiroom', 'setup']);
    for (const r of SPEAKER_ROUTES) expect(routeOfView(ROUTE_VIEW[r])).toBe(r);
  });

  it('leaves the music views and Wishes outside the area', () => {
    for (const v of ['box', 'library', 'recent', 'spotify', 'wishes', '', undefined]) {
      expect(routeOfView(v)).toBe('');
      expect(inSpeakersArea(v)).toBe(false);
    }
  });

  it('has five tabs in the mockup order, each with a label', () => {
    expect(SPEAKER_TABS).toEqual(['overview', 'sound', 'network', 'keys', 'maintenance']);
    for (const k of SPEAKER_TABS) expect(en[SPEAKER_TAB_LABEL_KEYS[k]], k).toBeTruthy();
    expect(normalizeTab('sound')).toBe('sound');
    expect(normalizeTab('nope')).toBe('overview');
    expect(normalizeTab()).toBe('overview');
  });

  it('moves through the tabs with the arrows, wrapping, and Home and End', () => {
    expect(tabIndexFor('ArrowRight', 0, 5)).toBe(1);
    expect(tabIndexFor('ArrowRight', 4, 5)).toBe(0);
    expect(tabIndexFor('ArrowLeft', 0, 5)).toBe(4);
    expect(tabIndexFor('Home', 3, 5)).toBe(0);
    expect(tabIndexFor('End', 1, 5)).toBe(4);
    expect(tabIndexFor('ArrowDown', 1, 5)).toBe(-1);
    expect(tabIndexFor('ArrowRight', 0, 0)).toBe(-1);
    // Right to left: the arrows follow the reading direction.
    expect(tabIndexFor('ArrowLeft', 0, 5, true)).toBe(1);
    expect(tabIndexFor('ArrowRight', 0, 5, true)).toBe(4);
  });

  it('offers All speakers, Groups and Add speaker, the groups badge on Groups', () => {
    expect(SUBNAV.map((s) => s.route)).toEqual(['list', 'groups', 'add']);
    expect(SUBNAV.find((s) => s.route === 'groups').badgeId).toBe('ui2GroupsBadge');
    for (const s of SUBNAV) expect(en[s.labelKey], s.labelKey).toBeTruthy();
  });
});

describe('the shell markup', () => {
  const html = shellMarkup();

  it('holds the area head and the list view in the main pane, both hidden', () => {
    expect(html).toMatch(/id="ui2MainInner"><div class="spa" id="ui2SpkArea" hidden>/);
    expect(html).toContain('<div id="view-speakers" class="view hidden"></div>');
    expect(html).toContain('<div class="sps" id="ui2SpkPage" hidden></div>');
  });

  it('marks the Speakers entry with every view it stands for', () => {
    expect(html).toContain('data-view="speakers" data-views="speakers settings multiroom setup"');
    expect(html).not.toMatch(/ui2-nav-item[^>]*data-view="(settings|multiroom|setup)"/);
  });

  it('names the row of places by the area title', () => {
    expect(html).toContain('<nav class="spa-subnav" id="ui2SpkSubnav" aria-labelledby="ui2SpkAreaTitle">');
    expect(html).toContain('<h2 class="spa-title" id="ui2SpkAreaTitle">Speakers</h2>');
    expect((html.match(/id="ui2GroupsBadge"/g) || []).length).toBe(1);
  });
});

// The list's cards, pills and actions: list.test.js.

describe('one speaker skeleton', () => {
  it('names the speaker, its model and address', () => {
    const head = speakerHead({ host: '192.0.2.1', friendlyName: 'Living Room', model: 'SoundTouch 20' });
    expect(head).toEqual({ name: 'Living Room', meta: 'SoundTouch 20 · 192.0.2.1' });
    expect(tabsLabel(head)).toBe('Settings for Living Room');
    expect(speakerHead(null)).toBeNull();
  });

  it('builds five tabs, one selected and the only tab stop, each panel named by its tab', () => {
    const html = speakerFrameHTML('sound');
    expect((html.match(/role="tab"/g) || []).length).toBe(5);
    expect((html.match(/role="tabpanel"/g) || []).length).toBe(5);
    expect((html.match(/aria-selected="true"/g) || []).length).toBe(1);
    expect(html).toContain('id="ui2SpkTab-sound" data-tab="sound" aria-controls="ui2SpkPanel-sound" aria-selected="true" tabindex="0"');
    expect(html).toContain('id="ui2SpkTab-overview" data-tab="overview" aria-controls="ui2SpkPanel-overview" aria-selected="false" tabindex="-1"');
    for (const k of SPEAKER_TABS) {
      expect(html).toContain(`id="ui2SpkPanel-${k}" data-tab="${k}" aria-labelledby="ui2SpkTab-${k}" tabindex="0"`);
    }
    expect((html.match(/role="tabpanel"[^>]*hidden/g) || []).length).toBe(4);
    for (const w of ['Overview', 'Sound', 'Network', 'Keys and alarms', 'Maintenance']) expect(html).toContain(`>${w}</button>`);
  });
});

describe('every part', () => {
  it('imports without a DOM and does nothing without the shell', () => {
    // The import above already ran in node (no document): the trap that
    // blanked the app is a module-scope DOM access, and it would throw here.
    expect(router.openSpeakers('list')).toBe(false);
    expect(router.speakersRoute()).toEqual({ route: '', host: '', tab: '' });
    expect(() => router.syncSpeakersArea('speakers')).not.toThrow();
    expect(() => router.refreshSpeakersArea()).not.toThrow();
    expect(() => mountGroups({})).not.toThrow();
  });

  for (const [name, text] of Object.entries(css)) {
    it(`${name}.css sits under html.ui2 and uses only shell2.css tokens`, () => {
      const selectors = text.replace(/\/\*[\s\S]*?\*\//g, '').match(/[^{}]+(?=\{)/g) || [];
      expect(selectors.length).toBeGreaterThan(0);
      for (const sel of selectors) {
        for (const part of sel.split(',')) expect(part.trim(), `${name}.css`).toMatch(/^html\.ui2\b/);
      }
      for (const m of text.matchAll(/var\((--u-[a-z0-9-]+)\)/g)) {
        expect(shellCss, `${name}.css ${m[1]}`).toContain(`${m[1]}:`);
      }
    });
  }

  it('keeps 40px targets on the row of places and the tabs', () => {
    expect(css.speakers).toMatch(/\.spa-sub \{[^}]*min-height: var\(--u-target\)/);
    expect(css.speaker).toMatch(/\.sps-tab \{[^}]*min-height: var\(--u-target\)/);
  });
});
