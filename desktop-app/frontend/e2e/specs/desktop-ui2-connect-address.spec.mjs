// Connect by address in the UI 2.0 preview (phase 5, src/shell2/state.js
// connectHTML): the speakers a network search cannot see (guest Wi-Fi, a VPN,
// isolation). Reachable from the empty sidebar, from the foot of the Speakers
// list and from the wizard's Find; a visible label, Enter submits, the classic
// AddBoxByIP (no new binding) and then a search; a failure stays under the
// field (role=alert) and the error dialog stays shut; the minute sweep never
// takes away what somebody is typing.
//
//   E2E_PORT=4181 npx playwright test specs/desktop-ui2-connect-address.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { settle } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';

const IP = '192.0.2.77';
const NONE = { boxes: [], current: null, seed: { cachedBoxes: false } };
const nav = (page, view) => page.locator(`#ui2Sidebar .ui2-nav-item[data-view="${view}"]`);

test.describe('Connect by address from the empty sidebar', () => {
  test.use({ ui2: true, scenario: { ...NONE, bindings: { AddBoxByIP: { value: null } } } });

  test('1. opens with its labelled field, Enter connects and searches', async ({ app, mock }) => {
    const { page } = app;
    const side = page.locator('#ui2SpkLists .u-state');
    await expect(side).toHaveAttribute('data-phase', 'none');
    const open = side.locator('[data-state-act="connect"]');
    await expect(open).toHaveAttribute('aria-expanded', 'false');
    await expect(page.locator('#ui2SpkLists .u-connect')).toBeHidden();
    await open.click();
    await expect(page.locator('#ui2SpkLists [data-state-act="connect"]')).toHaveAttribute('aria-expanded', 'true');
    const field = page.locator('#ui2SpkLists').getByLabel(en('ui2.state.addressLabel'));
    await expect(field).toBeFocused();
    await expect(field).toHaveAttribute('placeholder', en('speaker.manualIpPlaceholder'));
    await mock.resetCalls();
    await field.fill(IP);
    await field.press('Enter');
    await mock.expectCalled('AddBoxByIP', { pred: (c) => c.args[0] === IP, times: 1 });
    await mock.expectCalled('DiscoverBoxes');
    await expect(page.locator('#ui2SpkLists .u-connect-status')).toHaveText(en('ui2.state.connected', { ip: IP }));
    await expect(page.locator('#ui2SpkLists .u-connect-input')).toHaveValue('');
    await expect(page.locator('#errorModal')).toBeHidden();
  });
});

test.describe('Connect by address: a speaker that does not answer', () => {
  test.use({ ui2: true, scenario: { ...NONE, bindings: { AddBoxByIP: { error: 'dial tcp: i/o timeout' } } } });

  test('2. says it under the field, the dialog stays shut and the address stays', async ({ app }) => {
    const { page } = app;
    await page.locator('#ui2SpkLists [data-state-act="connect"]').click();
    const field = page.locator('#ui2SpkLists .u-connect-input');
    await field.fill(IP);
    await field.press('Enter');
    const err = page.locator('#ui2SpkLists .u-connect-error');
    await expect(err).toHaveAttribute('role', 'alert');
    await expect(err).toHaveText(en('speaker.manualIpNotFound', { ip: IP }));
    await expect(field).toHaveAttribute('aria-invalid', 'true');
    await expect(field).toHaveValue(IP);
    await expect(page.locator('#errorModal')).toBeHidden();
    await expect(page.locator('#toast')).not.toHaveClass(/show/);
    // A new address clears the answer about the old one.
    await field.fill('192.0.2.78');
    await expect(err).toHaveText('');
  });
});

test.describe('Connect by address: the minute sweep', () => {
  test.use({ ui2: true, fakeClock: true, scenario: NONE });

  test('3. never repaints the field while an address is in it', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 3_000);
    await page.locator('#ui2SpkLists [data-state-act="connect"]').click();
    const field = page.locator('#ui2SpkLists .u-connect-input');
    await field.fill('192.0.2.');
    await mock.resetCalls();
    // The empty list's sweeps (recovery and the minute timer) run meanwhile.
    await settle(page, 70_000);
    await mock.expectCalled('DiscoverBoxes');
    await expect(field).toHaveValue('192.0.2.');
    await expect(field).toBeFocused();
  });
});

test.describe('Connect by address at the foot of the Speakers list', () => {
  test.use({ ui2: true, scenario: { bindings: { AddBoxByIP: { value: null } } } });

  test('4. a speaker the search cannot see, by address, Enter submits', async ({ app, mock }) => {
    const { page } = app;
    await nav(page, 'speakers').click();
    const row = page.locator('#view-speakers .spl-cannot-see');
    await expect(row.locator('.spl-foot-text')).toHaveText(en('ui2.state.cannotSee'));
    const field = row.getByLabel(en('ui2.state.addressLabel'));
    await expect(field).toBeVisible();
    await mock.resetCalls();
    await field.fill(IP);
    await field.press('Enter');
    await mock.expectCalled('AddBoxByIP', { pred: (c) => c.args[0] === IP, times: 1 });
    await expect(page.locator('#view-speakers .spl-cannot-see .u-connect-status')).toHaveText(en('ui2.state.connected', { ip: IP }));
  });
});

test.describe('Connect by address in the wizard Find', () => {
  test.use({ ui2: true, scenario: { boxes: ['livingRoom'], bindings: { AddBoxByIP: { error: 'no answer' } } } });

  test('5. is in the empty state, and a failure stays inline', async ({ app, mock }) => {
    const { page } = app;
    await nav(page, 'speakers').click();
    await page.locator('#ui2SpkSubnav .spa-sub[data-route="add"]').click();
    const empty = page.locator('#w2FindList .w2-empty');
    await expect(empty).toBeVisible();
    const field = empty.getByLabel(en('ui2.state.addressLabel'));
    await mock.resetCalls();
    await field.fill(IP);
    await field.press('Enter');
    await mock.expectCalled('AddBoxByIP', { pred: (c) => c.args[0] === IP, times: 1 });
    await expect(empty.locator('.u-connect-error[role="alert"]')).toHaveText(en('speaker.manualIpNotFound', { ip: IP }));
    await expect(page.locator('#errorModal')).toBeHidden();
  });
});
