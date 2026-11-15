// Finding a speaker in the UI 2.0 sidebar (src/shell2/spksearch.js): with more
// than eight rows a search field shows above the speaker list. It narrows the
// other speakers by name, model and what they play, leaves the pinned group
// as it is, clears on Escape, and says inline when nothing matches. With a
// short list there is no field, and the classic layout has none at all.
//
// The network is the phase 3 fixture (testkit/demo-data.mjs SPEAKERS3): ten
// speakers, nine rows (the stereo pair is one). Bedroom is made to play a
// Spotify playlist so the search has a source and a station to find.
//
//   E2E_PORT=4187 npx playwright test specs/desktop-ui2-sidebar-search.spec.mjs --project=desktop

import { test, expect } from '../fixtures.mjs';
import { ROSTER, statusXml } from '../../testkit/demo-data.mjs';
import { mergeScenario, speakersScenario } from '../../testkit/scenario.mjs';

const R = ROSTER;
const field = (page) => page.locator('#ui2SpkQ');
const rows = (page) => page.locator('#ui2SpkLists .ui2-spk-pick');
const pick = (page, host) => page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${host}"]`);
const others = (page) => page.locator('#ui2SpkLists .ui2-spk-scroll .ui2-spk-pick');
const pinned = (page) => page.locator('#ui2SpkLists .ui2-spk-pinned .ui2-spk-pick');
const hostsOf = (loc) => loc.evaluateAll((els) => els.map((e) => e.dataset.host));

const SPOTIFY_BEDROOM = statusXml({ deviceID: R.bedroom.deviceID, source: 'SPOTIFY', itemName: 'Morning Mix', art: '' });

// The every-speaker poll has read Bedroom once its row says it plays.
async function ready(page) {
  await expect(rows(page)).toHaveCount(9);
  await expect(pick(page, R.bedroom.host)).toHaveAttribute('aria-label', /Playing/);
  await expect(field(page)).toBeVisible();
}

test.describe('UI 2.0 sidebar search', () => {
  test.use({
    ui2: true,
    scenario: mergeScenario(speakersScenario(), {
      bindings: { Status: { byHost: { [R.bedroom.host]: { value: SPOTIFY_BEDROOM } } } },
    }),
  });

  test('1. a named field above the list once there are more than eight rows', async ({ app }) => {
    const { page } = app;
    await ready(page);
    await expect(field(page)).toHaveAccessibleName('Find a speaker by name, model or what it plays');
    await expect(field(page)).toHaveAttribute('placeholder', 'Find a speaker');
    // Above the lists, inside the sidebar's speaker section.
    const order = await page.locator('#ui2Speakers').evaluate((s) => [...s.querySelectorAll('#ui2SpkQ, #ui2SpkLists')].map((e) => e.id));
    expect(order).toEqual(['ui2SpkQ', 'ui2SpkLists']);
    // Tab reaches it, and Down goes on to the first row.
    await field(page).focus();
    await page.keyboard.press('ArrowDown');
    await expect(pick(page, R.livingRoom.host)).toBeFocused();
    await page.keyboard.press('ArrowUp');
    await expect(field(page)).toBeFocused();
  });

  test('2. narrows the others by name and by model; the playing speaker stays pinned', async ({ app }) => {
    const { page } = app;
    await ready(page);
    expect(await hostsOf(pinned(page))).toEqual([R.livingRoom.host]);
    await field(page).fill('hall');
    await expect(others(page)).toHaveCount(2);
    expect((await hostsOf(others(page))).sort()).toEqual([R.oldAgent.host, R.hall.host].sort());
    // The pinned speaker does not match and is still there, with its heading.
    expect(await hostsOf(pinned(page))).toEqual([R.livingRoom.host]);
    await expect(page.locator('#ui2SpkNowHead')).toBeVisible();
    // By model: the only Portable.
    await field(page).fill('portable');
    await expect(others(page)).toHaveCount(1);
    await expect(others(page).first()).toHaveAttribute('data-host', R.nursery.host);
    // Case and accents do not matter, and every word must match.
    await field(page).fill('SOUNDTOUCH 30');
    await expect(others(page)).toHaveCount(1);
    await expect(others(page).first()).toHaveAttribute('data-host', R.office.host);
  });

  test('3. finds a speaker by its source and its station', async ({ app }) => {
    const { page } = app;
    await ready(page);
    await field(page).fill('spotify');
    await expect(others(page)).toHaveCount(1);
    await expect(others(page).first()).toHaveAttribute('data-host', R.bedroom.host);
    await field(page).fill('morning mix');
    await expect(others(page)).toHaveCount(1);
    await expect(others(page).first()).toHaveAttribute('data-host', R.bedroom.host);
    // The station the controlled speaker plays matches it, in the pinned group.
    await field(page).fill('rock');
    await expect(others(page)).toHaveCount(0);
    await expect(page.locator('#ui2SpkLists .ui2-spk-nomatch')).toHaveCount(0);
    await expect(pinned(page)).toHaveCount(1);
  });

  test('4. Escape clears, in the field and on a row', async ({ app }) => {
    const { page } = app;
    await ready(page);
    await field(page).fill('portable');
    await expect(rows(page)).toHaveCount(2);
    await page.keyboard.press('Escape');
    await expect(field(page)).toHaveValue('');
    await expect(field(page)).toBeFocused();
    await expect(rows(page)).toHaveCount(9);
    // On a row: the search goes and the keyboard stays on that row.
    await field(page).fill('portable');
    await expect(rows(page)).toHaveCount(2);
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('ArrowDown');
    await expect(pick(page, R.nursery.host)).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(field(page)).toHaveValue('');
    await expect(rows(page)).toHaveCount(9);
    await expect(pick(page, R.nursery.host)).toBeFocused();
  });

  test('5. nothing matches: said inline, with a way back', async ({ app }) => {
    const { page } = app;
    await ready(page);
    await field(page).fill('garage');
    const none = page.locator('#ui2SpkLists .ui2-spk-nomatch');
    await expect(none).toBeVisible();
    await expect(none.locator('.ui2-spk-empty')).toHaveText('No speaker matches "garage".');
    await expect(others(page)).toHaveCount(0);
    // Not the "no speakers found" state, which is about the network.
    await expect(page.locator('#ui2SpkState')).toHaveCount(0);
    // The pinned speaker stays on screen.
    await expect(pick(page, R.livingRoom.host)).toBeVisible();
    // The live region says it once typing pauses.
    await expect(page.locator('#ui2SpkLive')).toHaveText('No speaker matches "garage".');
    const clear = none.getByRole('button', { name: 'Clear the search' });
    await clear.focus();
    await page.keyboard.press('Enter');
    await expect(field(page)).toHaveValue('');
    await expect(field(page)).toBeFocused();
    await expect(rows(page)).toHaveCount(9);
    await expect(none).toHaveCount(0);
  });

  test('6. a search counts what it shows for screen readers', async ({ app }) => {
    const { page } = app;
    await ready(page);
    await field(page).fill('soundtouch 10');
    await expect(page.locator('#ui2SpkLive')).toHaveText(/^Speakers shown: \d of 9$/);
  });
});

test.describe('UI 2.0 sidebar search, short list', () => {
  test.use({ ui2: true });

  test('7. eight rows or fewer: no field', async ({ app }) => {
    const { page } = app;
    await expect(rows(page).first()).toBeVisible();
    expect(await rows(page).count()).toBeLessThanOrEqual(8);
    await expect(field(page)).toBeHidden();
  });
});

test.describe('classic layout', () => {
  test.use({ ui2: false, scenario: speakersScenario() });

  test('8. has no sidebar search', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('#ui2SpkQ')).toHaveCount(0);
    await expect(page.locator('#ui2SpkFind')).toHaveCount(0);
  });
});
