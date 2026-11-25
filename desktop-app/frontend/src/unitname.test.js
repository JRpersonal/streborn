// speakerUnitName: the name the key tiles and what a key reports give a
// speaker. Half of a live stereo pair is the pair, "Kitchen pair (stereo)",
// as in the sidebar since #2101; any other speaker keeps its own name (#1587).
import { beforeEach, describe, expect, it } from 'vitest';
import { setLocale } from './i18n/index.js';
import { state } from './state.js';
import { speakerUnitName } from './unitname.js';

setLocale('en');

const left = { host: '192.0.2.11', port: 8888, deviceID: 'AA0000000001', friendlyName: 'Left half' };
const right = { host: '192.0.2.12', port: 8888, deviceID: 'AA0000000002', friendlyName: 'Right half' };
const solo = { host: '192.0.2.13', port: 8888, deviceID: 'AA0000000003', friendlyName: 'Office' };
const pairDoc = (name) => ({ stereo: { id: 'pair-1', masterDeviceID: left.deviceID, name, members: [
  { deviceID: left.deviceID, ip: left.host, role: 'LEFT' },
  { deviceID: right.deviceID, ip: right.host, role: 'RIGHT' },
] } });

beforeEach(() => {
  state.boxes = [left, right, solo];
  state.zoneLive = { [left.deviceID]: pairDoc('Kitchen pair'), [right.deviceID]: pairDoc('Kitchen pair') };
});

describe('speakerUnitName', () => {
  it('names either half of a live pair as the pair', () => {
    expect(speakerUnitName(left)).toBe('Kitchen pair (stereo)');
    expect(speakerUnitName(right)).toBe('Kitchen pair (stereo)');
  });

  it('keeps the own name of a speaker in no pair', () => {
    expect(speakerUnitName(solo)).toBe('Office');
  });

  it('names a half by its own name again once the pair is undone', () => {
    state.zoneLive = {};
    expect(speakerUnitName(left)).toBe('Left half');
  });

  it('is empty without a speaker', () => {
    expect(speakerUnitName(null)).toBe('');
  });
});
