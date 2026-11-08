// desktop-update-running: a speaker update that is still running is shown
// clearly on that speaker in the classic layout (#1764): its tile in the
// speaker picker carries a bold "Update running" with a turning ring and the
// whole hint as tooltip, and Speaker Settings puts the hint over the update's
// progress line. The UI 2.0 side is desktop-ui2-speakers-list.spec.mjs 3b.
//
// RunSpeakerUpdate never answers, so the update stays on for the whole test;
// the bytes it streams arrive as box:update:progress, as from the backend.
//
//   E2E_PORT=4185 npx playwright test specs/desktop-update-running.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { en } from '../helpers/i18n.mjs';

const OLD_AGENT = '192.0.2.54';
const LIVING = 'demobox.local';

test.use({
  ui2: false,
  scenario: {
    boxes: ['livingRoom', 'bedroom', 'office'],
    current: 'oldAgent',
    banners: { speakerUpdate: true },
    routes: { '/api/stick/status': { status: 200, body: { mounted: false } } },
    bindings: { RunSpeakerUpdate: { never: true } },
  },
});

test('the updating speaker is marked on its tile and over the progress line', async ({ app, mock }) => {
  const { page } = app;
  const hint = en('speaker.updateRunningHint');
  await page.locator('#speakerUpdateCard #sucUpdate').click();
  await mock.expectCalled('RunSpeakerUpdate', { times: 1, pred: onHost(OLD_AGENT) });
  await mock.emit('box:update:progress', { host: OLD_AGENT, pct: 40, bytesPerSec: 250000 });

  // Speaker Settings: the notice over the progress line, bold, with the ring,
  // and the button cannot start a second update.
  const notice = page.locator('#view-settings .update-running');
  await expect(notice).toHaveCount(1);
  await expect(notice).toHaveText(hint);
  await expect(notice).toHaveAttribute('role', 'status');
  await expect(notice.locator('.update-running-spin')).toBeVisible();
  expect(Number(await notice.evaluate((e) => getComputedStyle(e).fontWeight))).toBeGreaterThanOrEqual(700);
  await expect(page.locator('#stickInfoUpdateBtn')).toBeDisabled();
  await expect(page.locator('#stickInfoUpdateStatus')).toContainText('40');
  // A second progress event keeps one notice.
  await mock.emit('box:update:progress', { host: OLD_AGENT, pct: 60, bytesPerSec: 250000 });
  await expect(page.locator('#stickInfoUpdateStatus')).toContainText('60');
  await expect(notice).toHaveCount(1);

  // The speaker picker: the chip on that speaker's tile, and on no other.
  const tile = page.locator(`.box-btn[data-host="${OLD_AGENT}"]`);
  const chip = tile.locator('.box-updating');
  await expect(chip).toHaveText(en('speaker.updateRunningBadge'));
  await expect(chip).toHaveAttribute('title', hint);
  await expect(tile.locator('.box-update-chip')).toHaveCount(0);
  await expect(page.locator('.box-btn .box-updating')).toHaveCount(1);
  await expect(page.locator(`.box-btn[data-host="${LIVING}"] .box-updating`)).toHaveCount(0);
});
