// The main speaker's card in the group view names its followers ("Haupt, mit
// Küche") instead of "Haupt von 1", which read like a group number.
import { describe, it, expect } from 'vitest';
import { groupFollowerNames } from './utils.js';

const boxes = [
  { deviceID: 'AAA', friendlyName: 'Wohnzimmer' },
  { deviceID: 'BBB', friendlyName: 'Küche' },
  { deviceID: 'CCC', friendlyName: 'Bad' },
  { deviceID: 'DDD', friendlyName: 'Büro' },
  { deviceID: 'EEE', friendlyName: 'Gästebad' },
];

describe('groupFollowerNames', () => {
  it('names a single follower', () => {
    expect(groupFollowerNames([{ deviceID: 'bbb' }], boxes, 'AAA', 'de')).toBe('Küche');
  });
  it('joins several followers the way the language does', () => {
    expect(groupFollowerNames([{ deviceID: 'BBB' }, { deviceID: 'CCC' }], boxes, 'AAA', 'de')).toBe('Küche und Bad');
    expect(groupFollowerNames([{ deviceID: 'BBB' }, { deviceID: 'CCC' }], boxes, 'AAA', 'en')).toBe('Küche and Bad');
  });
  it('skips the main speaker if the speaker lists itself', () => {
    expect(groupFollowerNames([{ deviceID: 'AAA' }, { deviceID: 'BBB' }], boxes, 'aaa', 'de')).toBe('Küche');
  });
  it('shows an undiscovered follower by its address', () => {
    expect(groupFollowerNames([{ deviceID: 'ZZZ', ip: '192.0.2.9' }], boxes, 'AAA', 'de')).toBe('192.0.2.9');
  });
  it('cuts more than three followers to two names and a count', () => {
    const m = ['BBB', 'CCC', 'DDD', 'EEE'].map(deviceID => ({ deviceID }));
    expect(groupFollowerNames(m, boxes, 'AAA', 'de')).toBe('Küche, Bad +2');
  });
});
