import { describe, it, expect } from 'vitest';
import {
  deezerView, deezerStatusLine, deezerPill, deezerCanDisconnect,
  deezerRefreshDelays, deezerDisconnectError,
  parseDeezerLink, isDeezerLink, deezerLinkKindKey, deezerPlayError,
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
