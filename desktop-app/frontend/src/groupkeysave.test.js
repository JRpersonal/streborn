import { describe, it, expect } from 'vitest';
import { gkMembersForSave } from './groupkeys.js';

// A field bundle of eleven speakers, 2026-09-13: the owner set out to put his
// groups on the remote's keys and afterwards the group-key store read
// {"bindings": null, "templates": []} on every one of them. No agent logged the
// line a successful save leaves, so nothing was ever sent.
//
// The composer read the picker's ticks and the saved permanent group, and
// nothing else. A group that is playing right now, which is the group the
// button names, was not a source at all: with nothing ticked it composed to
// null and the caller showed a validation warning instead of saving.
describe('which speakers "save the current group" means', () => {
  const a = { deviceID: 'A' }, b = { deviceID: 'B' }, c = { deviceID: 'C' };

  it('saves the group that is playing when nothing was ticked', () => {
    const got = gkMembersForSave({ picked: [], live: [b, c], stored: [] });
    expect(got.from).toBe('live');
    expect(got.members).toEqual([b, c]);
  });

  it('lets an explicit tick win over what the speakers are doing', () => {
    const got = gkMembersForSave({ picked: [a], live: [b, c], stored: [b] });
    expect(got.from).toBe('picked');
    expect(got.members).toEqual([a]);
  });

  it('falls back to the saved permanent group when nothing is live', () => {
    const got = gkMembersForSave({ picked: [], live: [], stored: [b] });
    expect(got.from).toBe('stored');
    expect(got.members).toEqual([b]);
  });

  it('reports nothing to save rather than an empty group', () => {
    const got = gkMembersForSave({ picked: [], live: [], stored: [] });
    expect(got.from).toBe('');
    expect(got.members).toEqual([]);
  });

  it('survives the sources being absent entirely', () => {
    expect(gkMembersForSave({}).members).toEqual([]);
  });
});
