// Hold a key to save onto it, and a key's choices (UI 2.0 preview): what a
// hold does, the gestures, the choices per kind of key and the words of the
// panel. The panel itself is checked in the browser
// (e2e/specs/desktop-ui2-hold-to-edit-key.spec.mjs).
import { describe, it, expect, vi } from 'vitest';
import { readFileSync } from 'node:fs';

vi.mock('./ui2radio.js', () => ({
  keyReport: () => null,
  restoreKeyFocus: vi.fn(),
}));
vi.mock('./save.js', () => ({ saveCurrentToSlot: vi.fn() }));
vi.mock('../../shell2/speakers/index.js', () => ({ openSpeakers: vi.fn(() => true) }));

const {
  HOLD_TO_SAVE_MS,
  choiceToKeep,
  holdAction,
  KEY_KINDS,
  isKeyEditGesture,
  keyEditActions,
  keyEditHTML,
  keyEditWords,
  ui2KeyHoldOpts,
} = await import('./ui2keyedit.js');
const { setLocale } = await import('../../i18n/index.js');

setLocale('en');

const src = (rel) => readFileSync(new URL(rel, import.meta.url), 'utf8');

describe('the hold', () => {
  it('takes as long as the current layout\'s hold-to-save, 1.1 s', () => {
    expect(HOLD_TO_SAVE_MS).toBe(1100);
    expect(src('./presettiles.js')).toContain('const LONG_PRESS_MS = 1100;');
  });

  it('saves what plays onto a station, empty or lost key', () => {
    expect(holdAction('station', true)).toBe('save');
    expect(holdAction('empty', true)).toBe('save');
    expect(holdAction('lost', true)).toBe('save');
  });

  it('opens the choices when nothing plays, and never saves over the speaker\'s own key', () => {
    for (const kind of KEY_KINDS) expect(holdAction(kind, false)).toBe('menu');
    expect(holdAction('native', true)).toBe('menu');
  });

  it('saves exactly where the choices offer "save"', () => {
    for (const kind of KEY_KINDS) {
      for (const playing of [true, false]) {
        expect(keyEditActions(kind, playing).includes('save')).toBe(holdAction(kind, playing) === 'save');
      }
    }
  });

  it('saves through the same path as the choice "Save what is playing", reported in the key\'s line', () => {
    const body = src('./ui2keyedit.js');
    const opts = body.slice(body.indexOf('export function ui2KeyHoldOpts'));
    expect(opts).toContain("holdAction(kind, playingNow()) === 'save'");
    expect(opts).toContain('saveCurrentToSlot(slot, keyReport(slot))');
    expect(opts).toContain('onHold: hold, onMenu: open');
  });

  it('a context menu opens the choices, never saves, and ends the press it interrupts', () => {
    const body = src('./presettiles.js');
    const menu = body.slice(body.indexOf("el.addEventListener('contextmenu'"));
    expect(menu.indexOf('cancel();')).toBeGreaterThan(-1);
    expect(menu.indexOf('cancel();')).toBeLessThan(menu.indexOf('opts.onMenu();'));
    expect(menu.slice(0, menu.indexOf('});'))).not.toContain('onHold');
    expect(body).toContain('if (opts.onMenu) {');
    // Only one listener opens the choices on a context menu (the press path's).
    expect(src('./ui2radio.js')).not.toContain("addEventListener('contextmenu'");
  });

  it('drops a hold whose tile a rebuilt grid replaced, instead of saving later', () => {
    expect(src('./presettiles.js')).toContain('if (opts.onHold && !el.isConnected) { cancel(); return; }');
  });

  it('never also plays: a hold that fired returns before the play', () => {
    const body = src('./presettiles.js');
    const finish = body.slice(body.indexOf('const finish = (e) => {'), body.indexOf("el.addEventListener('mousedown', start);"));
    expect(finish.indexOf('if (firedLong) return;')).toBeGreaterThan(-1);
    expect(finish.indexOf('if (firedLong) return;')).toBeLessThan(finish.indexOf('onPlay()'));
    expect(finish.indexOf('if (firedLong) return;')).toBeLessThan(finish.indexOf('onEmptyPress()'));
  });

  it('stands still when less motion is asked for, in the preview only', () => {
    expect(src('./presettiles.js')).toContain('still = !!opts.holdMs && reducedMotion();');
    expect(src('./ui2radio.css')).toMatch(/prefers-reduced-motion: reduce\)[\s\S]*?\.long-press-bar \{ transition: none; \}/);
  });

  it('leaves the current layout alone: without the shell no hold options', () => {
    const tile = { dataset: {} };
    expect(ui2KeyHoldOpts(tile, 3, 'station')).toEqual({});
    expect(tile.dataset.ui2Kind).toBeUndefined();
  });
});

describe('the keyboard way in', () => {
  it('is Shift+Enter, the context-menu key or Shift+F10', () => {
    expect(isKeyEditGesture({ key: 'Enter', shiftKey: true })).toBe(true);
    expect(isKeyEditGesture({ key: 'ContextMenu' })).toBe(true);
    expect(isKeyEditGesture({ key: 'F10', shiftKey: true })).toBe(true);
  });

  it('leaves plain Enter and Space to play, and modified keys alone', () => {
    expect(isKeyEditGesture({ key: 'Enter' })).toBe(false);
    expect(isKeyEditGesture({ key: ' ' })).toBe(false);
    expect(isKeyEditGesture({ key: 'F10' })).toBe(false);
    expect(isKeyEditGesture({ key: 'Enter', shiftKey: true, ctrlKey: true })).toBe(false);
    expect(isKeyEditGesture(null)).toBe(false);
  });
});

describe('the choices of a key', () => {
  it('offers saving what plays first, then finding, then the key\'s own rename and clear', () => {
    expect(keyEditActions('station', true)).toEqual(['save', 'find', 'favorites', 'rename', 'clear']);
    expect(keyEditActions('station', false)).toEqual(['find', 'favorites', 'rename', 'clear']);
  });

  it('gives an empty or lost key a way to be filled, without rename or clear', () => {
    expect(keyEditActions('empty', true)).toEqual(['save', 'find', 'favorites']);
    expect(keyEditActions('empty', false)).toEqual(['find', 'favorites']);
    expect(keyEditActions('lost', true)).toEqual(['save', 'find', 'favorites']);
  });

  it('never saves over the speaker\'s own key by a hold', () => {
    expect(keyEditActions('native', true)).not.toContain('save');
  });

  it('knows every kind the grid draws', () => {
    for (const kind of KEY_KINDS) expect(keyEditActions(kind, true).length).toBeGreaterThan(1);
  });
});

describe('a repaint while the choices are open', () => {
  it('keeps the keyboard on the choice it was on, also when save appears before it', () => {
    expect(choiceToKeep('find', ['save', 'find', 'favorites'])).toBe('find');
    expect(choiceToKeep('rename', ['save', 'find', 'favorites', 'rename', 'clear'])).toBe('rename');
    expect(choiceToKeep('close', ['save', 'find'])).toBe('close');
  });

  it('never lands on save when that choice is gone', () => {
    expect(choiceToKeep('save', ['find', 'favorites'])).toBe('find');
    expect(choiceToKeep('rename', ['save', 'find', 'favorites'])).toBe('find');
    expect(choiceToKeep('', ['save', 'find'])).toBe('find');
    expect(choiceToKeep('clear', ['save'])).toBe('close');
    expect(choiceToKeep(undefined, [])).toBe('close');
  });
});

describe('the words', () => {
  it('names the key and the speaker, and what is on it', () => {
    const w = keyEditWords({ kind: 'station', slot: 3, speaker: 'Kitchen', name: 'Jazz24', actions: ['save', 'rename'] });
    expect(w.title).toBe('Change key 3 on Kitchen');
    expect(w.now).toBe('On this key now: Jazz24');
    expect(w.note).toBe('');
    expect(w.actions).toEqual([{ id: 'save', label: 'Save what is playing' }, { id: 'rename', label: 'Rename' }]);
  });

  it('asks to fill an empty key and says where Find and a favourite lead', () => {
    const w = keyEditWords({ kind: 'empty', slot: 5, speaker: 'Kitchen', name: '', actions: ['find', 'favorites'] });
    expect(w.title).toBe('Fill key 5 on Kitchen');
    expect(w.now).toBe('This key is empty.');
    expect(w.note).toBe('Find a station and Choose a favorite open Keys and alarms for Kitchen, with key 5 chosen.');
  });

  it('draws one primary choice, escaped, in a named group, with a named close', () => {
    const html = keyEditHTML(keyEditWords({ kind: 'station', slot: 1, speaker: 'A <B>', name: '"X"', actions: ['save', 'find'] }));
    expect(html.match(/u-btn--primary/g)).toHaveLength(1);
    expect(html).toContain('data-ke="save"');
    expect(html).toContain('Change key 1 on A &lt;B&gt;');
    expect(html).toContain('On this key now: &quot;X&quot;');
    expect(html).toContain('role="group" aria-labelledby="ui2KeyEditTitle"');
    expect(html).toContain('data-ke="close" aria-label="Close"');
  });
});

// "Find a station" and "Choose a favorite" open the one key editor (Speakers
// > the speaker > Keys and alarms) on the key, instead of the Radio "Put on
// key" picker with a key to start on.
describe('find and favourites', () => {
  const body = src('./ui2keyedit.js');

  it('open the Keys tab of the speaker, on the key, in the right mode', () => {
    expect(body).toContain("openKeyTab(host, slot, id);");
    expect(body).toContain("openSpeakers('speaker', { host, tab: 'keys', key: { slot, mode } })");
    // On use, so the music views do not load the Speakers area with them.
    expect(body).toContain("await import('../../shell2/speakers/index.js')");
    expect(body).not.toMatch(/^import .*shell2\/speakers/m);
    // The quick choices stay in the card.
    expect(body).toContain('saveCurrentToSlot(slot, keyReport(slot, box))');
    expect(body).toContain("id === 'rename' ? '.ren' : '.del'");
  });

  it('left no second way to the picker behind', () => {
    expect(body).not.toMatch(/takeKeyAssignTarget|leaveKeyEditView|dropAssign|keyAssignPending|openUi2RadioTab/);
    const picker = src('./picker.js');
    expect(picker).not.toContain('takeKeyAssignTarget');
    expect(picker).toContain('const row = clampRow(state.presetRow, rows);');
    expect(src('../../main.js')).not.toContain('leaveKeyEditView');
  });
});
