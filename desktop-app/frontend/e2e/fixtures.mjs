// fixtures.mjs: the test object every spec imports.
//
//   import { test, expect } from '../fixtures.mjs';
//   test.use({ ui2: true, scenario: { boxes: ['livingRoom', 'bedroom'] } });
//   test('...', async ({ app, mock }) => { ... });
//
// Options (test.use):
//   ui2, theme, appLocale, scale   the seed (localStorage at boot)
//   scenario    a patch on testkit/scenario.mjs defaultScenario()
//   fakeClock   install the page clock at 2026-10-23T18:00:00Z before boot
//   ready       selector that means "booted" (null: the layout's default)
//   preInit     an init script added BEFORE the mock (e.g. storage that throws)
//   phoneLang, phoneView, phoneGrouped, phoneOverrides   for the phone fixture
//   phonePresetRows, phoneRow2, phonePlaying   the second row of keys (phone-mock.mjs)
//
// Fixtures:
//   app    boots the desktop frontend on the mock; { page, api, errors, scenario }
//          (api.calls: the agent HTTP requests answered in Node)
//   mock   helpers/mockclient.mjs bound to the page
//   phone  boots the phone remote on testkit/phone-mock.mjs; { page, handler, errors }
//   netGuard (automatic) aborts every request that leaves the preview server
//
// Every test fails on an uncaught page error. Console errors are attached to
// the report, not failed on.

import { test as base, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { DEMO, PHONE } from '../testkit/demo-data.mjs';
import { defaultScenario, mergeScenario, resolveScenario } from '../testkit/scenario.mjs';
import { installDesktopMock } from '../testkit/desktop-mock.mjs';
import { agentRoutes } from '../testkit/agent-routes.mjs';
import { phoneHandler } from '../testkit/phone-mock.mjs';
import { mockClient } from './helpers/mockclient.mjs';

export { expect };

export const CLOCK_START = new Date('2026-10-23T18:00:00Z');
export const OLD_READY = '#presets .preset-body, #presets .preset-hint';
export const UI2_READY = 'html.ui2 #ui2Shell';

const ASSETS = fileURLToPath(new URL('../../../internal/webui/assets/', import.meta.url));
let phoneAssets = null;
const loadPhoneAssets = () => {
  if (!phoneAssets) {
    phoneAssets = {
      indexHTML: readFileSync(`${ASSETS}index.html`, 'utf8'),
      iconPNG: readFileSync(`${ASSETS}icon.png`),
    };
  }
  return phoneAssets;
};

async function attachConsole(consoleErrors) {
  if (consoleErrors.length) {
    await base.info().attach('console-errors', { body: consoleErrors.join('\n'), contentType: 'text/plain' });
  }
}

export const test = base.extend({
  ui2: [false, { option: true }],
  theme: ['dark', { option: true }],
  appLocale: ['en', { option: true }],
  scale: ['1', { option: true }],
  scenario: [{}, { option: true }],
  fakeClock: [false, { option: true }],
  ready: [null, { option: true }],
  preInit: [null, { option: true }],
  phoneLang: ['en', { option: true }],
  phoneView: ['', { option: true }],
  phoneGrouped: [false, { option: true }],
  phoneOverrides: [{}, { option: true }],
  phonePresetRows: [true, { option: true }],
  phoneRow2: [false, { option: true }],
  phonePlaying: [null, { option: true }],

  // Registered first, so every route added later wins over it.
  netGuard: [async ({ page, baseURL }, use) => {
    await page.route('**/*', (route) => {
      const u = route.request().url();
      return (u.startsWith(baseURL) || u.startsWith('data:')) ? route.fallback() : route.abort();
    });
    await use();
  }, { auto: true }],

  mock: async ({ page }, use) => {
    await use(mockClient(page));
  },

  app: async ({ page, netGuard, ui2, theme, appLocale, scale, scenario, fakeClock, ready, preInit }, use) => {
    void netGuard;
    const sc = resolveScenario(mergeScenario(
      mergeScenario(defaultScenario(), { seed: { ui2, theme, locale: appLocale, scale } }),
      scenario,
    ));
    if (fakeClock) await page.clock.install({ time: CLOCK_START });
    if (preInit) await page.addInitScript(preInit);
    await page.addInitScript(installDesktopMock, { scenario: sc, demo: DEMO });
    const api = agentRoutes(sc);
    await page.route('**/api/**', api);
    const errors = [];
    page.on('pageerror', (e) => errors.push(e.message));
    const consoleErrors = [];
    page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text()); });
    await page.goto('/');
    await expect(page.locator(ready ?? (ui2 ? UI2_READY : OLD_READY)).first()).toBeVisible();
    await use({ page, api, errors, scenario: sc });
    await attachConsole(consoleErrors);
    expect(errors, 'uncaught page errors').toEqual([]);
  },

  phone: async ({ page, netGuard, phoneLang, phoneView, phoneGrouped, phoneOverrides, phonePresetRows, phoneRow2, phonePlaying }, use) => {
    void netGuard;
    const handler = phoneHandler({
      ...loadPhoneAssets(), view: phoneView, grouped: phoneGrouped, overrides: phoneOverrides,
      presetRows: phonePresetRows, row2: phoneRow2, playing: phonePlaying,
    });
    await page.addInitScript((lang) => {
      try {
        localStorage.setItem('str.remote.lang', lang);
        localStorage.setItem('coachSeen', '1'); // the first-visit hint bubble
      } catch (e) {}
    }, phoneLang);
    // Owns every request: it serves the placeholder origin and aborts the rest.
    await page.route('**/*', handler.handle);
    const errors = [];
    page.on('pageerror', (e) => errors.push(e.message));
    const consoleErrors = [];
    page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text()); });
    await page.goto(`${PHONE.ORIGIN}/`);
    await use({ page, handler, errors });
    await attachConsole(consoleErrors);
    expect(errors, 'uncaught page errors').toEqual([]);
  },
});
