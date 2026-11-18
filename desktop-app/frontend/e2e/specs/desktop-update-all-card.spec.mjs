// desktop-update-all-card: the speaker update card names the speakers it is
// about to update and says what the update does to them; its button is the
// owner's yes. Update all used to follow that yes with a second window listing
// the same speakers and asking again (#1954). Now the run goes straight to its
// progress window, which keeps saying what happens to the speakers, and asks a
// second time only about what its pre-scan finds that the card could not know.
// The "Update all" button in Speaker Settings names nothing, so it still asks
// with the whole list.
//
//   E2E_PORT=4193 npx playwright test specs/desktop-update-all-card.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const HALL = ROSTER.oldAgent;
const BEDROOM = ROSTER.bedroom;
const OLD = { version: 'v1.0.10', build: '2026-09-30' };
const NO_STICK = { '/api/stick/status': { status: 200, body: { mounted: false } } };
const ROUTED = { mode: 'agent-restart', chassis: 'bco', agentVersion: 'v1.0.10', reason: 'updating over SSH and restarting only the agent' };
const WARN = { mode: 'warn', chassis: 'unknown', agentVersion: 'v1.0.10', reason: 'agent v1.0.10 reboots after an update and the chassis could not be read' };

function twoBehind(restartPlan) {
  return {
    ui2: false,
    scenario: {
      boxes: ['livingRoom', 'bedroom', 'office', 'oldAgent'],
      boxPatches: { bedroom: OLD },
      routes: NO_STICK,
      bindings: { RunFleetUpdate: { never: true }, BoxRestartPlan: restartPlan },
    },
  };
}

const shot = async (loc, name) => {
  if (process.env.SHOOT_DIR) await loc.screenshot({ path: `${process.env.SHOOT_DIR}/${name}.png` });
};

test.describe('nothing new found', () => {
  test.use(twoBehind({ value: ROUTED }));

  test('the card\'s button starts the run and opens its progress window, no second question', async ({ app, mock }) => {
    const { page } = app;
    const card = page.locator('#speakerUpdateCard');
    await expect(card).toContainText(HALL.friendlyName);
    await expect(card).toContainText(BEDROOM.friendlyName);
    await card.locator('#sucUpdate').click();
    await mock.expectCalled('RunFleetUpdate', { times: 1 });
    await expect(page.locator('#warnModal')).toBeHidden();
    const overlay = page.locator('#updateAllOverlay');
    await expect(overlay).toBeVisible();
    // What happens to the speakers stays on screen while they restart.
    await expect(overlay.locator('#uaLead')).toContainText(en('updateAll.confirmLead'));
    await expect(overlay.locator('.ua-row')).toHaveCount(2);
    await mock.emit('box:update:progress', { host: HALL.host, pct: 40, bytesPerSec: 250000 });
    await shot(overlay.locator('.modal-content'), 'update-all-card-after');
  });
});

test.describe('the pre-scan found something', () => {
  test.use(twoBehind({ value: ROUTED, byHost: { [HALL.host]: { value: WARN } } }));

  test('it asks once more, about the note only, and Cancel leaves the speakers alone', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('#speakerUpdateCard #sucUpdate').click();
    const body = page.locator('#warnBody');
    await expect(body).toContainText(en('updateAll.confirmLeadNotes'));
    await expect(body.locator('.warn-notes')).toContainText(en('updateAll.noteDarkRisk', { name: HALL.friendlyName }));
    // The speakers the card already named are not listed a second time.
    await expect(body).not.toContainText(en('updateAll.confirmLead'));
    await expect(body.locator('ul:not(.warn-notes)')).toHaveCount(0);
    await shot(page.locator('#warnModal .modal-content'), 'update-all-card-note');
    await page.locator('#warnCancel').click();
    await expect(page.locator('#warnModal')).toBeHidden();
    expect(await mock.count('RunFleetUpdate')).toBe(0);
  });

  test('confirming it starts the run, with the note kept in the progress window', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('#speakerUpdateCard #sucUpdate').click();
    await expect(page.locator('#warnBody .warn-notes')).toBeVisible();
    await page.locator('#warnConfirm').click();
    await mock.expectCalled('RunFleetUpdate', { times: 1 });
    await expect(page.locator('#uaLead')).toContainText(en('updateAll.noteDarkRisk', { name: HALL.friendlyName }));
  });
});

test.describe('Update all in Speaker Settings', () => {
  test.use(twoBehind({ value: ROUTED }));

  test('names nothing itself, so it still asks with the whole list', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="settings"]').click();
    await page.locator('#settingsUpdateAllBtn').click();
    const body = page.locator('#warnBody');
    await expect(body).toContainText(en('updateAll.confirmLead'));
    await expect(body).toContainText(HALL.friendlyName);
    await expect(body).toContainText(BEDROOM.friendlyName);
    expect(await mock.count('RunFleetUpdate')).toBe(0);
    await page.locator('#warnConfirm').click();
    await mock.expectCalled('RunFleetUpdate', { times: 1, pred: () => true });
    await mock.expectCalled('BoxRestartPlan', { pred: onHost(HALL.host) });
  });
});
