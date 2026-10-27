// Screenshot harness for the phone remote: the mobile web page every STR
// speaker serves on http://<speaker>:8888/ (internal/webui/assets/index.html).
//
// The page is self-contained (inline CSS and JS), so this harness needs no
// server and no speaker: Playwright intercepts every request to a fake origin,
// answers "/" with the embedded index.html straight from the repo, and answers
// every /api/* call (and the radio-browser.info station search the page does
// from the phone) with deterministic demo data. Nothing leaves the machine and
// no real speaker is ever contacted.
//
// Output: docs/screenshots/phone/<lang>/<view>.png
//
// Run: npm run shoot-phone   (SHOOT_LANGS=en,de and SHOOT_VIEWS=phone-play to narrow)
//
// SHOOT_OUT=<dir> writes somewhere else (a review set that must not land in
// the repo), SHOOT_THEMES=dark,light adds a <theme> folder level and sets the
// page's theme, SHOOT_WIDTH=320 shoots a narrower phone. The phone-keys-*,
// phone-save-row2 and phone-alarm-row2 views show the second row of keys
// (#1110); they are not in the default set.

import { chromium } from 'playwright';
import { mkdir, readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { PHONE } from '../testkit/demo-data.mjs';
import { phoneHandler } from '../testkit/phone-mock.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(__dirname, '../../..');
const ASSETS = path.join(REPO, 'internal/webui/assets');
const OUT = process.env.SHOOT_OUT ? path.resolve(process.env.SHOOT_OUT) : path.join(REPO, 'docs/screenshots/phone');
const THEMES = (process.env.SHOOT_THEMES && process.env.SHOOT_THEMES.trim())
  ? process.env.SHOOT_THEMES.split(',').map((s) => s.trim()).filter(Boolean)
  : [''];
const WIDTH = Number(process.env.SHOOT_WIDTH) || 390;
const ROW2_FILLED = ['phone-keys-row1', 'phone-keys-row2-playing', 'phone-save-row2', 'phone-alarm-row2'];
const ORIGIN = PHONE.ORIGIN; // a placeholder origin, never resolved

const ALL_LANGS = ['en', 'de'];
const LANGS = (process.env.SHOOT_LANGS && process.env.SHOOT_LANGS.trim())
  ? process.env.SHOOT_LANGS.split(',').map((s) => s.trim()).filter(Boolean)
  : ALL_LANGS;
const ALL_VIEWS = ['phone-play', 'phone-alarm', 'phone-browse', 'phone-speakers'];
const VIEWS = (process.env.SHOOT_VIEWS && process.env.SHOOT_VIEWS.trim())
  ? process.env.SHOOT_VIEWS.split(',').map((s) => s.trim()).filter(Boolean)
  : ALL_VIEWS;

// ---- demo data and request handling ---------------------------------------
// Both live in ../testkit/ (demo-data.mjs PHONE, phone-mock.mjs), shared with
// the Playwright suite. Only the Speakers shot plays as a group.
let phone = null;

// ---- per-view driving ------------------------------------------------------
let shotPath = ''; // where the current view's shot goes
async function drive(page, view) {
  const tab = async (name) => {
    await page.click('#tab' + name);
    await page.waitForSelector('#panel' + name + ':not([hidden])', { timeout: 9000 });
  };
  const scrollTo = (sel, offset = 0) => page.evaluate(([s, o]) => {
    const e = document.querySelector(s);
    if (e) window.scrollTo(0, e.getBoundingClientRect().top + window.scrollY - o);
  }, [sel, offset]);

  await page.waitForSelector('#presets .preset.has-art', { timeout: 9000 });
  if (view === 'phone-play') {
    await page.evaluate(() => window.scrollTo(0, 0));
  } else if (view === 'phone-alarm') {
    await page.waitForSelector('#alarmList button', { timeout: 9000 });
    await scrollTo('#lblSleep', 70);
  } else if (view === 'phone-browse') {
    await tab('Browse');
    await page.fill('#q', 'rock');
    await page.click('#btnSearch');
    await page.waitForSelector('#results .station, #results button', { timeout: 9000 });
    await page.evaluate(() => window.scrollTo(0, 0));
  } else if (view === 'phone-keys-row1') {
    await page.waitForSelector('#presetRowSw button', { timeout: 9000 });
    await page.evaluate(() => window.scrollTo(0, 0));
  } else if (view === 'phone-keys-row2-empty') {
    await page.click('#presetRowSw button[data-row="2"]');
    await page.waitForSelector('#row2Hint:not([hidden])', { timeout: 9000 });
    await page.evaluate(() => window.scrollTo(0, 0));
  } else if (view === 'phone-keys-row2-playing') {
    // Row 1 with the playing dot on 2x first, as a twin shot.
    await page.waitForSelector('#presetRowSw button[data-row="2"] .rdot', { timeout: 9000 });
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.waitForTimeout(600);
    await page.screenshot({ path: shotPath.replace(/\.png$/, '-row1-dot.png') });
    await page.click('#presetRowSw button[data-row="2"]');
    await page.waitForSelector('#presets .preset.active[data-slot="9"]', { timeout: 9000 });
    await page.evaluate(() => window.scrollTo(0, 0));
  } else if (view === 'phone-save-row2') {
    await tab('Browse');
    await page.fill('#q', 'rock');
    await page.click('#btnSearch');
    await page.waitForSelector('#results .station', { timeout: 9000 });
    await page.click('#results .station button:nth-of-type(2)');
    await page.waitForSelector('#pickRowSw', { timeout: 9000 });
    await page.click('#pickRowSw button[data-row="2"]');
    await page.evaluate(() => window.scrollTo(0, 0));
  } else if (view === 'phone-alarm-row2') {
    await page.waitForSelector('#alarmList [data-alarm-edit="0"]', { timeout: 9000 });
    await page.click('#alarmList [data-alarm-edit="0"]');
    await page.click('#alarmRowSw button[data-row="2"]');
    await page.click('#alarmSlots [data-alarm-slot="9"]');
    await scrollTo('#alarmTime', 120);
  } else if (view === 'phone-speakers') {
    await tab('Speakers');
    await page.waitForSelector('#peers .peer', { timeout: 9000 });
    await page.waitForSelector('#groupCard:not([style*="none"]) #members', { timeout: 9000 });
    // The peer list painted before the group was known; repaint it once (the
    // page itself does so every 20 s) so a member no longer offers "+".
    await page.evaluate(() => (typeof loadPeers === 'function' ? loadPeers() : null));
    await page.evaluate(() => window.scrollTo(0, 0));
  }
  await page.waitForTimeout(600); // let art and transitions settle
}

// ---- main ------------------------------------------------------------------
phone = phoneHandler({ indexHTML: await readFile(path.join(ASSETS, 'index.html'), 'utf8'), iconPNG: await readFile(path.join(ASSETS, 'icon.png')) });

const browser = await chromium.launch({ headless: true });
let ok = 0, fail = 0;
try {
  for (const theme of THEMES) for (const lang of LANGS) {
    const dir = theme ? path.join(OUT, theme, lang) : path.join(OUT, lang);
    await mkdir(dir, { recursive: true });
    const ctx = await browser.newContext({
      viewport: { width: WIDTH, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true,
      locale: lang, timezoneId: 'Europe/Berlin',
      userAgent: PHONE.USER_AGENT,
    });
    await ctx.addInitScript(([l, th]) => {
      try {
        localStorage.setItem('str.remote.lang', l);
        localStorage.setItem('coachSeen', '1'); // the first-visit hint bubble
        if (th) localStorage.setItem('a11yTheme', th);
      } catch (e) {}
    }, [lang, theme]);
    await ctx.route('**/*', phone.handle);
    for (const view of VIEWS) {
      phone.options.view = view;
      phone.options.grouped = view === 'phone-speakers';
      // The second row of keys: filled for these shots, empty for the rest
      // (the explainer); the playing shot plays key 3 pressed twice.
      phone.options.row2 = ROW2_FILLED.includes(view);
      phone.options.playing = view === 'phone-keys-row2-playing'
        ? { name: 'Jazz Lounge', loc: 'http://192.0.2.50:8888/stream/9', key: '' } : null;
      shotPath = path.join(dir, view + '.png');
      const page = await ctx.newPage();
      page.on('pageerror', (e) => console.warn(`  [${lang}/${view}] pageerror:`, e.message));
      try {
        await page.goto(ORIGIN + '/', { waitUntil: 'domcontentloaded' });
        await drive(page, view);
        await page.screenshot({ path: shotPath });
        console.log(`ok  ${theme ? theme + '/' : ''}${lang}/${view}`);
        ok++;
      } catch (e) {
        console.error(`FAIL ${lang}/${view}: ${e.message}`);
        fail++;
      }
      await page.close();
    }
    await ctx.close();
  }
} finally {
  await browser.close();
}
if (phone.unmocked.size) console.log('\nUnmocked calls (answered {}):\n  ' + [...phone.unmocked].sort().join('\n  '));
console.log(`\nDone: ${ok} ok, ${fail} failed -> ${OUT}`);
if (fail) process.exitCode = 1;
