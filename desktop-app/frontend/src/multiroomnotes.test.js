// The Multi-room result notes and the group master default (#882).
//
// Field evidence (three SoundTouch 10s, two of them a stereo pair): the user
// tried to group a speaker, the agent refused because the app had submitted a
// hidden pair half as the group's master, and the red "a stereo pair cannot
// be grouped" note then stayed on screen across every screen change. Errors
// are meant to stay until the next action, but leaving the tab has to count as
// the end of that action, and the default master must never be a pair half.
//
// The DOM-bound pieces (switchView in main.js, renderMultiroom) are checked as
// source, the same way utils.test.js checks the settings view: vitest runs in
// a node environment on purpose (vitest.config.js).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { state } from './state.js';
import { resetMultiroomNotes } from './views/multiroom.js';

// Line endings normalised: the views are checked in with CRLF on some
// machines, and the slices below key on "\n}\n".
const read = (rel) => readFileSync(new URL(rel, import.meta.url), 'utf8').replace(/\r\n/g, '\n');
const mainJS = read('./main.js');
const multiroomJS = read('./views/multiroom.js');

describe('multiroom result notes', () => {
  it('resetMultiroomNotes clears both notes', () => {
    state.zoneMsg = '<div class="setup-err">A stereo pair cannot be part of a group right now</div>';
    state.stereoMsg = '<div class="setup-err">failed</div>';
    resetMultiroomNotes();
    expect(state.zoneMsg).toBe('');
    expect(state.stereoMsg).toBe('');
  });

  it('switchView resets the notes beside stopping the live poll when leaving the tab', () => {
    const leave = mainJS.match(/if \(view !== 'multiroom'\) \{[^\n]*\}/);
    expect(leave, 'the leave branch must still be findable').not.toBeNull();
    expect(leave[0]).toContain('stopMultiroomLive()');
    expect(leave[0]).toContain('resetMultiroomNotes()');
  });

  it('never clears the notes inside renderMultiroom itself (every action ends in a repaint)', () => {
    const fn = multiroomJS.slice(
      multiroomJS.indexOf('export function renderMultiroom('),
      multiroomJS.indexOf('\nasync function ', multiroomJS.indexOf('export function renderMultiroom(')),
    );
    expect(fn).not.toContain('resetMultiroomNotes(');
  });

  it('an action that finishes on a hidden tab drops its note instead of painting it', () => {
    const fa = multiroomJS.slice(multiroomJS.indexOf('function finishAction('));
    const body = fa.slice(0, fa.indexOf('\n}'));
    expect(body).toContain("state.view !== 'multiroom'");
    expect(body).toContain('resetMultiroomNotes()');
    for (const name of ['doFormZone', 'doDissolveZoneAt', 'doFormStereo', 'doDissolveStereo', 'doDissolveStereoPair']) {
      const start = multiroomJS.indexOf(`async function ${name}(`);
      expect(start, `${name} must still exist`).toBeGreaterThan(-1);
      const end = multiroomJS.indexOf('\n}\n', start);
      const fnSrc = multiroomJS.slice(start, end);
      expect(fnSrc.trimEnd().endsWith('finishAction();'), `${name} must end in finishAction()`).toBe(true);
    }
  });
});

describe('group master default', () => {
  it('computes the pair halves before the master default and defaults among the groupable speakers only', () => {
    const fn = multiroomJS.slice(multiroomJS.indexOf('export function renderMultiroom('));
    const pairs = fn.indexOf('const groupablePairIDs');
    const def = fn.indexOf('zoneBoxes[0].deviceID');
    expect(pairs).toBeGreaterThan(-1);
    expect(def).toBeGreaterThan(pairs);
    expect(fn).not.toContain('strBoxes[0].deviceID');
  });

  it('doFormZone submits from the pair-free set and refuses a pair half client-side', () => {
    const start = multiroomJS.indexOf('async function doFormZone(');
    const fnSrc = multiroomJS.slice(start, multiroomJS.indexOf('\n}\n', start));
    expect(fnSrc).toContain('pairMemberIds(state.zoneLive)');
    expect(fnSrc).toContain("t('multiroom.pairNotGroupable')");
    // Through the flash, never as a direct write that would outlive the tab.
    const refusal = fnSrc.slice(0, fnSrc.indexOf('const slaves ='));
    expect(refusal).toContain('flashZoneMsg(');
    expect(refusal).not.toContain('state.zoneMsg =');
  });
});
