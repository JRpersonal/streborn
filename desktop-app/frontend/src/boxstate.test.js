import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { answersWithoutSTR, displayTrackState } from './boxstate.js';

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
