// desktop-firmware-ladder: the step-by-step firmware update for a speaker on
// very old firmware (fwladder.js), driven in the speaker settings of a
// SoundTouch 20 that reports 5.2.0.
//
//   1. Screen A lists the five steps; Start stays off until the warning is
//      ticked.
//   2. Screen B checks the one stick, Prepare sends the right step and drive,
//      screen C follows, and "Done, check again" with the new version moves on
//      to the next step. 2b: a stick with other files gets a hint, no block.
//   3. An unchanged version shows the "bar stayed empty" help and, on 7.x, the
//      smaller in-between steps. 3b: on a plan from a picked version, the plan
//      is built again from what the speaker reports, so no downgrade is offered.
//   4. A file that fails its fingerprint says so and stays on the stick screen.
//   5. The guide resumes on the same step after a reload.
//   6. "Can't find the speaker?" asks for the version and plans from it.
//   7. "Another model" plans without the detected model.
// The screen walk also checks the Portable's own screen C (power button, no
// unplugging).
//
// Nothing here downloads anything: every backend call is answered by the mock.
// With FWLADDER_SHOTS=<folder> the screens are also saved there, light and
// dark, for review.

import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { test, expect } from '../fixtures.mjs';
import { en } from '../helpers/i18n.mjs';
import { SETTINGS } from '../../testkit/demo-data.mjs';

const ARCHIVE = 'https://archive.org/download/bose-soundtouch-software-and-firmware/Firmware/2013-2015_No_Bluetooth/Airplay-1_ST20-30-Portable/';
const step = (v, live, extra = {}) => ({
  id: `st-scm-${v}`, version: v, family: 'st-scm', source: 'archive', url: `${ARCHIVE}Airplay-1_ST20-30-Portable_${v}.zip`,
  fileName: `Airplay-1_ST20-30-Portable_${v}.zip`, innerSize: 93_000_000, liveVerified: live, networkOk: false, fallback: false, ...extra,
});
const BOSE = {
  id: 'st-scm-27.0.6', version: '27.0.6', family: 'st-scm', source: 'bose',
  url: 'https://downloads.bose.com/ced/soundtouch/downloads_stockholm/Update_ti_27.0.6.46330.5043500.scm.stu',
  fileName: 'Update_ti_27.0.6.46330.5043500.scm.stu', innerSize: 105_879_988, liveVerified: false, networkOk: true, fallback: false,
};
const S0602 = step('06.02.01', true);
const S0700 = step('07.00.37', true);
const S1400 = step('14.00.33', false);
const S1500 = step('15.00.20', false);
const plan = (current, steps, fallbacks = {}) => ({
  status: 'ladder', family: 'st-scm', model: 'SoundTouch 20', moduleType: 'scm', current, deviceId: 'device-id-here',
  versionFrom: 'lan', steps, oldStart: false, fallbacks,
});
const PLAN_5X = plan('5.2.0', [S0602, S0700, S1400, S1500, BOSE]);
const PLAN_6X = plan('6.2.1', [S0700, S1400, S1500, BOSE]);
const PLAN_7X = plan('7.0.37', [S1400, S1500, BOSE], {
  'st-scm-14.00.33': [step('09.00.41', false, { fallback: true }), step('12.00.09', false, { fallback: true }), S1400],
});
const STICK_OK = { ok: true, path: 'E:\\', filesystem: 'FAT32', totalBytes: 31043616768, isFat32: true, bigEnough: true, writable: true, reason: '', freeBytes: 30500000000, clusterBytes: 16384, tooLarge: false, clusterTooBig: false };

const OLD_SETTINGS = { ...SETTINGS, info: { ...SETTINGS.info, version: '5.2.0.11111.222', moduleType: '' } };

function scenario(extra = {}, ladder = PLAN_5X) {
  return {
    bindings: {
      BoxSettings: { value: OLD_SETTINGS },
      GetFirmwareLadder: { value: ladder },
      GetFirmwareLadderManual: { value: PLAN_6X },
      CheckStick: { value: STICK_OK },
      PrepareFirmwareStick: { value: { ok: true, ejected: true } },
      RecheckFirmware: { value: { found: true, version: '6.2.1', via: 'lan', model: 'SoundTouch 20', moduleType: '', deviceId: 'device-id-here' } },
      GetFirmwareGuide: { value: { model: 'SoundTouch 20', current: '15.0.20', required: '27.0.6', files: [{ name: BOSE.fileName, url: BOSE.url, generation: 'scm', deviceId: '0x0923', length: BOSE.innerSize, crc: 0x2d5a971e }], live: false } },
      ...extra,
    },
  };
}

async function openLadder(page) {
  await page.locator('.tab-btn[data-view="settings"]').click();
  await expect(page.locator('#settingsBody')).toBeVisible();
  await page.evaluate(() => {
    document.querySelectorAll('#view-settings .settings-group').forEach((d) => { d.open = true; });
  });
  const lad = page.locator('#view-settings .fw-ladder');
  await expect(lad).toBeVisible();
  return lad;
}

async function shot(lad, name) {
  const dir = process.env.FWLADDER_SHOTS;
  if (!dir) return;
  mkdirSync(dir, { recursive: true });
  await lad.scrollIntoViewIfNeeded();
  await lad.screenshot({ path: join(dir, `${name}.png`), animations: 'disabled' });
}

async function startLadder(lad) {
  await lad.locator('[data-ladder="ack"]').check();
  await lad.locator('[data-ladder="start"]').click();
  await expect(lad).toContainText(en('fwLadder.prepTitle'));
}

test.describe('the firmware ladder', () => {
  test.use({ ui2: false, scenario: scenario() });

  test('1. screen A lists five steps and Start waits for the warning to be ticked', async ({ app, mock }) => {
    const lad = await openLadder(app.page);
    await expect(lad).toContainText(en('fwLadder.title', { n: 5 }));
    await expect(lad.locator('.fw-ladder-steps li')).toHaveCount(5);
    await expect(lad).toContainText(en('fwLadder.w1', { n: 5 }));
    await expect(lad).toContainText(en('fwLadder.stepBose', { version: '27.0.6' }));
    await expect(lad.locator('[data-ladder="start"]')).toBeDisabled();
    await lad.locator('[data-ladder="ack"]').check();
    await expect(lad.locator('[data-ladder="start"]')).toBeEnabled();
    await mock.expectCalled('GetFirmwareLadder', { pred: (c) => c.args[0] === 'demobox.local', times: 1 });
  });

  test('2. prepare the stick, install, check again: on to the next step', async ({ app, mock }) => {
    const lad = await openLadder(app.page);
    await startLadder(lad);
    await expect(lad).toContainText(en('fwLadder.stepTitle', { k: 1, n: 5, version: '06.02.01' }));
    await expect(lad).toContainText(en('fwLadder.stickOk'));
    await lad.locator('[data-ladder="prepare"]').click();
    await mock.expectCalled('PrepareFirmwareStick', { pred: (c) => c.args[0] === 'st-scm-06.02.01' && c.args[1] === 'E:\\', times: 1 });
    await expect(lad).toContainText(en('fwLadder.installTitle'));
    await expect(lad).toContainText(en('fwLadder.c3FourVol'));
    await expect(lad).toContainText(en('fwLadder.w2'));
    await lad.locator('[data-ladder="recheck"]').click();
    await mock.expectCalled('GetFirmwareLadderManual', { pred: (c) => c.args[2] === '6.2.1', times: 1 });
    await expect(lad).toContainText(en('fwLadder.reached', { version: '6.2.1' }));
    await expect(lad).toContainText(en('fwLadder.stepTitle', { k: 1, n: 4, version: '07.00.37' }));
    await expect(lad).toContainText(en('fwLadder.prepTitle'));
  });

  test('2b. a stick with other files gets a hint, and Prepare stays on', async ({ app, mock }) => {
    await mock.set('CheckStick', { value: { ...STICK_OK, otherFiles: true } });
    const lad = await openLadder(app.page);
    await startLadder(lad);
    await expect(lad).toContainText(en('fwLadder.prepIntro'));
    await expect(lad).toContainText(en('fwLadder.stickOk'));
    await expect(lad.locator('[data-ladder-note="other-files"]')).toHaveText(en('fwLadder.otherFiles'));
    await expect(lad.locator('[data-ladder="prepare"]')).toBeEnabled();
  });

  test('4. a file that fails its fingerprint is not written and says so', async ({ app, mock }) => {
    await mock.set('PrepareFirmwareStick', { value: { ok: false, code: 'fingerprint', detail: 'sha1 mismatch' } });
    const lad = await openLadder(app.page);
    await startLadder(lad);
    await expect(lad).toContainText(en('fwLadder.stickOk'));
    await lad.locator('[data-ladder="prepare"]').click();
    await expect(lad.locator('.fw-ladder-status')).toHaveText(en('fwLadder.w7'));
    await expect(lad).toContainText(en('fwLadder.prepTitle'));
  });

  test('5. the guide resumes on the same step after a reload', async ({ app }) => {
    const { page } = app;
    let lad = await openLadder(page);
    await startLadder(lad);
    await expect(lad).toContainText(en('fwLadder.stickOk'));
    await lad.locator('[data-ladder="prepare"]').click();
    await expect(lad).toContainText(en('fwLadder.installTitle'));
    await page.reload();
    await expect(page.locator('.tab-btn[data-view="settings"]')).toBeVisible();
    lad = await openLadder(page);
    await expect(lad).toContainText(en('fwLadder.installTitle'));
    await expect(lad).toContainText(en('fwLadder.stepTitle', { k: 1, n: 5, version: '06.02.01' }));
  });

  test('6. "Can\'t find the speaker?" asks for the version and plans from it', async ({ app, mock }) => {
    const lad = await openLadder(app.page);
    await lad.locator('[data-ladder="manual"]').click();
    await expect(lad).toContainText(en('fwLadder.manualHow'));
    await lad.locator('[data-ladder-field="version"]').selectOption('7-13');
    await lad.locator('[data-ladder="manual-go"]').click();
    await mock.expectCalled('GetFirmwareLadderManual', { pred: (c) => c.args[0] === 'SoundTouch 20' && c.args[2] === '7.0.37', times: 1 });
    await expect(lad).toContainText(en('fwLadder.title', { n: 4 }));
  });

  test('7. "Another model" never plans with the detected model\'s ladder', async ({ app, mock }) => {
    await mock.set('GetFirmwareLadderManual', { value: { status: 'unsupported', reason: 'model', family: '', model: '', moduleType: '', current: '7.0.37', steps: [], fallbacks: {} } });
    const lad = await openLadder(app.page);
    await lad.locator('[data-ladder="manual"]').click();
    await lad.locator('[data-ladder-field="model"]').selectOption('');
    await lad.locator('[data-ladder-field="version"]').selectOption('7-13');
    await lad.locator('[data-ladder="manual-go"]').click();
    await mock.expectCalled('GetFirmwareLadderManual', { pred: (c) => c.args[0] === '' && c.args[2] === '7.0.37', times: 1 });
    await expect(lad).toContainText(en('fwLadder.unsupported'));
    await expect(lad.locator('.fw-ladder-steps')).toHaveCount(0);
  });
});

test.describe('a stall on a plan built from a picked version', () => {
  // The user picked "7 to 13"; the speaker really is on 12.x and stays there.
  // Smaller steps from 9.x would be a downgrade, so none are offered.
  test.use({
    ui2: false,
    scenario: scenario({
      RecheckFirmware: { value: { found: true, version: '12.0.9', via: 'lan', model: 'SoundTouch 20', moduleType: '', deviceId: 'device-id-here' } },
      GetFirmwareLadderManual: { value: plan('12.0.9', [S1400, S1500, BOSE]) },
    }, { ...PLAN_7X, versionFrom: 'manual' }),
  });

  test('3b. builds the plan again from the reported version and offers no downgrade', async ({ app, mock }) => {
    const lad = await openLadder(app.page);
    await startLadder(lad);
    await expect(lad).toContainText(en('fwLadder.stickOk'));
    await lad.locator('[data-ladder="prepare"]').click();
    await expect(lad).toContainText(en('fwLadder.installTitle'));
    await lad.locator('[data-ladder="recheck"]').click();
    await mock.expectCalled('GetFirmwareLadderManual', { pred: (c) => c.args[2] === '12.0.9', times: 1 });
    await expect(lad).toContainText(en('fwLadder.stalled', { version: '12.0.9' }));
    await expect(lad.locator('[data-ladder="retry"]')).toBeVisible();
    await expect(lad.locator('[data-ladder="smaller"]')).toHaveCount(0);
  });
});

test.describe('a step that leaves the version unchanged', () => {
  test.use({
    ui2: false,
    scenario: scenario({
      RecheckFirmware: { value: { found: true, version: '7.0.37', via: 'lan', model: 'SoundTouch 20', moduleType: '', deviceId: 'device-id-here' } },
    }, PLAN_7X),
  });

  test('3. shows the "bar stayed empty" help and offers smaller steps', async ({ app, mock }) => {
    const lad = await openLadder(app.page);
    await startLadder(lad);
    await expect(lad).toContainText(en('fwLadder.stickOk'));
    await lad.locator('[data-ladder="prepare"]').click();
    await expect(lad).toContainText(en('fwLadder.installTitle'));
    await lad.locator('[data-ladder="recheck"]').click();
    await expect(lad).toContainText(en('fwLadder.stalled', { version: '7.0.37' }));
    await expect(lad).toContainText(en('fwLadder.w6'));
    await lad.locator('[data-ladder="smaller"]').click();
    await expect(lad).toContainText(en('fwLadder.stepTitle', { k: 1, n: 5, version: '09.00.41' }));
    await expect(lad).toContainText(en('fwLadder.stickOk'));
    await lad.locator('[data-ladder="prepare"]').click();
    await mock.expectCalled('PrepareFirmwareStick', { pred: (c) => c.args[0] === 'st-scm-09.00.41', times: 1 });
  });
});

// The screens for review, in both themes. The assertions keep the walk honest;
// the images are written only with FWLADDER_SHOTS set.
for (const theme of ['light', 'dark']) {
  test.describe(`screens, ${theme}`, () => {
    test.use({
      ui2: false,
      theme,
      scenario: scenario({
        RecheckFirmware: { value: { found: true, version: '7.0.37', via: 'lan', model: 'SoundTouch 20', moduleType: '', deviceId: 'device-id-here' } },
        PrepareFirmwareStick: { sequence: [{ never: true }] },
      }, PLAN_7X),
    });

    test(`A, B, C and the stall help (${theme})`, async ({ app, mock }) => {
      const { page } = app;
      const lad = await openLadder(page);
      await expect(lad).toContainText(en('fwLadder.title', { n: 3 }));
      await shot(lad, `${theme}-A-plan`);
      await startLadder(lad);
      await expect(lad).toContainText(en('fwLadder.stickOk'));
      await expect(lad.locator('[data-ladder-note="other-files"]')).toHaveCount(0);
      await shot(lad, `${theme}-B-stick`);
      await lad.locator('[data-ladder="prepare"]').click();
      await mock.expectCalled('PrepareFirmwareStick', { times: 1 });
      await mock.emit('fwladder:progress', { stepId: 'st-scm-14.00.33', phase: 'download', pct: 42, done: 1, total: 2 });
      await expect(lad.locator('.fw-ladder-status')).toHaveText('Downloading: 42 %');
      await shot(lad, `${theme}-B-progress`);
      await lad.locator('[data-ladder="cancel"]').click();
      await mock.expectCalled('CancelFirmwareStick', { times: 1 });
    });

    test(`B with other files on the stick (${theme})`, async ({ app, mock }) => {
      await mock.set('CheckStick', { value: { ...STICK_OK, otherFiles: true } });
      const lad = await openLadder(app.page);
      await startLadder(lad);
      await expect(lad.locator('[data-ladder-note="other-files"]')).toBeVisible();
      await shot(lad, `${theme}-B-stick-other-files`);
    });

    test(`C and the stall help (${theme})`, async ({ app, mock }) => {
      await mock.set('PrepareFirmwareStick', { value: { ok: true, ejected: true } });
      const lad = await openLadder(app.page);
      await startLadder(lad);
      await expect(lad).toContainText(en('fwLadder.stickOk'));
      await lad.locator('[data-ladder="prepare"]').click();
      await expect(lad).toContainText(en('fwLadder.installTitle'));
      await shot(lad, `${theme}-C-install`);
      await lad.locator('[data-ladder="recheck"]').click();
      await expect(lad).toContainText(en('fwLadder.w6'));
      await shot(lad, `${theme}-C-stalled`);
    });

    test(`C on the battery Portable (${theme})`, async ({ app, mock }) => {
      await mock.set('GetFirmwareLadder', { value: { ...PLAN_7X, model: 'SoundTouch Portable' } });
      await mock.set('PrepareFirmwareStick', { value: { ok: true, ejected: true } });
      const lad = await openLadder(app.page);
      await startLadder(lad);
      await expect(lad).toContainText(en('fwLadder.stickOk'));
      await lad.locator('[data-ladder="prepare"]').click();
      await expect(lad).toContainText(en('fwLadder.installTitle'));
      // The Portable's battery cannot be removed: no "unplug", the power
      // button instead, and the button combination marked as unconfirmed.
      await expect(lad).toContainText(en('fwLadder.c1Battery'));
      await expect(lad).toContainText(en('fwLadder.c3Battery'));
      await expect(lad).toContainText(en('fwLadder.c3Unconfirmed'));
      await expect(lad).toContainText(en('fwLadder.w2Battery'));
      await expect(lad).not.toContainText(en('fwLadder.c1'));
      await shot(lad, `${theme}-C-install-portable`);
    });

    test(`the version picker and the last step D (${theme})`, async ({ app, mock }) => {
      const lad = await openLadder(app.page);
      await lad.locator('[data-ladder="manual"]').click();
      await expect(lad).toContainText(en('fwLadder.manualHow'));
      await shot(lad, `${theme}-A-version-picker`);
      await mock.set('GetFirmwareLadderManual', { value: { ...plan('15.0.20', [BOSE]), status: 'final' } });
      await lad.locator('[data-ladder-field="version"]').selectOption('15');
      await lad.locator('[data-ladder="manual-go"]').click();
      await expect(lad).toContainText(en('fwLadder.finalTitle'));
      await expect(lad.locator('.fw-guide[data-fwguide]')).toBeVisible();
      await expect(lad.locator('[data-ladder="final-stick"]')).toBeVisible();
      // The last step is the one-click network update the normal guide offers.
      await expect(lad.locator('[data-fwflash]')).toBeVisible();
      await shot(lad, `${theme}-D-final`);
    });
  });
}
