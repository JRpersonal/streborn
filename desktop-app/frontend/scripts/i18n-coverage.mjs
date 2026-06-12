// i18n coverage report. en.json is the reference (the global-audience default);
// every other locale bundle is compared against it. Prints, per locale, the
// keys it is missing and any keys it carries that en does not. Report-only:
// exits 0 so an in-progress translation never blocks a PR, but the drift is
// visible in the CI log. Run from desktop-app/frontend: `node scripts/i18n-coverage.mjs`.
import { readFileSync, readdirSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const dir = join(here, '..', 'src', 'i18n', 'bundles');
const load = (f) => JSON.parse(readFileSync(join(dir, f), 'utf-8'));

const ref = load('en.json');
const refKeys = Object.keys(ref);
const files = readdirSync(dir).filter((f) => f.endsWith('.json') && f !== 'en.json').sort();

console.log(`reference en.json: ${refKeys.length} keys\n`);
let worstMissing = 0;
for (const f of files) {
  const b = load(f);
  const keys = new Set(Object.keys(b));
  const missing = refKeys.filter((k) => !keys.has(k));
  const extra = Object.keys(b).filter((k) => !(k in ref));
  worstMissing = Math.max(worstMissing, missing.length);
  const pct = (((refKeys.length - missing.length) / refKeys.length) * 100).toFixed(1);
  console.log(`${f}: ${pct}% (${missing.length} missing, ${extra.length} extra)`);
  if (missing.length) console.log(`  missing: ${missing.slice(0, 20).join(', ')}${missing.length > 20 ? ', ...' : ''}`);
  if (extra.length) console.log(`  extra:   ${extra.slice(0, 20).join(', ')}${extra.length > 20 ? ', ...' : ''}`);
}
console.log(`\nworst-case missing in any locale: ${worstMissing}`);
