// Share buttons driven by the website registry (src/data/share-targets.json).
// Covers the acceptance rules: no second list of targets in the app, new and
// disabled registry entries need no code change, the Reddit rules, the shared
// URL without parameters, every UI language carrying every text, and the
// install success row showing exactly once per computer.
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

vi.mock('./api.js', () => ({ BrowserOpenURL: () => {}, ClipboardSetText: async () => true }));

// A minimal localStorage so share.js can remember the one-time offer.
const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => { store.set(k, String(v)); },
  removeItem: (k) => { store.delete(k); },
};

const { tIn, setLocale } = await import('./i18n/index.js');
const { validateRegistry, resolveShareTargets, cleanInstanceHost } = await import('./shareRegistry.js');
const { takeShareOffer } = await import('./share.js');

const here = dirname(fileURLToPath(import.meta.url));
const registry = JSON.parse(readFileSync(join(here, 'data', 'share-targets.json'), 'utf-8'));
const bundleDir = join(here, 'i18n', 'bundles');
const bundles = Object.fromEntries(readdirSync(bundleDir).filter((f) => f.endsWith('.json'))
  .map((f) => [f.slice(0, -5), JSON.parse(readFileSync(join(bundleDir, f), 'utf-8'))]));
const appLocales = Object.keys(bundles);
const ctx = { hasKey: (k) => k in bundles.en, appLocales };

// Wails refuses these in a URL passed to BrowserOpenURL (see mailtourl.test.js).
const WAILS_REJECTS = /[;|`$\\<>*{}[\]()~! \t\n\r]/;

const flat = (groups) => groups.flatMap((g) => g.targets);
const resolve = (defs, locale) => resolveShareTargets(defs, locale, tIn);
const target = (over) => ({ id: 'x', name: 'X', group: 'social', order: 99, enabled: true, kind: 'url-template', template: 'https://x.example/share?u={url}', icon: 'x', ...over });

describe('share registry', () => {
  it('the committed registry passes the website rules', () => {
    expect(() => validateRegistry(registry, ctx)).not.toThrow();
  });

  it('keeps the registry order and groups', () => {
    const groups = resolve(registry.targets, 'en');
    expect(groups.map((g) => g.group)).toEqual(['social', 'messenger', 'forum', 'direct']);
    const social = groups[0].targets.map((x) => x.id);
    const expected = registry.targets.filter((d) => d.group === 'social' && d.enabled)
      .sort((a, b) => a.order - b.order).map((d) => d.id);
    expect(social).toEqual(expected);
  });

  it('shows a new registry entry without a code change', () => {
    const defs = [...registry.targets, target({ id: 'newnet', name: 'NewNet', icon: 'unknown-icon' })];
    const trg = flat(resolve(defs, 'de')).find((x) => x.id === 'newnet');
    expect(trg.label).toBe('Auf NewNet teilen');
    expect(trg.href).toBe('https://x.example/share?u=' + encodeURIComponent('https://st-reborn.de/de/'));
  });

  it('hides a target with enabled false', () => {
    const defs = registry.targets.map((d) => (d.id === 'facebook' ? { ...d, enabled: false } : d));
    expect(flat(resolve(defs, 'en')).some((x) => x.id === 'facebook')).toBe(false);
  });

  it('honours locales', () => {
    const defs = [target({ id: 'deonly', locales: ['de'] })];
    expect(flat(resolve(defs, 'de')).map((x) => x.id)).toEqual(['deonly']);
    expect(flat(resolve(defs, 'en'))).toEqual([]);
  });

  it('shares the start page in the UI language, without any parameter', () => {
    const byLocale = { en: 'https://st-reborn.de/', de: 'https://st-reborn.de/de/', 'zh-Hant': 'https://st-reborn.de/zh-tw/' };
    for (const [loc, url] of Object.entries(byLocale)) {
      const copy = flat(resolve(registry.targets, loc)).find((x) => x.kind === 'copy');
      expect(copy.shareUrl).toBe(url);
    }
    for (const loc of appLocales) {
      for (const x of flat(resolve(registry.targets, loc))) {
        expect(x.shareUrl).not.toMatch(/[?#]/);
        expect(x.href || x.template || '').not.toMatch(/utm_|[?&]share\b/);
      }
    }
  });

  it('from the German UI, Reddit gets the English title, English page, no text, and the forum note', () => {
    setLocale('de');
    const reddit = flat(resolve(registry.targets, 'de')).find((x) => x.id === 'reddit');
    const u = new URL(reddit.href);
    expect(u.searchParams.get('url')).toBe('https://st-reborn.de/');
    expect(u.searchParams.get('title')).toBe(bundles.en['share.postTitle']);
    expect(u.searchParams.has('text')).toBe(false);
    expect(reddit.href).not.toContain(encodeURIComponent(bundles.de['share.text']));
    expect(reddit.note).toBe(bundles.de['share.notes.englishForum']);
    expect(reddit.label).toBe('Auf Reddit teilen');
  });

  it('hides the forum note when the UI already speaks the shared language', () => {
    const reddit = flat(resolve(registry.targets, 'en')).find((x) => x.id === 'reddit');
    expect(reddit.note).toBeUndefined();
  });

  it('builds URLs Wails accepts in every language', () => {
    for (const loc of appLocales) {
      for (const x of flat(resolve(registry.targets, loc))) {
        const url = x.href || (x.template && x.template.replace('{instance}', 'mastodon.social'));
        if (url) expect(url, `${loc} ${x.id}`).not.toMatch(WAILS_REJECTS);
      }
    }
  });

  it('rejects what the website build rejects', () => {
    const bad = (over) => () => validateRegistry({ targets: [target(over)] }, ctx);
    expect(bad({ template: 'https://www.reddit.com/submit?url={url}', allowBody: true })).toThrow(/allowBody/);
    expect(bad({ template: 'https://www.reddit.com/submit?url={url}' })).toThrow(/allowBody/);
    expect(bad({ group: 'video' })).toThrow(/group/);
    expect(bad({ kind: 'api' })).toThrow(/kind/);
    expect(bad({ name: undefined })).toThrow(/name or label/);
    expect(bad({ name: undefined, label: 'fax' })).toThrow(/label/);
    expect(bad({ note: 'nope' })).toThrow(/note/);
    expect(bad({ template: undefined })).toThrow(/template/);
    expect(bad({ kind: 'instance-prompt' })).toThrow(/instance/);
    expect(bad({ shareLocale: 'xx' })).toThrow(/language/);
    expect(() => validateRegistry({ targets: [target(), target()] }, ctx)).toThrow(/duplicate/);
  });
});

describe('Mastodon instance', () => {
  it('accepts only a bare host name', () => {
    expect(cleanInstanceHost('mastodon.social')).toBe('mastodon.social');
    expect(cleanInstanceHost(' https://Mastodon.Social/@me ')).toBe('mastodon.social');
    expect(cleanInstanceHost('@user@chaos.social')).toBe('chaos.social');
    expect(cleanInstanceHost('example.org:8443')).toBe('example.org:8443');
    expect(cleanInstanceHost('localhost')).toBe('');
    expect(cleanInstanceHost('evil.example/path?x=1')).toBe('evil.example');
    expect(cleanInstanceHost('javascript:alert(1)')).toBe('');
    expect(cleanInstanceHost('a b.com')).toBe('');
  });
});

describe('share texts', () => {
  const keys = [
    'share.menu', 'share.successLine', 'share.on', 'share.copied', 'share.copyFailed',
    'share.mastodonPrompt', 'share.mastodonChange', 'share.postTitle', 'share.text',
    'share.labels.email', 'share.labels.copy', 'share.notes.englishForum',
    'share.groups.social', 'share.groups.messenger', 'share.groups.forum', 'share.groups.direct',
  ];
  it('every UI language carries every share key', () => {
    for (const [loc, b] of Object.entries(bundles)) {
      for (const k of keys) expect(b[k], `${loc} ${k}`).toBeTruthy();
      expect(b['share.on'], loc).toContain('{{name}}');
      expect(b['share.successLine'] + b['share.menu'], loc).not.toContain('!');
    }
  });
});

describe('no second list of share targets', () => {
  it('no app source names a share intent URL', () => {
    const files = [];
    const walk = (dir) => {
      for (const e of readdirSync(dir, { withFileTypes: true })) {
        const p = join(dir, e.name);
        if (e.isDirectory()) { if (e.name !== 'data' && e.name !== 'bundles') walk(p); }
        else if (e.name.endsWith('.js') && !e.name.endsWith('.test.js')) files.push(p);
      }
    };
    walk(here);
    const intents = /wa\.me\/|sharer\.php|bsky\.app\/intent|t\.me\/share|reddit\.com\/submit|share-offsite/;
    for (const f of files) expect(readFileSync(f, 'utf-8'), f).not.toMatch(intents);
  });
});

describe('install success row', () => {
  beforeEach(() => store.clear());

  it('is handed out exactly once per computer', () => {
    const first = takeShareOffer();
    expect(first).toContain('installShareOffer');
    expect(first).toContain('share-btn');
    expect(takeShareOffer()).toBe('');
    expect(takeShareOffer()).toBe('');
  });

  it('is not shown when the decision cannot be remembered', () => {
    const orig = globalThis.localStorage.setItem;
    globalThis.localStorage.setItem = () => { throw new Error('blocked'); };
    try {
      expect(takeShareOffer()).toBe('');
    } finally {
      globalThis.localStorage.setItem = orig;
    }
  });
});
