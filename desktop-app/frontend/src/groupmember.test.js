import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

// #938: the stereo section now tells an owner whose SoundTouch 10s all sit in
// saved groups to "remove two of them from their group first, then pair them".
// The app had no way to do that: a saved group's only control was the x that
// deletes the whole group. These pin the shape of the control that closes that
// gap, because the rest of it is DOM and a speaker.

const src = readFileSync(new URL('./views/multiroom.js', import.meta.url), 'utf8');
const bundle = (lang) =>
  JSON.parse(readFileSync(new URL(`./i18n/bundles/${lang}.json`, import.meta.url), 'utf8'));

describe('taking one speaker out of a saved group', () => {
  it('puts an x on the member chips', () => {
    expect(src).toContain('zone-chip-x');
    expect(src).toContain('data-gkill-ip');
  });

  // Removing the speaker the group forms from IS deleting the group, and the
  // frame's own x already does that. Two controls for one action, one of them
  // in the wrong place, is how the previous confusion started.
  it('gives the main speaker no x of its own', () => {
    const chips = src.slice(src.indexOf('const memberX ='), src.indexOf('return `<div class="box-group box-group-stored">'));
    const masterChip = chips.slice(chips.indexOf('g.masterBox ?'), chips.indexOf('g.members.filter(m => m.box)'));
    expect(masterChip).not.toContain('memberX');
  });

  it('asks the speaker and reports what it answered', () => {
    expect(src).toContain('RemoveGroupMember(master.host, master.port, memberIP)');
    expect(src).toContain('res.groupGone');
    expect(src).toContain('multiroom.memberRemovedGroupGone');
  });

  // The same shape as the other per-frame actions: no startAction, exactly one
  // finishAction, or the tab is left spinning on an error.
  it('finishes the action once on both paths', () => {
    const fn = src.slice(src.indexOf('async function doRemoveGroupMemberAt'),
                         src.indexOf('// doDissolveZoneAt dissolves'));
    expect(fn).not.toContain('startAction');
    expect((fn.match(/finishAction\(\)/g) || []).length).toBe(1);
  });

  it('has all four strings in every bundle', () => {
    const langs = ['en', 'de', 'nl', 'fr', 'es', 'pl', 'tr', 'uk', 'lt', 'lv', 'ja', 'zh-Hant', 'ar'];
    const keys = ['multiroom.removeMemberTip', 'multiroom.memberRemoved',
                  'multiroom.memberRemovedGroupGone', 'multiroom.memberRemoveFailed'];
    for (const l of langs) {
      const b = bundle(l);
      for (const k of keys) expect(b[k], `${k} missing in ${l}`).toBeTruthy();
    }
  });

  // The tooltip names the speaker it removes: a row of identical x buttons on
  // chips sitting close together is exactly where a mis-click happens.
  it('names the speaker in the tooltip', () => {
    for (const l of ['en', 'de', 'fr', 'ja']) {
      expect(bundle(l)['multiroom.removeMemberTip']).toContain('{{name}}');
    }
  });
});
