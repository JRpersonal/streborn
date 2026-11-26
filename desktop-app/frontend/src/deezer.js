// Deezer on the speaker's own Deezer client (docs/streaming/deezer.md). A new
// sign-in is not possible without Bose; a login the speaker already had keeps
// working.
// The agent's GET /api/deezer/account answer is turned into what the Deezer
// card in the Spotify view shows, and a pasted Deezer link is recognised for
// the station search. Pure functions only, so the rules are unit-tested
// (deezer.test.js).

import { agentErrorCode, agentErrorText } from './usservices.js';

// The states the agent reports (internal/webui/deezer.go deezerState). An
// older agent also reports 'failed', for a new login that did not work.
const STATES = new Set(['connected', 'connecting', 'failed', 'unavailable', 'not-connected', 'unknown']);

// deezerView reads the agent's answer. An answer without a known state (an
// older agent, no answer at all) reads as 'unknown'. stuck: the speaker's
// Deezer client hangs until a power cycle (the agent says so, or an older
// agent's last login answered 1024 or not at all). rejected: Deezer refused
// the kept login (the firmware's retry code 3330).
export function deezerView(status) {
  const s = status && typeof status === 'object' ? status : {};
  const state = STATES.has(s.state) ? s.state : 'unknown';
  const reason = String(s.reason || '');
  const detail = String(s.detail || '');
  const oldStuck = state === 'failed' && (reason === 'no-answer' || /\b1024\b/.test(detail));
  return {
    state,
    reason,
    detail,
    account: String(s.account || '').trim(),
    stuck: s.stuck === true || oldStuck,
    rejected: s.rejected === true && state !== 'connected',
    recreated: !!(s.recreate && s.recreate.ran === true),
  };
}

// deezerStatusLine picks the i18n key and its parameters for the status line.
export function deezerStatusLine(v) {
  const view = v || deezerView(null);
  const named = !!view.account;
  const p = { account: view.account, detail: view.detail };
  switch (view.state) {
    case 'connected': return [named ? 'deezer.state.connected' : 'deezer.state.connectedNoName', p];
    case 'connecting': return ['deezer.state.connecting', p];
    case 'failed':
      if (view.reason === 'refused') return ['deezer.state.refused', p];
      if (view.reason === 'no-answer') return ['deezer.state.noAnswer', p];
      return ['deezer.state.notReady', p];
    case 'unavailable': return [named ? 'deezer.state.unavailable' : 'deezer.state.unavailableNoName', p];
    case 'not-connected': return ['deezer.state.notConnected', p];
    default: return ['deezer.state.unknown', p];
  }
}

// deezerPill is the pill tone and its i18n key.
export function deezerPill(v) {
  switch (v && v.state) {
    case 'connected': return ['ok', 'deezer.pill.connected'];
    case 'connecting': return ['muted', 'deezer.pill.connecting'];
    case 'failed': return ['warn', 'deezer.pill.failed'];
    case 'unavailable': return ['warn', 'deezer.pill.unavailable'];
    case 'not-connected': return ['muted', 'deezer.pill.notConnected'];
    default: return ['muted', 'deezer.pill.unknown'];
  }
}

// deezerCanDisconnect says whether the card offers Disconnect: whenever the
// speaker holds a Deezer account, or STR handed one back.
export function deezerCanDisconnect(v) {
  return !!v && (v.state === 'connected' || v.state === 'connecting' || v.state === 'failed' || v.state === 'unavailable');
}

// deezerRefreshDelays are the re-reads after a Check again that handed the
// kept login back: the speaker re-creates its account after the call
// returns, so the card asks again instead of guessing. The last one lies
// past the agent's 90 s "connecting" span, so the card ends on a verdict.
export const deezerRefreshDelays = [6000, 30000, 95000];

// deezerDisconnectError picks the message for a failed Disconnect.
export function deezerDisconnectError(err) {
  if (agentErrorCode(err) === 'still-linked') return ['usServices.disconnectStillLinked', {}];
  return ['usServices.disconnectFailed', { err: agentErrorText(err) }];
}

// The hosts of a canonical link and of Deezer's share links. The agent
// checks the link again and follows a share link itself; this only decides
// that the search field holds a Deezer link and not a stream address.
const CANONICAL = /^https?:\/\/(?:www\.)?deezer\.com\/(?:[a-z]{2}(?:-[a-z]{2})?\/)?(track|album|playlist)\/(\d{1,20})\/?(?:[?#].*)?$/i;
const SHORT = /^https:\/\/(?:link\.deezer\.com|deezer\.page\.link|dzr\.page\.link|deezer\.app\.link|deezer\.onelink\.me)\/\S+$/i;

// parseDeezerLink returns { kind, id } for a canonical link, { kind: '',
// id: '', short: true } for a share link, null for anything else.
export function parseDeezerLink(q) {
  const raw = String(q == null ? '' : q).trim();
  const m = CANONICAL.exec(raw);
  if (m) return { kind: m[1].toLowerCase(), id: m[2], short: false };
  if (SHORT.test(raw)) return { kind: '', id: '', short: true };
  return null;
}

// isDeezerLink says whether the search input is a Deezer link.
export function isDeezerLink(q) {
  return parseDeezerLink(q) !== null;
}

// deezerLinkKindKey is the i18n key naming what a link holds.
export function deezerLinkKindKey(link) {
  switch (link && link.kind) {
    case 'album': return 'deezer.kind.album';
    case 'track': return 'deezer.kind.track';
    case 'playlist': return 'deezer.kind.playlist';
    default: return 'deezer.kind.link';
  }
}

// ---- The Deezer logins of the home (GET /api/deezer/household) -------------
// The overview names every STR speaker of the home, its Deezer state and the
// login STR keeps for it, never a login or an account. A login goes to another
// speaker only on the owner's Copy (POST /api/deezer/copy), and an older
// backed-up one comes back only on "Use the older login" (POST
// /api/deezer/use-older). docs/streaming/deezer.md.

const HOME_STATES = new Set(['works', 'connecting', 'no-login', 'refused', 'not-set-up', 'unknown']);
const KEPT_FROM = {
  'sources-file': 'deezer.home.kept.own',
  registration: 'deezer.home.kept.registration',
  peer: 'deezer.home.kept.copied',
  restored: 'deezer.home.kept.restored',
};

function homeRow(r) {
  const o = r && typeof r === 'object' ? r : {};
  const previous = Math.max(0, Math.min(99, Number.parseInt(o.previous, 10) || 0));
  return {
    id: String(o.id || ''),
    name: String(o.name || '').trim(),
    self: o.self === true,
    state: HOME_STATES.has(o.state) ? o.state : 'unknown',
    why: String(o.why || ''),
    kept: o.kept === true,
    from: String(o.from || ''),
    fromName: String(o.fromName || '').trim(),
    keptAt: String(o.keptAt || ''),
    previous,
    account: o.account === 'same' || o.account === 'other' ? o.account : '',
    canCopy: o.canCopy === true && o.self !== true && !!o.id,
    canUseOlder: o.canUseOlder === true && o.self === true,
  };
}

// deezerHousehold reads the agent's answer: rows (this speaker first) and how
// many speakers a copy to all would reach. An older agent answers nothing.
export function deezerHousehold(raw) {
  const o = raw && typeof raw === 'object' ? raw : {};
  const rows = Array.isArray(o.speakers) ? o.speakers.map(homeRow) : [];
  return { rows, canCopy: rows.filter((r) => r.canCopy).length };
}

// deezerHomeVisible: the list shows when there is another speaker, or when
// this speaker keeps a login or can put an older one back.
export function deezerHomeVisible(hh) {
  const rows = (hh && hh.rows) || [];
  if (rows.length > 1) return true;
  return rows.some((r) => r.self && (r.kept || r.canUseOlder || r.previous > 0));
}

// deezerShowCopyAll: a separate "Copy to all" only when it reaches more than
// one speaker; for one, that row's own button does it.
export function deezerShowCopyAll(hh) {
  return !!hh && hh.canCopy > 1;
}

// deezerRowState is the i18n key for a row's state.
export function deezerRowState(row) {
  switch (row && row.state) {
    case 'works': return 'deezer.home.state.works';
    case 'connecting': return 'deezer.home.state.connecting';
    case 'no-login': return 'deezer.home.state.noLogin';
    case 'refused': return 'deezer.home.state.refused';
    case 'not-set-up': return 'deezer.home.state.notSetUp';
    default: return row && row.why === 'old-agent' ? 'deezer.home.state.oldAgent' : 'deezer.home.state.unknown';
  }
}

// deezerKeptDate formats the agent's timestamp as a local date, '' for none.
export function deezerKeptDate(iso, locale) {
  const ms = Date.parse(String(iso || ''));
  if (!Number.isFinite(ms) || ms <= 0) return '';
  try {
    return new Date(ms).toLocaleDateString(locale || undefined, { year: 'numeric', month: 'short', day: 'numeric' });
  } catch {
    return new Date(ms).toISOString().slice(0, 10);
  }
}

// deezerRowDetails lists a row's detail segments as [key, params]: the kept
// login and its origin, the backed-up older logins, and the account. date
// turns the agent's timestamp into a short local date ('' when unknown).
export function deezerRowDetails(row, date) {
  if (!row || row.state === 'unknown') return [];
  const out = [];
  if (row.kept) {
    const when = typeof date === 'function' ? date(row.keptAt) : '';
    const key = KEPT_FROM[row.from];
    if (key && when) out.push([key, { date: when, name: row.fromName || '?' }]);
    else out.push(['deezer.home.kept.yes', {}]);
  } else {
    out.push(['deezer.home.kept.none', {}]);
  }
  if (row.previous === 1) out.push(['deezer.home.older.one', {}]);
  else if (row.previous > 1) out.push(['deezer.home.older.many', { n: row.previous }]);
  if (!row.self && row.account === 'same') out.push(['deezer.home.account.same', {}]);
  if (!row.self && row.account === 'other') out.push(['deezer.home.account.other', {}]);
  return out;
}

// deezerCopyMessages turns a copy's answer into sentences, [key, params]
// each. all: a copy to all reports only the speakers that took the login or
// refused it; the others never needed it.
export function deezerCopyMessages(answer, all) {
  const results = answer && Array.isArray(answer.results) ? answer.results : [];
  const out = [];
  for (const r of results) {
    const name = String((r && r.name) || '').trim() || '?';
    const outcome = String((r && r.outcome) || '');
    if (outcome === 'accepted') out.push(['deezer.home.result.copied', { name }]);
    else if (outcome.startsWith('refused:')) out.push(['deezer.home.result.refused', { name, reason: outcome.slice(8) }]);
    else if (all) continue;
    else if (outcome.startsWith('not-needed:')) out.push(['deezer.home.result.notNeeded', { name }]);
    else if (outcome === 'hash-mismatch') out.push(['deezer.home.result.otherAccount', { name }]);
    else if (outcome === 'old-agent') out.push(['deezer.home.result.oldAgent', { name }]);
    else out.push(['deezer.home.result.unreachable', { name }]);
  }
  if (!out.length) out.push(['deezer.home.result.none', {}]);
  return out;
}

// deezerCopyOk says whether a copy's answer is only good news.
export function deezerCopyOk(answer) {
  const results = answer && Array.isArray(answer.results) ? answer.results : [];
  return Number(answer && answer.copied) > 0 && results.every((r) => !String((r && r.outcome) || '').startsWith('refused:'));
}

// deezerPlayError picks the message for a Deezer link that did not start.
export function deezerPlayError(err) {
  switch (agentErrorCode(err)) {
    case 'deezer-not-linked': return ['deezer.linkNotLinked', {}];
    case 'native-service-unavailable': return ['deezer.linkUnavailable', {}];
    case 'deezer-link-unresolved': return ['deezer.linkUnresolved', {}];
    default: return ['deezer.linkFailed', { err: agentErrorText(err) }];
  }
}
