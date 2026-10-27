// helpers/checkharness.mjs: the setup wizard's check step, bundled on its own
// and injected into the running app (checkharness-entry.js).
//
//   const harness = await checkHarness();          // once per worker
//   await injectCheckHarness(app.page, harness);   // after the app booted
//   await app.page.evaluate((b) => window.__checkStep.mount(b), box);
//
// The bundle is built in memory with the frontend's own vite, from the real
// src/ modules, so the step runs on the same i18n bundles, Wails mock and
// shell2 tokens as the app; only its host element is the harness's.

import { build } from 'vite';
import { fileURLToPath } from 'node:url';

const FRONTEND = fileURLToPath(new URL('../../', import.meta.url));
const ENTRY = fileURLToPath(new URL('./checkharness-entry.js', import.meta.url));

let cached = null;

export async function checkHarness() {
  if (cached) return cached;
  const out = await build({
    configFile: false,
    root: FRONTEND,
    logLevel: 'silent',
    build: {
      write: false,
      minify: false,
      cssCodeSplit: false,
      lib: { entry: ENTRY, formats: ['iife'], name: 'StrCheckHarness', fileName: () => 'checkharness.js' },
      rollupOptions: { output: { assetFileNames: 'checkharness.[ext]' } },
    },
  });
  const files = (Array.isArray(out) ? out : [out]).flatMap((o) => o.output);
  const js = files.find((f) => f.type === 'chunk');
  const css = files.find((f) => f.type === 'asset' && String(f.fileName).endsWith('.css'));
  cached = {
    js: js.code,
    css: css ? (typeof css.source === 'string' ? css.source : Buffer.from(css.source).toString('utf8')) : '',
  };
  return cached;
}

// The app's Content-Security-Policy allows scripts from its own origin only,
// so the bundle is served under that origin by a route rather than inlined.
export async function injectCheckHarness(page, harness) {
  await page.route('**/__e2e/checkharness.js', (r) => r.fulfill({ contentType: 'text/javascript', body: harness.js }));
  await page.route('**/__e2e/checkharness.css', (r) => r.fulfill({ contentType: 'text/css', body: harness.css }));
  if (harness.css) await page.addStyleTag({ url: '/__e2e/checkharness.css' });
  await page.addScriptTag({ url: '/__e2e/checkharness.js' });
  await page.waitForFunction(() => !!window.__checkStep);
}
