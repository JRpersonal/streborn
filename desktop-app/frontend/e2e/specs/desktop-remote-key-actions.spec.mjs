// The remote key map offers the new built-in actions (#1679 shuffle and
// repeat, #1242 fixed sleep lengths) on the thumbs keys only, marks a key
// that carries shuffle or repeat with an "S" or "R", and says on Back and
// Forward that they step through the radio keys and go back a song on
// Spotify (#1619, #1678). Same picker in the classic layout and on the
// UI 2.0 speaker page.
//
//   E2E_PORT=4190 npx playwright test specs/desktop-remote-key-actions.spec.mjs
//   (SHOOT_DIR=<dir> also writes the screenshots)

import { test, expect } from '../fixtures.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';
import { mergeScenario, speakersScenario } from '../../testkit/scenario.mjs';

const LR = ROSTER.livingRoom;
const KEYS = { GetKeyActions: { value: { keys: { thumbsUp: 'shuffle', thumbsDown: 'sleep30' } } } };

// shoot saves the picker with the remote: the whole section where it fits the
// window (classic), else the window around the picker (the UI 2.0 page
// scrolls inside its own panel, which an element shot would cut off).
async function shoot(page, name, anchor = '#keyActionSelect') {
  if (!process.env.SHOOT_DIR) return;
  const path = `${process.env.SHOOT_DIR}/${name}.png`;
  if (name.startsWith('ui2')) {
    // The roster's old speaker asks for an update; the shot is about the keys.
    const later = page.getByRole('button', { name: 'Later' });
    if (await later.isVisible()) await later.click();
    await page.locator(anchor).evaluate((el) => el.scrollIntoView({ block: 'center' }));
    await page.screenshot({ path, animations: 'disabled' });
    return;
  }
  await page.locator('#webhookSection').screenshot({ path, animations: 'disabled' });
}

async function checkPicker(page, mock, shot) {
  const select = page.locator('#keyActionSelect');
  // Thumbs up carries shuffle: picker shows it, the key wears the "S".
  await page.locator('.rc-keyg[data-key="thumbsUp"]').click();
  await expect(select).toBeVisible();
  await expect(select).toHaveValue('shuffle');
  await expect(page.locator('.rc-pmark[data-pmark="thumbsUp"]')).toHaveText('S');
  await expect(page.locator('.rc-pmark[data-pmark="thumbsUp"]')).toBeVisible();
  await expect(page.locator('#sleepKeyNote')).toContainText('Spotify playlist');
  for (const v of ['shuffle', 'repeat', 'sleep15', 'sleep30', 'sleep60', 'sleep90']) {
    await expect(select.locator(`option[value="${v}"]`)).toBeEnabled();
  }
  // Thumbs down carries a fixed 30-minute sleep timer: the moon marks it.
  await page.locator('.rc-keyg[data-key="thumbsDown"]').click();
  await expect(select).toHaveValue('sleep30');
  await expect(page.locator('.rc-smark[data-smark="thumbsDown"]')).toBeVisible();
  await expect(page.locator('#sleepKeyNote')).toContainText('30 minutes');
  // Switching it to repeat saves and swaps the mark for an "R".
  await mock.resetCalls();
  await select.selectOption('repeat');
  await mock.expectCalled('SaveKeyActions');
  await expect(page.locator('.rc-pmark[data-pmark="thumbsDown"]')).toHaveText('R');
  await shoot(page, `${shot}-keymap`);
  // AUX takes snooze only: the new actions are not offered there.
  await page.locator('.rc-keyg[data-key="aux"]').click();
  for (const v of ['shuffle', 'repeat', 'sleep30']) {
    await expect(select.locator(`option[value="${v}"]`)).toBeHidden();
  }
  // Back explains what it does now.
  await page.locator('.rc-keyg[data-key="prev"]').click();
  await expect(page.locator('#webhookModeNote')).toContainText('step through your radio keys');
  await shoot(page, `${shot}-back-key`, '#webhookModeNote');
}

test.describe('classic layout: remote key actions', () => {
  test.use({ ui2: false, scenario: { bindings: KEYS } });

  test('the picker offers shuffle, repeat and the fixed sleep lengths', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="settings"]').click();
    await page.evaluate(() => document.querySelectorAll('#settingsBody details').forEach((d) => { d.open = true; }));
    await expect(page.locator('#webhookKeyMap')).toBeVisible();
    await checkPicker(page, mock, 'classic');
  });
});

test.describe('UI 2.0: remote key actions on the speaker page', () => {

  test.use({ ui2: true, scenario: mergeScenario(speakersScenario(), { bindings: KEYS }) });

  test('the keys tab offers the same picker', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('#ui2Sidebar .ui2-nav-item[data-view="speakers"]').click();
    await page.locator(`#view-speakers .spl-open[data-host="${LR.host}"]`).click();
    await expect(page.locator('#ui2SpkPageName')).toHaveText(LR.friendlyName);
    await page.locator('#ui2SpkTab-keys').click();
    await page.locator('#webhookSection > summary').click();
    await expect(page.locator('#webhookKeyMap')).toBeVisible();
    await checkPicker(page, mock, 'ui2');
  });
});
