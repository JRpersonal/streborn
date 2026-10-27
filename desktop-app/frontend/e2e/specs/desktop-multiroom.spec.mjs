// The classic Multi-room view (#1554, open decision 5): the fixed-group editor
// never offers an offline speaker as a member, the stereo editor says it
// changes a pair that already exists instead of creating one, and the group's
// lead speaker is called the main speaker everywhere.
//
//   E2E_PORT=4182 npx playwright test specs/desktop-multiroom.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const OFFICE = ROSTER.office;

// Two more SoundTouch 10s that are a live stereo pair (the shape of
// demo-data.mjs UI2_PAIR). Bedroom is a SoundTouch 10 too, so it is the
// third pair candidate.
const ST10 = (n, id, name) => ({ ...BR, name, friendlyName: name, host: `192.0.2.${n}`, deviceID: id, volume: undefined });
const PAIR_L = ST10(61, 'C3D4E5F6A1B2', 'Terrace left');
const PAIR_R = ST10(62, 'D4E5F6A1B2C3', 'Terrace right');
const PAIR = {
  stereo: {
    id: 'pair-demo',
    masterDeviceID: PAIR_L.deviceID,
    name: 'Terrace',
    members: [
      { deviceID: PAIR_L.deviceID, ip: PAIR_L.host, role: 'LEFT' },
      { deviceID: PAIR_R.deviceID, ip: PAIR_R.host, role: 'RIGHT' },
    ],
  },
};

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

test.describe('stereo editor', () => {
  test.use({
    scenario: {
      boxes: ['livingRoom', 'bedroom', 'office', 'kitchen', PAIR_L, PAIR_R],
      zones: { [PAIR_L.host]: PAIR, [PAIR_R.host]: PAIR },
    },
  });

  test('an existing pair is changed, not created, and a new pick is created', async ({ app, mock }) => {
    const { page } = app;
    await openMultiroom(page);
    const btn = page.locator('#stereoCreate');
    // The live pair fills the dropdowns once the zone answers are in.
    await expect(page.locator('#stereoLeft')).toHaveValue(PAIR_L.deviceID);
    await expect(page.locator('#stereoRight')).toHaveValue(PAIR_R.deviceID);
    await expect(btn).toHaveText(en('multiroom.stereoUpdateBtn'));

    await mock.resetCalls();
    await btn.click();
    await mock.expectCalled('FormZone', { times: 1 });
    await expect(page.locator('#stereoResult')).toContainText(en('multiroom.stereoUpdated'));
    await expect(page.locator('#stereoResult')).not.toContainText(en('multiroom.stereoFormed'));

    // Bedroom on the right is not a pair yet: back to "create".
    await page.locator('#stereoRight').selectOption(BR.deviceID);
    await expect(page.locator('#stereoCreate')).toHaveText(en('multiroom.stereoCreateBtn'));
  });
});
