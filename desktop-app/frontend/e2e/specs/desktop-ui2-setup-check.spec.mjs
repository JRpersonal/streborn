// desktop-ui2-setup-check: step 2 of the UI 2.0 setup wizard, the firmware
// check (src/shell2/setup/check.js, #1288), against the mock backend.
//
// The step is injected into the running app (helpers/checkharness.mjs), so it
// runs on the app's own Wails mock, bundles and shell2 tokens before the
// wizard that hosts it lands. Kitchen (192.0.2.51) is the Bose original:
//
//   1. 27.0.6 says so and lets the install go ahead at once.
//   2. 15.x: the one-click update (Route A) is the main action, its
//      confirmation names the speaker, progress shows, and the step is done
//      only once the speaker reports 27.0.6 back.
//   3. 15.x, the speaker comes back on the old version: an error in the step,
//      not "done".
//   4. 15.x, the update fails: the error in the step, Try again.
//   5. 15.x: "Continue without updating" names the risk and continues.
//   6. 15.x without one certain Bose file: the steps by hand, open.
//   7. 7.x on a first generation SoundTouch 20: the stick ladder inside the
//      step, no way past it; the step completes when the speaker reports 27.0.6.
//   8. 10.x on another model: the single-step guide with the firmware guide
//      link, no way past it.
//   9. Not answering: the error in the step, named; Check again recovers.
//  10. An unreadable version may continue, with the risk named.
//  11. Back aborts the step.
//  12. No axe violations in dark and light; every control at least 40px.
//
// With CHECK_SHOTS=<folder> the main screens are also saved there for review.

import AxeBuilder from '@axe-core/playwright';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { test, expect } from '../fixtures.mjs';
import { en } from '../helpers/i18n.mjs';
import { WCAG_TAGS } from '../helpers/a11y.mjs';
import { checkHarness, injectCheckHarness } from '../helpers/checkharness.mjs';
import { SETUP_CHECK } from '../../testkit/demo-data.mjs';

const BOX = { host: SETUP_CHECK.box.host, name: SETUP_CHECK.box.name, model: SETUP_CHECK.box.model };
const NAME = BOX.name;
const LADDER_PLAN = SETUP_CHECK.ladderPlan;

const fwScenario = (fw, extra = {}) => ({
  bindings: {
    GetBoxFirmware: Array.isArray(fw) ? { sequence: fw.map((v) => ({ value: v })) } : { value: fw },
    GetFirmwareGuide: { value: SETUP_CHECK.guideOne },
    ...extra,
  },
});

let harness = null;
test.beforeAll(async () => { harness = await checkHarness(); });

async function mount(page) {
  await injectCheckHarness(page, harness);
  await page.evaluate((b) => window.__checkStep.mount(b), BOX);
  const step = page.locator('#e2eCheckHost .u-check');
  await expect(step).toBeVisible();
  await expect(step).not.toHaveAttribute('data-case', 'loading');
  return step;
}

const result = (page) => page.evaluate(() => window.__checkStep.result);

async function shot(page, name) {
  const dir = process.env.CHECK_SHOTS;
  if (!dir) return;
  mkdirSync(dir, { recursive: true });
  await page.locator('#e2eCheckHost').screenshot({ path: join(dir, `${name}.png`), animations: 'disabled' });
}

test.describe('UI 2.0 setup check: firmware', () => {
  test.use({ ui2: true });

  test.describe('on 27.0.6', () => {
    test.use({ scenario: fwScenario(SETUP_CHECK.current) });

    test('1. says so and lets the install go ahead', async ({ app, mock }) => {
      const step = await mount(app.page);
      await expect(step.locator('.u-check-title')).toHaveText(en('ui2.setup.check.title', { name: NAME }));
      await expect(step).toHaveAttribute('data-case', 'current');
      await expect(step.locator('[data-row="firmware"]')).toContainText(en('ui2.setup.check.fwCurrent', { version: '27.0.6' }));
      await expect(step.locator('[data-row="firmware"]')).toContainText(en('ui2.setup.check.stOk'));
      await expect(step.locator('.u-check-callout')).toContainText(en('ui2.setup.check.currentBody', { name: NAME, version: '27.0.6' }));
      await expect(step.locator('[data-check="flash"], [data-check="skip"]:visible, .fw-guide')).toHaveCount(0);
      await expect.poll(() => result(app.page)).toEqual({ ok: true, firmware: '27.0.6', route: 'current' });
      await mock.expectCalled('GetBoxFirmware', { pred: (c) => c.args[0] === BOX.host, times: 1 });
    });
  });

  test.describe('on 15.x', () => {
    test.use({
      scenario: fwScenario([SETUP_CHECK.routeA, SETUP_CHECK.current], {
        FlashBoseFirmware: { value: { before: '15.0.4', after: '27.0.6', via: 'page' }, delayMs: 1500 },
      }),
    });

    test('2. the one-click update is the main action and done only once the speaker reports 27.0.6', async ({ app, mock }) => {
      const page = app.page;
      const step = await mount(page);
      await expect(step).toHaveAttribute('data-case', 'routeA');
      await expect(step.locator('[data-row="firmware"]')).toContainText(en('ui2.setup.check.fwOld', { version: '15.0.4', required: '27.0.6' }));
      await expect(step.locator('[data-row="firmware"]')).toContainText(en('ui2.setup.check.stNeedsUpdate'));
      await expect(step.locator('[data-row="model"]')).toContainText(en('ui2.setup.check.modelHw', { model: 'SoundTouch 20', hardware: en('ui2.setup.check.hwNewer') }));
      await expect(step.locator('.u-check-routea-title')).toHaveText(en('ui2.setup.check.routeATitle', { required: '27.0.6' }));
      // The stick and the update page stay as the fallback, folded, without a
      // second update button.
      await expect(step.locator('.u-check-other')).not.toHaveAttribute('open', '');
      await expect(step.locator('.u-check-other [data-check-open]')).toHaveText(en('ui2.setup.check.stickRoute'));
      await expect(step.locator('.u-check-other [data-fwflash]')).toBeHidden();

      await shot(page, 'routeA');
      const btn = step.locator('[data-check="flash"]');
      await expect(btn).toHaveText(en('ui2.setup.check.routeABtn', { name: NAME }));
      await btn.click();
      await expect(page.locator('#warnModal .warn-title')).toContainText(`${NAME}: ${en('fwGuide.autoConfirmTitle')}`);
      await page.locator('#warnConfirm').click();
      await mock.expectCalled('FlashBoseFirmware', { pred: (c) => c.args[0] === BOX.host && c.args[1] === false, times: 1 });
      await expect(step.locator('[data-row="firmware"]')).toContainText(en('ui2.setup.check.stUpdating'));
      await expect(btn).toBeDisabled();
      await expect(step.locator('[data-check="skip"]')).toBeHidden();
      await mock.emit('firmware:progress', { host: BOX.host, phase: 'upload', pct: 40 });
      await expect(step.locator('.u-check-progress')).toHaveText(en('fwGuide.autoUpload', { pct: '40 %' }));
      // A finished upload is not done: nothing settles until the read-back.
      expect(await result(page)).toBeNull();
      await shot(page, 'routeA-progress');

      await expect(step.locator('.u-check-callout')).toContainText(en('ui2.setup.check.updateDone', { name: NAME, version: '27.0.6' }));
      await expect(step.locator('[data-row="firmware"]')).toContainText(en('ui2.setup.check.fwConfirmed', { name: NAME, version: '27.0.6' }));
      await expect.poll(() => result(page)).toEqual({ ok: true, firmware: '27.0.6', route: 'route-a' });
      await shot(page, 'routeA-done');
      await mock.expectCalled('GetBoxFirmware', { pred: (c) => c.args[0] === BOX.host, times: 2 });
    });
  });

  test.describe('on 15.x, back on the old version', () => {
    test.use({
      scenario: fwScenario([SETUP_CHECK.routeA, SETUP_CHECK.routeA], {
        FlashBoseFirmware: { value: { before: '15.0.4', after: '15.0.4', via: 'page' }, delayMs: 200 },
      }),
    });

    test('3. says what the speaker reported instead of done', async ({ app }) => {
      const step = await mount(app.page);
      await step.locator('[data-check="flash"]').click();
      await app.page.locator('#warnConfirm').click();
      await expect(step.locator('.u-check-callout')).toContainText(
        en('ui2.setup.check.updateNotReached', { name: NAME, version: '15.0.4', required: '27.0.6' }));
      await expect(step.locator('[data-row="firmware"]')).toContainText(en('ui2.setup.check.stNeedsUpdate'));
      await expect(step).toHaveAttribute('data-phase', 'needs-update');
      expect(await result(app.page)).toBeNull();
    });
  });

  test.describe('on 15.x, the update fails', () => {
    test.use({ scenario: fwScenario(SETUP_CHECK.routeA, { FlashBoseFirmware: { error: 'the speaker does not answer', delayMs: 100 } }) });

    test('4. shows the error in the step and offers to try again', async ({ app }) => {
      const step = await mount(app.page);
      await step.locator('[data-check="flash"]').click();
      await app.page.locator('#warnConfirm').click();
      await expect(step.locator('.u-check-progress')).toHaveText(en('fwGuide.autoFailed', { error: 'the speaker does not answer' }));
      await expect(step.locator('[data-check="flash"]')).toHaveText(en('ui2.setup.check.retry', { name: NAME }));
      await expect(step.locator('[data-check="flash"]')).toBeEnabled();
      await expect(step).toHaveAttribute('data-phase', 'failed');
      expect(await result(app.page)).toBeNull();
    });
  });

  test.describe('on 15.x, continuing', () => {
    test.use({ scenario: fwScenario(SETUP_CHECK.routeA) });

    test('5. "Continue without updating" names the risk in one sentence', async ({ app, mock }) => {
      const step = await mount(app.page);
      const skip = step.locator('[data-check="skip"]');
      await expect(skip).toHaveText(en('ui2.setup.check.skip', { name: NAME }));
      const risk = en('ui2.setup.check.skipRisk', { name: NAME, version: '15.0.4', required: '27.0.6' });
      await expect(step.locator('.u-check-risk')).toHaveText(risk);
      await expect(skip).toHaveAccessibleDescription(risk);
      await skip.click();
      await expect.poll(() => result(app.page)).toEqual({ ok: true, firmware: '15.0.4', route: 'skip' });
      await expect(mock.count('FlashBoseFirmware')).resolves.toBe(0);
    });
  });

  test.describe('on 15.x without one certain Bose file', () => {
    test.use({ scenario: fwScenario(SETUP_CHECK.routeA, { GetFirmwareGuide: { value: SETUP_CHECK.guideNone } }) });

    test('6. the steps by hand show open, without a one-click button', async ({ app }) => {
      const step = await mount(app.page);
      await expect(step.locator('.u-check-callout')).toContainText(en('ui2.setup.check.noAuto', { name: NAME }));
      await expect(step.locator('[data-check="flash"]')).toHaveCount(0);
      await expect(step.locator('.u-check-other')).toHaveAttribute('open', '');
      await expect(step.locator('.u-check-other .fw-guide')).toContainText(en('fwGuide.title'));
    });
  });

  test.describe('on 7.x, a first generation SoundTouch 20', () => {
    test.use({
      scenario: fwScenario([SETUP_CHECK.ladder, SETUP_CHECK.current], {
        GetFirmwareLadder: { value: LADDER_PLAN },
      }),
    });

    test('7. the stick ladder sits in the step, with no way past it', async ({ app, mock }) => {
      const step = await mount(app.page);
      await expect(step).toHaveAttribute('data-case', 'ladder');
      await expect(step.locator('.u-check-callout')).toContainText(en('ui2.setup.check.guideIntro', { name: NAME, version: '7.0.37', required: '27.0.6' }));
      await expect(step.locator('.u-check-callout')).toContainText(en('ui2.setup.check.noSkip', { name: NAME }));
      const lad = step.locator('.u-check-guide .fw-ladder');
      await expect(lad).toContainText(en('fwLadder.title', { n: 2 }));
      await mock.expectCalled('GetFirmwareLadder', { pred: (c) => c.args[0] === BOX.host, times: 1 });
      await expect(lad.locator('[data-ladder="start"]')).toBeDisabled();
      await lad.locator('[data-ladder="ack"]').check();
      await expect(lad.locator('[data-ladder="start"]')).toBeEnabled();
      await shot(app.page, 'ladder');
      await expect(step.locator('[data-check="skip"]')).toBeHidden();
      await expect(step.locator('[data-check="flash"]')).toHaveCount(0);

      // After the last step the speaker reports 27.0.6: the step confirms it.
      const again = step.locator('[data-check="recheck"]');
      await expect(again).toHaveText(en('ui2.setup.check.recheck', { name: NAME }));
      await again.click();
      await expect(step.locator('.u-check-callout')).toContainText(en('ui2.setup.check.updateDone', { name: NAME, version: '27.0.6' }));
      await expect.poll(() => result(app.page)).toEqual({ ok: true, firmware: '27.0.6', route: 'ladder' });
    });
  });

  test.describe('on 10.x, another model', () => {
    test.use({ scenario: fwScenario(SETUP_CHECK.guide, { GetFirmwareGuide: { value: { ...SETUP_CHECK.guideOne, model: 'SoundTouch 10', current: '10.0.11' } } }) });

    test('8. the single-step guide with the firmware guide link, no way past it', async ({ app }) => {
      const step = await mount(app.page);
      await expect(step).toHaveAttribute('data-case', 'guide');
      await expect(step.locator('.u-check-callout')).toContainText(en('ui2.setup.check.guideIntro', { name: NAME, version: '10.0.11', required: '27.0.6' }));
      const guide = step.locator('.u-check-guide .fw-guide');
      await expect(guide).toContainText(en('fwLadder.unsupported'));
      await expect(guide.locator('.fw-ladder-none [data-fwopen]')).toHaveText(en('fwLadder.docs'));
      await expect(guide.locator('.fw-ladder')).toHaveCount(0);
      await expect(step.locator('[data-check="skip"]')).toBeHidden();
    });
  });

  test.describe('not answering', () => {
    test.use({
      scenario: { bindings: { GetBoxFirmware: { sequence: [{ error: 'dial tcp 192.0.2.51:8090: i/o timeout' }, { value: SETUP_CHECK.current }] } } },
    });

    test('9. names the speaker and its address in the step, Check again recovers', async ({ app }) => {
      const step = await mount(app.page);
      await expect(step).toHaveAttribute('data-case', 'unreachable');
      const msg = en('ui2.setup.check.reachNo', { name: NAME, host: BOX.host });
      await expect(step.locator('[data-row="reach"]')).toContainText(msg);
      await expect(step.locator('[data-row="reach"]')).toContainText(en('ui2.setup.check.stError'));
      await expect(step.locator('[data-check="skip"]')).toBeHidden();
      await step.locator('[data-check="recheck"]').click();
      await expect.poll(() => result(app.page)).toEqual({ ok: true, firmware: '27.0.6', route: 'current' });
    });
  });

  test.describe('without a readable version', () => {
    test.use({ scenario: fwScenario(SETUP_CHECK.unknown) });

    test('10. may continue, with the risk named', async ({ app }) => {
      const step = await mount(app.page);
      await expect(step).toHaveAttribute('data-case', 'unknown');
      await expect(step.locator('.u-check-risk')).toHaveText(en('ui2.setup.check.skipRiskUnknown', { name: NAME }));
      await step.locator('[data-check="skip"]').click();
      await expect.poll(() => result(app.page)).toEqual({ ok: true, firmware: '', route: 'unknown' });
    });
  });

  test.describe('going back', () => {
    test.use({ scenario: fwScenario(SETUP_CHECK.routeA) });

    test('11. the wizard\'s abort settles the step', async ({ app }) => {
      await mount(app.page);
      await app.page.evaluate(() => window.__checkStep.abort());
      await expect.poll(() => result(app.page)).toEqual({ ok: false, firmware: '15.0.4', route: 'aborted' });
      const phases = await app.page.evaluate(() => window.__checkStep.states.map((s) => s.phase));
      expect(phases).toContain('needs-update');
    });
  });

  for (const theme of ['dark', 'light']) {
    test.describe(`accessibility, ${theme}`, () => {
      test.use({ theme, scenario: fwScenario(SETUP_CHECK.routeA) });

      test(`12. no axe violations and 40px targets (${theme})`, async ({ app }) => {
        const step = await mount(app.page);
        await expect(step.locator('[data-check="flash"]')).toBeVisible();
        const res = await new AxeBuilder({ page: app.page }).withTags(WCAG_TAGS).include('#e2eCheckHost').analyze();
        expect(res.violations.map((v) => `${v.id}: ${v.nodes.length}`)).toEqual([]);
        for (const sel of ['[data-check="flash"]', '[data-check="recheck"]', '[data-check="skip"]', '.u-check-other-sum']) {
          const h = await step.locator(sel).evaluate((el) => el.getBoundingClientRect().height);
          expect(h, sel).toBeGreaterThanOrEqual(40);
        }
        // Keyboard: the update button takes focus with a visible ring.
        await step.locator('[data-check="flash"]').focus();
        const outline = await step.locator('[data-check="flash"]').evaluate((el) => getComputedStyle(el).outlineStyle);
        expect(outline).not.toBe('none');
      });
    });
  }
});
