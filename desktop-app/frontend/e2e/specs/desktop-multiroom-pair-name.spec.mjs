// The stereo editor's name field starts with the name typed last (#1683),
// in the classic Multi-room tab and in UI 2.0 Speakers > Groups (which
// arranges the same markup). Per pair first, else the last name at all, from
// this app's local storage (src/pairnamememory.js). A pair that exists keeps
// its own name.
//
//   E2E_PORT=4184 npx playwright test specs/desktop-multiroom-pair-name.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { ROSTER, SPEAKERS3 } from '../../testkit/demo-data.mjs';
import { mergeScenario, speakersScenario } from '../../testkit/scenario.mjs';

const MEMORY_KEY = 'str.stereo.lastNames';
const BR = ROSTER.bedroom;
const TL = ROSTER.terraceLeft;
const TR = ROSTER.terraceRight;
const pairKey = (a, b) => [a.deviceID, b.deviceID].map((s) => s.toUpperCase()).sort().join('+');

// remembered seeds the memory before the app boots, the way an earlier
// session would have left it.
const remembered = (mem) => `try { localStorage.setItem(${JSON.stringify(MEMORY_KEY)}, ${JSON.stringify(JSON.stringify(mem))}); } catch (e) {}`;
const readMemory = (page) => page.evaluate((k) => JSON.parse(localStorage.getItem(k) || 'null'), MEMORY_KEY);

const NO_PAIR = mergeScenario(speakersScenario(), { zones: { terraceLeft: null, terraceRight: null } });

async function openClassic(page) {
  await page.locator('.tab-btn[data-view="multiroom"]').click();
  await expect(page.locator('#view-multiroom #stereoName')).toBeAttached();
}

async function openGroups(page) {
  await page.locator('#ui2Sidebar .ui2-nav-item[data-view="speakers"]').click();
  await page.locator('#ui2SpkSubnav .spa-sub[data-route="groups"]').click();
  await expect(page.locator('#view-multiroom > .ui2-grp')).toBeVisible();
}

async function pick(page, left, right) {
  await page.locator('#stereoLeft').selectOption(left.deviceID);
  await page.locator('#stereoRight').selectOption(right.deviceID);
}

for (const layout of ['classic', 'ui2']) {
  const open = layout === 'ui2' ? openGroups : openClassic;

  test.describe(`${layout}: pair name field`, () => {
    test.use({ ui2: layout === 'ui2' });

    test.describe('two speakers that are not paired', () => {
      test.use({ scenario: NO_PAIR });

      test('starts empty when no name was ever typed', async ({ app }) => {
        const { page } = app;
        await open(page);
        await pick(page, TL, TR);
        await expect(page.locator('#stereoName')).toHaveValue('');
      });

      test('a name typed and paired is remembered and offered for the next pair', async ({ app, mock }) => {
        const { page } = app;
        await open(page);
        await pick(page, TL, TR);
        await page.locator('#stereoName').fill('Patio');
        await mock.resetCalls();
        await page.locator('#stereoCreate').click();
        await mock.expectCalled('FormZone', { pred: (c) => c.args[0] === TL.host && c.args[2].name === 'Patio', times: 1 });
        await expect.poll(() => readMemory(page)).toEqual({ last: 'Patio', byPair: { [pairKey(TL, TR)]: 'Patio' } });

        // Another two speakers get the name typed last.
        await pick(page, BR, TR);
        await expect(page.locator('#stereoName')).toHaveValue('Patio');
      });
    });

    test.describe('a name remembered from an earlier session', () => {
      test.use({
        scenario: NO_PAIR,
        preInit: remembered({ last: 'Kitchen', byPair: { [pairKey(TL, TR)]: 'Patio' } }),
      });

      test('the pair\'s own name first, else the last one typed; it is what Create sends', async ({ app, mock }) => {
        const { page } = app;
        await open(page);
        await pick(page, TL, TR);
        await expect(page.locator('#stereoName')).toHaveValue('Patio');
        await pick(page, BR, TR);
        await expect(page.locator('#stereoName')).toHaveValue('Kitchen');
        // A name typed over it is kept through the 5 s repaints.
        await page.locator('#stereoName').fill('Den');
        await mock.resetCalls();
        await page.locator('#stereoCreate').click();
        await mock.expectCalled('FormZone', { pred: (c) => c.args[0] === BR.host && c.args[2].name === 'Den', times: 1 });
      });
    });

    test.describe('a pair that exists', () => {
      test.use({
        scenario: speakersScenario(),
        preInit: remembered({ last: 'Kitchen', byPair: { [pairKey(TL, TR)]: 'Patio' } }),
      });

      test('shows its own name, never the remembered one', async ({ app }) => {
        const { page } = app;
        await open(page);
        await pick(page, TL, TR);
        // Save name is only offered for a pair the speakers report.
        await expect(page.locator('#stereoNameSave')).toBeVisible();
        await expect(page.locator('#stereoName')).toHaveValue(SPEAKERS3.zones.terraceLeft.stereo.name);
      });
    });
  });
}
