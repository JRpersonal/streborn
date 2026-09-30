import { describe, it, expect, beforeEach } from 'vitest';
import { noteNotReady, noteWakeSucceeded, forgetAllWakeFailures, escalationForCount } from './wakeescalation.js';

beforeEach(() => forgetAllWakeFailures());

// A sentence that is right on the first attempt is a dead end on the fifth. A
// reporter read the same line over eighty times and had nowhere to go from it.
describe('escalationForCount', () => {
  it('asks for patience on the first two, and never suggests pressing again', () => {
    expect(escalationForCount(1)).toBe('play.errBoxStarting');
    expect(escalationForCount(2)).toBe('play.errBoxStarting');
  });

  it('suggests the power cycle once patience has clearly not worked', () => {
    expect(escalationForCount(3)).toBe('play.errBoxStuckTryPower');
    expect(escalationForCount(4)).toBe('play.errBoxStuckTryPower');
  });

  it('asks for a diagnostic once advice has run out', () => {
    expect(escalationForCount(5)).toBe('play.errBoxStuckSendLog');
    expect(escalationForCount(40)).toBe('play.errBoxStuckSendLog');
  });

  it('never proposes a factory reset at any count', () => {
    // A factory reset wipes the presets and the Wi-Fi credentials. No count
    // justifies steering somebody there for a speaker that is merely asleep.
    for (let n = 1; n <= 100; n++) {
      expect(escalationForCount(n)).not.toContain('Reset');
      expect(escalationForCount(n)).not.toContain('factory');
    }
  });

  it('treats nonsense as a first attempt rather than escalating', () => {
    for (const n of [0, -3, NaN, Infinity, null, undefined, 'x']) {
      expect(escalationForCount(n)).toBe('play.errBoxStarting');
    }
  });
});

describe('the per-speaker run', () => {
  it('counts each speaker separately, so one bad speaker does not escalate another', () => {
    expect(noteNotReady('192.0.2.10')).toBe(1);
    expect(noteNotReady('192.0.2.10')).toBe(2);
    expect(noteNotReady('192.0.2.20')).toBe(1);
    expect(noteNotReady('192.0.2.10')).toBe(3);
  });

  it('starts over once the speaker takes a command', () => {
    noteNotReady('192.0.2.10');
    noteNotReady('192.0.2.10');
    noteNotReady('192.0.2.10');
    noteWakeSucceeded('192.0.2.10');
    // The next failure is a FIRST failure: somebody whose speaker worked ten
    // minutes ago must not be told to pull the plug on the first hiccup.
    expect(noteNotReady('192.0.2.10')).toBe(1);
    expect(escalationForCount(noteNotReady('192.0.2.10'))).toBe('play.errBoxStarting');
  });

  it('survives a missing host instead of losing the escalation', () => {
    expect(noteNotReady(undefined)).toBe(1);
    expect(noteNotReady('')).toBe(2);
  });
});
