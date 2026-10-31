// UI 2.0 phase 5: the banner region in the shell's tokens (shell2/banners.css)
// and the notices about ONE speaker moved to that speaker (shell2/attention.js,
// main.js). The speaker update card reads --u-surface and --u-text in every
// theme and names the one speaker that is behind; the wedge and box issue
// banners are hidden in the preview and say the same, with the same buttons,
// on the speaker's card in the list and at the top of its Overview; an update
// that failed hands over its report on the speaker's own page.
//
// The classic look of all of it is pinned in desktop-banners.spec.mjs.
//
//   E2E_PORT=4183 npx playwright test specs/desktop-ui2-banners.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { settle } from '../helpers/clock.mjs';
import { KILL_ANIM } from '../helpers/a11y.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';
import { onHost } from '../helpers/mockclient.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const OLD = ROSTER.oldAgent;

const nav = (page, view) => page.locator(`#ui2Sidebar .ui2-nav-item[data-view="${view}"]`);
const listCard = (page, host) => page.locator(`#view-speakers li.spl-item[data-host="${host}"]`);

// tokenColour resolves a --u-* token to the rgb() string the browser paints.
const tokenColour = (page, token) => page.evaluate((tk) => {
  const probe = document.createElement('div');
  probe.style.color = `var(${tk})`;
  document.getElementById('ui2Banners').appendChild(probe);
  const c = getComputedStyle(probe).color;
  probe.remove();
  return c;
}, token);

for (const theme of ['dark', 'light', 'contrast']) {
  test.describe(`speaker update card, ${theme}`, () => {
    test.use({ ui2: true, theme, fakeClock: true, scenario: { banners: { speakerUpdate: true } } });

    test('reads the surface and text tokens, names the one speaker behind, and is a named region', async ({ app }) => {
      const { page } = app;
      await settle(page, 9_000);
      await page.addStyleTag({ content: KILL_ANIM });
      const cardEl = page.locator('#ui2Banners #speakerUpdateCard');
      await expect(cardEl).toBeVisible();
      const got = await cardEl.evaluate((el) => ({
        bg: getComputedStyle(el).backgroundColor,
        title: getComputedStyle(el.querySelector('.suc-title')).color,
        bar: getComputedStyle(el).borderInlineStartColor,
        barWidth: getComputedStyle(el).borderInlineStartWidth,
      }));
      expect(got.bg).toBe(await tokenColour(page, '--u-surface'));
      expect(got.title).toBe(await tokenColour(page, '--u-text'));
      expect(got.bar).toBe(await tokenColour(page, '--u-accent'));
      expect(got.barWidth).toBe('4px');
      // One speaker behind: the title names it (rule 1).
      await expect(cardEl.locator('.suc-title')).toHaveText(`${OLD.friendlyName} needs an update`);
      await expect(cardEl).toHaveAttribute('role', 'region');
      await expect(cardEl).toHaveAttribute('aria-label', `${OLD.friendlyName} needs an update`);
      // The buttons look like the shell's: the primary in the action colour.
      const btn = await cardEl.locator('#sucUpdate').evaluate((el) => getComputedStyle(el).backgroundColor);
      expect(btn).toBe(await tokenColour(page, '--u-action'));
    });
  });
}

test.describe('notices about one speaker', () => {
  test.use({
    ui2: true,
    fakeClock: true,
    scenario: {
      boxPatches: {
        bedroom: { boxHealth: 'wedged' },
        livingRoom: { conflictingMod: 'AfterTouch' },
      },
    },
  });

  test('are hidden in the banner region and stand on the speaker\'s card and Overview instead', async ({ app }) => {
    const { page } = app;
    await settle(page, 9_000);
    // The classic banners still render (main.js is unchanged), out of sight.
    await expect(page.locator('#boxWedgeBanner')).not.toHaveClass(/hidden/);
    await expect(page.locator('#boxIssueBanner')).not.toHaveClass(/hidden/);
    await expect(page.locator('#boxWedgeBanner')).toBeHidden();
    await expect(page.locator('#boxIssueBanner')).toBeHidden();

    await nav(page, 'speakers').click();
    const bed = listCard(page, BR.host).locator('.spl-attn');
    await expect(bed).toBeVisible();
    await expect(bed).toContainText('Speaker not responding:');
    await expect(bed).toContainText(BR.friendlyName);
    const lr = listCard(page, LR.host).locator('.spl-attn');
    await expect(lr).toContainText(`${LR.friendlyName}: leftovers of another SoundTouch tool (AfterTouch)`);
    await expect(lr.getByRole('button', { name: 'Remove leftovers' })).toBeVisible();
    await expect(lr.getByRole('button', { name: 'Dismiss' })).toBeVisible();

    // The same on the speaker's Overview, headed by its name.
    await page.locator(`#view-speakers .spl-open[data-host="${LR.host}"]`).click();
    await expect(page.locator('#ui2SpkPageName')).toHaveText(LR.friendlyName);
    const attn = page.getByRole('tabpanel', { name: 'Overview' }).locator('.sps-attn');
    await expect(attn).toBeVisible();
    await expect(attn.getByRole('heading', { name: `${LR.friendlyName} needs your attention` })).toBeVisible();
    await expect(attn).toContainText('AfterTouch');
    // Another tab: the card is part of Overview only.
    await page.locator('#ui2SpkTab-sound').click();
    await expect(attn).toBeHidden();
    await page.locator('#ui2SpkTab-overview').click();

    // Dismiss keeps the classic per-speaker key, and the note goes.
    await attn.getByRole('button', { name: 'Dismiss' }).click();
    await expect(page.locator('#ui2SpkPanels .sps-attn')).toHaveCount(0);
    const key = await page.evaluate(() => Object.keys(localStorage).find((k) => k.startsWith('warnDismiss:') && k.endsWith(':conflict')));
    expect(key).toBeTruthy();
    await nav(page, 'speakers').click();
    await expect(listCard(page, LR.host).locator('.spl-attn')).toHaveCount(0);
    await expect(listCard(page, BR.host).locator('.spl-attn')).toBeVisible();
  });
});

test.describe('the classic layout keeps its banners', () => {
  test.use({
    ui2: false,
    fakeClock: true,
    scenario: { boxPatches: { bedroom: { boxHealth: 'wedged' }, livingRoom: { conflictingMod: 'AfterTouch' } } },
  });

  test('wedge and issue banners show on top, with no attention notes anywhere', async ({ app }) => {
    const { page } = app;
    await settle(page, 9_000);
    await expect(page.locator('#boxWedgeBanner')).toBeVisible();
    await expect(page.locator('#boxWedgeBanner')).toContainText(BR.friendlyName);
    await expect(page.locator('#boxIssueBanner')).toBeVisible();
    await expect(page.locator('#boxIssueBanner #boxIssueConflictBtn')).toBeVisible();
    await expect(page.locator('.spl-attn, .sps-attn')).toHaveCount(0);
  });
});

test.describe('an update that failed', () => {
  test.use({
    ui2: true,
    scenario: {
      banners: { speakerUpdate: true },
      routes: { '/api/stick/status': { status: 200, body: { mounted: false } } },
      bindings: {
        RunSpeakerUpdate: { error: 'upload refused' },
        UpdateFailureReport: { value: 'STR update report\nphase: update\nerror: upload refused' },
      },
    },
  });

  test('hands its report over on the speaker\'s page, heading focused, with the classic overlay unused', async ({ app, mock }) => {
    const { page } = app;
    await nav(page, 'speakers').click();
    await page.locator(`#view-speakers .spl-act[data-act="update"][data-host="${OLD.host}"]`).click();
    await mock.expectCalled('RunSpeakerUpdate', { pred: onHost(OLD.host), times: 1 });
    // The classic flow's error dialog comes first; close it.
    await expect(page.locator('#errorModal')).toBeVisible();
    await page.locator('#errorClose').click();
    await expect(page.locator('#errorModal')).toBeHidden();
    await expect(page.locator('#ui2SpkPageName')).toHaveText(OLD.friendlyName);
    const report = page.getByRole('tabpanel', { name: 'Overview' }).locator('#ui2SpkReport');
    await expect(report).toBeVisible();
    const heading = report.getByRole('heading', { name: `The update for ${OLD.friendlyName} did not finish` });
    await expect(heading).toBeVisible();
    await expect(heading).toBeFocused();
    await expect(report.locator('#failReportText')).toHaveValue(/upload refused/);
    // Copy, Save logs, Mail and Close, as in the classic report.
    for (const id of ['failReportCopy', 'failReportSaveLogs', 'failReportMail', 'failReportClose']) {
      await expect(report.locator(`#${id}`), id).toBeVisible();
    }
    await expect(page.locator('#updateFailureReport')).toBeHidden();
    await expect(page.locator('#updateFailureReport')).toBeEmpty();
    await report.locator('#failReportClose').click();
    await expect(report).toBeHidden();
  });
});
