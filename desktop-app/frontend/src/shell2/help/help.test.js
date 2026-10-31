// Help and info (UI 2.0 phase 5, shell2/help): the section order and their
// headings, the trademark notice, the save-logs line, the legal pages per
// language, the strings in all thirteen bundles, the donate buttons living
// here alone, and an import that touches no DOM.
import { describe, it, expect, vi } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { fileSource, sourceFiles } from '../../test/source.js';
import { setLocale, t } from '../../i18n/index.js';
import { HELP_SECTIONS, helpHTML, helpModel, logsStatus, updateStatus } from './help.js';
import { imprintURL, privacyURL, releaseNotesURL, repoURL, websiteURL } from './about.js';
import { shellMarkup } from '../markup.js';
import { VOTE_URL, SUGGEST_URL } from '../../views/wishes.js';

setLocale('en');

const dir = new URL('../../i18n/bundles/', import.meta.url);
const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
  .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));

const INFO = { version: 'v1.2.1', build: '2026-10-30', author: 'Jens Roggenfelder', githubUrl: 'https://github.com/JRpersonal/streborn/', websiteUrl: 'https://st-reborn.de' };
const page = (over = {}) => helpHTML(helpModel({ appInfo: INFO, locale: 'en', faqUrl: 'https://st-reborn.de/#faq', ...over }), t);
// The markup between two markers, for "this is inside that section".
const sectionOf = (html, id) => {
  const at = html.indexOf(`data-section="${id}"`);
  const end = html.indexOf('</section>', at);
  return at < 0 ? '' : html.slice(at, end);
};

describe('Help and info sections', () => {
  it('renders the sections in HELP_SECTIONS order, each labelled by its heading', () => {
    expect(HELP_SECTIONS).toEqual(['getHelp', 'wishes', 'community', 'support', 'about']);
    const html = page();
    const order = [...html.matchAll(/<section class="hlp-sec" data-section="(\w+)" aria-labelledby="hlpHead-(\w+)">/g)];
    expect(order.map((m) => m[1])).toEqual(HELP_SECTIONS);
    for (const m of order) {
      expect(m[2]).toBe(m[1]);
      expect(html).toContain(`<h3 class="hlp-sec-title" id="hlpHead-${m[1]}">`);
    }
    expect(html).toContain('<h2 class="hlp-title" id="hlpTitle">Help and info</h2>');
  });

  it('lists Get help in its order: FAQ, logs, report, ask, then the pointer to Speakers', () => {
    const sec = sectionOf(page(), 'getHelp');
    const at = (s) => sec.indexOf(s);
    const marks = ['Questions and answers', 'Save diagnostic logs', 'Report a problem', 'Ask a question', 'class="hlp-pointer"'];
    for (let i = 1; i < marks.length; i++) expect(at(marks[i]), marks[i]).toBeGreaterThan(at(marks[i - 1]));
    expect(sec).toContain('data-url="https://st-reborn.de/#faq"');
    expect(sec).toContain('data-url="https://github.com/JRpersonal/streborn/issues"');
    expect(sec).toContain('data-url="https://github.com/JRpersonal/streborn/discussions"');
    // The pointer names the Speakers area as a link inside its sentence and
    // the speaker page's Maintenance tab by its own name.
    expect(sec).toMatch(/Help for one speaker is on its page in <button type="button" class="hlp-link" data-act="view" data-view="speakers">Speakers<\/button>, under &quot;Maintenance&quot;\./);
  });

  it('offers the wishes without listing them', () => {
    const sec = sectionOf(page(), 'wishes');
    expect(sec).toContain('>Wishes</h3>');
    expect(sec).toContain(t('ui2.help.wishesLead'));
    expect(sec).toContain('data-act="view" data-view="wishes"');
    expect(sec).toContain(`data-url="${SUGGEST_URL}"`.replace(/&/g, '&amp;'));
    expect(sec).toContain(`data-url="${VOTE_URL}"`.replace(/&/g, '&amp;'));
    expect(sec).not.toMatch(/wish-row|wishes-list/);
  });

  it('carries the world map with its ask, and Recommend STR', () => {
    const sec = sectionOf(page(), 'community');
    expect(sec).toContain('data-act="worldMap"');
    expect(sec).toContain(t('worldMap.pinPrompt'));
    expect(sec).toContain('data-act="share"');
    expect(sec).toContain('Recommend STR');
  });

  it('states the funding plainly before the donate buttons, with no slogan', () => {
    const sec = sectionOf(page(), 'support');
    expect(sec).toContain('ST Reborn is free and open source. It is funded by donations.');
    expect(sec).not.toContain(t('footer.donateSlogan'));
    expect(sec.match(/data-donate="/g)).toHaveLength(3);
    expect(sec.indexOf('hlp-lead')).toBeLessThan(sec.indexOf('data-donate'));
  });

  it('shows the version, build and What\'s new, the update check, the credits and the links in About', () => {
    const sec = sectionOf(page(), 'about');
    expect(sec).toContain('Version v1.2.1');
    expect(sec).toContain('(Build 2026-10-30)');
    expect(sec).toContain('data-url="https://github.com/JRpersonal/streborn/releases/tag/v1.2.1"');
    expect(sec).toContain(">What's new<".replace("'", '&#39;'));
    expect(sec).toContain('data-act="checkUpdate"');
    expect(sec).toContain('data-act="credits"');
    expect(sec).toContain('>Open source<');
    for (const u of ['https://st-reborn.de', 'https://github.com/JRpersonal/streborn', 'https://st-reborn.de/privacy', 'https://st-reborn.de/imprint']) {
      expect(sec, u).toContain(`data-url="${u}"`);
    }
    expect(sec).toContain('Jens Roggenfelder');
    expect(sec).toContain(t('footer.fine'));
    // A dev build carries no build stamp.
    expect(sectionOf(page({ appInfo: { ...INFO, build: 'dev' } }), 'about')).not.toContain('(Build');
  });

  it('always shows the trademark notice in About, in plain text', () => {
    for (const info of [INFO, {}, undefined]) {
      const sec = sectionOf(helpHTML(helpModel({ appInfo: info }), t), 'about');
      expect(sec).toContain(`<p class="hlp-trademark" id="hlpTrademark">${t('about.trademark')}</p>`);
    }
    expect(page()).not.toMatch(/<details[^>]*>[\s\S]*hlpTrademark/);
  });
});

describe('Save diagnostic logs line', () => {
  it('says what runs, where the file went, nothing on a cancel, the error inline', () => {
    expect(logsStatus({ state: 'idle' }, t)).toEqual({ kind: '', text: '' });
    expect(logsStatus({ state: 'working' }, t)).toEqual({ kind: 'working', text: 'Saving logs...' });
    expect(logsStatus({ state: 'done', path: 'C:\\Users\\x\\str.zip', size: 42 }, t)).toEqual({ kind: 'done', text: 'Saved 42 KB to C:\\Users\\x\\str.zip' });
    // A cancelled dialog returns no path: the line stays empty.
    expect(logsStatus({ state: 'done', path: '' }, t).text).toBe('');
    expect(logsStatus({ state: 'error', error: 'disk full' }, t)).toEqual({ kind: 'error', text: 'The logs could not be saved: disk full' });
  });

  it('renders the line inside Get help, escaped, and marks the button busy while saving', () => {
    const idle = sectionOf(page(), 'getHelp');
    expect(idle).toContain('<p class="hlp-status" id="hlpLogsStatus" role="status" aria-live="polite" data-kind=""></p>');
    const busy = sectionOf(page({ logs: { state: 'working' } }), 'getHelp');
    expect(busy).toMatch(/id="hlpSaveLogs" aria-describedby="hlpSaveLogsHint" aria-disabled="true"/);
    expect(busy).toContain('data-kind="working">Saving logs...</p>');
    const done = sectionOf(page({ logs: { state: 'done', path: 'C:\\<b>evil</b>\\a.zip', size: 3 } }), 'getHelp');
    expect(done).toContain('C:\\&lt;b&gt;evil&lt;/b&gt;\\a.zip');
    expect(done).not.toContain('<b>evil</b>');
    const err = sectionOf(page({ logs: { state: 'error', error: '<x>' } }), 'getHelp');
    expect(err).toContain('data-kind="error">The logs could not be saved: &lt;x&gt;</p>');
  });

  it('saves for every speaker, like the footer link', () => {
    const src = fileSource('shell2/help/help.js');
    expect(src).toContain('SaveDiagnosticBundle(hosts, true)');
    expect(src).toContain("(state.boxes || []).map((b) => b && b.host).filter(Boolean)");
  });
});

describe('About links', () => {
  it('opens the legal pages in the language of the app', () => {
    expect(privacyURL('en')).toBe('https://st-reborn.de/privacy');
    expect(imprintURL('en')).toBe('https://st-reborn.de/imprint');
    expect(privacyURL('de')).toBe('https://st-reborn.de/de/datenschutz');
    expect(imprintURL('de')).toBe('https://st-reborn.de/de/impressum');
    // Every other language reads the English pages.
    expect(privacyURL('fr')).toBe('https://st-reborn.de/privacy');
    expect(imprintURL('ja')).toBe('https://st-reborn.de/imprint');
    const de = sectionOf(helpHTML(helpModel({ appInfo: INFO, locale: 'de' }), t), 'about');
    expect(de).toContain('data-url="https://st-reborn.de/de/datenschutz"');
    expect(de).toContain('data-url="https://st-reborn.de/de/impressum"');
  });

  it('opens the release notes the way the classic footer always did', () => {
    // The logic renderFooter had inline before it moved to about.js.
    const old = (i) => {
      const repo = (i.githubUrl || 'https://github.com/JRpersonal/streborn').replace(/\/+$/, '');
      const isTag = /^v\d+\.\d+\.\d+$/.test(i.version || '');
      return isTag ? `${repo}/releases/tag/${i.version}` : `${repo}/releases`;
    };
    const cases = [
      { version: 'v1.2.1', githubUrl: 'https://github.com/JRpersonal/streborn' },
      { version: 'v1.2.1', githubUrl: 'https://github.com/JRpersonal/streborn///' },
      { version: 'v0.6.21-3-gabc-dirty', githubUrl: 'https://github.com/JRpersonal/streborn' },
      { version: '1.0.0', githubUrl: '' },
      { version: 'unknown' },
      {},
    ];
    for (const c of cases) expect(releaseNotesURL(c), JSON.stringify(c)).toBe(old(c));
    expect(releaseNotesURL({ version: 'v1.2.1' })).toBe('https://github.com/JRpersonal/streborn/releases/tag/v1.2.1');
    expect(releaseNotesURL({ version: 'v1.2.1-2-gdeadbee' })).toBe('https://github.com/JRpersonal/streborn/releases');
    expect(repoURL('https://example.org/x/')).toBe('https://example.org/x');
    expect(websiteURL({})).toBe('https://st-reborn.de');
  });

  it('is used by renderFooter too', () => {
    const main = fileSource('main.js');
    expect(main).toContain('const releaseNotesUrl = releaseNotesURL(i);');
    expect(main).toContain('const repo = repoURL(i.githubUrl);');
  });
});

describe('Help and info strings', () => {
  const HELP_KEYS = Object.keys(bundles.en).filter((k) => k.startsWith('ui2.help.'));
  const placeholders = (s) => [...String(s).matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();

  it('uses only keys the English bundle has', () => {
    const used = new Set([...fileSource('shell2/help/help.js').matchAll(/'((?:ui2|about|footer|banner|wishes|share|worldMap|setup|nav)\.[a-zA-Z.]+)'/g)].map((m) => m[1]));
    used.add('ui2.nav.help');
    for (const k of used) expect(bundles.en[k], k).toBeTruthy();
    expect(HELP_KEYS.length).toBe(17);
  });

  for (const [code, b] of Object.entries(bundles)) {
    it(`${code} has every Help and info string, no dash as punctuation, the English placeholders`, () => {
      for (const k of [...HELP_KEYS, 'ui2.nav.help', 'about.trademark']) {
        expect(typeof b[k] === 'string' && b[k].trim().length > 0, `${code}: ${k}`).toBe(true);
        expect(b[k], `${code}: ${k}`).not.toMatch(/[\u2013\u2014]/);
        expect(placeholders(b[k]), `${code}: ${k}`).toEqual(placeholders(bundles.en[k]));
      }
      // The legal notice keeps the names as they are.
      const tm = b['about.trademark'];
      for (const name of ['Bose', 'SoundTouch', 'Bose Corporation']) expect(tm, `${code}: ${name}`).toContain(name);
    });
  }

  it('sits as one block right after ui2.nav.speakers in every bundle', () => {
    for (const [code, b] of Object.entries(bundles)) {
      const keys = Object.keys(b);
      const at = keys.indexOf('ui2.nav.speakers');
      expect(keys.slice(at + 1, at + 2 + HELP_KEYS.length), code).toEqual(['ui2.nav.help', ...HELP_KEYS]);
    }
  });

  it('writes German with du and real umlauts', () => {
    const de = bundles.de;
    for (const k of [...HELP_KEYS, 'ui2.nav.help', 'about.trademark']) {
      expect(de[k], k).not.toMatch(/\b(Sie|Ihnen|Ihre?[mnrs]?)\b/);
      expect(de[k], k).not.toMatch(/Wuensch|ueber|Naechst|Laend|unterstuetz|oeffn|erklaer/i);
    }
    expect(de['ui2.help.oneSpeaker']).toContain('findest du');
    expect(de['ui2.help.wishesLead']).toContain('Wünsche');
    expect(de['ui2.help.about']).toBe('Über ST Reborn');
  });
});

describe('Donate buttons live in Help and info alone', () => {
  it('appear in the help markup and nowhere else the shell renders', () => {
    expect(page()).toContain('data-donate="');
    expect(shellMarkup()).not.toContain('data-donate');
    for (const f of sourceFiles().filter((x) => x.startsWith('shell2/') && !x.endsWith('.test.js') && x !== 'shell2/help/help.js')) {
      const src = fileSource(f);
      expect(src, f).not.toContain('data-donate');
      expect(src, f).not.toContain('donateButtonsHTML');
    }
  });
});

describe('Help and info at import', () => {
  it('touches no DOM, storage or navigator', async () => {
    // vitest runs in node: there is no document, so any DOM access at import
    // would throw here. A fresh module registry makes the import really run.
    expect(typeof globalThis.document).toBe('undefined');
    vi.resetModules();
    const m = await import('./help.js');
    expect(typeof m.mountHelp).toBe('function');
    // Without a mount, a sync is a no-op and a mount without a node refuses.
    expect(() => m.syncHelpView('help')).not.toThrow();
    expect(m.mountHelp(null, {})).toBe(false);
    // And no top-level statement of its own names them (the wiring.test.js rule).
    for (const f of ['shell2/help/help.js', 'shell2/help/about.js']) {
      const top = fileSource(f).split('\n').filter((l) => /^\S/.test(l) && !/^(\/\/|import|export function|function|\}|const \w+ = \(|let |\/\*| \*)/.test(l));
      for (const l of top) expect(l, `${f}: ${l}`).not.toMatch(/\b(document|localStorage|navigator|window)\b/);
    }
  });

  it('is mounted and painted only from syncShellNav, for the help view', () => {
    const mount = fileSource('shell2/mount.js');
    expect(mount).toContain("if (view === 'help') mountHelp(byId('view-help'), deps);");
    expect(mount).toContain('syncHelpView(view);');
    expect(markupHasHelpNode()).toBe(true);
  });
});

function markupHasHelpNode() {
  return shellMarkup().includes('<div id="view-speakers" class="view hidden"></div><div id="view-help" class="view hidden"></div>');
}

// Check for app update answers next to itself, in a live region: the version
// line at the bottom of the sidebar, where the classic answer goes, is out of
// the way and not announced.
describe('the app update check in Help and info', () => {
  it('says what the check found, in the line under the button', () => {
    expect(updateStatus({ state: 'idle' }, t)).toEqual({ kind: '', text: '' });
    expect(updateStatus({ state: 'working' }, t)).toEqual({ kind: 'working', text: t('banner.checkingForAppUpdate') });
    expect(updateStatus({ state: 'current' }, t)).toEqual({ kind: 'done', text: t('banner.appIsCurrent') });
    expect(updateStatus({ state: 'available', version: 'v9.9.9' }, t)).toEqual({ kind: 'done', text: `${t('banner.appUpdateAvail')} v9.9.9` });
    expect(updateStatus({ state: 'failed' }, t)).toEqual({ kind: 'error', text: t('banner.appCheckFailed') });
  });

  it('has a role=status line right after the About buttons', () => {
    const about = sectionOf(page({ update: { state: 'current' } }), 'about');
    const btn = about.indexOf('id="hlpCheckUpdate"');
    const line = about.indexOf('id="hlpUpdateStatus"');
    expect(btn).toBeGreaterThan(-1);
    expect(line).toBeGreaterThan(btn);
    expect(about).toContain(`<p class="hlp-status" id="hlpUpdateStatus" role="status" aria-live="polite" data-kind="done">${t('banner.appIsCurrent')}</p>`);
  });

  it('main.js checkAppUpdate resolves to what it found', () => {
    const main = fileSource('main.js');
    const body = main.slice(main.indexOf('async function checkAppUpdate(manual) {'), main.indexOf('// runAppUpdate downloads'));
    expect(body).toContain("return { outcome: 'current' };");
    expect(body).toContain("return { outcome: 'available', version: m.version };");
    expect(body).toContain("return { outcome: 'failed' };");
  });
});
