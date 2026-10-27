// The classic Multi-room view (#1554, open decision 5): the fixed-group editor
// never offers an offline speaker as a member, and the group's lead speaker is
// called the main speaker everywhere.
//
//   E2E_PORT=4182 npx playwright test specs/desktop-multiroom.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const OFFICE = ROSTER.office;

test.use({ ui2: false });

async function openMultiroom(page) {
  await page.locator('.tab-btn[data-view="multiroom"]').click();
  await expect(page.locator('#view-multiroom')).toBeVisible();
  await expect(page.locator('#view-multiroom #zoneCreate')).toBeAttached();
}

const card = (page, box) => page.locator(`#view-multiroom .zone-card[data-id="${box.deviceID}"]`);

test.describe('fixed-group editor', () => {
  test('an offline speaker is shown disabled with the reason and cannot be ticked', async ({ app, mock }) => {
    const { page } = app;
    await openMultiroom(page);
    const office = card(page, OFFICE);
    await expect(office).toHaveAttribute('aria-disabled', 'true');
    await expect(office).toHaveClass(/\boffline\b/);
    await expect(office).toContainText(en('multiroom.offlineNotSelectable'));
    await expect(office).toHaveAttribute('title', en('multiroom.offlineNotSelectable'));
    // No "make main" on it either.
    await expect(office.locator('.zone-makemain')).toHaveCount(0);

    // Playwright itself sees the card as disabled (aria-disabled on a
    // role=button), so the click is forced to prove the handler ignores it.
    await expect(office).toBeDisabled();
    await office.click({ force: true });
    await expect(office).not.toHaveClass(/\bselected\b/);

    // An online speaker still ticks, and the group sent leaves Office out.
    // Living Room is made the main speaker first so Bedroom is a member.
    await card(page, LR).locator('.zone-makemain').click();
    await expect(card(page, LR)).toHaveClass(/\bmaster\b/);
    const bedroom = card(page, BR);
    await expect(bedroom).not.toHaveAttribute('aria-disabled', /.*/);
    await bedroom.click();
    await expect(card(page, BR)).toHaveClass(/\bselected\b/);
    await mock.resetCalls();
    await page.locator('#zoneCreate').click();
    await mock.expectCalled('FormZone', { times: 1 });
    const call = await mock.last('FormZone');
    expect(call.args[0]).toBe(LR.host);
    expect(JSON.stringify(call.args)).toContain(BR.host);
    expect(JSON.stringify(call.args)).not.toContain(OFFICE.host);
    expect(JSON.stringify(call.args)).not.toContain(OFFICE.deviceID);
  });

  test('the group notes call the lead speaker the main speaker, never the master', async ({ app }) => {
    const { page } = app;
    await openMultiroom(page);
    const view = page.locator('#view-multiroom');
    await expect(view.locator('.zone-name-note')).toHaveText(en('multiroom.groupNameNote'));
    await expect(view.locator('.zone-name-note')).toContainText('main speaker');
    await expect(view).not.toContainText(/master/i);
  });
});
