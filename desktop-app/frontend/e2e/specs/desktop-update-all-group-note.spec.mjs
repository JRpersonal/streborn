// desktop-update-all-group-note: updating all speakers says beforehand that a
// grouped speaker leaves its group while it restarts for the update, like the
// single-speaker update does (#1339). The speaker update card, the Settings
// banner and the confirmation before the run all carry the note when a speaker
// of the run sits in a live group, and none of them when the group is not
// touched by the run or the only grouping is a stereo pair.
//
//   E2E_PORT=4188 npx playwright test specs/desktop-update-all-group-note.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const HALL = ROSTER.oldAgent;
const OLD = { version: 'v1.0.10', build: '2026-09-30' };
const NOTE = en('updateAll.groupNote');

// Bedroom and Hallway are both behind, so "Update all" is on offer.
function scenario(zones) {
  return { boxes: ['livingRoom', 'bedroom', 'office', 'kitchen', 'oldAgent'], boxPatches: { bedroom: OLD }, zones };
}

// Living Room leads a group. Its member decides whether the run touches it.
function group(member, extra = {}) {
  const live = { master: LR.deviceID, senderIP: LR.host, members: [{ deviceID: LR.deviceID, ip: LR.host }, { deviceID: member.deviceID, ip: member.host }], ...extra };
  return { [LR.host]: live, [member.host]: live };
}

async function openSettings(page) {
  await page.locator('.tab-btn[data-view="settings"]').click();
  await expect(page.locator('#settingsUpdateAllBtn')).toBeVisible();
}

test.describe('a speaker of the run is in a live group', () => {
  test.use({ scenario: scenario(group(HALL)) });

  test('the update card, the Settings banner and the confirmation say it', async ({ app }) => {
    const { page } = app;
    const card = page.locator('#speakerUpdateCard');
    await expect(card).toBeVisible();
    await expect(card.locator('#sucGroupNote')).toHaveText(NOTE);
    if (process.env.SHOOT_DIR) await card.screenshot({ path: `${process.env.SHOOT_DIR}/desktop-update-card-group-note.png` });

    await openSettings(page);
    await expect(page.locator('#updateAllGroupNote')).toHaveText(NOTE);
    // One note, not the single-speaker one beside it.
    await expect(page.locator('#updateGroupNote')).toHaveCount(0);
    if (process.env.SHOOT_DIR) await page.locator('#view-settings .update-banner').screenshot({ path: `${process.env.SHOOT_DIR}/desktop-settings-group-note.png` });

    await page.locator('#settingsUpdateAllBtn').click();
    const modal = page.locator('.warn-notes');
    await expect(modal).toContainText(NOTE);
    if (process.env.SHOOT_DIR) await page.locator('.modal:not(.hidden) .modal-content').last().screenshot({ path: `${process.env.SHOOT_DIR}/desktop-confirm-group-note.png` });
  });
});

test.describe('the group is not touched by the run', () => {
  test.use({ scenario: scenario(group({ deviceID: 'E1E2E3E4E5E6', host: '192.0.2.70' })) });

  test('no group note anywhere', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('#speakerUpdateCard')).toBeVisible();
    await expect(page.locator('#sucGroupNote')).toHaveCount(0);
    await openSettings(page);
    await expect(page.locator('#updateAllGroupNote')).toHaveCount(0);
  });
});

test.describe('the only grouping is a stereo pair', () => {
  test.use({ scenario: scenario(group(HALL, { stereo: {
    id: 'pair-demo', masterDeviceID: LR.deviceID, name: 'Living pair',
    members: [{ deviceID: LR.deviceID, ip: LR.host, role: 'LEFT' }, { deviceID: HALL.deviceID, ip: HALL.host, role: 'RIGHT' }],
  } })) });

  test('no group note: a pair survives the restart', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('#speakerUpdateCard')).toBeVisible();
    await expect(page.locator('#sucGroupNote')).toHaveCount(0);
    await openSettings(page);
    await expect(page.locator('#updateAllGroupNote')).toHaveCount(0);
  });
});
