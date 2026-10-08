import { describe, it, expect } from 'vitest';
import { findStalePairs, confirmStalePairs, staleFindingFor, staleNoticeText, STALE_GRACE_MS } from './stalestereo.js';

// The shape of the field case (mail, 2026-10-04, two SoundTouch 10s): speaker 2
// holds the pair document naming speaker 1 as master; speaker 1 holds nothing.
const one = { host: '192.0.2.25', port: 8888, deviceID: 'DEV#ONE', name: 'Kitchen' };
const two = { host: '192.0.2.26', port: 8888, deviceID: 'DEV#TWO', name: 'Bath' };
const pairDoc = {
  id: '895814', masterDeviceID: 'DEV#ONE', name: 'Pair',
  members: [{ deviceID: 'DEV#TWO', ip: '192.0.2.26', role: 'LEFT' }, { deviceID: 'DEV#ONE', ip: '192.0.2.25', role: 'RIGHT' }],
};
const fieldCase = () => ({ 'DEV#ONE': { members: [] }, 'DEV#TWO': { members: [], stereo: pairDoc } });

describe('findStalePairs', () => {
  it('finds the non-master half holding a pair its master denies', () => {
    const f = findStalePairs(fieldCase(), [one, two]);
    expect(f).toHaveLength(1);
    expect(f[0].reason).toBe('partner-denies');
    expect(f[0].holder).toBe(two);
    expect(f[0].partner).toBe(one);
    expect(f[0].pair.members).toHaveLength(2);
  });

  it('leaves a healthy pair alone: the master holds the document, the other half reports nothing', () => {
    const zl = { 'DEV#ONE': { members: [], stereo: pairDoc }, 'DEV#TWO': { members: [] } };
    expect(findStalePairs(zl, [one, two])).toEqual([]);
  });

  it('leaves a pair alone that both halves report', () => {
    const zl = { 'DEV#ONE': { members: [], stereo: pairDoc }, 'DEV#TWO': { members: [], stereo: pairDoc } };
    expect(findStalePairs(zl, [one, two])).toEqual([]);
  });

  it('says nothing when the master did not answer this round', () => {
    const zl = fieldCase();
    zl['DEV#ONE'] = { members: [], staleSince: 1 };
    expect(findStalePairs(zl, [one, two])).toEqual([]);
    delete zl['DEV#ONE'];
    expect(findStalePairs(zl, [one, two])).toEqual([]);
  });

  it('says nothing when the master is offline or not discovered', () => {
    expect(findStalePairs(fieldCase(), [two])).toEqual([]);
    expect(findStalePairs(fieldCase(), [{ ...one, offline: true }, two])).toEqual([]);
  });

  it('ignores an optimistic (null) entry for the holder', () => {
    const zl = fieldCase();
    zl['DEV#TWO'] = null;
    expect(findStalePairs(zl, [one, two])).toEqual([]);
  });

  it('flags a pair the firmware itself marks GROUP_ERROR, even on the master', () => {
    const zl = { 'DEV#ONE': { members: [], stereo: { ...pairDoc, status: 'GROUP_ERROR' } }, 'DEV#TWO': { members: [] } };
    const f = findStalePairs(zl, [one, two]);
    expect(f).toHaveLength(1);
    expect(f[0].reason).toBe('group-error');
    expect(f[0].holder).toBe(one);
  });

  it('flags the partner-gone finding of the agent and still gives the undo a target', () => {
    const zl = { 'DEV#TWO': { members: [], pairPartnerGone: '192.0.2.25', pairPartnerGoneId: 'DEV#ONE' } };
    const f = findStalePairs(zl, [one, two]);
    expect(f).toHaveLength(1);
    expect(f[0].reason).toBe('partner-gone');
    expect(f[0].partner).toBe(one);
    expect(f[0].pair.members[0].deviceID).toBe('DEV#TWO');
  });

  it('names the partner by its own name, not by the discovery placeholder that flips (#1208)', () => {
    const zl = { 'DEV#TWO': { members: [], pairPartnerGone: '192.0.2.25', pairPartnerGoneId: 'DEV#ONE' } };
    const holder = { ...two, name: 'STR-AA5F4C', friendlyName: 'Office Left' };
    for (const placeholder of ['str-192.0.2.25', 'STR-BB6E5D']) {
      const partner = { ...one, name: placeholder, friendlyName: 'Office Right' };
      const [f] = findStalePairs(zl, [partner, holder]);
      expect(f.partnerLabel).toBe('Office Right');
      const text = staleNoticeText(f, (_k, v) => `${v.name}|${v.partner}`);
      expect(text).toBe('Office Left|Office Right');
    }
  });

  it('treats a master that is now paired with a THIRD speaker as denying the old pair', () => {
    const three = { host: '192.0.2.27', deviceID: 'DEV#THREE', name: 'Office' };
    const newPair = { id: 'x', masterDeviceID: 'DEV#ONE', members: [{ deviceID: 'DEV#ONE' }, { deviceID: 'DEV#THREE' }] };
    const zl = { 'DEV#ONE': { members: [], stereo: newPair }, 'DEV#TWO': { members: [], stereo: pairDoc }, 'DEV#THREE': { members: [] } };
    const f = findStalePairs(zl, [one, two, three]);
    expect(f.map(x => x.holder)).toEqual([two]);
  });
});

describe('confirmStalePairs', () => {
  it('needs two rounds and the grace time before a finding is shown', () => {
    const tracker = new Map();
    const f = findStalePairs(fieldCase(), [one, two]);
    expect(confirmStalePairs(f, tracker, 1000)).toEqual([]);
    expect(confirmStalePairs(f, tracker, 1000 + 2000)).toEqual([]);
    expect(confirmStalePairs(f, tracker, 1000 + STALE_GRACE_MS)).toHaveLength(1);
  });

  it('starts over when a round does not see it (a pair being formed)', () => {
    const tracker = new Map();
    const f = findStalePairs(fieldCase(), [one, two]);
    confirmStalePairs(f, tracker, 0);
    confirmStalePairs([], tracker, 5000);
    expect(confirmStalePairs(f, tracker, STALE_GRACE_MS + 1)).toEqual([]);
  });
});

describe('notice', () => {
  const t = (k, v) => `${k}|${v.name}|${v.partner}`;
  it('names both speakers', () => {
    const [f] = findStalePairs(fieldCase(), [one, two]);
    expect(staleNoticeText(f, t)).toBe('stereoStale.partnerDenies|Bath|Kitchen');
    expect(staleFindingFor('192.0.2.26', [f])).toBe(f);
    expect(staleFindingFor('192.0.2.25', [f])).toBe(null);
  });
});
