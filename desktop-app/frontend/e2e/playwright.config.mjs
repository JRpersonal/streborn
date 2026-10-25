// Playwright config for the STR UI suite (README.md next to this file).
//
// The web server builds the frontend into desktop-app/.e2e-out/dist-<PORT>
// and serves it with vite preview. Everything this suite writes (the build,
// results, traces, the HTML report) goes to desktop-app/.e2e-out/, outside the
// Vite root, so a run never touches frontend/dist (and its go:embed anchor
// dist/.gitkeep), wails dev never scans report HTML, and two runs on two
// ports never overwrite each other's build.
//
//   E2E_PORT=4180 npx playwright test specs/desktop-smoke.spec.mjs
//   E2E_SKIP_BUILD=1 ...   reuses dist-<PORT> from an earlier run
//   E2E_WEBKIT=1 ...       adds the webkit-smoke project

import { defineConfig, devices } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import { PHONE } from '../testkit/demo-data.mjs';

const FRONTEND = fileURLToPath(new URL('..', import.meta.url));
const OUT = fileURLToPath(new URL('../../.e2e-out/', import.meta.url));
const PORT = Number(process.env.E2E_PORT) || 4179;
const DIST = `${OUT}dist-${PORT}`;

export default defineConfig({
  testDir: 'specs',
  outputDir: `${OUT}results-${PORT}`,
  timeout: 30_000,
  expect: { timeout: 5_000 },
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 3 : undefined,
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI
    ? [['github'], ['html', { open: 'never', outputFolder: `${OUT}report-${PORT}` }], ['list']]
    : [['list']],
  use: {
    baseURL: `http://127.0.0.1:${PORT}/`,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'off',
    timezoneId: 'Europe/Berlin',
    locale: 'en-US',
    reducedMotion: 'reduce',
    deviceScaleFactor: 1,
  },
  webServer: {
    command: (process.env.E2E_SKIP_BUILD ? '' : `npx vite build --outDir "${DIST}" --emptyOutDir && `)
      + `npx vite preview --outDir "${DIST}" --host 127.0.0.1 --port ${PORT} --strictPort`,
    cwd: FRONTEND,
    url: `http://127.0.0.1:${PORT}/`,
    // Never reuse: a preview server left on the port by another checkout
    // would serve ITS build, and the suite would test the wrong code.
    reuseExistingServer: false,
    timeout: 120_000,
    stdout: 'ignore',
    stderr: 'pipe',
  },
  projects: [
    // The app's default window (main.go) and the smallest one it allows.
    { name: 'desktop', use: { viewport: { width: 1240, height: 740 } }, testMatch: /(desktop-.*|_canary)\.spec\.mjs$/ },
    { name: 'desktop-min', use: { viewport: { width: 1000, height: 600 } }, testMatch: /desktop-(layout|i18n|banners)\.spec\.mjs$/ },
    {
      name: 'phone',
      use: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, userAgent: PHONE.USER_AGENT },
      testMatch: /phone-.*\.spec\.mjs$/,
    },
    ...(process.env.E2E_WEBKIT
      ? [{ name: 'webkit-smoke', use: { ...devices['Desktop Safari'], viewport: { width: 1240, height: 740 } }, testMatch: /desktop-smoke\.spec\.mjs$/ }]
      : []),
  ],
});
