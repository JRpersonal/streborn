import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { answersWithoutSTR, displayTrackState, displayMessagesState, displaySplashState, blockfallState } from './boxstate.js';

describe('answersWithoutSTR', () => {
  it('is false for a missing or offline record: nothing answers, that is the dead case', () => {
    expect(answersWithoutSTR(null)).toBe(false);
    expect(answersWithoutSTR(undefined)).toBe(false);
    expect(answersWithoutSTR({ kind: 'stock', offline: true })).toBe(false);
    expect(answersWithoutSTR({ kind: 'str', strSilent: true, offline: true })).toBe(false);
  });

  it('is false for a live STR record whose agent answered', () => {
    expect(answersWithoutSTR({ kind: 'str', version: '0.9.74' })).toBe(false);
  });

  it('is true when the Bose firmware answered but the agent did not', () => {
    // This refresh only: stock :8090 answered, agent port silent.
    expect(answersWithoutSTR({ kind: 'str', version: '0.9.74', strSilent: true })).toBe(true);
    // Discovery already degraded the record.
    expect(answersWithoutSTR({ kind: 'stock', strNotRunning: true })).toBe(true);
    // STR was removed: a plain stock speaker.
    expect(answersWithoutSTR({ kind: 'stock' })).toBe(true);
  });
});

describe('displayTrackState', () => {
  it('passes the answer through when the speaker gave one', () => {
    expect(displayTrackState({ enabled: true, mode: 'both' })).toBe(true);
    expect(displayTrackState({ enabled: false })).toBe(false);
  });

  it('is unknown, not off, when there is no answer (#1083)', () => {
    expect(displayTrackState(null)).toBe(null);
    expect(displayTrackState(undefined)).toBe(null);
    expect(displayTrackState({})).toBe(null);
    expect(displayTrackState({ enabled: 'yes' })).toBe(null);
  });

  it('the settings view paints a failed read as unknown', () => {
    const here = dirname(fileURLToPath(import.meta.url));
    const settings = readFileSync(join(here, 'views', 'settings.js'), 'utf8');
    expect(settings).toContain('catch { paintDisplayTrack(null); }');
    expect(settings).not.toContain('catch { paintDisplayTrack(false); }');
    expect(settings).toContain("t('settingsView.displayTrackUnknown')");
  });
});

describe('displayMessagesState', () => {
  it('shows the setting only for a speaker that says it has a display', () => {
    expect(displayMessagesState({ enabled: true, hasDisplay: true }).show).toBe(true);
    expect(displayMessagesState({ enabled: true, hasDisplay: false }).show).toBe(false);
    expect(displayMessagesState({ enabled: true }).show).toBe(false);
  });
  it('keeps an unreadable switch unknown instead of off', () => {
    expect(displayMessagesState(null)).toEqual({ show: false, enabled: null, last: '' });
    expect(displayMessagesState({ hasDisplay: true }).enabled).toBe(null);
    expect(displayMessagesState({ hasDisplay: true, enabled: false }).enabled).toBe(false);
  });
  it('reports the last message text', () => {
    const r = { hasDisplay: true, enabled: true, lastShown: { kind: 'no-internet', text: 'No internet connection' } };
    expect(displayMessagesState(r).last).toBe('No internet connection');
    expect(displayMessagesState({ hasDisplay: true, enabled: true, lastShown: {} }).last).toBe('');
  });
});

describe('displaySplashState', () => {
  it('shows the setting only where the agent can draw on the panel', () => {
    expect(displaySplashState({ enabled: true, supported: true }).show).toBe(true);
    expect(displaySplashState({ enabled: true, supported: false }).show).toBe(false);
    expect(displaySplashState({ enabled: true }).show).toBe(false);
  });
  it('keeps an unreadable state unknown instead of off', () => {
    expect(displaySplashState(null)).toEqual({ show: false, enabled: null });
    expect(displaySplashState({ supported: true }).enabled).toBe(null);
    expect(displaySplashState({ supported: true, enabled: false }).enabled).toBe(false);
  });
});

describe('blockfallState', () => {
  it('stays hidden until a round was played, so the app does not give the game away', () => {
    expect(blockfallState(null).show).toBe(false);
    expect(blockfallState({ rounds: 0, best: 0 }).show).toBe(false);
    expect(blockfallState({}).show).toBe(false);
  });
  it('reads the scores once a round exists', () => {
    const st = blockfallState({ rounds: 2, best: 1200, last: 300, lastRows: 4, screenshot: 'data:image/png;base64,iVBORw0KGgo=' });
    expect(st).toEqual({ show: true, best: 1200, last: 300, rows: 4, screenshot: 'data:image/png;base64,iVBORw0KGgo=' });
  });
  it('drops a screenshot that is not a PNG data URL', () => {
    expect(blockfallState({ rounds: 1, screenshot: 'javascript:alert(1)' }).screenshot).toBe('');
    expect(blockfallState({ rounds: 1, screenshot: 'data:image/png;base64,"onerror="x' }).screenshot).toBe('');
  });
});
