// The UI 2.0 preview's Radio (#1286 phase 2, docs/UI-2.0.md): the keys of the
// speaker chosen in the sidebar, Discover and Favorites as tabs, station rows
// whose actions name the station and the speaker, the technical data in the
// station details, feedback only after the speaker confirmed, and errors in
// the row they belong to. The last block checks that the current layout did
// not change.
//
//   E2E_PORT=4182 npx playwright test specs/desktop-ui2-radio.spec.mjs

import AxeBuilder from '@axe-core/playwright';
import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { KILL_ANIM, WCAG_TAGS } from '../helpers/a11y.mjs';
import { ROSTER, STATIONS } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const ROCK = STATIONS[0];
const JAZZ = STATIONS[3];

const rows = (page) => page.locator('#searchResults .ui2-st');
const row = (page, name) => rows(page).filter({ has: page.locator('.ui2-st-name', { hasText: name }) });
const tab = (page, id) => page.locator(`.ui2-radio-tab[data-tab="${id}"]`);

// waitForRowStatus advances the page clock a second at a time until the row says
// something final: playStation watches the stream for six seconds, and the
// agent's answers arrive between the steps.
async function waitForRowStatus(page, r, re) {
  await expect.poll(async () => {
    await runFor(page, 1000);
    return (await r.locator('.ui2-st-status').textContent()) || '';
  }, { timeout: 20_000 }).toMatch(re);
}

test.describe('UI 2.0 Radio', () => {
  test.use({ ui2: true });

  test.describe('keys', () => {
    test('1. names the speaker over its six keys and on every key', async ({ app }) => {
      const { page } = app;
      await expect(page.locator('#ui2KeysTitle')).toHaveText(`Keys on ${LR.friendlyName}`);
      await expect(page.locator('#presets')).toHaveAttribute('aria-labelledby', 'ui2KeysTitle');
      const keys = page.locator('#presets .preset');
      await expect(keys).toHaveCount(6);
      await expect(keys.nth(2)).toHaveAttribute('role', 'group');
      await expect(keys.nth(2).locator('.preset-body')).toHaveAttribute('role', 'button');
      await expect(keys.nth(2).locator('.preset-body')).toHaveAttribute('aria-label', new RegExp(`^Key 3 on ${LR.friendlyName}: NPR News`));
      await expect(keys.nth(2).locator('.ren')).toHaveAttribute('aria-label', `Rename key 3 on ${LR.friendlyName}`);
      await expect(keys.nth(2).locator('.del')).toHaveAttribute('aria-label', `Clear key 3 on ${LR.friendlyName}`);
      // The sidebar chooses the speaker; the speaker cards are not shown twice.
      await expect(page.locator('#boxSelect')).toBeHidden();
      await expect(page.locator('#view-box h1.ui2-radio-title')).toHaveText('Radio');
    });

    test('2. follows the speaker chosen in the sidebar', async ({ app }) => {
      const { page } = app;
      await page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${BR.host}"]`).click();
      await expect(page.locator('#ui2KeysTitle')).toHaveText(`Keys on ${BR.friendlyName}`);
      await expect(page.locator('#presets .preset-body').first()).toHaveAttribute('aria-label', new RegExp(`^Key 1 on ${BR.friendlyName}`));
      // The station rows name the new speaker in their actions too.
      await expect(row(page, ROCK.name).locator('[data-act="play"]')).toHaveAttribute('aria-label', `Play ${ROCK.name} on ${BR.friendlyName}`);
    });

    test('3. a key plays from the keyboard, on the speaker it names', async ({ app, mock }) => {
      const { page } = app;
      await mock.resetCalls();
      const key4 = page.locator('#presets .preset[data-slot="4"] .preset-body');
      await expect(key4).toHaveAttribute('role', 'button');
      await key4.focus();
      await expect(key4).toBeFocused();
      await page.keyboard.press('Enter');
      await mock.expectCalled('PlaySlot', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 4, times: 1 });
    });
  });

  test.describe('Discover', () => {
    test('4. opens with the top list, every action named with station and speaker', async ({ app }) => {
      const { page } = app;
      await expect(tab(page, 'discover')).toHaveAttribute('aria-selected', 'true');
      await expect(page.locator('#ui2RadioPanel')).toHaveAttribute('aria-labelledby', 'ui2RadioTab-discover');
      await expect(rows(page)).toHaveCount(STATIONS.length);
      const r = row(page, ROCK.name);
      await expect(r.locator('[data-act="play"]')).toHaveAttribute('aria-label', `Play ${ROCK.name} on ${LR.friendlyName}`);
      await expect(r.locator('[data-act="fav"]')).toHaveAttribute('aria-label', `Add ${ROCK.name} to favorites`);
      await expect(r.locator('[data-act="key"]')).toHaveAttribute('aria-label', `Put on key: ${ROCK.name} on ${LR.friendlyName}`);
      await expect(r.locator('[data-act="key"]')).toHaveText('Put on key');
      // The search field is part of Discover.
      await expect(page.locator('#searchQ')).toBeVisible();
    });

    test('5. keeps the technical data in the station details', async ({ app }) => {
      const { page } = app;
      const r = row(page, ROCK.name);
      const main = r.locator('.ui2-st-main');
      await expect(main).toBeVisible();
      for (const tech of [`${ROCK.bitrate} kbit/s`, ROCK.codec, ROCK.url]) await expect(main).not.toContainText(tech);
      const more = r.locator('[data-act="details"]');
      const panel = r.locator('.ui2-st-details');
      await expect(more).toHaveAttribute('aria-expanded', 'false');
      await expect(panel).toBeHidden();
      await more.click();
      await expect(more).toHaveAttribute('aria-expanded', 'true');
      await expect(panel).toBeVisible();
      await expect(panel).toContainText(`${ROCK.bitrate} kbit/s`);
      await expect(panel).toContainText(ROCK.codec);
      await expect(panel).toContainText(ROCK.url);
      await expect(panel).toContainText('Station was online at the last check');
      // A repaint (a filter change) keeps it open.
      await page.locator('#searchOnlyBose').uncheck();
      await expect(row(page, ROCK.name).locator('.ui2-st-details')).toBeVisible();
      await more.click();
      await expect(row(page, ROCK.name).locator('.ui2-st-details')).toBeHidden();
    });

    test('6. a station the directory could not reach says so in its row', async ({ app, mock }) => {
      const { page } = app;
      await mock.set('RadioSearchDetailed', { value: { relaxed: false, stations: [
        { name: 'Dead Air FM', url: 'http://example.com/dead', url_resolved: 'http://example.com/dead', stationuuid: 'dead', codec: 'MP3', bitrate: 128, lastcheckok: 0 },
      ] } });
      await page.locator('#topBtn').click();
      const r = row(page, 'Dead Air FM');
      await expect(r).toHaveClass(/is-broken/);
      await expect(r.locator('.ui2-st-broken')).toHaveText('Not reachable at the last directory check');
    });
  });

  test.describe('playing', () => {
    test.use({ fakeClock: true });

    test('7. "playing" only once the speaker reports it', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      await mock.resetCalls();
      const r = row(page, JAZZ.name);
      await r.locator('[data-act="play"]').click();
      await expect(r.locator('.ui2-st-status')).toHaveText(`Starting on ${LR.friendlyName}...`);
      await mock.expectCalled('PlayURL', { pred: (c) => onHost(LR.host)(c) && c.args[2] === JAZZ.url, times: 1 });
      await waitForRowStatus(page, r, /^Playing on /);
      await expect(r.locator('.ui2-st-status')).toHaveText(`Playing on ${LR.friendlyName}`);
      await expect(r.locator('.ui2-st-status')).toHaveClass(/is-ok/);
    });
  });

  test.describe('a station that does not play', () => {
    test.use({
      fakeClock: true,
      scenario: { routes: { '/api/stream-status': { body: { error: 'HTTP 403', url: ROCK.url, reason: 'blocked', status: 403 } } } },
    });

    test('8. reports in its own row, not in a toast', async ({ app }) => {
      const { page } = app;
      await settle(page, 9_000);
      const r = row(page, ROCK.name);
      await r.locator('[data-act="play"]').click();
      await waitForRowStatus(page, r, /^Did not play/);
      await expect(r.locator('.ui2-st-status')).toHaveClass(/is-error/);
      await expect(r.locator('.ui2-st-status')).toContainText(`Did not play on ${LR.friendlyName}. This stream is blocked`);
      await expect(page.locator('#toast')).not.toHaveClass(/show/);
      // The other rows are untouched.
      await expect(row(page, JAZZ.name).locator('.ui2-st-status')).toHaveText('');
    });
  });

  test.describe('Put on key', () => {
    test('9. the picker names the speaker and every key; done only after the speaker read back', async ({ app, mock }) => {
      const { page } = app;
      await mock.resetCalls();
      const r = row(page, JAZZ.name);
      await r.locator('[data-act="key"]').click();
      const modal = page.locator('#pickModal');
      await expect(modal).toBeVisible();
      await expect(page.locator('#pickTitle')).toHaveText(`Put ${JAZZ.name} on a key of ${LR.friendlyName}`);
      await expect(page.locator('#pickSub')).toHaveText(`Choose one of the six keys on ${LR.friendlyName}.`);
      const slot3 = page.locator('#pickGrid .pick-slot').nth(2);
      await expect(slot3).toHaveAttribute('aria-label', `Key 3 on ${LR.friendlyName}, now NPR News`);
      await slot3.click();
      await expect(modal).toBeHidden();
      await mock.expectCalled('SetPreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 3 && c.args[3] === JAZZ.name, times: 1 });
      await expect(r.locator('.ui2-st-status')).toHaveText(`On key 3 of ${LR.friendlyName}. The speaker confirmed it.`);
      await expect(page.locator('#presets .preset[data-slot="3"] .name')).toHaveText(JAZZ.name);
    });

    test.describe('when the speaker does not keep it', () => {
      // SetPreset answers ok but stores nothing: the read back shows the old key.
      test.use({ scenario: { bindings: { SetPreset: { value: null } } } });

      test('10. says the speaker did not confirm, in the row', async ({ app }) => {
        const { page } = app;
        const r = row(page, JAZZ.name);
        await r.locator('[data-act="key"]').click();
        await page.locator('#pickGrid .pick-slot').nth(2).click();
        await expect(r.locator('.ui2-st-status')).toHaveText(`${LR.friendlyName} did not confirm key 3. Check the key and try again.`);
        await expect(r.locator('.ui2-st-status')).toHaveClass(/is-warn/);
      });
    });

    test.describe('when the speaker refuses', () => {
      test.use({ scenario: { bindings: { SetPreset: { error: 'speaker busy' } } } });

      test('11. shows the refusal in the row, not in a window-wide dialog', async ({ app }) => {
        const { page } = app;
        const r = row(page, JAZZ.name);
        await r.locator('[data-act="key"]').click();
        await page.locator('#pickGrid .pick-slot').nth(4).click();
        await expect(r.locator('.ui2-st-status')).toContainText(`Key 5 of ${LR.friendlyName} was not changed: `);
        await expect(r.locator('.ui2-st-status')).toHaveClass(/is-error/);
        await expect(page.locator('#errorModal')).toBeHidden();
      });
    });
  });

  test.describe('Favorites', () => {
    test('12. a separate tab with the starred stations; the star and the key stay two actions', async ({ app }) => {
      const { page } = app;
      const star = row(page, ROCK.name).locator('[data-act="fav"]');
      await star.click();
      await expect(star).toHaveAttribute('aria-pressed', 'true');
      await expect(star).toHaveAttribute('aria-label', `Remove ${ROCK.name} from favorites`);

      await tab(page, 'favorites').click();
      await expect(tab(page, 'favorites')).toHaveAttribute('aria-selected', 'true');
      await expect(tab(page, 'discover')).toHaveAttribute('aria-selected', 'false');
      await expect(page.locator('#ui2RadioPanel')).toHaveAttribute('data-ui2-tab', 'favorites');
      await expect(rows(page)).toHaveCount(1);
      await expect(row(page, ROCK.name).locator('[data-act="key"]')).toBeVisible();
      // Favorites has no search field or filters.
      await expect(page.locator('#searchQ')).toBeHidden();
      await expect(page.locator('#searchCountry')).toBeHidden();

      // Unstarring on Favorites takes the row away and keeps the keyboard in the list.
      await row(page, ROCK.name).locator('[data-act="fav"]').click();
      await expect(rows(page)).toHaveCount(0);
      await expect(page.locator('#searchResults')).toContainText('No favorites yet');
      await expect(tab(page, 'favorites')).toBeFocused();

      await tab(page, 'discover').click();
      await expect(rows(page)).toHaveCount(STATIONS.length);
      await expect(page.locator('#searchQ')).toBeVisible();
    });

    test('13. the tabs work with the arrow keys', async ({ app }) => {
      const { page } = app;
      await tab(page, 'discover').focus();
      await page.keyboard.press('ArrowRight');
      await expect(tab(page, 'favorites')).toBeFocused();
      await expect(tab(page, 'favorites')).toHaveAttribute('aria-selected', 'true');
      await expect(tab(page, 'discover')).toHaveAttribute('tabindex', '-1');
      await page.keyboard.press('Home');
      await expect(tab(page, 'discover')).toBeFocused();
      await expect(tab(page, 'discover')).toHaveAttribute('aria-selected', 'true');
    });
  });

  test.describe('nothing disappears', () => {
    test('14. stream source, filters, transfer, refresh and the player stay reachable', async ({ app }) => {
      const { page } = app;
      for (const sel of ['#streamSourceDetails', '#searchCountry', '#searchOnlyBose', '#genreChips', '#presetTransferBtn', '#refreshBtn', '#addStationBox', '#ui2Player #pauseBtn']) {
        await expect(page.locator(sel), sel).toBeVisible();
      }
      await expect(page.locator('#refreshBtn')).toHaveAttribute('aria-label', /.+/);
    });
  });

  for (const theme of ['dark', 'light', 'contrast']) {
    test.describe(`axe, ${theme}`, () => {
      test.use({ theme });

      test(`15. the keys, the tabs and the rows (details open) have no WCAG findings (${theme})`, async ({ app }) => {
        const { page } = app;
        await expect(rows(page)).toHaveCount(STATIONS.length);
        await row(page, ROCK.name).locator('[data-act="details"]').click();
        await row(page, JAZZ.name).locator('[data-act="fav"]').click();
        await page.addStyleTag({ content: KILL_ANIM });
        const results = await new AxeBuilder({ page }).withTags(WCAG_TAGS)
          .include('.ui2-keys-head').include('#presets').include('.ui2-radio-tabs').include('.ui2-st-list')
          .analyze();
        const found = results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`);
        expect(found).toEqual([]);
      });
    });
  }

  test.describe('enlarged text in the smallest window', () => {
    test.use({ scale: '3', viewport: { width: 1000, height: 600 } });

    test('16. rows and keys fit without a horizontal scroll, targets stay 40px', async ({ app }) => {
      const { page } = app;
      await expect(rows(page)).toHaveCount(STATIONS.length);
      const main = page.locator('#ui2Main');
      const overflow = await main.evaluate((m) => m.scrollWidth - m.clientWidth);
      expect(overflow).toBeLessThanOrEqual(1);
      const r = row(page, ROCK.name);
      await r.scrollIntoViewIfNeeded();
      for (const act of ['play', 'fav', 'key', 'details']) {
        const box = await r.locator(`[data-act="${act}"]`).boundingBox();
        expect(box.height, act).toBeGreaterThanOrEqual(40);
        expect(box.width, act).toBeGreaterThanOrEqual(40);
      }
      const rowBox = await r.boundingBox();
      const mainBox = await main.boundingBox();
      expect(rowBox.x + rowBox.width).toBeLessThanOrEqual(mainBox.x + mainBox.width + 1);
    });
  });
});

test.describe('current layout', () => {
  test('17. Radio looks and works as before with the preview off', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('html')).not.toHaveClass(/ui2/);
    await expect(page.locator('#boxSelect')).toBeVisible();
    await expect(page.locator('.ui2-keys-head, .ui2-radio-tabs, .ui2-radio-title')).toHaveCount(0);
    await expect(page.locator('#presets .preset').first()).not.toHaveAttribute('role', /.*/);
    await expect(page.locator('#presets .preset-body').first()).not.toHaveAttribute('tabindex', /.*/);
    await page.locator('#topBtn').click();
    await expect(page.locator('#searchResults .result-row')).toHaveCount(STATIONS.length);
    await expect(page.locator('#searchResults .ui2-st')).toHaveCount(0);
    await expect(page.locator('#searchResults .result-row').first()).toContainText(`${ROCK.bitrate} kbit/s`);
  });
});
