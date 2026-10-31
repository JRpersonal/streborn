// Where a setting's outcome is reported (UI 2.0 phase 5, shared.js): the
// classic layout calls showError and showToast with exactly the arguments it
// always did; the preview writes into the card on the speaker page, and keeps
// a setting that could not be read on screen with Try again.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { setLocale } from '../../i18n/index.js';
import { state } from '../../state.js';
import { FakeDocument, el, speakerPage } from '../../shell2/speakers/fakedom.testutil.js';
import { readFileSync } from 'node:fs';

const showError = vi.fn();
const showToast = vi.fn();
vi.mock('../../utils.js', async (importOriginal) => ({
  ...(await importOriginal()),
  showError: (...a) => showError(...a),
  showToast: (...a) => showToast(...a),
}));

const {
  reportError, reportDone, reportWarn, reportFieldError, toggleDepsFor, wireSettingToggle, endpointMissing,
} = await import('./shared.js');

setLocale('en');

let doc;
beforeEach(() => {
  showError.mockClear();
  showToast.mockClear();
  doc = new FakeDocument();
  globalThis.document = doc;
  state.ui2Shell = false;
  state.settingsBox = { host: '192.0.2.10', friendlyName: 'Kitchen' };
});
afterEach(() => {
  delete globalThis.document;
  state.ui2Shell = false;
  state.settingsBox = null;
});

function nameCard() {
  const save = el(doc, 'button', { id: 'boxNameSave' });
  const input = el(doc, 'input', { id: 'boxNameInput' });
  const row = el(doc, 'div', { class: 'setting-row' }, [input, save]);
  const card = el(doc, 'div', { class: 'settings-section', attrs: { 'data-ui2-sec': 'name' } }, [row]);
  return { card, row, save, input };
}

function toggleCard(prefix) {
  const on = el(doc, 'button', { id: `${prefix}On` });
  const off = el(doc, 'button', { id: `${prefix}Off` });
  const card = el(doc, 'div', { class: 'settings-section', id: `${prefix}Section` }, [el(doc, 'div', { class: 'setting-row' }, [on, off])]);
  return { card, on, off };
}

const flush = () => new Promise((r) => setTimeout(r, 0));

describe('classic layout: the old calls, the old arguments', () => {
  it('reportError and reportDone call showError and showToast exactly as before', () => {
    const { card } = nameCard();
    speakerPage(doc, { overview: [card] });
    const err = new Error('refused');
    reportError('boxNameSave', err);
    reportDone('boxNameSave', 'Saved');
    reportDone('boxNameSave', 'Switched', 7000);
    expect(showError.mock.calls).toEqual([[err]]);
    expect(showToast.mock.calls).toEqual([['Saved'], ['Switched', 7000]]);
    expect(card.querySelectorAll('.sps-msg')).toHaveLength(0);
  });

  it('reportWarn and reportFieldError keep their classic dialog or toast', () => {
    const { card, input } = nameCard();
    speakerPage(doc, { overview: [card] });
    reportWarn('boxNameSave', 'partly');
    reportWarn('boxNameSave', 'partly toast', { classicToast: true });
    reportFieldError(input, 'Enter a name');
    expect(showError.mock.calls).toEqual([['partly'], ['Enter a name']]);
    expect(showToast.mock.calls).toEqual([['partly toast']]);
    expect(input.getAttribute('aria-invalid')).toBe(null);
  });

  it('toggleDepsFor hands wireToggleSection the plain showToast and showError', () => {
    const deps = toggleDepsFor('displayMsgSection', 'displayMsgOn');
    const err = new Error('nope');
    deps.showToast('On');
    deps.showError(err);
    expect(showToast.mock.calls).toEqual([['On']]);
    expect(showError.mock.calls).toEqual([[err]]);
    expect(typeof deps.$).toBe('function');
    expect(typeof deps.t).toBe('function');
  });

  it('a toggle whose read fails is hidden, as before', async () => {
    const { card } = toggleCard('displayMsg');
    speakerPage(doc, { sound: [card] }, 'sound');
    wireSettingToggle({
      sectionId: 'displayMsgSection', onId: 'displayMsgOn', offId: 'displayMsgOff',
      get: async () => { throw new Error('timeout'); },
      stateFn: (r) => ({ show: true, enabled: !!r.enabled }),
      set: async () => ({}),
      toastKey: 'settingsView.displayMsgSavedToast',
    });
    await flush();
    expect(card.classList.contains('hidden')).toBe(true);
    expect(card.querySelector('.u-sec-status')).toBe(null);
  });
});

describe('preview: errors in the card they belong to', () => {
  beforeEach(() => { state.ui2Shell = true; });

  it('writes a role=alert line naming the speaker instead of the dialog, replaces it, and a success clears it', () => {
    const { card, row } = nameCard();
    speakerPage(doc, { overview: [card] });
    reportError('boxNameSave', new Error('refused'));
    expect(showError).not.toHaveBeenCalled();
    let lines = card.querySelectorAll('.sps-msg');
    expect(lines).toHaveLength(1);
    expect(lines[0].getAttribute('role')).toBe('alert');
    expect(lines[0].textContent).toContain('Kitchen');
    expect(lines[0].textContent).toContain('refused');
    expect(row.nextSibling).toBe(lines[0]);
    reportError('boxNameSave', 'second');
    lines = card.querySelectorAll('.sps-msg');
    expect(lines).toHaveLength(1);
    expect(lines[0].textContent).toContain('second');
    reportDone('boxNameSave', 'Saved');
    expect(card.querySelectorAll('.sps-msg')).toHaveLength(0);
    expect(showToast.mock.calls).toEqual([['Saved']]);
  });

  it('falls back to the dialog for a card on a tab that is not open', () => {
    const { card } = nameCard();
    speakerPage(doc, { overview: [card] }, 'sound');
    const err = new Error('refused');
    reportError('boxNameSave', err);
    expect(showError.mock.calls).toEqual([[err]]);
    expect(card.querySelectorAll('.sps-msg')).toHaveLength(0);
  });

  it('marks a field error on the field itself', () => {
    const { card, input } = nameCard();
    speakerPage(doc, { overview: [card] });
    reportFieldError(input, 'Enter a name');
    expect(showError).not.toHaveBeenCalled();
    expect(input.getAttribute('aria-invalid')).toBe('true');
    expect(input.getAttribute('aria-describedby')).toBeTruthy();
  });

  it('toggleDepsFor reports a refused toggle in its section', () => {
    const { card } = toggleCard('airplayOpt');
    speakerPage(doc, { sound: [card] }, 'sound');
    toggleDepsFor('airplayOptSection', 'airplayOptOn').showError(new Error('refused'));
    expect(showError).not.toHaveBeenCalled();
    expect(card.querySelector('.sps-msg').textContent).toContain('refused');
  });

  it('keeps an unreadable toggle visible with Try again, refuses a press, and reads again', async () => {
    const { card, on } = toggleCard('displayMsg');
    speakerPage(doc, { sound: [card] }, 'sound');
    let fail = true;
    const set = vi.fn(async () => ({}));
    wireSettingToggle({
      sectionId: 'displayMsgSection', onId: 'displayMsgOn', offId: 'displayMsgOff',
      get: async () => { if (fail) throw new Error('timeout'); return { enabled: true }; },
      stateFn: (r) => ({ show: true, enabled: !!r.enabled }),
      set,
      toastKey: 'settingsView.displayMsgSavedToast',
    });
    await flush();
    expect(card.classList.contains('hidden')).toBe(false);
    const status = card.querySelector('.u-sec-status');
    expect(status.textContent).toContain('Could not read this setting from Kitchen.');
    expect(on.getAttribute('aria-disabled')).toBe('true');
    on.click();
    await flush();
    expect(set).not.toHaveBeenCalled();
    fail = false;
    status.querySelector('.u-sec-retry').click();
    await flush();
    expect(card.querySelector('.u-sec-status')).toBe(null);
    expect(on.getAttribute('aria-disabled')).toBe(null);
    expect(on.classList.contains('active')).toBe(true);
  });

  it('keeps a setting the agent does not know (404) hidden', async () => {
    const { card } = toggleCard('displaySplash');
    speakerPage(doc, { sound: [card] }, 'sound');
    wireSettingToggle({
      sectionId: 'displaySplashSection', onId: 'displaySplashOn', offId: 'displaySplashOff',
      get: async () => { throw new Error('status 404'); },
      stateFn: (r) => ({ show: true, enabled: !!r.enabled }),
      set: async () => ({}),
      toastKey: 'settingsView.displaySplashSavedToast',
    });
    await flush();
    expect(card.classList.contains('hidden')).toBe(true);
    expect(card.querySelector('.u-sec-status')).toBe(null);
  });

  it('tells a missing endpoint from a failed read', () => {
    expect(endpointMissing(new Error('status 404'))).toBe(true);
    expect(endpointMissing('Error: status 404')).toBe(true);
    expect(endpointMissing(new Error('status 4040'))).toBe(false);
    expect(endpointMissing(new Error('status 500'))).toBe(false);
    expect(endpointMissing(new Error('timeout'))).toBe(false);
  });
});

describe('the toggle module stays untouched', () => {
  it('togglesection.js still takes plain deps and knows nothing about the preview', () => {
    const src = readFileSync(new URL('../../togglesection.js', import.meta.url), 'utf8');
    expect(src).not.toMatch(/ui2|sectionmsg|reportError/);
  });
});
