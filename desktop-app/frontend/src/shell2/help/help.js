// shell2/help/help.js: Help and info, the one place of the UI 2.0 preview for
// help, wishes, community, support and about (phase 5, docs/UI-2.0.md,
// "Help and info").
//
// Everything the classic footer and the donate rail carry has a place here,
// so the preview hides the footer on every view (shell2.css) and nothing
// disappears: the questions and answers, the diagnostic logs, where to report
// a problem or ask, the wishes, the world map, Recommend STR, the donate
// buttons, the version with What's new, the app update check, the credits,
// the website, the source, the legal pages and the trademark notice. Support
// links live here and nowhere else in the preview: never a banner, never a
// success moment.
//
// The classic layout keeps its header, footer, donate rail and Wishes tab as
// they are; this file only runs with the preview shell mounted.
//
// Pure part: helpModel and helpHTML build the page from plain values (vitest).
// Live part: mountHelp(root, deps) wires one delegated click handler on
// #view-help, once; syncHelpView(view) paints the page when Help and info
// opens. mount.js calls both from syncShellNav, never at import: the view
// modules load before main.js creates the containers (the DOM trap).

import { state } from '../../state.js';
import { t as tr, getLocale } from '../../i18n/index.js';
import { escapeHtml, escapeAttr } from '../../utils.js';
import { BrowserOpenURL, SaveDiagnosticBundle } from '../../api.js';
import { donateButtonsHTML, wireDonateButtons } from '../../donate.js';
import { openShareModal } from '../../share.js';
import { worldMapURL } from '../../worldmapinvite.js';
import { VOTE_URL, SUGGEST_URL } from '../../views/wishes.js';
import { strFaqURL } from '../../views/settings/info.js';
import { imprintURL, privacyURL, releaseNotesURL, repoURL, websiteURL } from './about.js';
import './help.css';

// The sections, top to bottom. Each one is a <section> labelled by its
// heading (hlpHead-<id>).
export const HELP_SECTIONS = Object.freeze(['getHelp', 'wishes', 'community', 'support', 'about']);

const SECTION_TITLE = Object.freeze({
  getHelp: 'ui2.help.getHelp',
  wishes: 'nav.wishes',
  community: 'ui2.help.community',
  support: 'ui2.help.support',
  about: 'ui2.help.about',
});

// helpModel collects what the page shows from plain values.
//   appInfo   AppInfo (version, build, author, githubUrl, websiteUrl)
//   locale    the app's language (the legal pages and the world map follow it)
//   faqUrl    the website FAQ (strFaqURL(), the app's language, when left out)
//   logs      the save-logs state: { state: 'idle' | 'working' | 'done' |
//             'error', path, size, error }
//   update    the last app update check from this page: { state: 'idle' |
//             'working' | 'current' | 'available' | 'failed', version }
export function helpModel({ appInfo, locale, faqUrl, logs, update } = {}) {
  const i = appInfo || {};
  const loc = locale || 'en';
  const repo = repoURL(i.githubUrl);
  return {
    locale: loc,
    version: i.version ? String(i.version) : '',
    build: i.build && i.build !== 'dev' ? String(i.build) : '',
    author: i.author ? String(i.author) : '',
    urls: {
      faq: faqUrl || strFaqURL(),
      issues: `${repo}/issues`,
      discussions: `${repo}/discussions`,
      suggest: SUGGEST_URL,
      vote: VOTE_URL,
      worldMap: worldMapURL(loc),
      releaseNotes: releaseNotesURL(i),
      website: websiteURL(i),
      source: repo,
      privacy: privacyURL(loc),
      imprint: imprintURL(loc),
    },
    logs: logs || { state: 'idle' },
    update: update || { state: 'idle' },
  };
}

// updateStatus is the line under Check for app update: the answer the check
// gave, said next to the button that asked (the version line at the bottom of
// the sidebar is not a live region and sits out of the way).
export function updateStatus(update, t) {
  const u = update || {};
  if (u.state === 'working') return { kind: 'working', text: t('banner.checkingForAppUpdate') };
  if (u.state === 'current') return { kind: 'done', text: t('banner.appIsCurrent') };
  if (u.state === 'available' && u.version) return { kind: 'done', text: `${t('banner.appUpdateAvail')} ${u.version}` };
  if (u.state === 'failed') return { kind: 'error', text: t('banner.appCheckFailed') };
  return { kind: '', text: '' };
}

// logsStatus is the line under Save diagnostic logs: what is happening, where
// the file went, or why it failed. A cancelled save dialog shows nothing (the
// person knows what they did).
export function logsStatus(logs, t) {
  const l = logs || {};
  if (l.state === 'working') return { kind: 'working', text: t('ui2.help.saving') };
  if (l.state === 'done' && l.path) return { kind: 'done', text: t('footer.saveLogsDone', { path: l.path, size: l.size || 0 }) };
  if (l.state === 'error') return { kind: 'error', text: t('ui2.help.saveFailed', { error: l.error || '' }) };
  return { kind: '', text: '' };
}

// A button that opens a page in the browser.
function openBtn(url, label, extra = '') {
  return `<button type="button" class="u-btn u-btn--secondary hlp-btn" data-act="open" data-url="${escapeAttr(url)}"${extra}>${escapeHtml(label)}</button>`;
}

// A quiet text link (About's row of links, the pointer line).
function linkBtn(attrs, label) {
  return `<button type="button" class="hlp-link" ${attrs}>${escapeHtml(label)}</button>`;
}

function section(id, t, body) {
  return `<section class="hlp-sec" data-section="${id}" aria-labelledby="hlpHead-${id}">`
    + `<h3 class="hlp-sec-title" id="hlpHead-${id}">${escapeHtml(t(SECTION_TITLE[id]))}</h3>`
    + body
    + '</section>';
}

// A sentence with one link inside it: the {{speakers}} placeholder becomes the
// link, wherever the language puts it.
function sentenceWithLink(text, linkHTML) {
  const MARK = '\u0001';
  const parts = text.split(MARK);
  if (parts.length < 2) return `${escapeHtml(text)} ${linkHTML}`;
  return escapeHtml(parts[0]) + linkHTML + escapeHtml(parts.slice(1).join(''));
}

function getHelpHTML(m, t) {
  const st = logsStatus(m.logs, t);
  const busy = m.logs && m.logs.state === 'working';
  const pointer = sentenceWithLink(
    t('ui2.help.oneSpeaker', { speakers: '\u0001', tab: t('ui2.speakers.tab.maintenance') }),
    linkBtn('data-act="view" data-view="speakers"', t('ui2.nav.speakers')),
  );
  return section('getHelp', t, '<ul class="hlp-rows">'
    + `<li class="hlp-row">${openBtn(m.urls.faq, t('ui2.help.faq'), ' data-site="1"')}</li>`
    + '<li class="hlp-row">'
    + `<button type="button" class="u-btn u-btn--secondary hlp-btn" data-act="saveLogs" id="hlpSaveLogs" aria-describedby="hlpSaveLogsHint"${busy ? ' aria-disabled="true"' : ''}>${escapeHtml(t('footer.saveLogs'))}</button>`
    + `<p class="hlp-hint" id="hlpSaveLogsHint">${escapeHtml(t('footer.saveLogsHint'))}</p>`
    + `<p class="hlp-status" id="hlpLogsStatus" role="status" aria-live="polite" data-kind="${st.kind}">${escapeHtml(st.text)}</p>`
    + '</li>'
    + `<li class="hlp-row">${openBtn(m.urls.issues, t('footer.reportProblem'))}</li>`
    + `<li class="hlp-row">${openBtn(m.urls.discussions, t('ui2.help.ask'))}</li>`
    + '</ul>'
    + `<p class="hlp-pointer">${pointer}</p>`);
}

function wishesHTML(m, t) {
  return section('wishes', t, `<p class="hlp-lead">${escapeHtml(t('ui2.help.wishesLead'))}</p>`
    + '<div class="hlp-actions">'
    + `<button type="button" class="u-btn u-btn--primary hlp-btn" data-act="view" data-view="wishes">${escapeHtml(t('ui2.help.openWishes'))}</button>`
    + openBtn(m.urls.suggest, t('wishes.suggestBtn'))
    + openBtn(m.urls.vote, t('footer.voteIdeas'))
    + '</div>');
}

function communityHTML(m, t) {
  return section('community', t, '<ul class="hlp-rows">'
    + '<li class="hlp-row">'
    + `<button type="button" class="u-btn u-btn--secondary hlp-btn" data-act="worldMap" data-url="${escapeAttr(m.urls.worldMap)}" aria-describedby="hlpWorldMapLead">${escapeHtml(t('footer.worldMap'))}</button>`
    + `<p class="hlp-hint" id="hlpWorldMapLead">${escapeHtml(t('worldMap.pinPrompt'))}</p>`
    + '</li>'
    + `<li class="hlp-row"><button type="button" class="u-btn u-btn--secondary hlp-btn" data-act="share">${escapeHtml(t('share.menu'))}</button></li>`
    + '</ul>');
}

// Support STR: a plain statement of how the project is funded, then the three
// buttons. No slogan, no plea; this section is the only place in the preview
// that shows them.
function supportHTML(m, t) {
  return section('support', t, `<p class="hlp-lead">${escapeHtml(t('ui2.help.supportLead'))}</p>`
    + `<div class="hlp-donate">${donateButtonsHTML()}</div>`);
}

function aboutHTML(m, t) {
  const up = updateStatus(m.update, t);
  const version = m.version ? escapeHtml(t('setup.versionLabel', { version: m.version })) : '';
  const build = m.build ? ` <span class="hlp-build">(Build ${escapeHtml(m.build)})</span>` : '';
  const whatsNew = linkBtn(`data-act="open" data-url="${escapeAttr(m.urls.releaseNotes)}"`, t('banner.whatsNew'));
  const links = [
    linkBtn(`data-act="open" data-site="1" data-url="${escapeAttr(m.urls.website)}"`, t('footer.website')),
    linkBtn(`data-act="open" data-url="${escapeAttr(m.urls.source)}"`, t('ui2.help.source')),
    linkBtn(`data-act="open" data-site="1" data-url="${escapeAttr(m.urls.privacy)}"`, t('ui2.help.privacy')),
    linkBtn(`data-act="open" data-site="1" data-url="${escapeAttr(m.urls.imprint)}"`, t('ui2.help.imprint')),
  ];
  return section('about', t, `<p class="hlp-version">${version}${build}${version ? ' <span aria-hidden="true">&middot;</span> ' : ''}${whatsNew}</p>`
    + '<div class="hlp-actions">'
    + `<button type="button" class="u-btn u-btn--secondary hlp-btn" data-act="checkUpdate" id="hlpCheckUpdate">${escapeHtml(t('banner.checkForAppUpdate'))}</button>`
    + `<button type="button" class="u-btn u-btn--secondary hlp-btn" data-act="credits">${escapeHtml(t('footer.credits'))}</button>`
    + '</div>'
    + `<p class="hlp-status" id="hlpUpdateStatus" role="status" aria-live="polite" data-kind="${up.kind}">${escapeHtml(up.text)}</p>`
    + `<ul class="hlp-links">${links.map((l) => `<li>${l}</li>`).join('')}</ul>`
    + `<p class="hlp-author">ST Reborn${m.author ? ` <span aria-hidden="true">&middot;</span> ${escapeHtml(m.author)}` : ''}</p>`
    + `<p class="hlp-fine">${escapeHtml(t('footer.fine'))}</p>`
    + `<p class="hlp-trademark" id="hlpTrademark">${escapeHtml(t('about.trademark'))}</p>`);
}

const RENDER = { getHelp: getHelpHTML, wishes: wishesHTML, community: communityHTML, support: supportHTML, about: aboutHTML };

// helpHTML is the whole page: the title, the lead and the sections in
// HELP_SECTIONS order.
export function helpHTML(model, t) {
  const m = model || helpModel();
  return '<div class="hlp">'
    + '<header class="hlp-head">'
    + `<h2 class="hlp-title" id="hlpTitle">${escapeHtml(t('ui2.help.title'))}</h2>`
    + `<p class="hlp-lead">${escapeHtml(t('ui2.help.lead'))}</p>`
    + '</header>'
    + HELP_SECTIONS.map((id) => RENDER[id](m, t)).join('')
    + '</div>';
}

// ---- live part: only ever called with the shell mounted -------------------

let root = null;
let deps = {};
let logs = { state: 'idle' };
let update = { state: 'idle' };
let checking = false;

const currentModel = () => helpModel({
  appInfo: state.appInfo,
  locale: getLocale(),
  faqUrl: strFaqURL(),
  logs,
  update,
});

function open(url, site) {
  if (!url) return;
  const target = site && typeof deps.withAppReferrer === 'function' ? deps.withAppReferrer(url, 'help') : url;
  try { BrowserOpenURL(target); } catch (_) { /* no runtime */ }
}

function paint() {
  if (!root) return;
  root.innerHTML = helpHTML(currentModel(), tr);
  wireDonateButtons(root.querySelector('[data-section="support"]'));
}

// paintLogs updates the status line and the button only, so the keyboard
// stays on the button while the save runs.
function paintLogs() {
  if (!root) return;
  const st = logsStatus(logs, tr);
  const line = root.querySelector('#hlpLogsStatus');
  if (line) {
    line.textContent = st.text;
    line.dataset.kind = st.kind;
  }
  const btn = root.querySelector('#hlpSaveLogs');
  if (btn) {
    if (logs.state === 'working') btn.setAttribute('aria-disabled', 'true');
    else btn.removeAttribute('aria-disabled');
  }
}

async function saveLogs() {
  if (logs.state === 'working') return;
  logs = { state: 'working' };
  paintLogs();
  try {
    // Every speaker, as the footer link does.
    const hosts = (state.boxes || []).map((b) => b && b.host).filter(Boolean);
    const res = await SaveDiagnosticBundle(hosts, true);
    // A cancelled save dialog comes back without a path: nothing to say.
    logs = res && res.savePath
      ? { state: 'done', path: res.savePath, size: Math.round((res.bytes || 0) / 1024) }
      : { state: 'idle' };
  } catch (err) {
    logs = { state: 'error', error: String(err) };
  }
  paintLogs();
}

// paintUpdate updates the line under Check for app update only.
function paintUpdate() {
  if (!root) return;
  const st = updateStatus(update, tr);
  const line = root.querySelector('#hlpUpdateStatus');
  if (!line) return;
  // The same words again are news too (a second check): clear first.
  line.textContent = '';
  line.dataset.kind = st.kind;
  if (st.text) setTimeout(() => { if (line.isConnected) line.textContent = st.text; }, 30);
}

// checkUpdate runs the app's own manual check. Its answer appears where it
// always does (the version line at the bottom of the sidebar, or the notice
// on top) and in the line under this button, which is a live region. While
// it runs the button says so.
function checkUpdate(btn) {
  if (checking || typeof deps.checkAppUpdate !== 'function') return;
  checking = true;
  const label = tr('banner.checkForAppUpdate');
  btn.setAttribute('aria-disabled', 'true');
  btn.textContent = tr('banner.checkingForAppUpdate');
  update = { state: 'working' };
  paintUpdate();
  const done = (res) => {
    checking = false;
    const r = res && typeof res === 'object' ? res : null;
    update = r && ['current', 'available', 'failed'].includes(r.outcome)
      ? { state: r.outcome, version: r.version ? String(r.version) : '' }
      : { state: 'idle' };
    const again = root && root.querySelector('#hlpCheckUpdate');
    if (again) {
      again.removeAttribute('aria-disabled');
      again.textContent = label;
    }
    paintUpdate();
  };
  Promise.resolve().then(() => deps.checkAppUpdate(true)).then(done, () => done({ outcome: 'failed' }));
}

function onClick(e) {
  const btn = e.target.closest('[data-act]');
  if (!btn || !root || !root.contains(btn)) return;
  const act = btn.dataset.act;
  if (act === 'open') open(btn.dataset.url, btn.dataset.site === '1');
  else if (act === 'worldMap') open(worldMapURL(), false);
  else if (act === 'view' && typeof deps.switchView === 'function') deps.switchView(btn.dataset.view);
  else if (act === 'saveLogs') saveLogs();
  else if (act === 'checkUpdate') checkUpdate(btn);
  else if (act === 'credits' && typeof deps.showCredits === 'function') deps.showCredits();
  else if (act === 'share') openShareModal();
}

// mountHelp wires #view-help once. Returns whether it is mounted.
export function mountHelp(el, d) {
  if (root) return true;
  if (!el || typeof el.addEventListener !== 'function') return false;
  root = el;
  deps = d || {};
  root.addEventListener('click', onClick);
  return true;
}

// syncHelpView paints the page each time Help and info opens, so the version
// and the links follow what the app knows by then. The save-logs line keeps
// its state across visits.
export function syncHelpView(view) {
  if (view !== 'help' || !root) return;
  paint();
}
