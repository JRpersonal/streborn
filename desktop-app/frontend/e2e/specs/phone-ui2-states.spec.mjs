// phone-ui2-states: the phone remote's states in the UI 2.0 preview, each
// next to the current layout's behaviour for the same answer, which must not
// change. Offline after boot, a one-speaker home, an empty key store, an
// unreachable peer, and a play refused from the Find tab.

import { test, expect } from '../fixtures.mjs';
import { PHONE } from '../../testkit/demo-data.mjs';

const NAME = PHONE.SETTINGS.info.name;

async function bootDone(page) {
  await expect(page.locator('#presets .preset').first()).toBeVisible();
}

async function preview(page) {
  await page.addInitScript(() => { try { localStorage.setItem('str.ui2Preview', '1'); } catch (e) { /* none */ } });
  await page.reload();
  await expect(page.locator('html')).toHaveClass(/(^|\s)ui2(\s|$)/);
}

// Both layouts, the same test body; ui2 says which one runs.
const LAYOUTS = [['preview', true], ['current layout', false]];

// The speaker and the peer list stop answering after boot; the status poll
// needs two failures in a row before it says so (NET_FAIL_QUIET).
async function goOffline(phone) {
  const { page, handler } = phone;
  await expect(page.locator('#peers a.peer')).toHaveCount(PHONE.PEERS.length);
  handler.options.overrides = { ...handler.options.overrides, '/api/status': { abort: true }, '/api/peers': { abort: true } };
  await page.evaluate(async () => { await window.refreshStatus(); await window.refreshStatus(); await window.loadPeers(); });
}

for (const [label, ui2] of LAYOUTS) {
  test.describe(label, () => {
    test('a speaker that stops answering after boot', async ({ phone }) => {
      const { page, handler } = phone;
      if (ui2) await preview(page);
      await bootDone(page);
      await goOffline(phone);
      const offline = await page.evaluate(() => window.T.offline);
      // The pinned line still writes the general offline text.
      await expect(page.locator('#status .now')).toHaveText(offline);
      await expect(page.locator('#statusCard')).toHaveClass(/\boffline\b/);
      if (ui2) {
        await expect(page.locator('#u2Status')).toHaveText('Offline');
        await expect(page.locator('#u2Offline')).toBeVisible();
        await expect(page.locator('#u2OfflineTxt')).toContainText(`${NAME} is not answering right now.`);
        await expect(page.locator('#u2ShowList')).toHaveText('Switch to another speaker');
        // Greyed in words and colour, not faded.
        expect(await page.locator('#statusCard').evaluate((e) => getComputedStyle(e).opacity)).toBe('1');
        // Nothing disappears: the list keeps every speaker it knew.
        await page.locator('#u2ShowList').click();
        await expect(page.locator('#u2List')).toBeVisible();
        await expect(page.locator('#peers a.peer')).toHaveCount(PHONE.PEERS.length);
        await expect(page.locator('#tabSpeakers')).toBeVisible();
        // Try again asks the speaker again.
        const asked = () => handler.calls.filter((c) => new URL(c.url).pathname === '/api/status').length;
        const n = asked();
        await page.locator('#u2Retry').click();
        await expect.poll(asked).toBeGreaterThan(n);
      } else {
        await expect(page.locator('#u2Offline')).toBeHidden();
        expect(await page.locator('#statusCard').evaluate((e) => getComputedStyle(e).opacity)).toBe('0.6');
        // The current layout hides the list and the tab on no answer, as before.
        await expect(page.locator('#tabSpeakers')).toBeHidden();
        await expect(page.locator('#peersCard')).toBeHidden();
      }
      expect(phone.errors).toEqual([]);
    });

    test.describe('a failed key read on a fresh page', () => {
      test.use({ phoneOverrides: { '/api/presets': { status: 500, body: '' } } });

      test('names the speaker and offers Try again in the preview only', async ({ phone }) => {
        const { page } = phone;
        if (ui2) await preview(page);
        const note = page.locator('#presets .grid-note');
        await expect(note).toBeVisible();
        if (ui2) {
          await expect(note).toContainText(`${NAME} is not answering right now.`);
          await expect(note.locator('button')).toHaveText('Try again');
        } else {
          await expect(note).toHaveText(await page.evaluate(() => window.T.offline));
          await expect(note.locator('button')).toHaveCount(0);
        }
      });
    });

    test.describe('a one-speaker home', () => {
      test.use({ phoneOverrides: { '/api/peers': { body: [] } } });

      test('the Speakers tab', async ({ phone }) => {
        const { page, handler } = phone;
        if (ui2) await preview(page);
        await bootDone(page);
        await expect.poll(() => handler.calls.some((c) => new URL(c.url).pathname === '/api/peers')).toBe(true);
        if (ui2) {
          await expect(page.locator('#tabSpeakers')).toBeVisible();
          await page.locator('#tabSpeakers').click();
          await expect(page.locator('#u2OnlyOne')).toBeVisible();
          await expect(page.locator('#u2OnlyOneTxt')).toHaveText(`Only ${NAME} so far. Other speakers with ST Reborn on the same network show up here by themselves.`);
          // The list under the card shows this speaker alone, and says why.
          await page.locator('#dev').click();
          await expect(page.locator('#peers > *')).toHaveCount(1);
          await expect(page.locator('#peers .u2-self')).toContainText(NAME);
          await expect(page.locator('#lblPeersHint')).toContainText(`Only ${NAME} so far.`);
        } else {
          await page.evaluate(() => window.loadPeers());
          await expect(page.locator('#tabSpeakers')).toBeHidden();
          await expect(page.locator('#u2OnlyOne')).toBeHidden();
        }
      });
    });

    test.describe('an empty key store', () => {
      test.use({ phoneOverrides: { '/api/presets': { body: [] } } });

      test('the note under the keys', async ({ phone }) => {
        const { page } = phone;
        if (ui2) await preview(page);
        const note = page.locator('#presets .grid-note');
        await expect(note).toBeVisible();
        await expect(page.locator('#presets .preset.empty')).toHaveCount(6);
        if (ui2) {
          await expect(note).toContainText(`No keys are saved on ${NAME} yet.`);
          const go = note.locator('button.u2-link');
          await expect(go).toHaveText('Find radio & music');
          await go.click();
          await expect(page.locator('#panelBrowse')).toBeVisible();
          await expect(page.locator('#tabBrowse')).toHaveAttribute('aria-selected', 'true');
        } else {
          await expect(note).toHaveText(await page.evaluate(() => window.T.noPresets));
          await expect(note.locator('button')).toHaveCount(0);
        }
      });
    });

    test.describe('an unreachable peer', () => {
      test.use({
        phoneOverrides: {
          '/api/peers': { body: [{ ...PHONE.PEERS[0], reachable: false }, PHONE.PEERS[1]] },
        },
      });

      test('stays in the list, named', async ({ phone }) => {
        const { page } = phone;
        if (ui2) await preview(page);
        await bootDone(page);
        if (ui2) await page.locator('#dev').click();
        else await page.locator('#tabSpeakers').click();
        const gone = page.locator('#peers a.peer[aria-disabled="true"]');
        await expect(gone).toHaveCount(1);
        await expect(gone).toContainText(PHONE.PEERS[0].name);
        const opacity = await gone.evaluate((e) => getComputedStyle(e).opacity);
        if (ui2) {
          await expect(gone).toContainText('Offline');
          expect(opacity).toBe('1');
        } else {
          await expect(gone).not.toContainText('Offline');
          expect(opacity).toBe('0.5');
        }
      });
    });

    test.describe('a play refused from a Find result', () => {
      test.use({ phoneOverrides: { '/api/play': { status: 409, body: { error: 'stereo-incomplete', reason: 'partner-gone' } } } });

      test('says why where the tap happened', async ({ phone }) => {
        const { page } = phone;
        if (ui2) await preview(page);
        await bootDone(page);
        await page.locator('#tabBrowse').click();
        await page.locator('#q').fill('rock');
        await page.locator('#btnSearch').click();
        await page.locator('#results .station').first().locator('button').first().click();
        const broken = await page.evaluate(() => window.T.pairBroken);
        // Both layouts keep the notice under the keys.
        await expect(page.locator('#moveCard')).toContainText(broken);
        if (ui2) {
          await expect(page.locator('#searchMsg')).toHaveText(broken);
          await expect(page.locator('#searchMsg')).toHaveClass(/\bbad\b/);
          await expect(page.locator('#panelBrowse')).toBeVisible();
        } else {
          // The current layout's fix waits for a decision; it stays as it was.
          await expect(page.locator('#searchMsg')).not.toContainText(broken);
        }
      });
    });

    test.describe('a play refused without a known code', () => {
      test.use({ phoneOverrides: { '/api/play': { status: 502, body: { error: 'Station could not be played' } } } });

      test('the Find tab line', async ({ phone }) => {
        const { page } = phone;
        if (ui2) await preview(page);
        await bootDone(page);
        await page.locator('#tabBrowse').click();
        await page.locator('#q').fill('rock');
        await page.locator('#btnSearch').click();
        await page.locator('#results .station').first().locator('button').first().click();
        if (ui2) await expect(page.locator('#searchMsg')).toHaveText('Station could not be played');
        else await expect(page.locator('#searchMsg')).toHaveText(await page.evaluate(() => window.T.cantStart));
      });
    });
  });
}
