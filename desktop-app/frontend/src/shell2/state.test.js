// The states of the UI 2.0 preview (phase 5, shell2/state.js): no speakers,
// searching, the search failed, every speaker offline, none chosen; Connect
// by address; the player without a live speaker; a report in its own section
// and the classic toast or dialog outside the preview.
import { describe, it, expect, vi, beforeEach } from 'vitest';

const ui = vi.hoisted(() => ({ showToast: vi.fn(), showError: vi.fn() }));
vi.mock('../utils.js', async (orig) => ({ ...(await orig()), showToast: ui.showToast, showError: ui.showError }));

import {
  NO_SPEAKER_PHASES, connectHTML, discoveryPhase, loadingHTML, noNetworkText, noSpeakersState, playerStateHTML,
  playerStateModel, reportHook, sectionReport, setReportHook, speakersPhase, stateHTML, statusHTML,
  transportErrorText, wireStates,
} from './state.js';
import * as library from './library.js';
import { state } from '../state.js';
import { setLocale } from '../i18n/index.js';
import { escapeAttr, escapeHtml } from '../utils.js';
import en from '../i18n/bundles/en.json';
import { fileSource } from '../test/source.js';

setLocale('en');

// The three helpers exactly as shell2/library.js had them before they moved.
const OLD = {
  statusHTML(key, st) {
    const tone = (st && st.tone) || '';
    const text = (st && st.text) || '';
    return `<span class="lib2-status${tone ? ` lib2-status--${tone}` : ''}" role="status" data-skey="${escapeAttr(key)}">${escapeHtml(text)}</span>`;
  },
  stateHTML({ tone = 'info', title = '', body = '', detail = '', actions = [], extra = '' } = {}) {
    const role = tone === 'error' ? 'alert' : 'status';
    const acts = actions.length
      ? `<div class="lib2-state-acts">${actions.map((a) =>
        `<button type="button" class="u-btn ${a.primary ? 'u-btn--primary' : 'u-btn--secondary'}" id="${escapeAttr(a.id)}">${escapeHtml(a.label)}</button>`).join('')}</div>`
      : '';
    return `
    <div class="lib2-state lib2-state--${escapeAttr(tone)}" role="${role}">
      ${title ? `<h3 class="lib2-state-title lib2-focus-target" tabindex="-1">${escapeHtml(title)}</h3>` : ''}
      ${body ? `<p class="lib2-state-body">${escapeHtml(body)}</p>` : ''}
      ${detail ? `<p class="lib2-state-detail">${escapeHtml(detail)}</p>` : ''}
      ${extra}
      ${acts}
    </div>`;
  },
  loadingHTML(text) {
    return `<p class="lib2-loading" role="status"><span class="lib2-spinner" aria-hidden="true"></span>${escapeHtml(text)}</p>`;
  },
};

describe('the moved library helpers', () => {
  it('give byte for byte what library.js gave', () => {
    for (const st of [null, {}, { tone: 'busy', text: 'Starting on <Kitchen>' }, { tone: 'error', text: 'a & b' }]) {
      expect(statusHTML('k"1', st)).toBe(OLD.statusHTML('k"1', st));
    }
    const states = [
      {},
      { tone: 'error', title: 'No <answer>', body: 'b', detail: 'd', actions: [{ id: 'a"1', label: 'Retry', primary: true }, { id: 'b', label: 'Back' }], extra: '<i>x</i>' },
      { tone: 'empty', title: 'Empty', actions: [] },
    ];
    for (const s of states) expect(stateHTML(s)).toBe(OLD.stateHTML(s));
    expect(stateHTML()).toBe(OLD.stateHTML());
    expect(loadingHTML('Loading <x>')).toBe(OLD.loadingHTML('Loading <x>'));
  });

  it('are re-exported unchanged by library.js', () => {
    expect(library.statusHTML).toBe(statusHTML);
    expect(library.stateHTML).toBe(stateHTML);
    expect(library.loadingHTML).toBe(loadingHTML);
  });
});

describe('noSpeakersState', () => {
  const acts = (html) => [...html.matchAll(/data-state-act="(\w+)"/g)].map((m) => m[1]);
  const order = {
    searching: ['connect'],
    none: ['search', 'add', 'connect'],
    failed: ['search', 'connect'],
    allOffline: ['search', 'connect'],
    noneChosen: ['list'],
  };

  for (const phase of NO_SPEAKER_PHASES) {
    it(`renders ${phase} in words, with a focusable heading and real buttons in order`, () => {
      const html = noSpeakersState(phase, { searchBusy: false });
      expect(html).toContain(`data-phase="${phase}"`);
      expect(html).toMatch(/<h3 class="u-state-title" tabindex="-1">[^<]+<\/h3>/);
      // The state in words, the kicker; the card colour only repeats it.
      expect(html).toMatch(/<p class="u-state-word">[^<]+<\/p>/);
      expect(html).toContain(`role="${phase === 'failed' ? 'alert' : 'status'}"`);
      expect(acts(html)).toEqual(order[phase]);
      for (const b of html.match(/<button[^>]*data-state-act[^>]*>/g)) expect(b).toContain('type="button"');
    });
  }

  it('uses the card variants of shell2.css', () => {
    expect(noSpeakersState('none')).toContain('u-card u-card--empty');
    expect(noSpeakersState('allOffline')).toContain('u-card u-card--offline');
    expect(noSpeakersState('failed')).toContain('u-card u-card--error');
  });

  it('says each phase with its own words', () => {
    expect(noSpeakersState('searching')).toContain(en['ui2.state.searchingTitle']);
    expect(noSpeakersState('none')).toContain(en['ui2.side.empty']);
    expect(noSpeakersState('noneChosen')).toContain(en['ui2.state.goToList']);
    expect(noSpeakersState('allOffline')).toContain(en['ui2.state.allOfflineTitle']);
  });

  it('folds a failed search under Details, and says a dead network on this computer', () => {
    const failed = noSpeakersState('failed', { error: 'mdns: <boom>' });
    expect(failed).toContain(`<summary>${en['ui2.state.details']}</summary>`);
    expect(failed).toContain('mdns: &lt;boom&gt;');
    const offline = noSpeakersState('failed', { error: 'dial udp: connect: network is unreachable' });
    expect(offline).toContain(en['settingsView.noNetworkTitle']);
    expect(offline).not.toContain('<details');
    expect(noNetworkText('connect: network is unreachable')).toBe(true);
    expect(noNetworkText('no route to host')).toBe(false);
  });

  it('shows Search again as busy while it runs', () => {
    const html = noSpeakersState('none', { searchBusy: true });
    expect(html).toContain('aria-busy="true"');
    expect(html).toContain(en['ui2.state.searching']);
  });

  it('keeps Connect by address closed until it is opened, and names what it controls', () => {
    const html = noSpeakersState('none', { id: 'tst' });
    expect(html).toContain('aria-controls="tst-connect"');
    expect(html).toContain('aria-expanded="false"');
    expect(html).toMatch(/<form class="u-connect" id="tst-connect"[^>]* hidden/);
    expect(noSpeakersState('none', { id: 'tst2', connectOpen: true })).toContain('aria-expanded="true"');
  });

  it('takes other actions and words', () => {
    const html = noSpeakersState('noneChosen', { body: 'Pick one.', actions: ['allSpeakers', 'list'], level: 4 });
    expect(acts(html)).toEqual(['allSpeakers', 'list']);
    expect(html).toContain('<h4 class="u-state-title" tabindex="-1">');
    expect(html).toContain('Pick one.');
    expect(noSpeakersState('none', { actions: ['setup'] })).toContain(`data-state-act="add">${en['ui2.speakers.list.setUp']}</button>`);
  });
});

describe('Connect by address', () => {
  const html = connectHTML('cx');

  it('has a visible label for its field and the classic placeholder and button', () => {
    expect(html).toContain(`<label class="u-connect-label" for="cx-ip">${en['ui2.state.addressLabel']}</label>`);
    expect(html).toContain('id="cx-ip"');
    expect(html).toContain(`placeholder="${en['speaker.manualIpPlaceholder']}"`);
    expect(html).toContain(`>${en['speaker.manualIpButton']}</button>`);
  });

  it('is a form with a submit button, so Enter in the field submits', () => {
    expect(html).toMatch(/^<form class="u-connect" id="cx" data-connect="cx" novalidate>/);
    expect(html).toContain('<button type="submit" class="u-btn u-btn--secondary u-connect-btn"');
    expect(html).toContain('class="u-connect-status" role="status"');
    expect(html).toContain('class="u-connect-error" id="cx-err" role="alert"');
  });

  it('says a failure inline, tied to the field, and the progress while it connects', () => {
    const failed = connectHTML('cy', { state: { value: '192.0.2.9', busy: false, error: 'No speaker answered.' } });
    expect(failed).toContain('aria-invalid="true" aria-describedby="cy-err"');
    expect(failed).toContain('role="alert">No speaker answered.</p>');
    expect(failed).toContain('value="192.0.2.9"');
    const busy = connectHTML('cz', { state: { value: '192.0.2.9', busy: '192.0.2.9' } });
    expect(busy).toContain(en['speaker.manualIpSearching'].replace('{{ip}}', '192.0.2.9'));
    expect(busy).toContain('aria-busy="true"');
  });
});

describe('where the speakers stand', () => {
  it('reads the discovery, never the empty array alone', () => {
    expect(discoveryPhase(null).phase).toBe('searching');
    expect(discoveryPhase({ phase: 'searching', last: null }).phase).toBe('searching');
    expect(discoveryPhase({ phase: 'done' }).phase).toBe('none');
    expect(discoveryPhase({ phase: 'failed', error: 'x' })).toEqual({ phase: 'failed', busy: false, error: 'x' });
    // A repeat search keeps the last answer on screen, busy.
    expect(discoveryPhase({ phase: 'searching', last: { phase: 'done' } })).toEqual({ phase: 'none', busy: true, error: '' });
    expect(discoveryPhase({ phase: 'searching', last: { phase: 'failed', error: 'e' } }).phase).toBe('failed');
  });

  it('tells all offline and none chosen apart from a live speaker', () => {
    const a = { host: '192.0.2.1' };
    const b = { host: '192.0.2.2' };
    expect(speakersPhase({ boxes: [{ ...a, offline: true }, { ...b, offline: true }] }).phase).toBe('allOffline');
    expect(speakersPhase({ boxes: [a, b] }).phase).toBe('noneChosen');
    expect(speakersPhase({ boxes: [a, b], currentBox: a }).phase).toBe('');
    expect(speakersPhase({ boxes: [], discovery: { phase: 'done' } }).phase).toBe('none');
  });
});

describe('the player without a live speaker', () => {
  const K = { host: '192.0.2.5', friendlyName: 'Kitchen' };

  it('names an offline speaker and offers Search again', () => {
    const m = playerStateModel({ currentBox: K, boxes: [{ ...K, offline: true }] });
    expect(m.kind).toBe('offline');
    expect(m.text).toBe('Kitchen is not reachable.');
    expect(m.actions).toEqual(['search']);
    expect(playerStateHTML(m)).toContain('data-state-act="search"');
  });

  it('says searching, none found and none chosen, never "left" or "above"', () => {
    expect(playerStateModel({ boxes: [] }).text).toBe(en['ui2.state.searchingTitle']);
    const none = playerStateModel({ boxes: [], discovery: { phase: 'done' } });
    expect(none.text).toBe(en['ui2.state.player.none']);
    expect(none.actions).toEqual(['add']);
    const chosen = playerStateModel({ boxes: [K], discovery: { phase: 'done' } });
    expect(chosen.text).toBe(en['ui2.state.noneChosenBody']);
    expect(chosen.actions).toEqual(['list']);
    for (const m of [none, chosen]) expect(m.text).not.toMatch(/\b(left|above)\b/i);
    expect(playerStateModel({ currentBox: K, boxes: [K] }).kind).toBe('live');
    expect(playerStateHTML({ kind: 'live' })).toBe('');
  });

  it('names the speaker and what it did not do', () => {
    expect(transportErrorText('pause', 'Kitchen', new Error('timeout'))).toBe('Kitchen did not pause: timeout');
    expect(transportErrorText('queue', 'Kitchen', 'nope')).toBe('Kitchen did not change the queue: nope');
  });
});

describe('reports', () => {
  beforeEach(() => {
    ui.showToast.mockReset();
    ui.showError.mockReset();
    state.ui2Shell = false;
  });

  it('are the classic toast and dialog when the shell is not mounted', () => {
    expect(sectionReport(null, { tone: 'ok', text: 'Saved.' })).toBeNull();
    expect(ui.showToast).toHaveBeenCalledWith('Saved.');
    sectionReport({ closest: () => ({}) }, { tone: 'error', text: 'Failed.' });
    expect(ui.showError).toHaveBeenCalledWith('Failed.');
    expect(ui.showToast).toHaveBeenCalledTimes(1);
  });

  it('give no hook without the shell', () => {
    setReportHook('transport', () => {});
    expect(reportHook('transport')).toBeNull();
    state.ui2Shell = true;
    expect(typeof reportHook('transport')).toBe('function');
    setReportHook('transport', null);
    expect(reportHook('transport')).toBeNull();
    state.ui2Shell = false;
  });
});

describe('the module', () => {
  it('touches no DOM at import and wires nothing without a document', async () => {
    expect(typeof globalThis.document).toBe('undefined');
    vi.resetModules();
    const fresh = await import('./state.js');
    expect(fresh.wireStates(null)).toBe(false);
    expect(wireStates(undefined)).toBe(false);
    const top = fileSource('shell2/state.js').split('\n')
      .filter((l) => /^\S/.test(l) && !/^(\/\/|import|export (async )?function|function|async function|\}|const \w+ = \(|let |export const|const)/.test(l));
    for (const l of top) expect(l).not.toMatch(/\b(document|window|localStorage)\b/);
  });

  it('keeps em and en dashes out of its strings', () => {
    for (const [k, v] of Object.entries(en)) {
      if (k.startsWith('ui2.state.')) expect(v, k).not.toMatch(/[–—]/);
    }
  });
});
