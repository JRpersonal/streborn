// The search across every media server in the UI 2.0 preview, its failure and
// its empty answer (#1286 phase 5, docs/UI-2.0.md): a search that did not run
// is told apart from servers that stayed silent, with Try again for the same
// words and field; no hits says what to try next and leads back to the field.
// The last block checks that the current layout did not change.
//
//   E2E_PORT=4182 npx playwright test specs/desktop-ui2-library-search.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { en } from '../helpers/i18n.mjs';
import { MEDIA_SERVERS } from '../../testkit/demo-data.mjs';

const NAS = MEDIA_SERVERS[0].friendlyName;
const lib = (page) => page.locator('#ui2MainInner > #view-library');
const heading = (page) => lib(page).locator('#lib2LevelTitle');

async function openLibrary(page) {
  await page.locator('#ui2Sidebar .ui2-nav-item[data-view="library"]').click();
  await expect(heading(page)).toHaveText(NAS);
}

async function search(page, q, field) {
  await lib(page).locator('#libWholeField').selectOption(field);
  const input = lib(page).locator('#libWholeQ');
  await input.fill(q);
  await input.press('Enter');
}

test.describe('UI 2.0 Library search', () => {
  test.use({ ui2: true });

  test.describe('a search that does not run', () => {
    test.use({ scenario: { bindings: { SearchLibrary: { error: 'library search: context deadline exceeded' } } } });

    test('1. says so, apart from silent servers, and Try again repeats the same words and field', async ({ app, mock }) => {
      const { page } = app;
      await openLibrary(page);
      await search(page, 'blue', 'album');
      const err = lib(page).locator('.lib2-state');
      await expect(err).toHaveClass(/lib2-state--error/);
      await expect(err).toHaveAttribute('role', 'alert');
      await expect(err.locator('.lib2-state-title')).toHaveText(en('ui2.feedback.searchFailedTitle'));
      await expect(err.locator('.lib2-state-body')).toHaveText(en('ui2.feedback.searchFailedBody'));
      await expect(err.locator('.lib2-state-detail')).toContainText('deadline exceeded');
      // Not the note for servers that did not answer.
      await expect(lib(page)).not.toContainText(en('library.wholeSearchOffline', { name: NAS }));

      await mock.set('SearchLibrary', {});
      await mock.resetCalls();
      await lib(page).locator('#lib2SearchRetry').click();
      await mock.expectCalled('SearchLibrary', { pred: (c) => c.args[0] === 'blue' && c.args[1] === 'album', times: 1 });
      await expect(lib(page).locator('.lib2-track')).toHaveCount(2);
      await expect(lib(page).locator('.lib2-state')).toHaveCount(0);
    });
  });

  test.describe('no hits', () => {
    test.use({ scenario: { bindings: { SearchLibrary: { value: { items: [], unanswered: [], partial: false } } } } });

    test('2. suggests another field and leads back to the search field', async ({ app }) => {
      const { page } = app;
      await openLibrary(page);
      await search(page, 'zzz', 'artist');
      const empty = lib(page).locator('.lib2-state');
      await expect(empty).toHaveAttribute('role', 'status');
      await expect(empty.locator('.lib2-state-title')).toHaveText(en('library.wholeSearchNone'));
      await expect(empty.locator('.lib2-state-body')).toHaveText(en('ui2.feedback.tryAnotherField', { field: en('library.fieldAll') }));
      const again = lib(page).locator('#lib2SearchFocus');
      await expect(again).toHaveText(en('ui2.library.searchAgain'));
      await again.click();
      await expect(lib(page).locator('#libWholeQ')).toBeFocused();
      await expect(lib(page).locator('#libWholeQ')).toHaveValue('zzz');
    });

    test('3. searched in every field already: no field hint', async ({ app }) => {
      const { page } = app;
      await openLibrary(page);
      await search(page, 'zzz', 'any');
      const empty = lib(page).locator('.lib2-state');
      await expect(empty.locator('.lib2-state-title')).toHaveText(en('library.wholeSearchNone'));
      await expect(empty.locator('.lib2-state-body')).toHaveCount(0);
      await expect(lib(page).locator('#lib2SearchFocus')).toBeVisible();
    });
  });
});

test.describe('current layout', () => {
  test.use({ scenario: { bindings: { SearchLibrary: { error: 'library search: context deadline exceeded' } } } });

  test('4. a failed search keeps its note for the servers', async ({ app }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="library"]').first().click();
    const q = page.locator('#view-library #libWholeQ');
    await expect(q).toBeVisible();
    await q.fill('blue');
    await q.press('Enter');
    // The note names the server with its model, "<name> (<model>) did not answer, ...".
    await expect(page.locator('#view-library')).toContainText(NAS);
    await expect(page.locator('#view-library')).toContainText(en('library.wholeSearchOffline', { name: '' }).trim());
    await expect(page.locator('#view-library .lib2-state')).toHaveCount(0);
    await expect(page.locator('#lib2SearchRetry')).toHaveCount(0);
  });
});
