import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// The phone remote (internal/webui/assets/index.html) carries its own copy of
// the Bose sysLanguage table, because the speaker serves that page with no
// bundler. The desktop copy (BOSE_LANGS in views/settings.js) is the one that
// was resolved in full; the phone copy stopped at id 12 for a while, so a
// speaker set to Polish (19) or Turkish (24) never got its own language on the
// phone although both dictionaries were on board. This holds the two together.

const here = dirname(fileURLToPath(import.meta.url));
const settings = readFileSync(join(here, 'views', 'settings.js'), 'utf8');
const page = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8');

// extractObject evaluates one top-level object literal out of a source file,
// `<keyword> <name> = { ... };`, the way spotifyaccountlabel.test.js lifts
// functions off the phone page.
function extractObject(src, keyword, name, label) {
  const start = src.indexOf(`${keyword} ${name} = {`);
  expect(start, `${name} missing from ${label}`).toBeGreaterThan(-1);
  const end = src.indexOf('\n};', start);
  return new Function(`${src.slice(start, end + 3)}; return ${name};`)();
}

const desktop = extractObject(settings, 'const', 'BOSE_LANGS', 'views/settings.js');
const phone = extractObject(page, 'var', 'BOSE_LANG_CODES', 'the phone page');

// The desktop table names languages by their English key; the phone wants
// the two-letter code its dictionaries are filed under.
const CODE_OF = {
  danish: 'da', german: 'de', english: 'en', spanish: 'es', french: 'fr',
  italian: 'it', dutch: 'nl', swedish: 'sv', japanese: 'ja', chinese: 'zh',
  korean: 'ko', thai: 'th', czech: 'cs', finnish: 'fi', greek: 'el',
  norwegian: 'no', polish: 'pl', portuguese: 'pt', romanian: 'ro',
  russian: 'ru', slovenian: 'sl', turkish: 'tr', hungarian: 'hu',
};

// The locales the phone page ships, read off its first dictionary.
function phoneLocales() {
  const start = page.indexOf('var I18N = {');
  const end = page.indexOf('\n};', start);
  return [...page.slice(start, end).matchAll(/^ {2}([a-z]{2}):\{/gm)].map((m) => m[1]);
}

describe('phone remote copy of the Bose language table', () => {
  it('maps every id the desktop app knows to the same language', () => {
    const ids = Object.keys(desktop);
    expect(ids.length).toBeGreaterThan(12);
    for (const id of ids) {
      const key = desktop[id].key;
      expect(CODE_OF[key], `no code for desktop key ${key}`).toBeTruthy();
      expect(phone[id], `id ${id} (${key})`).toBe(CODE_OF[key]);
    }
  });

  it('knows no id the desktop app does not', () => {
    for (const id of Object.keys(phone)) {
      expect(desktop[id], `phone id ${id} is not in BOSE_LANGS`).toBeTruthy();
    }
  });

  it('resolves every phone dictionary that has a Bose id', () => {
    const codes = new Set(Object.values(phone));
    const withId = new Set(Object.values(CODE_OF));
    const locales = phoneLocales();
    expect(locales).toContain('pl');
    expect(locales).toContain('tr');
    for (const loc of locales) {
      if (!withId.has(loc)) continue; // ar, lt, lv, uk: no Bose language id
      expect(codes.has(loc), `speaker language ${loc} never selects the phone's ${loc} dictionary`).toBe(true);
    }
  });
});
