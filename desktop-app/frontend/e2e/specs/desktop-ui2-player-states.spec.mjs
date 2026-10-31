// The persistent player's states in the UI 2.0 preview (phase 5): a
// controlled speaker that is offline is no live player (its controls are
// hidden AND inert, the player names the speaker and offers Search again, and
// they come back when it answers); a transport press the speaker refuses is
// said in the player, naming the speaker, with no dialog. The classic layout
// keeps its dialog.
//
//   E2E_PORT=4181 npx playwright test specs/desktop-ui2-player-states.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const CONTROLS = ['.ui2-player-transport', '.ui2-player-volume', '.ui2-player-more', '.ui2-player-groupvol', '.ui2-player-levels'];

test.describe('UI 2.0 player: the speaker you control is offline', () => {
  test.use({ ui2: true, scenario: { boxPatches: { livingRoom: { offline: true } } } });

  test('1. hides and inerts the controls and names the speaker', async ({ app, mock }) => {
    const { page } = app;
    const player = page.locator('#ui2Player');
    const empty = player.locator('.ui2-player-empty');
    await expect(empty).toBeVisible();
    await expect(empty.locator('.ui2-player-state-text')).toHaveText(en('ui2.state.player.offline', { name: LR.friendlyName }));
    for (const sel of CONTROLS) {
      await expect(player.locator(sel), sel).toBeHidden();
      await expect(player.locator(sel), sel).toHaveAttribute('inert', '');
    }
    await expect(player).toHaveClass(/is-offline/);
    // Search again, and the speaker answers: the controls come back.
    await mock.set('DiscoverBoxes', { value: [{ ...LR, offline: false }, ROSTER.bedroom, ROSTER.office, ROSTER.kitchen] });
    await mock.set('RefreshKnownBoxes', { value: [{ ...LR, offline: false }, ROSTER.bedroom, ROSTER.office, ROSTER.kitchen] });
    await empty.locator('[data-state-act="search"]').click();
    await expect(player.locator('.ui2-player-transport')).toBeVisible();
    await expect(player.locator('.ui2-player-transport')).not.toHaveAttribute('inert', '');
    await expect(empty).toBeHidden();
    await expect(player).not.toHaveClass(/is-offline/);
  });
});

test.describe('UI 2.0 player: a refused transport press', () => {
  test.use({ ui2: true, scenario: { bindings: { Pause: { error: 'speaker refused' } } } });

  test('2. is said in the player with the speaker named, no dialog', async ({ app, mock }) => {
    const { page } = app;
    const pause = page.locator('#ui2Player #pauseBtn');
    await expect(pause).toBeVisible();
    await mock.resetCalls();
    await pause.click();
    await mock.expectCalled('Pause');
    const status = page.locator('#ui2PlayerStatus');
    await expect(status).toHaveAttribute('role', 'alert');
    await expect(status).toHaveText(en('ui2.state.player.failPause', { name: LR.friendlyName, err: 'speaker refused' }));
    await expect(page.locator('#errorModal')).toBeHidden();
  });
});

test.describe('the classic player: a refused transport press', () => {
  test.use({ scenario: { bindings: { Pause: { error: 'speaker refused' } } } });

  test('3. keeps the error dialog', async ({ app, mock }) => {
    const { page } = app;
    await mock.resetCalls();
    await page.locator('#pauseBtn').click();
    await mock.expectCalled('Pause');
    await expect(page.locator('#errorModal')).toBeVisible();
    await expect(page.locator('#ui2PlayerStatus')).toHaveCount(0);
  });
});
