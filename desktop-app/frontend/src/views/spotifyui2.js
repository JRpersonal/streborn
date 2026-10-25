// views/spotifyui2.js: the UI 2.0 preview's part of the Spotify view (#1286).
//
// Two cards on top of the Spotify view, drawn only while the preview shell is
// mounted (spotify.js asks shell2Mounted() before it calls in here), so the
// current layout never sees them:
//
//   - "Put the playing playlist on a key": what the selected speaker plays from
//     Spotify, its six keys, and one named action that stores it through the
//     same save path a long press on a key in the Music view takes
//     (saveSpotifyToSlot). Done is said only after the speaker stored it; every
//     outcome is reported in the card, not as a window-wide toast.
//   - The Spotify login of every STR speaker, from the same /spotify/info read
//     the Music view polls. A login lives on the speaker (whoever picked it in
//     the Spotify app last), so the list is per speaker.
//
// Everything else of the view (how to play, the steps, quality, copying the
// login, the notes) stays below, unchanged.

import { $, escapeHtml, escapeAttr } from '../utils.js';
import { t } from '../i18n/index.js';
import { state } from '../state.js';
import { SpotifyNowPlaying } from '../api.js';
import { parsePresetConflict, presetConflictNote } from '../presetmove.js';
import { saveSpotifyToSlot, presetMoveOffer } from './music/save.js';
import { loadPresets } from './music/presets.js';
import { refreshStatus } from './music/status.js';
import { accountRow, keySlots, spotifyPlaying } from './spotifyui2model.js';

let deps = { strBoxes: () => [] };

// The key picked for the save, per speaker; the last outcome, per speaker.
const chosen = new Map();
const results = new Map();
let busyHost = '';
let keysSig = '';
let ticker = null;
let accounts = { rows: null, busy: false, gen: 0 };

const hostKey = (b) => (b ? `${b.host}:${b.port}` : '');

// speakerName is the selected speaker as the sidebar names it.
function speakerName(box) {
  if (!box) return '';
  const known = ((deps.strBoxes ? deps.strBoxes() : []) || []).find((b) => b.host === box.host && b.port === box.port);
  return (known && known.name) || box.friendlyName || box.name || box.host || '';
}

export function spotifyUi2Markup() {
  return `
    <div class="sp2" id="sp2Root">
      <section class="u-card sp2-card" id="sp2Keys" aria-labelledby="sp2KeysTitle"></section>
      <section class="u-card sp2-card" id="sp2Accounts" aria-labelledby="sp2AcctTitle"></section>
    </div>`;
}

// renderSpotifyUi2 paints both cards and reads fresh: the selected speaker's
// keys and the logins of every speaker. Called on every visit of the view.
export function renderSpotifyUi2(d) {
  deps = { ...deps, ...(d || {}) };
  if (!$('sp2Root')) return;
  paintKeys(true);
  paintAccounts();
  refreshAccounts();
  // The Music view keeps the keys current only while it is open; a hardware
  // save or another app can have changed them since.
  if (state.currentBox) loadPresets().then(() => paintKeys(false)).catch(() => {});
  startTicker();
}

// startTicker follows the shared state while the view is open: the status poll
// keeps what plays current on every view of the shell, and a speaker picked in
// the sidebar changes whose keys these are. Reads state only, asks no speaker.
function startTicker() {
  if (ticker) return;
  ticker = setInterval(() => {
    if (state.view !== 'spotify' || !$('sp2Root')) {
      clearInterval(ticker);
      ticker = null;
      return;
    }
    paintKeys(false);
  }, 1000);
}

// ---------- the playing playlist onto a key ----------

function keysModel() {
  const box = state.currentBox;
  const name = speakerName(box);
  const now = spotifyPlaying(state);
  const key = hostKey(box);
  const slots = box ? keySlots(state.presets, now.uri) : [];
  let slot = chosen.get(key) || 0;
  if (!now.playing) slot = 0;
  // A result is about the playlist it saved: another one playing now drops it.
  let result = results.get(key) || null;
  if (result && now.playing && result.uri && now.uri && result.uri !== now.uri) {
    results.delete(key);
    result = null;
  }
  return { box, key, name, now, slots, slot, busy: !!key && busyHost === key, result };
}

function paintKeys(force) {
  const root = $('sp2Keys');
  if (!root) return;
  const m = keysModel();
  const sig = JSON.stringify({ k: m.key, n: m.name, now: m.now, s: m.slots, slot: m.slot, busy: m.busy, r: m.result });
  if (!force && sig === keysSig) return;
  keysSig = sig;
  const active = document.activeElement;
  const keep = active && root.contains(active) ? (active.id || (active.dataset && active.dataset.slot ? `slot${active.dataset.slot}` : '')) : '';
  root.innerHTML = keysHTML(m);
  root.setAttribute('aria-busy', m.busy ? 'true' : 'false');
  if (keep) {
    const again = keep.startsWith('slot') ? root.querySelector(`.sp2-key[data-slot="${keep.slice(4)}"]`) : root.querySelector(`#${keep}`);
    if (again && !again.disabled) again.focus();
  }
  wireKeys(root, m);
}

function kindLabel(kind) {
  return t(kind ? `ui2.spotify.kind.${kind}` : 'ui2.spotify.kind.other');
}

function keysHTML(m) {
  const title = `<h3 class="sp2-title" id="sp2KeysTitle">${escapeHtml(m.box ? t('ui2.spotify.keysTitle', { name: m.name }) : t('ui2.spotify.keysTitleNoSpeaker'))}</h3>`;
  if (!m.box) return `${title}<p class="sp2-text">${escapeHtml(t('ui2.spotify.noSpeaker'))}</p>`;
  if (!m.now.playing) {
    return `${title}
      <p class="sp2-text sp2-strong">${escapeHtml(t('ui2.spotify.notPlaying', { name: m.name }))}</p>
      <p class="sp2-text sp2-muted">${escapeHtml(t('ui2.spotify.notPlayingHow', { name: m.name }))}</p>
      ${resultHTML(m.result)}`;
  }
  const what = m.now.name || t('ui2.spotify.unnamed');
  const pickedSlot = m.slot ? m.slots.find((s) => s.slot === m.slot) : null;
  let hint = t('ui2.spotify.pickKey');
  if (pickedSlot && pickedSlot.here) hint = t('ui2.spotify.alreadyOnKey', { n: pickedSlot.slot, name: m.name });
  else if (pickedSlot && !pickedSlot.empty) hint = t('ui2.spotify.replaces', { current: pickedSlot.name });
  else if (pickedSlot) hint = t('ui2.spotify.keyIsEmpty');
  const canSave = !!pickedSlot && !pickedSlot.here && !m.busy;
  const btnText = m.busy
    ? t('ui2.spotify.saving', { name: m.name })
    : (pickedSlot ? t('ui2.spotify.saveBtn', { n: pickedSlot.slot, name: m.name }) : t('ui2.spotify.saveBtnNoKey'));
  const keys = m.slots.map((s) => {
    const sub = s.here ? t('ui2.spotify.keyHere') : (s.empty ? t('ui2.spotify.keyEmpty') : (s.spotify ? t('ui2.spotify.keySpotify') : t('ui2.spotify.keyOther')));
    const name = s.empty ? '' : `<span class="sp2-key-name">${escapeHtml(s.name)}</span>`;
    return `<button type="button" class="sp2-key${s.slot === m.slot ? ' is-chosen' : ''}${s.here ? ' is-here' : ''}" data-slot="${s.slot}" aria-pressed="${s.slot === m.slot ? 'true' : 'false'}"${m.busy ? ' disabled' : ''}>
        <span class="sp2-key-n" aria-hidden="true">${s.slot}</span>
        <span class="sp2-key-text"><span class="u-sr-only">${escapeHtml(t('ui2.spotify.keyN', { n: s.slot }))}</span>${name}<span class="sp2-key-sub">${escapeHtml(sub)}</span></span>
      </button>`;
  }).join('');
  return `${title}
    <p class="sp2-now">
      <span class="u-pill u-pill--ok"><span class="u-pill-dot" aria-hidden="true"></span>${escapeHtml(t('ui2.spotify.playingPill'))}</span>
      <span class="sp2-now-text">${escapeHtml(t('ui2.spotify.playingOn', { name: m.name, title: what }))}<span class="sp2-muted"> · ${escapeHtml(kindLabel(m.now.kind))}</span></span>
    </p>
    <p class="sp2-sub" id="sp2KeysLabel">${escapeHtml(t('ui2.spotify.keysLabel', { name: m.name }))}</p>
    <div class="sp2-keys" role="group" aria-labelledby="sp2KeysLabel">${keys}</div>
    <div class="sp2-actions">
      <button type="button" class="u-btn u-btn--primary" id="sp2Save"${canSave ? '' : ' disabled'}>${escapeHtml(btnText)}</button>
      <span class="sp2-hint sp2-muted" id="sp2Hint">${escapeHtml(hint)}</span>
    </div>
    ${resultHTML(m.result)}`;
}

// resultHTML is the outcome of the last save on this speaker, in the card. A
// polite status for done and notes, an alert for a failure.
function resultHTML(r) {
  if (!r) return '<p class="sp2-result" id="sp2Result" role="status" aria-live="polite"></p>';
  const lines = (r.lines || []).map((l) => `<span class="sp2-result-line sp2-result-line--${escapeAttr(l.tone)}">${escapeHtml(l.text)}</span>`).join('');
  const role = r.tone === 'error' ? 'role="alert"' : 'role="status" aria-live="polite"';
  return `<p class="sp2-result sp2-result--${escapeAttr(r.tone)}" id="sp2Result" ${role}>${lines}</p>`;
}

function wireKeys(root, m) {
  root.querySelectorAll('.sp2-key').forEach((btn) => {
    btn.addEventListener('click', () => {
      const slot = parseInt(btn.dataset.slot, 10);
      chosen.set(m.key, chosen.get(m.key) === slot ? 0 : slot);
      paintKeys(true);
    });
  });
  const save = root.querySelector('#sp2Save');
  if (save) save.addEventListener('click', () => { saveToChosenKey(); });
}

// saveToChosenKey stores what the selected speaker plays from Spotify on the
// chosen key, through the Music view's own Spotify save (saveSpotifyToSlot):
// the live context read, the duplicate guard, the login and plan notices. The
// card says done only when that resolved true, which is the speaker having
// stored the key.
export async function saveToChosenKey() {
  const box = state.currentBox;
  const key = hostKey(box);
  const slot = chosen.get(key) || 0;
  if (!box || !slot || busyHost) return false;
  const name = speakerName(box);
  busyHost = key;
  results.delete(key);
  paintKeys(true);
  const startUri = spotifyPlaying(state).uri;
  const lines = [];
  let tone = 'ok';
  const fail = (text) => { tone = 'error'; lines.push({ tone: 'error', text }); };
  let ok = false;
  try {
    // The same refresh the Music view's save starts with: what plays now, not
    // what the last poll saw.
    try { await refreshStatus(); } catch { /* the save reads the speaker itself */ }
    const now = spotifyPlaying(state);
    if (state.currentBox !== box) {
      fail(t('ui2.spotify.speakerChanged', { name }));
    } else if (!now.playing) {
      fail(t('ui2.spotify.stoppedBeforeSave', { name }));
    } else {
      const title = now.name || t('ui2.spotify.unnamed');
      ok = await saveSpotifyToSlot(slot, {
        saved: (savedName) => lines.push({ tone: 'ok', text: t('ui2.spotify.saved', { n: slot, name, title: savedName || title }) }),
        notice: (k) => { if (tone !== 'error') tone = 'warn'; lines.push({ tone: 'warn', text: t(k) }); },
        error: (k) => fail(t(k)),
        failed: (err) => {
          const conflict = parsePresetConflict(err);
          if (conflict) {
            tone = 'warn';
            lines.push({ tone: 'warn', text: presetConflictNote(conflict, t) });
            // The Music view's offer to move it onto the chosen key, on this
            // speaker.
            presetMoveOffer(conflict, slot, box);
          } else {
            fail(t('ui2.spotify.saveFailed', { name, err: String(err) }));
          }
        },
      });
    }
  } catch (err) {
    fail(t('ui2.spotify.saveFailed', { name, err: String(err) }));
  } finally {
    busyHost = '';
  }
  if (!ok && !lines.length) fail(t('ui2.spotify.notConfirmed', { name }));
  results.set(key, { tone, lines, uri: startUri });
  paintKeys(true);
  // The button the user pressed is gone after a repaint when the key now holds
  // it; the result is where the answer is, so the keyboard lands there.
  const res = $('sp2Result');
  if (res && document.activeElement === document.body) {
    res.tabIndex = -1;
    res.focus();
  }
  return ok;
}

// ---------- accounts ----------

// refreshAccounts asks every STR speaker for its Spotify login, all at once,
// offline ones excepted. The answers also feed the names a preset tile draws.
export async function refreshAccounts() {
  const gen = ++accounts.gen;
  const boxes = ((deps.strBoxes ? deps.strBoxes() : []) || []).map((b) => {
    const live = (state.boxes || []).find((x) => x && x.host === b.host && x.port === b.port);
    return { ...b, offline: !!(live && live.offline) };
  });
  accounts.busy = true;
  paintAccounts();
  const replies = await Promise.allSettled(boxes.map((b) => (b.offline ? Promise.resolve(null) : SpotifyNowPlaying(b.host, b.port))));
  if (gen !== accounts.gen) return accounts.rows;
  const names = { ...(state.spotifyAccountNames || {}) };
  replies.forEach((r) => {
    const np = r.status === 'fulfilled' ? r.value : null;
    if (np && np.accountNames && typeof np.accountNames === 'object') Object.assign(names, np.accountNames);
  });
  state.spotifyAccountNames = names;
  accounts.rows = boxes.map((b, i) => accountRow(b, replies[i].status === 'fulfilled' ? replies[i].value : null, names));
  accounts.busy = false;
  paintAccounts();
  return accounts.rows;
}

const PILL = {
  in: ['ok', 'ui2.spotify.acct.pillIn'],
  saved: ['ok', 'ui2.spotify.acct.pillSaved'],
  none: ['warn', 'ui2.spotify.acct.pillNone'],
  unknown: ['muted', 'ui2.spotify.acct.pillUnknown'],
  offline: ['muted', 'ui2.spotify.acct.pillOffline'],
};

function accountText(r) {
  switch (r.state) {
    case 'in': return r.account ? t('ui2.spotify.acct.inAs', { account: r.account }) : t('ui2.spotify.acct.inNameless');
    case 'saved': return t('ui2.spotify.acct.saved');
    case 'none': return t('ui2.spotify.acct.none', { name: r.name });
    case 'offline': return t('ui2.spotify.acct.offline', { name: r.name });
    default: return t('ui2.spotify.acct.unknown', { name: r.name });
  }
}

function paintAccounts() {
  const root = $('sp2Accounts');
  if (!root) return;
  const rows = accounts.rows;
  const active = document.activeElement;
  const hadFocus = !!active && active.id === 'sp2AcctRefresh';
  let list;
  if (!rows) {
    list = `<p class="sp2-text sp2-muted">${escapeHtml(t('ui2.spotify.acct.checking'))}</p>`;
  } else if (!rows.length) {
    list = `<p class="sp2-text sp2-muted">${escapeHtml(t('ui2.spotify.acct.noSpeakers'))}</p>`;
  } else {
    list = `<ul class="sp2-acct-list">${rows.map((r) => {
      const [tone, pill] = PILL[r.state] || PILL.unknown;
      const plan = r.state === 'in' || r.state === 'saved'
        ? (r.plan === 'premium' ? t('ui2.spotify.acct.premium') : (r.plan === 'free' ? t('ui2.spotify.acct.free') : ''))
        : '';
      const others = r.others.length ? `<p class="sp2-acct-line sp2-muted">${escapeHtml(t('ui2.spotify.acct.others', { accounts: r.others.join(', ') }))}</p>` : '';
      return `<li class="sp2-acct${r.state === 'offline' ? ' is-offline' : ''}" data-host="${escapeAttr(r.host)}" data-state="${escapeAttr(r.state)}">
          <div class="sp2-acct-head">
            <span class="sp2-acct-name">${escapeHtml(r.name)}</span>
            <span class="u-pill u-pill--${tone}"><span class="u-pill-dot" aria-hidden="true"></span>${escapeHtml(t(pill))}</span>
          </div>
          <p class="sp2-acct-line">${escapeHtml(accountText(r))}${plan ? `<span class="sp2-acct-plan${r.plan === 'free' ? ' is-free' : ''}"> · ${escapeHtml(plan)}</span>` : ''}</p>
          ${others}
        </li>`;
    }).join('')}</ul>`;
  }
  root.setAttribute('aria-busy', accounts.busy ? 'true' : 'false');
  root.innerHTML = `
    <div class="sp2-head">
      <h3 class="sp2-title" id="sp2AcctTitle">${escapeHtml(t('ui2.spotify.acct.title'))}</h3>
      <button type="button" class="u-btn u-btn--secondary" id="sp2AcctRefresh"${accounts.busy ? ' aria-disabled="true"' : ''}>${escapeHtml(accounts.busy ? t('ui2.spotify.acct.checkingShort') : t('ui2.spotify.acct.refresh'))}</button>
    </div>
    <p class="sp2-text sp2-muted">${escapeHtml(t('ui2.spotify.acct.intro'))}</p>
    ${list}`;
  const btn = root.querySelector('#sp2AcctRefresh');
  if (btn) {
    // Kept enabled while it runs, so the keyboard stays on it.
    btn.addEventListener('click', () => { if (!accounts.busy) refreshAccounts(); });
    if (hadFocus) btn.focus();
  }
}
