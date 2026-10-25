// UI 2.0 preview check against simulated speakers (#1284, #1285).
//
// Renders the built frontend in headless Chromium with the backend mocked
// (demo.mjs: eleven speakers, a group of two playing, a stereo pair, an
// offline speaker, a Bose original), switches the preview on and checks what
// the sidebar promises: every speaker listed once, the pair as one row,
// Offline in words, "+" and "-" where the group edit acts, the keyboard, the
// pinned group staying put while the list scrolls, the player's Levels, the
// version line in the frame, and that "+" sends the edit to the right group.
//
// Runs in CI (build.yml, job "UI 2.0 preview"). Locally:
//   cd desktop-app/frontend && npm ci
//   cd screenshots && npm ci && npx playwright install chromium
//   node ui2-check.mjs            (UI2_CHECK_PORT moves the preview server)

import { chromium } from 'playwright';
import { DEMO, buildFrontend, installMocks, routeAgent, startServer, stopServer, waitForServer } from './demo.mjs';

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

  // The others scroll on their own; the pinned group and the entries stay.
  const before = await page.evaluate(() => ({
    pinned: document.querySelector('.ui2-spk-pinned').getBoundingClientRect().top,
    radio: document.querySelector('.ui2-nav-item[data-view="box"]').getBoundingClientRect().top,
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
    radio: document.querySelector('.ui2-nav-item[data-view="box"]').getBoundingClientRect().top,
  }));
  check(scrolled, 'with eleven speakers the list scrolls on its own');
  check(before.pinned === after.pinned && before.radio === after.radio, 'the pinned group and the navigation stay put while it scrolls');
  const navFits = await page.evaluate(() => {
    const s = document.querySelector('.ui2-side-scroll');
    const side = document.querySelector('#ui2Sidebar').getBoundingClientRect();
    const last = [...document.querySelectorAll('.ui2-side-scroll .ui2-nav-item')].pop().getBoundingClientRect();
    return s.scrollHeight <= s.clientHeight + 1 && last.bottom <= side.bottom;
  });
  check(navFits, 'every navigation entry is on screen in the default window');

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

  // The version line sits in the frame.
  check(!!(await page.$('#ui2Version #appUpdateBanner.app-update-quiet')), 'the version line is at the bottom of the sidebar');

  // "+" edits the group of the speaker you control, even when the row is
  // chosen right after (the batch keeps the group it was clicked in).
  await page.click(`#ui2SpkLists .ui2-spk-act[data-host="${host('Office')}"]`);
  const pending = await page.$eval(`#ui2SpkLists .ui2-spk-act[data-host="${host('Office')}"]`, (b) => b.getAttribute('aria-busy') === 'true' && /Adding Office/.test(b.getAttribute('aria-label')));
  check(pending, 'the "+" says the edit is running');
  await page.click(`#ui2SpkLists .ui2-spk-pick[data-host="${host('Office')}"]`);
  await page.waitForFunction(() => (window.__ui2Calls || []).some((c) => c[0] === 'FormZone'), null, { timeout: 15000 });
  const form = await page.evaluate(() => window.__ui2Calls.find((c) => c[0] === 'FormZone'));
  const slaves = ((form[3] || {}).slaves || []).map((s) => s.ip).sort();
  check(form[1] === host('Living Room'), 'the edit goes to the master of the group it was clicked in');
  check(slaves.join() === [host('Dining room'), host('Office')].sort().join(), 'the edit keeps the member and adds Office, never the master itself');
}

await buildFrontend();
const server = startServer(PORT);
let browser;
try {
  await waitForServer(BASE);
  browser = await chromium.launch({ headless: true });
  const ctx = await browser.newContext({ viewport: { width: 1240, height: 740 }, timezoneId: 'Europe/Berlin' });
  const page = await ctx.newPage();
  page.on('pageerror', (e) => { console.warn('pageerror:', e.message); failures.push('page error: ' + e.message); });
  await page.addInitScript(installMocks, { locale: 'en', view: VIEW, demo: DEMO });
  await page.route('**/api/**', routeAgent);
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
