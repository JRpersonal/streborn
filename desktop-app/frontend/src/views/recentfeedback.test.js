// Recently played (phase 5): the history read names the speakers that did not
// answer, so the UI 2.0 preview can say whose history is missing; the current
// layout ignores them and draws exactly what it drew before.
import { describe, it, expect, vi, beforeEach } from 'vitest';

const api = vi.hoisted(() => ({ RecentPlayed: vi.fn(), GetPresets: vi.fn(), Status: vi.fn() }));
vi.mock('../api.js', async (importOriginal) => ({ ...(await importOriginal()), ...api }));

const KITCHEN = { host: '192.0.2.1', port: 8888, friendlyName: 'Kitchen', deviceID: 'device-id-k', kind: 'str' };
const OFFICE = { host: '192.0.2.2', port: 8888, friendlyName: 'Office', deviceID: 'device-id-o', kind: 'str' };
const ENTRY = { ts: '2026-10-23T18:42:00Z', source: 'radio', cardKey: 'r:rock', cardName: 'Rock FM', cardArt: '', cardURL: 'http://radio.example/rock', track: '' };

// A stand-in for the few nodes refreshRecentList touches.
function fakeDocument() {
  const nodes = {};
  const node = (id) => {
    if (!nodes[id]) {
      nodes[id] = {
        id, innerHTML: '', textContent: '', onclick: null,
        contains: () => false,
        querySelector: () => null,
        querySelectorAll: () => [],
        setAttribute() {}, removeAttribute() {}, getAttribute: () => null, focus() {},
      };
    }
    return nodes[id];
  };
  return { getElementById: node, querySelectorAll: () => [], activeElement: null, nodes };
}

let recent;
let state;

beforeEach(async () => {
  vi.resetModules();
  Object.values(api).forEach((f) => f.mockReset());
  globalThis.document = fakeDocument();
  ({ state } = await import('../state.js'));
  (await import('../i18n/index.js')).setLocale('en');
  state.boxes = [KITCHEN, OFFICE];
  state.currentBox = KITCHEN;
  state.recentAllBoxes = true;
  state.ui2Shell = false;
  state.view = 'recent';
  api.GetPresets.mockResolvedValue([]);
  api.Status.mockResolvedValue('<nowPlaying source="STANDBY"></nowPlaying>');
  recent = await import('./recent.js');
});

describe('loadRecentCards', () => {
  it('names a speaker whose history read failed', async () => {
    api.RecentPlayed.mockImplementation(async (host) => {
      if (host === OFFICE.host) throw new Error('speaker did not answer');
      return [ENTRY];
    });
    const { cards, unanswered } = await recent.loadRecentCards();
    expect(unanswered).toEqual(['Office']);
    expect(cards.map((c) => c.name)).toEqual(['Rock FM']);
  });

  it('names nobody when every speaker answered, even with an empty history', async () => {
    api.RecentPlayed.mockImplementation(async (host) => (host === OFFICE.host ? null : [ENTRY]));
    const { unanswered } = await recent.loadRecentCards();
    expect(unanswered).toEqual([]);
  });
});

describe('the note above the list (preview)', () => {
  it('names the speakers and offers Try again', () => {
    const html = recent.unansweredNoteHTML(['Office', 'Bedroom']);
    expect(html).toContain('role="status"');
    expect(html).toContain('Office, Bedroom did not answer, so their history is missing here.');
    expect(html).toContain('id="recentUnansweredRetry"');
    expect(recent.unansweredNoteHTML([])).toBe('');
  });
});

describe('the current layout', () => {
  it('draws the same list whether a speaker failed or answered empty', async () => {
    api.RecentPlayed.mockImplementation(async (host) => {
      if (host === OFFICE.host) throw new Error('speaker did not answer');
      return [ENTRY];
    });
    await recent.refreshRecentList();
    const failed = document.getElementById('recentList').innerHTML;

    api.RecentPlayed.mockImplementation(async (host) => (host === OFFICE.host ? [] : [ENTRY]));
    await recent.refreshRecentList();
    const answered = document.getElementById('recentList').innerHTML;

    expect(failed).toBe(answered);
    expect(failed).toContain('Rock FM');
    expect(failed).not.toContain('rc-unanswered');
    expect(failed).not.toContain('did not answer');
  });

  it('keeps its plain empty line', async () => {
    api.RecentPlayed.mockRejectedValue(new Error('speaker did not answer'));
    await recent.refreshRecentList();
    const { t } = await import('../i18n/index.js');
    expect(document.getElementById('recentList').innerHTML).toBe(`<div class="recent-empty">${t('recent.empty')}</div>`);
  });
});
