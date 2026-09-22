// Pulls the share target registry from the website into the app.
//
//   node scripts/sync-share-targets.mjs                  fetch https://st-reborn.de/share-targets.json
//   node scripts/sync-share-targets.mjs --file <path>    read a local copy, e.g. a
//                                                        streborn-website checkout's
//                                                        website/src/data/share-targets.json
//
// The result is written to src/data/share-targets.json and committed, so the
// app itself never makes a network request for it. Before writing, the file is
// checked with the same rules the website build applies (src/shareRegistry.js);
// any problem aborts with a non-zero exit and leaves the old copy untouched.
// Run from desktop-app/frontend, or via `make share-targets` at the repo root.
import { readFileSync, writeFileSync, readdirSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { validateRegistry } from '../src/shareRegistry.js';

const DEFAULT_URL = 'https://st-reborn.de/share-targets.json';
const here = dirname(fileURLToPath(import.meta.url));
const bundles = join(here, '..', 'src', 'i18n', 'bundles');
const out = join(here, '..', 'src', 'data', 'share-targets.json');

async function load() {
  const i = process.argv.indexOf('--file');
  if (i >= 0) {
    const path = process.argv[i + 1];
    if (!path) throw new Error('--file needs a path');
    return { source: path, raw: readFileSync(path, 'utf-8') };
  }
  const url = process.env.SHARE_TARGETS_URL || DEFAULT_URL;
  const res = await fetch(url, { headers: { Accept: 'application/json' } });
  if (!res.ok) throw new Error(`${url}: HTTP ${res.status}`);
  return { source: url, raw: await res.text() };
}

try {
  const { source, raw } = await load();
  const registry = JSON.parse(raw);
  const en = JSON.parse(readFileSync(join(bundles, 'en.json'), 'utf-8'));
  const appLocales = readdirSync(bundles).filter((f) => f.endsWith('.json')).map((f) => f.slice(0, -5));
  validateRegistry(registry, { hasKey: (k) => k in en, appLocales });
  writeFileSync(out, JSON.stringify(registry, null, 2) + '\n');
  console.log(`share targets: ${registry.targets.length} from ${source} -> src/data/share-targets.json`);
} catch (e) {
  console.error(`share targets NOT updated: ${e.message}`);
  process.exit(1);
}
