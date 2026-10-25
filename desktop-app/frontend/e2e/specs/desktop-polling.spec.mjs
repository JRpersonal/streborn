// desktop-polling: how often the desktop app asks the speakers, per layout
// and view. The speakers' firmware cannot take a tight poll (main.js, the
// note above STATUS_FAST_MS: a BCO box reboots under a 2 s status poll), so
// every cadence the app runs is pinned here as a count per minute.
//
// Method, the same for every case: boot on the mock under the fake clock,
// navigate, settle 30 s (the start-up timers and the first polls are done),
// forget the calls, run the clock one minute, count the window.go bindings by
// name and by speaker. Agent page.route calls are never counted (their number
// at the end of runFor is not deterministic, helpers/clock.mjs).
//
// The timers behind the numbers (src/main.js):
//   - the status loop (nextStatusDelayMs, STATUS_FAST_MS 5000 on the Music
//     tab while playing, STATUS_PLAYER_MS 15000 for the UI 2.0 player off that
//     tab, STATUS_IDLE_MS 15000 otherwise): Status on the current speaker, and
//     GetQueue + BoxSettings (volume) while the player is on screen;
//   - the every-speaker poll (15 s): one round of refreshMusicZones (one
//     GetZoneState per STR speaker, offline ones included) and refreshBoxPlaying
//     (one Status per online STR speaker), both behind an 8 s debounce. It runs
//     on the Music tab, and with the UI 2.0 sidebar on every view where the
//     player is on screen (shellListsSpeakers: not Setup, not during an OTA),
//     and only while the window is visible;
//   - the 60 s known-speaker refresh: one RefreshKnownBoxes, whose list
//     (applyBoxList) triggers one more zone and playing round, so even a view
//     without the every-speaker poll sees exactly one round a minute;
//   - the stock re-probe (45 s, only while a listed speaker reads as stock):
//     one more RefreshKnownBoxes each.
//
// The roster is livingRoom (current, demobox.local, playing), bedroom
// (192.0.2.52, standby) and office (192.0.2.53, offline); kitchen (stock)
// only in case 13.
//
// Ranges are the counts one run observed, plus or minus one. A count that is
// a gate rather than a cadence (an offline speaker, a stock speaker, a poll
// that must not run in this view) is exact.

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle, runFor } from '../helpers/clock.mjs';

const CUR = 'demobox.local';
const BED = '192.0.2.52';
const OFF = '192.0.2.53';
const KITCHEN = '192.0.2.51';
const OLD_AGENT = '192.0.2.54';
const ROSTER3 = ['livingRoom', 'bedroom', 'office'];

// The minute that is counted, after the 30 s settle.
const SETTLE_MS = 30_000;
const WINDOW_MS = 60_000;

async function nav(page, ui2, view) {
  const sel = ui2 ? `#ui2Sidebar .ui2-nav-item[data-view="${view}"]` : `.tab-btn[data-view="${view}"]`;
  await page.locator(sel).click();
  await expect(page.locator(`#view-${view}`)).toBeVisible();
}

// tally reads the counts the EXPECT rows name, from one pull of the calls.
async function tally(mock, current = CUR) {
  const calls = await mock.calls();
  const n = (name, host) => calls.filter((c) => c.name === name && (host === undefined || c.args[0] === host)).length;
  return {
    statusCur: n('Status', current),
    statusBed: n('Status', BED),
    statusOff: n('Status', OFF),
    zonesCur: n('GetZoneState', CUR),
    zonesBed: n('GetZoneState', BED),
    zonesOff: n('GetZoneState', OFF),
    zonesAll: n('GetZoneState'),
    queue: n('GetQueue'),
    refreshKnown: n('RefreshKnownBoxes'),
    discover: n('DiscoverBoxes'),
    calls,
  };
}

// expectCounts checks every key of want: a number is exact, [lo, hi] a range.
function expectCounts(got, want, label) {
  for (const [k, w] of Object.entries(want)) {
    const msg = `${label}: ${k} in ${WINDOW_MS / 1000} s`;
    if (Array.isArray(w)) {
      expect(got[k], msg).toBeGreaterThanOrEqual(w[0]);
      expect(got[k], msg).toBeLessThanOrEqual(w[1]);
    } else {
      expect(got[k], msg).toBe(w);
    }
  }
}

async function countMinute(page, mock, current) {
  await settle(page, SETTLE_MS);
  await mock.resetCalls();
  await runFor(page, WINDOW_MS);
  return tally(mock, current);
}

// Gates that hold in every row: the offline office is never asked for its
// status (refreshBoxPlaying drops offline speakers), nobody sweeps (the list
// is never empty, so the minute tick re-probes instead of discovering), and
// the minute tick fires exactly once in any 60 s window.
const ALWAYS = { statusOff: 0, discover: 0, refreshKnown: 1 };

// Cases 1 to 7. Observed per minute (run 2026-10-25):
//   view                 statusCur  statusBed  zones/host  queue
//   classic Music          16          4          4         12
//   classic Library         5          1          1          0
//   classic Settings        5          1          1          0
//   UI 2.0 Radio (box)     16          4          4         12
//   UI 2.0 Library          8          4          4          4
//   UI 2.0 Settings         8          4          4          4
//   UI 2.0 Setup            5          1          1          0
// statusCur = the status loop (12 at 5 s, 4 at 15 s) + one per playing round
// (4 every-speaker rounds, or the 1 round of the minute tick).
const EXPECT = [
  {
    no: 1, ui2: false, view: 'box', label: 'classic Music tab, playing',
    want: { statusCur: [15, 17], statusBed: [3, 5], zonesCur: [3, 5], zonesBed: [3, 5], zonesOff: [3, 5], queue: [11, 13] },
  },
  {
    // No every-speaker poll off the Music tab in the classic layout: the one
    // round is the minute tick's. The status loop is at the idle 15 s and the
    // player (queue, volume) is not on screen.
    no: 2, ui2: false, view: 'library', label: 'classic Library',
    want: { statusCur: [4, 6], statusBed: [0, 2], zonesCur: [0, 2], zonesBed: [0, 2], zonesOff: [0, 2], queue: 0 },
  },
  {
    no: 3, ui2: false, view: 'settings', label: 'classic Speaker settings',
    want: { statusCur: [4, 6], statusBed: [0, 2], zonesCur: [0, 2], zonesBed: [0, 2], zonesOff: [0, 2], queue: 0 },
  },
  {
    no: 4, ui2: true, view: 'box', label: 'UI 2.0 Radio view, playing',
    want: { statusCur: [15, 17], statusBed: [3, 5], zonesCur: [3, 5], zonesBed: [3, 5], zonesOff: [3, 5], queue: [11, 13] },
  },
  {
    // The sidebar lists every speaker here too, so the every-speaker poll runs
    // (4 rounds), while the player's own poll stays at the slow 15 s even
    // though the speaker plays (STATUS_PLAYER_MS, the watchdog note).
    no: 5, ui2: true, view: 'library', label: 'UI 2.0 Library',
    want: { statusCur: [7, 9], statusBed: [3, 5], zonesCur: [3, 5], zonesBed: [3, 5], zonesOff: [3, 5], queue: [3, 5] },
  },
  {
    no: 6, ui2: true, view: 'settings', label: 'UI 2.0 Speaker settings',
    want: { statusCur: [7, 9], statusBed: [3, 5], zonesCur: [3, 5], zonesBed: [3, 5], zonesOff: [3, 5], queue: [3, 5] },
  },
  {
    // Setup hides the player (playerOnScreen), so neither the every-speaker
    // poll nor the queue poll runs: only the minute tick's round is left.
    no: 7, ui2: true, view: 'setup', label: 'UI 2.0 Setup',
    want: { statusCur: [4, 6], statusBed: [0, 2], zonesCur: [0, 2], zonesBed: [0, 2], zonesOff: [0, 2], queue: 0 },
  },
];

for (const row of EXPECT) {
  test.describe(`case ${row.no}: ${row.label}`, () => {
    test.use({ ui2: row.ui2, fakeClock: true, scenario: { boxes: ROSTER3 } });

    test('polls per minute stay in the expected ranges', async ({ app, mock }) => {
      const { page } = app;
      await nav(page, row.ui2, row.view);
      const got = await countMinute(page, mock);
      expectCounts(got, { ...ALWAYS, ...row.want }, row.label);
    });
  });
}

test.describe('case 8: the every-speaker poll on the Library view, classic vs UI 2.0', () => {
  test.use({ ui2: false, fakeClock: true, scenario: { boxes: ROSTER3 } });

  test('the classic Library has no every-speaker rounds, the UI 2.0 Library has them', async ({ app, mock }) => {
    const { page } = app;
    await nav(page, false, 'library');
    const classic = await countMinute(page, mock);
    // One round, and it is the minute tick's: as many zone rounds as list
    // refreshes. The 15 s poll added none.
    expect(classic.refreshKnown).toBe(1);
    expect(classic.zonesBed, 'classic Library: zone rounds').toBe(classic.refreshKnown);
    expect(classic.statusBed, 'classic Library: playing rounds').toBe(classic.refreshKnown);

    // Same tab, same mock state: switch the preview on and reload, the way the
    // "Aa" menu does (uipreview.js).
    await page.evaluate(() => localStorage.setItem('str.ui2Preview', '1'));
    await page.reload();
    await expect(page.locator('html.ui2 #ui2Shell')).toBeVisible();
    await nav(page, true, 'library');
    const ui2 = await countMinute(page, mock);
    expect(ui2.refreshKnown).toBe(1);
    expect(ui2.zonesBed, 'UI 2.0 Library: zone rounds').toBeGreaterThanOrEqual(3);
    expect(ui2.zonesBed, 'UI 2.0 Library: zone rounds').toBeLessThanOrEqual(5);
    expect(ui2.statusBed, 'UI 2.0 Library: playing rounds').toBeGreaterThanOrEqual(3);
    expect(ui2.statusBed, 'UI 2.0 Library: playing rounds').toBeLessThanOrEqual(5);
    expect(ui2.zonesBed).toBeGreaterThan(classic.zonesBed);
  });
});

// Cases 9 and 10: a speaker update is running. Nothing may poll a speaker that
// is about to restart: no every-speaker round, no minute tick (it skips while
// otaInProgress), only the status loop on the current speaker at 15 s.
// The current speaker is oldAgent (Hallway, v1.0.10), which the speaker-update
// card offers; RunSpeakerUpdate never answers, so the OTA stays in progress
// for the whole minute. /api/stick/status says no stick is in, so the
// "stick left in" pre-flight does not ask.
const OTA = {
  boxes: ROSTER3,
  current: 'oldAgent',
  banners: { speakerUpdate: true },
  routes: { '/api/stick/status': { status: 200, body: { mounted: false } } },
  bindings: { RunSpeakerUpdate: { never: true } },
};
const OTA_WANT = {
  statusCur: [3, 5], statusBed: 0, statusOff: 0,
  zonesAll: 0, refreshKnown: 0, discover: 0,
};

async function startOta(page, mock) {
  await page.locator('#speakerUpdateCard #sucUpdate').click();
  await mock.expectCalled('RunSpeakerUpdate', { times: 1, pred: onHost(OLD_AGENT) });
}

test.describe('case 9: classic Music tab while a speaker update runs', () => {
  test.use({ ui2: false, fakeClock: true, scenario: OTA });

  test('no every-speaker rounds and no known-speaker refresh', async ({ app, mock }) => {
    const { page } = app;
    await startOta(page, mock);
    // The update moved the window to Speaker settings; the Music tab is the
    // one view where the classic layout runs the every-speaker poll.
    await nav(page, false, 'box');
    const got = await countMinute(page, mock, OLD_AGENT);
    expectCounts(got, OTA_WANT, 'classic Music, OTA');
    // Only the speaker being updated is asked, and only by the status loop.
    expect(got.calls.filter((c) => c.name === 'Status').every((c) => c.args[0] === OLD_AGENT)).toBe(true);
  });
});

test.describe('case 10: UI 2.0 Library while a speaker update runs', () => {
  test.use({ ui2: true, fakeClock: true, scenario: OTA });

  test('the sidebar stops polling every speaker during the update', async ({ app, mock }) => {
    const { page } = app;
    await startOta(page, mock);
    await nav(page, true, 'library');
    const got = await countMinute(page, mock, OLD_AGENT);
    expectCounts(got, { ...OTA_WANT, queue: 0 }, 'UI 2.0 Library, OTA');
    expect(got.calls.filter((c) => c.name === 'Status').every((c) => c.args[0] === OLD_AGENT)).toBe(true);
  });
});

test.describe('case 11: UI 2.0 window hidden', () => {
  // The minute tick's list refresh triggers a zone round of its own
  // (applyBoxList), visible or not, which is a different mechanism from the
  // every-speaker poll this case is about. RefreshKnownBoxes answers an empty
  // list here, so that round is skipped (`quick.length` is 0) and every
  // GetZoneState in the window comes from the 15 s poll alone.
  test.use({ ui2: true, fakeClock: true, scenario: { boxes: ROSTER3, bindings: { RefreshKnownBoxes: { value: [] } } } });

  test('no zone poll while the window is hidden, and it runs again once visible', async ({ app, mock }) => {
    const { page } = app;
    await nav(page, true, 'library');
    await settle(page, SETTLE_MS);

    await page.evaluate(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => true });
      document.dispatchEvent(new Event('visibilitychange'));
    });
    await mock.resetCalls();
    await runFor(page, WINDOW_MS);
    const hidden = await tally(mock);
    expect(hidden.refreshKnown, 'the minute tick still ran').toBe(1);
    expect(hidden.zonesAll, 'GetZoneState while hidden').toBe(0);
    expect(hidden.statusBed, 'other speakers asked while hidden').toBe(0);

    // Control: the same minute visible has the 4 rounds of case 5.
    await page.evaluate(() => {
      delete document.visibilityState;
      delete document.hidden;
      document.dispatchEvent(new Event('visibilitychange'));
    });
    await expect.poll(() => page.evaluate(() => document.visibilityState)).toBe('visible');
    await mock.resetCalls();
    await runFor(page, WINDOW_MS);
    const visible = await tally(mock);
    expect(visible.zonesBed).toBeGreaterThanOrEqual(3);
    expect(visible.zonesBed).toBeLessThanOrEqual(5);
  });
});

test.describe('case 12: current speaker idle on the classic Music tab', () => {
  test.use({ ui2: false, fakeClock: true, scenario: { boxes: ROSTER3, playing: { livingRoom: 'STANDBY' } } });

  test('the status loop drops to the idle 15 s cadence', async ({ app, mock }) => {
    const { page } = app;
    await nav(page, false, 'box');
    const got = await countMinute(page, mock);
    // 4 status-loop readings at STATUS_IDLE_MS + 4 every-speaker rounds,
    // where case 1 (playing) has 12 + 4.
    expectCounts(got, {
      ...ALWAYS,
      statusCur: [7, 9], statusBed: [3, 5], zonesCur: [3, 5], zonesBed: [3, 5], zonesOff: [3, 5], queue: [3, 5],
    }, 'classic Music, idle');
  });
});

test.describe('case 13: a stock speaker in the list', () => {
  test.use({ ui2: true, fakeClock: true, scenario: { boxes: [...ROSTER3, 'kitchen'] } });

  test('the stock re-probe adds known-speaker refreshes, the stock speaker is never polled', async ({ app, mock }) => {
    const { page } = app;
    await nav(page, true, 'box');
    const got = await countMinute(page, mock);
    // Observed 3: the minute tick plus two 45 s re-probes in this window (the
    // re-probe timer's phase puts two of its ticks inside this minute; any
    // 60 s window holds one or two of them, so 2 to 3 in theory, 3 observed,
    // range 2 to 4 per the plus-or-minus-one rule).
    expect(got.refreshKnown, 'RefreshKnownBoxes with a stock speaker listed').toBeGreaterThanOrEqual(2);
    expect(got.refreshKnown).toBeLessThanOrEqual(4);
    expect(got.discover).toBe(0);
    // A stock speaker has no agent: no Status, no zone, nothing.
    expect(got.calls.filter((c) => c.args && c.args[0] === KITCHEN).map((c) => c.name)).toEqual([]);
    // The STR speakers keep case 4's cadence.
    expectCounts(got, { statusCur: [15, 17], statusBed: [3, 5], statusOff: 0, zonesBed: [3, 5] }, 'UI 2.0 Radio with a stock speaker');
  });
});
