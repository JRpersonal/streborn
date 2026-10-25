// phone-smoke: the phone remote (internal/webui/assets/index.html, the page
// every STR speaker serves on :8888) boots on testkit/phone-mock.mjs, every
// tab opens, search, a preset tap and the group card reach the agent paths
// they should, both first-class languages render without raw keys or
// overflow, a failed preset read shows the offline note, and an axe scan of
// the Play tab stays inside baselines/a11y-phone.json.
//
// Owns baselines/a11y-phone.json and baselines/unmocked-phone.json.

import { test, expect } from '../fixtures.mjs';
import { expectDriftList } from '../helpers/mockclient.mjs';
import { KILL_ANIM, scan } from '../helpers/a11y.mjs';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { PHONE } from '../../testkit/demo-data.mjs';

const UNMOCKED_FILE = 'baselines/unmocked-phone.json';

const TABS = ['Play', 'Browse', 'Speakers', 'More'];

// The tab buttons of the bottom bar; each one shows its #panel<Name>.
async function openTab(page, name) {
  await page.locator('#tab' + name).click();
  await expect(page.locator('#panel' + name)).toBeVisible();
  await expect(page.locator('#panel' + name)).not.toHaveAttribute('hidden', /.*/);
  await expect(page.locator('#tab' + name)).toHaveAttribute('aria-selected', 'true');
}

// The page shows the Speakers tab only once it knows other speakers.
async function speakersTabReady(page) {
  await expect(page.locator('#tabSpeakers')).toBeVisible();
}

async function bootDone(page) {
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
}

// Text the page shows (text nodes and the labelling attributes) that is
// exactly one of the page's own en dictionary keys (I18N.en, I18N2.en), or
// that still carries a {{ }} placeholder. Hidden panels count too: their text
// is filled by the same applyStaticI18n pass.
function rawTextProblems() {
  const keys = new Set([
    ...Object.keys((window.I18N && window.I18N.en) || {}),
    ...Object.keys((window.I18N2 && window.I18N2.en) || {}),
  ]);
  const out = [];
  const check = (where, s) => {
    const v = String(s || '').trim();
    if (!v) return;
    if (keys.has(v)) out.push(`${where}: raw key "${v}"`);
    if (/\{\{|\}\}/.test(v)) out.push(`${where}: placeholder in "${v.slice(0, 80)}"`);
  };
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const p = n.parentElement;
    if (!p || p.closest('script,style,template,noscript')) continue;
    check(`<${p.tagName.toLowerCase()}${p.id ? '#' + p.id : ''}>`, n.nodeValue);
  }
  for (const el of document.body.querySelectorAll('[aria-label],[title],[placeholder],[alt]')) {
    for (const a of ['aria-label', 'title', 'placeholder', 'alt']) {
      if (el.hasAttribute(a)) check(`${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}[${a}]`, el.getAttribute(a));
    }
  }
  return out;
}

test.describe('boot and tabs', () => {
  test('boots with six presets with art and the speaker name', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await expect(page.locator('#devName')).toHaveText(PHONE.SETTINGS.info.name);
    await expect(page).toHaveTitle(PHONE.TITLE);
    expect(phone.errors).toEqual([]);
  });

  test('every tab shows its panel', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await speakersTabReady(page);
    for (const name of [...TABS.slice(1), 'Play']) {
      await openTab(page, name);
      for (const other of TABS.filter((t) => t !== name)) {
        await expect(page.locator('#panel' + other)).toBeHidden();
      }
    }
  });

  test('the agent paths the page asks for without a model stay as listed', async ({ phone }) => {
    const { page, handler } = phone;
    await bootDone(page);
    await speakersTabReady(page);
    for (const name of TABS) await openTab(page, name);
    // Search, so the library search the Browse tab makes is part of the walk.
    await openTab(page, 'Browse');
    await page.locator('#q').fill('rock');
    await page.locator('#btnSearch').click();
    await expect(page.locator('#results .station').first()).toBeVisible();
    // Some of those requests come from background loaders after the tab
    // opened. Wait until the listed ones were seen; a listed path that never
    // comes then fails below as stale, and a new one fails as added.
    let listed = [];
    try { listed = JSON.parse(readFileSync(fileURLToPath(new URL(`../${UNMOCKED_FILE}`, import.meta.url)), 'utf8')); } catch { listed = []; }
    if (listed.length && !process.env.E2E_UPDATE_UNMOCKED) {
      await expect.poll(() => listed.every((n) => handler.unmocked.has(n)), { timeout: 10_000 })
        .toBe(true)
        .catch(() => {});
    } else {
      // Refreshing (or seeding an empty list): take the set once it has not
      // grown for three seconds of polling.
      let last = -1, streak = 0;
      await expect.poll(() => {
        const n = handler.unmocked.size;
        streak = n === last && n > 0 ? streak + 1 : 0;
        last = n;
        return streak;
      }, { timeout: 20_000, intervals: [500] }).toBeGreaterThanOrEqual(6).catch(() => {});
    }
    expectDriftList([...handler.unmocked], UNMOCKED_FILE);
  });
});

test.describe('actions', () => {
  test('a search on Browse lists stations and asks radio-browser for the term', async ({ phone }) => {
    const { page, handler } = phone;
    await bootDone(page);
    await openTab(page, 'Browse');
    await page.locator('#q').fill('rock');
    await page.locator('#btnSearch').click();
    await expect(page.locator('#results .station')).toHaveCount(PHONE.STATIONS.length);
    await expect(page.locator('#results .station').first()).toContainText(PHONE.STATIONS[0].name);
    const rb = handler.calls.filter((c) => /radio-browser\.info\//.test(c.url));
    expect(rb.length).toBeGreaterThan(0);
    expect(rb.some((c) => /[?&]name=rock(&|$)/.test(c.url))).toBe(true);
  });

  test('a tap on preset 1 starts slot 1 on the speaker', async ({ phone }) => {
    const { page, handler } = phone;
    await bootDone(page);
    const before = handler.calls.length;
    await page.locator('#presets .preset').first().click();
    await expect.poll(() => handler.calls.slice(before)
      .filter((c) => c.method === 'POST' && new URL(c.url).pathname.startsWith('/api/play/'))
      .map((c) => new URL(c.url).pathname)).toEqual(['/api/play/1']);
    await expect(page.locator('#presets .preset').first()).toHaveClass(/\bactive\b/);
  });
});

test.describe('grouped', () => {
  test.use({ phoneGrouped: true });

  test('Speakers lists the peers and shows the group card with its members', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await speakersTabReady(page);
    await openTab(page, 'Speakers');
    await expect(page.locator('#peers .peer')).toHaveCount(PHONE.PEERS.length);
    await expect(page.locator('#groupCard')).toBeVisible();
    await expect(page.locator('#groupCard #members')).toBeVisible();
  });
});

for (const lang of ['en', 'de']) {
  test.describe(`language ${lang}`, () => {
    test.use({ phoneLang: lang });

    test('no raw keys, no placeholders, no sideways scroll on any tab', async ({ phone }) => {
      const { page } = phone;
      await bootDone(page);
      await expect(page.locator('html')).toHaveAttribute('lang', lang);
      await speakersTabReady(page);
      await page.addStyleTag({ content: KILL_ANIM });
      for (const name of TABS) {
        await openTab(page, name);
        expect(await page.evaluate(rawTextProblems), `tab ${name}`).toEqual([]);
        const w = await page.evaluate(() => document.documentElement.scrollWidth);
        expect(w, `tab ${name} scrolls sideways`).toBeLessThanOrEqual(390);
      }
    });
  });
}

test.describe('a failed preset read', () => {
  test.use({ phoneOverrides: { '/api/presets': { status: 500, body: '' } } });

  test('shows the offline note instead of six empty keys', async ({ phone }) => {
    const { page } = phone;
    const note = page.locator('#presets .grid-note');
    await expect(note).toBeVisible();
    const offline = await page.evaluate(() => window.T && window.T.offline);
    expect(offline).toBeTruthy();
    await expect(note).toHaveText(offline);
    await expect(page.locator('#presets .preset')).toHaveCount(0);
    expect(phone.errors).toEqual([]);
  });
});

test.describe('accessibility', () => {
  test('the Play tab stays inside the axe baseline', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    // Everything the scan judges has to be painted first, or the counts move:
    // the speaker name in #dev and both alarm rows.
    await expect(page.locator('#devName')).toHaveText(PHONE.SETTINGS.info.name);
    await expect(page.locator('#alarmList input[data-alarm-on]')).toHaveCount(PHONE.ALARMS.alarms.length);
    await page.addStyleTag({ content: KILL_ANIM });
    await scan(page, 'phone|play', 'baselines/a11y-phone.json');
  });
});
