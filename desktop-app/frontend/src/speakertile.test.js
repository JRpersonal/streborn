import { describe, it, expect } from 'vitest';
import { speakerTileState } from './speakertile.js';
import { srcOf, fileSource } from './test/source.js';

const A = { host: '192.0.2.10', port: 8888, deviceID: 'AAA', kind: 'STR', version: 'v1.0.0' };
const B = { host: '192.0.2.11', port: 8888, deviceID: 'BBB', kind: 'STR', version: 'v1.0.0' };
const STOCK = { host: '192.0.2.12', port: 8090, deviceID: 'CCC', kind: 'stock' };

describe('speakerTileState', () => {
  it('starts with every flag off', () => {
    expect(speakerTileState(A, {})).toEqual({
      offline: false, stock: false, strNotRunning: false, playing: false,
      needsUpdate: false, noInternet: false, isCurrent: false, standby: false, inStereoPair: false,
    });
  });

  it('marks offline and stock speakers', () => {
    expect(speakerTileState({ ...A, offline: true }).offline).toBe(true);
    const s = speakerTileState(STOCK);
    expect(s.stock).toBe(true);
    expect(s.strNotRunning).toBe(false);
    expect(speakerTileState({ ...STOCK, strNotRunning: true }).strNotRunning).toBe(true);
  });

  it('is playing from the every-speaker poll alone', () => {
    expect(speakerTileState(B, { boxPlaying: { BBB: true } }).playing).toBe(true);
    expect(speakerTileState(B, { boxPlaying: { BBB: false } }).playing).toBe(false);
  });

  it('is playing from the current box and its play state alone', () => {
    for (const ps of ['PLAY_STATE', 'BUFFERING_STATE']) {
      expect(speakerTileState(A, { currentBox: A, nowPlayState: ps }).playing).toBe(true);
    }
    expect(speakerTileState(A, { currentBox: A, nowPlayState: 'PAUSE_STATE' }).playing).toBe(false);
    // The play state belongs to the current box only.
    expect(speakerTileState(B, { currentBox: A, nowPlayState: 'PLAY_STATE' }).playing).toBe(false);
  });

  it('knows the current box by host', () => {
    expect(speakerTileState(A, { currentBox: { ...A } }).isCurrent).toBe(true);
    expect(speakerTileState(B, { currentBox: A }).isCurrent).toBe(false);
  });

  it('reads standby for the current box only', () => {
    expect(speakerTileState(A, { currentBox: A, nowSource: 'STANDBY' }).standby).toBe(true);
    expect(speakerTileState(B, { currentBox: A, nowSource: 'STANDBY' }).standby).toBe(false);
  });

  it('asks the injected checks for update and internet, STR speakers only', () => {
    const ctx = { needsUpdate: () => true, cannotReachInternet: () => true };
    expect(speakerTileState(A, ctx).needsUpdate).toBe(true);
    expect(speakerTileState(A, ctx).noInternet).toBe(true);
    expect(speakerTileState(STOCK, ctx).needsUpdate).toBe(false);
    expect(speakerTileState(STOCK, ctx).noInternet).toBe(false);
  });

  it('finds a speaker in a live stereo pair by id or by address', () => {
    const pairs = [{ id: 'p', master: 'AAA', members: [{ deviceID: 'AAA' }, { ip: '192.0.2.11' }] }];
    expect(speakerTileState(A, { stereoPairs: pairs }).inStereoPair).toBe(true);
    expect(speakerTileState(B, { stereoPairs: pairs }).inStereoPair).toBe(true);
    expect(speakerTileState({ ...A, host: '192.0.2.99', deviceID: 'ZZZ' }, { stereoPairs: pairs }).inStereoPair).toBe(false);
  });

  it('is what both the picker and the room switcher read', () => {
    // The two drawings of a speaker must never disagree about its state.
    expect(srcOf('renderBoxSelect')).toContain('speakerTileState(b, tileCtx)');
    expect(fileSource('shell2/rooms.js')).toContain('speakerTileState(');
    // The picker keeps no rule of its own for "playing".
    expect(srcOf('renderBoxSelect')).not.toContain("state.nowPlayState === 'PLAY_STATE'");
  });
});
