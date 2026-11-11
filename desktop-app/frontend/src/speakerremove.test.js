// "Remove from list" (#1852): the gate (only an entry that is not reachable,
// never one an update is restarting), what the list and the selections look
// like afterwards, what each answer of the backend leads to, the words in all
// thirteen languages, and where the two layouts draw the action.
import { describe, it, expect, vi } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import {
  canRemoveFromList, dropEntrySelections, removeEntry, removeOutcomeText, withoutEntry,
} from './speakerremove.js';
import { fileSource } from './test/source.js';

const dir = new URL('./i18n/bundles/', import.meta.url);
const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
  .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));
const en = bundles.en;
const tEn = (k, vars = {}) => String(en[k] || k).replace(/\{\{(\w+)\}\}/g, (_, v) => (v in vars ? vars[v] : ''));
const placeholders = (s) => [...String(s).matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();

const KEYS = ['speaker.removeFromList', 'speaker.removeFromListLabel', 'speaker.removeConfirmBody',
  'speaker.removedFromList', 'speaker.removeAnswers', 'speaker.removeUpdating', 'speaker.removeFailed'];

const OLD = { host: '192.0.2.40', port: 8888, deviceID: 'AAA', friendlyName: 'Kitchen', kind: 'str', offline: true, offlineSinceSec: 900 };
const NEW = { host: '192.0.2.41', port: 8888, deviceID: 'AAA', friendlyName: 'Kitchen', kind: 'str' };
const BATH = { host: '192.0.2.42', port: 8888, deviceID: 'BBB', friendlyName: 'Bath', kind: 'str' };

describe('canRemoveFromList', () => {
  it('offers the action on an entry that is not reachable', () => {
    expect(canRemoveFromList(OLD)).toBe(true);
    expect(canRemoveFromList({ host: '192.0.2.43', kind: 'stock', offline: true })).toBe(true);
  });

  it('does not offer it on a speaker that answers', () => {
    expect(canRemoveFromList(NEW)).toBe(false);
    expect(canRemoveFromList({ ...NEW, strSilent: true })).toBe(false);
    expect(canRemoveFromList({ ...NEW, kind: 'stock', strNotRunning: true })).toBe(false);
  });

  it('does not offer it inside the restart grace, where the tile is not greyed yet', () => {
    expect(canRemoveFromList({ ...NEW, unreachable: true })).toBe(false);
  });

  it('does not offer it while an update from this app restarts the speaker', () => {
    expect(canRemoveFromList({ ...OLD, otaPending: true })).toBe(false);
    expect(canRemoveFromList(OLD, { updateRunning: (h) => h === OLD.host })).toBe(false);
    expect(canRemoveFromList(OLD, { updateRunning: () => false })).toBe(true);
    expect(canRemoveFromList(OLD, { installHolds: { [OLD.host]: { since: 1 } } })).toBe(false);
    expect(canRemoveFromList(OLD, { installHolds: { [BATH.host]: { since: 1 } } })).toBe(true);
  });

  it('tolerates a missing record and one without an address', () => {
    expect(canRemoveFromList(null)).toBe(false);
    expect(canRemoveFromList(undefined)).toBe(false);
    expect(canRemoveFromList({ offline: true })).toBe(false);
  });
});

describe('withoutEntry', () => {
  it('drops the entry at the address and keeps the same speaker at another one', () => {
    expect(withoutEntry([OLD, NEW, BATH], OLD.host)).toEqual([NEW, BATH]);
  });

  it('tolerates holes and a missing list', () => {
    expect(withoutEntry([null, BATH, undefined], OLD.host)).toEqual([BATH]);
    expect(withoutEntry(null, OLD.host)).toEqual([]);
  });
});

describe('dropEntrySelections', () => {
  it('clears the Settings speaker and the Setup target at the address', () => {
    const state = { currentBox: BATH, settingsBox: OLD, setupTarget: { kind: 'str', box: OLD } };
    dropEntrySelections(state, OLD.host);
    expect(state).toEqual({ currentBox: BATH, settingsBox: null, setupTarget: null });
  });

  it('leaves a selected speaker with an id to the list refresh, which follows it to its new address', () => {
    const state = { currentBox: OLD, settingsBox: BATH, setupTarget: { kind: 'str', box: BATH } };
    dropEntrySelections(state, OLD.host);
    expect(state.currentBox).toBe(OLD);
    expect(state.settingsBox).toBe(BATH);
    expect(state.setupTarget.box).toBe(BATH);
  });

  it('clears a selected speaker that has no id to be followed by', () => {
    const state = { currentBox: { host: OLD.host, offline: true } };
    dropEntrySelections(state, OLD.host);
    expect(state.currentBox).toBeNull();
  });

  it('tolerates a missing state', () => {
    expect(() => dropEntrySelections(null, OLD.host)).not.toThrow();
    expect(() => dropEntrySelections({}, '')).not.toThrow();
  });
});

describe('removeEntry', () => {
  const env = (answer, state = { settingsBox: OLD }) => {
    const e = {
      state,
      list: [OLD, NEW, BATH],
      applied: null,
      remove: vi.fn(async () => {
        if (answer instanceof Error) throw answer;
        return answer;
      }),
      boxes: () => e.list,
      applyList: vi.fn((l) => { e.applied = l; }),
      discover: vi.fn(async () => {}),
    };
    return e;
  };

  it('takes the entry off the list by its address when the backend removed it', async () => {
    const e = env('removed');
    expect(await removeEntry(OLD, e)).toEqual({ status: 'removed' });
    expect(e.remove).toHaveBeenCalledWith(OLD.host);
    expect(e.applied).toEqual([NEW, BATH]);
    expect(e.state.settingsBox).toBeNull();
    expect(e.discover).not.toHaveBeenCalled();
  });

  it('keeps the entry and searches again when the speaker answers after all', async () => {
    const e = env('reachable');
    expect(await removeEntry(OLD, e)).toEqual({ status: 'reachable' });
    expect(e.applyList).not.toHaveBeenCalled();
    expect(e.discover).toHaveBeenCalledTimes(1);
    expect(e.state.settingsBox).toBe(OLD);
  });

  it('keeps the entry of a speaker that is being updated', async () => {
    const e = env('updating');
    expect(await removeEntry(OLD, e)).toEqual({ status: 'updating' });
    expect(e.applyList).not.toHaveBeenCalled();
    expect(e.discover).not.toHaveBeenCalled();
  });

  it('never throws: a failed call and an unknown answer are reported', async () => {
    const failed = env(new Error('boom'));
    expect(await removeEntry(OLD, failed)).toEqual({ status: 'failed', error: 'boom' });
    expect(failed.applyList).not.toHaveBeenCalled();
    expect(await removeEntry(OLD, env('what'))).toEqual({ status: 'failed', error: 'what' });
    expect(await removeEntry(null, env('removed'))).toEqual({ status: 'failed', error: '' });
    expect(await removeEntry(OLD, {})).toEqual({ status: 'failed', error: '' });
  });
});

describe('remove from list strings', () => {
  it('has every key in all thirteen languages with the English placeholders', () => {
    expect(Object.keys(bundles)).toHaveLength(13);
    for (const [code, b] of Object.entries(bundles)) {
      for (const k of KEYS) {
        expect(typeof b[k] === 'string' && b[k].trim().length > 0, `${code}: ${k}`).toBe(true);
        expect(placeholders(b[k]), `${code}: ${k}`).toEqual(placeholders(en[k]));
      }
    }
  });

  it('is translated, not left in English, and uses no dashes as punctuation', () => {
    for (const [code, b] of Object.entries(bundles)) {
      for (const k of KEYS) {
        expect(b[k], `${code}: ${k}`).not.toMatch(/[–—]| - /);
        if (code !== 'en') expect(b[k], `${code}: ${k}`).not.toBe(en[k]);
      }
    }
  });

  it('writes German informally, with real umlauts', () => {
    expect(bundles.de['speaker.removeConfirmBody']).toContain('ändert');
    expect(bundles.de['speaker.removeUpdating']).toContain('läuft');
    for (const k of KEYS) expect(bundles.de[k], k).not.toMatch(/\b(Sie|Ihr|Ihre|Ihren)\b/);
  });

  it('says that nothing is changed on the speaker and that a found speaker comes back', () => {
    const body = tEn('speaker.removeConfirmBody', { name: 'Kitchen', host: OLD.host });
    expect(body).toContain('<b>Kitchen</b> does not answer at 192.0.2.40.');
    expect(body).toContain('nothing is changed on the speaker');
    expect(body).toContain('comes back by itself');
  });

  it('words every outcome', () => {
    expect(removeOutcomeText({ status: 'removed' }, 'Kitchen', tEn)).toBe('Kitchen was removed from the list.');
    expect(removeOutcomeText({ status: 'reachable' }, 'Kitchen', tEn)).toBe('Kitchen answers again, so it stays in the list.');
    expect(removeOutcomeText({ status: 'updating' }, 'Kitchen', tEn)).toBe('An update is still running on Kitchen. Try again once it is done.');
    expect(removeOutcomeText({ status: 'failed', error: 'boom' }, 'Kitchen', tEn)).toBe('Kitchen could not be removed from the list: boom');
    expect(removeOutcomeText(null, 'Kitchen', tEn)).toBe('Kitchen could not be removed from the list: ');
  });
});

// The classic picker draws its tiles inside renderBoxSelect, which needs the
// whole app around it, so its wiring is pinned in the source like the other
// picker tests do.
describe('classic speaker picker', () => {
  const src = fileSource('views/boxpicker.js', import.meta.url);

  it('draws the x through the gate, on greyed tiles only', () => {
    expect(src).toContain("const removable = off && !tile.updateRunning && canRemoveFromList(b, { installHolds: state.installHolds });");
    expect(src).toMatch(/const rmBtn = removable\s*\? `<span class="box-remove" role="button" tabindex="0"/);
  });

  it('does not select or search for the speaker when the x is pressed', () => {
    expect(src).toContain("e.target.closest('.box-remove')) return;");
  });

  it('asks before it removes, and says what came of it', () => {
    const fn = src.slice(src.indexOf('export async function removeFromListDialog'), src.indexOf('// pickBox is what choosing'));
    expect(fn).toContain("t('speaker.removeConfirmBody', { name: escapeHtml(name), host: escapeHtml(box.host) })");
    expect(fn.indexOf('if (!ok) return;')).toBeLessThan(fn.indexOf('deps.removeSpeakerEntry(box)'));
    expect(fn).toContain('showToast(removeOutcomeText(res, name, t));');
  });
});

describe('main.js', () => {
  const src = fileSource('main.js', import.meta.url);

  it('hands one removal path to both layouts', () => {
    expect(src).toMatch(/initBoxPicker\(\{[^}]*\bremoveSpeakerEntry\b/);
    expect(src).toContain('removeSpeaker: removeSpeakerEntry,');
    expect(src).toMatch(/function removeSpeakerEntry\(box\) \{\s*return removeEntry\(box, \{\s*remove: RemoveSpeakerFromList,/);
  });
});
