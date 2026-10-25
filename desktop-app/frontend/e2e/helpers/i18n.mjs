// helpers/i18n.mjs: the desktop app's language bundles, read from the source
// (src/i18n/bundles/*.json, flat key -> text).

import { readdirSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const DIR = fileURLToPath(new URL('../../src/i18n/bundles/', import.meta.url));

// Every bundle code, en first.
export const LOCALES = readdirSync(DIR)
  .filter((f) => f.endsWith('.json'))
  .map((f) => f.slice(0, -5))
  .sort((a, b) => (a === 'en' ? -1 : b === 'en' ? 1 : a.localeCompare(b)));

export function bundle(code) {
  return JSON.parse(readFileSync(`${DIR}${code}.json`, 'utf8'));
}

export const EN = bundle('en');
export const EN_KEYS = new Set(Object.keys(EN));

// The en text of a key with {{name}} placeholders filled in.
export function en(key, vars = {}) {
  const s = EN[key];
  if (typeof s !== 'string') throw new Error(`no en text for ${key}`);
  return s.replace(/\{\{\s*(\w+)\s*\}\}/g, (_, k) => (k in vars ? String(vars[k]) : `{{${k}}}`));
}

export const RTL = ['ar'];
// The languages whose labels run longest, the ones that overflow first.
export const LONG = ['de', 'lt', 'uk', 'ar'];
