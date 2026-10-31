// UI 2.0 Help and info (phase 5, docs/UI-2.0.md "Help and info";
// src/shell2/help/help.js): the one place for help, wishes, community,
// support and about. Reached from the small sidebar entry under Wishes; the
// classic footer is hidden on every preview view, so the donate buttons show
// here and nowhere else. The classic layout keeps its footer, its donate rail
// and its Wishes tab exactly as they were (last describe).
//
//   E2E_PORT=4184 npx playwright test specs/desktop-ui2-help.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const SECTIONS = ['getHelp', 'wishes', 'community', 'support', 'about'];
const REPO = 'https://github.com/JRpersonal/streborn';
const VOTE = `${REPO}/discussions/categories/ideas?discussions_q=category%3AIdeas+sort%3Atop`;

const help = (page) => page.locator('#view-help');
const section = (page, id) => page.locator(`#view-help section[data-section="${id}"]`);

async function openHelp(page) {
  await page.locator('#ui2NavHelp').click();
  await expect(page.locator('#ui2MainInner > #view-help')).toBeVisible();
  await expect(page.locator('#hlpTitle')).toHaveText(en('ui2.help.title'));
}

async function opened(mock) {
  return (await mock.calls('runtime.BrowserOpenURL')).map((c) => c.args[0]);
}

test.describe('UI 2.0 Help and info', () => {
  test.use({ ui2: true });

  test('1. the Help entry opens the page, marked current, with every section labelled', async ({ app }) => {
    const { page } = app;
    await openHelp(page);
    await expect(page.locator('#ui2NavHelp')).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('#ui2Sidebar .ui2-nav-item[aria-current]')).toHaveCount(1);
    const shown = await page.locator('#ui2MainInner > [id^="view-"]').evaluateAll((els) => els
      .filter((e) => !e.classList.contains('hidden')).map((e) => e.id));
    expect(shown).toEqual(['view-help']);
    const ids = await help(page).locator('section.hlp-sec').evaluateAll((els) => els.map((e) => e.dataset.section));
    expect(ids).toEqual(SECTIONS);
    for (const id of SECTIONS) {
      const sec = section(page, id);
      await expect(sec).toHaveAttribute('aria-labelledby', `hlpHead-${id}`);
      await expect(page.locator(`#hlpHead-${id}`)).toBeVisible();
    }
    await expect(page.getByRole('region', { name: en('ui2.help.about') })).toBeVisible();
  });

  test('2. Tab walks through the sections in order', async ({ app }) => {
    const { page } = app;
    await openHelp(page);
    await help(page).locator('button').first().focus();
    const seen = [];
    for (let i = 0; i < 40; i++) {
      const sec = await page.evaluate(() => {
        const el = document.activeElement;
        const s = el && el.closest && el.closest('#view-help section[data-section]');
        return s ? s.dataset.section : null;
      });
      if (!sec) break;
      if (seen[seen.length - 1] !== sec) seen.push(sec);
      await page.keyboard.press('Tab');
    }
    expect(seen).toEqual(SECTIONS);
  });

  test('3. Save diagnostic logs says what it does in its section, for every speaker', async ({ app, mock }) => {
    const { page } = app;
    await openHelp(page);
    const status = page.locator('#hlpLogsStatus');
    await expect(status).toBeHidden();
    await mock.set('SaveDiagnosticBundle', { value: { savePath: 'C:\\Users\\me\\str-diagnostic.zip', bytes: 20480 }, delayMs: 1500, times: 1 });
    const btn = page.locator('#hlpSaveLogs');
    await btn.click();
    await expect(status).toHaveText(en('ui2.help.saving'));
    await expect(btn).toHaveAttribute('aria-disabled', 'true');
    await expect(btn).toBeFocused();
    await expect(status).toHaveText(en('footer.saveLogsDone', { path: 'C:\\Users\\me\\str-diagnostic.zip', size: 20 }));
    await expect(status).toHaveAttribute('data-kind', 'done');
    await expect(btn).not.toHaveAttribute('aria-disabled', /.*/);
    // In the section, not as a window-wide toast.
    await expect(page.locator('#toast')).not.toHaveClass(/\bshow\b/);
    const call = await mock.last('SaveDiagnosticBundle');
    expect(call.args[1]).toBe(true);
    expect(call.args[0]).toEqual(expect.arrayContaining([LR.host, BR.host]));

    // A cancelled save dialog: nothing to say.
    await mock.set('SaveDiagnosticBundle', { value: { savePath: '' }, times: 1 });
    await btn.click();
    await expect(status).toHaveText('');
    await expect(status).toBeHidden();

    // A failure: the reason inline, no error dialog.
    await mock.set('SaveDiagnosticBundle', { error: 'disk full', times: 1 });
    await btn.click();
    await expect(status).toHaveText(en('ui2.help.saveFailed', { error: 'disk full' }));
    await expect(status).toHaveAttribute('data-kind', 'error');
    await expect(page.locator('#errorModal')).toBeHidden();
  });

  test('4. the donate buttons show on Help and info and nowhere else', async ({ app }) => {
    const { page } = app;
    for (const view of ['box', 'library', 'recent', 'spotify', 'speakers', 'wishes']) {
      await page.locator(`#ui2Sidebar .ui2-nav-item[data-view="${view}"]`).click();
      await expect(page.locator(`#ui2MainInner > #view-${view}`)).toBeVisible();
      await expect(page.locator('[data-donate]:visible'), view).toHaveCount(0);
      await expect(page.locator('#appFooter'), view).toBeHidden();
      await expect(page.locator('.donate-side'), view).toBeHidden();
    }
    await openHelp(page);
    await expect(page.locator('[data-donate]:visible')).toHaveCount(3);
    await expect(section(page, 'support').locator('[data-donate]:visible')).toHaveCount(3);
    await expect(section(page, 'support')).toContainText(en('ui2.help.supportLead'));
    await expect(page.locator('#appFooter')).toBeHidden();
  });

  test('5. the trademark notice is on screen in About', async ({ app }) => {
    const { page } = app;
    await openHelp(page);
    const tm = page.locator('#hlpTrademark');
    await tm.scrollIntoViewIfNeeded();
    await expect(tm).toBeVisible();
    await expect(tm).toHaveText(en('about.trademark'));
    await expect(tm).toContainText('Bose Corporation');
  });

  test('6. choosing a speaker on Help and info opens Radio with it', async ({ app }) => {
    const { page } = app;
    await openHelp(page);
    await page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${BR.host}"]`).click();
    await expect(page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${BR.host}"]`)).toHaveAttribute('aria-current', 'true');
    await expect(page.locator('#ui2MainInner > #view-box')).toBeVisible();
    await expect(help(page)).toBeHidden();
  });

  test('7. its links open the right pages, and its buttons the right places', async ({ app, mock }) => {
    const { page } = app;
    await openHelp(page);
    await mock.resetCalls();
    const getHelp = section(page, 'getHelp');
    await getHelp.getByRole('button', { name: en('footer.reportProblem') }).click();
    await getHelp.getByRole('button', { name: en('ui2.help.ask') }).click();
    await getHelp.getByRole('button', { name: en('ui2.help.faq') }).click();
    await section(page, 'wishes').getByRole('button', { name: en('footer.voteIdeas') }).click();
    await section(page, 'about').getByRole('button', { name: en('ui2.help.privacy') }).click();
    await section(page, 'about').getByRole('button', { name: en('ui2.help.imprint') }).click();
    await section(page, 'about').getByRole('button', { name: en('banner.whatsNew') }).click();
    await section(page, 'community').getByRole('button', { name: en('footer.worldMap') }).click();
    await expect.poll(() => opened(mock)).toHaveLength(8);
    const urls = await opened(mock);
    expect(urls[0]).toBe(`${REPO}/issues`);
    expect(urls[1]).toBe(`${REPO}/discussions`);
    expect(urls[2]).toMatch(/^https:\/\/st-reborn\.de\/\?utm_source=st-reborn-app.*#faq$/);
    expect(urls[3]).toBe(VOTE);
    expect(urls[4]).toMatch(/^https:\/\/st-reborn\.de\/privacy\?utm_source=st-reborn-app/);
    expect(urls[5]).toMatch(/^https:\/\/st-reborn\.de\/imprint\?utm_source=st-reborn-app/);
    expect(urls[6]).toBe(`${REPO}/releases/tag/v1.0.19`);
    expect(urls[7]).toBe('https://st-reborn.de/?share&src=app#community');

    // The credits dialog, the share dialog.
    await section(page, 'about').getByRole('button', { name: en('footer.credits') }).click();
    await expect(page.locator('#creditsModal')).toBeVisible();
    await expect(page.locator('#creditsTrademark')).toHaveText(en('about.trademark'));
    await page.locator('#creditsClose').click();
    await section(page, 'community').getByRole('button', { name: en('share.menu') }).click();
    await expect(page.locator('#shareModal')).toBeVisible();
    await page.locator('#shareClose').click();
    await expect(page.locator('#shareModal')).toBeHidden();

    // Open wishes and the pointer to Speakers switch views.
    await section(page, 'wishes').getByRole('button', { name: en('ui2.help.openWishes') }).click();
    await expect(page.locator('#ui2MainInner > #view-wishes')).toBeVisible();
    await openHelp(page);
    await section(page, 'getHelp').locator('.hlp-pointer .hlp-link').click();
    await expect(page.locator('#ui2MainInner > #view-speakers')).toBeVisible();
  });

  test('8. Check for app updates runs the app\'s check; the answer shows in the version line', async ({ app, mock }) => {
    const { page } = app;
    await openHelp(page);
    await mock.resetCalls();
    const btn = page.locator('#hlpCheckUpdate');
    await btn.click();
    await mock.expectCalled('CheckAppUpdate');
    await expect(page.locator('#ui2Version #appUpdateBanner')).toContainText(en('banner.appIsCurrent'));
    await expect(btn).toHaveText(en('banner.checkForAppUpdate'));
    await expect(btn).not.toHaveAttribute('aria-disabled', /.*/);
    // And next to the button, in a live region, where it was asked.
    const line = page.locator('#hlpUpdateStatus');
    await expect(line).toHaveAttribute('role', 'status');
    await expect(line).toHaveText(en('banner.appIsCurrent'));
    await expect(line).toHaveAttribute('data-kind', 'done');
    // A check that cannot run says so there too.
    await mock.set('CheckAppUpdate', { error: 'dial tcp: i/o timeout' });
    await btn.click();
    await expect(line).toHaveText(en('banner.appCheckFailed'));
    await expect(line).toHaveAttribute('data-kind', 'error');
  });

  test.describe('in German', () => {
    test.use({ appLocale: 'de' });

    test('9. the legal pages open in German', async ({ app, mock }) => {
      const { page } = app;
      await page.locator('#ui2NavHelp').click();
      await expect(help(page)).toBeVisible();
      await mock.resetCalls();
      await section(page, 'about').locator('[data-url*="datenschutz"]').click();
      await section(page, 'about').locator('[data-url*="impressum"]').click();
      await expect.poll(() => opened(mock)).toHaveLength(2);
      const urls = await opened(mock);
      expect(urls[0]).toMatch(/^https:\/\/st-reborn\.de\/de\/datenschutz\?/);
      expect(urls[1]).toMatch(/^https:\/\/st-reborn\.de\/de\/impressum\?/);
    });
  });
});

test.describe('classic layout keeps its footer, donate rail and Wishes tab', () => {
  test.use({ ui2: false, viewport: { width: 1700, height: 900 } });

  test('10. footer with the same links, the donate rail at a wide window, the Wishes tab, no Help page', async ({ app, mock }) => {
    const { page } = app;
    const footer = page.locator('#appFooter');
    await expect(footer).toBeVisible();
    for (const id of ['footerDonate', 'footerWorldMap', 'footerSaveLogs', 'footerCredits', 'footerReport', 'footerVoteIdeas', 'footerShare', 'appVersionLink']) {
      await expect(footer.locator(`#${id}`), id).toBeVisible();
    }
    await expect(footer).toContainText(en('footer.fine'));
    await expect(page.locator('.donate-side')).toBeVisible();
    await expect(page.locator('.donate-side [data-donate]')).toHaveCount(3);
    await expect(page.locator('.tab-btn[data-view="wishes"]')).toBeVisible();
    await expect(page.locator('#view-help')).toHaveCount(0);
    await expect(page.locator('#ui2NavHelp')).toHaveCount(0);

    // The links open what they always opened.
    await mock.resetCalls();
    await footer.locator('#footerVoteIdeas').click();
    await footer.locator('#appVersionLink').click();
    await footer.locator('#footerReport').click();
    await footer.locator('#footerWorldMap').click();
    await expect.poll(() => opened(mock)).toHaveLength(4);
    expect(await opened(mock)).toEqual([VOTE, `${REPO}/releases/tag/v1.0.19`, `${REPO}/issues`, 'https://st-reborn.de/?share&src=app#community']);
  });
});
