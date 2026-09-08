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
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { state } from './state.js';
import { resetMultiroomNotes, setZoneMsg, renderMultiroom, ZONE_MSG_FLASH_MS } from './views/multiroom.js';

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
    // Through the one writer and marked transient, never as a direct write
    // that would outlive the tab.
    const refusal = fnSrc.slice(0, fnSrc.indexOf('const slaves ='));
    expect(refusal).toContain('setZoneMsg(');
    expect(refusal).toContain('transient: true');
    expect(refusal).not.toContain('state.zoneMsg =');
  });
});

// The group frame showed a raw hex id like "884AEAE811FF" where the main
// speaker's name belongs, and its x was then a silent no-op: the master key
// comes from the speakers' own zone documents (the firmware SoundTouch id)
// while the box record carries what mDNS announced (the chassis MAC), so the
// plain deviceID lookup found nothing (field bundle 2026-09-07).
describe('the group frame never shows a raw id', () => {
  const fn = multiroomJS.slice(multiroomJS.indexOf('export function renderMultiroom('));

  it('resolves the frame master through masterBoxForKey, not by deviceID', () => {
    expect(fn).toContain('masterBoxForKey(mk, state.zoneLive, strBoxes)');
    expect(fn).not.toContain("const masterBox = strBoxes.find(b => (b.deviceID || '').toUpperCase() === mk)");
  });

  it('falls back to the generic group word instead of printing the key', () => {
    const label = fn.slice(fn.indexOf('const label = pair'));
    expect(label.slice(0, label.indexOf(';'))).toContain("t('multiroom.groupLabelPrefix')");
  });

  it('marks the main speaker by host, because the id is the untrustworthy field', () => {
    expect(fn).toContain('b.host === masterBox.host');
  });

  it('sends the frame x through the same resolver and says so when it cannot', () => {
    const x = fn.slice(fn.indexOf('.box-group-x'));
    const handler = x.slice(0, x.indexOf('.zone-card'));
    expect(handler).toContain('masterBoxForKey(mk, state.zoneLive, strBoxes)');
    expect(handler).toContain("t('multiroom.dissolveIncomplete')");
  });
});

// The mirror image of #792: forming a GROUP out of a pair half was refused,
// forming a PAIR out of two speakers already in a group was checked nowhere, so
// the firmware kept the group standing behind the fresh pair (field 2026-09-07).
// The agent is the authority; the app says it before the request goes out.
describe('a group cannot become a stereo pair', () => {
  const bundles = ['en', 'de', 'fr', 'es', 'nl', 'pl', 'tr', 'uk', 'lt', 'lv', 'ar', 'ja', 'zh-Hant'];
  const load = (loc) => JSON.parse(
    readFileSync(new URL(`./i18n/bundles/${loc}.json`, import.meta.url), 'utf8'));

  it('doFormStereo refuses grouped speakers before it calls the agent', () => {
    const start = multiroomJS.indexOf('async function doFormStereo(');
    const fnSrc = multiroomJS.slice(start, multiroomJS.indexOf('\n}\n', start));
    const before = fnSrc.slice(0, fnSrc.indexOf('await FormZone('));
    expect(before).toContain('zoneMasterOf(');
    expect(before).toContain("t('multiroom.groupNotPairable')");
  });

  // A SAVED permanent group is not live while its main speaker is idle, so the
  // live check above sees two free speakers and waves the pairing through. The
  // pair then replaces the group document on the speaker and the group is gone
  // with nothing to restore it from (the loss class of the wiped zones.json,
  // 2026-09-03). Both halves of the UI have to know: the dropdowns must not
  // offer those speakers, and the submit path must refuse a stale pick.
  it('keeps the speakers of a saved group out of the stereo dropdowns', () => {
    const cands = multiroomJS.slice(multiroomJS.indexOf('const storedGroupHosts ='));
    const decl = cands.slice(0, cands.indexOf('const canPair'));
    // pairBlockedHosts, not the raw stored set: a speaker that is half of a
    // LIVE pair stays in the dropdowns, because they are the only place that
    // pair can be renamed or undone (see groupmasterbundle.test.js).
    expect(decl).toContain('pairBlockedHosts(state.zoneLive, strBoxes)');
    expect(decl).toContain('!storedGroupHosts.has(b.host)');
  });

  it('doFormStereo refuses a speaker held by a saved group too', () => {
    const start = multiroomJS.indexOf('async function doFormStereo(');
    const fnSrc = multiroomJS.slice(start);
    const before = fnSrc.slice(0, fnSrc.indexOf('await FormZone('));
    expect(before).toContain('pairBlockedHosts(');
    expect(before).toContain('storedHosts.has(b.host)');
  });

  it('says why a speaker is missing from the picker', () => {
    // An ST10 that is simply absent from the dropdowns reads as a bug.
    const status = multiroomJS.slice(multiroomJS.indexOf('const pairStatus ='));
    expect(status.slice(0, status.indexOf('const pairDis'))).toContain('pairInGroupCount > 0');
  });

  it('has its explanation in every bundle, translated', () => {
    const en = load('en');
    for (const loc of bundles) {
      const b = load(loc);
      expect(b['multiroom.groupNotPairable'], `${loc} is missing the key`).toBeTruthy();
      if (loc !== 'en') {
        expect(b['multiroom.groupNotPairable'], `${loc} is still English`)
          .not.toBe(en['multiroom.groupNotPairable']);
      }
    }
  });

  it('spells German with real umlauts', () => {
    expect(load('de')['multiroom.groupNotPairable']).toMatch(/[äöüßÄÖÜ]/);
  });
});

// The green "Group active: N speaker(s) joined" stayed on the Multi-Room screen
// for as long as the view lived: renderMultiroom re-emits state.zoneMsg on every
// repaint and the form branch assigned it raw, so the confirmation was still
// there after the group was gone, sitting above a line that said "No group right
// now" (#843 in another guise, field 2026-09-07). A self-clearing flash existed
// by then and was wired into the ungroup paths only, because using it was
// optional. setZoneMsg is now the ONE writer and every branch states its policy.
describe('the one zone-note writer', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    // The flash repaints when the tab is open. There is no DOM here, and there
    // does not need to be: renderMultiroom keeps no copy of the note, so with
    // no element to paint into it has nothing to put the cleared note back
    // from. Stubbed rather than left undefined so the timer does not throw.
    globalThis.document = { getElementById: () => null };
    state.view = 'multiroom';
    resetMultiroomNotes();
  });
  afterEach(() => {
    vi.useRealTimers();
    delete globalThis.document;
    state.view = '';
    resetMultiroomNotes();
  });

  it('clears a transient confirmation by itself', () => {
    setZoneMsg('<div class="setup-ok">Group active: 2 speaker(s) joined</div>', { transient: true });
    expect(state.zoneMsg).toContain('Group active');
    vi.advanceTimersByTime(ZONE_MSG_FLASH_MS);
    expect(state.zoneMsg).toBe('');
  });

  it('leaves a sticky error alone, because the user still has to act on it', () => {
    const err = '<div class="setup-err">Could not change the group</div>';
    setZoneMsg(err, { transient: false });
    vi.advanceTimersByTime(ZONE_MSG_FLASH_MS * 4);
    expect(state.zoneMsg).toBe(err);
  });

  it('does not resurrect a cleared note on the next repaint', () => {
    setZoneMsg('<div class="setup-ok">Group dissolved</div>', { transient: true });
    vi.advanceTimersByTime(ZONE_MSG_FLASH_MS);
    renderMultiroom(false);
    expect(state.zoneMsg).toBe('');
    // ...because the repaint has exactly one place to read the note from.
    expect(multiroomJS).toMatch(/<div id="zoneResult">\$\{state\.zoneMsg \|\| ''\}<\/div>/);
  });

  it('lets a newer note stand: an older flash timer never wipes it', () => {
    setZoneMsg('<div class="setup-ok">Group active</div>', { transient: true });
    vi.advanceTimersByTime(ZONE_MSG_FLASH_MS - 100);
    const err = '<div class="setup-err">Could not change the group</div>';
    setZoneMsg(err, { transient: false });
    vi.advanceTimersByTime(ZONE_MSG_FLASH_MS * 2);
    expect(state.zoneMsg).toBe(err);
  });

  it('is the only writer of state.zoneMsg, and every call states its policy', () => {
    // A future branch cannot forget the policy: there is nowhere else to write
    // the note, and the flag is explicit rather than sniffed from the html.
    const body = multiroomJS.slice(multiroomJS.indexOf('export function setZoneMsg('));
    const rest = multiroomJS.slice(0, multiroomJS.indexOf('export function setZoneMsg('))
      + body.slice(body.indexOf('\n}\n'));
    expect(rest).not.toMatch(/state\.zoneMsg\s*=\s*`/);
    expect(multiroomJS).not.toContain('flashZoneMsg');
    const calls = multiroomJS.split('\n')
      .filter((l) => l.includes('setZoneMsg(') && !l.includes('function setZoneMsg('));
    expect(calls.length).toBeGreaterThan(5);
    for (const line of calls) {
      expect(line, line.trim()).toMatch(/\{ transient: (true|false) \}\)/);
    }
  });
});
