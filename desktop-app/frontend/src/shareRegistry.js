// Share target registry: validation and resolution.
//
// The share targets themselves are NOT listed anywhere in the app. The single
// source is the website registry (st-reborn.de/share-targets.json, maintained
// in JRpersonal/streborn-website at website/src/data/share-targets.json). A
// copy is pulled at build time by scripts/sync-share-targets.mjs into
// src/data/share-targets.json, so the app never fetches it at runtime. This
// module mirrors the website's website/src/lib/share.ts: the same validation
// rules and the same resolution, so both surfaces show the same targets in the
// same order and groups. A new target is a new registry entry, nothing else.
//
// Pure module on purpose (no DOM, no Wails, no i18n import): the sync script
// runs it under plain Node, the tests call it directly, and share.js feeds it
// the app's translation lookup.

// Same encoder as utils.js encodeURIStrict (utils.js pulls in the i18n bundles,
// which plain Node cannot import). Wails refuses a URL that still carries one
// of ! ' ( ) * ~ after encodeURIComponent, see mailtourl.test.js.
function encodeURIStrict(s) {
  return encodeURIComponent(s).replace(/[!'()*~]/g,
    (c) => '%' + c.charCodeAt(0).toString(16).toUpperCase());
}

export const SHARE_GROUPS = ['social', 'messenger', 'forum', 'direct'];
export const SHARE_KINDS = ['url-template', 'copy', 'mailto', 'instance-prompt'];

// Languages of st-reborn.de (website/src/i18n/ui.ts). A registry entry may
// only name these in locales, shareLocale and titleLocale, same rule as the
// website build. English lives at the site root, every other language under
// /<lang>/.
export const SITE_LANGS = [
  'en', 'de', 'fr', 'es', 'ja', 'uk', 'nl', 'pl', 'lt', 'lv', 'tr', 'ar',
  'zh-tw', 'zh-cn', 'ko', 'hi', 'sv', 'no', 'da', 'fi',
];
const SITE_ROOT = 'https://st-reborn.de';

// The app and the website use the same language codes except for Traditional
// Chinese: the app bundle is zh-Hant, the website path is /zh-tw/.
const APP_TO_SITE = { 'zh-Hant': 'zh-tw' };
const SITE_TO_APP = { 'zh-tw': 'zh-Hant' };

export function siteLang(appLocale) {
  const l = APP_TO_SITE[appLocale] || appLocale;
  return SITE_LANGS.includes(l) ? l : 'en';
}
export function appLang(siteCode) {
  return SITE_TO_APP[siteCode] || siteCode;
}

// homeUrl is the shared page: the website's start page in that language, with
// no parameter of any kind (no UTM, no ?share).
export function homeUrl(site) {
  return site === 'en' ? SITE_ROOT + '/' : `${SITE_ROOT}/${site}/`;
}

// validateRegistry applies the website's build-time rules and throws on the
// first problem, so a typo in the registry fails the sync (and the test run)
// instead of shipping a broken button. hasKey(key) reports whether the app's
// English bundle carries a text key; appLocales lists the app's UI languages.
export function validateRegistry(registry, { hasKey, appLocales }) {
  if (!registry || !Array.isArray(registry.targets)) {
    throw new Error('share-targets.json: "targets" array missing');
  }
  const ids = new Set();
  for (const d of registry.targets) {
    const where = `share-targets.json, target "${d && d.id}"`;
    if (!d || !d.id || ids.has(d.id)) throw new Error(`${where}: id missing or duplicate`);
    ids.add(d.id);
    if (!SHARE_GROUPS.includes(d.group)) throw new Error(`${where}: unknown group`);
    if (!SHARE_KINDS.includes(d.kind)) throw new Error(`${where}: unknown kind`);
    if (typeof d.order !== 'number') throw new Error(`${where}: order missing`);
    if (typeof d.enabled !== 'boolean') throw new Error(`${where}: enabled missing`);
    if (!d.name && !d.label) throw new Error(`${where}: name or label missing`);
    if (d.label && !hasKey(`share.labels.${d.label}`)) throw new Error(`${where}: label "${d.label}" has no app text share.labels.${d.label}`);
    if (d.note && !hasKey(`share.notes.${d.note}`)) throw new Error(`${where}: note "${d.note}" has no app text share.notes.${d.note}`);
    if (d.kind !== 'copy' && !d.template) throw new Error(`${where}: template missing`);
    if (d.kind === 'instance-prompt' && !d.template.includes('{instance}')) throw new Error(`${where}: template without {instance}`);
    for (const l of [d.shareLocale, d.titleLocale, ...(d.locales || [])]) {
      if (l && !SITE_LANGS.includes(l)) throw new Error(`${where}: unknown language "${l}"`);
    }
    // The title and text come from the app's own bundles, so a titleLocale the
    // app has no bundle for would silently fall back to English.
    if (d.titleLocale && !appLocales.includes(appLang(d.titleLocale))) {
      throw new Error(`${where}: titleLocale "${d.titleLocale}" has no app bundle`);
    }
    // Hard rule: Reddit never gets a prefilled text. Identical prewritten posts
    // with the same link are treated as coordinated spam and get the domain
    // banned.
    if (/reddit\.com/.test(d.template || '') && d.allowBody !== false) {
      throw new Error(`${where}: Reddit targets need allowBody false`);
    }
  }
  return registry.targets;
}

function fill(template, url, title, text) {
  return template
    .replaceAll('{url}', encodeURIStrict(url))
    .replaceAll('{title}', encodeURIStrict(title))
    .replaceAll('{text}', encodeURIStrict(text));
}

// resolveShareTargets returns the enabled targets for an app UI locale, grouped
// in the fixed group order and sorted by order inside each group; empty groups
// are dropped. tr(appLocale, key, params) is the translation lookup.
export function resolveShareTargets(defs, appLocale, tr) {
  const lang = siteLang(appLocale);
  const resolved = defs
    .filter((d) => d.enabled && (!d.locales || !d.locales.length || d.locales.includes(lang)))
    .sort((a, b) => a.order - b.order)
    .map((d) => {
      const shareLang = d.shareLocale && SITE_LANGS.includes(d.shareLocale) ? d.shareLocale : lang;
      const titleApp = d.titleLocale && SITE_LANGS.includes(d.titleLocale) ? appLang(d.titleLocale) : appLocale;
      const shareUrl = homeUrl(shareLang);
      const title = tr(titleApp, 'share.postTitle');
      const text = d.allowBody === false ? '' : tr(titleApp, 'share.text');
      const name = d.label ? tr(appLocale, `share.labels.${d.label}`) : d.name;
      const showNote = d.note && (!d.shareLocale || d.shareLocale !== lang);
      return {
        id: d.id,
        group: d.group,
        kind: d.kind,
        name,
        label: d.label ? name : tr(appLocale, 'share.on', { name }),
        note: showNote ? tr(appLocale, `share.notes.${d.note}`) : undefined,
        icon: d.icon,
        color: d.color,
        shareUrl,
        href: d.kind === 'url-template' || d.kind === 'mailto' ? fill(d.template, shareUrl, title, text) : undefined,
        template: d.kind === 'instance-prompt' ? fill(d.template, shareUrl, title, text) : undefined,
      };
    });
  return SHARE_GROUPS
    .map((group) => ({ group, targets: resolved.filter((r) => r.group === group) }))
    .filter((g) => g.targets.length > 0);
}

// cleanInstanceHost accepts only a bare host name (optionally with a port), the
// same rule as the website: scheme, path and user part are stripped, anything
// else is refused, so the Mastodon link can never point somewhere arbitrary.
// The path goes before the user part, so a pasted profile URL
// (https://mastodon.social/@me) keeps its host instead of turning into "me".
export function cleanInstanceHost(input) {
  const host = String(input || '').trim()
    .replace(/^https?:\/\//i, '')
    .replace(/\/.*$/, '')
    .replace(/^@?[^@]*@/, '')
    .toLowerCase();
  return /^[a-z0-9.-]+\.[a-z]{2,}(:\d+)?$/.test(host) ? host : '';
}
