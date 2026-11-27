import { describe, it, expect } from 'vitest';
import {
  deezerView, deezerStatusLine, deezerPill, deezerCanDisconnect,
  deezerRefreshDelays, deezerDisconnectError,
  parseDeezerLink, isDeezerLink, deezerLinkKindKey, deezerPlayError,
  deezerHousehold, deezerHomeVisible, deezerShowCopyAll, deezerRowState, deezerRowDetails,
  deezerKeptDate, deezerCopyMessages, deezerCopyOk,
} from './deezer.js';
import en from './i18n/bundles/en.json';
import de from './i18n/bundles/de.json';

describe('Deezer card state', () => {
  it('reads the agent answer and falls back to unknown', () => {
    expect(deezerView(null).state).toBe('unknown');
    expect(deezerView({ state: 'bogus' }).state).toBe('unknown');
    const v = deezerView({ state: 'connected', account: ' us***@example.com ' });
    expect(v).toEqual({
      state: 'connected', reason: '', detail: '', account: 'us***@example.com',
      stuck: false, rejected: false, recreated: false,
    });
  });

  it('picks a status line per state and reason', () => {
    expect(deezerStatusLine(deezerView({ state: 'connected', account: 'a' }))[0]).toBe('deezer.state.connected');
    expect(deezerStatusLine(deezerView({ state: 'connected' }))[0]).toBe('deezer.state.connectedNoName');
    expect(deezerStatusLine(deezerView({ state: 'connecting' }))[0]).toBe('deezer.state.connecting');
    expect(deezerStatusLine(deezerView({ state: 'failed', reason: 'refused', detail: '1005 X' }))).toEqual(
      ['deezer.state.refused', { account: '', detail: '1005 X' }]);
    expect(deezerStatusLine(deezerView({ state: 'failed', reason: 'no-answer' }))[0]).toBe('deezer.state.noAnswer');
    expect(deezerStatusLine(deezerView({ state: 'failed', reason: 'not-ready' }))[0]).toBe('deezer.state.notReady');
    expect(deezerStatusLine(deezerView({ state: 'unavailable', account: 'a' }))[0]).toBe('deezer.state.unavailable');
    expect(deezerStatusLine(deezerView({ state: 'unavailable' }))[0]).toBe('deezer.state.unavailableNoName');
    expect(deezerStatusLine(deezerView({ state: 'not-connected' }))[0]).toBe('deezer.state.notConnected');
    expect(deezerStatusLine(null)[0]).toBe('deezer.state.unknown');
  });

  it('reads the stuck, rejected and recreate signals', () => {
    expect(deezerView({ state: 'unavailable', stuck: true }).stuck).toBe(true);
    // An older agent: its last login answered 1024 or not at all.
    expect(deezerView({ state: 'failed', reason: 'refused', detail: '1024 SOURCE_ALREADY_EXISTS' }).stuck).toBe(true);
    expect(deezerView({ state: 'failed', reason: 'no-answer' }).stuck).toBe(true);
    expect(deezerView({ state: 'failed', reason: 'refused', detail: '1005 X' }).stuck).toBe(false);
    expect(deezerView({ state: 'unavailable', rejected: true }).rejected).toBe(true);
    expect(deezerView({ state: 'connected', rejected: true }).rejected).toBe(false);
    expect(deezerView({ state: 'connecting', recreate: { ran: true } }).recreated).toBe(true);
    expect(deezerView({ state: 'unavailable', recreate: { ran: false, skipped: 'ran-recently' } }).recreated).toBe(false);
  });

  it('offers Disconnect where the speaker holds an account', () => {
    const at = (state) => deezerView({ state });
    expect(deezerCanDisconnect(at('connected'))).toBe(true);
    expect(deezerCanDisconnect(at('failed'))).toBe(true);
    expect(deezerCanDisconnect(at('unavailable'))).toBe(true);
    expect(deezerCanDisconnect(at('not-connected'))).toBe(false);
    expect(deezerCanDisconnect(at('unknown'))).toBe(false);
    expect(deezerPill(at('connected'))[0]).toBe('ok');
    expect(deezerPill(at('failed'))[0]).toBe('warn');
  });

  it('reads the card again past the agent connecting span', () => {
    expect(Math.max(...deezerRefreshDelays)).toBeGreaterThan(90000);
  });

  it('turns agent refusals into sentences', () => {
    expect(deezerDisconnectError(new Error('{"code":"still-linked"}'))[0]).toBe('usServices.disconnectStillLinked');
  });
});

describe('Deezer links', () => {
  it('reads canonical links', () => {
    expect(parseDeezerLink('https://www.deezer.com/en/album/376222197')).toEqual({ kind: 'album', id: '376222197', short: false });
    expect(parseDeezerLink(' https://deezer.com/playlist/123?utm=x ')).toEqual({ kind: 'playlist', id: '123', short: false });
    expect(parseDeezerLink('https://www.deezer.com/pt-br/track/9#t')).toEqual({ kind: 'track', id: '9', short: false });
  });

  it('knows share links and nothing else', () => {
    expect(parseDeezerLink('https://link.deezer.com/s/30abcdEF')).toEqual({ kind: '', id: '', short: true });
    expect(isDeezerLink('https://deezer.page.link/xyz')).toBe(true);
    for (const q of ['', 'Radio Bob', 'https://stream.example.com/live.mp3', 'https://www.deezer.com/en/artist/27',
      'https://www.deezer.com.example.com/album/1', 'http://link.deezer.com/s/x']) {
      expect(isDeezerLink(q)).toBe(false);
    }
  });

  it('names what a link holds and why it did not play', () => {
    expect(deezerLinkKindKey({ kind: 'album' })).toBe('deezer.kind.album');
    expect(deezerLinkKindKey({ kind: '' })).toBe('deezer.kind.link');
    expect(deezerPlayError(new Error('{"code":"deezer-not-linked"}'))[0]).toBe('deezer.linkNotLinked');
    expect(deezerPlayError(new Error('{"code":"native-service-unavailable"}'))[0]).toBe('deezer.linkUnavailable');
    expect(deezerPlayError(new Error('{"code":"deezer-link-unresolved"}'))[0]).toBe('deezer.linkUnresolved');
    expect(deezerPlayError(new Error('{"error":"boom","code":"select-failed"}'))).toEqual(['deezer.linkFailed', { err: 'boom' }]);
  });
});

describe('Deezer logins of the home', () => {
  it('reads the overview and trusts no button the agent did not allow', () => {
    const hh = deezerHousehold({
      speakers: [
        { id: '', name: ' Kitchen ', self: true, state: 'works', kept: true, canCopy: true, canUseOlder: true, previous: '2' },
        { id: '192.0.2.5:8888', name: 'Bedroom', state: 'no-login', canCopy: true, account: 'same' },
        { id: '', name: 'NoId', state: 'no-login', canCopy: true },
        { id: 'x', name: 'Odd', state: 'bogus', account: 'maybe', canUseOlder: true },
      ],
    });
    expect(hh.rows[0]).toMatchObject({ name: 'Kitchen', self: true, canCopy: false, canUseOlder: true, previous: 2 });
    expect(hh.rows[1]).toMatchObject({ canCopy: true, account: 'same' });
    expect(hh.rows[2].canCopy).toBe(false);
    expect(hh.rows[3]).toMatchObject({ state: 'unknown', account: '', canUseOlder: false });
    expect(hh.canCopy).toBe(1);
    expect(deezerHousehold(undefined)).toEqual({ rows: [], canCopy: 0 });
  });

  it('shows the list only when there is something to show', () => {
    const one = (r) => deezerHousehold({ speakers: [{ self: true, state: 'works', ...r }] });
    expect(deezerHomeVisible(one({}))).toBe(false);
    expect(deezerHomeVisible(one({ kept: true }))).toBe(true);
    expect(deezerHomeVisible(one({ previous: 1 }))).toBe(true);
    expect(deezerHomeVisible(deezerHousehold({ speakers: [{ self: true }, { id: 'a' }] }))).toBe(true);
    expect(deezerShowCopyAll({ canCopy: 1 })).toBe(false);
    expect(deezerShowCopyAll({ canCopy: 2 })).toBe(true);
  });

  it('names states, origins, backups and accounts', () => {
    const row = (r) => deezerHousehold({ speakers: [r] }).rows[0];
    expect(deezerRowState(row({ state: 'refused' }))).toBe('deezer.home.state.refused');
    expect(deezerRowState(row({ state: 'unknown', why: 'old-agent' }))).toBe('deezer.home.state.oldAgent');
    expect(deezerRowState(row({ state: 'unknown', why: 'unreachable' }))).toBe('deezer.home.state.unknown');
    const date = () => '1 Dec';
    expect(deezerRowDetails(row({ state: 'works', kept: true, from: 'peer', fromName: 'Hall', keptAt: 'x', previous: 3, account: 'same' }), date)).toEqual([
      ['deezer.home.kept.copied', { date: '1 Dec', name: 'Hall' }],
      ['deezer.home.older.many', { n: 3 }],
      ['deezer.home.account.same', {}],
    ]);
    expect(deezerRowDetails(row({ self: true, state: 'refused', kept: true, from: 'restored', previous: 1, account: 'same' }), date)).toEqual([
      ['deezer.home.kept.restored', { date: '1 Dec', name: '?' }],
      ['deezer.home.older.one', {}],
    ]);
    expect(deezerRowDetails(row({ state: 'no-login', account: 'other' }), date)).toEqual([
      ['deezer.home.kept.none', {}], ['deezer.home.account.other', {}],
    ]);
    // No date, or an origin this build does not know: just "keeps a login".
    expect(deezerRowDetails(row({ state: 'works', kept: true, from: 'sources-file' }), () => '')[0][0]).toBe('deezer.home.kept.yes');
    expect(deezerRowDetails(row({ state: 'works', kept: true, from: 'future' }), date)[0][0]).toBe('deezer.home.kept.yes');
    expect(deezerRowDetails(row({ state: 'unknown' }), date)).toEqual([]);
    expect(deezerKeptDate('')).toBe('');
    expect(deezerKeptDate('0001-01-01T00:00:00Z')).toBe('');
    expect(deezerKeptDate('2026-11-24T10:00:00Z', 'en')).toContain('2026');
  });

  it('turns a copy into sentences', () => {
    const ans = { copied: 1, results: [
      { name: 'Bedroom', outcome: 'accepted' },
      { name: 'Office', outcome: 'refused:kept' },
      { name: 'Garage', outcome: 'hash-mismatch' },
      { name: 'Hall', outcome: 'not-needed:works' },
      { name: 'Porch', outcome: 'unreachable' },
      { name: 'Attic', outcome: 'old-agent' },
    ] };
    expect(deezerCopyMessages(ans, false)).toEqual([
      ['deezer.home.result.copied', { name: 'Bedroom' }],
      ['deezer.home.result.refused', { name: 'Office', reason: 'kept' }],
      ['deezer.home.result.otherAccount', { name: 'Garage' }],
      ['deezer.home.result.notNeeded', { name: 'Hall' }],
      ['deezer.home.result.unreachable', { name: 'Porch' }],
      ['deezer.home.result.oldAgent', { name: 'Attic' }],
    ]);
    // Copy to all names only who took it or refused it.
    expect(deezerCopyMessages(ans, true)).toHaveLength(2);
    expect(deezerCopyMessages({ copied: 0, results: [{ name: 'Hall', outcome: 'not-needed:works' }] }, true)).toEqual([['deezer.home.result.none', {}]]);
    expect(deezerCopyOk(ans)).toBe(false);
    expect(deezerCopyOk({ copied: 1, results: [{ outcome: 'accepted' }] })).toBe(true);
    expect(deezerCopyOk(null)).toBe(false);
  });

  it('has every overview string in English and German', () => {
    for (const k of Object.keys(en).filter((key) => key.startsWith('deezer.home.'))) {
      expect(de[k], k).toBeTruthy();
    }
    for (const k of ['deezer.home.copy', 'deezer.home.copyAll', 'deezer.home.useOlder', 'deezer.home.hint',
      'deezer.home.result.failed', 'deezer.home.older.done', 'deezer.home.older.failed', 'deezer.home.copying']) {
      expect(en[k], k).toBeTruthy();
    }
  });
});

describe('Deezer strings', () => {
  const keys = Object.keys(en).filter((k) => k.startsWith('deezer.'));
  it('every key the logic picks exists in English and German', () => {
    const used = [
      'deezer.state.connected', 'deezer.state.connectedNoName', 'deezer.state.connecting', 'deezer.state.refused',
      'deezer.state.noAnswer', 'deezer.state.notReady', 'deezer.state.unavailable', 'deezer.state.unavailableNoName',
      'deezer.state.notConnected', 'deezer.state.unknown', 'deezer.intro', 'deezer.diagHint', 'deezer.stuck',
      'deezer.rejected', 'deezer.linkNotLinked',
      'deezer.linkUnavailable', 'deezer.linkUnresolved', 'deezer.linkFailed', 'deezer.kind.album', 'deezer.kind.link',
      'deezer.pill.connected', 'deezer.pill.failed', 'deezer.pill.unknown',
    ];
    for (const k of used) {
      expect(en[k], k).toBeTruthy();
      expect(de[k], k).toBeTruthy();
    }
  });
  it('German says du, has no em or en dash, and names the brands', () => {
    for (const k of keys) {
      expect(de[k], k).toBeTruthy();
      expect(de[k]).not.toMatch(/\bSie\b|\bIhr\b|[–—]/);
      expect(en[k]).not.toMatch(/[–—]/);
      if (en[k].includes('Bose')) expect(de[k]).toContain('Bose');
    }
  });
});

describe('restore from a backup file', () => {
  it('is offered only while Deezer is not working', async () => {
    const { deezerCanRestore, deezerView } = await import('./deezer.js');
    expect(deezerCanRestore(deezerView({ state: 'unavailable' }))).toBe(true);
    expect(deezerCanRestore(deezerView({ state: 'not-connected' }))).toBe(true);
    expect(deezerCanRestore(deezerView({ state: 'failed' }))).toBe(true);
    expect(deezerCanRestore(deezerView({ state: 'connected' }))).toBe(false);
    expect(deezerCanRestore(deezerView({ state: 'connecting' }))).toBe(false);
    expect(deezerCanRestore(null)).toBe(false);
  });
  it('names each outcome and stays silent on a cancel', async () => {
    const { deezerRestoreMessage } = await import('./deezer.js');
    expect(deezerRestoreMessage({ result: 'canceled' })).toBe(null);
    expect(deezerRestoreMessage({ result: 'written' })).toEqual(['setup-ok', 'deezer.restore.done']);
    expect(deezerRestoreMessage({ result: 'already-kept' })).toEqual(['setup-warn', 'deezer.restore.kept']);
    expect(deezerRestoreMessage({ result: 'no-login' })).toEqual(['setup-err', 'deezer.restore.noLogin']);
    expect(deezerRestoreMessage({ result: 'no-ssh' })).toEqual(['setup-err', 'deezer.restore.noReach']);
    expect(deezerRestoreMessage({ result: 'write-failed' })).toEqual(['setup-err', 'deezer.restore.notWritten']);
    expect(deezerRestoreMessage(null)).toEqual(['setup-err', 'deezer.restore.notWritten']);
  });
});
