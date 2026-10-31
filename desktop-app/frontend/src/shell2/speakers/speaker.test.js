// The UI 2.0 page for one speaker (#1287, phase 3): every section of the
// classic Speaker settings has a fixed tab (the checklist in speaker.js), the
// cards the page adds, the head's state, and the confirmations that name the
// speaker. The browser walk through all five tabs is the e2e spec
// desktop-ui2-speakers-page.spec.mjs.
import { describe, it, expect, afterEach } from 'vitest';
import {
  PAGE_SECTIONS, SPEAKER_SECTIONS, bodyUnsorted, networkSummaryHTML, otherPairHalf, networkSummaryRows, sectionTab,
  speakerConfirmTitle, speakerStateHTML, speakerStateKind,
} from './speaker.js';
import { SPEAKER_TABS } from './routes.js';
import { state } from '../../state.js';
import { setLocale } from '../../i18n/index.js';
import { fileSource, srcOf } from '../../test/source.js';
import en from '../../i18n/bundles/en.json';

setLocale('en');

const MODULES = ['status', 'basics', 'sound', 'network', 'info', 'actions', 'advanced'];
const settingsSrc = Object.fromEntries(MODULES.map((m) => [m, fileSource(`views/settings/${m}.js`)]));
const keysIn = (src) => [...src.matchAll(/data-ui2-sec="([A-Za-z]+)"/g)].map((m) => m[1]);

afterEach(() => { state.ui2Shell = false; });

describe('the classic-section checklist', () => {
  it('gives every classic section a key, and every key one tab', () => {
    const classic = [];
    for (const m of MODULES) {
      const src = settingsSrc[m];
      const sections = (src.match(/<(div|details) [^>]*class="settings-section/g) || []);
      const keys = keysIn(src);
      expect(keys.length, `${m}.js: a section without data-ui2-sec`).toBe(sections.length);
      classic.push(...keys);
    }
    const mapped = SPEAKER_SECTIONS.map(([k]) => k);
    expect(new Set(mapped).size, 'a key mapped twice').toBe(mapped.length);
    expect(new Set(classic).size, 'a key used by two sections').toBe(classic.length);
    // Every classic section has its tab, and every mapped key is a classic
    // section or one of the page's own cards.
    for (const k of classic) expect(mapped, k).toContain(k);
    expect([...mapped].sort()).toEqual([...classic, ...PAGE_SECTIONS].sort());
  });

  it('puts every section on one of the five tabs, and no tab is empty', () => {
    for (const [k, tab] of SPEAKER_SECTIONS) expect(SPEAKER_TABS, k).toContain(tab);
    for (const tab of SPEAKER_TABS) expect(SPEAKER_SECTIONS.some(([, t]) => t === tab), tab).toBe(true);
  });

  it('keeps restart, remove and reset in Maintenance, and the settings that say what the speaker is on Overview', () => {
    expect(sectionTab('actions')).toBe('maintenance');
    for (const id of ['boxRebootBtn', 'boxTrueFactoryResetBtn', 'boxRemoveSTRBtn', 'boxIdentifyBtn', 'boxSaveLogsBtn']) {
      expect(settingsSrc.actions, id).toContain(`id="${id}"`);
    }
    for (const k of ['ssh', 'backup', 'restore', 'foreign']) expect(sectionTab(k), k).toBe('maintenance');
    for (const k of ['status', 'name', 'speakerInfo', 'phone', 'netSummary']) expect(sectionTab(k), k).toBe('overview');
    for (const k of ['volume', 'bass', 'levels']) expect(sectionTab(k), k).toBe('sound');
    for (const k of ['wifi', 'region']) expect(sectionTab(k), k).toBe('network');
    for (const k of ['hwKeys', 'syncKeys', 'keyMap']) expect(sectionTab(k), k).toBe('keys');
  });

  it('shows a section nobody mapped yet on Overview rather than nowhere', () => {
    expect(sectionTab('somethingNew')).toBe('overview');
    expect(sectionTab(undefined)).toBe('overview');
  });
});

describe('the Overview network card', () => {
  it('names the network, the signal and the address of a Wi-Fi speaker', () => {
    const r = networkSummaryRows({ kind: 'wifi', ssid: 'Home', signal: 'GOOD_SIGNAL', ip: '192.0.2.10' });
    expect(r.rows).toEqual([
      ['settingsView.wlanSsid', 'Home'],
      ['settingsView.wlanSignal', en['signal.good']],
      ['settingsView.wlanIp', '192.0.2.10'],
    ]);
    expect(r.note).toBe('');
  });

  it('says when a speaker is on a cable or not connected', () => {
    expect(networkSummaryRows({ kind: 'wired', ip: '192.0.2.11' })).toEqual({ rows: [['settingsView.wlanIp', '192.0.2.11']], note: 'settingsView.wlanWired' });
    expect(networkSummaryRows({ kind: 'none' })).toEqual({ rows: [], note: 'settingsView.wlanNotConnected' });
    expect(networkSummaryRows(undefined).note).toBe('settingsView.wlanNotConnected');
  });

  it('leads to the Network tab with a named button', () => {
    const html = networkSummaryHTML({ kind: 'wifi', ssid: 'Home <x>', ip: '192.0.2.10' });
    expect(html).toContain(`<h3>${en['ui2.speakers.tab.network']}</h3>`);
    expect(html).toContain('Home &lt;x&gt;');
    expect(html).toMatch(/<button type="button" class="btn btn-mini sps-to-tab" data-to-tab="network">Network details<\/button>/);
  });
});

describe('the head', () => {
  const tile = (o = {}) => ({ offline: false, stock: false, playing: false, standby: false, inStereoPair: false, needsUpdate: false, noInternet: false, ...o });

  it('says starting up for a speaker whose agent is not answering yet, offline before anything else', () => {
    expect(speakerStateKind({ host: 'a', kind: 'str', strSilent: true }, tile())).toBe('startingUp');
    expect(speakerStateKind({ host: 'a', kind: 'str', strSilent: true, offline: true }, tile({ offline: true }))).toBe('notReachable');
    expect(speakerStateKind({ host: 'a', kind: 'str' }, tile({ playing: true }))).toBe('playing');
    expect(speakerStateKind({ host: 'a', kind: 'str' }, tile({ inStereoPair: true }))).toBe('stereoPair');
    expect(speakerStateKind(null, tile())).toBe('');
  });

  it('shows the state in words, the update chip and what plays', () => {
    const html = speakerStateHTML({ host: 'a', kind: 'str' }, tile({ playing: true, needsUpdate: true }), 'Radio <1>');
    expect(html).toContain(`<span class="u-pill-text">${en['ui2.pill.playing']}</span>`);
    expect(html).toContain('data-chip="update"');
    expect(html).toContain('Now playing: Radio &lt;1&gt;');
    expect(speakerStateHTML({ host: 'a', kind: 'str' }, tile(), '')).toBe('');
  });
});

describe('otherPairHalf', () => {
  const L = { host: '192.0.2.71', deviceID: 'F01', kind: 'str' };
  const R = { host: '192.0.2.72', deviceID: 'F02', kind: 'str' };
  const X = { host: '192.0.2.50', deviceID: 'AAA', kind: 'str' };
  const zl = { F01: { stereo: { masterDeviceID: 'F01', members: [{ deviceID: 'F01', ip: L.host, role: 'LEFT' }, { deviceID: 'F02', ip: R.host, role: 'RIGHT' }] } } };

  it('leads from either half of a live pair to the other', () => {
    expect(otherPairHalf(L, zl, [L, R, X])).toBe(R);
    expect(otherPairHalf(R, zl, [L, R, X])).toBe(L);
  });

  it('is null off a pair, without a known partner, or without a speaker', () => {
    expect(otherPairHalf(X, zl, [L, R, X])).toBe(null);
    expect(otherPairHalf(L, zl, [L, X])).toBe(null);
    expect(otherPairHalf(L, {}, [L, R])).toBe(null);
    expect(otherPairHalf(null, zl, [L, R])).toBe(null);
  });
});

describe('confirmations name the speaker', () => {
  it('in the preview only; the classic layout keeps its titles', () => {
    expect(speakerConfirmTitle('Restart speaker', 'Kitchen')).toBe('Restart speaker');
    state.ui2Shell = true;
    expect(speakerConfirmTitle('Restart speaker', 'Kitchen')).toBe('Kitchen: Restart speaker');
    expect(speakerConfirmTitle('Restart speaker', '')).toBe('Restart speaker');
  });

  it('every confirmation in Speaker settings goes through confirmFor with its speaker', () => {
    for (const m of MODULES) {
      expect(settingsSrc[m], `${m}.js calls confirmWarn directly`).not.toMatch(/[^.\w]confirmWarn\(/);
    }
    // Restart, factory reset and remove STR among them.
    const wire = srcOf('wireActionSections');
    for (const k of ["t('speaker.reboot')", "t('settingsView.trueFactoryResetConfirmTitle')", "t('settingsView.removeSTRConfirmTitle')"]) {
      expect(wire.replace(/\s+/g, ' ')).toContain(`confirmFor(box, ${k}`);
    }
    expect(wire).toContain('confirmWarn: (title, body, o) => confirmFor(box, title, body, o)');
  });
});

describe('the hooks in the classic view', () => {
  it('hand the settings body to the page after the shell and after every render', () => {
    expect(srcOf('loadBoxSettings')).toMatch(/mountSettingsShell\(\);[^\n]*\n\s*placeSettingsBody\(\);/);
    const render = srcOf('renderBoxSettings');
    expect(render.indexOf('arrangeSpeakerSections(ctx)')).toBeGreaterThan(render.indexOf('wireAdvancedSections(ctx)'));
    expect(srcOf('revealInSettings')).toContain('revealSpeakerTabOf(el)');
  });

  it('sort a body rendered before the shell mounted, once it lands in a tab', () => {
    const body = (groups, sections) => ({
      querySelector: (sel) => (sel === 'details.settings-group' && groups ? {} : null),
      querySelectorAll: () => sections.map((inTab) => ({ closest: () => (inTab ? {} : null) })),
    });
    expect(bodyUnsorted(body(true, []))).toBe(true);
    expect(bodyUnsorted(body(false, [true, false]))).toBe(true);
    expect(bodyUnsorted(body(false, [true, true]))).toBe(false);
    // The "speaker unreachable" or loading panels hold no section at all.
    expect(bodyUnsorted(body(false, []))).toBe(false);
    expect(bodyUnsorted(null)).toBe(false);
    const place = srcOf('placeSettingsBody');
    expect(place).toMatch(/bodyUnsorted\(body\)\)\s*\{\s*arrangeSpeakerSections\(lastArrangeCtx\)/);
    // The ctx is kept before the shell check, so a late mount has it.
    const arrange = srcOf('arrangeSpeakerSections');
    expect(arrange.indexOf('lastArrangeCtx = ctx')).toBeLessThan(arrange.indexOf('state.ui2Shell'));
  });

  it('do nothing without the shell', async () => {
    const m = await import('./speaker.js');
    expect(() => m.placeSettingsBody()).not.toThrow();
    expect(() => m.arrangeSpeakerSections({})).not.toThrow();
    expect(m.revealSpeakerTabOf({ closest: () => null })).toBe(false);
  });
});
