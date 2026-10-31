// The refusal line in a speaker page card (UI 2.0 phase 5, sectionmsg.js):
// where it goes, what it says, that Details keeps the copyable dialog, and
// that importing the module touches no DOM (the view-extraction trap).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { setLocale } from '../../i18n/index.js';
import { FakeDocument, el, speakerPage } from './fakedom.testutil.js';

const showError = vi.fn();
vi.mock('../../utils.js', async (importOriginal) => ({
  ...(await importOriginal()),
  showError: (...a) => showError(...a),
}));

setLocale('en');

let doc;
beforeEach(() => {
  showError.mockClear();
  doc = new FakeDocument();
  globalThis.document = doc;
});
afterEach(() => { delete globalThis.document; });

// The module and the i18n instance it reads (a reset module registry hands
// out a fresh one, back on its default locale).
const mod = async () => {
  const m = await import('./sectionmsg.js');
  (await import('../../i18n/index.js')).setLocale('en');
  return m;
};

function nameCard() {
  const input = el(doc, 'input', { id: 'boxNameInput' });
  const save = el(doc, 'button', { id: 'boxNameSave' });
  const row = el(doc, 'div', { class: 'setting-row' }, [input, save]);
  const help = el(doc, 'small', { class: 'muted' });
  const card = el(doc, 'div', { class: 'settings-section', attrs: { 'data-ui2-sec': 'name' } }, [el(doc, 'h3'), row, help]);
  return { card, row, save, input, help };
}

describe('sectionmsg', () => {
  it('touches no DOM at import', async () => {
    delete globalThis.document;
    vi.resetModules();
    await expect(import('./sectionmsg.js')).resolves.toBeTruthy();
  });

  it('puts the line right after the control\'s .setting-row, naming the speaker and the answer', async () => {
    const { showSectionError } = await mod();
    const { card, row, save } = nameCard();
    speakerPage(doc, { overview: [card] });
    const line = showSectionError(card, save, new Error('refused'), { name: 'Kitchen' });
    expect(line.parentNode).toBe(card);
    expect(row.nextSibling).toBe(line);
    expect(line.getAttribute('role')).toBe('alert');
    expect(line.classList.contains('sps-msg--error')).toBe(true);
    expect(line.textContent).toContain('Kitchen');
    expect(line.textContent).toContain('refused');
    expect(line.textContent).not.toMatch(/Error: refused/);
    expect(line.scrolled).toBe(1);
  });

  it('keeps one line per card: a second refusal replaces the first', async () => {
    const { showSectionError } = await mod();
    const { card, save } = nameCard();
    speakerPage(doc, { overview: [card] });
    showSectionError(card, save, 'first', { name: 'Kitchen' });
    showSectionError(card, save, 'second', { name: 'Kitchen' });
    const lines = card.querySelectorAll('.sps-msg');
    expect(lines).toHaveLength(1);
    expect(lines[0].textContent).toContain('second');
  });

  it('opens the error dialog with the raw error from Details, and Close takes the line away', async () => {
    const { showSectionError } = await mod();
    const { card, save } = nameCard();
    speakerPage(doc, { overview: [card] });
    const err = new Error('status 500: refused');
    const line = showSectionError(card, save, err, { name: 'Kitchen' });
    line.querySelector('.sps-msg-details').click();
    expect(showError).toHaveBeenCalledWith(err);
    line.querySelector('.sps-msg-close').click();
    expect(card.querySelectorAll('.sps-msg')).toHaveLength(0);
    expect(save.focused).toBe(true);
  });

  it('places the line under the pressed button in a card without rows, and outside the Wi-Fi form', async () => {
    const { placeAfter } = await mod();
    const btn = el(doc, 'button', { id: 'boxRebootBtn' });
    const grid = el(doc, 'div', { class: 'actions-grid' }, [btn]);
    const card = el(doc, 'div', { class: 'settings-section' }, [grid]);
    expect(placeAfter(card, btn)).toBe(btn);
    const save = el(doc, 'button', { id: 'boxWlanSave' });
    const form = el(doc, 'div', { id: 'wlanSwitchForm' }, [save]);
    const wifi = el(doc, 'div', { class: 'settings-section' }, [form]);
    expect(placeAfter(wifi, save)).toBe(form);
    expect(placeAfter(wifi, wifi)).toBe(null);
  });

  it('finds the open card only: another tab, a hidden card or a node outside the page is null', async () => {
    const { openSection } = await mod();
    const a = nameCard();
    const sound = el(doc, 'div', { class: 'settings-section' }, [el(doc, 'button', { id: 'displayMsgOn' })]);
    speakerPage(doc, { overview: [a.card], sound: [sound] }, 'overview');
    expect(openSection('boxNameSave').section).toBe(a.card);
    expect(openSection('displayMsgOn')).toBe(null);
    const loose = el(doc, 'div', { class: 'settings-section' }, [el(doc, 'button', { id: 'classicBtn' })]);
    doc.body.appendChild(loose);
    expect(openSection('classicBtn')).toBe(null);
    a.card.classList.add('hidden');
    expect(openSection('boxNameSave')).toBe(null);
  });

  it('marks a field with aria-invalid and aria-describedby, and clears it on input', async () => {
    const { showFieldError } = await mod();
    const { card, input } = nameCard();
    speakerPage(doc, { overview: [card] });
    const line = showFieldError(input, 'Enter a name');
    expect(input.getAttribute('aria-invalid')).toBe('true');
    expect(input.getAttribute('aria-describedby')).toBe(line.id);
    expect(line.textContent).toBe('Enter a name');
    input.dispatch('input');
    expect(input.getAttribute('aria-invalid')).toBe(null);
    expect(input.getAttribute('aria-describedby')).toBe(null);
    expect(card.querySelectorAll('.sps-field-err')).toHaveLength(0);
  });

  it('shows an unreadable setting with Try again and aria-disabled toggles, and clears it', async () => {
    const { paintReadState } = await mod();
    const on = el(doc, 'button', { id: 'displayMsgOn' });
    const off = el(doc, 'button', { id: 'displayMsgOff' });
    const card = el(doc, 'div', { class: 'settings-section', id: 'displayMsgSection' }, [el(doc, 'div', { class: 'setting-row' }, [on, off])]);
    speakerPage(doc, { overview: [card] });
    const retry = vi.fn();
    paintReadState(card, true, { name: 'Kitchen', buttons: [on, off], retry });
    const status = card.querySelector('.u-sec-status');
    expect(status.textContent).toContain('Could not read this setting from Kitchen.');
    expect(on.getAttribute('aria-disabled')).toBe('true');
    status.querySelector('.u-sec-retry').click();
    expect(retry).toHaveBeenCalledTimes(1);
    paintReadState(card, false, { buttons: [on, off] });
    expect(card.querySelector('.u-sec-status')).toBe(null);
    expect(on.getAttribute('aria-disabled')).toBe(null);
  });
});
