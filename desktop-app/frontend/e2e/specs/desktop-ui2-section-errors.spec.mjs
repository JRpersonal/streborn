// UI 2.0 phase 5: a setting that fails on a speaker's page says so in its own
// card, not in a dialog across the window (docs/UI-2.0.md, "Decided for
// later", rule 3; views/settings/shared.js reportError, shell2/speakers/
// sectionmsg.js). The line names the speaker and what it answered, Details
// still opens the copyable error dialog, a setting that could not be read
// stays on screen with Try again (rule 4), and a value the user typed wrong
// is marked on the field. The classic layout keeps its error dialog.
//
//   E2E_PORT=4183 npx playwright test specs/desktop-ui2-section-errors.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';
import { onHost } from '../helpers/mockclient.mjs';

const LR = ROSTER.livingRoom;

const nav = (page, view) => page.locator(`#ui2Sidebar .ui2-nav-item[data-view="${view}"]`);
const tab = (page, key) => page.locator(`#ui2SpkTab-${key}`);
const card = (page, key) => page.locator(`#ui2SpkPanels [data-ui2-sec="${key}"]`);

async function openSpeaker(page, host, name) {
  await nav(page, 'speakers').click();
  await page.locator(`#view-speakers .spl-open[data-host="${host}"]`).click();
  await expect(page.locator('#ui2SpkPageName')).toHaveText(name);
}

const FAILING = {
  SetBoxName: { error: 'refused' },
  // A speaker with a display, so the display message toggle shows.
  GetDisplayMessages: { value: { enabled: true, hasDisplay: true } },
  SetDisplayMessages: { error: 'refused' },
  // The splash setting does not answer at all (not a 404: the agent has it).
  GetDisplaySplash: { error: 'timeout' },
  RebootBox: { error: 'refused' },
};

test.describe('UI 2.0 preview: errors where they happen', () => {
  test.use({ ui2: true, scenario: { bindings: FAILING } });

  test('1. a refused rename is a line in the Name card naming the speaker, and Details opens the dialog', async ({ app, mock }) => {
    const { page } = app;
    await openSpeaker(page, LR.host, LR.friendlyName);
    await page.locator('#boxNameInput').fill('Lounge');
    await page.locator('#boxNameSave').click();
    await mock.expectCalled('SetBoxName', { pred: onHost(LR.host) });
    const line = card(page, 'name').locator('.sps-msg[role="alert"]');
    await expect(line).toBeVisible();
    await expect(line).toContainText(LR.friendlyName);
    await expect(line).toContainText('refused');
    await expect(page.locator('#errorModal')).toBeHidden();
    // Only this card carries it.
    await expect(page.locator('#ui2SpkPanels .sps-msg')).toHaveCount(1);
    await line.getByRole('button', { name: 'Details' }).click();
    await expect(page.locator('#errorModal')).toBeVisible();
    await expect(page.locator('#errorText')).toHaveValue(/refused/);
  });

  test('2. a refused Sound toggle reports in its card and paints the old state back', async ({ app, mock }) => {
    const { page } = app;
    await openSpeaker(page, LR.host, LR.friendlyName);
    await tab(page, 'sound').click();
    const section = page.locator('#displayMsgSection');
    await expect(section).toBeVisible();
    await expect(page.locator('#displayMsgOn')).toHaveClass(/active/);
    await page.locator('#displayMsgOff').click();
    await mock.expectCalled('SetDisplayMessages', { pred: onHost(LR.host) });
    const line = section.locator('.sps-msg[role="alert"]');
    await expect(line).toContainText(LR.friendlyName);
    await expect(line).toContainText('refused');
    await expect(page.locator('#errorModal')).toBeHidden();
    await expect(page.locator('#displayMsgOn')).toHaveClass(/active/);
    // Close takes it away.
    await line.getByRole('button', { name: 'Close' }).click();
    await expect(section.locator('.sps-msg')).toHaveCount(0);
  });

  test('3. a refused restart reports in the Maintenance card', async ({ app, mock }) => {
    const { page } = app;
    await openSpeaker(page, LR.host, LR.friendlyName);
    await tab(page, 'maintenance').click();
    await page.locator('#boxRebootBtn').click();
    await page.locator('#warnConfirm').click();
    await mock.expectCalled('RebootBox', { pred: onHost(LR.host) });
    const line = card(page, 'actions').locator('.sps-msg[role="alert"]');
    await expect(line).toContainText(LR.friendlyName);
    await expect(line).toContainText('refused');
    await expect(page.locator('#errorModal')).toBeHidden();
  });

  test('4. a setting that could not be read stays visible with Try again', async ({ app, mock }) => {
    const { page } = app;
    await openSpeaker(page, LR.host, LR.friendlyName);
    await tab(page, 'sound').click();
    const section = page.locator('#displaySplashSection');
    await expect(section).toBeVisible();
    const status = section.locator('.u-sec-status');
    await expect(status).toContainText(`Could not read this setting from ${LR.friendlyName}.`);
    await expect(page.locator('#displaySplashOn')).toHaveAttribute('aria-disabled', 'true');
    await expect(page.locator('#displaySplashOff')).toHaveAttribute('aria-disabled', 'true');
    // A press on an unread setting is refused.
    await mock.resetCalls();
    await page.locator('#displaySplashOn').click({ force: true });
    expect(await mock.count('SetDisplaySplash')).toBe(0);
    // Try again reads it again; this time it answers.
    await mock.set('GetDisplaySplash', { value: { supported: true, enabled: true } });
    await status.getByRole('button', { name: 'Try again' }).click();
    await mock.expectCalled('GetDisplaySplash', { pred: onHost(LR.host) });
    await expect(section.locator('.u-sec-status')).toHaveCount(0);
    await expect(page.locator('#displaySplashOn')).toHaveClass(/active/);
    await expect(page.locator('#displaySplashOn')).not.toHaveAttribute('aria-disabled', 'true');
  });

  test('5. a webhook without an address is a field error on the address', async ({ app }) => {
    const { page } = app;
    await openSpeaker(page, LR.host, LR.friendlyName);
    await tab(page, 'keys').click();
    await page.locator('#webhookSection > summary').click();
    await page.locator('#webhookOn').click();
    if (await page.locator('#webhookType').count()) await page.locator('#webhookType').selectOption('http');
    await page.locator('#webhookUrl').fill('');
    await page.locator('#webhookSaveBtn').click();
    const url = page.locator('#webhookUrl');
    await expect(url).toHaveAttribute('aria-invalid', 'true');
    const describedBy = await url.getAttribute('aria-describedby');
    expect(describedBy).toContain('webhookUrl-err');
    await expect(page.locator('#webhookUrl-err')).toBeVisible();
    await expect(page.locator('#webhookUrl-err')).toHaveAttribute('role', 'alert');
    await expect(page.locator('#errorModal')).toBeHidden();
    // Typing clears the mark.
    await url.fill('http://192.0.2.1/hook');
    await expect(url).not.toHaveAttribute('aria-invalid', 'true');
  });
});

test.describe('classic layout: the error dialog as before', () => {
  test.use({ ui2: false, scenario: { bindings: FAILING } });

  test('6. the same refused rename opens #errorModal, and nothing is written into the card', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="settings"]').click();
    await expect(page.locator('#boxNameInput')).toBeAttached();
    // Classic Speaker settings are an accordion; open every group.
    await page.evaluate(() => document.querySelectorAll('#settingsBody details').forEach((d) => { d.open = true; }));
    await page.locator('#boxNameInput').fill('Lounge');
    await page.locator('#boxNameSave').click();
    await mock.expectCalled('SetBoxName', { pred: onHost(LR.host) });
    await expect(page.locator('#errorModal')).toBeVisible();
    await expect(page.locator('#errorText')).toHaveValue(/refused/);
    await expect(page.locator('.sps-msg')).toHaveCount(0);
  });

  test('7. an unreadable setting stays hidden in the classic layout', async ({ app }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="settings"]').click();
    await expect(page.locator('#boxNameInput')).toBeAttached();
    await expect(page.locator('#displayMsgSection')).not.toHaveClass(/hidden/);
    await expect(page.locator('#displaySplashSection')).toHaveClass(/hidden/);
    await expect(page.locator('.u-sec-status')).toHaveCount(0);
  });
});
