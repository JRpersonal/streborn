// UI 2.0 preview check against simulated speakers (#1284, #1285).
//
// Renders the built frontend in headless Chromium with the backend mocked
// (../testkit via demo.mjs: eleven speakers, a group of two playing, a stereo pair, an
// offline speaker, a Bose original), switches the preview on and checks what
// the sidebar promises: every speaker listed once, the pair as one row,
// Offline in words, "+" and "-" where the group edit acts, the keyboard, every
// speaker shown whole in the default window and the other speakers as the
// sidebar's one scroll box in the smallest, the tabs above the page, the
// player's Levels, the version line in the frame, and that "+" sends the edit
// to the right group.
//
// Runs in CI (build.yml, job "UI 2.0 preview"). Locally:
//   cd desktop-app/frontend && npm ci
//   cd screenshots && npm ci && npx playwright install chromium
//   node ui2-check.mjs            (UI2_CHECK_PORT moves the preview server)

import { chromium } from 'playwright';
import { DEMO, assertPortFree, agentRoutes, buildFrontend, installDesktopMock, shotScenario, startServer, stopServer, waitForServer } from './demo.mjs';

const PORT = Number(process.env.UI2_CHECK_PORT) || 4179;
const BASE = `http://127.0.0.1:${PORT}/`;
const VIEW = 'app-ui2-dark';

const failures = [];
function check(ok, what) {
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${what}`);
  if (!ok) failures.push(what);
}

const STR_ROWS = DEMO.ui2Net.length - 1; // the pair is one row
const host = (name) => DEMO.ui2Net.find((b) => b.friendlyName === name).host;

async function run(page) {
  await page.waitForSelector('html.ui2 .ui2-shell', { timeout: 15000 });
  await page.waitForSelector('#ui2SpkLists .ui2-spk-pinned .ui2-spk-pick', { timeout: 15000 });
  // "+" needs the first every-speaker poll (play state and zones known).
  await page.waitForSelector('#ui2SpkLists .ui2-spk-act[data-act="add"]', { timeout: 25000 });

  const rows = await page.$$eval('#ui2SpkLists .ui2-spk-row', (els) => els.map((li) => {
    const pick = li.querySelector('.ui2-spk-pick');
    const act = li.querySelector('.ui2-spk-act');
    return {
      host: pick.dataset.host,
      label: pick.getAttribute('aria-label'),
      text: pick.textContent,
      current: pick.getAttribute('aria-current') === 'true',
      pinned: !!li.closest('.ui2-spk-pinned'),
      offline: li.classList.contains('is-offline'),
      pair: li.classList.contains('is-pair'),
      act: act ? act.dataset.act : '',
      actLabel: act ? act.getAttribute('aria-label') : '',
    };
  }));
  const row = (name) => rows.find((r) => r.host === host(name));

  check(rows.length === STR_ROWS, `one row per speaker, the pair as one (${rows.length} of ${STR_ROWS})`);
  check(new Set(rows.map((r) => r.host)).size === rows.length, 'no speaker listed twice');
  const pair = rows.filter((r) => r.pair);
  check(pair.length === 1 && /\(stereo\)/.test(pair[0].text) && !pair[0].act, 'the stereo pair is one "(stereo)" row without "+"');
  check(!rows.some((r) => r.host === host('Terrace right')), 'the second pair half has no row of its own');
  const off = row('Garage');
  check(off.offline && /Offline/.test(off.text) && /Offline/.test(off.label) && !off.act, 'the offline speaker is greyed, says Offline and offers no "+"');
  const stock = row('Kitchen');
  check(/Ready for STR/.test(stock.text) && /Opens the setup/.test(stock.label) && !stock.act, 'the Bose original says "Ready for STR" and opens the setup');
  check(rows.filter((r) => r.current).length === 1 && row('Living Room').current, 'exactly one row is the speaker you control');
  check(rows.filter((r) => r.pinned).map((r) => r.host).join() === [host('Living Room'), host('Dining room')].join(), 'the playing group is pinned, master first');
  check(row('Dining room').act === 'remove' && /Take Dining room out/.test(row('Dining room').actLabel), 'a member can be taken out ("-")');
  check(!row('Living Room').act, 'the master has no "-"');
  const adds = rows.filter((r) => r.act === 'add').map((r) => r.host).sort();
  const idle = ['Office', 'Bedroom', 'Hall', 'Bathroom', 'Studio'].map(host).sort();
  check(adds.join() === idle.join(), `"+" on exactly the idle speakers (${adds.length})`);
  check(/Play on Office too/.test(row('Office').actLabel), 'the "+" names its speaker');

  // Keyboard: one tab stop, arrows move, the right arrow reaches the action.
  const tabbable = await page.$$eval('#ui2SpkLists .ui2-spk-pick[tabindex="0"]', (els) => els.map((e) => e.dataset.host));
  check(tabbable.length === 1 && tabbable[0] === host('Living Room'), 'one row takes Tab, the current one');
  await page.focus(`#ui2SpkLists .ui2-spk-pick[data-host="${host('Living Room')}"]`);
  await page.keyboard.press('ArrowDown');
  let focused = await page.evaluate(() => document.activeElement && document.activeElement.dataset.host);
  check(focused === host('Dining room'), 'ArrowDown moves to the next row');
  await page.keyboard.press('ArrowRight');
  focused = await page.evaluate(() => document.activeElement && document.activeElement.classList.contains('ui2-spk-act'));
  check(focused, 'ArrowRight reaches the row\'s "-"');
  await page.keyboard.press('ArrowLeft');
  await page.keyboard.press('End');
  focused = await page.evaluate(() => document.activeElement && document.activeElement.dataset.host);
  check(focused === rows[rows.length - 1].host, 'End moves to the last row');
  const ring = await page.evaluate(() => getComputedStyle(document.activeElement).outlineStyle);
  check(ring !== 'none', 'the focused row shows a focus ring');
  // The ring (2px outline at a 2px offset) is drawn in full: every pick and
  // every "+"/"-" keeps 4px of room inside the list that scrolls it, the top
  // and bottom rows included.
  const clipped = await page.evaluate(() => {
    const out = [];
    for (const list of document.querySelectorAll('#ui2SpkLists .ui2-spk-scroll, #ui2SpkLists .ui2-spk-pinned .ui2-spk-list')) {
      const box = list.getBoundingClientRect();
      const inner = { left: box.left + list.clientLeft, top: box.top + list.clientTop };
      inner.right = inner.left + list.clientWidth;
      inner.bottom = inner.top + list.clientHeight;
      for (const el of list.querySelectorAll('.ui2-spk-pick, .ui2-spk-act')) {
        el.scrollIntoView({ block: 'nearest' });
        const r = el.getBoundingClientRect();
        if (r.left - 4 < inner.left - 0.5 || r.right + 4 > inner.right + 0.5
          || r.top - 4 < inner.top - 0.5 || r.bottom + 4 > inner.bottom + 0.5) {
          out.push(el.dataset.host + (el.classList.contains('ui2-spk-act') ? ' +/-' : ''));
        }
      }
      list.scrollTop = 0;
    }
    return out;
  });
  check(clipped.length === 0, `the focus ring has room on every side of every row (${clipped.join(', ') || 'none clipped'})`);

  // The sidebar is the speakers' (2026-11-02): in the default window every
  // speaker of the eleven-speaker network shows whole, and the navigation is
  // the tab row above the page, every tab on screen.
  const sideGeo = () => page.evaluate(() => {
    const r = (el) => el.getBoundingClientRect();
    const side = document.querySelector('#ui2Sidebar');
    // Every box in the sidebar that scrolls vertically right now.
    const scrolling = [...side.querySelectorAll('*')].filter((el) => {
      const oy = getComputedStyle(el).overflowY;
      return (oy === 'auto' || oy === 'scroll') && el.scrollHeight > el.clientHeight + 1;
    }).map((el) => el.className);
    const whole = [...side.querySelectorAll('#ui2SpkLists .ui2-spk-row')].filter((li) => {
      const b = r(li);
      const list = li.closest('.ui2-spk-scroll, .ui2-spk-pinned .ui2-spk-list');
      const lb = r(list);
      return b.top >= lb.top - 0.5 && b.bottom <= lb.bottom + 0.5;
    }).length;
    const tabs = [...document.querySelectorAll('#ui2Tabs .ui2-nav-item')].map((el) => {
      const b = r(el);
      return { view: el.dataset.view, inside: b.left >= 0 && b.right <= window.innerWidth + 0.5 && b.top >= 0 && b.bottom <= window.innerHeight + 0.5 };
    });
    const bottom = r(document.querySelector('.ui2-side-bottom'));
    return {
      scrolling,
      whole,
      tabs,
      noNavInSidebar: !side.querySelector('.ui2-nav-item, nav'),
      bottomIn: bottom.height > 0 && bottom.bottom <= r(side).bottom + 0.5,
    };
  });
  const big = await sideGeo();
  check(big.whole === STR_ROWS && big.scrolling.length === 0, `all ${STR_ROWS} speakers show whole in the 1240 x 740 window, no scroll box needed (${big.whole}, ${big.scrolling.join(', ') || 'none scrolling'})`);
  check(big.noNavInSidebar, 'the sidebar holds no navigation');
  check(big.tabs.length === 7 && big.tabs.every((x) => x.inside), `every tab is on screen above the page (${big.tabs.filter((x) => !x.inside).map((x) => x.view).join(', ') || 'all'})`);
  check(big.bottomIn, 'Aa and the language stay at the foot of the sidebar');

  // The smallest window: the other speakers are the sidebar's one scroll box;
  // the pinned group and the tabs stay put while it scrolls.
  await page.setViewportSize({ width: 1000, height: 600 });
  await page.waitForTimeout(400);
  const small = await sideGeo();
  check(small.scrolling.length === 1 && /\bui2-spk-scroll\b/.test(small.scrolling[0]), `exactly one vertical scroll box in the sidebar at 1000 x 600, the other speakers (${small.scrolling.join(', ') || 'none'})`);
  check(small.tabs.length === 7 && small.tabs.every((x) => x.inside), `every tab is on screen at 1000 x 600 (${small.tabs.filter((x) => !x.inside).map((x) => x.view).join(', ') || 'all'})`);
  check(small.bottomIn, 'Aa and the language stay at the foot of the sidebar at 1000 x 600');
  const before = await page.evaluate(() => ({
    pinned: document.querySelector('.ui2-spk-pinned').getBoundingClientRect().top,
    radio: document.querySelector('#ui2Tabs .ui2-nav-item[data-view="box"]').getBoundingClientRect().top,
  }));
  const scrolled = await page.evaluate(() => {
    const s = document.querySelector('.ui2-spk-scroll');
    s.scrollTop = 0;
    const can = s.scrollHeight > s.clientHeight;
    s.scrollTop = s.scrollHeight;
    return can && s.scrollTop > 0;
  });
  const after = await page.evaluate(() => ({
    pinned: document.querySelector('.ui2-spk-pinned').getBoundingClientRect().top,
    radio: document.querySelector('#ui2Tabs .ui2-nav-item[data-view="box"]').getBoundingClientRect().top,
  }));
  check(scrolled, 'in the smallest window the eleven speakers scroll on their own');
  check(before.pinned === after.pinned && before.radio === after.radio, 'the pinned group and the tabs stay put while it scrolls');
  check(small.whole >= 5, `the smallest window still shows at least five whole rows (${small.whole})`);
  await page.evaluate(() => { document.querySelector('.ui2-spk-scroll').scrollTop = 0; });
  await page.setViewportSize({ width: 1240, height: 740 });
  await page.waitForTimeout(400);

  // The player: group volume and Levels, no Output popover.
  check(!(await page.$('#ui2OutputBtn')) && !(await page.$('#ui2RoomBtn')), 'no room switcher and no Output popover');
  check(await page.$eval('#ui2Player', (p) => p.classList.contains('is-grouped')), 'the player is in its grouped state');
  check(!!(await page.$('#ui2GroupVolSlot #groupVolume')), 'the group volume sits in the player');
  await page.click('#ui2LevelsBtn');
  await page.waitForSelector('#ui2LevelsPop:not([hidden])', { timeout: 5000 });
  const levels = await page.$$eval('#ui2LevelsPop .group-member-vol input[type=range]', (els) => els.length);
  const chipsShown = await page.$$eval('#ui2LevelsPop .group-chip', (els) => els.some((e) => e.offsetParent !== null));
  check(levels === 2 && !chipsShown, 'Levels holds one slider per member and no speaker chooser');
  await page.keyboard.press('Escape');
  // From the keyboard, Levels takes focus to its first slider, not to a hidden chip.
  await page.focus('#ui2LevelsBtn');
  await page.keyboard.press('Enter');
  await page.waitForSelector('#ui2LevelsPop:not([hidden])', { timeout: 5000 });
  const levelsFocus = await page.evaluate(() => (document.activeElement && document.activeElement.id) || '');
  check(/^memberVol-/.test(levelsFocus), `Enter on Levels puts focus on the first slider (${levelsFocus || 'none'})`);
  await page.keyboard.press('Escape');

  // Choosing an idle speaker keeps the playing group pinned, and its "+"
  // still adds to that music (#1284).
  await page.click(`#ui2SpkLists .ui2-spk-pick[data-host="${host('Hall')}"]`);
  await page.waitForSelector(`#ui2SpkLists .ui2-spk-pick[data-host="${host('Hall')}"][aria-current="true"]`, { timeout: 5000 });
  const idleSel = await page.$$eval('#ui2SpkLists .ui2-spk-row', (els) => ({
    pinned: els.filter((li) => li.closest('.ui2-spk-pinned')).map((li) => li.querySelector('.ui2-spk-pick').dataset.host),
    masters: els.map((li) => li.querySelector('.ui2-spk-act[data-act="add"]')).filter(Boolean).map((a) => a.dataset.master),
  }));
  check(idleSel.pinned.join() === [host('Living Room'), host('Dining room')].join(), 'with an idle speaker chosen, the playing group stays pinned');
  check(idleSel.masters.length > 0 && idleSel.masters.every((m) => m === host('Living Room')), 'every "+" adds to the playing group');

  // The version line waits in the frame, out of sight: nothing is pending,
  // and the version and the update check live on Help and info.
  check(!!(await page.$('#ui2Version #appUpdateBanner.app-update-quiet')), 'the version line waits in the update slot of the sidebar');
  check(await page.$eval('#ui2Version', (el) => el.hidden && el.offsetParent === null), 'with no update waiting the sidebar shows no version line');

  // "+" edits the playing group, even when the row is chosen right after
  // (the batch keeps the group it was clicked in).
  await page.click(`#ui2SpkLists .ui2-spk-act[data-host="${host('Office')}"]`);
  const pending = await page.$eval(`#ui2SpkLists .ui2-spk-act[data-host="${host('Office')}"]`, (b) => b.getAttribute('aria-busy') === 'true' && /Adding Office/.test(b.getAttribute('aria-label')));
  check(pending, 'the "+" says the edit is running');
  await page.click(`#ui2SpkLists .ui2-spk-pick[data-host="${host('Office')}"]`);
  await page.waitForFunction(() => window.__strMock.calls.some((c) => c.name === 'FormZone'), null, { timeout: 15000 });
  const form = await page.evaluate(() => { const c = window.__strMock.calls.find((x) => x.name === 'FormZone'); return [c.name, ...c.args]; });
  const slaves = ((form[3] || {}).slaves || []).map((s) => s.ip).sort();
  check(form[1] === host('Living Room'), 'the edit goes to the master of the group it was clicked in');
  check(slaves.join() === [host('Dining room'), host('Office')].sort().join(), 'the edit keeps the member and adds Office, never the master itself');

  // Help and info (phase 5): the small tab after Wishes opens the one place
  // for help, wishes, community, support and about. The classic footer is
  // hidden on every preview view; the donate buttons sit at the foot of the
  // sidebar on every view, and Help and info has its own three.
  const helpEntry = await page.$eval('#ui2Tabs .ui2-tabs-small', (el) => [...el.querySelectorAll('.ui2-nav-item')].map((b) => b.dataset.view));
  check(helpEntry.join() === 'wishes,help', `Wishes and Help and info are the small tabs, Help last (${helpEntry.join()})`);
  const donateBefore = await page.$$eval('[data-donate]', (els) => els.filter((e) => e.offsetParent !== null).map((e) => !!e.closest('#ui2Support')));
  check(donateBefore.length === 3 && donateBefore.every(Boolean), `the three donate buttons are on screen at the foot of the sidebar, and only there (${donateBefore.length})`);
  await page.click('#ui2NavHelp');
  await page.waitForSelector('#view-help:not(.hidden) #hlpTitle', { timeout: 5000 });
  const helpPage = await page.evaluate(() => {
    const v = document.getElementById('view-help');
    const footer = document.getElementById('appFooter');
    const tm = document.getElementById('hlpTrademark');
    const donate = [...document.querySelectorAll('[data-donate]')].filter((e) => e.offsetParent !== null);
    return {
      sections: [...v.querySelectorAll('section[data-section]')].map((s) => s.dataset.section),
      labelled: [...v.querySelectorAll('section[data-section]')].every((s) => {
        const h = document.getElementById(s.getAttribute('aria-labelledby'));
        return !!h && s.contains(h) && h.textContent.trim().length > 0;
      }),
      current: document.getElementById('ui2NavHelp').getAttribute('aria-current'),
      footerHidden: !footer || footer.offsetParent === null,
      donateInHelp: donate.filter((e) => v.contains(e)).length === 3 && donate.filter((e) => !v.contains(e)).every((e) => !!e.closest('#ui2Support')),
      trademark: !!tm && tm.offsetParent !== null && /Bose Corporation/.test(tm.textContent),
    };
  });
  check(helpPage.sections.join() === 'getHelp,wishes,community,support,about', `Help and info lists its sections in order (${helpPage.sections.join()})`);
  check(helpPage.labelled, 'every Help and info section is labelled by its own heading');
  check(helpPage.current === 'page', 'the Help entry is marked current');
  check(helpPage.footerHidden, 'the classic footer is hidden on Help and info');
  check(helpPage.donateInHelp, 'Help and info shows its own three donate buttons, besides the sidebar foot');
  check(helpPage.trademark, 'the trademark notice is on screen');
}

await assertPortFree(BASE);
await buildFrontend(PORT);
const server = startServer(PORT);
let browser;
try {
  await waitForServer(BASE, server);
  browser = await chromium.launch({ headless: true });
  const ctx = await browser.newContext({ viewport: { width: 1240, height: 740 }, timezoneId: 'Europe/Berlin' });
  const page = await ctx.newPage();
  page.on('pageerror', (e) => { console.warn('pageerror:', e.message); failures.push('page error: ' + e.message); });
  const scenario = shotScenario(VIEW, 'en');
  await page.addInitScript(installDesktopMock, { scenario, demo: DEMO });
  await page.route('**/api/**', agentRoutes(scenario));
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await run(page);
} catch (e) {
  failures.push(String(e && e.message ? e.message : e));
  console.error('FAIL', e);
} finally {
  if (browser) await browser.close();
  stopServer(server);
}
console.log(failures.length ? `\n${failures.length} check(s) failed` : '\nAll UI 2.0 checks passed');
process.exitCode = failures.length ? 1 : 0;
