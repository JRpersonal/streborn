// The UI 2.0 preview's Radio states and row feedback (#1286 phase 5,
// docs/UI-2.0.md): a station directory that does not answer, the list on its
// way, the three empty lists with their way forward, a row whose play the
// speaker never confirms, and a speaker without internet, each said where it
// happened. The last block checks that the current layout did not change.
//
//   E2E_PORT=4182 npx playwright test specs/desktop-ui2-radio-states.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER, STATIONS } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const JAZZ = STATIONS[3];
const DOWN = 'all radio-browser mirrors failed';

const rows = (page) => page.locator('#searchResults .ui2-st');
const row = (page, name) => rows(page).filter({ has: page.locator('.ui2-st-name', { hasText: name }) });
const tab = (page, id) => page.locator(`.ui2-radio-tab[data-tab="${id}"]`);
const state = (page) => page.locator('#searchResults .lib2-state');

// reopenTop asks the directory again: the pressed Top button closes its list
// (#1521), a second click opens it on the directory's next answer.
async function reopenTop(page) {
  const top = page.locator('#topBtn');
  if ((await top.getAttribute('aria-pressed')) === 'true') await top.click();
  await top.click();
}

async function waitForRowStatus(page, r, re) {
  await expect.poll(async () => {
    await runFor(page, 1000);
    return (await r.locator('.ui2-st-status').textContent()) || '';
  }, { timeout: 30_000 }).toMatch(re);
}

test.describe('UI 2.0 Radio states', () => {
  test.use({ ui2: true });

  test.describe('the directory does not answer', () => {
    test.use({ scenario: { bindings: { RadioSearchDetailed: { error: DOWN } } } });

    test('1. says so in place of the list, and Try again asks again', async ({ app, mock }) => {
      const { page } = app;
      const err = state(page);
      await expect(err).toHaveClass(/lib2-state--error/);
      await expect(err).toHaveAttribute('role', 'alert');
      await expect(err.locator('.lib2-state-title')).toHaveText(en('ui2.feedback.stationsErrorTitle'));
      await expect(err.locator('.lib2-state-body')).toHaveText(en('ui2.feedback.stationsErrorBody'));
      await expect(err.locator('.lib2-state-detail')).toContainText(DOWN);
      await expect(rows(page)).toHaveCount(0);

      // A filter change repaints the list; the error stays.
      await page.locator('#ui2Filters > summary').click();
      await page.locator('#searchOnlyBose').uncheck();
      await expect(state(page).locator('.lib2-state-title')).toHaveText(en('ui2.feedback.stationsErrorTitle'));
      await expect(rows(page)).toHaveCount(0);

      // Try again: the directory answers now.
      await mock.set('RadioSearchDetailed', { value: { stations: STATIONS, relaxed: false } });
      await mock.resetCalls();
      const retry = page.locator('#ui2StationsRetry');
      await expect(retry).toHaveText(en('ui2.feedback.tryAgain'));
      await retry.click();
      await mock.expectCalled('RadioSearchDetailed', { times: 1 });
      await expect(rows(page)).toHaveCount(STATIONS.length);
      await expect(state(page)).toHaveCount(0);
    });

    test('2. the error does not take the keyboard off the tab that asked', async ({ app, mock }) => {
      const { page } = app;
      await expect(state(page)).toBeVisible();
      await tab(page, 'favorites').click();
      await mock.resetCalls();
      await tab(page, 'discover').focus();
      await page.keyboard.press('Enter');
      await mock.expectCalled('RadioSearchDetailed');
      await expect(state(page).locator('.lib2-state-title')).toHaveText(en('ui2.feedback.stationsErrorTitle'));
      await expect(tab(page, 'discover')).toBeFocused();
    });
  });

  test('3. the list on its way: a status line, the panel busy until it arrives', async ({ app, mock }) => {
    const { page } = app;
    await expect(rows(page)).toHaveCount(STATIONS.length);
    await mock.set('RadioSearchDetailed', { delayMs: 1500 });
    await reopenTop(page);
    const loading = page.locator('#searchResults .lib2-loading');
    await expect(loading).toHaveAttribute('role', 'status');
    await expect(loading).toHaveText(en('search.loadingStations'));
    await expect(page.locator('#ui2RadioPanel')).toHaveAttribute('aria-busy', 'true');
    await expect(rows(page)).toHaveCount(STATIONS.length, { timeout: 10_000 });
    await expect(page.locator('#ui2RadioPanel')).toHaveAttribute('aria-busy', 'false');
  });

  test.describe('empty lists', () => {
    test('4. nothing matches: Clear filters asks again without them, and the guide opens', async ({ app, mock }) => {
      const { page } = app;
      await expect(rows(page)).toHaveCount(STATIONS.length);
      await mock.set('RadioSearchDetailed', { value: { stations: [], relaxed: false } });
      await page.locator('#ui2Filters > summary').click();
      await page.locator('#searchCountry').selectOption('DE');
      const empty = state(page);
      await expect(empty.locator('.lib2-state-title')).toHaveText(en('ui2.feedback.noMatchTitle'));
      await expect(empty).toHaveAttribute('role', 'status');

      await mock.set('RadioSearchDetailed', { value: { stations: STATIONS, relaxed: false } });
      await mock.resetCalls();
      await page.locator('#ui2ClearFilters').click();
      await mock.expectCalled('RadioSearchDetailed', { pred: (c) => c.args[0] && c.args[0].cc === '' });
      await expect(page.locator('#searchCountry')).toHaveValue('');
      await expect(rows(page)).toHaveCount(STATIONS.length);
    });

    test('5. nothing matches: "How to add a missing station" opens the guide on its summary', async ({ app, mock }) => {
      const { page } = app;
      await expect(rows(page)).toHaveCount(STATIONS.length);
      await mock.set('RadioSearchDetailed', { value: { stations: [], relaxed: false } });
      await reopenTop(page);
      const add = page.locator('#ui2AddMissing');
      await expect(add).toHaveText(en('ui2.feedback.addMissing'));
      await page.locator('#addStationBox').evaluate((d) => { d.open = false; });
      await add.click();
      await expect(page.locator('#addStationBox')).toHaveAttribute('open', '');
      await expect(page.locator('#addStationBox > summary')).toBeFocused();
    });

    test('6. the speaker filter hides everything: "Show HTTPS stations too" turns it off', async ({ app, mock }) => {
      const { page } = app;
      await expect(rows(page)).toHaveCount(STATIONS.length);
      const opus = { name: 'Opus Only FM', url: 'https://example.com/opus', url_resolved: 'https://example.com/opus', stationuuid: 'opus', codec: 'OGG', bitrate: 128, lastcheckok: 1 };
      await mock.set('RadioSearchDetailed', { value: { stations: [opus], relaxed: false } });
      await reopenTop(page);
      await expect(page.locator('#searchOnlyBose')).toBeChecked();
      await expect(state(page).locator('.lib2-state-title')).toHaveText(en('ui2.feedback.boseOnlyTitle'));
      await page.locator('#ui2ShowHttps').click();
      await expect(page.locator('#searchOnlyBose')).not.toBeChecked();
      await expect(row(page, opus.name)).toBeVisible();
    });

    test('7. no favorites yet: says how to keep one, and Open Discover goes there', async ({ app }) => {
      const { page } = app;
      await expect(rows(page)).toHaveCount(STATIONS.length);
      await tab(page, 'favorites').click();
      const empty = state(page);
      await expect(empty.locator('.lib2-state-title')).toHaveText(en('ui2.feedback.noFavTitle'));
      await expect(empty.locator('.lib2-state-body')).toHaveText(en('ui2.feedback.noFavBody'));
      await page.locator('#ui2OpenDiscover').click();
      await expect(tab(page, 'discover')).toHaveAttribute('aria-selected', 'true');
      await expect(tab(page, 'discover')).toBeFocused();
      await expect(rows(page)).toHaveCount(STATIONS.length);
    });
  });

  test.describe('a play the speaker does not confirm', () => {
    // The speaker takes the URL but keeps playing what it played before.
    test.use({ fakeClock: true, scenario: { bindings: { PlayURL: { value: null } } } });

    test('8. says "Sent", and "Playing" only once the speaker plays it', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      const r = row(page, JAZZ.name);
      await r.locator('[data-act="play"]').click();
      await expect(r.locator('.ui2-st-status')).toHaveText(en('ui2.radio.starting', { speaker: LR.friendlyName }));
      await waitForRowStatus(page, r, /^Sent to /);
      await expect(r.locator('.ui2-st-status')).toHaveText(en('ui2.feedback.sentNotStarted', { speaker: LR.friendlyName }));
      await expect(r.locator('.ui2-st-status')).toHaveClass(/is-warn/);
      await expect(r.locator('.ui2-st-status')).not.toContainText('Playing');

      // Now the speaker plays it: the status poll upgrades the row.
      await page.evaluate(([host, url, name]) => {
        window.__strMock.state.item[host] = { name, location: url, art: '' };
      }, [LR.host, JAZZ.url, JAZZ.name]);
      await mock.resetCalls();
      await waitForRowStatus(page, r, /^Playing on /);
      await expect(r.locator('.ui2-st-status')).toHaveText(en('ui2.radio.playing', { speaker: LR.friendlyName }));
      await expect(r.locator('.ui2-st-status')).toHaveClass(/is-ok/);
    });
  });

  test.describe('a speaker without internet', () => {
    test.use({
      fakeClock: true,
      scenario: { routes: { '/api/stream-status': { body: { error: 'offline', url: JAZZ.url, reason: 'offline', status: 0 } } } },
    });

    test('9. says it in the row with the speaker\'s Wi-Fi as a button, no window-wide dialog', async ({ app }) => {
      const { page } = app;
      await settle(page, 9_000);
      const r = row(page, JAZZ.name);
      await r.locator('[data-act="play"]').click();
      await waitForRowStatus(page, r, /internet/);
      await expect(r.locator('.ui2-st-status')).toHaveText(en('ui2.feedback.noInternet', { speaker: LR.friendlyName }));
      await expect(r.locator('.ui2-st-status')).toHaveClass(/is-error/);
      await expect(page.locator('#warnModal')).toBeHidden();
      const fix = r.locator('.ui2-st-fix');
      await expect(fix).toHaveText(en('ui2.feedback.wifiOf', { speaker: LR.friendlyName }));
      // A repaint (a filter change) keeps the line and its button.
      await page.locator('#ui2Filters > summary').click();
      await page.locator('#searchOnlyBose').uncheck();
      await expect(row(page, JAZZ.name).locator('.ui2-st-fix')).toBeVisible();
      await row(page, JAZZ.name).locator('.ui2-st-fix').click();
      await runFor(page, 500);
      await expect(page.locator('#view-box')).toBeHidden();
    });
  });
});

test.describe('current layout', () => {
  test.describe('the directory does not answer', () => {
    test.use({ scenario: { bindings: { RadioSearchDetailed: { error: DOWN } } } });

    test('10. keeps its one muted line, no UI 2.0 state', async ({ app }) => {
      const { page } = app;
      await page.locator('#topBtn').click();
      await expect(page.locator('#searchResults .muted')).toContainText(DOWN);
      await expect(page.locator('#searchResults .lib2-state')).toHaveCount(0);
      await expect(page.locator('#ui2StationsRetry')).toHaveCount(0);
    });
  });

  test.describe('a speaker without internet', () => {
    test.use({
      fakeClock: true,
      scenario: { routes: { '/api/stream-status': { body: { error: 'offline', url: STATIONS[0].url, reason: 'offline', status: 0 } } } },
    });

    test('11. keeps its dialog', async ({ app }) => {
      const { page } = app;
      await settle(page, 9_000);
      await page.locator('#topBtn').click();
      await page.locator('#searchResults .result-row').first().locator('.play-now').click();
      await expect.poll(async () => {
        await runFor(page, 1000);
        return page.locator('#warnModal').isVisible();
      }, { timeout: 20_000 }).toBe(true);
    });
  });
});
