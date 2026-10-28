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

import { chromium } from 'playwright';
import { mkdir, readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { PHONE } from '../testkit/demo-data.mjs';
import { phoneHandler } from '../testkit/phone-mock.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(__dirname, '../../..');
const ASSETS = path.join(REPO, 'internal/webui/assets');
const OUT = path.join(REPO, 'docs/screenshots/phone');
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
  for (const lang of LANGS) {
    await mkdir(path.join(OUT, lang), { recursive: true });
    const ctx = await browser.newContext({
      viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true,
      locale: lang, timezoneId: 'Europe/Berlin',
      userAgent: PHONE.USER_AGENT,
    });
    await ctx.addInitScript((l) => {
      try {
        localStorage.setItem('str.remote.lang', l);
      } catch (e) {}
    }, lang);
    await ctx.route('**/*', phone.handle);
    for (const view of VIEWS) {
      phone.options.view = view;
      phone.options.grouped = view === 'phone-speakers';
      const page = await ctx.newPage();
      page.on('pageerror', (e) => console.warn(`  [${lang}/${view}] pageerror:`, e.message));
      try {
        await page.goto(ORIGIN + '/', { waitUntil: 'domcontentloaded' });
        await drive(page, view);
        await page.screenshot({ path: path.join(OUT, lang, view + '.png') });
        console.log(`ok  ${lang}/${view}`);
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
