// The mouse wheel and the touchpad on every UI 2.0 slider (src/sliderwheel.js,
// wired in shell2/mount.js): the player volume, a speaker's level under
// Levels, and the Sound tab's volume and bass. A notch is one step, small
// touchpad deltas add up to whole steps on both axes, the page stays still
// while a slider takes the wheel, a page scroll that slides a slider under the
// pointer changes nothing, and every step goes through the slider's own write
// path, so the volume ceiling holds. The classic layout keeps its slider,
// which takes the wheel only after a click.
//
//   E2E_PORT=4188 npx playwright test specs/desktop-ui2-slider-wheel.spec.mjs --project=desktop

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
// The demo living room's level (testkit/demo-data.mjs).
const LR_VOLUME = 32;
const LR_BR_GROUP = {
  master: LR.deviceID,
  senderIP: LR.host,
  members: [{ deviceID: LR.deviceID, ip: LR.host }, { deviceID: BR.deviceID, ip: BR.host }],
};

// More than the gesture gap (sliderwheel.js WHEEL_GESTURE_GAP_MS) and the
// rate cap between two steps (WHEEL_MIN_STEP_MS).
const RELEASE_MS = 600;
const BETWEEN_MS = 100;

const nav = (page, view) => page.locator(`#ui2Tabs .ui2-nav-item[data-view="${view}"]`);

async function openSound(page) {
  await nav(page, 'speakers').click();
  await page.locator(`#view-speakers .spl-open[data-host="${LR.host}"]`).click();
  await expect(page.locator('#ui2SpkPageName')).toHaveText(LR.friendlyName);
  await page.locator('#ui2SpkTab-sound').click();
  await expect(page.locator('#boxVolume')).toBeVisible();
}

// notches turns the wheel n times (negative: down) with the pointer where it is.
async function notches(page, n) {
  for (let i = 0; i < Math.abs(n); i++) {
    await page.mouse.wheel(0, n > 0 ? -100 : 100);
    await runFor(page, BETWEEN_MS);
  }
}

// pad sends one touchpad sample (pixel deltas, a wheelDelta that is no 120
// notch) to the slider; resolves to whether the page's scroll was held back.
const pad = (loc, dx, dy) => loc.evaluate((el, [x, y]) => {
  const ev = new WheelEvent('wheel', {
    deltaX: x, deltaY: y, deltaMode: 0, wheelDeltaX: Math.round(-x * 1.2), wheelDeltaY: Math.round(-y * 1.2),
    bubbles: true, cancelable: true,
  });
  el.dispatchEvent(ev);
  return ev.defaultPrevented;
}, [dx, dy]);

// lastVolume is the level the speaker at host was last asked for.
async function lastVolume(mock, host) {
  const c = await mock.last('SetBoxVolume', onHost(host));
  return c ? c.args[2] : null;
}

test.describe('UI 2.0 sliders take the wheel and the touchpad', () => {
  test.use({ ui2: true, fakeClock: true });

  test('1. the player volume moves one step per notch, up and down, and the speaker gets it', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    const vol = page.locator('#ui2Player #musicVolume');
    await expect(vol).toHaveValue(String(LR_VOLUME));
    // Its tooltip says the wheel works without a click first.
    await expect(vol).toHaveAttribute('title', en('ui2.slider.wheelHint'));
    await mock.resetCalls();
    await vol.hover();
    await notches(page, 2);
    await expect(vol).toHaveValue(String(LR_VOLUME + 2));
    await expect(page.locator('#ui2Player #musicVolumeVal')).toHaveText(String(LR_VOLUME + 2));
    await expect.poll(() => lastVolume(mock, LR.host)).toBe(LR_VOLUME + 2);
    // A status poll during and right after the gesture does not pull it back.
    await runFor(page, RELEASE_MS);
    await notches(page, -3);
    await expect(vol).toHaveValue(String(LR_VOLUME - 1));
    await expect.poll(() => lastVolume(mock, LR.host)).toBe(LR_VOLUME - 1);
    await runFor(page, 3_000);
    await expect(vol).toHaveValue(String(LR_VOLUME - 1));
    // The throttle still holds: never two writes at once for one speaker.
    expect(await mock.maxOpen('SetBoxVolume')).toBeLessThanOrEqual(1);
  });

  test('2. a touchpad adds small deltas up to whole steps, vertically and horizontally', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    const vol = page.locator('#ui2Player #musicVolume');
    await expect(vol).toHaveValue(String(LR_VOLUME));
    await mock.resetCalls();
    // Playwright's wheel always reports a whole 120 notch, so the touchpad's
    // stream of small pixel deltas is sent the way the WebView delivers it.
    for (let i = 0; i < 10; i++) {
      // Ten samples of 8 px up: 80 px, two steps, no more.
      expect(await pad(vol, 0, -8)).toBe(true);
      await runFor(page, 50);
    }
    await expect(vol).toHaveValue(String(LR_VOLUME + 2));
    await runFor(page, RELEASE_MS);
    // A sideways swipe to the right: six samples of 20 px, three steps up.
    for (let i = 0; i < 6; i++) {
      await pad(vol, 20, 0);
      await runFor(page, 50);
    }
    await expect(vol).toHaveValue(String(LR_VOLUME + 5));
    await runFor(page, RELEASE_MS);
    // And to the left, back down.
    for (let i = 0; i < 4; i++) {
      await pad(vol, -20, 0);
      await runFor(page, 50);
    }
    await expect(vol).toHaveValue(String(LR_VOLUME + 3));
    await expect.poll(() => lastVolume(mock, LR.host)).toBe(LR_VOLUME + 3);
  });

  test('3. the page stays still under a slider that takes the wheel, and a passing page scroll changes nothing', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    await openSound(page);
    const slider = page.locator('#boxVolume');
    await expect(slider).toHaveValue(String(LR_VOLUME));
    // The scroll box the Sound tab sits in.
    const scroller = await slider.evaluateHandle((el) => {
      for (let p = el.parentElement; p; p = p.parentElement) {
        const o = getComputedStyle(p).overflowY;
        if ((o === 'auto' || o === 'scroll') && p.scrollHeight > p.clientHeight) return p;
      }
      return document.scrollingElement;
    });
    const top = () => scroller.evaluate((el) => el.scrollTop);
    // Room to scroll both ways, with the slider still on screen.
    await scroller.evaluate((el) => { el.scrollTop = 40; });
    await slider.scrollIntoViewIfNeeded();
    const before = await top();
    // Above the top, so a wheel up that reached the page would show.
    expect(before).toBeGreaterThan(0);
    await mock.resetCalls();

    // Over the slider: the level moves, the page does not, also at the end.
    await slider.hover();
    await notches(page, 3);
    await expect(slider).toHaveValue(String(LR_VOLUME + 3));
    expect(await top()).toBe(before);
    await expect.poll(() => lastVolume(mock, LR.host)).toBe(LR_VOLUME + 3);
    // The tooltip names the wheel on a slider without one of its own.
    await expect(slider).toHaveAttribute('title', en('ui2.slider.wheelHint'));
    await runFor(page, RELEASE_MS);

    // A page scroll that starts next to the slider and then runs over it
    // scrolls the page and leaves the level alone.
    const box = await slider.boundingBox();
    await page.mouse.move(box.x + box.width / 2, box.y - 60);
    const sent = await mock.count('SetBoxVolume');
    await page.mouse.wheel(0, 40);
    await expect.poll(top).toBeGreaterThan(before);
    await slider.hover();
    await page.mouse.wheel(0, 40);
    await page.mouse.wheel(0, 40);
    await expect(slider).toHaveValue(String(LR_VOLUME + 3));
    expect(await mock.count('SetBoxVolume')).toBe(sent);
  });

  test.describe('with a volume ceiling', () => {
    test.use({ scenario: { bindings: { GetMaxVolume: { value: { supported: true, volume: LR_VOLUME + 2 } } } } });

    test('4. the Sound tab volume stops at the ceiling and the bass follows the wheel too', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      await openSound(page);
      const slider = page.locator('#boxVolume');
      await expect(page.locator('#boxMaxVolOn')).toBeChecked();
      await expect(slider).toHaveValue(String(LR_VOLUME));
      await mock.resetCalls();
      await slider.hover();
      await notches(page, 5);
      await expect(slider).toHaveValue(String(LR_VOLUME + 2));
      await expect(page.locator('#boxVolumeVal')).toHaveText(String(LR_VOLUME + 2));
      await runFor(page, RELEASE_MS);
      const asked = (await mock.calls('SetBoxVolume')).filter(onHost(LR.host)).map((c) => c.args[2]);
      expect(asked.length).toBeGreaterThan(0);
      expect(Math.max(...asked)).toBe(LR_VOLUME + 2);

      const bass = page.locator('#boxBass');
      const start = Number(await bass.inputValue());
      await bass.hover();
      await notches(page, -1);
      await expect(bass).toHaveValue(String(start - 1));
      await mock.expectCalled('SetBoxBass', { pred: onHost(LR.host) });
    });
  });

  test.describe('grouped', () => {
    test.use({ scenario: { zones: { livingRoom: LR_BR_GROUP, bedroom: LR_BR_GROUP }, playing: { livingRoom: 'PLAY_STATE', bedroom: 'PLAY_STATE' } } });

    test('5. a speaker\'s own level under Levels follows the wheel and only that speaker gets it', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      await page.locator('#ui2LevelsBtn').click();
      const pop = page.locator('#ui2LevelsPop');
      await expect(pop).toBeVisible();
      const bedroom = pop.locator(`input[data-host="${BR.host}"]`);
      await expect(bedroom).toHaveValue('18');
      await mock.resetCalls();
      await bedroom.hover();
      await notches(page, 2);
      await expect(bedroom).toHaveValue('20');
      await expect.poll(() => lastVolume(mock, BR.host)).toBe(20);
      expect(await mock.count('SetBoxVolume', onHost(LR.host))).toBe(0);
      // The panel stays open: the wheel is no click outside it.
      await expect(pop).toBeVisible();
    });
  });
});

test.describe('the classic layout', () => {
  test.use({ fakeClock: true });

  test('6. keeps its volume slider: the wheel only turns it after a click', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    const vol = page.locator('#musicVolume');
    await expect(vol).toHaveValue(String(LR_VOLUME));
    await expect(vol).toHaveAttribute('title', en('controls.volumeWheelHint'));
    await mock.resetCalls();
    await vol.hover();
    await notches(page, 2);
    await expect(vol).toHaveValue(String(LR_VOLUME));
    expect(await mock.count('SetBoxVolume')).toBe(0);
  });
});
