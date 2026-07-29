// Tests for splitUpdateTargets — who takes part in an "update all" run.
// Pure data-in/data-out, no DOM.
import { describe, it, expect } from 'vitest';
import { splitUpdateTargets } from './utils.js';

// Placeholder LAN (192.0.2.0/24, RFC 5737) only.
const behind = { host: '192.0.2.1', port: 8888, kind: 'str' };
const current = { host: '192.0.2.2', port: 8888, kind: 'str' };
const noEngine = { host: '192.0.2.3', port: 8888, kind: 'str' };
const asleep = { host: '192.0.2.4', port: 8888, kind: 'str' };

describe('splitUpdateTargets', () => {
  it('sends a speaker that is behind through the full update', () => {
    const r = splitUpdateTargets([{ box: behind, needsUpdate: true, engineMissing: false }]);
    expect(r.updateTargets).toEqual([behind]);
    expect(r.engineTargets).toEqual([]);
  });

  it('leaves a speaker alone when it is current and has its engine', () => {
    const r = splitUpdateTargets([{ box: current, needsUpdate: false, engineMissing: false }]);
    expect(r.targets).toEqual([]);
  });

  // The reported bug: the speaker was skipped by the whole-house run and could
  // only be repaired by opening it and pressing its own button.
  it('repairs a current speaker whose Spotify engine is missing', () => {
    const r = splitUpdateTargets([{ box: noEngine, needsUpdate: false, engineMissing: true }]);
    expect(r.updateTargets).toEqual([]);
    expect(r.engineTargets).toEqual([noEngine]);
    expect(r.targets).toEqual([noEngine]);
  });

  // A speaker that is behind AND has no engine belongs in the update group: the
  // update delivers the engine itself, so it must not be counted twice.
  it('does not queue a behind speaker twice when its engine is missing too', () => {
    const r = splitUpdateTargets([{ box: behind, needsUpdate: true, engineMissing: true }]);
    expect(r.updateTargets).toEqual([behind]);
    expect(r.engineTargets).toEqual([]);
    expect(r.targets).toHaveLength(1);
  });

  // A speaker in deep standby cannot be asked, so it reports no missing engine
  // and must stay untouched rather than being woken for a repair.
  it('leaves an unreachable speaker out entirely', () => {
    const r = splitUpdateTargets([{ box: asleep, needsUpdate: false, engineMissing: false }]);
    expect(r.targets).toEqual([]);
  });

  it('keeps the two groups apart and updates first', () => {
    const r = splitUpdateTargets([
      { box: noEngine, needsUpdate: false, engineMissing: true },
      { box: behind, needsUpdate: true, engineMissing: false },
      { box: current, needsUpdate: false, engineMissing: false },
    ]);
    expect(r.updateTargets).toEqual([behind]);
    expect(r.engineTargets).toEqual([noEngine]);
    expect(r.targets).toEqual([behind, noEngine]);
  });

  it('survives empty and malformed input', () => {
    expect(splitUpdateTargets().targets).toEqual([]);
    expect(splitUpdateTargets([]).targets).toEqual([]);
    expect(splitUpdateTargets([null, {}, { needsUpdate: true }]).targets).toEqual([]);
  });
});
