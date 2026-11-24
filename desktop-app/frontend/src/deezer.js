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

// deezerPlayError picks the message for a Deezer link that did not start.
export function deezerPlayError(err) {
  switch (agentErrorCode(err)) {
    case 'deezer-not-linked': return ['deezer.linkNotLinked', {}];
    case 'native-service-unavailable': return ['deezer.linkUnavailable', {}];
    case 'deezer-link-unresolved': return ['deezer.linkUnresolved', {}];
    default: return ['deezer.linkFailed', { err: agentErrorText(err) }];
  }
}
