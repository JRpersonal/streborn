// UI 2.0 sidebar: Play/Pause on every playing row (src/shell2/sessionplay.js).
// Each row that plays (a single speaker, a group's master, a stereo pair)
// carries its own Play/Pause, on hover and on keyboard focus. It acts on that
// row's speaker through Pause and Resume, never changes the speaker you
// control, waits for the speaker before it says done, and reports a refusal
// in its own row instead of a toast. A group is paused through its master.
//
//   E2E_PORT=4181 npx playwright test specs/desktop-ui2-session-transport.spec.mjs --project=desktop

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';
import { mergeScenario, speakersScenario } from '../../testkit/scenario.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const OFFICE = ROSTER.office;
const KITCHEN = ROSTER.kitchen;
const TL = ROSTER.terraceLeft;
const TR = ROSTER.terraceRight;

const LR_BR_GROUP = {
  master: LR.deviceID,
  senderIP: LR.host,
  members: [{ deviceID: LR.deviceID, ip: LR.host }, { deviceID: BR.deviceID, ip: BR.host }],
};

const row = (page, host) => page.locator('#ui2SpkLists .ui2-spk-row')
  .filter({ has: page.locator(`.ui2-spk-pick[data-host="${host}"]`) });
const tp = (page, host) => row(page, host).locator('.ui2-spk-tp');
const pick = (page, host) => page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${host}"]`);
const opacity = (loc) => loc.evaluate((el) => getComputedStyle(el).opacity);

test.describe('UI 2.0 sidebar: Play/Pause per playing row', () => {
  test.use({ ui2: true, fakeClock: true, scenario: { playing: { livingRoom: 'PLAY_STATE', bedroom: 'PLAY_STATE' } } });

  test('1. every playing row has its own Play/Pause; idle, offline and Bose original rows have none', async ({ app }) => {
    const { page } = app;
    await settle(page, 9_000);
    await expect(tp(page, LR.host)).toHaveCount(1);
    await expect(tp(page, BR.host)).toHaveCount(1);
    await expect(tp(page, LR.host)).toHaveAttribute('aria-label', en('ui2.side.pause', { name: 'Living Room' }));
    await expect(tp(page, BR.host)).toHaveAttribute('aria-label', en('ui2.side.pause', { name: 'Bedroom' }));
    await expect(tp(page, BR.host)).toHaveAttribute('title', en('ui2.side.pause', { name: 'Bedroom' }));
    await expect(tp(page, BR.host)).toHaveAttribute('data-port', String(BR.port));
    await expect(tp(page, OFFICE.host)).toHaveCount(0);
    await expect(tp(page, KITCHEN.host)).toHaveCount(0);
  });

  test('2. Pause on another row pauses that speaker, keeps the one you control, and says done only once it reports it', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    await expect(pick(page, LR.host)).toHaveAttribute('aria-current', 'true');
    await mock.resetCalls();
    await tp(page, BR.host).click();
    await mock.expectCalled('Pause', { pred: onHost(BR.host), times: 1 });
    expect(await mock.count('Pause', onHost(LR.host))).toBe(0);
    expect(await mock.count('Stop')).toBe(0);
    // Sent is not arrived: the button waits and says so.
    await expect(tp(page, BR.host)).toHaveAttribute('aria-busy', 'true');
    await expect(tp(page, BR.host)).toHaveAttribute('aria-label', en('ui2.side.pausing', { name: 'Bedroom' }));
    // The speaker you control is the same one, and the player still names it.
    await expect(pick(page, LR.host)).toHaveAttribute('aria-current', 'true');
    await expect(pick(page, BR.host)).not.toHaveAttribute('aria-current', 'true');
    // The speaker reports the pause on the next read: the row offers Play.
    await runFor(page, 3_500);
    await expect(tp(page, BR.host)).not.toHaveAttribute('aria-busy', 'true');
    await expect(tp(page, BR.host)).toHaveAttribute('data-tp', 'play');
    await expect(tp(page, BR.host)).toHaveAttribute('aria-label', en('ui2.side.play', { name: 'Bedroom' }));
    await expect(page.locator('#ui2SpkLive')).toHaveText(en('ui2.side.paused', { name: 'Bedroom' }));
    await expect(pick(page, LR.host)).toHaveAttribute('aria-current', 'true');
    // Living Room plays on.
    expect((await mock.state()).playing[LR.host]).toBe('PLAY_STATE');

    // Play resumes the same speaker.
    await mock.resetCalls();
    await tp(page, BR.host).click();
    await mock.expectCalled('Resume', { pred: onHost(BR.host), times: 1 });
    await expect(tp(page, BR.host)).toHaveAttribute('aria-label', en('ui2.side.resuming', { name: 'Bedroom' }));
    await runFor(page, 3_500);
    await expect(tp(page, BR.host)).toHaveAttribute('data-tp', 'pause');
    await expect(page.locator('#ui2SpkLive')).toHaveText(en('ui2.side.resumed', { name: 'Bedroom' }));
    await expect(pick(page, LR.host)).toHaveAttribute('aria-current', 'true');
    await expect(page.locator('#toast')).not.toHaveClass(/\bshow\b/);
  });

  test('3. shown on hover and on keyboard focus, reached with the arrow keys, pressed with Enter', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    await page.mouse.move(900, 400);
    expect(await opacity(tp(page, BR.host))).toBe('0');
    await row(page, BR.host).hover();
    expect(await opacity(tp(page, BR.host))).toBe('1');
    await page.mouse.move(900, 400);
    expect(await opacity(tp(page, BR.host))).toBe('0');
    // Keyboard: the name of the row, then Right to its Play/Pause.
    await pick(page, LR.host).focus();
    await page.keyboard.press('ArrowDown');
    await expect(pick(page, BR.host)).toBeFocused();
    await page.keyboard.press('ArrowRight');
    await expect(tp(page, BR.host)).toBeFocused();
    expect(await opacity(tp(page, BR.host))).toBe('1');
    await expect(tp(page, BR.host)).toHaveAttribute('tabindex', '0');
    await mock.resetCalls();
    await page.keyboard.press('Enter');
    await mock.expectCalled('Pause', { pred: onHost(BR.host), times: 1 });
    // The repaints keep the keyboard on the button.
    await runFor(page, 3_500);
    await expect(tp(page, BR.host)).toBeFocused();
    await expect(tp(page, BR.host)).toHaveAttribute('data-tp', 'play');
    // Left goes back to the name; choosing stays a separate key press.
    await page.keyboard.press('ArrowLeft');
    await expect(pick(page, BR.host)).toBeFocused();
    await expect(pick(page, LR.host)).toHaveAttribute('aria-current', 'true');
  });
});

test.describe('UI 2.0 sidebar: a group is paused through its master', () => {
  test.use({
    ui2: true,
    fakeClock: true,
    scenario: {
      playing: { livingRoom: 'PLAY_STATE', bedroom: 'PLAY_STATE' },
      zones: { livingRoom: LR_BR_GROUP, bedroom: LR_BR_GROUP },
    },
  });

  test('4. the master row pauses the group; the member row has no button of its own', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    await expect(tp(page, BR.host)).toHaveCount(0);
    await expect(tp(page, LR.host)).toHaveAttribute('aria-label', en('ui2.side.pauseGroup', { name: 'Living Room' }));
    await mock.resetCalls();
    await tp(page, LR.host).click();
    await mock.expectCalled('Pause', { pred: onHost(LR.host), times: 1 });
    expect(await mock.count('Pause', onHost(BR.host))).toBe(0);
  });
});

test.describe('UI 2.0 sidebar: a stereo pair row', () => {
  test.use({
    ui2: true,
    fakeClock: true,
    scenario: mergeScenario(speakersScenario(), { playing: { terraceLeft: 'PLAY_STATE', terraceRight: 'PLAY_STATE' } }),
  });

  test('5. the pair row pauses the pair through its master', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    const pairRow = row(page, TL.host);
    await expect(pairRow).toHaveClass(/\bis-pair\b/);
    await expect(pairRow.locator('.ui2-spk-tp')).toHaveAttribute('aria-label', en('ui2.side.pause', { name: en('ui2.side.pairName', { name: 'Terrace' }) }));
    await mock.resetCalls();
    await pairRow.locator('.ui2-spk-tp').click();
    await mock.expectCalled('Pause', { pred: onHost(TL.host), times: 1 });
    expect(await mock.count('Pause', onHost(TR.host))).toBe(0);
    await expect(pick(page, LR.host)).toHaveAttribute('aria-current', 'true');
  });
});

test.describe('UI 2.0 sidebar: a refused Play/Pause', () => {
  test.use({
    ui2: true,
    fakeClock: true,
    scenario: {
      playing: { livingRoom: 'PLAY_STATE', bedroom: 'PLAY_STATE' },
      bindings: { Pause: { byHost: { [BR.host]: { error: 'speaker busy' } } } },
    },
  });

  test('6. says so in its own row, no toast and no dialog, and the button is back', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    await tp(page, BR.host).click();
    await mock.expectCalled('Pause', { pred: onHost(BR.host), times: 1 });
    const note = row(page, BR.host).locator('.ui2-spk-rownote');
    await expect(note).toHaveAttribute('role', 'alert');
    await expect(note).toHaveText(en('ui2.state.player.failPause', { name: 'Bedroom', err: 'speaker busy' }));
    await expect(page.locator('#toast')).not.toHaveClass(/\bshow\b/);
    await expect(page.locator('#errorModal')).toBeHidden();
    await expect(tp(page, BR.host)).not.toHaveAttribute('aria-busy', 'true');
    await expect(tp(page, BR.host)).toHaveAttribute('aria-label', en('ui2.side.pause', { name: 'Bedroom' }));
  });
});

test.describe('classic layout', () => {
  test.use({ ui2: false, scenario: { playing: { livingRoom: 'PLAY_STATE', bedroom: 'PLAY_STATE' } } });

  test('7. has no per-row Play/Pause', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('#pauseBtn')).toBeVisible();
    await expect(page.locator('.ui2-spk-tp')).toHaveCount(0);
  });
});
