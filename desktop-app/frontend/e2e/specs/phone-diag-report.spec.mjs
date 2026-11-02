// phone-diag-report: "Report a problem" on the phone remote's More screen
// (internal/webui/assets/index.html) says where the diagnostic file goes
// (discussion #1339): a bug report on GitHub, with the link right below the
// button, in the current layout as well as in UI 2.0.
//
//   SHOOT_DIR=<dir> also saves a screenshot of the card after saving.

import { test, expect } from '../fixtures.mjs';

async function bootDone(page) {
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
}

test.describe('Report a problem, current layout', () => {
  test.use({ phoneOverrides: { '/api/debug/state': { body: { agent: { version: '1.3.2' } } } } });

  test('names GitHub and offers the link below the button', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await expect(page.locator('html')).not.toHaveClass(/(^|\s)ui2(\s|$)/);
    const T = await page.evaluate(() => ({ sum: window.T.diagSum, ok: window.T.diagOk, issues: window.T.issues }));
    await page.locator('#tabMore').click();
    await expect(page.locator('#lblDiagSum')).toHaveText(T.sum);
    expect(T.sum).toContain('GitHub');
    const link = page.locator('#u2Issues');
    await expect(link).toBeVisible();
    await expect(link).toHaveText(T.issues);
    await expect(link).toHaveAttribute('href', 'https://github.com/JRpersonal/streborn/issues');
    await page.locator('#btnDiag').click();
    await expect(page.locator('#diagMsg')).toHaveText(T.ok);
    expect(T.ok).toContain(T.issues);
    if (process.env.SHOOT_DIR) {
      const card = page.locator('#btnDiag').locator('xpath=..');
      await card.scrollIntoViewIfNeeded();
      await card.screenshot({ path: `${process.env.SHOOT_DIR}/phone-report-a-problem.png` });
    }
  });
});

test.describe('Report a problem, current layout, German', () => {
  test.use({ phoneLang: 'de' });

  test('the link carries its German label', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await page.locator('#tabMore').click();
    await expect(page.locator('#u2Issues')).toHaveText('Auf GitHub melden');
    await expect(page.locator('#lblDiagSum')).toContainText('GitHub');
  });
});
